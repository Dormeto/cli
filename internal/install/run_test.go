package install

// The commands' runs (run.go) through fakes of every port: one account's world (its organization, the install's
// bucket, role, log group, project and builds, a started build running at once, the manager's table and the regions'
// functions; with rwOrganization, the management account's and the member accounts' too), a scripted prompter, the
// releases on a fake HTTP, a fake clock.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cogtypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const (
	rwAccount   = "532105290256"
	rwRole      = "AWSReservedSSO_Admin_a15b943a5b9a9846"
	rwDashboard = "https://d1a2b3c4.cloudfront.net"
	rwArgs      = "--profile p --region eu-west-1 --env prod"
)

var rwT0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

// The plans a plan build writes: three resources created; nothing changed; one updated and one replaced.
const (
	rwPlan = `{"format_version":"1.2","resource_changes":[` +
		`{"address":"module.dormeto.aws_cloudfront_distribution.dashboard","type":"aws_cloudfront_distribution","change":{"actions":["create"]}},` +
		`{"address":"module.dormeto.aws_cognito_user_pool.users","type":"aws_cognito_user_pool","change":{"actions":["create"]}},` +
		`{"address":"module.dormeto.aws_lambda_function.api","type":"aws_lambda_function","change":{"actions":["create"]}}]}`
	rwEmpty = `{"format_version":"1.2","resource_changes":[` +
		`{"address":"module.dormeto.aws_lambda_function.api","type":"aws_lambda_function","change":{"actions":["no-op"]}}]}`
	rwReplace = `{"format_version":"1.2","resource_changes":[` +
		`{"address":"module.dormeto.aws_lambda_function.api","type":"aws_lambda_function","change":{"actions":["update"]}},` +
		`{"address":"module.dormeto.aws_dynamodb_table.events","type":"aws_dynamodb_table","change":{"actions":["delete","create"]}}]}`
	rwOutputs = `{"dashboard_url":{"value":"` + rwDashboard + `"},"user_pool_id":{"value":"eu-west-1_abc"},"version":{"value":"0.11.0"}}`
	// The manager's Terraform state: with its resources (applied), and without (destroyed).
	rwState     = `{"version":4,"terraform_version":"1.16.5","resources":[{"mode":"managed","type":"aws_dynamodb_table","name":"manager"}]}`
	rwNoneState = `{"version":4,"terraform_version":"1.16.5","resources":[]}`
	// What a destroy plan build writes: the manager's three resources destroyed.
	rwDestroy = `{"format_version":"1.2","resource_changes":[` +
		`{"address":"module.dormeto.aws_cognito_user_pool.users","type":"aws_cognito_user_pool","change":{"actions":["delete"]}},` +
		`{"address":"module.dormeto.aws_dynamodb_table.manager","type":"aws_dynamodb_table","change":{"actions":["delete"]}},` +
		`{"address":"module.dormeto.aws_lambda_function.api","type":"aws_lambda_function","change":{"actions":["delete"]}}]}`
)

// rwBuild is a build of a project: vars is nil for a build the run did not start (env then holds its variables).
type rwBuild struct {
	id     string
	vars   map[string]string
	env    map[string]string
	status cbtypes.StatusType
	start  time.Time
	until  time.Time // in progress until the clock reaches it, then status
	log    []string
	// accounts are the accounts an organization build read in the bucket's accounts.txt as it started.
	accounts []string
}

// state is the build's status at now.
func (b *rwBuild) state(now time.Time) cbtypes.StatusType {
	if now.Before(b.until) {
		return cbtypes.StatusTypeInProgress
	}
	return b.status
}

// rwAcct is one account's install: its bucket, its project's role, log group and builds.
type rwAcct struct {
	id, name                        string            // the account; its project: dmt-prod-install, or dmt-prod-organization in the management account
	prefix                          string            // its writes' in rwWorld.writes: "" for the manager's account, "mgmt " for the management account
	objects                         map[string]string // its bucket, by key
	bucket, role, project, logGroup bool              // the bootstrap's resources exist
	builds                          []*rwBuild        // oldest first
	ends                            map[string]cbtypes.StatusType
	runFor                          time.Duration    // a started build runs that long (0: it ends at once)
	followErr                       map[string]error // by ACTION: following its build fails (BatchGetBuilds of it alone)
}

func rwNewAcct(id, name, prefix string) rwAcct {
	return rwAcct{id: id, name: name, prefix: prefix, objects: map[string]string{}, ends: map[string]cbtypes.StatusType{}, followErr: map[string]error{}}
}

// started are the builds the run started.
func (a *rwAcct) started() []*rwBuild {
	var out []*rwBuild
	for _, b := range a.builds {
		if b.vars != nil {
			out = append(out, b)
		}
	}
	return out
}

// actions are the ACTION of each build the run started.
func (a *rwAcct) actions() []string {
	var out []string
	for _, b := range a.started() {
		out = append(out, b.vars["ACTION"])
	}
	return out
}

func (a *rwAcct) build(id string) *rwBuild {
	for _, b := range a.builds {
		if b.id == id {
			return b
		}
	}
	return nil
}

// rwWorld is one account in eu-west-1 (the profile p), member of an organization with all features unless org is nil;
// with rwOrganization, the organization's management account (the profile m) and other accounts too.
type rwWorld struct {
	rwAcct        // the manager's account
	t             *testing.T
	org           *Organization
	region        string // the profile's region; "": none
	plan          string // the plan.json a plan build writes
	destroyPlan   string // the plan.json a plan-destroy build writes
	simDenied     bool   // iam:SimulatePrincipalPolicy refused: a warning
	identityErr   error  // sts:GetCallerIdentity's error
	cancelOnStart bool   // Ctrl-C as the build starts
	batchErr      error  // codebuild:BatchGetBuilds's error
	writes        []string
	failWrites    map[string]error // a write (as writes records it) that fails so
	readOnly      bool
	clock         time.Time
	detach        bool // following a running build: Ctrl-C
	cancel        context.CancelFunc
	http          fakeHTTP
	status        *trkStatus // the terminal's status line; nil: none

	mgmt    *rwAcct            // the management account, the profile m; nil: no such profile
	mgmtErr error              // the profile m's clients fail so
	members map[string]orgAcct // the organization's other accounts, as the member role reaches them
	policy  *string            // the delegation policy; nil: none
	failing map[string]string  // account → its error in the organization build's roles.json
	mu      sync.Mutex         // the survey assumes the member role from many goroutines
	assumed []string           // "<profile> <role ARN>" of each member role assumed

	regions      map[string]string   // the account's regions and their opt-in status
	functions    map[string][]string // region → the Lambda functions' names
	listed       []string            // the regions whose functions were listed
	routers      int                 // the router items in the manager's table
	lambdaDenied map[string]bool     // the regions whose functions cannot be listed
	noTable      bool                // the manager's table does not exist
	found        map[string]bool     // the functions GetFunction finds (a manager installed by other means)
	conditional  []string            // the keys put only where there was none (If-None-Match: *)
}

func rwNew(t *testing.T) *rwWorld {
	w := &rwWorld{
		rwAcct: rwNewAcct(rwAccount, "dmt-prod-install", ""),
		t:      t, region: "eu-west-1", plan: rwPlan, destroyPlan: rwDestroy, clock: rwT0,
		org:  &Organization{ID: "o-vvk0115ixn", Management: "809706483662", AllFeatures: true},
		http: fakeHTTP{bodies: map[string]string{rwDashboard: "ok"}},
		regions: map[string]string{"eu-west-1": "opt-in-not-required", "us-east-1": "opt-in-not-required", "eu-south-1": "opted-in",
			"ap-east-1": "not-opted-in"},
		functions: map[string][]string{},
	}
	w.release("0.11.0", true)
	return w
}

// rwMine is this manager's discovery role, which the member accounts' roles trust.
const rwMine = "arn:aws:iam::" + rwAccount + ":role/dmt-prod-manager-discovery"

// rwOrganization gives the world its organization: the management account, reached with the profile m, and four other
// accounts: roles trusting this manager (444…), none (555…), the member role not assumed (666…), another manager's
// roles (777…).
func (w *rwWorld) rwOrganization() {
	m := rwNewAcct(w.org.Management, "dmt-prod-organization", "mgmt ")
	w.mgmt = &m
	w.failing = map[string]string{}
	w.members = map[string]orgAcct{
		"444444444444": {status: orgtypes.AccountStatusActive, discovery: []string{rwMine}},
		"555555555555": {status: orgtypes.AccountStatusActive},
		"666666666666": {status: orgtypes.AccountStatusActive, unreach: "AccessDenied: not authorized to perform sts:AssumeRole"},
		"777777777777": {status: orgtypes.AccountStatusActive, discovery: []string{orgDev}, boundary: true},
	}
}

// release publishes a manager version on releases.dormeto.com, its delegation policy the organization tests' template;
// latest: latest.json names it.
func (w *rwWorld) release(v string, latest bool) {
	var sums strings.Builder
	for _, f := range releaseFiles(v) {
		body := f
		if f == "dormeto-organization-policy.json" {
			body = orgTemplate
		}
		sums.WriteString(sum(body) + "  " + f + "\n")
	}
	w.http.bodies[ReleasesURL+"/"+v+"/SHA256SUMS"] = sums.String()
	w.http.bodies[ReleasesURL+"/"+v+"/dormeto-organization-policy.json"] = orgTemplate
	if latest {
		w.http.bodies[ReleasesURL+"/latest.json"] = `{"version":"` + v + `"}`
	}
}

// installed is prod installed at version: the bootstrap, install.json (its plan p0), applied.json (the apply a0, after
// p0), outputs.json and the state.
func (w *rwWorld) installed(version string) {
	w.bucket, w.role, w.project, w.logGroup = true, true, true, true
	w.save(Settings{Env: "prod", Region: "eu-west-1", Version: version, Mode: "organization", AdminEmails: []string{"you@example.com"},
		OrganizationID: "o-vvk0115ixn", ReleaseBase: ReleasesURL + "/" + version, LastPlan: "dmt-prod-install:p0"})
	w.objects["prod/applied.json"] = `{"version":"` + version + `","build":"dmt-prod-install:a0"}`
	w.objects["prod/outputs.json"] = rwOutputs
	w.objects["prod/manager.tfstate"] = rwState
	w.objects["prod/plans/dmt-prod-install:p0/plan.json"] = rwPlan
	w.builds = append(w.builds,
		&rwBuild{id: "dmt-prod-install:p0", env: map[string]string{"ACTION": "plan"}, status: cbtypes.StatusTypeSucceeded, start: rwT0.Add(-2 * time.Hour)},
		&rwBuild{id: "dmt-prod-install:a0", env: map[string]string{"ACTION": "apply", "PLAN_BUILD": "dmt-prod-install:p0"}, status: cbtypes.StatusTypeSucceeded,
			start: rwT0.Add(-time.Hour)})
}

func (w *rwWorld) save(s Settings) {
	b, err := s.JSON()
	if err != nil {
		w.t.Fatal(err)
	}
	w.objects["prod/install.json"] = string(b)
}

// saved is prod's install.json.
func (w *rwWorld) saved() Settings {
	w.t.Helper()
	s, err := ParseSettings([]byte(w.objects["prod/install.json"]))
	if err != nil {
		w.t.Fatal(err)
	}
	return s
}

func (w *rwWorld) write(call string) {
	if w.readOnly {
		w.t.Errorf("a write in a read-only run: %s", call)
	}
	w.writes = append(w.writes, call)
}

// failed is the error failWrites gives the write call, recorded first; nil when it succeeds.
func (w *rwWorld) failed(call string) error {
	err := w.failWrites[call]
	if err != nil {
		w.write(call + " (failed)")
	}
	return err
}

func (w *rwWorld) now() time.Time { return w.clock }

// every advances the clock and answers at once; with detach, it is Ctrl-C instead (the run cancelled, no tick).
func (w *rwWorld) every(d time.Duration) <-chan time.Time {
	w.clock = w.clock.Add(d)
	if w.detach {
		w.cancel()
		return make(chan time.Time)
	}
	c := make(chan time.Time)
	close(c)
	return c
}

func (w *rwWorld) clients() Clients { return w.clientsOf(&w.rwAcct) }

// clientsOf are the fakes of account a.
func (w *rwWorld) clientsOf(a *rwAcct) Clients {
	return Clients{
		STS: rwSTS{w: w, a: a}, IAM: rwIAM{w: w, a: a}, S3: rwS3{w: w, a: a}, CodeBuild: rwCB{w: w, a: a}, EventBridge: rwEvents{w: w, a: a}, Logs: rwLogs{w: w, a: a},
		Organizations: rwOrgs{w: w}, EC2: rwEC2{w: w},
		ACM:     chkACM{status: "ISSUED", names: []string{"dormeto.example.com", "*.dormeto.example.com"}},
		Route53: chkR53{name: "example.com."}, SESv2: chkSES{verified: true, production: true},
		Lambda: rwLambda{chkLambda: chkLambda{found: w.found}, w: w, region: "eu-west-1"}, Cognito: rwCognito{}, DynamoDB: rwDDB{w: w},
	}
}

// enabled tells whether the account's region is enabled.
func (w *rwWorld) enabled(region string) bool {
	return w.regions[region] == "opt-in-not-required" || w.regions[region] == "opted-in"
}

// assume is the member role assumed in an account of the organization, from the management account's profile.
func (w *rwWorld) assume(_ context.Context, profile, region, roleARN string) (Clients, error) {
	w.mu.Lock()
	w.assumed = append(w.assumed, profile+" "+roleARN)
	w.mu.Unlock()
	if region != "eu-west-1" {
		w.t.Errorf("member role assumed in %q", region)
	}
	rest, _ := strings.CutPrefix(roleARN, "arn:aws:iam::")
	account, _, _ := strings.Cut(rest, ":")
	a, ok := w.members[account]
	switch {
	case !ok:
		return Clients{}, fmt.Errorf("no account %s", account)
	case a.unreach != "":
		return Clients{}, errors.New(a.unreach)
	}
	return Clients{IAM: orgIAM{a: a}}, nil
}

// run runs a command; answers are the person's, in order: "" takes the suggestion, "^C" is Ctrl-C at that prompt.
func (w *rwWorld) run(cmd func(context.Context, Deps, Request) int, r Request, terminal bool, answers ...string) (code int, out string, asked []string) {
	w.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.cancel = cancel
	p := &rwPrompter{t: w.t, answers: answers, cancel: cancel}
	var b bytes.Buffer
	d := Deps{
		Clients: func(_ context.Context, profile, region string) (Clients, error) {
			switch {
			case region != "eu-west-1" && profile == "p" && w.enabled(region): // another region's functions
				c := w.clients()
				c.Lambda = rwLambda{w: w, region: region}
				return c, nil
			case region != "eu-west-1":
			case profile == "p":
				return w.clients(), nil
			case profile == "m" && w.mgmtErr != nil:
				return Clients{}, w.mgmtErr
			case profile == "m" && w.mgmt != nil:
				return w.clientsOf(w.mgmt), nil
			}
			w.t.Errorf("clients of profile %q in %q", profile, region)
			return Clients{}, errors.New("no such profile")
		},
		Region:   func(context.Context, string) (string, error) { return w.region, nil },
		Assume:   w.assume,
		HTTP:     w.http,
		Prompter: p,
		Out:      &b,
		Terminal: terminal,
		Now:      w.now,
		Every:    w.every,
		Version:  "0.2.0",
	}
	if w.status != nil {
		d.Status = w.status
	}
	code = cmd(ctx, d, r)
	if len(p.answers) > 0 {
		w.t.Errorf("answers left: %q", p.answers)
	}
	return code, b.String(), p.asked
}

// rwPrompter answers from a script and records each question with its suggestion.
type rwPrompter struct {
	t       *testing.T
	answers []string
	asked   []string
	cancel  context.CancelFunc
}

func (p *rwPrompter) next(suggestion string) (string, error) {
	if len(p.answers) == 0 {
		p.t.Errorf("unexpected question %q", p.asked[len(p.asked)-1])
		return "", io.EOF
	}
	a := p.answers[0]
	p.answers = p.answers[1:]
	switch a {
	case "^C":
		p.cancel()
		return "", context.Canceled
	case "^D":
		return "", io.EOF
	case "":
		return suggestion, nil
	}
	return a, nil
}

func (p *rwPrompter) Ask(question, suggestion string) (string, error) {
	if suggestion != "" {
		p.asked = append(p.asked, question+" ["+suggestion+"]")
	} else {
		p.asked = append(p.asked, question)
	}
	return p.next(suggestion)
}

func (p *rwPrompter) Confirm(question string) (bool, error) {
	p.asked = append(p.asked, question+" [y/N]")
	a, err := p.next("")
	return a == "y", err
}

type rwSTS struct {
	STS
	w *rwWorld
	a *rwAcct
}

func (f rwSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	if f.w.identityErr != nil {
		return nil, f.w.identityErr
	}
	return &sts.GetCallerIdentityOutput{Account: aws.String(f.a.id), Arn: aws.String("arn:aws:sts::" + f.a.id + ":assumed-role/" + rwRole + "/you@example.com")}, nil
}

type rwOrgs struct {
	Organizations
	w *rwWorld
}

func (f rwOrgs) DescribeOrganization(context.Context, *organizations.DescribeOrganizationInput, ...func(*organizations.Options)) (*organizations.DescribeOrganizationOutput, error) {
	o := f.w.org
	if o == nil {
		return nil, &orgtypes.AWSOrganizationsNotInUseException{Message: aws.String("not in use")}
	}
	features := orgtypes.OrganizationFeatureSetConsolidatedBilling
	if o.AllFeatures {
		features = orgtypes.OrganizationFeatureSetAll
	}
	return &organizations.DescribeOrganizationOutput{Organization: &orgtypes.Organization{Id: aws.String(o.ID), MasterAccountId: aws.String(o.Management), FeatureSet: features}}, nil
}

// ListAccounts is the manager's account, the management account and the other accounts, in one page.
func (f rwOrgs) ListAccounts(context.Context, *organizations.ListAccountsInput, ...func(*organizations.Options)) (*organizations.ListAccountsOutput, error) {
	out := &organizations.ListAccountsOutput{Accounts: []orgtypes.Account{
		{Id: aws.String(rwAccount), Status: orgtypes.AccountStatusActive}, {Id: aws.String(f.w.org.Management), Status: orgtypes.AccountStatusActive}}}
	for _, id := range slices.Sorted(maps.Keys(f.w.members)) {
		out.Accounts = append(out.Accounts, orgtypes.Account{Id: aws.String(id), Status: f.w.members[id].status})
	}
	return out, nil
}

func (f rwOrgs) DescribeResourcePolicy(context.Context, *organizations.DescribeResourcePolicyInput, ...func(*organizations.Options)) (*organizations.DescribeResourcePolicyOutput, error) {
	if f.w.policy == nil {
		return nil, &orgtypes.ResourcePolicyNotFoundException{Message: aws.String("none")}
	}
	return &organizations.DescribeResourcePolicyOutput{ResourcePolicy: &orgtypes.ResourcePolicy{Content: f.w.policy}}, nil
}

type rwIAM struct {
	IAM
	w *rwWorld
	a *rwAcct
}

func (f rwIAM) ListAccountAliases(context.Context, *iam.ListAccountAliasesInput, ...func(*iam.Options)) (*iam.ListAccountAliasesOutput, error) {
	return &iam.ListAccountAliasesOutput{AccountAliases: []string{"sandbox"}}, nil
}

func (f rwIAM) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	switch name := aws.ToString(in.RoleName); {
	case name == rwRole:
		return &iam.GetRoleOutput{Role: &iamtypes.Role{Arn: aws.String("arn:aws:iam::" + f.a.id + ":role/aws-reserved/sso.amazonaws.com/eu-west-1/" + name)}}, nil
	case name == f.a.name && f.a.role:
		return &iam.GetRoleOutput{Role: &iamtypes.Role{RoleName: aws.String(name), Path: aws.String("/dormeto/"),
			Arn: aws.String("arn:aws:iam::" + f.a.id + ":role/dormeto/" + name), Tags: []iamtypes.Tag{{Key: aws.String("managed-by"), Value: aws.String("dormeto-cli")}}}}, nil
	}
	return nil, chkNoEntity()
}

func (f rwIAM) SimulatePrincipalPolicy(_ context.Context, in *iam.SimulatePrincipalPolicyInput, _ ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error) {
	if f.w.simDenied {
		return nil, chkAPI("AccessDenied")
	}
	return chkIAM{}.SimulatePrincipalPolicy(context.Background(), in)
}

func (f rwIAM) GetPolicy(context.Context, *iam.GetPolicyInput, ...func(*iam.Options)) (*iam.GetPolicyOutput, error) {
	return nil, chkNoEntity()
}

func (f rwIAM) CreateRole(_ context.Context, in *iam.CreateRoleInput, _ ...func(*iam.Options)) (*iam.CreateRoleOutput, error) {
	f.w.write(f.a.prefix + "iam:CreateRole " + aws.ToString(in.RoleName))
	f.a.role = true
	return &iam.CreateRoleOutput{Role: &iamtypes.Role{Arn: aws.String("arn:aws:iam::" + f.a.id + ":role/dormeto/" + aws.ToString(in.RoleName))}}, nil
}

func (f rwIAM) UpdateAssumeRolePolicy(context.Context, *iam.UpdateAssumeRolePolicyInput, ...func(*iam.Options)) (*iam.UpdateAssumeRolePolicyOutput, error) {
	f.w.write(f.a.prefix + "iam:UpdateAssumeRolePolicy")
	return &iam.UpdateAssumeRolePolicyOutput{}, nil
}

func (f rwIAM) AttachRolePolicy(context.Context, *iam.AttachRolePolicyInput, ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error) {
	f.w.write(f.a.prefix + "iam:AttachRolePolicy")
	return &iam.AttachRolePolicyOutput{}, nil
}

func (f rwIAM) PutRolePolicy(_ context.Context, in *iam.PutRolePolicyInput, _ ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error) {
	f.w.write(f.a.prefix + "iam:PutRolePolicy " + aws.ToString(in.RoleName))
	return &iam.PutRolePolicyOutput{}, nil
}

func (f rwIAM) DeleteRolePolicy(_ context.Context, in *iam.DeleteRolePolicyInput, _ ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error) {
	f.w.write(f.a.prefix + "iam:DeleteRolePolicy " + aws.ToString(in.RoleName))
	return &iam.DeleteRolePolicyOutput{}, nil
}

func (f rwIAM) DetachRolePolicy(context.Context, *iam.DetachRolePolicyInput, ...func(*iam.Options)) (*iam.DetachRolePolicyOutput, error) {
	f.w.write(f.a.prefix + "iam:DetachRolePolicy")
	return &iam.DetachRolePolicyOutput{}, nil
}

func (f rwIAM) DeleteRole(_ context.Context, in *iam.DeleteRoleInput, _ ...func(*iam.Options)) (*iam.DeleteRoleOutput, error) {
	if err := f.w.failed(f.a.prefix + "iam:DeleteRole " + aws.ToString(in.RoleName)); err != nil {
		return nil, err
	}
	f.w.write(f.a.prefix + "iam:DeleteRole " + aws.ToString(in.RoleName))
	f.a.role = false
	return &iam.DeleteRoleOutput{}, nil
}

// rwEvents are the rule's calls; the world records its writes.
type rwEvents struct {
	EventBridge
	w *rwWorld
	a *rwAcct
}

func (f rwEvents) PutRule(_ context.Context, in *eventbridge.PutRuleInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutRuleOutput, error) {
	f.w.write(f.a.prefix + "events:PutRule " + aws.ToString(in.Name))
	return &eventbridge.PutRuleOutput{}, nil
}

func (f rwEvents) PutTargets(_ context.Context, in *eventbridge.PutTargetsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutTargetsOutput, error) {
	f.w.write(f.a.prefix + "events:PutTargets " + aws.ToString(in.Rule))
	return &eventbridge.PutTargetsOutput{}, nil
}

func (f rwEvents) RemoveTargets(_ context.Context, in *eventbridge.RemoveTargetsInput, _ ...func(*eventbridge.Options)) (*eventbridge.RemoveTargetsOutput, error) {
	f.w.write(f.a.prefix + "events:RemoveTargets " + aws.ToString(in.Rule))
	return &eventbridge.RemoveTargetsOutput{}, nil
}

func (f rwEvents) DeleteRule(_ context.Context, in *eventbridge.DeleteRuleInput, _ ...func(*eventbridge.Options)) (*eventbridge.DeleteRuleOutput, error) {
	f.w.write(f.a.prefix + "events:DeleteRule " + aws.ToString(in.Name))
	return &eventbridge.DeleteRuleOutput{}, nil
}

type rwS3 struct {
	S3
	w *rwWorld
	a *rwAcct
}

func (f rwS3) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	if f.a.bucket {
		return &s3.HeadBucketOutput{}, nil
	}
	return nil, chkAPI("NotFound")
}

func (f rwS3) CreateBucket(context.Context, *s3.CreateBucketInput, ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	f.w.write(f.a.prefix + "s3:CreateBucket")
	f.a.bucket = true
	return &s3.CreateBucketOutput{}, nil
}

func (f rwS3) PutBucketVersioning(context.Context, *s3.PutBucketVersioningInput, ...func(*s3.Options)) (*s3.PutBucketVersioningOutput, error) {
	f.w.write(f.a.prefix + "s3:PutBucketVersioning")
	return &s3.PutBucketVersioningOutput{}, nil
}

func (f rwS3) PutBucketEncryption(context.Context, *s3.PutBucketEncryptionInput, ...func(*s3.Options)) (*s3.PutBucketEncryptionOutput, error) {
	f.w.write(f.a.prefix + "s3:PutBucketEncryption")
	return &s3.PutBucketEncryptionOutput{}, nil
}

func (f rwS3) PutPublicAccessBlock(context.Context, *s3.PutPublicAccessBlockInput, ...func(*s3.Options)) (*s3.PutPublicAccessBlockOutput, error) {
	f.w.write(f.a.prefix + "s3:PutPublicAccessBlock")
	return &s3.PutPublicAccessBlockOutput{}, nil
}

func (f rwS3) PutBucketPolicy(context.Context, *s3.PutBucketPolicyInput, ...func(*s3.Options)) (*s3.PutBucketPolicyOutput, error) {
	f.w.write(f.a.prefix + "s3:PutBucketPolicy")
	return &s3.PutBucketPolicyOutput{}, nil
}

func (f rwS3) PutBucketLifecycleConfiguration(context.Context, *s3.PutBucketLifecycleConfigurationInput, ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error) {
	f.w.write(f.a.prefix + "s3:PutBucketLifecycleConfiguration")
	return &s3.PutBucketLifecycleConfigurationOutput{}, nil
}

func (f rwS3) PutBucketNotificationConfiguration(context.Context, *s3.PutBucketNotificationConfigurationInput, ...func(*s3.Options)) (*s3.PutBucketNotificationConfigurationOutput, error) {
	f.w.write(f.a.prefix + "s3:PutBucketNotificationConfiguration")
	return &s3.PutBucketNotificationConfigurationOutput{}, nil
}

func (f rwS3) PutBucketTagging(context.Context, *s3.PutBucketTaggingInput, ...func(*s3.Options)) (*s3.PutBucketTaggingOutput, error) {
	f.w.write(f.a.prefix + "s3:PutBucketTagging")
	return &s3.PutBucketTaggingOutput{}, nil
}

func (f rwS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if !f.a.bucket {
		return nil, chkAPI("NoSuchBucket")
	}
	body, ok := f.a.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, chkAPI("NoSuchKey")
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (f rwS3) PutObject(ctx context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	if err := f.w.failed(f.a.prefix + "s3:PutObject " + aws.ToString(in.Key)); err != nil {
		return nil, err
	}
	if aws.ToString(in.IfNoneMatch) == "*" { // put only where there is none
		if _, ok := f.a.objects[aws.ToString(in.Key)]; ok {
			return nil, chkAPI("PreconditionFailed")
		}
		f.w.conditional = append(f.w.conditional, aws.ToString(in.Key))
	}
	f.w.write(f.a.prefix + "s3:PutObject " + aws.ToString(in.Key))
	f.a.objects[aws.ToString(in.Key)] = string(b)
	return &s3.PutObjectOutput{}, nil
}

func (f rwS3) ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if !f.a.bucket {
		return nil, chkAPI("NoSuchBucket")
	}
	out := &s3.ListObjectsV2Output{}
	for _, k := range slices.Sorted(maps.Keys(f.a.objects)) {
		out.Contents = append(out.Contents, s3types.Object{Key: aws.String(k)})
	}
	return out, nil
}

// ListObjectVersions is each object as one version, in one page.
func (f rwS3) ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	if !f.a.bucket {
		return nil, chkAPI("NoSuchBucket")
	}
	out := &s3.ListObjectVersionsOutput{}
	for _, k := range slices.Sorted(maps.Keys(f.a.objects)) {
		out.Versions = append(out.Versions, s3types.ObjectVersion{Key: aws.String(k), VersionId: aws.String("v1")})
	}
	return out, nil
}

func (f rwS3) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	f.w.write(f.a.prefix + "s3:DeleteObjects")
	for _, o := range in.Delete.Objects {
		delete(f.a.objects, aws.ToString(o.Key))
	}
	return &s3.DeleteObjectsOutput{}, nil
}

func (f rwS3) DeleteBucket(context.Context, *s3.DeleteBucketInput, ...func(*s3.Options)) (*s3.DeleteBucketOutput, error) {
	f.w.write(f.a.prefix + "s3:DeleteBucket")
	if len(f.a.objects) > 0 {
		return nil, chkAPI("BucketNotEmpty")
	}
	f.a.bucket = false
	return &s3.DeleteBucketOutput{}, nil
}

type rwCB struct {
	CodeBuild
	w *rwWorld
	a *rwAcct
}

func (f rwCB) ListCuratedEnvironmentImages(context.Context, *codebuild.ListCuratedEnvironmentImagesInput, ...func(*codebuild.Options)) (*codebuild.ListCuratedEnvironmentImagesOutput, error) {
	return chkCB{images: []string{buildImage}}.ListCuratedEnvironmentImages(context.Background(), nil)
}

func (f rwCB) BatchGetProjects(context.Context, *codebuild.BatchGetProjectsInput, ...func(*codebuild.Options)) (*codebuild.BatchGetProjectsOutput, error) {
	if f.a.project {
		return &codebuild.BatchGetProjectsOutput{Projects: []cbtypes.Project{{Name: aws.String(f.a.name)}}}, nil
	}
	return &codebuild.BatchGetProjectsOutput{}, nil
}

func (f rwCB) CreateProject(context.Context, *codebuild.CreateProjectInput, ...func(*codebuild.Options)) (*codebuild.CreateProjectOutput, error) {
	f.w.write(f.a.prefix + "codebuild:CreateProject")
	f.a.project = true
	return &codebuild.CreateProjectOutput{}, nil
}

func (f rwCB) UpdateProject(context.Context, *codebuild.UpdateProjectInput, ...func(*codebuild.Options)) (*codebuild.UpdateProjectOutput, error) {
	f.w.write(f.a.prefix + "codebuild:UpdateProject")
	return &codebuild.UpdateProjectOutput{}, nil
}

func (f rwCB) DeleteProject(context.Context, *codebuild.DeleteProjectInput, ...func(*codebuild.Options)) (*codebuild.DeleteProjectOutput, error) {
	f.w.write(f.a.prefix + "codebuild:DeleteProject")
	f.a.project = false
	return &codebuild.DeleteProjectOutput{}, nil
}

func (f rwCB) ListBuildsForProject(context.Context, *codebuild.ListBuildsForProjectInput, ...func(*codebuild.Options)) (*codebuild.ListBuildsForProjectOutput, error) {
	var ids []string
	for i := len(f.a.builds) - 1; i >= 0; i-- {
		ids = append(ids, f.a.builds[i].id)
	}
	return &codebuild.ListBuildsForProjectOutput{Ids: ids}, nil
}

func (f rwCB) BatchGetBuilds(_ context.Context, in *codebuild.BatchGetBuildsInput, _ ...func(*codebuild.Options)) (*codebuild.BatchGetBuildsOutput, error) {
	if f.w.batchErr != nil {
		return nil, f.w.batchErr
	}
	if len(in.Ids) == 1 {
		if b := f.a.build(in.Ids[0]); b != nil && b.vars != nil && f.a.followErr[b.vars["ACTION"]] != nil {
			return nil, f.a.followErr[b.vars["ACTION"]]
		}
	}
	out := &codebuild.BatchGetBuildsOutput{}
	for _, id := range in.Ids {
		b := f.a.build(id)
		if b == nil {
			out.BuildsNotFound = append(out.BuildsNotFound, id)
			continue
		}
		vars := b.vars
		if vars == nil {
			vars = b.env
		}
		env := &cbtypes.ProjectEnvironment{}
		for _, k := range slices.Sorted(maps.Keys(vars)) {
			env.EnvironmentVariables = append(env.EnvironmentVariables, cbtypes.EnvironmentVariable{Name: aws.String(k), Value: aws.String(vars[k])})
		}
		out.Builds = append(out.Builds, cbtypes.Build{Id: aws.String(b.id), BuildStatus: b.state(f.w.clock), StartTime: aws.Time(b.start), CurrentPhase: aws.String("BUILD"),
			Environment: env, Logs: &cbtypes.LogsLocation{GroupName: aws.String("/aws/codebuild/" + f.a.name), StreamName: aws.String(b.id)}})
	}
	return out, nil
}

// StartBuild runs the build: it ends as ends says (SUCCEEDED by default), at once unless runFor. A manager's plan that
// succeeds writes its plan.json (a destroy plan's: destroyPlan), an apply its applied.json, outputs.json and the state
// with resources, an apply-destroy removes the first two and empties the state; the organization's build says each
// account's roles (accounts.txt's) and writes roles.json, the accounts in failing failed.
func (f rwCB) StartBuild(_ context.Context, in *codebuild.StartBuildInput, _ ...func(*codebuild.Options)) (*codebuild.StartBuildOutput, error) {
	vars := map[string]string{}
	for _, v := range in.EnvironmentVariablesOverride {
		vars[aws.ToString(v.Name)] = aws.ToString(v.Value)
	}
	action := vars["ACTION"]
	f.w.write(f.a.prefix + "codebuild:StartBuild " + action)
	b := &rwBuild{id: fmt.Sprintf("%s:b%d", f.a.name, len(f.a.started())+1), vars: vars, status: cbtypes.StatusTypeSucceeded, start: f.w.clock,
		until: f.w.clock.Add(f.a.runFor), log: []string{"dormeto: terraform " + action, "dormeto: done"}}
	if end, ok := f.a.ends[action]; ok {
		b.status = end
	}
	switch {
	case b.status == cbtypes.StatusTypeFailed:
		b.log = []string{"dormeto: terraform " + action, "Error: creating the user pool: AccessDenied"}
	case strings.HasSuffix(f.a.name, "-organization"):
		var roles strings.Builder
		b.log = []string{"dormeto: putting the delegation policy"}
		b.accounts = strings.Fields(f.a.objects["prod/organization/accounts.txt"])
		for _, account := range b.accounts {
			if e, failed := f.w.failing[account]; failed {
				roles.WriteString(`{"account":"` + account + `","ok":false,"error":"` + e + `"}` + "\n")
				b.log = append(b.log, "dormeto: roles of "+account+": failed")
			} else {
				roles.WriteString(`{"account":"` + account + `","ok":true}` + "\n")
				b.log = append(b.log, "dormeto: roles of "+account+": ok")
			}
		}
		b.log = append(b.log, "dormeto: done")
		f.a.objects["prod/organization/roles.json"] = roles.String()
	case b.status == cbtypes.StatusTypeSucceeded && action == "plan":
		f.a.objects["prod/plans/"+b.id+"/plan.json"] = f.w.plan
	case b.status == cbtypes.StatusTypeSucceeded && action == "plan-destroy":
		f.a.objects["prod/plans/"+b.id+"/plan.json"] = f.w.destroyPlan
	case b.status == cbtypes.StatusTypeSucceeded && action == "apply":
		f.a.objects["prod/applied.json"] = `{"version":"` + vars["RELEASE_VERSION"] + `","build":"` + b.id + `"}`
		f.a.objects["prod/outputs.json"] = rwOutputs
		f.a.objects["prod/manager.tfstate"] = rwState
	case b.status == cbtypes.StatusTypeSucceeded && action == "apply-destroy":
		delete(f.a.objects, "prod/applied.json")
		delete(f.a.objects, "prod/outputs.json")
		f.a.objects["prod/manager.tfstate"] = rwNoneState
	}
	f.a.builds = append(f.a.builds, b)
	if f.w.cancelOnStart {
		f.w.cancel()
	}
	return &codebuild.StartBuildOutput{Build: &cbtypes.Build{Id: aws.String(b.id)}}, nil
}

type rwLogs struct {
	Logs
	w *rwWorld
	a *rwAcct
}

func (f rwLogs) DescribeLogGroups(context.Context, *cloudwatchlogs.DescribeLogGroupsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
	if f.a.logGroup {
		return &cloudwatchlogs.DescribeLogGroupsOutput{LogGroups: []cwtypes.LogGroup{{LogGroupName: aws.String("/aws/codebuild/" + f.a.name)}}}, nil
	}
	return &cloudwatchlogs.DescribeLogGroupsOutput{}, nil
}

func (f rwLogs) CreateLogGroup(context.Context, *cloudwatchlogs.CreateLogGroupInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error) {
	f.w.write(f.a.prefix + "logs:CreateLogGroup")
	f.a.logGroup = true
	return &cloudwatchlogs.CreateLogGroupOutput{}, nil
}

func (f rwLogs) PutRetentionPolicy(context.Context, *cloudwatchlogs.PutRetentionPolicyInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutRetentionPolicyOutput, error) {
	f.w.write(f.a.prefix + "logs:PutRetentionPolicy")
	return &cloudwatchlogs.PutRetentionPolicyOutput{}, nil
}

func (f rwLogs) DeleteLogGroup(context.Context, *cloudwatchlogs.DeleteLogGroupInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error) {
	f.w.write(f.a.prefix + "logs:DeleteLogGroup")
	f.a.logGroup = false
	return &cloudwatchlogs.DeleteLogGroupOutput{}, nil
}

// GetLogEvents is the build's whole log in one page, then nothing more.
func (f rwLogs) GetLogEvents(_ context.Context, in *cloudwatchlogs.GetLogEventsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
	out := &cloudwatchlogs.GetLogEventsOutput{NextForwardToken: aws.String("end")}
	if b := f.a.build(aws.ToString(in.LogStreamName)); b != nil && in.NextToken == nil {
		for _, l := range b.log {
			out.Events = append(out.Events, cwtypes.OutputLogEvent{Message: aws.String(l), Timestamp: aws.Int64(b.start.UnixMilli())})
		}
	}
	return out, nil
}

// rwEC2 lists the account's regions, whatever the filter (the region check looks for its own).
type rwEC2 struct {
	EC2
	w *rwWorld
}

func (f rwEC2) DescribeRegions(_ context.Context, in *ec2.DescribeRegionsInput, _ ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error) {
	if !aws.ToBool(in.AllRegions) {
		f.w.t.Errorf("regions listed without AllRegions")
	}
	out := &ec2.DescribeRegionsOutput{}
	for _, r := range slices.Sorted(maps.Keys(f.w.regions)) {
		out.Regions = append(out.Regions, ec2types.Region{RegionName: aws.String(r), OptInStatus: aws.String(f.w.regions[r])})
	}
	return out, nil
}

// rwLambda is a region's functions, listed two a page; GetFunction finds none (chkLambda's).
type rwLambda struct {
	chkLambda
	w      *rwWorld
	region string
}

func (f rwLambda) ListFunctions(_ context.Context, in *lambda.ListFunctionsInput, _ ...func(*lambda.Options)) (*lambda.ListFunctionsOutput, error) {
	if f.w.lambdaDenied[f.region] {
		return nil, chkAPI("AccessDeniedException")
	}
	start := 0
	if m := aws.ToString(in.Marker); m != "" {
		fmt.Sscan(m, &start)
	} else {
		f.w.listed = append(f.w.listed, f.region)
	}
	names := f.w.functions[f.region]
	end := min(start+2, len(names))
	out := &lambda.ListFunctionsOutput{}
	for _, n := range names[start:end] {
		out.Functions = append(out.Functions, lambdatypes.FunctionConfiguration{FunctionName: aws.String(n)})
	}
	if end < len(names) {
		out.NextMarker = aws.String(fmt.Sprint(end))
	}
	return out, nil
}

// rwDDB is the manager's table, dmt-prod-manager, its router items queried.
type rwDDB struct {
	DynamoDB
	w *rwWorld
}

func (f rwDDB) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	pk, _ := in.ExpressionAttributeValues[":pk"].(*ddbtypes.AttributeValueMemberS)
	prefix, _ := in.ExpressionAttributeValues[":prefix"].(*ddbtypes.AttributeValueMemberS)
	if aws.ToString(in.TableName) != "dmt-prod-manager" || aws.ToString(in.KeyConditionExpression) != "pk = :pk AND begins_with(sk, :prefix)" ||
		pk == nil || pk.Value != "T#default" || prefix == nil || prefix.Value != "ROUTER#" || aws.ToInt32(in.Limit) != 1 {
		f.w.t.Errorf("query %s %q %v limit %d", aws.ToString(in.TableName), aws.ToString(in.KeyConditionExpression), in.ExpressionAttributeValues, aws.ToInt32(in.Limit))
	}
	if f.w.noTable {
		return nil, &ddbtypes.ResourceNotFoundException{Message: aws.String("Requested resource not found")}
	}
	out := &dynamodb.QueryOutput{}
	if f.w.routers > 0 {
		out.Items = []map[string]ddbtypes.AttributeValue{{"pk": &ddbtypes.AttributeValueMemberS{Value: "T#default"},
			"sk": &ddbtypes.AttributeValueMemberS{Value: "ROUTER#444444444444#eu-west-1"}}}
	}
	out.Count = int32(len(out.Items))
	return out, nil
}

type rwCognito struct{ Cognito }

func (rwCognito) ListUserPools(context.Context, *cognitoidentityprovider.ListUserPoolsInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.ListUserPoolsOutput, error) {
	return &cognitoidentityprovider.ListUserPoolsOutput{}, nil
}

func (rwCognito) ListUsers(context.Context, *cognitoidentityprovider.ListUsersInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.ListUsersOutput, error) {
	return &cognitoidentityprovider.ListUsersOutput{Users: []cogtypes.UserType{{UserStatus: cogtypes.UserStatusTypeForceChangePassword}}}, nil
}

// rwFlags is a request whose flags give every answer.
func rwFlags() Request {
	return Request{Profile: "p", Region: "eu-west-1",
		Flags: Settings{Env: "prod", Version: "0.11.0", Mode: "organization", AdminEmails: []string{"you@example.com"}},
		Set:   map[string]bool{"region": true, "env": true, "version": true, "mode": true, "admin-emails": true, "domain": true, "ses-identity": true, "email-from": true}}
}

// rwInOrder fails unless each part appears in out after the previous one.
func rwInOrder(t *testing.T, out string, parts ...string) {
	t.Helper()
	rest := out
	for _, p := range parts {
		i := strings.Index(rest, p)
		if i < 0 {
			t.Fatalf("%q missing, or out of order, in:\n%s", p, out)
		}
		rest = rest[i+len(p):]
	}
}

func rwEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}

func TestInstallGuidedFirstInstall(t *testing.T) {
	w := rwNew(t)
	w.region = "" // the profile names none: asked
	code, out, asked := w.run(Install, Request{Profile: "p"}, true, "", "", "", "you@example.com", "", "", "", "y")
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "questions", asked, []string{
		"Region [eu-west-1]",
		"Environment [prod]",
		"Manager release [0.11.0]",
		"Administrators (emails, comma-separated)",
		"Mode: organization (the manager reads every account of o-vvk0115ixn) or account [organization]",
		"Custom domain [none]",
		"SES identity [none]",
		"Apply this plan? [y/N]",
	})
	rwInOrder(t, out,
		"Profile p: account 532105290256 (sandbox) in the organization o-vvk0115ixn (management 809706483662)\n",
		"\nChecks\n",
		"  ✓ credentials: "+rwRole+" in 532105290256\n",
		"  ✓ existing install: none: a first install of env prod\n",
		"  ✓ release: manager 0.11.0\n",
		"  ✓ administrators: you@example.com\n",
		"  ! organization access: not deployed (no management profile)\n",
		"    Organization access needs the management account 809706483662; without its profile (--management-profile) it is not deployed.\n",
		"    The procedure: https://dormeto.com/organization-access.html\n",
		"    Check it later: dormeto install check --management-profile <profile>; the dashboard's Organization access panel too.\n",
		"\nBootstrap (API calls): bucket dmt-install-532105290256-eu-west-1, role dmt-prod-install, project dmt-prod-install\n",
		"Plan (build dmt-prod-install:b1…)  succeeded",
		"Plan (build dmt-prod-install:b1)\n",
		"  manager 0.11.0: 3 to add, 0 to change, 0 to destroy\n",
		"      1 aws_cloudfront_distribution\n",
		"Apply (build dmt-prod-install:b2…)  succeeded",
		"  ✓ applied: 3 added, 0 changed, 0 destroyed\n",
		"  ✓ dashboard: "+rwDashboard+" answers\n",
		"  ✓ administrators: invitation sent to you@example.com (valid 7 days)\n",
		"Next: sign in at "+rwDashboard+"; in Discovery, Perimeter settings, add a filter, then Run now.\n",
	)
	if strings.Contains(out, "destroys or replaces") || strings.Contains(out, "Continue despite") {
		t.Errorf("unexpected line in:\n%s", out)
	}
	b := w.started()
	rwEqual(t, "builds", w.actions(), []string{"plan", "apply"})
	base := ReleasesURL + "/0.11.0"
	rwEqual(t, "plan's variables", b[0].vars, map[string]string{"ACTION": "plan", "RELEASE_BASE": base, "RELEASE_VERSION": "0.11.0"})
	rwEqual(t, "apply's variables", b[1].vars, map[string]string{"ACTION": "apply", "PLAN_BUILD": "dmt-prod-install:b1", "RELEASE_BASE": base, "RELEASE_VERSION": "0.11.0"})

	s := w.saved()
	want := Settings{Env: "prod", Region: "eu-west-1", Version: "0.11.0", Mode: "organization", AdminEmails: []string{"you@example.com"},
		OrganizationID: "o-vvk0115ixn", ReleaseBase: base, LastPlan: "dmt-prod-install:b1", LastPlanKind: "apply"}
	rwEqual(t, "install.json", s, want)
	cfg := w.objects["prod/config/main.tf.json"]
	if !strings.Contains(cfg, `"discovery_mode": "organization"`) || !strings.Contains(cfg, `"you@example.com"`) {
		t.Errorf("main.tf.json:\n%s", cfg)
	}
	// The configuration and the answers are written before the plan starts, the plan's id once it has.
	at := func(call string, last bool) int {
		i := slices.Index(w.writes, call)
		if last {
			for j, c := range w.writes {
				if c == call {
					i = j
				}
			}
		}
		if i < 0 {
			t.Fatalf("no %s in %v", call, w.writes)
		}
		return i
	}
	if !(at("s3:PutObject prod/config/main.tf.json", false) < at("s3:PutObject prod/install.json", false) &&
		at("s3:PutObject prod/install.json", false) < at("codebuild:StartBuild plan", false) &&
		at("codebuild:StartBuild plan", false) < at("s3:PutObject prod/install.json", true) &&
		at("s3:PutObject prod/install.json", true) < at("codebuild:StartBuild apply", false)) {
		t.Errorf("writes out of order: %v", w.writes)
	}
	if w.objects["prod/cli.json"] != `{"version":"0.2.0"}` || at("events:PutTargets dmt-prod-install-requests", false) > at("s3:PutObject prod/cli.json", false) ||
		at("s3:PutObject prod/cli.json", false) > at("s3:PutObject prod/config/main.tf.json", false) {
		t.Errorf("cli.json = %q, writes %v", w.objects["prod/cli.json"], w.writes)
	}
	if at("iam:CreateRole dmt-prod-install", false) > at("codebuild:CreateProject", false) {
		t.Errorf("the project before its role: %v", w.writes)
	}
}

func TestInstallWithoutATerminalStopsAfterThePlan(t *testing.T) {
	w := rwNew(t)
	code, out, asked := w.run(Install, rwFlags(), false)
	if code != 0 || len(asked) != 0 {
		t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	if !strings.HasSuffix(out, "  manager 0.11.0: 3 to add, 0 to change, 0 to destroy\n      1 aws_cloudfront_distribution\n      1 aws_cognito_user_pool\n      1 aws_lambda_function\n"+
		"Plan saved. Apply it: dormeto install apply "+rwArgs+"\n") {
		t.Errorf("output:\n%s", out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan"})
	if w.saved().LastPlan != "dmt-prod-install:b1" {
		t.Errorf("install.json: %s", w.objects["prod/install.json"])
	}
}

func TestInstallWithoutATerminalAMissingAnswerNamesItsFlag(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(w *rwWorld, r *Request)
		flag string
	}{
		{"administrators", func(_ *rwWorld, r *Request) { delete(r.Set, "admin-emails"); r.Flags.AdminEmails = nil }, "--admin-emails"},
		{"region", func(w *rwWorld, r *Request) { w.region, r.Region = "", ""; delete(r.Set, "region") }, "--region"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := rwNew(t)
			r := rwFlags()
			c.edit(w, &r)
			code, out, _ := w.run(Install, r, false)
			if code != 2 || !strings.HasSuffix(out, c.flag+": needed\n") {
				t.Errorf("exit code %d:\n%s", code, out)
			}
			if len(w.writes) != 0 {
				t.Errorf("created: %v", w.writes)
			}
		})
	}
}

// Without a terminal, an answer with a default takes it (ruling R26): the latest version, the suggested mode (said), no
// domain, no SES identity, no sender, env prod.
func TestInstallWithoutATerminalTakesTheDefaults(t *testing.T) {
	r := Request{Profile: "p", Region: "eu-west-1", Flags: Settings{AdminEmails: []string{"you@example.com"}}, Set: map[string]bool{"admin-emails": true}}
	w := rwNew(t)
	code, out, asked := w.run(Install, r, false)
	if code != 0 || len(asked) != 0 {
		t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	rwInOrder(t, out,
		"Profile p: account 532105290256",
		"Mode: organization (the manager reads every account of o-vvk0115ixn)\n",
		"  ✓ release: manager 0.11.0\n",
		"Plan saved. Apply it: dormeto install apply "+rwArgs+"\n")
	rwEqual(t, "builds", w.actions(), []string{"plan"})
	if s := w.saved(); s.Env != "prod" || s.Version != "0.11.0" || s.Mode != "organization" || s.Domain != nil || s.SESIdentity != "" || s.EmailFrom != "" {
		t.Errorf("install.json: %+v", s)
	}

	// An SES identity without a sender: none.
	w = rwNew(t)
	r.Flags.SESIdentity, r.Set["ses-identity"] = "example.com", true
	code, out, _ = w.run(Install, r, false)
	if s := w.saved(); code != 0 || s.SESIdentity != "example.com" || s.EmailFrom != "" {
		t.Errorf("exit code %d, install.json %+v:\n%s", code, s, out)
	}
}

func TestInstallYesTakesTheSuggestionsAndNeverConfirms(t *testing.T) {
	r := Request{Profile: "p", Region: "eu-west-1", Yes: true, Flags: Settings{AdminEmails: []string{"you@example.com"}}, Set: map[string]bool{"admin-emails": true}}

	w := rwNew(t)
	code, out, asked := w.run(Install, r, false)
	if code != 0 || len(asked) != 0 || !strings.HasSuffix(out, "Plan saved. Apply it: dormeto install apply "+rwArgs+"\n") {
		t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan"})
	s := w.saved()
	if s.Version != "0.11.0" || s.Mode != "organization" || s.Domain != nil || s.SESIdentity != "" || s.Env != "prod" {
		t.Errorf("install.json: %+v", s)
	}

	// In a terminal, the plan is still asked about.
	w = rwNew(t)
	code, out, asked = w.run(Install, r, true, "n")
	rwEqual(t, "questions", asked, []string{"Apply this plan? [y/N]"})
	if code != 0 || !strings.HasSuffix(out, "Not applied; the plan is kept: dormeto install apply "+rwArgs+"\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan"})

	// No suggestion for the administrators.
	w = rwNew(t)
	r.Flags.AdminEmails, r.Set = nil, nil
	code, out, _ = w.run(Install, r, true)
	if code != 2 || !strings.HasSuffix(out, "--admin-emails: needed\n") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

func TestInstallAFailedCheckCreatesNothing(t *testing.T) {
	w := rwNew(t)
	w.bucket = true
	w.objects["dev/install.json"] = "{}"
	code, out, _ := w.run(Install, rwFlags(), true)
	if code != 1 {
		t.Errorf("exit code %d", code)
	}
	rwInOrder(t, out, "  ✗ another environment: one environment per account: dev installed here\n", "Nothing was created.\n")
	if len(w.writes) != 0 {
		t.Errorf("created: %v", w.writes)
	}
}

func TestInstallWarningsAreAskedAboutOnce(t *testing.T) {
	w := rwNew(t)
	w.simDenied = true
	code, out, asked := w.run(Install, rwFlags(), true, "n")
	rwEqual(t, "questions", asked, []string{"Continue despite the warnings? [y/N]"})
	if code != 1 || !strings.Contains(out, "  ! administrator rights: not checked (iam:SimulatePrincipalPolicy refused)\n") || !strings.HasSuffix(out, "Nothing was created.\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	if len(w.writes) != 0 {
		t.Errorf("created: %v", w.writes)
	}

	// --yes, or no terminal: the warnings do not stop the install.
	for _, terminal := range []bool{true, false} {
		w = rwNew(t)
		w.simDenied = true
		r := rwFlags()
		r.Yes = terminal
		var answers []string
		if terminal {
			answers = []string{"n"}
		}
		code, _, asked := w.run(Install, r, terminal, answers...)
		if code != 0 || slices.Contains(asked, "Continue despite the warnings? [y/N]") {
			t.Errorf("terminal %v: exit code %d, asked %q", terminal, code, asked)
		}
		rwEqual(t, "builds", w.actions(), []string{"plan"})
	}
}

func TestInstallNoAtTheConfirmationKeepsThePlan(t *testing.T) {
	w := rwNew(t)
	code, out, asked := w.run(Install, rwFlags(), true, "n")
	rwEqual(t, "questions", asked, []string{"Apply this plan? [y/N]"})
	if code != 0 || !strings.HasSuffix(out, "Not applied; the plan is kept: dormeto install apply "+rwArgs+"\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan"})
	if w.saved().LastPlan != "dmt-prod-install:b1" {
		t.Errorf("install.json: %s", w.objects["prod/install.json"])
	}
}

func TestInstallUpgradeAsksNothing(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	w.release("0.12.0", true)
	w.plan = rwReplace
	code, out, asked := w.run(Install, Request{Profile: "p", Region: "eu-west-1"}, true, "y")
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "questions", asked, []string{"Apply this plan? [y/N]"})
	rwInOrder(t, out,
		"  ✓ existing install: upgrade: 0.11.0 → 0.12.0\n",
		"  ✓ another install: none\n",
		"  ✓ release: manager 0.12.0\n",
		"  manager 0.12.0: 1 to add, 1 to change, 1 to destroy\n",
		"    replaced: module.dormeto.aws_dynamodb_table.events\n",
		"  ! this plan destroys or replaces resources (above)\n",
		"  ✓ applied: 1 added, 1 changed, 1 destroyed\n",
		"Next: sign in at ",
	)
	b := w.started()
	rwEqual(t, "builds", w.actions(), []string{"plan", "apply"})
	if b[0].vars["RELEASE_VERSION"] != "0.12.0" || b[1].vars["PLAN_BUILD"] != b[0].id || b[1].vars["RELEASE_BASE"] != ReleasesURL+"/0.12.0" {
		t.Errorf("builds' variables: %v, %v", b[0].vars, b[1].vars)
	}
	for _, c := range w.writes {
		if c == "iam:CreateRole dmt-prod-install" || c == "s3:CreateBucket" || c == "codebuild:CreateProject" {
			t.Errorf("the bootstrap created %s again", c)
		}
	}
	if s := w.saved(); s.Version != "0.12.0" || s.AdminEmails[0] != "you@example.com" {
		t.Errorf("install.json: %+v", s)
	}
}

func TestInstallNothingToChange(t *testing.T) {
	w := rwNew(t)
	w.plan = rwEmpty
	code, out, asked := w.run(Install, rwFlags(), true)
	if code != 0 || len(asked) != 0 || !strings.HasSuffix(out, "Nothing to change: manager 0.11.0 is installed.\n") {
		t.Errorf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan"})
}

func TestInstallARunningBuildStopsThePlan(t *testing.T) {
	w := rwNew(t)
	w.builds = []*rwBuild{{id: "dmt-prod-install:r0", status: cbtypes.StatusTypeInProgress, start: rwT0.Add(-time.Minute)}}
	code, out, _ := w.run(Install, rwFlags(), true)
	if code != 1 || !strings.HasSuffix(out, "build dmt-prod-install:r0 is running: dormeto install status follows it\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	if len(w.actions()) != 0 {
		t.Errorf("started %v", w.actions())
	}
}

func TestInstallAFailedPlan(t *testing.T) {
	w := rwNew(t)
	w.ends["plan"] = cbtypes.StatusTypeFailed
	code, out, asked := w.run(Install, rwFlags(), true)
	if code != 1 || len(asked) != 0 {
		t.Errorf("exit code %d, asked %q", code, asked)
	}
	rwInOrder(t, out, "Plan failed\n", "  Error: creating the user pool: AccessDenied\n", ConsoleURL(Names(rwAccount, "eu-west-1", "prod"), "dmt-prod-install:b1"))
	rwEqual(t, "builds", w.actions(), []string{"plan"})
}

func TestInstallCtrlCAtTheConfirmationAppliesNothing(t *testing.T) {
	w := rwNew(t)
	code, out, _ := w.run(Install, rwFlags(), true, "^C")
	if code != 130 || !strings.HasSuffix(out, "      1 aws_lambda_function\n\nNot applied; the plan is kept: dormeto install apply "+rwArgs+"\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan"})
}

func TestInstallCtrlCAtAQuestionCreatesNothing(t *testing.T) {
	w := rwNew(t)
	w.region = ""
	code, out, _ := w.run(Install, Request{Profile: "p"}, true, "^C")
	if code != 130 || out != "\nInterrupted: nothing was created.\n" || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

func TestInstallCtrlCWhileABuildRunsDetaches(t *testing.T) {
	w := rwNew(t)
	w.ends["plan"] = cbtypes.StatusTypeInProgress
	w.detach = true
	code, out, _ := w.run(Install, rwFlags(), true)
	if code != 130 || !strings.HasSuffix(out, "Detached: the plan goes on in CodeBuild. Follow it: dormeto install status "+rwArgs+
		"; apply it once done: dormeto install apply "+rwArgs+"\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}

	w = rwNew(t)
	w.ends["apply"] = cbtypes.StatusTypeInProgress
	w.detach = true
	code, out, _ = w.run(Install, rwFlags(), true, "y")
	if code != 130 || !strings.HasSuffix(out, "Detached: the apply goes on in CodeBuild. Follow it: dormeto install status "+rwArgs+"\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan", "apply"})
}

func TestInstallDomainAndSESQuestions(t *testing.T) {
	w := rwNew(t)
	r := rwFlags()
	delete(r.Set, "domain")
	delete(r.Set, "ses-identity")
	delete(r.Set, "email-from")
	code, out, asked := w.run(Install, r, true, "dormeto.example.com", "arn:aws:acm:us-east-1:532105290256:certificate/x", "Z1", "example.com", "dmt@example.com", "n")
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "questions", asked, []string{
		"Custom domain [none]", "Certificate ARN (us-east-1)", "Route 53 zone id", "SES identity [none]", "Sender (email_from) [none]", "Apply this plan? [y/N]",
	})
	s := w.saved()
	rwEqual(t, "domain", s.Domain, &Domain{Dashboard: "dormeto.example.com", CertificateARN: "arn:aws:acm:us-east-1:532105290256:certificate/x", ZoneID: "Z1"})
	if s.SESIdentity != "example.com" || s.EmailFrom != "dmt@example.com" {
		t.Errorf("install.json: %+v", s)
	}
	rwInOrder(t, out, "  ✓ domain: dormeto.example.com, auth.dormeto.example.com: certificate and zone\n", "  ✓ SES: example.com verified for sending\n")
}

func TestInstallABadAnswerNamesItsFlag(t *testing.T) {
	w := rwNew(t)
	r := rwFlags()
	delete(r.Set, "admin-emails")
	code, out, _ := w.run(Install, r, true, "not-an-email")
	if code != 2 || !strings.HasSuffix(out, `--admin-emails: "not-an-email" is not an email address`+"\n") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

func TestInstallOutsideAnOrganizationIsAnAccountDeployment(t *testing.T) {
	w := rwNew(t)
	w.org = nil
	r := rwFlags()
	delete(r.Set, "mode")
	code, out, asked := w.run(Install, r, true, "n")
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "questions", asked, []string{"Apply this plan? [y/N]"})
	if s := w.saved(); s.Mode != "account" || s.OrganizationID != "" {
		t.Errorf("install.json: %+v", s)
	}
	rwInOrder(t, out, "Profile p: account 532105290256 (sandbox), not in an organization\n", "  ✓ organization: account deployment\n")
	if strings.Contains(out, "organization access") {
		t.Errorf("organization access in an account deployment:\n%s", out)
	}
}

// rwPlanned is prod installed at 0.11.0, then planned for 0.12.0 without a terminal: the plan p1, after the last apply.
func rwPlanned(t *testing.T) *rwWorld {
	w := rwNew(t)
	w.installed("0.11.0")
	w.release("0.12.0", true)
	s := w.saved()
	s.Version, s.ReleaseBase, s.LastPlan = "0.12.0", ReleasesURL+"/0.12.0", "dmt-prod-install:p1"
	w.save(s)
	w.objects["prod/plans/dmt-prod-install:p1/plan.json"] = rwPlan
	w.builds = append(w.builds, &rwBuild{id: "dmt-prod-install:p1", env: map[string]string{"ACTION": "plan"}, status: cbtypes.StatusTypeSucceeded, start: rwT0.Add(-10 * time.Minute)})
	return w
}

func TestApplyWithoutAnInstall(t *testing.T) {
	w := rwNew(t)
	code, out, _ := w.run(Apply, Request{Profile: "p", Region: "eu-west-1"}, true)
	if code != 1 || !strings.HasSuffix(out, "no install of env prod in 532105290256 eu-west-1: run dormeto install\n") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

func TestApplyWithoutAPlan(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	s := w.saved()
	s.LastPlan = ""
	w.save(s)
	code, out, _ := w.run(Apply, Request{Profile: "p", Region: "eu-west-1"}, false)
	if code != 1 || !strings.HasSuffix(out, "no plan to apply: run dormeto install\n") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

func TestApplyRefusesAStalePlan(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0") // its plan p0 predates its apply a0
	code, out, _ := w.run(Apply, Request{Profile: "p", Region: "eu-west-1"}, false)
	if code != 1 || !strings.HasSuffix(out, "the plan dmt-prod-install:p0 predates the last apply dmt-prod-install:a0: plan again\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	if len(w.actions()) != 0 {
		t.Errorf("started %v", w.actions())
	}
}

func TestApplyShowsThePlanThenAppliesIt(t *testing.T) {
	w := rwPlanned(t)
	code, out, asked := w.run(Apply, Request{Profile: "p", Region: "eu-west-1"}, false)
	if code != 0 || len(asked) != 0 {
		t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	rwInOrder(t, out,
		"Profile p: account 532105290256",
		"Plan (build dmt-prod-install:p1)\n  manager 0.12.0: 3 to add, 0 to change, 0 to destroy\n",
		"Apply (build dmt-prod-install:b1…)  succeeded",
		"  ✓ applied: 3 added, 0 changed, 0 destroyed\n",
		"  ✓ dashboard: "+rwDashboard+" answers\n",
		"Next: sign in at "+rwDashboard+"; in Discovery, Perimeter settings, add a filter, then Run now.\n",
	)
	rwEqual(t, "builds", w.actions(), []string{"apply"})
	rwEqual(t, "apply's variables", w.started()[0].vars,
		map[string]string{"ACTION": "apply", "PLAN_BUILD": "dmt-prod-install:p1", "RELEASE_BASE": ReleasesURL + "/0.12.0", "RELEASE_VERSION": "0.12.0"})
	for _, c := range w.writes {
		if c != "codebuild:StartBuild apply" {
			t.Errorf("apply wrote %s", c)
		}
	}
}

func TestApplyAFailedApply(t *testing.T) {
	w := rwPlanned(t)
	w.ends["apply"] = cbtypes.StatusTypeFailed
	code, out, _ := w.run(Apply, Request{Profile: "p", Region: "eu-west-1"}, false)
	if code != 1 {
		t.Errorf("exit code %d", code)
	}
	rwInOrder(t, out, "Apply failed\n", "  Error: creating the user pool: AccessDenied\n", ConsoleURL(Names(rwAccount, "eu-west-1", "prod"), "dmt-prod-install:b1"))
	if strings.Contains(out, "applied:") || strings.Contains(out, "Next:") {
		t.Errorf("a failed apply reported applied:\n%s", out)
	}
}

func TestCheckIsReadOnly(t *testing.T) {
	w := rwNew(t)
	w.readOnly = true
	r := Request{Profile: "p", Region: "eu-west-1", Flags: Settings{AdminEmails: []string{"you@example.com"}}, Set: map[string]bool{"admin-emails": true}}
	code, out, asked := w.run(InstallCheck, r, true)
	if code != 0 || len(asked) != 0 {
		t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	rwInOrder(t, out,
		"Profile p: account 532105290256 (sandbox) in the organization o-vvk0115ixn (management 809706483662)\n",
		"\nChecks\n",
		"  ✓ organization: o-vvk0115ixn: all features\n",
		"  ✓ existing install: none: a first install of env prod\n",
		"  ✓ release: manager 0.11.0\n",
		"  ✓ administrators: you@example.com\n",
		"  ! organization access: not deployed (no management profile)\n",
		"    Organization access needs the management account 809706483662; without its profile (--management-profile) it is not deployed.\n",
		"    Check it later: dormeto install check --management-profile <profile>; the dashboard's Organization access panel too.\n",
	)
	if len(w.writes) != 0 {
		t.Errorf("writes: %v", w.writes)
	}
}

func TestCheckWithoutAdministratorsYet(t *testing.T) {
	w := rwNew(t)
	w.readOnly = true
	code, out, asked := w.run(InstallCheck, Request{Profile: "p"}, true)
	if code != 0 || len(asked) != 0 {
		t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	if !strings.Contains(out, "  ! administrators: none given yet (--admin-emails)\n") {
		t.Errorf("output:\n%s", out)
	}
}

func TestCheckAFailedRow(t *testing.T) {
	w := rwNew(t)
	w.readOnly = true
	w.bucket = true
	w.objects["dev/install.json"] = "{}"
	code, out, _ := w.run(InstallCheck, rwFlags(), false)
	if code != 1 || !strings.Contains(out, "  ✗ another environment: one environment per account: dev installed here\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

func TestCheckAccountMode(t *testing.T) {
	w := rwNew(t)
	w.readOnly = true
	r := rwFlags()
	r.Flags.Mode = "account"
	code, out, _ := w.run(InstallCheck, r, false)
	if code != 0 || !strings.Contains(out, "  ✓ organization: account deployment (in o-vvk0115ixn)\n") || strings.Contains(out, "organization access") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

func TestCheckAnUpgradeComparesWithTheLatest(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	w.release("0.12.0", true)
	w.readOnly = true
	code, out, _ := w.run(InstallCheck, Request{Profile: "p", Region: "eu-west-1"}, false)
	if code != 0 || !strings.Contains(out, "  ✓ existing install: upgrade: 0.11.0 → 0.12.0\n") || !strings.Contains(out, "  ✓ administrators: you@example.com\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

func TestAnExpiredSSOSessionSaysHowToSignIn(t *testing.T) {
	w := rwNew(t)
	w.identityErr = fmt.Errorf("get identity: get credentials: %w", &ssocreds.InvalidTokenError{Err: errors.New("the SSO session has expired or is invalid")})
	code, out, _ := w.run(InstallCheck, rwFlags(), false)
	if code != 1 || !strings.HasSuffix(out, "the SSO session has expired or is invalid\nRun aws sso login --profile p and try again.\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}

	w = rwNew(t)
	w.identityErr = chkAPI("AccessDenied")
	code, out, _ = w.run(Install, rwFlags(), false)
	if code != 1 || strings.Contains(out, "sso login") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

// A new plan replaces the last one in install.json before anything can stop the run: a plan of the old settings is never
// left next to the new release.
func TestInstallForgetsTheLastPlanBeforeANewOne(t *testing.T) {
	w := rwPlanned(t) // the plan p1 of 0.12.0, pending
	w.release("0.13.0", true)
	w.builds = append(w.builds, &rwBuild{id: "dmt-prod-install:r0", status: cbtypes.StatusTypeInProgress, start: rwT0.Add(-time.Minute)})
	code, out, _ := w.run(Install, Request{Profile: "p", Region: "eu-west-1"}, true)
	if code != 1 || !strings.HasSuffix(out, "build dmt-prod-install:r0 is running: dormeto install status follows it\n") {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	if s := w.saved(); s.LastPlan != "" || s.Version != "0.13.0" {
		t.Errorf("install.json: %+v", s)
	}
	code, out, _ = w.run(Apply, Request{Profile: "p", Region: "eu-west-1"}, false)
	if code != 1 || !strings.HasSuffix(out, "no plan to apply: run dormeto install\n") || len(w.actions()) != 0 {
		t.Errorf("exit code %d, started %v:\n%s", code, w.actions(), out)
	}
}

// Ctrl-C as the plan starts: its id is kept all the same, for install status and install apply.
func TestInstallKeepsTheStartedPlanOnCtrlC(t *testing.T) {
	w := rwNew(t)
	w.cancelOnStart = true
	code, out, _ := w.run(Install, rwFlags(), true)
	if code != 130 || !strings.HasSuffix(out, "dormeto install status "+rwArgs+"; apply it once done: dormeto install apply "+rwArgs+"\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	if s := w.saved(); s.LastPlan != "dmt-prod-install:b1" {
		t.Errorf("install.json: %+v", s)
	}
}

// Following fails for a reason of AWS's (not expired credentials): the build goes on, and the commands that follow and
// apply it are named.
func TestInstallAFollowErrorSaysTheBuildGoesOn(t *testing.T) {
	w := rwNew(t)
	w.followErr["plan"] = chkAPI("ThrottlingException")
	code, out, _ := w.run(Install, rwFlags(), true)
	if code != 1 {
		t.Errorf("exit code %d", code)
	}
	rwInOrder(t, out, "follow build dmt-prod-install:b1: api error ThrottlingException: ThrottlingException message\n",
		"The plan goes on in CodeBuild. Follow it: dormeto install status "+rwArgs+"; apply it once done: dormeto install apply "+rwArgs+"\n")

	w = rwNew(t)
	w.followErr["apply"] = chkAPI("ThrottlingException")
	code, out, _ = w.run(Install, rwFlags(), true, "y")
	if code != 1 || !strings.HasSuffix(out, "The apply goes on in CodeBuild. Follow it: dormeto install status "+rwArgs+"\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

func TestInstallALocalRelease(t *testing.T) {
	dir := writeLocal(t, "0.11.0-dev", nil)
	w := rwNew(t)
	r := rwFlags()
	delete(r.Set, "version")
	r.Flags.Version, r.Release = "", dir
	code, out, _ := w.run(Install, r, false)
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	base := "s3://dmt-install-532105290256-eu-west-1/prod/release"
	if !strings.Contains(out, "  ✓ release: manager 0.11.0-dev (local: "+dir+")\n") ||
		!strings.HasSuffix(out, "Plan saved. Apply it: dormeto install apply "+rwArgs+" --release "+dir+"\n") {
		t.Errorf("output:\n%s", out)
	}
	for _, f := range []string{"SHA256SUMS", "terraform-aws-dormeto-0.11.0-dev.zip", "dormeto-organization-policy.json"} {
		if !slices.Contains(w.writes, "s3:PutObject prod/release/"+f) {
			t.Errorf("%s not uploaded: %v", f, w.writes)
		}
	}
	if b := w.started(); len(b) != 1 || b[0].vars["RELEASE_BASE"] != base || b[0].vars["RELEASE_VERSION"] != "0.11.0-dev" {
		t.Errorf("builds %v", w.actions())
	}
	if s := w.saved(); s.ReleaseBase != base || s.Version != "0.11.0-dev" {
		t.Errorf("install.json: %+v", s)
	}
}

func TestInstallAnEmptyAnswerIsAskedAgain(t *testing.T) {
	r := rwFlags()
	delete(r.Set, "admin-emails")

	w := rwNew(t)
	code, out, asked := w.run(Install, r, true, "", "", "you@example.com", "n")
	rwEqual(t, "questions", asked, []string{"Administrators (emails, comma-separated)", "Administrators (emails, comma-separated)",
		"Administrators (emails, comma-separated)", "Apply this plan? [y/N]"})
	if code != 0 || w.saved().AdminEmails[0] != "you@example.com" {
		t.Errorf("exit code %d:\n%s", code, out)
	}

	// Three empty answers, or the end of the input at once: the flag is needed.
	for _, answers := range [][]string{{"", "", ""}, {"^D"}} {
		w = rwNew(t)
		code, out, asked = w.run(Install, r, true, answers...)
		if code != 2 || len(asked) != len(answers) || !strings.HasSuffix(out, "--admin-emails: needed\n") || len(w.writes) != 0 {
			t.Errorf("%q: exit code %d, asked %q:\n%s", answers, code, asked, out)
		}
	}
}

// The configuration is generated with the resolved version, not the one the command line gave: the install input
// (manager 0.12.0 or later) is in what the plan runs when the version came from the latest release or a local one.
func TestInstallConfigFollowsTheResolvedVersion(t *testing.T) {
	hasInstall := func(w *rwWorld) bool { return strings.Contains(w.objects["prod/config/main.tf.json"], `"install": {`) }

	w := rwNew(t)
	w.installed("0.11.0")
	w.release("0.12.0", true)
	if code, out, _ := w.run(Install, Request{Profile: "p", Region: "eu-west-1"}, true, "y"); code != 0 {
		t.Fatalf("upgrade: exit code %d:\n%s", code, out)
	}
	if !hasInstall(w) {
		t.Errorf("upgrade to 0.12.0 without --version: no install input:\n%s", w.objects["prod/config/main.tf.json"])
	}

	dir := writeLocal(t, "0.12.0-dev.1", nil)
	w = rwNew(t)
	r := rwFlags()
	delete(r.Set, "version")
	r.Flags.Version, r.Release = "", dir
	if code, out, _ := w.run(Install, r, false); code != 0 {
		t.Fatalf("local release: exit code %d:\n%s", code, out)
	}
	if !hasInstall(w) {
		t.Errorf("local 0.12.0-dev.1: no install input:\n%s", w.objects["prod/config/main.tf.json"])
	}

	w = rwNew(t)
	w.installed("0.11.0")
	if code, out, _ := w.run(Install, Request{Profile: "p", Region: "eu-west-1"}, true, "y"); code != 0 {
		t.Fatalf("0.11.0: exit code %d:\n%s", code, out)
	}
	if hasInstall(w) {
		t.Errorf("0.11.0 got the install input:\n%s", w.objects["prod/config/main.tf.json"])
	}
}
