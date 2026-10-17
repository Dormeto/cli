package build

import (
	"strings"
	"testing"
)

func orgEnv() map[string]string {
	return map[string]string{
		"BUCKET": "dmt-install-123456789012-eu-west-1", "ENV": "prod", "MANAGER_ACCOUNT": "123456789012", "ORGANIZATION": "o-abcdefghij",
		"MEMBER_ROLE":  "OrganizationAccountAccessRole",
		"RELEASE_BASE": "https://releases.dormeto.com/manager/0.11.0", "RELEASE_VERSION": "0.11.0", "CODEBUILD_BUILD_ID": "dmt-prod-install:build-3",
	}
}

const ob = "dmt-install-123456789012-eu-west-1/prod/organization/"

// newOrgSandbox is a sandbox whose plan names three accounts, 222… discovery only.
func newOrgSandbox(t *testing.T) *sandbox {
	s := newSandbox(t)
	orgLists(t, s, []string{"111111111111", "222222222222", "333333333333"}, []string{"222222222222"})
	return s
}

// orgLists writes the plan's accounts.txt and discovery-only.txt, one id per line, as the CLI does.
func orgLists(t *testing.T, s *sandbox, accounts, discoveryOnly []string) {
	t.Helper()
	lines := func(ids []string) string {
		if len(ids) == 0 {
			return ""
		}
		return strings.Join(ids, "\n") + "\n"
	}
	mustPut(t, s, "accounts.txt", lines(accounts))
	mustPut(t, s, "discovery-only.txt", lines(discoveryOnly))
}

// The accounts come from the plan in the bucket (accounts.txt, discovery-only.txt), not from the environment.
func TestOrganizationReadsTheAccountsFromThePlan(t *testing.T) {
	s := newSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	orgLists(t, s, []string{"444444444444", "555555555555"}, []string{"555555555555"})
	env := orgEnv()
	env["ACCOUNTS"], env["DISCOVERY_ONLY"] = "111111111111", "111111111111"
	if out, err := s.run("organization.sh", env); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	log := s.log()
	if !has(log, "aws s3 cp --only-show-errors s3://"+ob+"accounts.txt accounts.txt") || !has(log, "aws s3 cp --only-show-errors s3://"+ob+"discovery-only.txt discovery-only.txt") {
		t.Errorf("lists not read:\n%s", strings.Join(log, "\n"))
	}
	if !has(log, "access ACCOUNT=444444444444 MANAGER_ACCOUNT=123456789012 ENVS=prod ORGANIZATION=o-abcdefghij DISCOVERY_ROLE=DmtDiscoveryRole COMMAND_ROLE=DmtCommandRole ") ||
		!has(log, "access ACCOUNT=555555555555 MANAGER_ACCOUNT=123456789012 ENVS=prod ORGANIZATION=o-abcdefghij DISCOVERY_ROLE=DmtDiscoveryRole COMMAND_ROLE= ") ||
		has(log, "access ACCOUNT=111111111111") {
		t.Errorf("log:\n%s", strings.Join(log, "\n"))
	}
}

// No account to give roles (none reachable): the delegation policy alone, an empty roles.json.
func TestOrganizationNoAccount(t *testing.T) {
	s := newSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	orgLists(t, s, nil, nil)
	out, err := s.run("organization.sh", orgEnv())
	if err != nil || !strings.HasSuffix(out, "dormeto: putting the delegation policy\ndormeto: done\n") {
		t.Fatalf("%v\n%s", err, out)
	}
	if roles, ok := s.uploaded(ob + "roles.json"); !ok || roles != "" || has(s.log(), "aws sts assume-role") {
		t.Errorf("roles.json %q %v:\n%s", roles, ok, strings.Join(s.log(), "\n"))
	}
}

func TestOrganizationChangedPolicyStops(t *testing.T) {
	s := newOrgSandbox(t)
	s.write(s.fake+"/policy", `{"Statement":"someone else's"}`+"\n", 0o644)
	mustPut(t, s, "policy-base.json", `{"Statement":[]}`)
	mustPut(t, s, "policy.json", `{"Statement":["new"]}`)
	out, err := s.run("organization.sh", orgEnv())
	if err == nil {
		t.Fatalf("no failure: %s", out)
	}
	if !strings.Contains(out, "dormeto: the delegation policy changed since the plan: plan again") {
		t.Errorf("message: %s", out)
	}
	if has(s.log(), "aws organizations put-resource-policy") || has(s.log(), "aws sts assume-role") {
		t.Errorf("went on:\n%s", strings.Join(s.log(), "\n"))
	}
}

func TestOrganizationUnchangedPolicyIsPut(t *testing.T) {
	s := newOrgSandbox(t)
	s.write(s.fake+"/policy", `{"Statement":[]}`+"\n", 0o644)
	mustPut(t, s, "policy-base.json", `{"Statement":[]}`)
	mustPut(t, s, "policy.json", `{"Statement":["new"]}`)
	if out, err := s.run("organization.sh", orgEnv()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !has(s.log(), "aws organizations put-resource-policy --content file://policy.json") {
		t.Errorf("not put:\n%s", strings.Join(s.log(), "\n"))
	}
}

func TestOrganizationNoPolicyYetAndEmptyBase(t *testing.T) {
	s := newOrgSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{"Statement":["new"]}`)
	if out, err := s.run("organization.sh", orgEnv()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !has(s.log(), "aws organizations put-resource-policy") {
		t.Error("not put")
	}
}

func TestOrganizationRoles(t *testing.T) {
	s := newOrgSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	env := orgEnv()
	env["FAKE_ASSUME_FAIL"] = "333333333333"
	out, err := s.run("organization.sh", env)
	if err != nil {
		t.Fatalf("one account failing must not fail the script: %v\n%s", err, out)
	}
	roles, ok := s.uploaded(ob + "roles.json")
	if !ok {
		t.Fatal("roles.json not uploaded")
	}
	lines := strings.Split(strings.TrimSpace(roles), "\n")
	want := []string{
		`{"account":"111111111111","ok":true}`,
		`{"account":"222222222222","ok":true}`,
		`{"account":"333333333333","ok":false,"error":"An error occurred (AccessDenied) when calling the AssumeRole operation: not \"allowed\""}`,
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("roles.json:\n%s\nwant\n%s", roles, strings.Join(want, "\n"))
	}
	log := s.log()
	if !has(log, "access ACCOUNT=111111111111 MANAGER_ACCOUNT=123456789012 ENVS=prod ORGANIZATION=o-abcdefghij DISCOVERY_ROLE=DmtDiscoveryRole COMMAND_ROLE=DmtCommandRole KEY=AK111111111111") {
		t.Errorf("first account:\n%s", strings.Join(log, "\n"))
	}
	if !has(log, "access ACCOUNT=222222222222 MANAGER_ACCOUNT=123456789012 ENVS=prod ORGANIZATION=o-abcdefghij DISCOVERY_ROLE=DmtDiscoveryRole COMMAND_ROLE= KEY=AK222222222222") {
		t.Errorf("discovery-only account must run with an empty COMMAND_ROLE:\n%s", strings.Join(log, "\n"))
	}
	if has(log, "access ACCOUNT=333333333333") {
		t.Error("access script ran for an account not assumed")
	}
	if !has(log, "aws sts assume-role --role-arn arn:aws:iam::111111111111:role/OrganizationAccountAccessRole --role-session-name dmt-prod-organization") {
		t.Errorf("assume-role:\n%s", strings.Join(log, "\n"))
	}
}

// The roles' names the manager is given reach the release's script, their paths kept; a discovery-only account still
// gets an empty COMMAND_ROLE.
func TestOrganizationRoleNames(t *testing.T) {
	s := newOrgSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	env := orgEnv()
	env["DISCOVERY_ROLE"], env["COMMAND_ROLE"] = "team/MyDiscovery", "team/MyCommand"
	if out, err := s.run("organization.sh", env); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	log := s.log()
	if !has(log, "access ACCOUNT=111111111111 MANAGER_ACCOUNT=123456789012 ENVS=prod ORGANIZATION=o-abcdefghij DISCOVERY_ROLE=team/MyDiscovery COMMAND_ROLE=team/MyCommand ") ||
		!has(log, "access ACCOUNT=222222222222 MANAGER_ACCOUNT=123456789012 ENVS=prod ORGANIZATION=o-abcdefghij DISCOVERY_ROLE=team/MyDiscovery COMMAND_ROLE= ") {
		t.Errorf("log:\n%s", strings.Join(log, "\n"))
	}
}

// What the AWS CLI writes on stderr while assuming the member role is not taken for the credentials.
func TestOrganizationAssumeRoleStderrKeptApart(t *testing.T) {
	s := newOrgSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	env := orgEnv()
	env["FAKE_ASSUME_WARN"] = "1"
	if out, err := s.run("organization.sh", env); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if log := s.log(); !has(log, "access ACCOUNT=111111111111 ") || !strings.HasSuffix(log[index(log, "access ACCOUNT=111111111111 ")], " KEY=AK111111111111") {
		t.Errorf("log:\n%s", strings.Join(log, "\n"))
	}
	roles, _ := s.uploaded(ob + "roles.json")
	if !strings.Contains(roles, `{"account":"111111111111","ok":true}`) {
		t.Errorf("roles.json:\n%s", roles)
	}
}

// A failed account's whole output reaches the build's log, each line prefixed with the account; a good one's does not.
func TestOrganizationFailedAccountsOutputInTheLog(t *testing.T) {
	s := newOrgSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	env := orgEnv()
	env["FAKE_ACCESS_FAIL"] = "222222222222"
	env["FAKE_ASSUME_FAIL"] = "333333333333"
	out, err := s.run("organization.sh", env)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"dormeto: 222222222222: line one\ndormeto: 222222222222: boom \"quoted\" \\ here\ndormeto: roles of 222222222222: failed\n",
		"dormeto: 333333333333: An error occurred (AccessDenied) when calling the AssumeRole operation: not \"allowed\"\ndormeto: roles of 333333333333: failed\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "dormeto: 111111111111: ") {
		t.Errorf("a good account's output printed:\n%s", out)
	}
}

// The CLI counts the accounts done from the log: one line per account, once its result is in roles.json.
func TestOrganizationSaysEachAccountsRoles(t *testing.T) {
	s := newOrgSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	env := orgEnv()
	env["FAKE_ASSUME_FAIL"] = "333333333333"
	out, err := s.run("organization.sh", env)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var said []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "dormeto: roles of ") || strings.HasPrefix(l, "dormeto: account ") {
			said = append(said, l)
		}
	}
	want := []string{"dormeto: roles of 111111111111: ok", "dormeto: roles of 222222222222: ok", "dormeto: roles of 333333333333: failed"}
	if strings.Join(said, "\n") != strings.Join(want, "\n") {
		t.Errorf("lines:\n%s\nwant\n%s", strings.Join(said, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(out, "dormeto: roles of 333333333333: failed\ndormeto: done\n") {
		t.Errorf("output:\n%s", out)
	}
}

func TestOrganizationAccessFailureKeepsTheLastLine(t *testing.T) {
	s := newOrgSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	env := orgEnv()
	orgLists(t, s, []string{"111111111111", "222222222222"}, nil)
	env["FAKE_ACCESS_FAIL"] = "222222222222"
	if out, err := s.run("organization.sh", env); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	roles, _ := s.uploaded(ob + "roles.json")
	if !strings.Contains(roles, `{"account":"222222222222","ok":false,"error":"boom \"quoted\" \\ here"}`) || !strings.Contains(roles, `{"account":"111111111111","ok":true}`) {
		t.Errorf("roles.json:\n%s", roles)
	}
}

func TestOrganizationBatchesOfTen(t *testing.T) {
	s := newOrgSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	env := orgEnv()
	var ids []string
	for i := 0; i < 12; i++ {
		ids = append(ids, "1000000000"+string(rune('0'+i/10))+string(rune('0'+i%10)))
	}
	orgLists(t, s, ids, nil)
	if out, err := s.run("organization.sh", env); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	uploads := 0
	for _, l := range s.log() {
		if strings.HasPrefix(l, "aws s3 cp --only-show-errors roles.json ") {
			uploads++
		}
	}
	if uploads != 3 {
		t.Errorf("roles.json uploaded %d times, want 3 (empty at the start, then after each batch)", uploads)
	}
	roles, _ := s.uploaded(ob + "roles.json")
	if n := len(strings.Split(strings.TrimSpace(roles), "\n")); n != 12 {
		t.Errorf("%d lines, want 12", n)
	}
}

func TestOrganizationWrongAccessScriptSumStops(t *testing.T) {
	s := newOrgSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	env := orgEnv()
	env["FAKE_BAD"] = "dormeto-access-commands-0.11.0.sh"
	if out, err := s.run("organization.sh", env); err == nil {
		t.Fatalf("no failure: %s", out)
	}
	if has(s.log(), "aws organizations") {
		t.Error("policy touched after a bad sum")
	}
}

func TestOrganizationS3ReleaseBase(t *testing.T) {
	s := newOrgSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	env := orgEnv()
	env["RELEASE_BASE"] = "s3://b/prod/release"
	if out, err := s.run("organization.sh", env); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !has(s.log(), "aws s3 cp --only-show-errors s3://b/prod/release/dormeto-access-commands-0.11.0.sh dormeto-access-commands-0.11.0.sh") || has(s.log(), "curl") {
		t.Errorf("log:\n%s", strings.Join(s.log(), "\n"))
	}
}

// mustPut places a file of the plan in the fake bucket, where the script reads it.
func mustPut(t *testing.T, s *sandbox, name, body string) {
	t.Helper()
	p := s.fake + "/s3/" + ob + name
	s.write2(p, body)
}

func TestOrganizationEmptiesRolesJSONFirst(t *testing.T) {
	s := newOrgSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	s.write2(s.fake+"/s3/"+ob+"roles.json", "previous run\n")
	if out, err := s.run("organization.sh", orgEnv()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	log := s.log()
	empty := index(log, "aws s3 cp --only-show-errors roles.json s3://"+ob+"roles.json")
	if empty < 0 || empty > index(log, "aws organizations put-resource-policy") {
		t.Errorf("an empty roles.json must be uploaded before the policy step:\n%s", strings.Join(log, "\n"))
	}
}

func TestOrganizationBadAccountIDStopsBeforeThePolicy(t *testing.T) {
	s := newOrgSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	orgLists(t, s, []string{"111111111111", "12345"}, nil)
	out, err := s.run("organization.sh", orgEnv())
	if err == nil || !strings.Contains(out, "12345") {
		t.Fatalf("err %v\n%s", err, out)
	}
	if has(s.log(), "aws organizations put-resource-policy") || has(s.log(), "aws sts assume-role") {
		t.Errorf("went on:\n%s", strings.Join(s.log(), "\n"))
	}
}
