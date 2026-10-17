package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const (
	orgMgmt    = "111111111111"
	orgManager = "333333333333"
	orgMine    = "arn:aws:iam::333333333333:role/dmt-prd-manager-discovery"
	orgMineOps = "arn:aws:iam::333333333333:role/dmt-prd-manager-operations"
	orgDev     = "arn:aws:iam::121014831971:role/dmt-dev-manager-discovery"
)

// orgAcct is what an account of the fake organization holds.
type orgAcct struct {
	status    orgtypes.AccountStatus
	discovery []string // the trust of DmtDiscoveryRole; nil: no such role
	command   []string
	boundary  bool
	unreach   string // the assume error's message
	iamErr    error
}

type orgWorld struct {
	caller   string
	accounts map[string]orgAcct
	policy   *string // nil: ResourcePolicyNotFoundException
	polErr   error
	listErr  error
	calls    []string
	assumed  []string
}

type orgSTS struct {
	STS
	w *orgWorld
}

func (f orgSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	f.w.calls = append(f.w.calls, "sts.GetCallerIdentity")
	return &sts.GetCallerIdentityOutput{Account: aws.String(f.w.caller)}, nil
}

type orgOrgs struct {
	Organizations
	w *orgWorld
}

func (f orgOrgs) ListAccounts(_ context.Context, in *organizations.ListAccountsInput, _ ...func(*organizations.Options)) (*organizations.ListAccountsOutput, error) {
	f.w.calls = append(f.w.calls, "organizations.ListAccounts")
	if f.w.listErr != nil {
		return nil, f.w.listErr
	}
	ids := make([]string, 0, len(f.w.accounts))
	for id := range f.w.accounts {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var accts []orgtypes.Account
	for _, id := range ids {
		accts = append(accts, orgtypes.Account{Id: aws.String(id), Status: f.w.accounts[id].status})
	}
	// two pages, the first of three accounts
	if in.NextToken == nil && len(accts) > 3 {
		return &organizations.ListAccountsOutput{Accounts: accts[:3], NextToken: aws.String("p2")}, nil
	}
	if in.NextToken != nil {
		return &organizations.ListAccountsOutput{Accounts: accts[3:]}, nil
	}
	return &organizations.ListAccountsOutput{Accounts: accts}, nil
}

func (f orgOrgs) DescribeResourcePolicy(context.Context, *organizations.DescribeResourcePolicyInput, ...func(*organizations.Options)) (*organizations.DescribeResourcePolicyOutput, error) {
	f.w.calls = append(f.w.calls, "organizations.DescribeResourcePolicy")
	if f.w.polErr != nil {
		return nil, f.w.polErr
	}
	if f.w.policy == nil {
		return nil, &orgtypes.ResourcePolicyNotFoundException{Message: aws.String("none")}
	}
	return &organizations.DescribeResourcePolicyOutput{ResourcePolicy: &orgtypes.ResourcePolicy{Content: f.w.policy}}, nil
}

type orgIAM struct {
	IAM
	a orgAcct
	w *orgWorld
}

func (f orgIAM) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	if f.a.iamErr != nil {
		return nil, f.a.iamErr
	}
	var trust []string
	switch aws.ToString(in.RoleName) {
	case "DmtDiscoveryRole":
		trust = f.a.discovery
	case "DmtCommandRole":
		trust = f.a.command
	}
	if trust == nil {
		return nil, chkNoEntity()
	}
	return chkTrust(trust...), nil
}

func (f orgIAM) GetPolicy(_ context.Context, in *iam.GetPolicyInput, _ ...func(*iam.Options)) (*iam.GetPolicyOutput, error) {
	if !f.a.boundary {
		return nil, chkNoEntity()
	}
	return &iam.GetPolicyOutput{Policy: &iamtypes.Policy{Arn: in.PolicyArn}}, nil
}

func (w *orgWorld) assume(account string) (Clients, error) {
	w.assumed = append(w.assumed, account)
	a := w.accounts[account]
	if a.unreach != "" {
		return Clients{}, errors.New(a.unreach)
	}
	return Clients{IAM: orgIAM{a: a, w: w}}, nil
}

func (w *orgWorld) mgmt() Clients { return Clients{STS: orgSTS{w: w}, Organizations: orgOrgs{w: w}} }

func orgNew() *orgWorld {
	return &orgWorld{caller: orgMgmt, accounts: map[string]orgAcct{
		orgMgmt:        {status: orgtypes.AccountStatusActive},
		orgManager:     {status: orgtypes.AccountStatusActive},
		"444444444444": {status: orgtypes.AccountStatusActive, discovery: []string{orgMine}, command: []string{orgMineOps}, boundary: true},
		"555555555555": {status: orgtypes.AccountStatusActive},
		"666666666666": {status: orgtypes.AccountStatusActive, unreach: "operation error STS: AssumeRole, https response error StatusCode: 403\nsecond line"},
		"777777777777": {status: orgtypes.AccountStatusActive, discovery: []string{orgDev}, boundary: true},
		"888888888888": {status: orgtypes.AccountStatusActive, boundary: true},
		"999999999999": {status: orgtypes.AccountStatusSuspended},
	}}
}

var (
	orgWhere    = Where{Account: orgManager, Region: "eu-west-1", Organization: &Organization{ID: "o-abc", Management: orgMgmt, AllFeatures: true}}
	orgSettings = Settings{Env: "prd", Region: "eu-west-1", MemberRole: "OrganizationAccountAccessRole"}
)

const orgTemplate = `{"Version":"2012-10-17","Statement":[{"Sid":"DormetoReadOrganization","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::MANAGER_ACCOUNT:role/dmt-ENV-manager-discovery"},"Action":["organizations:DescribeOrganization","organizations:ListAccounts"],"Resource":"*"}]}`

func (w *orgWorld) survey(t *testing.T, s Settings) (Access, []Check) {
	t.Helper()
	a, cs, err := Survey(context.Background(), w.mgmt(), w.assume, s, orgWhere, []byte(orgTemplate))
	if err != nil {
		t.Fatal(err)
	}
	return a, cs
}

func orgRow(t *testing.T, cs []Check, name string) Check {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", name, cs)
	return Check{}
}

func orgWant(t *testing.T, cs []Check, name string, v Verdict, detail string) {
	t.Helper()
	if c := orgRow(t, cs, name); c.Verdict != v || c.Detail != detail {
		t.Errorf("%s = %v %q, want %v %q", name, c.Verdict, c.Detail, v, detail)
	}
}

func orgEq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func TestSurveyOtherAccountThanManagement(t *testing.T) {
	w := orgNew()
	w.caller = "222222222222"
	a, cs := w.survey(t, orgSettings)
	orgWant(t, cs, "management account", Fail, "the profile reaches 222222222222, not the management account 111111111111")
	if len(cs) != 1 || a.Management != "" || len(a.Accounts()) != 0 {
		t.Errorf("a = %+v, cs = %+v", a, cs)
	}
	if !slices.Equal(w.calls, []string{"sts.GetCallerIdentity"}) || len(w.assumed) != 0 {
		t.Errorf("calls = %v, assumed %v", w.calls, w.assumed)
	}
}

func TestSurveyClassifiesAccounts(t *testing.T) {
	w := orgNew()
	a, cs := w.survey(t, orgSettings)
	orgEq(t, "Ready", a.Ready, []string{"444444444444"})
	orgEq(t, "Missing", a.Missing, []string{"555555555555"})
	orgEq(t, "Unreachable", a.Unreachable, []string{"666666666666"})
	orgEq(t, "Foreign", a.Foreign, []string{"777777777777", "888888888888"})
	orgEq(t, "Accounts", a.Accounts(), []string{"444444444444", "555555555555"})
	if a.Management != orgMgmt {
		t.Errorf("Management = %q", a.Management)
	}
	if n := a.Notes["666666666666"]; n != "operation error STS: AssumeRole, https response error StatusCode: 403" {
		t.Errorf("unreachable note = %q", n)
	}
	if n := a.Notes["777777777777"]; !strings.Contains(n, orgDev) {
		t.Errorf("foreign note = %q", n)
	}
	if n := a.Notes["888888888888"]; !strings.Contains(n, "DmtRouterBoundary") {
		t.Errorf("boundary note = %q", n)
	}
	if slices.Contains(w.assumed, orgMgmt) || slices.Contains(w.assumed, orgManager) || slices.Contains(w.assumed, "999999999999") {
		t.Errorf("assumed = %v", w.assumed)
	}
	orgWant(t, cs, "management account", OK, orgMgmt)
	orgWant(t, cs, "member role", Warn, "OrganizationAccountAccessRole: assumed in 4 of 5 accounts; unreachable: 666666666666 (the procedure for them: https://dormeto.com/organization-access.html)")
	orgWant(t, cs, "member accounts", Warn, "1 ready, 1 to create, 0 discovery only; another manager's roles, left out: 777777777777, 888888888888")
}

func TestSurveyRoleTrustingThisAndAnotherManagerIsForeign(t *testing.T) {
	w := orgNew()
	w.accounts["444444444444"] = orgAcct{status: orgtypes.AccountStatusActive, discovery: []string{orgMine, orgDev}}
	a, _ := w.survey(t, orgSettings)
	orgEq(t, "Foreign", a.Foreign, []string{"444444444444", "777777777777", "888888888888"})
}

func TestSurveyAccountsExcludeAndDiscoveryOnly(t *testing.T) {
	w := orgNew()
	s := orgSettings
	s.Accounts = []string{"444444444444", "555555555555", "666666666666", "123123123123"}
	s.ExcludeAccounts = []string{"666666666666"}
	s.DiscoveryOnly = []string{"555555555555"}
	a, cs := w.survey(t, s)
	orgEq(t, "Ready", a.Ready, []string{"444444444444"})
	orgEq(t, "Missing", a.Missing, []string{"555555555555"})
	orgEq(t, "DiscoveryOnly", a.DiscoveryOnly, []string{"555555555555"})
	orgEq(t, "Unreachable", a.Unreachable, nil)
	orgEq(t, "Foreign", a.Foreign, nil)
	orgWant(t, cs, "organization accounts", Warn, "not in the organization: 123123123123")
	orgWant(t, cs, "member role", OK, "OrganizationAccountAccessRole: assumed in 2 of 2 accounts")
	orgWant(t, cs, "member accounts", OK, "1 ready, 1 to create, 1 discovery only")
}

func TestSurveyRoleNameVars(t *testing.T) {
	w := orgNew()
	w.accounts["444444444444"] = orgAcct{status: orgtypes.AccountStatusActive}
	s := orgSettings
	s.Vars = map[string]json.RawMessage{"discovery_role_name": json.RawMessage(`"/p/MyDisc"`), "command_role_name": json.RawMessage(`"MyCmd"`)}
	// the fake only knows the default names, so an account with the defaults is Missing under custom names
	w.accounts["555555555555"] = orgAcct{status: orgtypes.AccountStatusActive, discovery: []string{orgMine}}
	a, _ := w.survey(t, s)
	orgEq(t, "Missing", a.Missing, []string{"444444444444", "555555555555"})
}

func TestSurveyNoneReachable(t *testing.T) {
	w := orgNew()
	for id, a := range w.accounts {
		if a.status == orgtypes.AccountStatusActive && id != orgMgmt && id != orgManager {
			a.unreach = "denied"
			a.discovery, a.command, a.boundary = nil, nil, false
			w.accounts[id] = a
		}
	}
	a, cs := w.survey(t, orgSettings)
	if len(a.Accounts()) != 0 || len(a.Ready)+len(a.Foreign) != 0 {
		t.Errorf("a = %+v", a)
	}
	c := orgRow(t, cs, "member role")
	if c.Verdict != Fail || !strings.HasPrefix(c.Detail, "OrganizationAccountAccessRole: assumed in 0 of 5 accounts") {
		t.Errorf("member role = %+v", c)
	}
}

func TestSurveyAllReachable(t *testing.T) {
	w := orgNew()
	delete(w.accounts, "666666666666")
	delete(w.accounts, "777777777777")
	delete(w.accounts, "888888888888")
	_, cs := w.survey(t, orgSettings)
	orgWant(t, cs, "member role", OK, "OrganizationAccountAccessRole: assumed in 2 of 2 accounts")
	orgWant(t, cs, "member accounts", OK, "1 ready, 1 to create, 0 discovery only")
}

func TestSurveyListAccountsRefused(t *testing.T) {
	w := orgNew()
	w.listErr = chkAPI("AccessDeniedException")
	_, _, err := Survey(context.Background(), w.mgmt(), w.assume, orgSettings, orgWhere, []byte(orgTemplate))
	if err == nil {
		t.Fatal("no error")
	}
}

func TestSurveyPolicy(t *testing.T) {
	w := orgNew()
	a, cs := w.survey(t, orgSettings)
	if a.PolicyChange != "new" || len(a.PolicyBase) != 0 || !strings.Contains(string(a.Policy), "arn:aws:iam::333333333333:role/dmt-prd-manager-discovery") {
		t.Errorf("a = %+v", a)
	}
	orgWant(t, cs, "delegation policy", OK, "new")

	cur := `{"Version":"2012-10-17","Statement":[{"Sid":"DormetoReadOrganization","Effect":"Allow","Principal":{"AWS":"` + orgDev + `"},"Action":["organizations:ListAccounts"],"Resource":"*"}]}`
	w.policy = &cur
	a, cs = w.survey(t, orgSettings)
	if a.PolicyChange != "updated" || string(a.PolicyBase) != cur {
		t.Errorf("a = %+v", a)
	}
	orgWant(t, cs, "delegation policy", OK, "DormetoReadOrganization gets this manager")

	w.policy = aws.String(string(a.Policy))
	a, cs = w.survey(t, orgSettings)
	if a.PolicyChange != "unchanged" {
		t.Errorf("change = %q", a.PolicyChange)
	}
	orgWant(t, cs, "delegation policy", OK, "already names this manager")

	w.policy = aws.String("{not json")
	_, cs = w.survey(t, orgSettings)
	if c := orgRow(t, cs, "delegation policy"); c.Verdict != Fail {
		t.Errorf("delegation policy = %+v", c)
	}
}

func TestSurveySortsWhateverTheOrder(t *testing.T) {
	w := orgNew()
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("2%011d", i)
		w.accounts[id] = orgAcct{status: orgtypes.AccountStatusActive}
	}
	a, _ := w.survey(t, orgSettings)
	if !slices.IsSorted(a.Missing) || len(a.Missing) != 31 {
		t.Errorf("Missing = %v", a.Missing)
	}
}

func TestMergePolicy(t *testing.T) {
	filled := `{"Version":"2012-10-17","Statement":[{"Sid":"DormetoReadOrganization","Effect":"Allow","Principal":{"AWS":"` + orgMine + `"},"Action":["organizations:DescribeOrganization","organizations:ListAccounts"],"Resource":"*"}]}`
	got, change, err := MergePolicy(nil, []byte(orgTemplate), orgManager, "prd")
	if err != nil || change != "new" || string(got) != filled {
		t.Fatalf("empty: %s %q %v", got, change, err)
	}

	foreign := `{"Sid":"Other","Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"*"}`
	cur := `{"Version":"2012-10-17","Statement":[` + foreign + `,{"Sid":"DormetoReadOrganization","Effect":"Allow","Principal":{"AWS":"` + orgDev + `"},"Action":"organizations:ListAccounts","Resource":"*"}]}`
	got, change, err = MergePolicy([]byte(cur), []byte(orgTemplate), orgManager, "prd")
	want := `{"Version":"2012-10-17","Statement":[` + foreign + `,{"Sid":"DormetoReadOrganization","Effect":"Allow","Principal":{"AWS":["` + orgMine + `","` + orgDev + `"]},"Action":["organizations:DescribeOrganization","organizations:ListAccounts"],"Resource":"*"}]}`
	want = strings.Replace(want, `["`+orgMine+`","`+orgDev+`"]`, `["`+orgDev+`","`+orgMine+`"]`, 1)
	if err != nil || change != "updated" || string(got) != want {
		t.Fatalf("updated:\n%s\n%s\n%q %v", got, want, change, err)
	}

	again, change, err := MergePolicy(got, []byte(orgTemplate), orgManager, "prd")
	if err != nil || change != "unchanged" || string(again) != string(got) {
		t.Fatalf("again: %s %q %v", again, change, err)
	}

	spaced := "{\n  \"Version\": \"2012-10-17\",\n  \"Statement\": [" + strings.TrimPrefix(strings.TrimSuffix(filled, "]}"), `{"Version":"2012-10-17","Statement":[`) + "]\n}\n"
	again, change, err = MergePolicy([]byte(spaced), []byte(orgTemplate), orgManager, "prd")
	if err != nil || change != "unchanged" || string(again) != spaced {
		t.Fatalf("spaced: %q %q %v", again, change, err)
	}

	onlyForeign := `{"Version":"2012-10-17","Statement":[` + foreign + `]}`
	got, change, err = MergePolicy([]byte(onlyForeign), []byte(orgTemplate), orgManager, "prd")
	if err != nil || change != "updated" || !strings.HasPrefix(string(got), `{"Version":"2012-10-17","Statement":[`+foreign+`,{"Sid":"DormetoReadOrganization"`) {
		t.Fatalf("appended: %s %q %v", got, change, err)
	}

	if _, _, err := MergePolicy([]byte("{nope"), []byte(orgTemplate), orgManager, "prd"); err == nil {
		t.Error("invalid JSON: no error")
	}
}

func TestOrgNames(t *testing.T) {
	p := OrgNames(orgMgmt, "eu-west-1", "prd")
	want := Project{Account: orgMgmt, Region: "eu-west-1", Env: "prd", Bucket: "dmt-install-111111111111-eu-west-1",
		Name: "dmt-prd-organization", Role: "dmt-prd-organization", LogGroup: "/aws/codebuild/dmt-prd-organization"}
	if p != want {
		t.Errorf("p = %+v", p)
	}
}

const wantOrgPolicy = `{"Version":"2012-10-17","Statement":[{"Sid":"AssumeMemberRole","Effect":"Allow","Action":"sts:AssumeRole","Resource":"arn:aws:iam::*:role/org/Member"},` +
	`{"Sid":"DelegationPolicy","Effect":"Allow","Action":["organizations:DescribeResourcePolicy","organizations:PutResourcePolicy","organizations:ListAccounts"],"Resource":"*"},` +
	`{"Sid":"State","Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:ListBucket"],"Resource":["arn:aws:s3:::dmt-install-111111111111-eu-west-1","arn:aws:s3:::dmt-install-111111111111-eu-west-1/*"]},` +
	`{"Sid":"Logs","Effect":"Allow","Action":["logs:CreateLogStream","logs:PutLogEvents"],"Resource":"arn:aws:logs:eu-west-1:111111111111:log-group:/aws/codebuild/dmt-prd-organization:*"}]}`

func TestOrgBootstrap(t *testing.T) {
	bootClock(t)
	w := &bootWorld{bucket: &s3types.NotFound{}}
	p := OrgNames(orgMgmt, "eu-west-1", "prd")
	if err := OrgBootstrap(context.Background(), w.clients(), p, "/org/Member"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(w.calls, "iam.AttachRolePolicy") {
		t.Errorf("a managed policy was attached: %v", w.calls)
	}
	cr := w.in["iam.CreateRole"].(*iam.CreateRoleInput)
	if aws.ToString(cr.RoleName) != "dmt-prd-organization" || aws.ToString(cr.Path) != "/dormeto/" ||
		!strings.Contains(aws.ToString(cr.AssumeRolePolicyDocument), "project/dmt-prd-organization") ||
		!strings.Contains(aws.ToString(cr.AssumeRolePolicyDocument), `"aws:SourceAccount":"111111111111"`) {
		t.Errorf("CreateRole = %+v", cr)
	}
	pr := w.in["iam.PutRolePolicy"].(*iam.PutRolePolicyInput)
	if aws.ToString(pr.RoleName) != "dmt-prd-organization" || aws.ToString(pr.PolicyName) != "dormeto-organization" || aws.ToString(pr.PolicyDocument) != wantOrgPolicy {
		t.Errorf("PutRolePolicy = %s\nwant %s", aws.ToString(pr.PolicyDocument), wantOrgPolicy)
	}
	cp := w.in["codebuild.CreateProject"].(*codebuild.CreateProjectInput)
	script := ""
	for _, e := range cp.Environment.EnvironmentVariables {
		if aws.ToString(e.Name) == "SCRIPT" {
			script = aws.ToString(e.Value)
		}
	}
	if aws.ToString(cp.Name) != "dmt-prd-organization" || script != "organization.sh" ||
		aws.ToString(cp.ServiceRole) != "arn:aws:iam::123456789012:role/dormeto/dmt-prd-organization" {
		t.Errorf("CreateProject = %s %s %s", aws.ToString(cp.Name), script, aws.ToString(cp.ServiceRole))
	}
}

func TestOrgBootstrapRefusesRoleOutsideDormeto(t *testing.T) {
	w := &bootWorld{role: true, rolePath: "/other/"}
	err := OrgBootstrap(context.Background(), w.clients(), OrgNames(orgMgmt, "eu-west-1", "prd"), "Member")
	if err == nil || !strings.Contains(err.Error(), "outside /dormeto/") {
		t.Errorf("err = %v", err)
	}
}

func TestOrgTeardownKeepsBucket(t *testing.T) {
	w := &bootWorld{role: true}
	if err := OrgTeardown(context.Background(), w.clients(), OrgNames(orgMgmt, "eu-west-1", "prd")); err != nil {
		t.Fatal(err)
	}
	want := []string{"codebuild.DeleteProject", "iam.GetRole", "iam.DeleteRolePolicy", "iam.DeleteRole", "logs.DeleteLogGroup"}
	if !slices.Equal(w.calls, want) {
		t.Errorf("calls = %v", w.calls)
	}
	if rp := w.in["iam.DeleteRolePolicy"].(*iam.DeleteRolePolicyInput); aws.ToString(rp.PolicyName) != "dormeto-organization" {
		t.Errorf("DeleteRolePolicy = %+v", rp)
	}
}

func TestOrgTeardownAlreadyGone(t *testing.T) {
	w := &bootWorld{noProject: true}
	if err := OrgTeardown(context.Background(), w.clients(), OrgNames(orgMgmt, "eu-west-1", "prd")); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(w.calls, "iam.DeleteRole") {
		t.Errorf("calls = %v", w.calls)
	}
}

func TestPutOrgPlanAndReadRoles(t *testing.T) {
	f := &stS3{}
	c := Clients{S3: f}
	p := OrgNames(orgMgmt, "eu-west-1", "prd")
	if err := PutOrgPlan(context.Background(), c, p, Access{Policy: []byte(`{"a":1}`), PolicyBase: []byte(`{"b":2}`)}); err != nil {
		t.Fatal(err)
	}
	if f.puts[p.Bucket+"/prd/organization/policy.json"] != `{"a":1}` || f.puts[p.Bucket+"/prd/organization/policy-base.json"] != `{"b":2}` {
		t.Errorf("puts = %v", f.puts)
	}
	if err := PutOrgPlan(context.Background(), c, p, Access{Policy: []byte(`{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	if v, ok := f.puts[p.Bucket+"/prd/organization/policy-base.json"]; !ok || v != "" {
		t.Errorf("no base: %q %v", v, ok)
	}

	got, err := ReadRoles(context.Background(), c, p)
	if err != nil || got != nil {
		t.Fatalf("absent: %v %v", got, err)
	}
	f.objects = map[string]string{"prd/organization/roles.json": `{"account":"444444444444","ok":true}` + "\n" + `{"account":"555555555555","ok":false,"error":"boom"}` + "\n\n"}
	got, err = ReadRoles(context.Background(), c, p)
	want := []RoleResult{{Account: "444444444444", OK: true}, {Account: "555555555555", Error: "boom"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("roles = %+v %v", got, err)
	}
	f.objects["prd/organization/roles.json"] = "{bad"
	if _, err := ReadRoles(context.Background(), c, p); err == nil {
		t.Error("bad line: no error")
	}
}

func TestDisabled(t *testing.T) {
	want := "Organization access needs the management account 111111111111; without its profile (--management-profile) it is not deployed.\n" +
		"Without it, the manager is installed in organization mode but cannot list the accounts until the delegation policy exists, nor read an account without its roles.\n" +
		"The procedure: https://dormeto.com/organization-access.html\n" +
		"Check it later: dormeto install check --management-profile <profile>; the dashboard's Organization access panel too."
	if got := Disabled(orgMgmt); got != want {
		t.Errorf("Disabled =\n%s", got)
	}
}
