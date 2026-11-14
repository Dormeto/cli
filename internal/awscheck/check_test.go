package awscheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/install"
)

const (
	account = "111122223333"
	caller  = "arn:aws:sts::111122223333:assumed-role/ops/ci"
	role    = "arn:aws:iam::111122223333:role/ops"
)

// fakeIAM simulates a policy: allow tells whether it allows an action on a resource. It answers a result per action and
// resource, as IAM does when several resources are asked, and records each simulation; refuse refuses them.
type fakeIAM struct {
	allow     func(action, resource string) bool
	refuse    error
	roleErr   error
	sources   []string
	asked     [][]string // the resources of each simulation
	aliasless bool
}

func (f *fakeIAM) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	if f.roleErr != nil {
		return nil, f.roleErr
	}
	return &iam.GetRoleOutput{Role: &iamtypes.Role{Arn: aws.String("arn:aws:iam::111122223333:role/" + aws.ToString(in.RoleName))}}, nil
}

func (f *fakeIAM) SimulatePrincipalPolicy(_ context.Context, in *iam.SimulatePrincipalPolicyInput, _ ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error) {
	if f.refuse != nil {
		return nil, f.refuse
	}
	f.sources = append(f.sources, aws.ToString(in.PolicySourceArn))
	f.asked = append(f.asked, in.ResourceArns)
	var rs []iamtypes.EvaluationResult
	for _, a := range in.ActionNames {
		for _, r := range in.ResourceArns {
			d := iamtypes.PolicyEvaluationDecisionTypeImplicitDeny
			if f.allow(a, r) {
				d = iamtypes.PolicyEvaluationDecisionTypeAllowed
			}
			rs = append(rs, iamtypes.EvaluationResult{EvalActionName: aws.String(a), EvalResourceName: aws.String(r), EvalDecision: d})
		}
	}
	return &iam.SimulatePrincipalPolicyOutput{EvaluationResults: rs}, nil
}

func (f *fakeIAM) ListAccountAliases(context.Context, *iam.ListAccountAliasesInput, ...func(*iam.Options)) (*iam.ListAccountAliasesOutput, error) {
	if f.aliasless {
		return &iam.ListAccountAliasesOutput{}, nil
	}
	return &iam.ListAccountAliasesOutput{AccountAliases: []string{"prod"}}, nil
}

type fakeSTS struct{ arn string }

func (f fakeSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return &sts.GetCallerIdentityOutput{Account: aws.String(account), Arn: aws.String(f.arn)}, nil
}

func allowAll(string, string) bool { return true }

// readOnly is the README's read-only policy, with apigateway:GET to find the API: execute-api:Invoke on GET only.
func readOnly(action, resource string) bool {
	return action == "apigateway:GET" || (action == Invoke && strings.Contains(resource, "/$default/GET/iam/"))
}

var found = &Manager{API: "dmt-prod-manager-api", APIID: "abc123", Region: "eu-west-1", Version: "0.24.0", ByName: true}

func run(t *testing.T, i *fakeIAM, who string, r Request) (Report, string) {
	t.Helper()
	rep, err := Check(context.Background(), i, fakeSTS{who}, r)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := PrintText(&b, rep); err != nil {
		t.Fatal(err)
	}
	return rep, b.String()
}

func row(rep Report, command string) CommandReport {
	i := slices.IndexFunc(rep.Commands, func(c CommandReport) bool { return c.Command == command })
	if i < 0 {
		return CommandReport{}
	}
	return rep.Commands[i]
}

// A profile that holds everything: every method's row held, a ✓ each, in gcp check's layout.
func TestEveryRowHeldIsOK(t *testing.T) {
	i := &fakeIAM{allow: allowAll}
	rep, text := run(t, i, caller, Request{Env: "prod", Region: "eu-west-1", Manager: found})
	want := "Account 111122223333 (prod), manager dmt-prod-manager-api 0.24.0, checked as " + caller + "\n"
	if !strings.HasPrefix(text, want) || !rep.OK() {
		t.Fatalf("ok %v, text:\n%s", rep.OK(), text)
	}
	var names []string
	for _, c := range rep.Commands {
		names = append(names, c.Command)
		if !c.Checked || len(c.Missing) != 0 || !strings.Contains(text, "  ✓ "+c.Command+": every permission held\n") {
			t.Errorf("%+v", c)
		}
	}
	want2 := append(fleet.RouteMethods(), "estimate", "install", "uninstall")
	slices.Sort(want2)
	if !slices.Equal(names, want2) {
		t.Errorf("rows %v, want %v", names, want2)
	}
	// Each manager row needs apigateway:GET (found by name) and execute-api:Invoke on each of its routes.
	whoami := row(rep, "whoami")
	if !slices.Equal(whoami.Held, []string{"apigateway:GET", "execute-api:Invoke on GET /iam/me", "execute-api:Invoke on GET /iam/health"}) {
		t.Errorf("whoami held %v", whoami.Held)
	}
	if got := row(rep, "install").Held; !slices.Equal(got, install.RightsActions()) {
		t.Errorf("install held %v", got)
	}
	if got := row(rep, "estimate").Held; !slices.Contains(got, "elasticloadbalancing:DescribeTargetHealth") || !slices.Contains(got, "pricing:GetProducts") {
		t.Errorf("estimate held %v", got)
	}
	// The assumed role's role is simulated, on the routes' ARNs in the API's $default stage.
	if len(i.sources) == 0 || i.sources[0] != role {
		t.Errorf("sources %v", i.sources)
	}
	if !slices.ContainsFunc(i.asked, func(rs []string) bool {
		return slices.Contains(rs, "arn:aws:execute-api:eu-west-1:111122223333:abc123/$default/POST/iam/workloads/*/*/wake") &&
			slices.Contains(rs, "arn:aws:execute-api:eu-west-1:111122223333:abc123/$default/*/iam/*")
	}) {
		t.Errorf("asked %v", i.asked)
	}
	if !strings.HasSuffix(text, "  ! IAM's simulation leaves out service control policies and the API's own refusals\n") {
		t.Errorf("text:\n%s", text)
	}
}

// The README's read-only policy holds the reading methods, not the writing ones: their writes are said missing.
func TestAReadOnlyPolicyLacksTheWritesRows(t *testing.T) {
	rep, text := run(t, &fakeIAM{allow: readOnly}, caller, Request{Env: "prod", Region: "eu-west-1", Manager: found})
	if rep.OK() {
		t.Fatalf("a read-only policy is OK:\n%s", text)
	}
	for _, m := range []string{"whoami", "overview", "alerts", "audit", "ip-lists", "operations"} {
		if !row(rep, m).OK() {
			t.Errorf("%s: %+v", m, row(rep, m))
		}
	}
	for m, routes := range fleet.Routes {
		var writes []string
		for _, r := range routes {
			if r.Method != "GET" {
				writes = append(writes, Invoke+" on "+r.String())
			}
		}
		if got := row(rep, m).Missing; !slices.Equal(got, writes) && !(len(writes) == 0 && len(got) == 0) {
			t.Errorf("%s missing %v, want %v", m, got, writes)
		}
	}
	for _, line := range []string{
		"  ✗ api: missing execute-api:Invoke on ANY /iam/*\n",
		"  ✗ savings: missing execute-api:Invoke on PUT /iam/savings/settings\n",
		"  ✓ whoami: every permission held\n",
		"  ✗ install: missing s3:CreateBucket",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("text lacks %q:\n%s", line, text)
		}
	}
}

// IAM refuses the simulation: every row is not checked, a warning says why, and it is no failure.
func TestASimulationRefusedChecksNothingAndIsNoFailure(t *testing.T) {
	denied := &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized to perform: iam:SimulatePrincipalPolicy"}
	for _, i := range []*fakeIAM{{refuse: denied}, {roleErr: denied, allow: allowAll}} {
		rep, text := run(t, i, caller, Request{Env: "prod", Region: "eu-west-1", Manager: found})
		if !rep.OK() {
			t.Errorf("not OK:\n%s", text)
		}
		for _, c := range rep.Commands {
			if c.Checked || len(c.Held)+len(c.Missing) != 0 || !strings.Contains(text, "  - "+c.Command+": not checked\n") {
				t.Errorf("%+v", c)
			}
		}
		if !strings.Contains(text, "  ! not checked (iam:SimulatePrincipalPolicy refused)") && !strings.Contains(text, "  ! not checked (iam:GetRole refused)") {
			t.Errorf("text:\n%s", text)
		}
	}
	// Any other error is one.
	if _, err := Check(context.Background(), &fakeIAM{refuse: errors.New("throttled")}, fakeSTS{caller}, Request{Manager: found}); err == nil {
		t.Error("no error")
	}
}

// No manager in the region for the environment: install, uninstall and estimate only, and a line that says so.
func TestNoManagerChecksInstallAndEstimateOnly(t *testing.T) {
	rep, text := run(t, &fakeIAM{allow: allowAll, aliasless: true}, caller, Request{Env: "dev", Region: "eu-west-3"})
	var names []string
	for _, c := range rep.Commands {
		names = append(names, c.Command)
	}
	if !slices.Equal(names, []string{"estimate", "install", "uninstall"}) || rep.Manager != nil {
		t.Errorf("rows %v, manager %v", names, rep.Manager)
	}
	if !strings.HasPrefix(text, "Account 111122223333, checked as "+caller+"\n") ||
		!strings.Contains(text, "  ! no manager found in eu-west-3 for env dev: only install, uninstall and estimate are checked\n") {
		t.Errorf("text:\n%s", text)
	}
	// Exit by those rows: an install right missing fails it.
	rep, _ = run(t, &fakeIAM{allow: func(a, _ string) bool { return a != "iam:PassRole" }}, caller, Request{Env: "dev", Region: "eu-west-3"})
	if rep.OK() || !slices.Equal(row(rep, "install").Missing, []string{"iam:PassRole"}) {
		t.Errorf("%+v", rep.Commands)
	}
}

// The lookup of the API refused: the manager's rows lack apigateway:GET, their routes unchecked.
func TestALookupRefusedLacksApigatewayGet(t *testing.T) {
	rep, text := run(t, &fakeIAM{allow: allowAll}, caller, Request{Env: "prod", Region: "eu-west-1", LookupRefused: true})
	if rep.OK() || !slices.Equal(row(rep, "whoami").Missing, []string{"apigateway:GET"}) || !row(rep, "install").OK() {
		t.Errorf("%+v", rep.Commands)
	}
	if !strings.Contains(text, "  ! the manager's API could not be looked up in eu-west-1 (apigateway:GET refused)") {
		t.Errorf("text:\n%s", text)
	}
}

// --api-url: no apigateway:GET needed; a manager whose version could not be read is said so.
func TestAnAPIURLNeedsNoLookup(t *testing.T) {
	m := &Manager{API: "https://abc123.execute-api.eu-west-1.amazonaws.com", APIID: "abc123", Region: "eu-west-1", VersionErr: errors.New("forbidden")}
	rep, text := run(t, &fakeIAM{allow: allowAll}, caller, Request{Env: "prod", Region: "eu-west-1", Manager: m})
	if slices.Contains(row(rep, "whoami").Held, "apigateway:GET") {
		t.Errorf("%+v", row(rep, "whoami"))
	}
	if !strings.Contains(text, ", manager https://abc123.execute-api.eu-west-1.amazonaws.com (version unknown), ") ||
		!strings.Contains(text, "  ! the manager's version is not read: forbidden\n") {
		t.Errorf("text:\n%s", text)
	}
}

// The account's root holds every right: nothing is simulated.
func TestRootHoldsEverything(t *testing.T) {
	i := &fakeIAM{allow: func(string, string) bool { return false }}
	rep, text := run(t, i, "arn:aws:iam::111122223333:root", Request{Manager: found})
	if !rep.OK() || len(i.sources) != 0 || !strings.Contains(text, "  ! the account's root holds every right: not simulated\n") {
		t.Errorf("ok %v, simulated %v:\n%s", rep.OK(), i.sources, text)
	}
}

// --json: {account, caller, manager: {api, version} | null, commands: [{command, checked, held, missing}], warnings}.
func TestJSON(t *testing.T) {
	for _, r := range []Request{{Manager: found}, {Env: "prod", Region: "eu-west-1"}} {
		rep, _ := run(t, &fakeIAM{allow: readOnly}, caller, r)
		var b bytes.Buffer
		if err := PrintJSON(&b, rep); err != nil {
			t.Fatal(err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(b.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var keys []string
		for k := range got {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"account", "caller", "commands", "manager", "warnings"}) {
			t.Errorf("keys %v", keys)
		}
		if r.Manager == nil && string(got["manager"]) != "null" || r.Manager != nil && strings.Join(strings.Fields(string(got["manager"])), "") != `{"api":"dmt-prod-manager-api","version":"0.24.0"}` {
			t.Errorf("manager %s", got["manager"])
		}
		if !strings.Contains(b.String(), `"command": "install"`) || !strings.Contains(b.String(), `"checked": true`) {
			t.Errorf("%s", b.String())
		}
	}
}
