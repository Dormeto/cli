package gcponboard

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/gcp/access"
	"github.com/Dormeto/cli/internal/gcp/gcptest"
)

func (g *installRig) uninstall(t *testing.T, r Request) (int, string) {
	t.Helper()
	g.out.Reset()
	*g.slept = nil
	code := Uninstall(context.Background(), g.d, r)
	return code, g.out.String()
}

var uninstallEurope = Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true}

// policyGrants are the grants of the project's policy as keys.
func (g *installRig) policyGrants(t *testing.T) []string {
	t.Helper()
	p := g.projectPolicy(t)
	var out []string
	for _, x := range grantsOf(&p) {
		out = append(out, grantKey(x))
	}
	slices.Sort(out)
	return out
}

// nothingLeft fails unless every resource of the router dmtr-<region> is gone from the fake.
func (g *installRig) nothingLeft(t *testing.T, region string) {
	t.Helper()
	g.s.Lock()
	defer g.s.Unlock()
	name := "dmtr-" + region
	email := name + "@dormeto-dev.iam.gserviceaccount.com"
	_, svc1 := g.s.RunServices[region+"/"+name+"-controller"]
	_, svc2 := g.s.RunServices[region+"/"+name+"-waker"]
	_, neg := g.s.NEGs[region+"/"+name+"-waker"]
	_, job := g.s.Jobs[region+"/"+name+"-check"]
	_, queue := g.s.Queues[region+"/"+name]
	_, db := g.s.Databases[name]
	_, account := g.s.ServiceAccounts[email]
	if svc1 || svc2 || neg || job || queue || db || account {
		t.Errorf("left: controller %v waker %v neg %v job %v queue %v database %v account %v", svc1, svc2, neg, job, queue, db, account)
	}
}

func TestUninstallRestoresTheProjectsPolicyExceptGooglesBindings(t *testing.T) {
	g := newRouterRig(t)
	g.s.DefaultEditor = true
	before := g.policyGrants(t)
	if code, out := g.run(t, withImage); code != 0 {
		t.Fatalf("install: exit %d:\n%s", code, out)
	}
	rec := g.record(t)
	if len(rec.Google) == 0 {
		t.Fatal("the fake played no Google binding")
	}
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	want := slices.Clone(before)
	for _, x := range rec.Google {
		want = append(want, grantKey(Grant{Role: x.Role, Member: x.Member}))
	}
	slices.Sort(want)
	if got := g.policyGrants(t); !slices.Equal(got, want) {
		t.Fatalf("policy:\n%q\nwant before plus Google's:\n%q", got, want)
	}
	g.nothingLeft(t, "europe-west1")
	g.s.Lock()
	defer g.s.Unlock()
	if len(g.s.TagKeys)+len(g.s.TagValues) != 0 {
		t.Errorf("tags left: %v %v", g.s.TagKeys, g.s.TagValues)
	}
	for id, r := range g.s.Roles {
		if !r.Deleted {
			t.Errorf("role %s is not deleted", id)
		}
	}
	if len(g.s.Enabled) == 0 {
		t.Error("an API was turned off")
	}
	if _, err := os.Stat(g.cacheFile()); !os.IsNotExist(err) {
		t.Errorf("cache file: %v", err)
	}
}

func TestUninstallSaysWhatItLeavesInPlace(t *testing.T) {
	g := newRouterRig(t)
	g.s.DefaultEditor = true
	g.run(t, withImage)
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Left in place:\n", "  API compute.googleapis.com\n", "  API run.googleapis.com\n",
		"  roles/editor to "+computeDefault+" on the project (added by Google)\n",
		"  roles/firestore.serviceAgent to "+firestoreAgent+" on the project (added by Google)\n",
		"Uninstalled router dmtr-europe-west1 from project dormeto-dev.\n")
}

func TestUninstallKeepsAMemberSomeoneElseAddedToADormetoRole(t *testing.T) {
	g := routerInstalled(t)
	role := "projects/dormeto-dev/roles/dormetoRouter"
	grantInFake(t, g.s, role, "user:other@example.com")
	if code, out := g.uninstall(t, uninstallEurope); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	p := g.projectPolicy(t)
	if !has(p, Grant{Role: role, Member: "user:other@example.com"}) {
		t.Fatal("the other member is gone")
	}
	if has(p, Grant{Role: role, Member: routerMember}) {
		t.Fatal("the router is still bound")
	}
}

func TestUninstallKeepsTheCustomRolesWhileAnotherRoutersRecordUsesThem(t *testing.T) {
	g := newRouterRig(t)
	other := withImage
	other.Region = "europe-west4"
	if code, out := g.run(t, withImage); code != 0 {
		t.Fatalf("install: %s", out)
	}
	if code, out := g.run(t, other); code != 0 {
		t.Fatalf("install 2: %s", out)
	}
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "= role dormetoRouter kept: used by router dmtr-europe-west4\n", "= role dormetoRouterBackends kept: used by router dmtr-europe-west4\n")
	g.nothingLeft(t, "europe-west1")
	g.s.Lock()
	for id, r := range g.s.Roles {
		if r.Deleted {
			t.Errorf("role %s is deleted", id)
		}
	}
	if len(g.s.TagValues) != 1 || len(g.s.TagKeys) != 1 {
		t.Errorf("the shared tag is gone: %v %v", g.s.TagKeys, g.s.TagValues)
	}
	g.s.Unlock()
	p := g.projectPolicy(t)
	if !has(p, Grant{Role: "projects/dormeto-dev/roles/dormetoRouter", Member: "serviceAccount:dmtr-europe-west4@dormeto-dev.iam.gserviceaccount.com"}) {
		t.Fatal("the other router's grant is gone")
	}
	// The last router takes the roles and the tag with it.
	if code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west4", Yes: true}); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.s.Lock()
	defer g.s.Unlock()
	for id, r := range g.s.Roles {
		if !r.Deleted {
			t.Errorf("role %s is not deleted", id)
		}
	}
	if len(g.s.TagValues)+len(g.s.TagKeys) != 0 {
		t.Errorf("tags left")
	}
}

func TestUninstallRefusesWhileWorkloadsAreOnboarded(t *testing.T) {
	g := routerInstalled(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.Workloads["blog"] = "ASLEEP"
	mark := len(g.s.Calls())
	code, out := g.uninstall(t, uninstallEurope)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "shop", "blog", "--force", "Nothing was changed.")
	if m := g.changes(mark); len(m) != 0 {
		t.Fatalf("changes: %v", m)
	}
	if len(g.s.Workloads) != 2 {
		t.Fatal("a workload was offboarded")
	}
}

func TestUninstallForceOffboardsEachWorkloadFirst(t *testing.T) {
	g := routerInstalled(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.Workloads["blog"] = "ASLEEP"
	mark := len(g.s.Calls())
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, Force: true})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	changes := g.changes(mark)
	want := []string{"DELETE dmtr-europe-west1-controller-1034397096264.europe-west1.run.app/v1/workloads/blog",
		"DELETE dmtr-europe-west1-controller-1034397096264.europe-west1.run.app/v1/workloads/shop"}
	if len(changes) < 3 || !slices.Equal(changes[:2], want) {
		t.Fatalf("changes:\n%s", strings.Join(changes, "\n"))
	}
	if !strings.Contains(changes[2], "cloudscheduler.googleapis.com") {
		t.Fatalf("the job is deleted after the workloads: %v", changes[2])
	}
	contains(t, out, "- offboard workload blog", "- offboard workload shop")
	if len(g.s.Workloads) != 0 {
		t.Fatal("workloads left")
	}
	g.nothingLeft(t, "europe-west1")
}

func TestUninstallChecksOffboardsPermissionsWithForceBeforeAnyChange(t *testing.T) {
	g := routerInstalled(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.HoldsAll = false
	g.s.Held = map[string]bool{}
	for _, c := range []string{"uninstall", "offboard"} {
		for _, p := range access.Needs(c) {
			g.s.Held[p] = p != "compute.instances.deleteTagBinding"
		}
	}
	mark := len(g.s.Calls())
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, Force: true})
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ offboard: missing compute.instances.deleteTagBinding\n", "Nothing was changed.\n")
	if m := g.changes(mark); len(m) != 0 {
		t.Fatalf("changes: %v", m)
	}
}

func TestUninstallWithoutForceDoesNotAskOffboardsPermissionsNorInstancesUse(t *testing.T) {
	g := routerInstalled(t)
	mark := len(g.s.Calls())
	if code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", DryRun: true}); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	asked := ""
	for _, c := range g.s.Calls()[mark:] {
		if strings.HasSuffix(c.Path, ":testIamPermissions") {
			asked += c.Body
		}
	}
	if asked == "" || strings.Contains(asked, "deleteTagBinding") || strings.Contains(asked, "compute.instances.use") {
		t.Fatalf("asked: %s", asked)
	}
}

func TestUninstallRemovesTheDeletedMembersOfAServiceAccountDeletedByHand(t *testing.T) {
	g := routerInstalled(t)
	g.s.Lock()
	delete(g.s.ServiceAccounts, routerSA)
	b, _ := json.Marshal(g.s.Policy)
	replaced := strings.ReplaceAll(string(b), `"`+routerMember+`"`, `"deleted:`+routerMember+`?uid=123456789"`)
	var m map[string]any
	json.Unmarshal([]byte(replaced), &m)
	g.s.Policy = m
	g.s.Unlock()
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "deleted:"+routerMember+"?uid=123456789")
	// The router's own grants, its account deleted: the plan removes them, they are not someone else's.
	if strings.Contains(out, "since the last run") {
		t.Fatalf("%s", out)
	}
	for _, k := range g.policyGrants(t) {
		if strings.Contains(k, routerSA) {
			t.Errorf("still in the policy: %q", k)
		}
	}
	g.nothingLeft(t, "europe-west1")
}

func TestUninstallDryRunPrintsThePlanAndChangesNothing(t *testing.T) {
	g := routerInstalled(t)
	mark := len(g.s.Calls())
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", DryRun: true})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Plan for project dormeto-dev, uninstalling router dmtr-europe-west1:\n",
		"  - job dmtr-europe-west1-check\n", "  - network endpoint group dmtr-europe-west1-waker\n",
		"  - service dmtr-europe-west1-waker\n", "  - service dmtr-europe-west1-controller\n", "  - queue dmtr-europe-west1\n",
		"  - database dmtr-europe-west1 (and the install record)\n", "  - grant projects/dormeto-dev/roles/dormetoRouter to "+routerMember+" on the project\n",
		"  - tag dormeto/managed\n", "  - service account "+routerSA+"\n", "  - role dormetoRouter\n", "  - role dormetoRouterBackends\n")
	// In the order of the spec: job, NEG, services, queue, database, bindings, tag, service account, roles.
	order := []string{"- job ", "- network endpoint group", "- service dmtr-europe-west1-waker", "- service dmtr-europe-west1-controller", "- queue", "- database",
		"- grant projects/dormeto-dev/roles/dormetoRouter ", "- tag ", "- service account", "- role "}
	at := -1
	for _, w := range order {
		i := strings.Index(out, w)
		if i < at {
			t.Fatalf("%q is out of order:\n%s", w, out)
		}
		at = i
	}
	if m := g.changes(mark); len(m) != 0 {
		t.Fatalf("changes: %v", m)
	}
}

func TestUninstallWithoutTerminalNeedsYes(t *testing.T) {
	g := routerInstalled(t)
	mark := len(g.s.Calls())
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1"})
	if code != 2 || !strings.Contains(out, "--yes: needed") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if m := g.changes(mark); len(m) != 0 {
		t.Fatalf("changes: %v", m)
	}
}

func TestUninstallWithoutARecordChangesNothing(t *testing.T) {
	g := newRouterRig(t)
	code, out := g.uninstall(t, uninstallEurope)
	if code != 1 || !strings.Contains(out, "no router dmtr-europe-west1 is installed in project dormeto-dev") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutations: %v", m)
	}
}

func TestUninstallStoppedAfterTheDatabaseContinuesFromTheCache(t *testing.T) {
	g := routerInstalled(t)
	g.s.Refuse(gcptest.Refusal{Method: "POST", Host: "cloudresourcemanager.googleapis.com", Path: "/v3/projects/dormeto-dev:setIamPolicy", Times: 1, Code: 403, Status: "PERMISSION_DENIED", Message: "denied"})
	code, out := g.uninstall(t, uninstallEurope)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ projectBindings:", "Run the same command again to continue.")
	if _, err := os.Stat(g.cacheFile()); err != nil {
		t.Fatalf("the record waits in the cache: %v", err)
	}
	code, out = g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("second run: exit %d:\n%s", code, out)
	}
	g.nothingLeft(t, "europe-west1")
	for _, k := range g.policyGrants(t) {
		if strings.Contains(k, routerSA) {
			t.Errorf("still in the policy: %q", k)
		}
	}
	if _, err := os.Stat(g.cacheFile()); !os.IsNotExist(err) {
		t.Errorf("cache file: %v", err)
	}
}

const controllerHost = "dmtr-europe-west1-controller-1034397096264.europe-west1.run.app"

func TestUninstallAsksTheInvokePermissionToListWorkloadsWithoutForce(t *testing.T) {
	g := routerInstalled(t)
	mark := len(g.s.Calls())
	if code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", DryRun: true}); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	asked := ""
	for _, c := range g.s.Calls()[mark:] {
		if strings.HasSuffix(c.Path, ":testIamPermissions") {
			asked += c.Body
		}
	}
	if !strings.Contains(asked, "run.routes.invoke") {
		t.Fatalf("asked: %s", asked)
	}
}

func TestUninstallWithAControllerThatDoesNotAnswer(t *testing.T) {
	for _, code := range []int{404, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			g := routerInstalled(t)
			g.s.Refuse(gcptest.Refusal{Method: "GET", Host: controllerHost, Path: "/v1/workloads", Code: code, Status: "UNAVAILABLE", Message: "down"})
			mark := len(g.s.Calls())
			exit, out := g.uninstall(t, uninstallEurope)
			if exit != 1 {
				t.Fatalf("exit %d:\n%s", exit, out)
			}
			contains(t, out, "workloads cannot be listed", "--force", "Nothing was changed.")
			if m := g.changes(mark); len(m) != 0 {
				t.Fatalf("changes: %v", m)
			}
			exit, out = g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, Force: true})
			if exit != 0 {
				t.Fatalf("--force: exit %d:\n%s", exit, out)
			}
			contains(t, out, "  ! the workloads are unknown")
			g.nothingLeft(t, "europe-west1")
		})
	}
}

func TestUninstallStopsOnAControllerThatRefusesTheOperator(t *testing.T) {
	for _, force := range []bool{false, true} {
		g := routerInstalled(t)
		g.s.Refuse(gcptest.Refusal{Method: "GET", Host: controllerHost, Path: "/v1/workloads", Code: 403, Status: "PERMISSION_DENIED", Message: "no invoker"})
		mark := len(g.s.Calls())
		exit, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, Force: force})
		if exit != 1 || !strings.Contains(out, "Nothing was changed.") {
			t.Fatalf("force %v: exit %d:\n%s", force, exit, out)
		}
		if m := g.changes(mark); len(m) != 0 {
			t.Fatalf("changes: %v", m)
		}
	}
}

func TestUninstallLeavesATagValueGoogleRefusesToDeleteAndGoesOn(t *testing.T) {
	g := routerInstalled(t)
	g.s.Lock()
	for _, v := range g.s.TagValues {
		v.Bound = true
	}
	g.s.Unlock()
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Left in place:\n", "  tag dormeto/managed: The tag value tagValues/", " is still bound to resources\n", "  tag dormeto: The tag key tagKeys/", " still has values\n",
		"  ✓ serviceAccount:", "  ✓ roles:", "Uninstalled router dmtr-europe-west1")
	g.nothingLeft(t, "europe-west1")
	g.s.Lock()
	defer g.s.Unlock()
	if len(g.s.TagValues) != 1 || len(g.s.TagKeys) != 1 {
		t.Errorf("tags: %v %v", g.s.TagKeys, g.s.TagValues)
	}
}

func TestUninstallStopsOnAnotherRefusalOfTheTag(t *testing.T) {
	g := routerInstalled(t)
	g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: "cloudresourcemanager.googleapis.com", Path: "/v3/tagValues", Code: 403, Status: "PERMISSION_DENIED", Message: "denied"})
	code, out := g.uninstall(t, uninstallEurope)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ tag:", "Not done: tag, serviceAccount, roles.")
}

func TestUninstallForceRemovesTheTagBindingsOfTheWorkloadsBeforeTheTag(t *testing.T) {
	g := newWorkloadsRig(t)
	value := g.tagValue(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{
		map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"},
		map[string]any{"kind": "mig", "location": "europe-west1", "name": "web-mig", "id": "222"}}}
	g.s.TagBindings = []gcptest.TagBinding{{Parent: vmParent, TagValue: value}, {Parent: groupParent, TagValue: value}}
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, Force: true})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "still bound") {
		t.Fatalf("the tag is left:\n%s", out)
	}
	g.nothingLeft(t, "europe-west1")
	g.s.Lock()
	defer g.s.Unlock()
	if len(g.s.TagBindings) != 0 || len(g.s.TagValues)+len(g.s.TagKeys) != 0 || len(g.s.Workloads) != 0 {
		t.Errorf("left: %v %v %v %v", g.s.TagBindings, g.s.TagValues, g.s.TagKeys, g.s.Workloads)
	}
}

func TestUninstallTagValueStillBoundIsRefusedByGoogle(t *testing.T) {
	g := routerInstalled(t)
	g.s.Lock()
	var value string
	for name := range g.s.TagValues {
		value = name
	}
	g.s.TagBindings = []gcptest.TagBinding{{Parent: "//compute.googleapis.com/projects/1034397096264/zones/z/instances/1", TagValue: value}}
	g.s.Unlock()
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Left in place:\n", " is still bound to resources")
}

func TestUninstallForceFinishesAnOffboardingThatStopped(t *testing.T) {
	g := newWorkloadsRig(t)
	g.stuckOffboarding(t)
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, Force: true})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "still bound") {
		t.Fatalf("the tag is left:\n%s", out)
	}
	g.nothingLeft(t, "europe-west1")
	g.s.Lock()
	defer g.s.Unlock()
	if len(g.s.TagBindings) != 0 || len(g.s.TagValues)+len(g.s.TagKeys) != 0 {
		t.Errorf("left: %v %v %v", g.s.TagBindings, g.s.TagValues, g.s.TagKeys)
	}
	if _, err := os.Stat(g.offboardFile()); !os.IsNotExist(err) {
		t.Errorf("the file stays: %v", err)
	}
}

func TestUninstallForceAsksOffboardsTagPermissions(t *testing.T) {
	for _, missing := range []string{"resourcemanager.tagValueBindings.delete", "compute.instances.deleteTagBinding", "compute.instances.listTagBindings",
		"compute.instanceGroupManagers.deleteTagBinding", "compute.instanceGroupManagers.listTagBindings"} {
		g := routerInstalled(t)
		g.s.Workloads["shop"] = "AWAKE"
		g.s.HoldsAll = false
		g.s.Held = map[string]bool{}
		for _, c := range []string{"uninstall", "offboard"} {
			for _, p := range access.Needs(c) {
				g.s.Held[p] = p != missing
			}
		}
		mark := len(g.s.Calls())
		code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, Force: true})
		if code != 1 || !strings.Contains(out, "missing "+missing) {
			t.Errorf("%s: exit %d:\n%s", missing, code, out)
		}
		if m := g.changes(mark); len(m) != 0 {
			t.Errorf("%s: changes %v", missing, m)
		}
	}
}

func TestUninstallForceGoesOnWhenABindingIsGoneAlready(t *testing.T) {
	g := newWorkloadsRig(t)
	value := g.tagValue(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{
		map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"},
		map[string]any{"kind": "mig", "location": "europe-west1", "name": "web-mig", "id": "222"}}}
	g.s.TagBindings = []gcptest.TagBinding{{Parent: groupParent, TagValue: value}} // the VM's was removed by hand
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, Force: true})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.nothingLeft(t, "europe-west1")
	g.s.Lock()
	defer g.s.Unlock()
	if len(g.s.TagBindings) != 0 || len(g.s.TagValues) != 0 {
		t.Errorf("left: %v %v", g.s.TagBindings, g.s.TagValues)
	}
}

func TestUninstallForceLeavesTheBackendsOfAWorkloadThatCannotWakeDown(t *testing.T) {
	g := newWorkloadsRig(t)
	g.failingToWake(t)
	mark := len(g.s.Calls())
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, Force: true})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var deletes []string
	for _, c := range g.s.Calls()[mark:] {
		if c.Method == "DELETE" && c.Host == controllerHost {
			deletes = append(deletes, strings.TrimPrefix(c.URL, "https://"+controllerHost))
		}
	}
	if !slices.Equal(deletes, []string{"/v1/workloads/shop", "/v1/workloads/shop?force=true"}) {
		t.Fatalf("%v", deletes)
	}
	contains(t, out, "left down", "vm web-1 (europe-west1-b)", "group web-mig (europe-west1)")
	g.nothingLeft(t, "europe-west1")
	g.s.Lock()
	defer g.s.Unlock()
	if len(g.s.TagBindings) != 0 || len(g.s.TagValues) != 0 {
		t.Errorf("left: %v %v", g.s.TagBindings, g.s.TagValues)
	}
}

func TestUninstallShowsAndLeavesABindingGoogleAddedAfterTheInstall(t *testing.T) {
	g := routerInstalled(t)
	grantInFake(t, g.s, registryAgentRole, artifactAgent)
	grantInFake(t, g.s, "roles/viewer", "user:someone@example.com")
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "IAM changes since the last run, not Dormeto's:\n"+
		"  + "+registryAgentRole+" to "+artifactAgent+" on the project (Google, after install (seen later))\n"+
		"  + roles/viewer to user:someone@example.com on the project (not a Google service agent: not recorded)\n",
		"Left in place:\n", "  "+registryAgentRole+" to "+artifactAgent+" on the project (added by Google)\n")
	p := g.projectPolicy(t)
	if !has(p, Grant{Role: registryAgentRole, Member: artifactAgent}) {
		t.Fatal("Google's binding is gone")
	}
}

func TestUninstallPlanNamesTheGrantsThatGoWithTheirResources(t *testing.T) {
	g := routerInstalled(t)
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", DryRun: true})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  - service "+controllerName+"\n  - grant roles/run.invoker to "+routerMember+" on controller "+controllerName+" (goes with it)\n",
		"  - queue dmtr-europe-west1\n  - grant roles/cloudtasks.enqueuer to "+routerMember+" on queue dmtr-europe-west1 (goes with it)\n",
		"  - service account "+routerSA+"\n  - grant roles/iam.serviceAccountUser to "+routerMember+" on service account "+routerSA+" (goes with it)\n")
}

func TestUninstallPrintsEveryIAMChange(t *testing.T) {
	g := routerInstalled(t)
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✓ cloudRun: removed service "+wakerName+", removed service "+controllerName+"\n"+
		"      IAM - roles/run.invoker to "+routerMember+" on controller "+controllerName+" (Dormeto)\n",
		"  ✓ queue: removed queue dmtr-europe-west1\n"+
			"      IAM - roles/cloudtasks.enqueuer to "+routerMember+" on queue dmtr-europe-west1 (Dormeto)\n",
		"  ✓ projectBindings: removed 3 grants from the project\n",
		"      IAM - projects/dormeto-dev/roles/dormetoRouter to "+routerMember+" on the project (Dormeto)\n",
		"      IAM - projects/dormeto-dev/roles/dormetoRouterBackends to "+routerMember+" on the project, if \"Dormeto-managed resources\": ",
		"      IAM - roles/datastore.user to "+routerMember+" on the project, if \"The router's database\": ",
		"  ✓ serviceAccount: removed service account "+routerSA+"\n"+
			"      IAM - roles/iam.serviceAccountUser to "+routerMember+" on service account "+routerSA+" (Dormeto)\n")
}

const uninstallVerified = "  ✓ verified: the project's IAM policy holds its bindings from before the install, and Google's\n"

func TestUninstallChecksThePolicyWhereItEnded(t *testing.T) {
	g := routerInstalled(t)
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, uninstallVerified+"Left in place:\n")
	if strings.Contains(out, "  ! ") {
		t.Fatalf("a warning:\n%s", out)
	}
}

func TestUninstallWarnsOfAPolicyThatEndsElsewhereAndStillSucceeds(t *testing.T) {
	g := routerInstalled(t)
	grantInFake(t, g.s, "roles/viewer", "user:someone@example.com")
	g.s.Lock()
	var kept []any
	for _, b := range g.s.Policy["bindings"].([]any) {
		if b.(map[string]any)["role"] != "roles/owner" {
			kept = append(kept, b)
		}
	}
	g.s.Policy["bindings"] = kept
	g.s.Unlock()
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ! IAM difference from before the install, not this uninstall's: + roles/viewer to user:someone@example.com on the project\n",
		"  ! IAM difference from before the install, not this uninstall's: - roles/owner to user:owner@example.com on the project\n",
		"Uninstalled router dmtr-europe-west1")
	if strings.Contains(out, uninstallVerified) {
		t.Fatalf("verified:\n%s", out)
	}
	g.nothingLeft(t, "europe-west1")
}

func TestUninstallDoesNotWarnOfAnotherRoutersGrants(t *testing.T) {
	g := newRouterRig(t)
	other := withImage
	other.Region = "europe-west4"
	for _, r := range []Request{withImage, other} {
		if code, out := g.run(t, r); code != 0 {
			t.Fatalf("install: %s", out)
		}
	}
	// The second router's before holds the first's grants: those it does not find any more are not a difference either.
	for _, region := range []string{"europe-west1", "europe-west4"} {
		code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: region, Yes: true})
		if code != 0 {
			t.Fatalf("%s: exit %d:\n%s", region, code, out)
		}
		contains(t, out, uninstallVerified)
	}
}

func TestUninstallSaysWhenThePolicyIsNotReadAtTheEnd(t *testing.T) {
	g := routerInstalled(t)
	set := "/v3/projects/dormeto-dev:setIamPolicy"
	installed := len(g.s.CallsTo(set))
	// The uninstall's read after its own write of the project's policy is the end's.
	g.s.Route("cloudresourcemanager.googleapis.com", "/v3/projects/dormeto-dev:getIamPolicy", func(w http.ResponseWriter, r *http.Request) {
		if len(g.s.CallsTo(set)) > installed {
			gcptest.Fail(w, 403, "PERMISSION_DENIED", "denied at the end")
			return
		}
		g.s.Lock()
		p := g.s.Policy
		g.s.Unlock()
		gcptest.Reply(w, p)
	})
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ! the project's IAM policy is not read again: ", "denied at the end", "Uninstalled router dmtr-europe-west1")
	if strings.Contains(out, uninstallVerified) {
		t.Fatalf("verified:\n%s", out)
	}
}

func TestUninstallSaysHowLongGoogleHoldsTheNamesOfADeletedQueueAndDatabase(t *testing.T) {
	g := routerInstalled(t)
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", DryRun: true})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "up to 3 days", "about 5 minutes")
}

func TestUninstallSaysNothingOfTheNamesWhenNeitherTheQueueNorTheDatabaseGoes(t *testing.T) {
	g := routerInstalled(t)
	g.s.Refuse(gcptest.Refusal{Method: "POST", Host: "cloudresourcemanager.googleapis.com", Path: "/v3/projects/dormeto-dev:setIamPolicy", Times: 1, Code: 403, Status: "PERMISSION_DENIED", Message: "denied"})
	if code, out := g.uninstall(t, uninstallEurope); code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", DryRun: true})
	if code != 0 || strings.Contains(out, "3 days") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestInstallAfterAnUninstallNamesTheWindowOfAQueuesName(t *testing.T) {
	g := routerInstalled(t)
	if code, out := g.uninstall(t, uninstallEurope); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.s.Lock()
	g.s.BlockedQueues = map[string]bool{"europe-west1/dmtr-europe-west1": true}
	g.s.Unlock()
	code, out := g.run(t, withImage)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ queue: ", "up to 3 days", "Run the same command again to continue.")
	if strings.Contains(out, "google: 404") {
		t.Fatalf("a bare 404:\n%s", out)
	}
	// The window over, the same command continues.
	g.s.Lock()
	g.s.BlockedQueues = nil
	g.s.Unlock()
	if code, out := g.run(t, withImage); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestUninstallTakesADeletedDatabasesOperationAnsweringNotFoundAsDone(t *testing.T) {
	g := routerInstalled(t)
	g.s.Lock()
	g.s.DatabaseOperationGone = true
	g.s.Unlock()
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✓ database: removed database dmtr-europe-west1\n")
	g.nothingLeft(t, "europe-west1")
}

func TestUninstallLeavesATagWhoseDeletionOperationFailsOnAPrecondition(t *testing.T) {
	g := routerInstalled(t)
	g.s.Lock()
	for _, v := range g.s.TagValues {
		v.BoundLate = true
	}
	g.s.Unlock()
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Left in place:\n", "  tag dormeto/managed: The tag value tagValues/", " is still bound to resources\n",
		"  ✓ serviceAccount:", "  ✓ roles:", "Uninstalled router dmtr-europe-west1")
	g.nothingLeft(t, "europe-west1")
}

func TestUninstallKeepsTheRecordWhileATagIsStrandedSoARerunFinishesIt(t *testing.T) {
	g := routerInstalled(t)
	g.s.Lock()
	for _, v := range g.s.TagValues {
		v.Bound = true
	}
	g.s.Unlock()
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Left in place:\n", "Run the same command again to remove the tag once Google allows it")
	if _, err := os.Stat(g.cacheFile()); err != nil {
		t.Fatalf("the record is not kept: %v", err)
	}
	// Still bound: it stays, and so does the record.
	code, out = g.uninstall(t, uninstallEurope)
	if code != 0 || strings.Contains(out, "no router") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(g.cacheFile()); err != nil {
		t.Fatalf("the record is not kept: %v", err)
	}
	g.s.Lock()
	for _, v := range g.s.TagValues {
		v.Bound = false
	}
	g.s.Unlock()
	code, out = g.uninstall(t, uninstallEurope)
	if code != 0 || strings.Contains(out, "no router") || strings.Contains(out, "Run the same command again") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.s.Lock()
	tags := len(g.s.TagValues) + len(g.s.TagKeys)
	g.s.Unlock()
	if tags != 0 {
		t.Fatalf("%d tags left", tags)
	}
	if _, err := os.Stat(g.cacheFile()); !os.IsNotExist(err) {
		t.Fatalf("cache file: %v", err)
	}
}

const negPath = "/compute/v1/projects/dormeto-dev/regions/europe-west1/networkEndpointGroups/" + wakerName

func TestUninstallForceWithABrokenControllerNamesTheWayOutOfANetworkEndpointGroupInUse(t *testing.T) {
	g := routerInstalled(t)
	g.s.Refuse(gcptest.Refusal{Method: "GET", Host: controllerHost, Path: "/v1/workloads", Code: 500, Status: "UNAVAILABLE", Message: "down"})
	g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: "compute.googleapis.com", Path: negPath, Code: 400, Message: "The network_endpoint_group resource is already being used"})
	force := Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, Force: true, Configuration: "c"}
	for i := 0; i < 2; i++ { // at every rerun
		code, out := g.uninstall(t, force)
		if code != 1 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		contains(t, out, "  ✗ neg: ", "already being used",
			"dormeto gcp install --project dormeto-dev --region europe-west1 --gcloud-configuration c --image <a good image>",
			"dormeto gcp workloads offboard <workload> --project dormeto-dev --region europe-west1 --gcloud-configuration c")
	}
}

func TestUninstallDoesNotNameThatWayOutWhenTheControllerAnswers(t *testing.T) {
	g := routerInstalled(t)
	g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: "compute.googleapis.com", Path: negPath, Code: 400, Message: "refused"})
	code, out := g.uninstall(t, uninstallEurope)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ neg: ")
	if strings.Contains(out, "workloads offboard") {
		t.Fatalf("%s", out)
	}
}

func TestUninstallForceGoesOnWhenTheBindingsOfAVMDeletedByHandAreDenied(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{
		map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"},
		map[string]any{"kind": "mig", "location": "europe-west1", "name": "web-mig", "id": "222"}}}
	g.s.TagBindings = []gcptest.TagBinding{{Parent: groupParent, TagValue: g.tagValue(t)}}
	g.s.Refuse(gcptest.Refusal{Method: "GET", Host: zoneHost, Path: "/v3/tagBindings", Code: 403, Status: "PERMISSION_DENIED", Message: forbidden})
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, Force: true})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ! the tag bindings of vm web-1 (europe-west1-b) are not read")
	g.nothingLeft(t, "europe-west1")
}

// stopAtPoll stops the run (Ctrl-C) at the at-th poll of the operations under prefix on host, the other polls answered as
// Google does. seen is the record the cache holds at that moment: what a run killed there leaves.
func stopAtPoll(t *testing.T, g *installRig, host, prefix, version string, at int) (ctx context.Context, seen *Record) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	seen = &Record{}
	polls := 0
	g.s.Route(host, prefix, func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, version)
		if polls++; polls != at {
			g.s.Poll(w, name)
			return
		}
		if b, err := os.ReadFile(g.cacheFile()); err == nil {
			json.Unmarshal(b, seen)
		}
		cancel()
		gcptest.Reply(w, map[string]any{"name": name, "done": false})
	})
	return ctx, seen
}

func TestUninstallRemovesWhatARunStoppedDuringItsOperationLeft(t *testing.T) {
	const crm, firestore = "cloudresourcemanager.googleapis.com", "firestore.googleapis.com"
	const database = "/v1/projects/dormeto-dev/databases/dmtr-europe-west1/operations/"
	cases := map[string]struct {
		host, prefix, version string
		at                    int
		kind, name, plan      string
	}{
		"the tag's key":   {crm, "/v3/operations/", "/v3/", 1, "tagKey", "dormeto-dev/dormeto", "  - tag dormeto\n"},
		"the tag's value": {crm, "/v3/operations/", "/v3/", 3, "tagValue", "dormeto-dev/dormeto/managed", "  - tag dormeto/managed\n"},
		"the database":    {firestore, database, "/v1/", 1, "database", "projects/dormeto-dev/databases/dmtr-europe-west1", "  - database dmtr-europe-west1 (and the install record)\n"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			g := newInstallRig(t)
			ctx, seen := stopAtPoll(t, g, c.host, c.prefix, c.version, c.at)
			g.out.Reset()
			if code := Install(ctx, g.d, europe); code != 130 {
				t.Fatalf("exit %d:\n%s", code, g.out)
			}
			if !listsResource(*seen, c.kind, c.name) {
				t.Errorf("the cache does not list it during the operation: %+v", seen.Resources)
			}
			// Google finishes the operation after the run stopped.
			g.s.FinishOperations()
			code, out := g.uninstall(t, uninstallEurope)
			if code != 0 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			contains(t, out, c.plan)
			g.nothingLeft(t, "europe-west1")
			g.s.Lock()
			defer g.s.Unlock()
			if len(g.s.TagKeys)+len(g.s.TagValues) != 0 {
				t.Errorf("tags left: %v %v", g.s.TagKeys, g.s.TagValues)
			}
		})
	}
}

// A created tag's name replaces the name it waited under, whichever run finds it.
func TestInstallAfterAStopDuringTheTagsOperationRecordsItsIDAlone(t *testing.T) {
	g := newInstallRig(t)
	ctx, _ := stopAtPoll(t, g, "cloudresourcemanager.googleapis.com", "/v3/operations/", "/v3/", 1)
	g.out.Reset()
	if code := Install(ctx, g.d, europe); code != 130 {
		t.Fatalf("exit %d:\n%s", code, g.out)
	}
	g.s.FinishOperations()
	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	rec := g.record(t)
	for _, name := range []string{"dormeto-dev/dormeto", "dormeto-dev/dormeto/managed"} {
		if listsResource(rec, "tagKey", name) || listsResource(rec, "tagValue", name) {
			t.Errorf("%s is still listed: %+v", name, rec.Resources)
		}
	}
	g.s.Lock()
	defer g.s.Unlock()
	for name := range g.s.TagKeys {
		if !listsResource(rec, "tagKey", name) {
			t.Errorf("%s is not listed: %+v", name, rec.Resources)
		}
	}
	for name := range g.s.TagValues {
		if !listsResource(rec, "tagValue", name) {
			t.Errorf("%s is not listed: %+v", name, rec.Resources)
		}
	}
}

// The answer to a creation lost: the resource is in Google, the run does not know it, the record does.
func TestUninstallRemovesWhatACreationWhoseAnswerWasLostLeft(t *testing.T) {
	cases := map[string]struct {
		host, path string
		plant      func(s *gcptest.Server)
		gone       func(s *gcptest.Server) bool
	}{
		"role": {"iam.googleapis.com", "/v1/projects/dormeto-dev/roles",
			func(s *gcptest.Server) { s.Roles["dormetoRouter"] = &gcptest.Role{Stage: "GA"} },
			func(s *gcptest.Server) bool { return s.Roles["dormetoRouter"].Deleted }},
		"service account": {"iam.googleapis.com", "/v1/projects/dormeto-dev/serviceAccounts",
			func(s *gcptest.Server) { s.ServiceAccounts[routerSA] = &gcptest.ServiceAccount{} },
			func(s *gcptest.Server) bool { return s.ServiceAccounts[routerSA] == nil }},
		"queue": {"cloudtasks.googleapis.com", "/v2/projects/dormeto-dev/locations/europe-west1/queues",
			func(s *gcptest.Server) { s.Queues["europe-west1/dmtr-europe-west1"] = &gcptest.Queue{} },
			func(s *gcptest.Server) bool { return s.Queues["europe-west1/dmtr-europe-west1"] == nil }},
		"job": {"cloudscheduler.googleapis.com", "/v1/projects/dormeto-dev/locations/europe-west1/jobs",
			func(s *gcptest.Server) { s.Jobs["europe-west1/"+jobName] = &gcptest.Job{} },
			func(s *gcptest.Server) bool { return s.Jobs["europe-west1/"+jobName] == nil }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			g := newRouterRig(t)
			g.s.Refuse(gcptest.Refusal{Method: "POST", Host: c.host, Path: c.path, Times: 1, Code: 500, Status: "INTERNAL", Message: "boom"})
			if code, out := g.run(t, withImage); code != 1 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			g.s.Lock()
			c.plant(g.s)
			g.s.Unlock()
			code, out := g.uninstall(t, uninstallEurope)
			if code != 0 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			g.s.Lock()
			defer g.s.Unlock()
			if !c.gone(g.s) {
				t.Errorf("still there:\n%s", out)
			}
		})
	}
}

func TestUninstallForceSaysTheControllersRefusalOfAnOffboardingAlone(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "NAPPING"
	g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: controllerHost, Path: "/v1/workloads/shop", Code: 409, Status: "ABORTED", Message: "cannot offboard shop: it is NAPPING", Reason: "busy"})
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, Force: true})
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ offboard: workload shop: cannot offboard shop: it is NAPPING\n")
}

// A role that goes between the listing's read and the removal's is no step: it panicked in combine.
func TestUninstallARoleVanishingBetweenTwoReadsIsNoStep(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	reads := 0
	const role = "/v1/projects/dormeto-dev/roles/dormetoRouterBackends"
	g.s.Route("iam.googleapis.com", role, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == role {
			if reads++; reads == 1 {
				gcptest.Reply(w, map[string]any{"name": "projects/dormeto-dev/roles/dormetoRouterBackends", "stage": "GA"})
				return
			}
		}
		gcptest.Fail(w, 404, "NOT_FOUND", "The role was not found.")
	})
	code, out := g.uninstall(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "- role dormetoRouterBackends") {
		t.Errorf("planned:\n%s", out)
	}
	if !strings.Contains(out, "- role dormetoRouter\n") {
		t.Errorf("the other role is not planned:\n%s", out)
	}
}
