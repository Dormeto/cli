package install

// `dormeto install status` (status.go), through run_test.go's fakes.

import (
	"slices"
	"strings"
	"testing"
	"time"

	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
)

// rwLocal is a build's start time as status prints it.
func rwLocal(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }

var rwStatus = Request{Profile: "p", Region: "eu-west-1"}

func TestStatusVersionsTheLastBuildsAndTheChecks(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	w.release("0.12.0", true)
	w.readOnly = true
	code, out, asked := w.run(Status, rwStatus, true)
	if code != 0 || len(asked) != 0 {
		t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	rwInOrder(t, out,
		"Profile p: account 532105290256 (sandbox) in the organization o-vvk0115ixn (management 809706483662)\n",
		"Dormeto prod in 532105290256 eu-west-1\n",
		"  manager 0.11.0; latest release 0.12.0: an upgrade is available (dormeto install)\n",
		"  last plan dmt-prod-install:p0: succeeded "+rwLocal(rwT0.Add(-2*time.Hour))+"; last apply dmt-prod-install:a0: succeeded "+rwLocal(rwT0.Add(-time.Hour))+"\n",
		"  ✓ dashboard: "+rwDashboard+" answers\n",
		"  ✓ administrators: invitation sent to you@example.com (valid 7 days)\n",
		"  - organization access: checked by the manager at each discovery run (dashboard: Discovery, Organization access); "+
			"dormeto install check --management-profile <profile> "+rwArgs+" checks it here\n",
	)
	if strings.Contains(out, "is running") {
		t.Errorf("a build said running:\n%s", out)
	}

	// The latest installed: no upgrade; the dashboard tried once, without waiting.
	w = rwNew(t)
	w.installed("0.11.0")
	delete(w.http.bodies, rwDashboard)
	code, out, _ = w.run(Status, rwStatus, false)
	if code != 0 || !strings.Contains(out, "  manager 0.11.0; latest release 0.11.0\n") ||
		!strings.Contains(out, "  ! dashboard: "+rwDashboard+" does not answer yet") || !w.clock.Equal(rwT0) {
		t.Errorf("exit code %d, clock %s:\n%s", code, w.clock.Sub(rwT0), out)
	}
}

func TestStatusShowsTheTagsSortedByKey(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	s := w.saved()
	s.Tags = map[string]string{"owner": "platform", "cost-center": "1234", "team": ""}
	w.save(s)
	code, out, _ := w.run(Status, rwStatus, false)
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwInOrder(t, out,
		"Dormeto prod in 532105290256 eu-west-1\n",
		"  manager 0.11.0; latest release 0.11.0\n",
		"  tags: cost-center=1234, owner=platform, team=\n",
		"  last plan ",
	)

	w = rwNew(t)
	w.installed("0.11.0")
	if _, out, _ := w.run(Status, rwStatus, false); strings.Contains(out, "tags:") {
		t.Errorf("a tags line without tags:\n%s", out)
	}
}

func TestStatusNotAppliedYet(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	delete(w.objects, "prod/applied.json")
	delete(w.objects, "prod/outputs.json")
	w.builds = w.builds[:1] // the plan p0, never applied
	code, out, _ := w.run(Status, rwStatus, false)
	if code != 0 || !strings.Contains(out, "  manager not applied yet; latest release 0.11.0\n") || !strings.Contains(out, "; last apply: none\n") ||
		strings.Contains(out, "dashboard: "+rwDashboard) {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

func TestStatusWithoutAnInstall(t *testing.T) {
	w := rwNew(t)
	code, out, _ := w.run(Status, rwStatus, false)
	if code != 1 || !strings.HasSuffix(out, "no install of env prod in 532105290256 eu-west-1\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

func TestStatusTheLatestReleaseUnknown(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	w.http.status = map[string]int{ReleasesURL + "/latest.json": 503}
	code, out, _ := w.run(Status, rwStatus, false)
	if code != 0 || !strings.Contains(out, "  manager 0.11.0; latest release unknown (GET "+ReleasesURL+"/latest.json: 503 Service Unavailable)\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

// A running apply is followed, then the dashboard and the invitations checked once.
func TestStatusFollowsARunningApply(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	w.builds = append(w.builds, &rwBuild{id: "dmt-prod-install:r1", env: map[string]string{"ACTION": "apply", "PLAN_BUILD": "dmt-prod-install:p0"},
		status: cbtypes.StatusTypeSucceeded, start: rwT0.Add(-time.Minute), until: rwT0.Add(10 * time.Second)})
	code, out, _ := w.run(Status, rwStatus, false)
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwInOrder(t, out,
		"Build dmt-prod-install:r1 is running (apply)\n",
		"Apply (build dmt-prod-install:r1…)  succeeded",
		"  ✓ dashboard: "+rwDashboard+" answers\n",
		"Dormeto prod in 532105290256 eu-west-1\n",
	)
	if n := strings.Count(out, "dashboard: "+rwDashboard); n != 1 {
		t.Errorf("the dashboard checked %d times:\n%s", n, out)
	}
}

func TestStatusAccountMode(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	s := w.saved()
	s.Mode = "account"
	w.save(s)
	code, out, _ := w.run(Status, rwStatus, false)
	if code != 0 || !strings.HasSuffix(out, "  - organization access: not used (account deployment)\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

func TestStatusALastBuildFailed(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	s := w.saved()
	s.LastPlan = "dmt-prod-install:p1"
	w.save(s)
	w.builds = append(w.builds, &rwBuild{id: "dmt-prod-install:p1", env: map[string]string{"ACTION": "plan"}, status: cbtypes.StatusTypeFailed, start: rwT0.Add(-10 * time.Minute)})
	code, out, _ := w.run(Status, rwStatus, false)
	if code != 1 || !strings.Contains(out, "  last plan dmt-prod-install:p1: failed "+rwLocal(rwT0.Add(-10*time.Minute))+"; last apply dmt-prod-install:a0: succeeded ") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

// A detached organization run leaves the management account's project and role: install status removes them once the
// build has ended, and follows it first while it runs.
func TestStatusRemovesWhatADetachedOrganizationRunLeft(t *testing.T) {
	detached := func(t *testing.T) *rwWorld {
		w := rwNew(t)
		w.rwOrganization()
		w.mgmt.ends["apply"] = cbtypes.StatusTypeInProgress
		w.detach = true
		if code, out, _ := w.run(Install, rwMgmtFlags(), true, "y", "y", "y"); code != 130 {
			t.Fatalf("exit code %d:\n%s", code, out)
		}
		w.detach = false
		return w
	}
	r := Request{Profile: "p", Region: "eu-west-1", ManagementProfile: "m"}

	w := detached(t)
	w.mgmt.builds[0].status = cbtypes.StatusTypeSucceeded // it ended since
	code, out, _ := w.run(Status, r, false)
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwInOrder(t, out, "Dormeto prod in 532105290256 eu-west-1\n",
		"  removed the management account's build role and project left by a detached run\n",
		"\nOrganization access\n  ✓ management account: 809706483662\n")
	if w.mgmt.project || w.mgmt.role {
		t.Errorf("left: %v", w.mgmtWrites())
	}

	// Still running: followed (the accounts done out of accounts.txt's), then removed, its results said.
	w = detached(t)
	b := w.mgmt.builds[0]
	b.status, b.until = cbtypes.StatusTypeSucceeded, w.clock.Add(5*time.Second)
	w.status = &trkStatus{}
	code, out, _ = w.run(Status, r, false)
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	if !slices.Contains(w.status.updates, "Organization access (build dmt-prod-organization:b1…)  2/2  5s") {
		t.Errorf("status line: %q", w.status.updates)
	}
	if !strings.HasPrefix(w.status.end, "Organization access (build dmt-prod-organization:b1…)  succeeded  ") {
		t.Errorf("status line's end: %q", w.status.end)
	}
	rwInOrder(t, out, "Build dmt-prod-organization:b1 is running (organization access)\n",
		"  the management account's build role and project removed\n",
		"  ✓ organization access: delegation policy put; roles in 2 accounts\n",
		"\nOrganization access\n  ✓ management account: 809706483662\n")
	if w.mgmt.project || w.mgmt.role {
		t.Errorf("left: %v", w.mgmtWrites())
	}

	// Nothing left: nothing removed.
	w = rwNew(t)
	w.installed("0.11.0")
	w.rwOrganization()
	w.readOnly = true
	code, out, _ = w.run(Status, r, false)
	if code != 0 || strings.Contains(out, "removed") || !strings.Contains(out, "\nOrganization access\n  ✓ management account: 809706483662\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

// The last apply is the project's, not applied.json's (which only a successful apply writes): a failed apply after the
// last good one is shown failed.
func TestStatusAFailedApply(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	w.builds = append(w.builds,
		&rwBuild{id: "dmt-prod-install:p1", env: map[string]string{"ACTION": "plan"}, status: cbtypes.StatusTypeSucceeded, start: rwT0.Add(-20 * time.Minute)},
		&rwBuild{id: "dmt-prod-install:a1", env: map[string]string{"ACTION": "apply", "PLAN_BUILD": "dmt-prod-install:p1"}, status: cbtypes.StatusTypeFailed,
			start: rwT0.Add(-10 * time.Minute)})
	code, out, _ := w.run(Status, rwStatus, false)
	if code != 1 || !strings.Contains(out, "  manager 0.11.0; latest release 0.11.0\n") ||
		!strings.Contains(out, "  last plan dmt-prod-install:p1: succeeded "+rwLocal(rwT0.Add(-20*time.Minute))+"; last apply dmt-prod-install:a1: failed "+rwLocal(rwT0.Add(-10*time.Minute))+"\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

// A build the dashboard's update request started is shown as the last dashboard update, after the plan and the apply;
// failed and the last build, it fails status.
func TestStatusADashboardUpdate(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	w.builds = append(w.builds,
		&rwBuild{id: "dmt-prod-install:d1", env: map[string]string{"ACTION": "request"}, status: cbtypes.StatusTypeFailed, start: rwT0.Add(-5 * time.Minute)})
	code, out, _ := w.run(Status, rwStatus, false)
	if code != 1 || !strings.Contains(out, "; last apply dmt-prod-install:a0: succeeded "+rwLocal(rwT0.Add(-time.Hour))+
		"; last dashboard update dmt-prod-install:d1: failed "+rwLocal(rwT0.Add(-5*time.Minute))+"\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

// The exit code follows the last build of all, the dashboard's among them: a failed build followed by one that
// succeeded does not fail status.
func TestStatusTheLastBuildDecidesTheExitCode(t *testing.T) {
	for name, c := range map[string]struct {
		builds []*rwBuild
		code   int
	}{
		"a failed plan, then a dashboard update that succeeded": {[]*rwBuild{
			{id: "dmt-prod-install:p1", env: map[string]string{"ACTION": "plan"}, status: cbtypes.StatusTypeFailed, start: rwT0.Add(-20 * time.Minute)},
			{id: "dmt-prod-install:d1", env: map[string]string{"ACTION": "request"}, status: cbtypes.StatusTypeSucceeded, start: rwT0.Add(-10 * time.Minute)},
		}, 0},
		"a failed dashboard update, then a plan that succeeded": {[]*rwBuild{
			{id: "dmt-prod-install:d1", env: map[string]string{"ACTION": "request"}, status: cbtypes.StatusTypeFailed, start: rwT0.Add(-20 * time.Minute)},
			{id: "dmt-prod-install:p1", env: map[string]string{"ACTION": "plan"}, status: cbtypes.StatusTypeSucceeded, start: rwT0.Add(-10 * time.Minute)},
		}, 0},
		"a dashboard update that succeeded, then one stopped": {[]*rwBuild{
			{id: "dmt-prod-install:d1", env: map[string]string{"ACTION": "request"}, status: cbtypes.StatusTypeSucceeded, start: rwT0.Add(-20 * time.Minute)},
			{id: "dmt-prod-install:d2", env: map[string]string{"ACTION": "request"}, status: cbtypes.StatusTypeStopped, start: rwT0.Add(-10 * time.Minute)},
		}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			w := rwNew(t)
			w.installed("0.11.0")
			w.builds = append(w.builds, c.builds...)
			if code, out, _ := w.run(Status, rwStatus, false); code != c.code {
				t.Errorf("exit code %d, want %d:\n%s", code, c.code, out)
			}
		})
	}
}
