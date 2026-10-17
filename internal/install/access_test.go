package install

// Organization access in the runs (access.go), through run_test.go's fakes with rwOrganization: the management account
// (the profile m) and the organization's other accounts.

import (
	"slices"
	"strings"
	"testing"
	"time"

	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
)

// rwMgmtArgs are the flags that name the install again with the management account's profile.
const rwMgmtArgs = "--management-profile m " + rwArgs

// rwMgmtFlags is a request whose flags give every answer, the management account's profile m among them.
func rwMgmtFlags() Request {
	r := rwFlags()
	r.ManagementProfile = "m"
	return r
}

// mgmtWrites are the run's writes in the management account.
func (w *rwWorld) mgmtWrites() []string {
	var out []string
	for _, c := range w.writes {
		if strings.HasPrefix(c, "mgmt ") {
			out = append(out, c)
		}
	}
	return out
}

func TestInstallDeploysOrganizationAccess(t *testing.T) {
	w := rwNew(t)
	w.rwOrganization()
	w.mgmt.runFor = 5 * time.Second // in progress at the first poll, every account's line in the log already
	w.status = &trkStatus{}
	code, out, asked := w.run(Install, rwMgmtFlags(), true, "y", "y")
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "questions", asked, []string{"Apply this plan? [y/N]", "Deploy organization access? [y/N]"})
	rwInOrder(t, out,
		"  ✓ administrators: you@example.com\n",
		"  ✓ applied: 3 added, 0 changed, 0 destroyed\n",
		"\nOrganization access\n",
		"  ✓ management account: 809706483662\n",
		"  ! member role: OrganizationAccountAccessRole: assumed in 3 of 4 accounts; unreachable: 666666666666 (the procedure for them: https://dormeto.com/organization-access.html)\n",
		"  ! member accounts: 1 ready, 1 to create, 0 discovery only; another manager's roles, left out: 777777777777\n",
		"  ✓ delegation policy: new\n",
		"Plan (organization access)\n",
		"  delegation policy: new\n",
		"  roles: 1 updated, 1 created in 2 accounts (0 discovery only)\n",
		"  left out: 1 unreachable (666666666666), 1 another manager's (777777777777)\n",
		"\nBootstrap (API calls) in the management account 809706483662: bucket dmt-install-809706483662-eu-west-1, role dmt-prod-organization, project dmt-prod-organization\n",
		"  the management account's build role and project removed\n",
		"  ✓ organization access: delegation policy created; roles in 2 accounts\n",
	)
	if strings.Contains(out, "not deployed (no management profile)") {
		t.Errorf("organization access said not deployed:\n%s", out)
	}
	// The follow counts the accounts' lines out of the accounts.
	if !slices.Contains(w.status.updates, "Organization access (build dmt-prod-organization:b1…)  2/2  0s") ||
		w.status.end != "Organization access (build dmt-prod-organization:b1…)  succeeded  5s" {
		t.Errorf("status line: %q, end %q", w.status.updates, w.status.end)
	}
	b := w.mgmt.started()
	if len(b) != 1 {
		t.Fatalf("organization builds: %v", w.mgmt.actions())
	}
	rwEqual(t, "organization build's variables", b[0].vars, map[string]string{"ACTION": "apply", "MANAGER_ACCOUNT": rwAccount, "ORGANIZATION": "o-vvk0115ixn",
		"MEMBER_ROLE": "OrganizationAccountAccessRole", "ACCOUNTS": "444444444444 555555555555", "DISCOVERY_ONLY": "",
		"RELEASE_BASE": ReleasesURL + "/0.11.0", "RELEASE_VERSION": "0.11.0"})
	// After the manager's apply: the bootstrap, the plan, the build, then its project and role removed.
	rwInOrder(t, strings.Join(w.writes, "\n")+"\n",
		"codebuild:StartBuild apply\n",
		"mgmt s3:CreateBucket\n",
		"mgmt iam:CreateRole dmt-prod-organization\n",
		"mgmt iam:PutRolePolicy dmt-prod-organization\n",
		"mgmt codebuild:CreateProject\n",
		"mgmt s3:PutObject prod/organization/policy.json\n",
		"mgmt s3:PutObject prod/organization/policy-base.json\n",
		"mgmt codebuild:StartBuild apply\n",
		"mgmt codebuild:DeleteProject\n",
		"mgmt iam:DeleteRolePolicy dmt-prod-organization\n",
		"mgmt iam:DeleteRole dmt-prod-organization\n",
		"mgmt logs:DeleteLogGroup\n",
	)
	if p := w.mgmt.objects["prod/organization/policy.json"]; !strings.Contains(p, rwMine) || w.mgmt.objects["prod/organization/policy-base.json"] != "" {
		t.Errorf("policy.json %s", p)
	}
	if w.mgmt.project || w.mgmt.role {
		t.Error("the management account's project or role left")
	}
	for _, a := range w.assumed {
		if !strings.HasPrefix(a, "m arn:aws:iam::") || !strings.HasSuffix(a, ":role/OrganizationAccountAccessRole") {
			t.Errorf("assumed %q", a)
		}
	}
}

func TestInstallOrganizationAccessAFailedAccount(t *testing.T) {
	w := rwNew(t)
	w.rwOrganization()
	w.failing["555555555555"] = "AccessDenied: iam:CreateRole"
	code, out, _ := w.run(Install, rwMgmtFlags(), true, "y", "y")
	if code != 1 || !strings.HasSuffix(out, "  the management account's build role and project removed\n"+
		"  ✓ organization access: delegation policy created; roles in 1 accounts\n"+
		"  ✗ roles failed in 1 accounts: 555555555555 (AccessDenied: iam:CreateRole)\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

func TestInstallOrganizationAccessNotConfirmed(t *testing.T) {
	w := rwNew(t)
	w.rwOrganization()
	code, out, asked := w.run(Install, rwMgmtFlags(), true, "y", "n")
	rwEqual(t, "questions", asked, []string{"Apply this plan? [y/N]", "Deploy organization access? [y/N]"})
	if code != 0 || !strings.HasSuffix(out, "Organization access not deployed: dormeto install apply "+rwMgmtArgs+" deploys it\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan", "apply"})
	if m := w.mgmtWrites(); len(m) != 0 {
		t.Errorf("wrote in the management account: %v", m)
	}
}

// Without a terminal the manager's plan is not applied, so organization access waits for install apply, which deploys
// both without a question.
func TestInstallWithoutATerminalLeavesOrganizationAccessToInstallApply(t *testing.T) {
	w := rwNew(t)
	w.rwOrganization()
	code, out, _ := w.run(Install, rwMgmtFlags(), false)
	if code != 0 || !strings.HasSuffix(out, "Plan saved. Apply it: dormeto install apply "+rwMgmtArgs+"\n") || strings.Contains(out, "Organization access") {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	if m := w.mgmtWrites(); len(m) != 0 {
		t.Errorf("wrote in the management account: %v", m)
	}

	code, out, asked := w.run(Apply, Request{Profile: "p", Region: "eu-west-1", ManagementProfile: "m"}, false)
	if code != 0 || len(asked) != 0 {
		t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	rwInOrder(t, out, "  ✓ applied: 3 added, 0 changed, 0 destroyed\n", "\nOrganization access\n", "Plan (organization access)\n",
		"  ✓ organization access: delegation policy created; roles in 2 accounts\n")
	rwEqual(t, "builds", w.actions(), []string{"plan", "apply"})
	rwEqual(t, "organization builds", w.mgmt.actions(), []string{"apply"})
}

// An upgrade whose plan changes nothing still runs organization access (decision 18); without a terminal, it is shown
// and left to install apply.
func TestInstallNothingToChangeOffersOrganizationAccess(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	w.rwOrganization()
	w.plan = rwEmpty
	r := Request{Profile: "p", Region: "eu-west-1", ManagementProfile: "m"}
	code, out, _ := w.run(Install, r, false)
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwInOrder(t, out, "Nothing to change: manager 0.11.0 is installed.\n", "\nOrganization access\n", "  roles: 1 updated, 1 created in 2 accounts (0 discovery only)\n",
		"Organization access not deployed: dormeto install apply "+rwMgmtArgs+" deploys it\n")
	if m := w.mgmtWrites(); len(m) != 0 {
		t.Errorf("wrote in the management account: %v", m)
	}

	// install apply: the plan that changes nothing is not applied; organization access is deployed without a question.
	code, out, asked := w.run(Apply, r, false)
	if code != 0 || len(asked) != 0 || !strings.HasSuffix(out, "  ✓ organization access: delegation policy created; roles in 2 accounts\n") {
		t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan"})
	rwEqual(t, "organization builds", w.mgmt.actions(), []string{"apply"})

	// In a terminal, it is asked.
	w = rwNew(t)
	w.installed("0.11.0")
	w.rwOrganization()
	w.plan = rwEmpty
	code, out, asked = w.run(Install, r, true, "y")
	rwEqual(t, "questions", asked, []string{"Deploy organization access? [y/N]"})
	if code != 0 || !strings.HasSuffix(out, "  ✓ organization access: delegation policy created; roles in 2 accounts\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

// install apply with the management profile and no manager plan to apply (none, or one applied already): organization
// access alone, without a question (ruling R16).
func TestApplyDeploysOrganizationAccessWithoutAManagerPlan(t *testing.T) {
	for _, c := range []struct {
		name     string
		lastPlan string
	}{{"no plan", ""}, {"the plan applied already", "dmt-prod-install:p0"}} {
		t.Run(c.name, func(t *testing.T) {
			w := rwNew(t)
			w.installed("0.11.0") // its plan p0 predates its apply a0
			w.rwOrganization()
			s := w.saved()
			s.LastPlan = c.lastPlan
			w.save(s)
			code, out, asked := w.run(Apply, Request{Profile: "p", Region: "eu-west-1", ManagementProfile: "m"}, true)
			if code != 0 || len(asked) != 0 || !strings.HasSuffix(out, "  ✓ organization access: delegation policy created; roles in 2 accounts\n") {
				t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
			}
			if len(w.actions()) != 0 {
				t.Errorf("manager builds %v", w.actions())
			}
			rwEqual(t, "organization builds", w.mgmt.actions(), []string{"apply"})
		})
	}

	// Without the management profile, as before.
	w := rwNew(t)
	w.installed("0.11.0")
	code, out, _ := w.run(Apply, Request{Profile: "p", Region: "eu-west-1"}, false)
	if code != 1 || !strings.HasSuffix(out, "the plan dmt-prod-install:p0 predates the last apply dmt-prod-install:a0: plan again\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

// The install's own profile is the management account's: organization access is offered without --management-profile.
func TestInstallFromTheManagementAccountOffersOrganizationAccess(t *testing.T) {
	w := rwNew(t)
	w.org.Management = rwAccount
	w.rwOrganization()
	code, out, asked := w.run(Install, rwFlags(), true, "y", "n")
	rwEqual(t, "questions", asked, []string{"Apply this plan? [y/N]", "Deploy organization access? [y/N]"})
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwInOrder(t, out, "\nOrganization access\n  ✓ management account: "+rwAccount+"\n", "Organization access not deployed: dormeto install apply "+rwArgs+" deploys it\n")
	if strings.Contains(out, "not deployed (no management profile)") {
		t.Errorf("organization access said not deployed:\n%s", out)
	}
	if len(w.assumed) == 0 {
		t.Fatal("no member role assumed")
	}
	for _, a := range w.assumed {
		if !strings.HasPrefix(a, "p arn:aws:iam::") {
			t.Errorf("assumed %q", a)
		}
	}
}

func TestInstallOrganizationAccessAFailedSurvey(t *testing.T) {
	w := rwNew(t)
	w.rwOrganization()
	w.mgmt.id = "222222222222" // the profile m reaches another account
	code, out, _ := w.run(Install, rwMgmtFlags(), true, "y")
	if code != 1 {
		t.Errorf("exit code %d", code)
	}
	rwInOrder(t, out, "  ✓ applied: 3 added, 0 changed, 0 destroyed\n",
		"  ✗ management account: the profile reaches 222222222222, not the management account 809706483662\n",
		"Organization access is not deployed (a check failed); the manager is installed.\n")
	if m := w.mgmtWrites(); len(m) != 0 {
		t.Errorf("wrote in the management account: %v", m)
	}
}

// Ctrl-C while the organization build runs detaches: its project and role stay for install status to remove.
func TestInstallDetachesFromOrganizationAccess(t *testing.T) {
	w := rwNew(t)
	w.rwOrganization()
	w.mgmt.ends["apply"] = cbtypes.StatusTypeInProgress
	w.detach = true
	code, out, _ := w.run(Install, rwMgmtFlags(), true, "y", "y")
	if code != 130 || !strings.HasSuffix(out, "Detached: organization access goes on in CodeBuild. dormeto install status "+rwMgmtArgs+
		" follows it and then removes its build role and project.\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	if slices.Contains(w.writes, "mgmt codebuild:DeleteProject") || !w.mgmt.project || !w.mgmt.role {
		t.Errorf("the management account's project removed: %v", w.mgmtWrites())
	}
}

func TestInstallUpgradeRemindsOrganizationAccess(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	w.release("0.12.0", true)
	code, out, _ := w.run(Install, Request{Profile: "p", Region: "eu-west-1"}, true, "n")
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwInOrder(t, out, "  ! organization access: not deployed (no management profile)\n",
		"    Check it later: dormeto install check --management-profile <profile>; the dashboard's Organization access panel too.\n",
		"    Organization access is not applied again without --management-profile: each release's notes since 0.11.0 say whether the roles need it (https://dormeto.com/releases.html).\n",
		"\nBootstrap")

	// Not an upgrade: no reminder.
	w = rwNew(t)
	code, out, _ = w.run(Install, rwFlags(), true, "n")
	if code != 0 || strings.Contains(out, "not applied again") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

func TestCheckWithTheManagementProfileSurveys(t *testing.T) {
	w := rwNew(t)
	w.rwOrganization()
	w.readOnly = true
	r := Request{Profile: "p", Region: "eu-west-1", ManagementProfile: "m", Flags: Settings{AdminEmails: []string{"you@example.com"}}, Set: map[string]bool{"admin-emails": true}}
	code, out, asked := w.run(InstallCheck, r, true)
	if code != 0 || len(asked) != 0 {
		t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	rwInOrder(t, out, "\nChecks\n", "  ✓ administrators: you@example.com\n", "\nOrganization access\n", "  ✓ management account: 809706483662\n",
		"  ! member accounts: 1 ready, 1 to create, 0 discovery only; another manager's roles, left out: 777777777777\n", "  ✓ delegation policy: new\n")
	if strings.Contains(out, "not deployed (no management profile)") || strings.Contains(out, "Plan (organization access)") || len(w.writes) != 0 {
		t.Errorf("writes %v:\n%s", w.writes, out)
	}

	// A failed row of the survey fails the check.
	w = rwNew(t)
	w.rwOrganization()
	w.readOnly = true
	w.mgmt.id = "222222222222"
	if code, out, _ := w.run(InstallCheck, r, false); code != 1 || !strings.Contains(out, "  ✗ management account: the profile reaches 222222222222") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

// --access: organization access alone, the manager installed and not planned.
func TestInstallAccessAlone(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	w.rwOrganization()
	r := Request{Profile: "p", Region: "eu-west-1", ManagementProfile: "m", Access: true}
	code, out, asked := w.run(Install, r, true, "y")
	rwEqual(t, "questions", asked, []string{"Deploy organization access? [y/N]"})
	if code != 0 || !strings.HasSuffix(out, "  ✓ organization access: delegation policy created; roles in 2 accounts\n") || strings.Contains(out, "\nChecks\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	if len(w.actions()) != 0 {
		t.Errorf("manager builds %v", w.actions())
	}
	if b := w.mgmt.started(); len(b) != 1 || b[0].vars["RELEASE_VERSION"] != "0.11.0" {
		t.Errorf("organization builds %v", w.mgmt.actions())
	}

	// Without the management account's profile.
	w = rwNew(t)
	w.installed("0.11.0")
	r.ManagementProfile = ""
	code, out, _ = w.run(Install, r, true)
	if code != 2 || !strings.HasSuffix(out, "--access: organization access needs organization mode and the management account's profile (--management-profile)\n") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

// No account to give roles and the delegation policy already naming this manager: nothing to deploy, nothing asked.
func TestInstallOrganizationAccessNothingToDo(t *testing.T) {
	w := rwNew(t)
	w.rwOrganization()
	w.members = nil
	policy, _, err := MergePolicy(nil, []byte(orgTemplate), rwAccount, "prod")
	if err != nil {
		t.Fatal(err)
	}
	p := string(policy)
	w.policy = &p
	code, out, asked := w.run(Install, rwMgmtFlags(), true, "y")
	rwEqual(t, "questions", asked, []string{"Apply this plan? [y/N]"})
	if code != 0 || !strings.HasSuffix(out, "  ✓ delegation policy: already names this manager\nOrganization access: nothing to do.\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	if m := w.mgmtWrites(); len(m) != 0 {
		t.Errorf("wrote in the management account: %v", m)
	}
}

// A detached run's organization build still running: nothing is written over it, install status follows it.
func TestInstallOrganizationAccessAlreadyRunning(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	w.rwOrganization()
	w.mgmt.bucket, w.mgmt.role, w.mgmt.project, w.mgmt.logGroup = true, true, true, true
	w.mgmt.builds = []*rwBuild{{id: "dmt-prod-organization:b1", env: map[string]string{"ACTION": "apply"}, status: cbtypes.StatusTypeInProgress, start: rwT0.Add(-time.Minute)}}
	r := Request{Profile: "p", Region: "eu-west-1", ManagementProfile: "m", Access: true}
	code, out, _ := w.run(Install, r, true, "y")
	if code != 1 || !strings.HasSuffix(out, "Organization access is running already (build dmt-prod-organization:b1): dormeto install status "+rwMgmtArgs+" follows it\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	if m := w.mgmtWrites(); len(m) != 0 {
		t.Errorf("wrote in the management account: %v", m)
	}
}
