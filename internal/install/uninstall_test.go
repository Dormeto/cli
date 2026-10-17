package install

// `dormeto uninstall` and `dormeto uninstall apply` (uninstall.go), and the plan's kind that keeps each explicit apply
// to its own kind of plan (ruling R15), through run_test.go's fakes.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
)

// install apply never applies a destroy plan: uninstall apply does.
func TestApplyRefusesADestroyPlan(t *testing.T) {
	w := rwPlanned(t)
	s := w.saved()
	s.LastPlanKind = "destroy"
	w.save(s)
	code, out, _ := w.run(Apply, Request{Profile: "p", Region: "eu-west-1"}, false)
	if code != 1 || !strings.HasSuffix(out, "the last plan destroys the manager: dormeto uninstall apply applies it\n") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

// A new plan of install is an install plan, whatever the kind of the last one.
func TestInstallPlanIsOfKindApply(t *testing.T) {
	w := rwPlanned(t)
	s := w.saved()
	s.LastPlanKind = "destroy"
	w.save(s)
	code, out, _ := w.run(Install, Request{Profile: "p", Region: "eu-west-1"}, false)
	if s := w.saved(); code != 0 || s.LastPlan != "dmt-prod-install:b1" || s.LastPlanKind != "apply" {
		t.Errorf("exit code %d, install.json %+v:\n%s", code, s, out)
	}
}

// What the plan lifting the deletion protection writes: the table and the user pool updated.
const unProtection = `{"format_version":"1.2","resource_changes":[` +
	`{"address":"module.dormeto.aws_cognito_user_pool.users","type":"aws_cognito_user_pool","change":{"actions":["update"]}},` +
	`{"address":"module.dormeto.aws_dynamodb_table.manager","type":"aws_dynamodb_table","change":{"actions":["update"]}},` +
	`{"address":"module.dormeto.aws_lambda_function.api","type":"aws_lambda_function","change":{"actions":["no-op"]}}]}`

const (
	unEvent     = "Is event-driven refresh off (Discovery, Perimeter settings), with a discovery run since? Its forwarders in member accounts are removed only by a run. [y/N]"
	unLift      = "Lift the deletion protection? [y/N]"
	unDestroy   = "Destroy the manager? This deletes its users, its table and its history. [y/N]"
	unBootstrap = "Remove the install's bucket (its state and answers), role, log group and project too? [y/N]"
	unWarning   = "! Event-driven refresh must be off (Discovery, Perimeter settings), with a discovery run since: its forwarders in member accounts are removed only by a run.\n"
	unKept      = "Kept the bucket dmt-install-532105290256-eu-west-1 (its answers serve a reinstall) and the project; dormeto uninstall " + rwArgs + " again in a terminal removes them.\n"
	unRemoved   = "Removed the bucket dmt-install-532105290256-eu-west-1, the role dmt-prod-install, the log group /aws/codebuild/dmt-prod-install and the project dmt-prod-install.\n"
	unAccess    = "Organization access is not removed: its roles keep working for a reinstall. To remove them and Dormeto's statement of the delegation policy: https://dormeto.com/organization-access.html\n"
	unRefused   = "offboard the workloads and uninstall the routers in the dashboard (Routers) first.\n"
)

var unRequest = Request{Profile: "p", Region: "eu-west-1"}

// unNew is prod installed at 0.11.0 with its deletion protection on (the module's default), the plan lifting it ready.
func unNew(t *testing.T) *rwWorld {
	w := rwNew(t)
	w.installed("0.11.0")
	w.plan = unProtection
	return w
}

// unProtectionOff is install.json with the deletion protection off.
func unProtectionOff(w *rwWorld) {
	s := w.saved()
	s.Vars = map[string]json.RawMessage{"deletion_protection": json.RawMessage("false")}
	w.save(s)
}

// gone tells that the install's bucket, role, log group and project are removed.
func (w *rwWorld) gone() bool { return !w.bucket && !w.role && !w.logGroup && !w.project }

func TestUninstallRefusesWhileRoutersAreInstalled(t *testing.T) {
	for _, c := range []struct {
		name  string
		edit  func(w *rwWorld)
		where string
	}{
		{"an item in the table", func(w *rwWorld) { w.routers = 1 }, "table dmt-prod-manager"},
		// The second page of a region's functions; a name only containing dmtr- is not a router's.
		{"a function in a region", func(w *rwWorld) {
			w.functions["eu-south-1"] = []string{"other-a", "other-b", "dmtr-prod"}
			w.functions["us-east-1"] = []string{"my-dmtr-x"}
		}, "dmtr- functions in eu-south-1"},
		{"both", func(w *rwWorld) {
			w.routers = 1
			w.functions["eu-south-1"] = []string{"dmtr-prod"}
			w.functions["us-east-1"] = []string{"dmtr-prod"}
		}, "table dmt-prod-manager; dmtr- functions in eu-south-1, us-east-1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := unNew(t)
			c.edit(w)
			code, out, asked := w.run(Uninstall, unRequest, true)
			if code != 1 || len(asked) != 0 || !strings.HasSuffix(out, "Routers are installed ("+c.where+"): "+unRefused) {
				t.Errorf("exit code %d, asked %q:\n%s", code, asked, out)
			}
			if len(w.writes) != 0 {
				t.Errorf("writes: %v", w.writes)
			}
			// Every enabled region's functions are read, not the region opted out.
			rwEqual(t, "regions listed", w.listed, []string{"eu-south-1", "eu-west-1", "us-east-1"})
		})
	}

	// The table absent and no router's function: the uninstall goes on.
	w := unNew(t)
	w.noTable = true
	w.functions["eu-west-1"] = []string{"dmt-prod-manager-api"}
	code, out, asked := w.run(Uninstall, unRequest, true, "n")
	rwEqual(t, "questions", asked, []string{unEvent})
	if code != 1 || !strings.Contains(out, "Routers: none\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

// A region whose functions cannot be listed (a policy of the organization denying it) is said; in a terminal, the
// person decides whether to go on; the manager's own region must be checked.
func TestUninstallARegionDenied(t *testing.T) {
	w := unNew(t)
	w.lambdaDenied = map[string]bool{"eu-south-1": true}
	code, out, asked := w.run(Uninstall, unRequest, true, "y", "n")
	rwEqual(t, "questions", asked, []string{"Continue? [y/N]", unEvent})
	if code != 1 || !strings.Contains(out, "\nRouters: none found (1 region not checked: eu-south-1)\n") || strings.Contains(out, "Routers: none\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}

	// No: nothing removed.
	w = unNew(t)
	w.lambdaDenied = map[string]bool{"eu-south-1": true, "us-east-1": true}
	code, out, _ = w.run(Uninstall, unRequest, true, "n")
	if code != 1 || !strings.HasSuffix(out, "Routers: none found (2 regions not checked: eu-south-1, us-east-1)\n"+
		"Nothing was removed: look for dmtr- functions in eu-south-1, us-east-1, then run dormeto uninstall "+rwArgs+" again.\n") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}

	// Without a terminal: said, and the uninstall goes on.
	w = unNew(t)
	w.lambdaDenied = map[string]bool{"eu-south-1": true}
	code, out, _ = w.run(Uninstall, unRequest, false)
	if code != 0 || !strings.Contains(out, "Routers: none found (1 region not checked: eu-south-1)\n"+unWarning) {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan"})

	// The manager's own region not checked: refused.
	for _, terminal := range []bool{true, false} {
		w = unNew(t)
		w.lambdaDenied = map[string]bool{"eu-west-1": true}
		code, out, asked = w.run(Uninstall, unRequest, terminal)
		if code != 1 || len(asked) != 0 || len(w.writes) != 0 ||
			!strings.HasSuffix(out, "Routers not checked in eu-west-1, the manager's region (lambda:ListFunctions refused): nothing was removed.\n") {
			t.Errorf("terminal %v: exit code %d, asked %q, writes %v:\n%s", terminal, code, asked, w.writes, out)
		}
	}
}

func TestUninstallEventDrivenRefreshNotOff(t *testing.T) {
	w := unNew(t)
	code, out, asked := w.run(Uninstall, unRequest, true, "n")
	rwEqual(t, "questions", asked, []string{unEvent})
	if code != 1 || !strings.HasSuffix(out, "Nothing was removed: turn event-driven refresh off in the dashboard (Discovery, Perimeter settings), "+
		"run discovery, then dormeto uninstall "+rwArgs+" again.\n") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}

	w = unNew(t)
	code, out, _ = w.run(Uninstall, unRequest, true, "^C")
	if code != 130 || !strings.HasSuffix(out, "Routers: none\n\nInterrupted: nothing was removed.\n") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

func TestUninstallLiftsTheProtectionThenDestroys(t *testing.T) {
	w := unNew(t)
	code, out, asked := w.run(Uninstall, unRequest, true, "y", "y", "y", "y")
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "questions", asked, []string{unEvent, unLift, unDestroy, unBootstrap})
	rwInOrder(t, out,
		"Profile p: account 532105290256",
		"Routers: none\n",
		"\nDeletion protection is on: a plan lifts it first.\n",
		"Plan (build dmt-prod-install:b1…)  succeeded",
		"Plan (build dmt-prod-install:b1)\n  manager 0.11.0: 0 to add, 2 to change, 0 to destroy\n",
		"Apply (build dmt-prod-install:b2…)  succeeded",
		"  ✓ applied: 0 added, 2 changed, 0 destroyed\n",
		"Destroy plan (build dmt-prod-install:b3…)  succeeded",
		"Plan (build dmt-prod-install:b3)\n  manager 0.11.0: 0 to add, 0 to change, 3 to destroy\n",
		"    destroyed: module.dormeto.aws_cognito_user_pool.users, module.dormeto.aws_dynamodb_table.manager, module.dormeto.aws_lambda_function.api\n",
		"  ! this plan destroys or replaces resources (above)\n",
		"Destroy (build dmt-prod-install:b4…)  succeeded",
		"  ✓ destroyed: 3 resources\n",
		unRemoved,
		unAccess,
	)
	if !strings.HasSuffix(out, unAccess) || strings.Contains(out, "Next: sign in") || strings.Contains(out, "dashboard:") {
		t.Errorf("output:\n%s", out)
	}
	base := ReleasesURL + "/0.11.0"
	b := w.started()
	rwEqual(t, "builds", w.actions(), []string{"plan", "apply", "plan-destroy", "apply-destroy"})
	rwEqual(t, "protection plan's variables", b[0].vars, map[string]string{"ACTION": "plan", "RELEASE_BASE": base, "RELEASE_VERSION": "0.11.0"})
	rwEqual(t, "protection apply's variables", b[1].vars,
		map[string]string{"ACTION": "apply", "PLAN_BUILD": "dmt-prod-install:b1", "RELEASE_BASE": base, "RELEASE_VERSION": "0.11.0"})
	rwEqual(t, "destroy plan's variables", b[2].vars, map[string]string{"ACTION": "plan-destroy", "RELEASE_BASE": base, "RELEASE_VERSION": "0.11.0"})
	rwEqual(t, "destroy's variables", b[3].vars,
		map[string]string{"ACTION": "apply-destroy", "PLAN_BUILD": "dmt-prod-install:b3", "RELEASE_BASE": base, "RELEASE_VERSION": "0.11.0"})
	rwEqual(t, "writes", w.writes, []string{
		"s3:PutObject prod/config/main.tf.json", "s3:PutObject prod/install.json", "codebuild:StartBuild plan", "s3:PutObject prod/install.json",
		"codebuild:StartBuild apply",
		"s3:PutObject prod/install.json", "codebuild:StartBuild plan-destroy", "s3:PutObject prod/install.json",
		"codebuild:StartBuild apply-destroy", "s3:PutObject prod/install.json",
		"codebuild:DeleteProject", "iam:DetachRolePolicy", "iam:DeleteRole dmt-prod-install", "logs:DeleteLogGroup", "s3:DeleteObjects", "s3:DeleteBucket",
	})
	if !w.gone() || len(w.objects) != 0 {
		t.Errorf("left: bucket %v, role %v, log group %v, project %v, objects %v", w.bucket, w.role, w.logGroup, w.project, w.objects)
	}
}

// install.json's version and release are the last plan's: after a declined upgrade, the newer one. The protection and
// destroy plans run with what is installed (applied.json's version, its release on releases.dormeto.com).
func TestUninstallPlansWithTheInstalledVersion(t *testing.T) {
	w := unNew(t)
	w.release("0.12.0", true)
	s := w.saved()
	s.Version, s.ReleaseBase = "0.12.0", ReleasesURL+"/0.12.0" // a plan of 0.12.0, not applied
	w.save(s)
	code, out, _ := w.run(Uninstall, unRequest, true, "y", "y", "y", "n")
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwInOrder(t, out, "Plan (build dmt-prod-install:b1)\n  manager 0.11.0: 0 to add, 2 to change, 0 to destroy\n",
		"Plan (build dmt-prod-install:b3)\n  manager 0.11.0: 0 to add, 0 to change, 3 to destroy\n")
	base := ReleasesURL + "/0.11.0"
	for _, b := range w.started() {
		if b.vars["RELEASE_VERSION"] != "0.11.0" || b.vars["RELEASE_BASE"] != base {
			t.Errorf("%s build's variables %v", b.vars["ACTION"], b.vars)
		}
	}
	if s := w.saved(); s.Version != "0.11.0" || s.ReleaseBase != base {
		t.Errorf("install.json: %+v", s)
	}
}

// The version installed from a local release, install.json naming a later plan's: its release is asked for (--release),
// then uploaded to the install's bucket for the plans.
func TestUninstallALocalInstalledVersionNeedsItsRelease(t *testing.T) {
	w := unNew(t)
	w.objects["prod/applied.json"] = `{"version":"0.11.0-dev","build":"dmt-prod-install:a0"}`
	local := "s3://dmt-install-532105290256-eu-west-1/prod/release"
	s := w.saved()
	s.Version, s.ReleaseBase = "0.11.1-dev", local
	w.save(s)
	code, out, _ := w.run(Uninstall, unRequest, false)
	if code != 1 || !strings.HasSuffix(out, "manager 0.11.0-dev is installed, install.json names 0.11.1-dev (a later plan's): manager 0.11.0-dev is not "+
		"published on releases.dormeto.com; give its local release: dormeto uninstall "+rwArgs+" --release <the directory of release 0.11.0-dev>\n") ||
		len(w.writes) != 0 {
		t.Fatalf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}

	// A release of another version is refused.
	r := unRequest
	r.Release = writeLocal(t, "0.11.1-dev", nil)
	if code, out, _ = w.run(Uninstall, r, false); code != 1 || !strings.Contains(out, "0.11.0-dev") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}

	r.Release = writeLocal(t, "0.11.0-dev", nil)
	code, out, _ = w.run(Uninstall, r, false)
	if code != 0 || !strings.Contains(out, "  manager 0.11.0-dev: 0 to add, 2 to change, 0 to destroy\n") {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	if b := w.started(); len(b) != 1 || b[0].vars["RELEASE_VERSION"] != "0.11.0-dev" || b[0].vars["RELEASE_BASE"] != local {
		t.Errorf("builds %v", w.actions())
	}
	if w.objects["prod/release/terraform-aws-dormeto-0.11.0-dev.zip"] != "content of terraform-aws-dormeto-0.11.0-dev.zip" {
		t.Errorf("the local release not uploaded: %v", w.writes)
	}
}

// The protection lifted in install.json, its plan applied, then the destroy: each written as it is.
func TestUninstallWritesTheProtectionOffThenForgetsTheDestroyPlan(t *testing.T) {
	w := unNew(t)
	code, out, _ := w.run(Uninstall, unRequest, true, "y", "y", "y", "n")
	if code != 0 || !strings.HasSuffix(out, unKept+unAccess) {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	if cfg := w.objects["prod/config/main.tf.json"]; !strings.Contains(cfg, `"deletion_protection": false`) {
		t.Errorf("main.tf.json:\n%s", cfg)
	}
	s := w.saved()
	if string(s.Vars["deletion_protection"]) != "false" || s.LastPlan != "" || s.LastPlanKind != "" || s.Version != "0.11.0" || s.AdminEmails[0] != "you@example.com" {
		t.Errorf("install.json: %+v", s)
	}
	if _, ok := w.objects["prod/applied.json"]; ok || w.gone() {
		t.Errorf("applied.json %q, bootstrap gone %v", w.objects["prod/applied.json"], w.gone())
	}
}

func TestUninstallProtectionAlreadyOff(t *testing.T) {
	w := unNew(t)
	unProtectionOff(w)
	code, out, asked := w.run(Uninstall, unRequest, true, "y", "y", "n")
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "questions", asked, []string{unEvent, unDestroy, unBootstrap})
	rwEqual(t, "builds", w.actions(), []string{"plan-destroy", "apply-destroy"})
	if strings.Contains(out, "Deletion protection") || !strings.HasSuffix(out, "  ✓ destroyed: 3 resources\n"+unKept+unAccess) {
		t.Errorf("output:\n%s", out)
	}

	// Nothing applied any more: straight to the bootstrap, asked again.
	code, out, asked = w.run(Uninstall, unRequest, true, "y")
	rwEqual(t, "questions", asked, []string{unBootstrap})
	if code != 0 || !strings.HasSuffix(out, unRemoved+unAccess) || !w.gone() {
		t.Errorf("exit code %d, gone %v:\n%s", code, w.gone(), out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan-destroy", "apply-destroy"})
}

// The protection off in install.json, but the plan that lifts it not applied yet: it is planned again, not destroyed
// with the protection on.
func TestUninstallPlansTheProtectionAgainWhenNotApplied(t *testing.T) {
	w := unNew(t)
	code, out, _ := w.run(Uninstall, unRequest, false)
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	code, out, _ = w.run(Uninstall, unRequest, false)
	if code != 0 || !strings.HasSuffix(out, "Plan saved. Apply it: dormeto install apply "+rwArgs+", then run dormeto uninstall "+rwArgs+" again\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan", "plan"})
}

func TestUninstallWithoutATerminal(t *testing.T) {
	w := unNew(t)
	w.runFor = time.Minute // each build starts after the one before ended
	code, out, asked := w.run(Uninstall, unRequest, false)
	if code != 0 || len(asked) != 0 {
		t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	rwInOrder(t, out, "Routers: none\n", unWarning, "\nDeletion protection is on: a plan lifts it first.\n",
		"  manager 0.11.0: 0 to add, 2 to change, 0 to destroy\n")
	if !strings.HasSuffix(out, "Plan saved. Apply it: dormeto install apply "+rwArgs+", then run dormeto uninstall "+rwArgs+" again\n") {
		t.Errorf("output:\n%s", out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan"})
	if s := w.saved(); s.LastPlan != "dmt-prod-install:b1" || s.LastPlanKind != "apply" || string(s.Vars["deletion_protection"]) != "false" {
		t.Errorf("install.json: %+v", s)
	}

	// The plan applied by its command, uninstall stops after the destroy plan.
	if code, out, _ = w.run(Apply, unRequest, false); code != 0 {
		t.Fatalf("install apply: exit code %d:\n%s", code, out)
	}
	code, out, asked = w.run(Uninstall, unRequest, false)
	if code != 0 || len(asked) != 0 || strings.Contains(out, "Deletion protection") {
		t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	rwInOrder(t, out, unWarning, "Plan (build dmt-prod-install:b3)\n  manager 0.11.0: 0 to add, 0 to change, 3 to destroy\n",
		"Plan saved. Apply it: dormeto uninstall apply "+rwArgs+"\n")
	if !strings.HasSuffix(out, "Plan saved. Apply it: dormeto uninstall apply "+rwArgs+"\n") {
		t.Errorf("output:\n%s", out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan", "apply", "plan-destroy"})
	if s := w.saved(); s.LastPlan != "dmt-prod-install:b3" || s.LastPlanKind != "destroy" {
		t.Errorf("install.json: %+v", s)
	}
	// install apply does not apply it.
	if code, out, _ = w.run(Apply, unRequest, false); code != 1 || !strings.HasSuffix(out, "the last plan destroys the manager: dormeto uninstall apply applies it\n") {
		t.Errorf("install apply: exit code %d:\n%s", code, out)
	}

	// uninstall apply applies it without a question, and keeps the bootstrap.
	code, out, asked = w.run(UninstallApply, unRequest, false)
	if code != 0 || len(asked) != 0 {
		t.Fatalf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	rwInOrder(t, out, "Plan (build dmt-prod-install:b3)\n  manager 0.11.0: 0 to add, 0 to change, 3 to destroy\n",
		"Destroy (build dmt-prod-install:b4…)  succeeded", "  ✓ destroyed: 3 resources\n")
	if !strings.HasSuffix(out, "  ✓ destroyed: 3 resources\n"+unKept+unAccess) || strings.Contains(out, "Event-driven") {
		t.Errorf("output:\n%s", out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan", "apply", "plan-destroy", "apply-destroy"})
	rwEqual(t, "destroy's plan", w.started()[3].vars["PLAN_BUILD"], "dmt-prod-install:b3")
	if _, ok := w.objects["prod/applied.json"]; ok || w.gone() || w.saved().LastPlan != "" {
		t.Errorf("applied.json %q, bootstrap gone %v, install.json %+v", w.objects["prod/applied.json"], w.gone(), w.saved())
	}
}

func TestUninstallApplyRefusesAnotherPlan(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(w *rwWorld)
		want string
	}{
		{"an install plan", func(*rwWorld) {}, "the last plan is not a destroy plan: dormeto uninstall\n"},
		{"no plan", func(w *rwWorld) {
			s := w.saved()
			s.LastPlan = ""
			w.save(s)
		}, "no destroy plan: run dormeto uninstall\n"},
		{"no install", func(w *rwWorld) { delete(w.objects, "prod/install.json") }, "no install of env prod in 532105290256 eu-west-1\n"},
		{"nothing applied", func(w *rwWorld) {
			s := w.saved()
			s.LastPlanKind = "destroy"
			w.save(s)
			w.objects["prod/manager.tfstate"] = rwNoneState
		}, "nothing applied to destroy: dormeto uninstall " + rwArgs + " removes the bootstrap\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := unNew(t)
			c.edit(w)
			code, out, _ := w.run(UninstallApply, unRequest, false)
			if code != 1 || !strings.HasSuffix(out, c.want) || len(w.writes) != 0 {
				t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
			}
		})
	}
}

// Nothing applied is the Terraform state's say: none, or one without a resource (applied.json present or not).
func TestUninstallNothingAppliedGoesToTheBootstrap(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(w *rwWorld)
	}{
		{"no state", func(w *rwWorld) { delete(w.objects, "prod/manager.tfstate"); delete(w.objects, "prod/applied.json") }},
		{"a state without resources", func(w *rwWorld) { w.objects["prod/manager.tfstate"] = rwNoneState }},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := unNew(t)
			c.edit(w)
			w.routers = 1 // not read: nothing is applied
			code, out, asked := w.run(Uninstall, unRequest, true, "y")
			rwEqual(t, "questions", asked, []string{unBootstrap})
			if code != 0 || !strings.HasSuffix(out, unRemoved+unAccess) || !w.gone() || len(w.actions()) != 0 || len(w.listed) != 0 {
				t.Errorf("exit code %d, gone %v, builds %v, listed %v:\n%s", code, w.gone(), w.actions(), w.listed, out)
			}
		})
	}

	// Without a terminal: kept.
	w := unNew(t)
	delete(w.objects, "prod/manager.tfstate")
	code, out, asked := w.run(Uninstall, unRequest, false)
	if code != 0 || len(asked) != 0 || !strings.HasSuffix(out, unKept+unAccess) || w.gone() || len(w.writes) != 0 {
		t.Errorf("exit code %d, asked %q, writes %v:\n%s", code, asked, w.writes, out)
	}
}

func TestUninstallAccountModeSaysNothingOfOrganizationAccess(t *testing.T) {
	w := unNew(t)
	s := w.saved()
	s.Mode, s.OrganizationID = "account", ""
	w.save(s)
	delete(w.objects, "prod/manager.tfstate")
	code, out, _ := w.run(Uninstall, unRequest, true, "n")
	if code != 0 || !strings.HasSuffix(out, unKept) || strings.Contains(out, "Organization access") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

func TestUninstallWithoutAnInstall(t *testing.T) {
	w := rwNew(t)
	code, out, _ := w.run(Uninstall, unRequest, true)
	if code != 1 || !strings.HasSuffix(out, "no install of env prod in 532105290256 eu-west-1\n") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

func TestUninstallNotConfirmedKeepsThePlans(t *testing.T) {
	// No to the protection's plan: kept for install apply, nothing destroyed.
	w := unNew(t)
	code, out, _ := w.run(Uninstall, unRequest, true, "y", "n")
	if code != 0 || !strings.HasSuffix(out, "Not applied; the plan is kept: dormeto install apply "+rwArgs+", then dormeto uninstall "+rwArgs+" again. "+
		"install.json now has deletion_protection=false: dormeto install "+rwArgs+" --var deletion_protection=true puts it back.\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan"})

	// No, or Ctrl-C, to the destroy: kept for uninstall apply.
	for _, c := range []struct {
		answer, newline string // Ctrl-C ends the question's line
		code            int
	}{{"n", "", 0}, {"^C", "\n", 130}} {
		w = unNew(t)
		unProtectionOff(w)
		code, out, _ = w.run(Uninstall, unRequest, true, "y", c.answer)
		if code != c.code || !strings.HasSuffix(out, "(above)\n"+c.newline+"Not applied; the destroy plan is kept: dormeto uninstall apply "+rwArgs+"\n") {
			t.Errorf("%s: exit code %d:\n%s", c.answer, code, out)
		}
		rwEqual(t, "builds", w.actions(), []string{"plan-destroy"})
		if s := w.saved(); s.LastPlan != "dmt-prod-install:b1" || s.LastPlanKind != "destroy" {
			t.Errorf("install.json: %+v", s)
		}
	}
}

// --yes answers the event-driven refresh question and keeps the bootstrap (the default answer); it confirms no plan.
func TestUninstallYesConfirmsNoPlan(t *testing.T) {
	w := unNew(t)
	unProtectionOff(w)
	r := unRequest
	r.Yes = true
	code, out, asked := w.run(Uninstall, r, true, "y")
	rwEqual(t, "questions", asked, []string{unDestroy})
	if code != 0 || !strings.Contains(out, unWarning) || !strings.HasSuffix(out, unKept+unAccess) || w.gone() {
		t.Errorf("exit code %d, gone %v:\n%s", code, w.gone(), out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan-destroy", "apply-destroy"})

	// Regions not checked: said, and no question.
	w = unNew(t)
	unProtectionOff(w)
	w.lambdaDenied = map[string]bool{"eu-south-1": true}
	code, out, asked = w.run(Uninstall, r, true, "n")
	rwEqual(t, "questions", asked, []string{unDestroy})
	if code != 0 || !strings.Contains(out, "Routers: none found (1 region not checked: eu-south-1)\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

func TestUninstallAFailedDestroy(t *testing.T) {
	w := unNew(t)
	unProtectionOff(w)
	w.ends["apply-destroy"] = cbtypes.StatusTypeFailed
	code, out, asked := w.run(Uninstall, unRequest, true, "y", "y")
	rwEqual(t, "questions", asked, []string{unEvent, unDestroy})
	if code != 1 {
		t.Errorf("exit code %d", code)
	}
	rwInOrder(t, out, "Destroy failed\n", "  Error: creating the user pool: AccessDenied\n", ConsoleURL(Names(rwAccount, "eu-west-1", "prod"), "dmt-prod-install:b2"))
	if strings.Contains(out, "✓ destroyed") || strings.Contains(out, "Kept the bucket") || w.saved().LastPlan != "dmt-prod-install:b1" {
		t.Errorf("install.json %+v:\n%s", w.saved(), out)
	}
}

// Ctrl-C while the destroy is followed: it goes on, and what follows it is named.
func TestUninstallDetachesFromTheDestroy(t *testing.T) {
	w := unNew(t)
	unProtectionOff(w)
	if code, out, _ := w.run(Uninstall, unRequest, false); code != 0 { // the destroy plan saved
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	w.ends["apply-destroy"] = cbtypes.StatusTypeInProgress
	w.detach = true
	code, out, _ := w.run(UninstallApply, unRequest, false)
	if code != 130 || !strings.HasSuffix(out, "Detached: the destroy goes on in CodeBuild. Follow it: dormeto install status "+rwArgs+
		"; once done, dormeto uninstall "+rwArgs+" removes the bootstrap.\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

// The protection set on by --var deletion_protection=true is lifted as the module's default is.
func TestUninstallProtectionSetOnIsLifted(t *testing.T) {
	w := unNew(t)
	s := w.saved()
	s.Vars = map[string]json.RawMessage{"deletion_protection": json.RawMessage("true"), "retention_days": json.RawMessage("30")}
	w.save(s)
	code, out, _ := w.run(Uninstall, unRequest, false)
	if code != 0 || !strings.Contains(out, "\nDeletion protection is on: a plan lifts it first.\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan"})
	rwEqual(t, "vars", w.saved().Vars, map[string]json.RawMessage{"deletion_protection": json.RawMessage("false"), "retention_days": json.RawMessage("30")})
}

// uninstall apply is the confirmation: in a terminal too, it asks nothing, and keeps the bootstrap.
func TestUninstallApplyInATerminalAsksNothing(t *testing.T) {
	w := unNew(t)
	unProtectionOff(w)
	if code, out, _ := w.run(Uninstall, unRequest, false); code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	code, out, asked := w.run(UninstallApply, unRequest, true)
	if code != 0 || len(asked) != 0 || !strings.HasSuffix(out, "  ✓ destroyed: 3 resources\n"+unKept+unAccess) || w.gone() {
		t.Errorf("exit code %d, asked %q, gone %v:\n%s", code, asked, w.gone(), out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan-destroy", "apply-destroy"})
}

// A first install whose apply failed halfway: resources in the state, no applied.json. The manager's resources are
// destroyed (the protection first), never left behind by removing the bucket and its state.
func TestUninstallAFailedFirstApplyIsDestroyed(t *testing.T) {
	w := unNew(t)
	delete(w.objects, "prod/applied.json")
	code, out, asked := w.run(Uninstall, unRequest, true, "y", "y", "y", "n")
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "questions", asked, []string{unEvent, unLift, unDestroy, unBootstrap})
	rwEqual(t, "builds", w.actions(), []string{"plan", "apply", "plan-destroy", "apply-destroy"})
	if !strings.HasSuffix(out, "  ✓ destroyed: 3 resources\n"+unKept+unAccess) {
		t.Errorf("output:\n%s", out)
	}

	// uninstall apply too: a destroy plan, the state's resources, no applied.json.
	w = unNew(t)
	delete(w.objects, "prod/applied.json")
	s := w.saved()
	s.LastPlan, s.LastPlanKind = "dmt-prod-install:d1", "destroy"
	w.save(s)
	w.objects["prod/plans/dmt-prod-install:d1/plan.json"] = rwDestroy
	w.builds = append(w.builds, &rwBuild{id: "dmt-prod-install:d1", env: map[string]string{"ACTION": "plan-destroy"}, status: cbtypes.StatusTypeSucceeded,
		start: rwT0.Add(-time.Minute)})
	code, out, _ = w.run(UninstallApply, unRequest, false)
	if code != 0 || !strings.HasSuffix(out, "  ✓ destroyed: 3 resources\n"+unKept+unAccess) {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"apply-destroy"})
}

// The protection off in install.json but its plan never started (a build running refused it): planned again.
func TestUninstallPlansTheProtectionAgainWhenItsPlanDidNotStart(t *testing.T) {
	w := unNew(t)
	running := &rwBuild{id: "dmt-prod-install:r0", status: cbtypes.StatusTypeInProgress, start: rwT0.Add(-time.Minute)}
	w.builds = append(w.builds, running)
	code, out, _ := w.run(Uninstall, unRequest, false)
	if code != 1 || !strings.HasSuffix(out, "build dmt-prod-install:r0 is running: dormeto install status follows it\n") {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	if s := w.saved(); string(s.Vars["deletion_protection"]) != "false" || s.LastPlan != "" {
		t.Fatalf("install.json: %+v", s)
	}
	running.status = cbtypes.StatusTypeSucceeded
	code, out, _ = w.run(Uninstall, unRequest, false)
	if code != 0 || !strings.Contains(out, "\nDeletion protection is on: a plan lifts it first.\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan"})
}

// The protection's plan failed: planned again, not taken as lifted.
func TestUninstallPlansTheProtectionAgainWhenItsPlanFailed(t *testing.T) {
	w := unNew(t)
	w.ends["plan"] = cbtypes.StatusTypeFailed
	code, out, _ := w.run(Uninstall, unRequest, false)
	if code != 1 || !strings.Contains(out, "Plan failed\n") {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	delete(w.ends, "plan")
	code, out, _ = w.run(Uninstall, unRequest, false)
	if code != 0 || !strings.Contains(out, "\nDeletion protection is on: a plan lifts it first.\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan", "plan"})
}

// AWS failing to say how the last plan stands is an error, not a protection lifted.
func TestUninstallAnErrorReadingTheLastPlan(t *testing.T) {
	w := unNew(t)
	unProtectionOff(w)
	w.batchErr = chkAPI("ThrottlingException")
	code, out, _ := w.run(Uninstall, unRequest, false)
	if code != 1 || !strings.HasSuffix(out, "get build dmt-prod-install:p0: api error ThrottlingException: ThrottlingException message\n") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

// The destroy plan, as the protection's, forgets the last plan before it starts: a plan refused leaves none to apply.
func TestUninstallTheDestroyPlanForgetsTheLastPlan(t *testing.T) {
	w := unNew(t)
	unProtectionOff(w)
	w.builds = append(w.builds, &rwBuild{id: "dmt-prod-install:r0", status: cbtypes.StatusTypeInProgress, start: rwT0.Add(-time.Minute)})
	code, out, _ := w.run(Uninstall, unRequest, false)
	if code != 1 || !strings.HasSuffix(out, "build dmt-prod-install:r0 is running: dormeto install status follows it\n") {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	if s := w.saved(); s.LastPlan != "" || s.LastPlanKind != "" {
		t.Errorf("install.json: %+v", s)
	}
	rwEqual(t, "writes", w.writes, []string{"s3:PutObject prod/install.json"})
}

// uninstall apply checks the routers again: one installed since the plan refuses it.
func TestUninstallApplyChecksTheRoutersAgain(t *testing.T) {
	w := unNew(t)
	unProtectionOff(w)
	if code, out, _ := w.run(Uninstall, unRequest, false); code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	w.routers = 1
	code, out, _ := w.run(UninstallApply, unRequest, false)
	if code != 1 || !strings.HasSuffix(out, "Routers are installed (table dmt-prod-manager): "+unRefused) {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan-destroy"})

	// Regions not checked: said, and no question (the command is the confirmation).
	w.routers = 0
	w.lambdaDenied = map[string]bool{"eu-south-1": true}
	code, out, asked := w.run(UninstallApply, unRequest, true)
	if code != 0 || len(asked) != 0 || !strings.Contains(out, "Routers: none found (1 region not checked: eu-south-1)\n") {
		t.Errorf("exit code %d, asked %q:\n%s", code, asked, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan-destroy", "apply-destroy"})
}

// uninstall apply refuses a destroy plan that failed, or that an apply followed, before reading it, with what to run.
func TestUninstallApplyAnUnusableDestroyPlan(t *testing.T) {
	w := unNew(t)
	unProtectionOff(w)
	w.ends["plan-destroy"] = cbtypes.StatusTypeFailed
	if code, out, _ := w.run(Uninstall, unRequest, false); code != 1 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	code, out, _ := w.run(UninstallApply, unRequest, false)
	if code != 1 || !strings.HasSuffix(out, "the destroy plan dmt-prod-install:b1 did not succeed (FAILED): run dormeto uninstall "+rwArgs+"\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	rwEqual(t, "builds", w.actions(), []string{"plan-destroy"})

	w = unNew(t)
	s := w.saved()
	s.LastPlan, s.LastPlanKind = "dmt-prod-install:d0", "destroy"
	w.save(s)
	w.objects["prod/plans/dmt-prod-install:d0/plan.json"] = rwDestroy
	w.builds = append(w.builds, &rwBuild{id: "dmt-prod-install:d0", env: map[string]string{"ACTION": "plan-destroy"}, status: cbtypes.StatusTypeSucceeded,
		start: rwT0.Add(-3 * time.Hour)})
	code, out, _ = w.run(UninstallApply, unRequest, false)
	if code != 1 || !strings.HasSuffix(out, "the destroy plan dmt-prod-install:d0 predates the last apply dmt-prod-install:a0: run dormeto uninstall "+rwArgs+"\n") ||
		strings.Contains(out, "Plan (build") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}
