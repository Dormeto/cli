package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/awscheck"
	"github.com/Dormeto/cli/internal/fleet"
)

// checkIAM simulates a policy (allow; refuse refuses the simulation) for `aws check`'s command tests.
type checkIAM struct {
	allow  func(action, resource string) bool
	refuse bool
}

func (f checkIAM) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	return &iam.GetRoleOutput{Role: &iamtypes.Role{Arn: awssdk.String("arn:aws:iam::111122223333:role/" + awssdk.ToString(in.RoleName))}}, nil
}

func (f checkIAM) SimulatePrincipalPolicy(_ context.Context, in *iam.SimulatePrincipalPolicyInput, _ ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error) {
	if f.refuse {
		return nil, &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized to perform: iam:SimulatePrincipalPolicy"}
	}
	var rs []iamtypes.EvaluationResult
	for _, a := range in.ActionNames {
		for _, r := range in.ResourceArns {
			d := iamtypes.PolicyEvaluationDecisionTypeImplicitDeny
			if f.allow(a, r) {
				d = iamtypes.PolicyEvaluationDecisionTypeAllowed
			}
			rs = append(rs, iamtypes.EvaluationResult{EvalActionName: awssdk.String(a), EvalResourceName: awssdk.String(r), EvalDecision: d})
		}
	}
	return &iam.SimulatePrincipalPolicyOutput{EvaluationResults: rs}, nil
}

func (f checkIAM) ListAccountAliases(context.Context, *iam.ListAccountAliasesInput, ...func(*iam.Options)) (*iam.ListAccountAliasesOutput, error) {
	return &iam.ListAccountAliasesOutput{AccountAliases: []string{"prod"}}, nil
}

type checkSTS struct{}

func (checkSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return &sts.GetCallerIdentityOutput{Account: awssdk.String("111122223333"), Arn: awssdk.String("arn:aws:sts::111122223333:assumed-role/ops/ci")}, nil
}

// toServer sends every request to the test server, whatever its host: the API's execute-api URL is answered by the fake.
type toServer struct{ srv *url.URL }

func (t toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = t.srv.Scheme, t.srv.Host
	return http.DefaultTransport.RoundTrip(r)
}

const checkAPI = "https://abc123.execute-api.eu-west-1.amazonaws.com"

func runCheck(t *testing.T, i checkIAM, lookupErr error, args ...string) fleetRan {
	t.Helper()
	api := &fakeAPI{custom: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/iam/health" {
			fmt.Fprint(w, `{"status":"ok","version":"0.24.0"}`)
			return
		}
		w.WriteHeader(500)
	}}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	tune := func(d *fleetDeps) {
		d.http = &http.Client{Transport: toServer{u}}
		d.checkClients = func(awssdk.Config) (awscheck.IAM, awscheck.STS) { return i, checkSTS{} }
		d.locator = func(awssdk.Config) fleet.Locator {
			return locatorFunc(func(context.Context, string, string) (string, error) {
				if lookupErr != nil {
					return "", lookupErr
				}
				return checkAPI, nil
			})
		}
	}
	check := func(d fleetDeps) []*cobra.Command { return []*cobra.Command{newAwsCheck(d)} }
	return runCommands(t, api, "eu-west-1", tune, check, append([]string{"check"}, args...)...)
}

func allowEverything(string, string) bool { return true }

// The profile holds everything: exit 0, the manager found by name and its version read with GET /health, its only call.
func TestAwsCheckHoldsEverything(t *testing.T) {
	r := runCheck(t, checkIAM{allow: allowEverything}, nil)
	if r.exit != 0 || !strings.HasPrefix(r.stdout, "Account 111122223333 (prod), manager dmt-prod-manager-api 0.24.0, checked as arn:aws:sts::111122223333:assumed-role/ops/ci\n") ||
		!strings.Contains(r.stdout, "  ✓ routers: every permission held\n") || !strings.Contains(r.stdout, "  ✓ install: every permission held\n") {
		t.Fatalf("exit %d, stdout:\n%s\nstderr %q", r.exit, r.stdout, r.stderr)
	}
	for _, q := range r.api.requests {
		if q.Method != http.MethodGet || q.URL.Path != "/iam/health" {
			t.Errorf("aws check called %s %s", q.Method, q.URL.Path)
		}
	}
	if len(r.api.requests) != 1 {
		t.Errorf("%d requests", len(r.api.requests))
	}
}

// A read-only policy: the writing methods lack their writes, exit 1.
func TestAwsCheckReadOnlyExits1(t *testing.T) {
	readOnly := func(a, r string) bool { return a != awscheck.Invoke || strings.Contains(r, "/$default/GET/iam/") }
	r := runCheck(t, checkIAM{allow: readOnly}, nil)
	if r.exit != 1 || !strings.Contains(r.stdout, "  ✗ routers: missing execute-api:Invoke on POST /iam/operations, ") ||
		!strings.Contains(r.stdout, "  ✓ whoami: every permission held\n") {
		t.Fatalf("exit %d, stdout:\n%s\nstderr %q", r.exit, r.stdout, r.stderr)
	}
	// --json: the same report as one object, exit 1 still.
	r = runCheck(t, checkIAM{allow: readOnly}, nil, "--json")
	var got struct {
		Account  string
		Manager  *struct{ API, Version string }
		Commands []struct {
			Command string
			Checked bool
			Missing []string
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || r.exit != 1 || got.Account != "111122223333" || got.Manager == nil || got.Manager.Version != "0.24.0" || len(got.Commands) < 20 {
		t.Fatalf("exit %d, %v, stdout %s", r.exit, err, r.stdout)
	}
}

// IAM refuses the simulation: every method not checked, exit 0.
func TestAwsCheckSimulationRefusedExits0(t *testing.T) {
	r := runCheck(t, checkIAM{refuse: true}, nil)
	if r.exit != 0 || !strings.Contains(r.stdout, "  - whoami: not checked\n") || !strings.Contains(r.stdout, "  ! not checked (iam:SimulatePrincipalPolicy refused)") {
		t.Fatalf("exit %d, stdout:\n%s\nstderr %q", r.exit, r.stdout, r.stderr)
	}
}

// No manager in the region: install, uninstall and estimate only, exit by those rows, and no call to a manager.
func TestAwsCheckWithoutAManager(t *testing.T) {
	noAPI := fmt.Errorf("%w: no API named dmt-dev-manager-api for env dev in region eu-west-1", fleet.ErrNoAPI)
	r := runCheck(t, checkIAM{allow: allowEverything}, noAPI, "--env", "dev")
	if r.exit != 0 || !strings.Contains(r.stdout, "  ! no manager found in eu-west-1 for env dev: only install, uninstall and estimate are checked\n") ||
		strings.Contains(r.stdout, "routers") || len(r.api.requests) != 0 {
		t.Fatalf("exit %d, requests %d, stdout:\n%s\nstderr %q", r.exit, len(r.api.requests), r.stdout, r.stderr)
	}
	r = runCheck(t, checkIAM{allow: func(a, _ string) bool { return a != "s3:CreateBucket" }}, noAPI)
	if r.exit != 1 || !strings.Contains(r.stdout, "  ✗ install: missing s3:CreateBucket\n") {
		t.Fatalf("exit %d, stdout:\n%s", r.exit, r.stdout)
	}
}

// The lookup refused: the manager's methods lack apigateway:GET (exit 1); another error of the lookup is one (exit 1).
func TestAwsCheckLookupRefused(t *testing.T) {
	denied := fmt.Errorf("list the APIs of eu-west-1: %w", &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"})
	r := runCheck(t, checkIAM{allow: allowEverything}, denied)
	if r.exit != 1 || !strings.Contains(r.stdout, "  ✗ whoami: missing apigateway:GET\n") {
		t.Fatalf("exit %d, stdout:\n%s\nstderr %q", r.exit, r.stdout, r.stderr)
	}
	r = runCheck(t, checkIAM{allow: allowEverything}, fmt.Errorf("list the APIs of eu-west-1: throttled"))
	if r.exit != 1 || r.stdout != "" || !strings.Contains(r.stderr, "throttled") {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
}

// --api-url: no lookup, its API id in the ARNs; a bad flag exits 2 before any call.
func TestAwsCheckFlags(t *testing.T) {
	r := runCheck(t, checkIAM{allow: func(a, res string) bool {
		return a != "apigateway:GET" && (a != awscheck.Invoke || strings.Contains(res, ":xyz789/$default/"))
	}}, nil, "--api-url", "https://xyz789.execute-api.eu-west-1.amazonaws.com")
	if r.exit != 0 || !strings.Contains(r.stdout, ", manager https://xyz789.execute-api.eu-west-1.amazonaws.com 0.24.0, ") {
		t.Fatalf("exit %d, stdout:\n%s\nstderr %q", r.exit, r.stdout, r.stderr)
	}
	for _, args := range [][]string{{"--env", "Bad_Env"}, {"extra"}, {"--nope"}} {
		if r := runCheck(t, checkIAM{allow: allowEverything}, nil, args...); r.exit != 2 || len(r.api.requests) != 0 {
			t.Errorf("%v: exit %d, stderr %q", args, r.exit, r.stderr)
		}
	}
}

// dormeto aws's help, the README and the CHANGELOG name aws check.
func TestAwsCheckIsInTheHelpReadmeAndChangelog(t *testing.T) {
	out, _, _ := executed(t, build{}, "aws", "--help")
	if !strings.Contains(out, "\n  check ") || !strings.Contains(out, "\n  whoami ") {
		t.Errorf("aws help:\n%s", out)
	}
	for _, f := range []string{"../../README.md", "../../CHANGELOG.md"} {
		if b, err := os.ReadFile(f); err != nil || !strings.Contains(string(b), "dormeto aws check") {
			t.Errorf("%s does not name dormeto aws check", f)
		}
	}
}
