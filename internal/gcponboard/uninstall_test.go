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
		"- grant ", "- tag ", "- service account", "- role "}
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
