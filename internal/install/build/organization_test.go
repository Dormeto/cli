package build

import (
	"strings"
	"testing"
)

func orgEnv() map[string]string {
	return map[string]string{
		"BUCKET": "dmt-install-123456789012-eu-west-1", "ENV": "prod", "MANAGER_ACCOUNT": "123456789012", "ORGANIZATION": "o-abcdefghij",
		"MEMBER_ROLE": "OrganizationAccountAccessRole", "ACCOUNTS": "111111111111 222222222222 333333333333", "DISCOVERY_ONLY": "222222222222",
		"RELEASE_BASE": "https://releases.dormeto.com/manager/0.11.0", "RELEASE_VERSION": "0.11.0", "CODEBUILD_BUILD_ID": "dmt-prod-install:build-3",
	}
}

const ob = "dmt-install-123456789012-eu-west-1/prod/organization/"

func TestOrganizationChangedPolicyStops(t *testing.T) {
	s := newSandbox(t)
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
	s := newSandbox(t)
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
	s := newSandbox(t)
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
	s := newSandbox(t)
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
	s := newSandbox(t)
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

// The CLI counts the accounts done from the log: one line per account, once its result is in roles.json.
func TestOrganizationSaysEachAccountsRoles(t *testing.T) {
	s := newSandbox(t)
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
	s := newSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	env := orgEnv()
	env["ACCOUNTS"] = "111111111111 222222222222"
	env["DISCOVERY_ONLY"] = ""
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
	s := newSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	env := orgEnv()
	var ids []string
	for i := 0; i < 12; i++ {
		ids = append(ids, "1000000000"+string(rune('0'+i/10))+string(rune('0'+i%10)))
	}
	env["ACCOUNTS"] = strings.Join(ids, " ")
	env["DISCOVERY_ONLY"] = ""
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
	s := newSandbox(t)
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
	s := newSandbox(t)
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
	s := newSandbox(t)
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
	s := newSandbox(t)
	mustPut(t, s, "policy-base.json", "")
	mustPut(t, s, "policy.json", `{}`)
	env := orgEnv()
	env["ACCOUNTS"] = "111111111111 12345"
	out, err := s.run("organization.sh", env)
	if err == nil || !strings.Contains(out, "12345") {
		t.Fatalf("err %v\n%s", err, out)
	}
	if has(s.log(), "aws organizations put-resource-policy") || has(s.log(), "aws sts assume-role") {
		t.Errorf("went on:\n%s", strings.Join(s.log(), "\n"))
	}
}
