package gcponboard

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/gcp/access"
	"github.com/Dormeto/cli/internal/gcp/gcptest"
	"github.com/Dormeto/cli/internal/install"
)

const (
	vmParent    = "//compute.googleapis.com/projects/1034397096264/zones/europe-west1-b/instances/111"
	groupParent = "//compute.googleapis.com/projects/1034397096264/regions/europe-west1/instanceGroupManagers/222"
	zoneHost    = "europe-west1-b-cloudresourcemanager.googleapis.com"
	regionHost  = "europe-west1-cloudresourcemanager.googleapis.com"
)

var workloadsEurope = Request{Project: "dormeto-dev", Region: "europe-west1"}

func ptr[T any](v T) *T { return &v }

// workloadsRig is an installed router whose dry run names a VM and a regional group, with the audiences of the identity
// tokens collected.
type workloadsRig struct {
	*installRig
	audiences *[]string
}

func newWorkloadsRig(t *testing.T) *workloadsRig {
	t.Helper()
	g := routerInstalled(t)
	aud := &[]string{}
	g.d.Google.Tokens = gcptest.Tokens{AccountName: "me@example.com", Audiences: aud}
	g.s.DryRun = gcptest.DryRun{
		VMs:    []gcptest.Resource{{Location: "europe-west1-b", Name: "web-1", ID: "111"}},
		Groups: []gcptest.Resource{{Kind: "regional", Location: "europe-west1", Name: "web-mig", ID: "222"}},
	}
	g.s.Instances["zones/europe-west1-b/instances/web-1"] = "111"
	g.s.Instances["regions/europe-west1/instanceGroupManagers/web-mig"] = "222"
	return &workloadsRig{g, aud}
}

func (g *workloadsRig) call(t *testing.T, f func(context.Context, Deps, Request) int, r Request) (int, string) {
	t.Helper()
	g.out.Reset()
	return f(context.Background(), g.d, r), g.out.String()
}

// controllerCalls are the calls to the controller since mark, as "<method> <path>[?query]".
func (g *workloadsRig) controllerCalls(mark int) []string {
	var out []string
	for _, c := range g.s.Calls()[mark:] {
		if c.Host == controllerHost {
			out = append(out, c.Method+" "+strings.TrimPrefix(c.URL, "https://"+controllerHost))
		}
	}
	return out
}

func (g *workloadsRig) tagValue(t *testing.T) string {
	t.Helper()
	g.s.Lock()
	defer g.s.Unlock()
	for name, v := range g.s.TagValues {
		if v.NamespacedName == "dormeto-dev/dormeto/managed" {
			return name
		}
	}
	t.Fatal("no tag value")
	return ""
}

func (g *workloadsRig) bindings() []gcptest.TagBinding {
	g.s.Lock()
	defer g.s.Unlock()
	return slices.Clone(g.s.TagBindings)
}

func (g *workloadsRig) body(t *testing.T, method, host, pathPrefix string) map[string]any {
	t.Helper()
	for _, c := range g.s.Calls() {
		if c.Method == method && c.Host == host && strings.HasPrefix(c.URL, "https://"+host+pathPrefix) {
			var m map[string]any
			if err := json.Unmarshal([]byte(c.Body), &m); err != nil {
				t.Fatalf("%s: %v", c.Body, err)
			}
			return m
		}
	}
	t.Fatalf("no %s %s%s", method, host, pathPrefix)
	return nil
}

func TestWorkloadsListCallsTheControllerAsTheOperator(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"], g.s.Workloads["blog"] = "AWAKE", "ASLEEP"
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsList, workloadsEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.controllerCalls(mark); !slices.Equal(got, []string{"GET /v1/workloads"}) {
		t.Fatalf("%v", got)
	}
	if !slices.Contains(*g.audiences, "https://"+controllerHost) {
		t.Fatalf("audiences %v", *g.audiences)
	}
	contains(t, out, "blog", "ASLEEP", "shop", "AWAKE")
	if strings.Index(out, "blog") > strings.Index(out, "shop") {
		t.Fatalf("not sorted:\n%s", out)
	}
	r := workloadsEurope
	r.JSON = true
	code, out = g.call(t, WorkloadsList, r)
	var list []map[string]any
	if err := json.Unmarshal([]byte(out), &list); code != 0 || err != nil || len(list) != 2 || list[1]["id"] != "shop" {
		t.Fatalf("%d %v:\n%s", code, err, out)
	}
}

func TestWorkloadsListSaysWhenThereIsNone(t *testing.T) {
	g := newWorkloadsRig(t)
	code, out := g.call(t, WorkloadsList, workloadsEurope)
	if code != 0 || !strings.Contains(out, "no workload") {
		t.Fatalf("%d:\n%s", code, out)
	}
	r := workloadsEurope
	r.JSON = true
	if _, out = g.call(t, WorkloadsList, r); strings.TrimSpace(out) != "[]" {
		t.Fatalf("%q", out)
	}
}

func TestWorkloadsShowPrintsTheWorkload(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.WorkloadDocs["shop"] = map[string]any{"urlMap": "shop-map", "backendService": "shop-be", "wakerBackendService": "dmtr-europe-west1-shop",
		"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1"}}}
	mark := len(g.s.Calls())
	r := workloadsEurope
	r.ID = "shop"
	code, out := g.call(t, WorkloadsShow, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.controllerCalls(mark); !slices.Equal(got, []string{"GET /v1/workloads/shop"}) {
		t.Fatalf("%v", got)
	}
	contains(t, out, "shop", "AWAKE", "shop-map", "shop-be", "dmtr-europe-west1-shop", "vm web-1 (europe-west1-b)")
	r.JSON = true
	_, out = g.call(t, WorkloadsShow, r)
	var w map[string]any
	if err := json.Unmarshal([]byte(out), &w); err != nil || w["backendService"] != "shop-be" {
		t.Fatalf("%v:\n%s", err, out)
	}
	r.ID = "ghost"
	code, out = g.call(t, WorkloadsShow, r)
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	contains(t, out, "no workload ghost")
}

func TestWorkloadsNapAndWakePostToTheController(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	r := workloadsEurope
	r.ID = "shop"
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsNap, r)
	if code != 0 || g.s.Workloads["shop"] != "ASLEEP" {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "shop", "ASLEEP")
	code, out = g.call(t, WorkloadsWake, r)
	if code != 0 || g.s.Workloads["shop"] != "AWAKE" {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.controllerCalls(mark); !slices.Equal(got, []string{"POST /v1/workloads/shop/nap", "POST /v1/workloads/shop/wake"}) {
		t.Fatalf("%v", got)
	}
	g.s.Workloads["shop"] = "WAKING"
	code, out = g.call(t, WorkloadsNap, r)
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	contains(t, out, "workload shop is WAKING")
	if strings.Contains(out, `{"error"`) {
		t.Fatalf("the controller's JSON is shown raw:\n%s", out)
	}
}

func TestWorkloadsCommandsNeedTheirPermissions(t *testing.T) {
	for name, c := range map[string]struct {
		run  func(context.Context, Deps, Request) int
		perm string
	}{"list": {WorkloadsList, "run.routes.invoke"}, "nap": {WorkloadsNap, "run.routes.invoke"},
		"onboard": {WorkloadsOnboard, "compute.instances.createTagBinding"}, "offboard": {WorkloadsOffboard, "compute.instanceGroupManagers.deleteTagBinding"}} {
		g := newWorkloadsRig(t)
		g.s.Workloads["shop"] = "AWAKE"
		g.s.HoldsAll = false
		g.s.Held = map[string]bool{}
		for _, command := range []string{"workloads", "onboard", "offboard"} {
			for _, p := range access.Needs(command) {
				g.s.Held[p] = p != c.perm
			}
		}
		mark := len(g.s.Calls())
		code, out := g.call(t, c.run, Request{Project: "dormeto-dev", Region: "europe-west1", ID: "shop", URLMap: "m", BackendService: "shop"})
		if code != 1 {
			t.Fatalf("%s: exit %d:\n%s", name, code, out)
		}
		contains(t, out, "missing "+c.perm, "Nothing was changed.")
		if m := g.changes(mark); len(m) != 0 || len(g.controllerCalls(mark)) != 0 {
			t.Fatalf("%s: changes %v", name, m)
		}
	}
}

var shopOnboarding = Request{Project: "dormeto-dev", Region: "europe-west1", URLMap: "shop-map", BackendService: "shop"}

func TestWorkloadsOnboardDryRunThenTagThenOnboard(t *testing.T) {
	g := newWorkloadsRig(t)
	mark := len(g.s.Calls())
	r := shopOnboarding
	r.IdleMinutes, r.WaitSeconds, r.Hibernate = ptr(30), ptr(20), ptr(true)
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	value := g.tagValue(t)
	// The order: the dry run, the bindings as the operator (each in its location), the onboarding.
	var order []string
	for _, c := range g.s.Calls()[mark:] {
		switch {
		case c.Host == controllerHost && c.Method == "POST":
			order = append(order, c.Method+" "+strings.TrimPrefix(c.URL, "https://"+controllerHost))
		case c.Method == "POST" && strings.HasSuffix(c.Path, "/v3/tagBindings"):
			order = append(order, "POST "+c.Host+c.Path)
		}
	}
	want := []string{"POST /v1/workloads?dryRun=true", "POST " + zoneHost + "/v3/tagBindings", "POST " + regionHost + "/v3/tagBindings", "POST /v1/workloads"}
	if !slices.Equal(order, want) {
		t.Fatalf("order:\n%s", strings.Join(order, "\n"))
	}
	settings := map[string]any{"idleMinutes": 30.0, "waitSeconds": 20.0, "hibernate": true}
	for _, query := range []string{"/v1/workloads?dryRun=true", "/v1/workloads"} {
		in := g.body(t, "POST", controllerHost, query)
		if in["urlMap"] != "shop-map" || in["backendService"] != "shop" || in["id"] != "shop" || len(in) != 4 || !jsonEqual(in["settings"], settings) {
			t.Errorf("%s: %v", query, in)
		}
	}
	bound := g.bindings()
	if !slices.Equal(bound, []gcptest.TagBinding{{Parent: vmParent, TagValue: value}, {Parent: groupParent, TagValue: value}}) {
		t.Fatalf("bindings %v", bound)
	}
	if b := g.body(t, "POST", zoneHost, "/v3/tagBindings"); b["parent"] != vmParent || b["tagValue"] != value {
		t.Fatalf("%v", b)
	}
	if g.s.Workloads["shop"] != "AWAKE" {
		t.Fatalf("%v", g.s.Workloads)
	}
	contains(t, out, "workload shop", "AWAKE", "web-1", "web-mig")
	if !slices.Contains(*g.audiences, "https://"+controllerHost) {
		t.Fatalf("audiences %v", *g.audiences)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestWorkloadsOnboardSendsNoSettingsWhenNoneIsGiven(t *testing.T) {
	g := newWorkloadsRig(t)
	if code, out := g.call(t, WorkloadsOnboard, shopOnboarding); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if in := g.body(t, "POST", controllerHost, "/v1/workloads"); in["settings"] != nil {
		t.Fatalf("%v", in)
	}
}

func TestWorkloadsOnboardJSONPrintsTheWorkload(t *testing.T) {
	g := newWorkloadsRig(t)
	r := shopOnboarding
	r.JSON = true
	code, out := g.call(t, WorkloadsOnboard, r)
	var w map[string]any
	if err := json.Unmarshal([]byte(out), &w); code != 0 || err != nil || w["id"] != "shop" || w["state"] != "AWAKE" {
		t.Fatalf("%d %v:\n%s", code, err, out)
	}
}

func TestWorkloadsOnboardTagsNothingWhenTheDryRunRefuses(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.DryRun.Reasons = []string{"backend service shop is in another URL map", "VM web-1 has a local SSD"}
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "not eligible", "backend service shop is in another URL map", "VM web-1 has a local SSD", "Nothing was changed.")
	if m := g.changes(mark); len(m) != 1 || m[0] != "POST "+controllerHost+"/v1/workloads?dryRun=true" {
		t.Fatalf("changes: %v", m)
	}
	if len(g.bindings()) != 0 || len(g.s.Workloads) != 0 {
		t.Fatal("something was changed")
	}
}

func TestWorkloadsOnboardDryRunFlagStopsBeforeTheTags(t *testing.T) {
	g := newWorkloadsRig(t)
	r := shopOnboarding
	r.DryRun = true
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "eligible", "web-1", "web-mig")
	if len(g.bindings()) != 0 || len(g.s.Workloads) != 0 {
		t.Fatal("something was changed")
	}
	if got := g.controllerCalls(mark); !slices.Equal(got, []string{"POST /v1/workloads?dryRun=true"}) {
		t.Fatalf("%v", got)
	}
}

func TestWorkloadsOnboardNeedsAnIDForABackendServiceNameTooLongForOne(t *testing.T) {
	g := newWorkloadsRig(t)
	long := "a" + strings.Repeat("b", 44) // 45 characters: Compute Engine accepts it, a workload ID does not
	r := shopOnboarding
	r.BackendService = long
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 2 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "--id", long)
	if len(g.s.Calls()) != mark {
		t.Fatalf("calls: %v", g.s.Calls()[mark:])
	}
	// With --id, the workload takes it; the backend service keeps its name.
	r.ID = "shop"
	if code, out = g.call(t, WorkloadsOnboard, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if in := g.body(t, "POST", controllerHost, "/v1/workloads"); in["id"] != "shop" || in["backendService"] != long {
		t.Fatalf("%v", in)
	}
}

func TestWorkloadsOnboardAsksForTheIDOnATerminal(t *testing.T) {
	g := newWorkloadsRig(t)
	g.d.Terminal = true
	g.d.Prompter = install.NewPrompter(strings.NewReader("shop\n"), g.out, true)
	r := shopOnboarding
	r.BackendService = "Shop_Backend"
	if code, out := g.call(t, WorkloadsOnboard, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, ok := g.s.Workloads["shop"]; !ok {
		t.Fatalf("%v", g.s.Workloads)
	}
}

func TestWorkloadsOnboardRefusesBadIDsAndSettingsBeforeAnyCall(t *testing.T) {
	g := newWorkloadsRig(t)
	for name, c := range map[string]struct {
		change func(*Request)
		want   []string
	}{
		"id with capitals":     {func(r *Request) { r.ID = "Shop" }, []string{"--id", "Shop"}},
		"id ending in dash":    {func(r *Request) { r.ID = "shop-" }, []string{"--id"}},
		"router name too long": {func(r *Request) { r.ID = strings.Repeat("a", 40); r.Region = "northamerica-northeast1" }, []string{"--id", "63"}},
		"idle too short":       {func(r *Request) { r.IdleMinutes = ptr(4) }, []string{"--idle-minutes", "5 to 1440"}},
		"idle too long":        {func(r *Request) { r.IdleMinutes = ptr(1441) }, []string{"--idle-minutes", "5 to 1440"}},
		"wait too short":       {func(r *Request) { r.WaitSeconds = ptr(4) }, []string{"--wait-seconds", "5 to 120"}},
		"wait too long":        {func(r *Request) { r.WaitSeconds = ptr(121) }, []string{"--wait-seconds", "5 to 120"}},
		"no url map":           {func(r *Request) { r.URLMap = "" }, []string{"--url-map"}},
		"no backend service":   {func(r *Request) { r.BackendService = "" }, []string{"--backend-service"}},
	} {
		r := shopOnboarding
		c.change(&r)
		mark := len(g.s.Calls())
		code, out := g.call(t, WorkloadsOnboard, r)
		if code != 2 {
			t.Errorf("%s: exit %d:\n%s", name, code, out)
			continue
		}
		contains(t, out, c.want...)
		if len(g.s.Calls()) != mark {
			t.Errorf("%s: calls: %v", name, g.s.Calls()[mark:])
		}
	}
}

func TestWorkloadsOnboardAcceptsTheRangesEnds(t *testing.T) {
	for _, c := range []struct{ idle, wait int }{{5, 5}, {1440, 120}} {
		g := newWorkloadsRig(t)
		r := shopOnboarding
		r.IdleMinutes, r.WaitSeconds = ptr(c.idle), ptr(c.wait)
		if code, out := g.call(t, WorkloadsOnboard, r); code != 0 {
			t.Fatalf("%v: exit %d:\n%s", c, code, out)
		}
	}
}

func TestWorkloadsOnboardGoesOnWhenATagIsBoundAlready(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.TagBindings = []gcptest.TagBinding{{Parent: vmParent, TagValue: g.tagValue(t)}}
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 0 || len(g.bindings()) != 2 {
		t.Fatalf("exit %d, %v:\n%s", code, g.bindings(), out)
	}
}

func TestWorkloadsOnboardSaysTheControllersRefusal(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "workload shop is onboarded already")
	if strings.Contains(out, `{"error"`) {
		t.Fatalf("the controller's JSON is shown raw:\n%s", out)
	}
}

func TestWorkloadsOnboardNeedsTheInstalledTag(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Lock()
	g.s.TagValues = map[string]*gcptest.Tag{}
	g.s.Unlock()
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "dormeto/managed", "dormeto gcp install")
	if len(g.s.Workloads) != 0 {
		t.Fatal("onboarded")
	}
}

func TestWorkloadsOffboardDeletesThenRemovesTheTagBindings(t *testing.T) {
	g := newWorkloadsRig(t)
	value := g.tagValue(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{
		map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1"},
		map[string]any{"kind": "mig", "location": "europe-west1", "name": "web-mig"}}}
	other := gcptest.TagBinding{Parent: "//compute.googleapis.com/projects/1034397096264/zones/europe-west1-b/instances/999", TagValue: value}
	g.s.TagBindings = []gcptest.TagBinding{{Parent: vmParent, TagValue: value}, {Parent: groupParent, TagValue: value}, other}
	mark := len(g.s.Calls())
	r := workloadsEurope
	r.ID = "shop"
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if len(g.s.Workloads) != 0 {
		t.Fatal("not offboarded")
	}
	if got := g.bindings(); !slices.Equal(got, []gcptest.TagBinding{other}) {
		t.Fatalf("bindings %v", got)
	}
	changes := g.changes(mark)
	if len(changes) != 3 || changes[0] != "DELETE "+controllerHost+"/v1/workloads/shop" ||
		!strings.HasPrefix(changes[1], "DELETE "+zoneHost+"/v3/tagBindings/"+vmParent+"/tagValues/") ||
		!strings.HasPrefix(changes[2], "DELETE "+regionHost+"/v3/tagBindings/"+groupParent+"/tagValues/") {
		t.Fatalf("changes:\n%s", strings.Join(changes, "\n"))
	}
	contains(t, out, "Offboarded workload shop", "web-1", "web-mig")
}

func TestWorkloadsOffboardOfAnUnknownWorkloadChangesNothing(t *testing.T) {
	g := newWorkloadsRig(t)
	r := workloadsEurope
	r.ID = "ghost"
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "no workload ghost")
	if m := g.changes(mark); len(m) != 0 {
		t.Fatalf("changes: %v", m)
	}
}

func TestWorkloadsOffboardJSONSaysWhatItUntagged(t *testing.T) {
	g := newWorkloadsRig(t)
	value := g.tagValue(t)
	g.s.Workloads["shop"] = "ASLEEP"
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1"}}}
	g.s.TagBindings = []gcptest.TagBinding{{Parent: vmParent, TagValue: value}}
	r := workloadsEurope
	r.ID, r.JSON = "shop", true
	code, out := g.call(t, WorkloadsOffboard, r)
	var got struct {
		ID         string
		Offboarded bool
		Untagged   []string
	}
	if err := json.Unmarshal([]byte(out), &got); code != 0 || err != nil || got.ID != "shop" || !got.Offboarded || len(got.Untagged) != 1 {
		t.Fatalf("%d %v:\n%s", code, err, out)
	}
}
