package install

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cogtypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sestypes "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
)

// Fakes of the checks' ports: each has a function per call, nil answering "all is well".

type chkIAM struct {
	IAM
	getRole  func(name string) (*iam.GetRoleOutput, error)
	simulate func(in *iam.SimulatePrincipalPolicyInput) (*iam.SimulatePrincipalPolicyOutput, error)
	policy   func(arn string) (*iam.GetPolicyOutput, error)
}

func (f chkIAM) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	if f.getRole != nil {
		return f.getRole(aws.ToString(in.RoleName))
	}
	return nil, chkNoEntity()
}

func (f chkIAM) SimulatePrincipalPolicy(_ context.Context, in *iam.SimulatePrincipalPolicyInput, _ ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error) {
	if f.simulate != nil {
		return f.simulate(in)
	}
	var rs []iamtypes.EvaluationResult
	for _, a := range in.ActionNames {
		rs = append(rs, iamtypes.EvaluationResult{EvalActionName: aws.String(a), EvalDecision: iamtypes.PolicyEvaluationDecisionTypeAllowed})
	}
	return &iam.SimulatePrincipalPolicyOutput{EvaluationResults: rs}, nil
}

func (f chkIAM) GetPolicy(_ context.Context, in *iam.GetPolicyInput, _ ...func(*iam.Options)) (*iam.GetPolicyOutput, error) {
	if f.policy != nil {
		return f.policy(aws.ToString(in.PolicyArn))
	}
	return nil, chkNoEntity()
}

func chkNoEntity() error { return &iamtypes.NoSuchEntityException{Message: aws.String("not found")} }

func chkAPI(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: code + " message"}
}

type chkEC2 struct {
	EC2
	status string // "" : the region is not listed
	err    error
	in     *ec2.DescribeRegionsInput
}

func (f *chkEC2) DescribeRegions(_ context.Context, in *ec2.DescribeRegionsInput, _ ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error) {
	f.in = in
	if f.err != nil {
		return nil, f.err
	}
	if f.status == "" {
		return &ec2.DescribeRegionsOutput{}, nil
	}
	return &ec2.DescribeRegionsOutput{Regions: []ec2types.Region{{RegionName: aws.String("eu-west-1"), OptInStatus: aws.String(f.status)}}}, nil
}

type chkCB struct {
	CodeBuild
	images []string
	err    error
}

func (f chkCB) ListCuratedEnvironmentImages(context.Context, *codebuild.ListCuratedEnvironmentImagesInput, ...func(*codebuild.Options)) (*codebuild.ListCuratedEnvironmentImagesOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	var imgs []cbtypes.EnvironmentImage
	for _, n := range f.images {
		imgs = append(imgs, cbtypes.EnvironmentImage{Name: aws.String(n)})
	}
	return &codebuild.ListCuratedEnvironmentImagesOutput{Platforms: []cbtypes.EnvironmentPlatform{{Languages: []cbtypes.EnvironmentLanguage{{Images: imgs}}}}}, nil
}

type chkLambda struct {
	Lambda
	found map[string]bool
	err   error
}

func (f chkLambda) GetFunction(_ context.Context, in *lambda.GetFunctionInput, _ ...func(*lambda.Options)) (*lambda.GetFunctionOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.found[aws.ToString(in.FunctionName)] {
		return &lambda.GetFunctionOutput{}, nil
	}
	return nil, chkAPI("ResourceNotFoundException")
}

type chkCognito struct {
	Cognito
	pools []string
	pages int
}

func (f *chkCognito) ListUserPools(_ context.Context, in *cognitoidentityprovider.ListUserPoolsInput, _ ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.ListUserPoolsOutput, error) {
	if aws.ToInt32(in.MaxResults) != 60 {
		return nil, errors.New("MaxResults must be 60")
	}
	f.pages++
	if in.NextToken == nil && len(f.pools) > 0 { // a first page of other pools, the wanted ones on the second
		return &cognitoidentityprovider.ListUserPoolsOutput{UserPools: []cogtypes.UserPoolDescriptionType{{Name: aws.String("other")}}, NextToken: aws.String("p2")}, nil
	}
	var ps []cogtypes.UserPoolDescriptionType
	for _, n := range f.pools {
		ps = append(ps, cogtypes.UserPoolDescriptionType{Name: aws.String(n)})
	}
	return &cognitoidentityprovider.ListUserPoolsOutput{UserPools: ps}, nil
}

type chkACM struct {
	ACM
	status string
	names  []string // the first is DomainName
	err    error
}

func (f chkACM) DescribeCertificate(context.Context, *acm.DescribeCertificateInput, ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	c := &acmtypes.CertificateDetail{Status: acmtypes.CertificateStatus(f.status), DomainName: aws.String(f.names[0]), SubjectAlternativeNames: f.names}
	return &acm.DescribeCertificateOutput{Certificate: c}, nil
}

type chkR53 struct {
	Route53
	name string
	err  error
}

func (f chkR53) GetHostedZone(context.Context, *route53.GetHostedZoneInput, ...func(*route53.Options)) (*route53.GetHostedZoneOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &route53.GetHostedZoneOutput{HostedZone: &r53types.HostedZone{Name: aws.String(f.name)}}, nil
}

type chkSES struct {
	SESv2
	verified   bool
	notFound   bool
	production bool
}

func (f chkSES) GetEmailIdentity(context.Context, *sesv2.GetEmailIdentityInput, ...func(*sesv2.Options)) (*sesv2.GetEmailIdentityOutput, error) {
	if f.notFound {
		return nil, &sestypes.NotFoundException{Message: aws.String("nope")}
	}
	return &sesv2.GetEmailIdentityOutput{VerifiedForSendingStatus: f.verified}, nil
}

func (f chkSES) GetAccount(context.Context, *sesv2.GetAccountInput, ...func(*sesv2.Options)) (*sesv2.GetAccountOutput, error) {
	return &sesv2.GetAccountOutput{ProductionAccessEnabled: f.production}, nil
}

const chkImage = "aws/codebuild/amazonlinux-x86_64-standard:6.0"

// chkWorld is a clean account: every check passes.
type chkWorld struct {
	iam  chkIAM
	ec2  *chkEC2
	cb   chkCB
	lam  chkLambda
	cog  *chkCognito
	acm  chkACM
	r53  chkR53
	ses  chkSES
	w    Where
	s    Settings
	f    Found
	root bool
}

func chkNew() *chkWorld {
	return &chkWorld{
		iam: chkIAM{getRole: func(name string) (*iam.GetRoleOutput, error) {
			if name == "AWSReservedSSO_Admin_a15b943a5b9a9846" {
				return &iam.GetRoleOutput{Role: &iamtypes.Role{Arn: aws.String("arn:aws:iam::532105290256:role/aws-reserved/sso.amazonaws.com/eu-west-1/" + name)}}, nil
			}
			return nil, chkNoEntity()
		}},
		ec2: &chkEC2{status: "opt-in-not-required"},
		cb:  chkCB{images: []string{"aws/codebuild/standard:7.0", chkImage}},
		cog: &chkCognito{},
		acm: chkACM{status: "ISSUED", names: []string{"dormeto.example.com", "*.dormeto.example.com"}},
		r53: chkR53{name: "example.com."},
		ses: chkSES{verified: true, production: true},
		w: Where{
			Account: "532105290256", Region: "eu-west-1",
			Caller:       "arn:aws:sts::532105290256:assumed-role/AWSReservedSSO_Admin_a15b943a5b9a9846/u@example.com",
			Organization: &Organization{ID: "o-vvk0115ixn", Management: "809706483662", AllFeatures: true},
		},
		s: Settings{
			Env: "dev", Region: "eu-west-1", Version: "0.11.0", Mode: "organization", AdminEmails: []string{"a@example.com", "b@example.com"},
			Domain:      &Domain{Dashboard: "dormeto.example.com", CertificateARN: "arn:aws:acm:us-east-1:532105290256:certificate/x", ZoneID: "Z1"},
			SESIdentity: "example.com",
		},
		f: Found{Release: Release{Version: "0.11.0"}},
	}
}

func (x *chkWorld) run() []Check {
	return Checks(context.Background(), Clients{IAM: x.iam, EC2: x.ec2, CodeBuild: x.cb, Lambda: x.lam, Cognito: x.cog, ACM: x.acm, Route53: x.r53, SESv2: x.ses}, x.s, x.w, x.f)
}

func chkRow(t *testing.T, cs []Check, name string) Check {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no row %q in %v", name, cs)
	return Check{}
}

func chkWant(t *testing.T, x *chkWorld, name string, v Verdict, detail string) {
	t.Helper()
	c := chkRow(t, x.run(), name)
	if c.Verdict != v || !strings.Contains(c.Detail, detail) {
		t.Errorf("%s = %d %q, want %d containing %q", name, c.Verdict, c.Detail, v, detail)
	}
}

func TestChecksOrderAndClean(t *testing.T) {
	cs := chkNew().run()
	var names []string
	for _, c := range cs {
		names = append(names, c.Name)
		if c.Verdict != OK {
			t.Errorf("%s = %d %q", c.Name, c.Verdict, c.Detail)
		}
	}
	want := "credentials,administrator rights,region,organization,existing install,another install,another environment,Dormeto roles,release,administrators,domain,SES"
	if strings.Join(names, ",") != want {
		t.Errorf("rows = %v", names)
	}
	if Failed(cs) || len(Warnings(cs)) != 0 {
		t.Error("clean run failed or warned")
	}
}

func TestChecksCredentials(t *testing.T) {
	x := chkNew()
	chkWant(t, x, "credentials", OK, "AWSReservedSSO_Admin_a15b943a5b9a9846 in 532105290256")
	x.w.Caller = "arn:aws:iam::532105290256:user/admins/me"
	chkWant(t, x, "credentials", OK, "me in 532105290256")
	x.w.Caller = "arn:aws:iam::532105290256:root"
	chkWant(t, x, "credentials", OK, "root in 532105290256")
}

func TestChecksRights(t *testing.T) {
	x := chkNew()
	var got *iam.SimulatePrincipalPolicyInput
	x.iam.simulate = func(in *iam.SimulatePrincipalPolicyInput) (*iam.SimulatePrincipalPolicyOutput, error) {
		got = in
		return &iam.SimulatePrincipalPolicyOutput{}, nil
	}
	chkWant(t, x, "administrator rights", OK, "administrator rights (simulated)")
	if aws.ToString(got.PolicySourceArn) != "arn:aws:iam::532105290256:role/aws-reserved/sso.amazonaws.com/eu-west-1/AWSReservedSSO_Admin_a15b943a5b9a9846" {
		t.Errorf("source = %s", aws.ToString(got.PolicySourceArn))
	}
	if len(got.ActionNames) != 13 || got.ActionNames[0] != "s3:CreateBucket" || got.ActionNames[12] != "logs:GetLogEvents" || got.ResourceArns[0] != "*" {
		t.Errorf("input = %+v", got)
	}
}

func TestChecksRightsFollowsPagesAndDenies(t *testing.T) {
	x := chkNew()
	pages := 0
	x.iam.simulate = func(in *iam.SimulatePrincipalPolicyInput) (*iam.SimulatePrincipalPolicyOutput, error) {
		pages++
		deny := func(a string) iamtypes.EvaluationResult {
			return iamtypes.EvaluationResult{EvalActionName: aws.String(a), EvalDecision: iamtypes.PolicyEvaluationDecisionTypeImplicitDeny}
		}
		if in.Marker == nil {
			return &iam.SimulatePrincipalPolicyOutput{IsTruncated: true, Marker: aws.String("m"), EvaluationResults: []iamtypes.EvaluationResult{deny("iam:CreateRole")}}, nil
		}
		return &iam.SimulatePrincipalPolicyOutput{EvaluationResults: []iamtypes.EvaluationResult{deny("s3:CreateBucket")}}, nil
	}
	chkWant(t, x, "administrator rights", Fail, "denied: s3:CreateBucket, iam:CreateRole")
	if pages != 2 {
		t.Errorf("pages = %d", pages)
	}
}

func TestChecksRightsRefusedIsWarning(t *testing.T) {
	x := chkNew()
	x.iam.simulate = func(*iam.SimulatePrincipalPolicyInput) (*iam.SimulatePrincipalPolicyOutput, error) {
		return nil, chkAPI("AccessDenied")
	}
	chkWant(t, x, "administrator rights", Warn, "not checked (iam:SimulatePrincipalPolicy refused)")

	x = chkNew()
	x.iam.getRole = func(string) (*iam.GetRoleOutput, error) { return nil, chkAPI("AccessDenied") }
	chkWant(t, x, "administrator rights", Warn, "not checked (iam:SimulatePrincipalPolicy refused)")

	x = chkNew()
	x.iam.simulate = func(*iam.SimulatePrincipalPolicyInput) (*iam.SimulatePrincipalPolicyOutput, error) {
		return nil, chkAPI("Throttling")
	}
	chkWant(t, x, "administrator rights", Fail, "Throttling message")
}

func TestChecksRightsUserAndRoot(t *testing.T) {
	x := chkNew()
	x.w.Caller = "arn:aws:iam::532105290256:user/me"
	var src string
	x.iam.simulate = func(in *iam.SimulatePrincipalPolicyInput) (*iam.SimulatePrincipalPolicyOutput, error) {
		src = aws.ToString(in.PolicySourceArn)
		return &iam.SimulatePrincipalPolicyOutput{}, nil
	}
	chkWant(t, x, "administrator rights", OK, "simulated")
	if src != x.w.Caller {
		t.Errorf("source = %s", src)
	}
	x.w.Caller = "arn:aws:iam::532105290256:root"
	src = ""
	chkWant(t, x, "administrator rights", OK, "root")
	if src != "" {
		t.Error("root was simulated")
	}
}

func TestChecksRegion(t *testing.T) {
	x := chkNew()
	chkWant(t, x, "region", OK, "eu-west-1 enabled; CodeBuild available")
	if x.ec2.in == nil || !aws.ToBool(x.ec2.in.AllRegions) || aws.ToString(x.ec2.in.Filters[0].Name) != "region-name" || x.ec2.in.Filters[0].Values[0] != "eu-west-1" {
		t.Errorf("input = %+v", x.ec2.in)
	}
	x.ec2.status = "opted-in"
	chkWant(t, x, "region", OK, "enabled")
	x.ec2.status = "not-opted-in"
	chkWant(t, x, "region", Fail, "eu-west-1 is not enabled in this account")
	x.ec2.status = ""
	chkWant(t, x, "region", Fail, "eu-west-1 is not enabled in this account")
	x = chkNew()
	x.cb.images = []string{"aws/codebuild/standard:7.0"}
	chkWant(t, x, "region", Fail, "CodeBuild has no image "+chkImage+" in eu-west-1")
	x.cb.err = chkAPI("InvalidInput")
	chkWant(t, x, "region", Fail, "CodeBuild has no image")
}

func TestChecksOrganization(t *testing.T) {
	x := chkNew()
	chkWant(t, x, "organization", OK, "o-vvk0115ixn: all features")
	x.w.Organization.AllFeatures = false
	chkWant(t, x, "organization", Fail, "--mode account")
	x.w.Organization = nil
	chkWant(t, x, "organization", Fail, "--mode account")
	x.w.OrganizationUnknown = true
	chkWant(t, x, "organization", Fail, "--mode account")
	x.s.Mode = "account"
	chkWant(t, x, "organization", OK, "account deployment")
	x.w.OrganizationUnknown = false
	x.w.Organization = &Organization{ID: "o-abc"}
	chkWant(t, x, "organization", OK, "o-abc")
}

func TestChecksExistingInstall(t *testing.T) {
	x := chkNew()
	chkWant(t, x, "existing install", OK, "none: a first install of env dev")
	x.f.Saved = &Settings{Installed: "0.11.0"}
	chkWant(t, x, "existing install", OK, "reinstall of 0.11.0")
	x.f.Saved = &Settings{Installed: "0.10.0"}
	x.s.Version = "0.11.1"
	chkWant(t, x, "existing install", OK, "upgrade: 0.10.0 → 0.11.1")
	x.f.Saved = &Settings{Installed: "0.12.0"}
	chkWant(t, x, "existing install", Fail, "0.12.0 is installed, newer than 0.11.1")
}

func TestChecksAnotherInstall(t *testing.T) {
	x := chkNew()
	chkWant(t, x, "another install", OK, "none")
	x.lam = chkLambda{found: map[string]bool{"dmt-dev-manager-api": true}}
	chkWant(t, x, "another install", Fail, "dmt-dev-manager resources exist without dormeto install's record: installed by other means (keep it in its Terraform)")
	x.f.Saved = &Settings{Installed: "0.10.0"}
	chkWant(t, x, "another install", OK, "none")

	x = chkNew()
	x.cog = &chkCognito{pools: []string{"dmt-dev-manager"}}
	chkWant(t, x, "another install", Fail, "dmt-dev-manager resources exist")
	if x.cog.pages != 2 {
		t.Errorf("pages = %d", x.cog.pages)
	}
	x = chkNew()
	x.lam = chkLambda{err: chkAPI("ServiceException")}
	chkWant(t, x, "another install", Fail, "ServiceException message")
}

func TestChecksAnotherEnvironment(t *testing.T) {
	x := chkNew()
	chkWant(t, x, "another environment", OK, "")
	x.f.OtherEnvs = []string{"prd", "stg"}
	chkWant(t, x, "another environment", Fail, "one environment per account: prd, stg installed here")
}

func chkTrust(principals ...string) *iam.GetRoleOutput {
	var q []string
	for _, p := range principals {
		q = append(q, `"`+p+`"`)
	}
	doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":[` + strings.Join(q, ",") + `]}}]}`
	return &iam.GetRoleOutput{Role: &iamtypes.Role{AssumeRolePolicyDocument: aws.String(url.QueryEscape(doc))}}
}

func TestChecksDormetoRoles(t *testing.T) {
	x := chkNew()
	chkWant(t, x, "Dormeto roles", OK, "none")
	const mgr = "arn:aws:iam::121014831971:role/dmt-dev-manager-discovery"
	var asked []string
	x.iam.getRole = func(name string) (*iam.GetRoleOutput, error) {
		asked = append(asked, name)
		if name == "DmtDiscoveryRole" {
			return chkTrust(mgr), nil
		}
		return nil, chkNoEntity()
	}
	c := chkRow(t, x.run(), "Dormeto roles")
	if c.Verdict != Fail || !strings.Contains(c.Detail, "DmtDiscoveryRole") || !strings.Contains(c.Detail, mgr) || !strings.Contains(c.Detail, "another manager's (decision 15)") {
		t.Errorf("roles = %d %q", c.Verdict, c.Detail)
	}
	x.f.Saved = &Settings{Installed: "0.10.0"}
	chkWant(t, x, "Dormeto roles", OK, "this install's")
}

func TestChecksDormetoRolesStaleUniqueID(t *testing.T) {
	x := chkNew()
	x.iam.getRole = func(name string) (*iam.GetRoleOutput, error) {
		if name == "DmtDiscoveryRole" {
			return chkTrust("AROAEXAMPLE123"), nil
		}
		return nil, chkNoEntity()
	}
	chkWant(t, x, "Dormeto roles", Fail, "DmtDiscoveryRole exist, trusting a deleted role: left by an earlier manager; delete them before installing")
	x.f.Saved = &Settings{Installed: "0.10.0"}
	chkWant(t, x, "Dormeto roles", OK, "this install's")
}

func TestChecksDormetoRolesNamesAndBoundary(t *testing.T) {
	x := chkNew()
	x.s.Vars = map[string]json.RawMessage{"discovery_role_name": json.RawMessage(`"/dmt/MyDiscovery"`), "command_role_name": json.RawMessage(`"MyCommand"`)}
	var asked []string
	x.iam.getRole = func(name string) (*iam.GetRoleOutput, error) {
		asked = append(asked, name)
		return nil, chkNoEntity()
	}
	x.iam.policy = func(arn string) (*iam.GetPolicyOutput, error) {
		if arn != "arn:aws:iam::532105290256:policy/DmtRouterBoundary" {
			t.Errorf("policy = %s", arn)
		}
		return &iam.GetPolicyOutput{}, nil
	}
	chkWant(t, x, "Dormeto roles", Fail, "DmtRouterBoundary exist")
	var roles []string
	for _, a := range asked {
		if a == "MyDiscovery" || a == "MyCommand" {
			roles = append(roles, a)
		}
	}
	if len(roles) != 2 {
		t.Errorf("asked = %v", asked)
	}
}

func TestChecksRelease(t *testing.T) {
	x := chkNew()
	chkWant(t, x, "release", OK, "manager 0.11.0")
	x.f.Release = Release{Version: "0.11.0-dev", Local: "/tmp/build"}
	chkWant(t, x, "release", OK, "manager 0.11.0-dev (local: /tmp/build)")
	x.f.ReleaseErr = errors.New("0.9.0 is older than 0.11.0")
	chkWant(t, x, "release", Fail, "0.9.0 is older than 0.11.0")
}

func TestChecksAdministrators(t *testing.T) {
	chkWant(t, chkNew(), "administrators", OK, "a@example.com, b@example.com")
}

func TestChecksDomain(t *testing.T) {
	x := chkNew()
	chkWant(t, x, "domain", OK, "dormeto.example.com, auth.dormeto.example.com: certificate and zone")
	x.acm.names = []string{"dormeto.example.com"}
	chkWant(t, x, "domain", Fail, "auth.dormeto.example.com")
	x.acm.names = []string{"*.example.com"}
	chkWant(t, x, "domain", Fail, "dormeto.example.com")
	c := chkRow(t, x.run(), "domain")
	if !strings.Contains(c.Detail, "auth.dormeto.example.com") {
		t.Errorf("two labels deeper: %q", c.Detail)
	}
	x.acm.names = []string{"*.dormeto.example.com", "dormeto.example.com"}
	chkWant(t, x, "domain", OK, "certificate and zone")
	x.acm.status = "PENDING_VALIDATION"
	chkWant(t, x, "domain", Fail, "not issued")
	x.acm.status = "ISSUED"
	x.r53.name = "other.org."
	chkWant(t, x, "domain", Fail, "outside the zone")
	x.r53.err = chkAPI("NoSuchHostedZone")
	chkWant(t, x, "domain", Fail, "zone Z1 not found")
	x.s.Domain = nil
	chkWant(t, x, "domain", Skip, "not used (a cloudfront.net host)")
}

func TestChecksSES(t *testing.T) {
	x := chkNew()
	chkWant(t, x, "SES", OK, "")
	x.ses.verified = false
	chkWant(t, x, "SES", Fail, "example.com is not verified for sending in eu-west-1")
	x.ses.notFound = true
	chkWant(t, x, "SES", Fail, "not verified for sending")
	x = chkNew()
	x.ses.production = false
	chkWant(t, x, "SES", Warn, "SES sandbox: invitations reach verified addresses only")
	x.s.SESIdentity = ""
	chkWant(t, x, "SES", Skip, "not used (Cognito sends the invitations, about 50 a day)")
}

func TestCheckLineAndHelpers(t *testing.T) {
	for _, c := range []struct {
		v    Verdict
		want string
	}{{OK, "  ✓ n: d"}, {Warn, "  ! n: d"}, {Fail, "  ✗ n: d"}, {Skip, "  - n: d"}} {
		if got := (Check{Name: "n", Detail: "d", Verdict: c.v}).Line(); got != c.want {
			t.Errorf("Line = %q, want %q", got, c.want)
		}
	}
	cs := []Check{{Name: "a"}, {Name: "b", Verdict: Warn}, {Name: "c", Verdict: Skip}}
	if Failed(cs) {
		t.Error("Failed without a Fail")
	}
	if w := Warnings(cs); len(w) != 1 || w[0].Name != "b" {
		t.Errorf("warnings = %v", w)
	}
	if !Failed(append(cs, Check{Verdict: Fail})) {
		t.Error("Failed missed a Fail")
	}
}
