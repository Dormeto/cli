package gcponboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/gcp/access"
	"github.com/Dormeto/cli/internal/gcp/gcptest"
	"github.com/Dormeto/cli/internal/install"
	"github.com/Dormeto/cli/internal/schedule"
)

const (
	vmParent    = "//compute.googleapis.com/projects/1034397096264/zones/europe-west1-b/instances/111"
	groupParent = "//compute.googleapis.com/projects/1034397096264/regions/europe-west1/instanceGroupManagers/222"
	zoneHost    = "europe-west1-b-cloudresourcemanager.googleapis.com"
	regionHost  = "europe-west1-cloudresourcemanager.googleapis.com"
)

var workloadsEurope = Request{Project: "dormeto-dev", Region: "europe-west1"}

func ptr[T any](v T) *T { return &v }

// workloadsRig is an installed router whose dry run names a VM and a regional group, and whose backend service shop logs every
// request (global and in the router's region), with the audiences of the identity tokens collected.
type workloadsRig struct {
	*installRig
	audiences *[]string
}

func newWorkloadsRig(t *testing.T) *workloadsRig {
	t.Helper()
	return workloadsRigOf(routerInstalled(t))
}

// newPublicWorkloadsRig is newWorkloadsRig with the router of Dormeto's public image, at the CLI's version.
func newPublicWorkloadsRig(t *testing.T) *workloadsRig {
	t.Helper()
	g := publicRig(t, embeddedRouterVersion())
	if code, out := g.run(t, plain); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	return workloadsRigOf(g)
}

func workloadsRigOf(g *installRig) *workloadsRig {
	aud := &[]string{}
	g.d.Google.Tokens = gcptest.Tokens{AccountName: "me@example.com", Audiences: aud}
	g.s.DryRun = gcptest.DryRun{
		VMs:    []gcptest.Resource{{Location: "europe-west1-b", Name: "web-1", ID: "111"}},
		Groups: []gcptest.Resource{{Kind: "regional", Location: "europe-west1", Name: "web-mig", ID: "222"}},
	}
	for _, location := range []string{"global", "europe-west1"} {
		g.s.BackendServices[location+"/shop"] = &gcptest.BackendService{LogConfig: map[string]any{"enable": true, "sampleRate": 1.0}}
	}
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

// onboarding is the body of the onboarding itself, not of its dry run.
func (g *workloadsRig) onboarding(t *testing.T) map[string]any {
	t.Helper()
	for _, c := range g.s.Calls() {
		if c.Method == "POST" && c.URL == "https://"+controllerHost+"/v1/workloads" {
			var m map[string]any
			if err := json.Unmarshal([]byte(c.Body), &m); err != nil {
				t.Fatalf("%s: %v", c.Body, err)
			}
			return m
		}
	}
	t.Fatal("no onboarding")
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
		"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"}}}
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
	// The answer is at once and the action runs through Cloud Tasks: the state read is the current one, not the result.
	contains(t, out, "Nap of workload shop asked", "current state: AWAKE", "workloads show shop")
	code, out = g.call(t, WorkloadsWake, r)
	if code != 0 || g.s.Workloads["shop"] != "AWAKE" {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Wake of workload shop asked", "current state: ASLEEP", "workloads show shop")
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

// refusedOnboarding is what the controller says at the onboarding of what its dry run found eligible: what changed between the two.
const refusedOnboarding = "cannot onboard shop: backend service shop is not named in URL map shop-map"

func TestWorkloadsOnboardDryRunThenTagThenOnboard(t *testing.T) {
	g := newWorkloadsRig(t)
	mark := len(g.s.Calls())
	r := shopOnboarding
	r.IdleMinutes, r.Hibernate = ptr(30), ptr(true)
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
	settings := map[string]any{"idleMinutes": 30.0, "hibernate": true}
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
	g.s.BackendServices["global/"+long] = &gcptest.BackendService{}
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
	g.s.BackendServices["global/Shop_Backend"] = &gcptest.BackendService{}
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
		"no url map":           {func(r *Request) { r.URLMap = "" }, []string{"--url-map"}},
		"no backend service":   {func(r *Request) { r.BackendService = "" }, []string{"--backend-service"}},
		"wake too short":       {func(r *Request) { r.WakeTimeout = ptr(59) }, []string{"--wake-timeout", "60 to 1700"}},
		"wake too long":        {func(r *Request) { r.WakeTimeout = ptr(1701) }, []string{"--wake-timeout", "60 to 1700"}},
		"load balancer":        {func(r *Request) { r.LoadBalancer = "private" }, []string{"--load-balancer", "private", "global, regional, classic, internal or cross-region-internal"}},
		"wake mode":            {func(r *Request) { r.WakeMode = "never" }, []string{"--wake-mode", "any or browser"}},
		"six fallback types": {func(r *Request) {
			r.FallbackTypes = []string{"e2-small", "e2-medium", "n1-standard-1", "n2-standard-2", "n2-standard-4", "c2-standard-4"}
		}, []string{"--fallback-type", "at most 5"}},
		"fallback type, EC2's":      {func(r *Request) { r.FallbackTypes = []string{"t3a.medium"} }, []string{"--fallback-type", "t3a.medium", "machine type"}},
		"fallback type twice":       {func(r *Request) { r.FallbackTypes = []string{"e2-small", "e2-small"} }, []string{"--fallback-type", "e2-small", "twice"}},
		"cluster, no slash":         {func(r *Request) { r.GKECluster = "shop-cluster" }, []string{"--gke-cluster", "<location>/<name>"}},
		"cluster, bad name":         {func(r *Request) { r.GKECluster = "europe-west1-b/Shop" }, []string{"--gke-cluster", "<location>/<name>"}},
		"cluster, no location":      {func(r *Request) { r.GKECluster = "/shop-cluster" }, []string{"--gke-cluster", "<location>/<name>"}},
		"cloud sql, bad name":       {func(r *Request) { r.CloudSQL = []string{"db", "Bad_Name"} }, []string{"--cloud-sql", "Bad_Name"}},
		"cloud sql, twice":          {func(r *Request) { r.CloudSQL = []string{"db", "db"} }, []string{"--cloud-sql", "db", "twice"}},
		"cluster with json, no yes": {func(r *Request) { r.GKECluster, r.JSON = "europe-west1-b/shop-cluster", true }, []string{"--yes"}},
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
	for _, idle := range []int{5, 1440} {
		g := newWorkloadsRig(t)
		r := shopOnboarding
		r.IdleMinutes = ptr(idle)
		if code, out := g.call(t, WorkloadsOnboard, r); code != 0 {
			t.Fatalf("%d: exit %d:\n%s", idle, code, out)
		}
	}
}

func TestWorkloadsOnboardSendsTheLoadBalancerAndTheWakeTimeoutToBothCalls(t *testing.T) {
	for _, wake := range []int{60, 1700} {
		g := newWorkloadsRig(t)
		r := shopOnboarding
		r.LoadBalancer, r.WakeTimeout = "regional", ptr(wake)
		if code, out := g.call(t, WorkloadsOnboard, r); code != 0 {
			t.Fatalf("%d: exit %d:\n%s", wake, code, out)
		}
		for _, query := range []string{"/v1/workloads?dryRun=true", "/v1/workloads"} {
			in := g.body(t, "POST", controllerHost, query)
			if in["loadBalancer"] != "regional" || !jsonEqual(in["settings"], map[string]any{"wakeTimeoutSeconds": wake}) {
				t.Errorf("%s: %v", query, in)
			}
		}
	}
}

func TestWorkloadsOnboardSendsNoLoadBalancerWhenNoneIsGiven(t *testing.T) {
	g := newWorkloadsRig(t)
	if code, out := g.call(t, WorkloadsOnboard, shopOnboarding); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	in := g.body(t, "POST", controllerHost, "/v1/workloads")
	for _, key := range []string{"loadBalancer", "cloudSql", "gke"} {
		if _, there := in[key]; there {
			t.Errorf("%s sent: %v", key, in)
		}
	}
}

// A controller that does not read the flag would go on with what it found by itself.
func TestWorkloadsOnboardStopsWhenTheDryRunIgnoresTheLoadBalancer(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.ControllerBelow035 = true
	r := shopOnboarding
	r.LoadBalancer = "classic"
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "--load-balancer", "0.35.0", "Nothing was changed.",
		"Install a newer router first: dormeto gcp routers install --project dormeto-dev --region europe-west1 --image <the router's image, 0.35.0 or later>.\n")
	if m := g.changes(mark); len(m) != 1 || len(g.bindings()) != 0 {
		t.Fatalf("changes %v", m)
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
	contains(t, out, "dormeto/managed", "dormeto gcp routers install")
	if len(g.s.Workloads) != 0 {
		t.Fatal("onboarded")
	}
}

func TestWorkloadsOffboardDeletesThenRemovesTheTagBindings(t *testing.T) {
	g := newWorkloadsRig(t)
	value := g.tagValue(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{
		map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"},
		map[string]any{"kind": "mig", "location": "europe-west1", "name": "web-mig", "id": "222"}}}
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
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"}}}
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

func (g *workloadsRig) offboardFile() string {
	return filepath.Join(g.cache, "dormeto", "gcp", "dormeto-dev-europe-west1-offboard-shop.json")
}

// stuckOffboarding offboards shop, the first unbind refused: the controller's workload is gone, its bindings are not.
func (g *workloadsRig) stuckOffboarding(t *testing.T) {
	t.Helper()
	value := g.tagValue(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{
		map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"},
		map[string]any{"kind": "mig", "location": "europe-west1", "name": "web-mig", "id": "222"}}}
	g.s.TagBindings = []gcptest.TagBinding{{Parent: vmParent, TagValue: value}, {Parent: groupParent, TagValue: value}}
	g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: zoneHost, Path: "/v3/tagBindings", Times: 1, Code: 500, Status: "INTERNAL", Message: "boom"})
	r := workloadsEurope
	r.ID = "shop"
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "boom")
	if len(g.s.Workloads) != 0 || len(g.bindings()) == 0 {
		t.Fatalf("workloads %v bindings %v", g.s.Workloads, g.bindings())
	}
	if _, err := os.Stat(g.offboardFile()); err != nil {
		t.Fatalf("the backends are not kept for a rerun: %v", err)
	}
}

// The router has deleted the run once it answers: a failed untag must not lose the VMs it left on a fallback type, and a rerun
// finds no workload to name them.
func TestWorkloadsOffboardNamesTheVMsOnAFallbackTypeEvenWhenTheUntagFails(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.OnFallback = map[string]map[string]map[string]string{"shop": {"web-1": {"from": "e2-small", "to": "e2-medium"}}}
	g.stuckOffboarding(t)
	r := workloadsEurope
	r.ID = "shop"
	g.s.Workloads["shop"] = "AWAKE"
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"}}}
	g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: zoneHost, Path: "/v3/tagBindings", Times: 1, Code: 500, Status: "INTERNAL", Message: "boom"})
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "boom", "VM web-1 is on e2-medium, its own is e2-small: it keeps e2-medium until you change it by hand")
}

func TestWorkloadsOffboardRerunFinishesTheUnbindsAfterAFailure(t *testing.T) {
	g := newWorkloadsRig(t)
	g.stuckOffboarding(t)
	r := workloadsEurope
	r.ID = "shop"
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Offboarded workload shop", "web-1", "web-mig")
	if len(g.bindings()) != 0 {
		t.Fatalf("bindings %v", g.bindings())
	}
	if _, err := os.Stat(g.offboardFile()); !os.IsNotExist(err) {
		t.Fatalf("the file stays: %v", err)
	}
}

func TestWorkloadsOffboardKeepsTheTagUntilTheControllerDeleted(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: controllerHost, Path: "/v1/workloads/shop", Times: 1, Code: 500, Status: "INTERNAL", Message: "boom"})
	g.s.TagBindings = []gcptest.TagBinding{{Parent: vmParent, TagValue: g.tagValue(t)}}
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"}}}
	r := workloadsEurope
	r.ID = "shop"
	if code, out := g.call(t, WorkloadsOffboard, r); code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if len(g.bindings()) != 1 || len(g.s.Workloads) != 1 {
		t.Fatalf("bindings %v workloads %v", g.bindings(), g.s.Workloads)
	}
}

func TestWorkloadsOnboardRefusedAfterTheTagsRemovesTheOnesItMade(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.OnboardingRefused = refusedOnboarding
	value := g.tagValue(t)
	before := gcptest.TagBinding{Parent: vmParent, TagValue: value} // there first: kept
	g.s.TagBindings = []gcptest.TagBinding{before}
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, refusedOnboarding, "removed", "web-mig", "fix the refusal", "run the same command again")
	if strings.Contains(out, "or offboard") {
		t.Fatalf("there is no workload to offboard:\n%s", out)
	}
	if got := g.bindings(); !slices.Equal(got, []gcptest.TagBinding{before}) {
		t.Fatalf("bindings %v", got)
	}
}

func TestWorkloadsOnboardRefusedSaysWhichBindingItCouldNotRemove(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.OnboardingRefused = refusedOnboarding
	g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: regionHost, Path: "/v3/tagBindings", Times: 1, Code: 500, Status: "INTERNAL", Message: "boom"})
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, refusedOnboarding, "not removed", "group web-mig (europe-west1)", "boom")
	if len(g.bindings()) != 1 {
		t.Fatalf("bindings %v", g.bindings())
	}
}

func TestWorkloadsWakeDuringANapIsKeptPending(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "NAPPING"
	r := workloadsEurope
	r.ID = "shop"
	code, out := g.call(t, WorkloadsWake, r)
	if code != 0 || g.s.Workloads["shop"] != "NAPPING" {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "napping", "kept", "once the nap ends", "current state: NAPPING", "workloads show shop")
	r.JSON = true
	_, out = g.call(t, WorkloadsWake, r)
	var w map[string]any
	if err := json.Unmarshal([]byte(out), &w); err != nil || w["state"] != "NAPPING" || w["id"] != "shop" {
		t.Fatalf("%v:\n%s", err, out)
	}
}

// failingToWake is a workload whose wake fails, with a VM and a group bound to the tag.
func (g *workloadsRig) failingToWake(t *testing.T) {
	t.Helper()
	value := g.tagValue(t)
	g.s.Workloads["shop"] = "FAILED"
	g.s.WakeFails["shop"] = true
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{
		map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"},
		map[string]any{"kind": "mig", "location": "europe-west1", "name": "web-mig", "id": "222"}}}
	g.s.TagBindings = []gcptest.TagBinding{{Parent: vmParent, TagValue: value}, {Parent: groupParent, TagValue: value}}
}

func TestWorkloadsOffboardOfAWorkloadThatCannotWakeNamesForce(t *testing.T) {
	g := newWorkloadsRig(t)
	g.failingToWake(t)
	mark := len(g.s.Calls())
	r := workloadsEurope
	r.ID = "shop"
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "its wake failed", "--force", "its backends are then left down")
	if got := g.controllerCalls(mark); !slices.Equal(got[len(got)-1:], []string{"DELETE /v1/workloads/shop"}) || slices.Contains(got, "DELETE /v1/workloads/shop?force=true") {
		t.Fatalf("%v", got)
	}
	if len(g.s.Workloads) != 1 || len(g.bindings()) != 2 {
		t.Fatalf("workloads %v bindings %v", g.s.Workloads, g.bindings())
	}
}

func TestWorkloadsOffboardForceLeavesTheBackendsDownAndRemovesTheTags(t *testing.T) {
	g := newWorkloadsRig(t)
	g.failingToWake(t)
	mark := len(g.s.Calls())
	r := workloadsEurope
	r.ID, r.Force = "shop", true
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var deletes []string
	for _, c := range g.controllerCalls(mark) {
		if strings.HasPrefix(c, "DELETE") {
			deletes = append(deletes, c)
		}
	}
	if !slices.Equal(deletes, []string{"DELETE /v1/workloads/shop", "DELETE /v1/workloads/shop?force=true"}) {
		t.Fatalf("%v", deletes)
	}
	contains(t, out, "Offboarded workload shop", "left down", "vm web-1 (europe-west1-b)", "group web-mig (europe-west1)")
	if len(g.bindings()) != 0 || len(g.s.Workloads) != 0 {
		t.Fatalf("workloads %v bindings %v", g.s.Workloads, g.bindings())
	}
}

func TestWorkloadsOffboardForceTriesThePlainDeleteFirst(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	mark := len(g.s.Calls())
	r := workloadsEurope
	r.ID, r.Force = "shop", true
	if code, out := g.call(t, WorkloadsOffboard, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.controllerCalls(mark); slices.Contains(got, "DELETE /v1/workloads/shop?force=true") {
		t.Fatalf("%v", got)
	}
}

func TestWorkloadsNapAndWakeSayAlreadyWhenNothingWasAsked(t *testing.T) {
	g := newWorkloadsRig(t)
	r := workloadsEurope
	r.ID = "shop"
	for _, c := range []struct {
		state string
		run   func(context.Context, Deps, Request) int
		want  string
	}{{"ASLEEP", WorkloadsNap, "Workload shop is already asleep: nothing was asked.\n"}, {"AWAKE", WorkloadsWake, "Workload shop is already awake: nothing was asked.\n"}} {
		g.s.Workloads["shop"] = c.state
		code, out := g.call(t, c.run, r)
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		contains(t, out, c.want)
		if strings.Contains(out, "Cloud Tasks") || strings.Contains(out, "Follow it") {
			t.Fatalf("%s", out)
		}
	}
}

func TestWorkloadsWakeDuringAWakeSaysTheControllersRefusalAlone(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "WAKING"
	r := workloadsEurope
	r.ID = "shop"
	code, out := g.call(t, WorkloadsWake, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "workload shop is WAKING\n")
	if strings.Contains(out, "google: 409") {
		t.Fatalf("%s", out)
	}
}

// A companion deleted is the router's to read, the operator's to act on.
func TestWorkloadsShowSaysACompanionGone(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{map[string]any{"kind": "cloudsql", "name": "shop-db"}}}
	r := workloadsEurope
	r.ID = "shop"
	_, out := g.call(t, WorkloadsShow, r)
	if strings.Contains(out, "no longer exists") {
		t.Fatalf("none gone:\n%s", out)
	}
	g.s.WorkloadDocs["shop"]["companionsGone"] = []any{"shop-db"}
	code, out := g.call(t, WorkloadsShow, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "! Cloud SQL instance shop-db no longer exists: its workload no longer naps; offboard and onboard it again")
}

func TestWorkloadsShowSaysTheControllersRefusalAlone(t *testing.T) {
	g := newWorkloadsRig(t)
	r := workloadsEurope
	r.ID = "ghost"
	code, out := g.call(t, WorkloadsShow, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "no workload ghost\n")
	if strings.Contains(out, "google: 404") {
		t.Fatalf("%s", out)
	}
}

// A lost race on the binding (409 ABORTED) leaves the VM or group untagged: it is not "bound already".
func TestWorkloadsOnboardStopsOnABindingAbortedRatherThanTakeItAsBound(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Refuse(gcptest.Refusal{Method: "POST", Host: regionHost, Path: "/v3/tagBindings", Times: 1, Code: 409, Status: "ABORTED", Message: "try again"})
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "the tag is not bound to group web-mig (europe-west1)", "try again", "vm web-1 (europe-west1-b)")
	if len(g.bindings()) != 0 || len(g.s.Workloads) != 0 {
		t.Fatalf("bindings %v workloads %v", g.bindings(), g.s.Workloads)
	}
}

func TestWorkloadsOnboardKeepsTheBindingsWhenTheControllersAnswerIsLost(t *testing.T) {
	for name, answer := range map[string]func(w http.ResponseWriter){
		"connection": gcptest.Cut,
		"gateway":    func(w http.ResponseWriter) { gcptest.Fail(w, 502, "", "bad gateway") },
	} {
		t.Run(name, func(t *testing.T) {
			g := newWorkloadsRig(t)
			g.s.AnswerOnboarding = answer
			r := shopOnboarding
			r.Configuration = "c"
			code, out := g.call(t, WorkloadsOnboard, r)
			if code != 1 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			contains(t, out, "the workload may be onboarded", "The tag bindings stay", "dormeto gcp workloads show shop --project dormeto-dev --region europe-west1 --gcloud-configuration c")
			if strings.Contains(out, "are removed") {
				t.Fatalf("%s", out)
			}
			if len(g.bindings()) != 2 || g.s.Workloads["shop"] != "AWAKE" {
				t.Fatalf("bindings %v workloads %v", g.bindings(), g.s.Workloads)
			}
		})
	}
}

func TestWorkloadsOnboardInterruptedInTheControllersCallKeepsTheBindingsAndSaysSo(t *testing.T) {
	g := newWorkloadsRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.s.AnswerOnboarding = func(w http.ResponseWriter) { cancel(); gcptest.Cut(w) }
	g.out.Reset()
	code := WorkloadsOnboard(ctx, g.d, shopOnboarding)
	out := g.out.String()
	if code != 130 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "the workload may be onboarded", "The tag bindings stay")
	if len(g.bindings()) != 2 {
		t.Fatalf("bindings %v", g.bindings())
	}
}

const forbidden = "Permission denied on the resource (or it may not exist)."

func TestWorkloadsOffboardGoesOnWhenTheBindingsOfAVMDeletedByHandAreDenied(t *testing.T) {
	g := newWorkloadsRig(t)
	g.failingToWake(t)
	g.s.WakeFails["shop"] = false
	g.s.TagBindings = []gcptest.TagBinding{{Parent: groupParent, TagValue: g.tagValue(t)}}
	g.s.Refuse(gcptest.Refusal{Method: "GET", Host: zoneHost, Path: "/v3/tagBindings", Code: 403, Status: "PERMISSION_DENIED", Message: forbidden})
	r := workloadsEurope
	r.ID, r.JSON = "shop", true
	var errs bytes.Buffer
	g.d.Err = &errs
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s\n%s", code, out, errs.String())
	}
	// The warning is the error stream's: the answer stays JSON.
	var got struct{ Untagged []string }
	if err := json.Unmarshal([]byte(out), &got); err != nil || !slices.Equal(got.Untagged, []string{"group web-mig (europe-west1)"}) {
		t.Fatalf("%v: %s", err, out)
	}
	contains(t, errs.String(), "  ! the tag bindings of vm web-1 (europe-west1-b) are not read", "it may be gone", forbidden)
	if len(g.bindings()) != 0 {
		t.Fatalf("bindings %v", g.bindings())
	}
	if _, err := os.Stat(g.offboardFile()); !os.IsNotExist(err) {
		t.Fatalf("the file stays: %v", err)
	}
}

// SERVICE_DISABLED is a 403 too: it says nothing of the VM.
func TestWorkloadsOffboardStopsOnAnAPIOffRatherThanTakeTheVMAsGone(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"}}}
	g.s.TagBindings = []gcptest.TagBinding{{Parent: vmParent, TagValue: g.tagValue(t)}}
	g.s.Refuse(gcptest.Refusal{Method: "GET", Host: zoneHost, Path: "/v3/tagBindings", Times: 1, Code: 403, Status: "PERMISSION_DENIED", Message: "off", Reason: "SERVICE_DISABLED"})
	r := workloadsEurope
	r.ID = "shop"
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "run the same command again to finish")
	if len(g.bindings()) != 1 {
		t.Fatalf("bindings %v", g.bindings())
	}
}

func TestWorkloadsListOffboardAndOnboardSayTheControllersRefusalAlone(t *testing.T) {
	refused := func(method, path string) gcptest.Refusal {
		return gcptest.Refusal{Method: method, Host: controllerHost, Path: path, Code: 409, Status: "ABORTED", Message: "the controller says no", Reason: "busy"}
	}
	cases := map[string]struct {
		run     func(context.Context, Deps, Request) int
		r       Request
		refusal gcptest.Refusal
	}{
		"list":          {WorkloadsList, workloadsEurope, refused("GET", "/v1/workloads")},
		"offboard read": {WorkloadsOffboard, Request{Project: "dormeto-dev", Region: "europe-west1", ID: "shop"}, refused("GET", "/v1/workloads/shop")},
		"offboard":      {WorkloadsOffboard, Request{Project: "dormeto-dev", Region: "europe-west1", ID: "shop"}, refused("DELETE", "/v1/workloads/shop")},
		"onboard":       {WorkloadsOnboard, shopOnboarding, refused("POST", "/v1/workloads")},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			g := newWorkloadsRig(t)
			if name != "onboard" {
				g.s.Workloads["shop"] = "AWAKE"
			}
			g.s.Refuse(c.refusal)
			code, out := g.call(t, c.run, c.r)
			if code != 1 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			contains(t, out, "the controller says no\n")
			if strings.Contains(out, "google: ") {
				t.Fatalf("%s", out)
			}
		})
	}
}

func TestWorkloadsOffboardOfAnUnknownWorkloadSaysTheControllersRefusalAlone(t *testing.T) {
	g := newWorkloadsRig(t)
	r := workloadsEurope
	r.ID = "ghost"
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 1 || strings.Contains(out, "google: ") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "no workload ghost\n")
}

// A schedule as the CLI reads it from a file: a nap window at night and an awake window by day, in Paris.
const parisSchedule = `{"timeZone":"Europe/Paris","windows":[{"kind":"nap","days":["mon","tue"],"start":"22:00","end":"06:00"}],"maxNapDays":14}`

var parisEvents = []any{
	map[string]any{"at": "2026-11-02T21:00:00.000Z", "kind": "nap-start", "window": 0},
	map[string]any{"at": "2026-11-03T05:00:00.000Z", "kind": "nap-end", "window": 0},
}

func (g *workloadsRig) scheduleFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorkloadsScheduleSetShowClearCallTheControllerAsTheOperator(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.NextEvents = parisEvents
	r := workloadsEurope
	r.ID = "shop"
	mark := len(g.s.Calls())
	r.File = g.scheduleFile(t, parisSchedule)
	code, out := g.call(t, WorkloadsScheduleSet, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	body := g.body(t, "PUT", controllerHost, "/v1/workloads/shop/schedule")
	if body["timeZone"] != "Europe/Paris" || body["maxNapDays"] != float64(14) || body["by"] != "me@example.com" {
		t.Fatalf("%v", body)
	}
	r.File = ""
	code, out = g.call(t, WorkloadsScheduleShow, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// The events with their local time and zone, as the router's nextEvents gives them in UTC.
	contains(t, out, "Europe/Paris", "nap", "mon", "22:00", "06:00", "maxNapDays", "2026-11-02 22:00", "CET", "nap starts", "2026-11-03 06:00", "nap ends")
	r.JSON = true
	_, out = g.call(t, WorkloadsScheduleShow, r)
	var shown map[string]any
	if err := json.Unmarshal([]byte(out), &shown); err != nil || len(shown["nextEvents"].([]any)) != 2 || shown["schedule"] == nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	r.JSON = false
	code, out = g.call(t, WorkloadsScheduleClear, r)
	if code != 0 || g.s.Schedules["shop"] != nil {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "cleared")
	_, out = g.call(t, WorkloadsScheduleShow, r)
	contains(t, out, "no schedule")
	want := []string{"PUT /v1/workloads/shop/schedule", "GET /v1/workloads/shop/schedule", "GET /v1/workloads/shop/schedule",
		"DELETE /v1/workloads/shop/schedule", "GET /v1/workloads/shop/schedule"}
	if got := g.controllerCalls(mark); !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
	if !slices.Contains(*g.audiences, "https://"+controllerHost) {
		t.Fatalf("audiences %v", *g.audiences)
	}
}

func TestWorkloadsScheduleSetSaysTheControllersRefusalAndExitsOne(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.ScheduleProblem = "windows[0].start: not a time on a 5-minute step"
	r := workloadsEurope
	r.ID, r.File = "shop", g.scheduleFile(t, parisSchedule)
	code, out := g.call(t, WorkloadsScheduleSet, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "windows[0].start: not a time on a 5-minute step")
	if strings.Contains(out, "google: 400") || strings.Contains(out, `{"error"`) {
		t.Fatalf("%s", out)
	}
}

// A recurring schedule, as a router 0.36.0 takes it.
const recurringSchedule = `{"timeZone":"Europe/Paris","windows":[` +
	`{"kind":"nap","start":"20:00","end":"07:00","repeat":{"pattern":"weekly","interval":2,"days":["mon","thu"]},"from":"2027-01-04","wake":"header"},` +
	`{"kind":"awake","start":"08:00","end":"18:00","repeat":{"pattern":"monthly","interval":3,"nth":1,"weekday":"mon"},"from":"2027-01-01","until":"2027-06-30"},` +
	`{"kind":"awake","repeat":{"pattern":"cron","cron":"30 8 * * 1-5","minutes":630}},` +
	`{"kind":"nap","days":["sat"],"start":"22:00","end":"23:00","wake":"header"}],"maxNapDays":14}`

func TestWorkloadsScheduleSaysARecurrenceAndKeepsItThroughJSON(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	r := workloadsEurope
	r.ID = "shop"
	r.File = g.scheduleFile(t, recurringSchedule)
	code, out := g.call(t, WorkloadsScheduleSet, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(recurringSchedule), &want); err != nil {
		t.Fatal(err)
	}
	body := g.body(t, "PUT", controllerHost, "/v1/workloads/shop/schedule")
	if !reflect.DeepEqual(body["windows"], want["windows"]) {
		t.Fatalf("sent %v", body["windows"])
	}
	contains(t, out,
		"  nap window: Every 2 weeks on Mon, Thu, 20:00–07:00 next day, from 2027-01-04, no end date, wake header\n",
		"  awake window: Every 3 months on the first Mon, 08:00–18:00, from 2027-01-01, until 2027-06-30\n",
		"  awake window: Cron 30 8 * * 1-5, 10 h 30 min\n",
		"  nap window: sat 22:00-23:00, wake header\n")

	// What show --json prints under "schedule" is the file set takes again.
	r.File, r.JSON = "", true
	_, out = g.call(t, WorkloadsScheduleShow, r)
	var shown struct {
		Schedule json.RawMessage `json:"schedule"`
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	var again map[string]any
	if err := json.Unmarshal(shown.Schedule, &again); err != nil || !reflect.DeepEqual(again["windows"], want["windows"]) {
		t.Fatalf("%v: shown %s", err, shown.Schedule)
	}
	r.JSON, r.File = false, g.scheduleFile(t, string(shown.Schedule))
	if code, out := g.call(t, WorkloadsScheduleSet, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !reflect.DeepEqual(g.s.Schedules["shop"]["windows"], want["windows"]) {
		t.Fatalf("stored %v", g.s.Schedules["shop"])
	}
}

// A stored window with an unreadable pattern raises no error.
func TestWorkloadsScheduleShowAnUnreadableRepeatRaisesNoError(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.Schedules["shop"] = map[string]any{"timeZone": "Europe/Paris", "by": "me", "at": "now", "maxNapDays": 14, "windows": []any{
		map[string]any{"kind": "awake", "start": "08:00", "end": "18:00", "repeat": map[string]any{"pattern": "monthly", "day": "first"}},
	}}
	r := workloadsEurope
	r.ID = "shop"
	code, out := g.call(t, WorkloadsScheduleShow, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  awake window: repeat unreadable, no occurrence\n", "  maxNapDays: 14\n")
}

func TestWorkloadsScheduleSetOnARouterBefore036SaysItNeeds036(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.ScheduleProblem = "windows[0].repeat: unknown field; windows[0].from: unknown field"
	r := workloadsEurope
	r.ID, r.File = "shop", g.scheduleFile(t, recurringSchedule)
	code, out := g.call(t, WorkloadsScheduleSet, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "windows[0].repeat: unknown field; windows[0].from: unknown field\n",
		"Recurrences and joined windows need a router of version 0.36.0 or later. Upgrade it with: dormeto gcp routers install --project "+r.Project+" --region "+r.Region+" --image <the router's image, 0.36.0 or later>")
	// This router's own refusal, in a recurrence: its words alone.
	g.s.ScheduleProblem = "windows[0].repeat.cron: five fields: minute hour day-of-month month day-of-week"
	code, out = g.call(t, WorkloadsScheduleSet, r)
	if code != 1 || strings.Contains(out, "0.36.0") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "windows[0].repeat.cron: five fields")
}

func TestWorkloadsScheduleSaysTheJoinAndKeepsIt(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.NextEvents = parisEvents
	g.s.ScheduleJoin = map[string]any{"joinMinutes": 0, "joinFrom": "workload", "joinInherited": map[string]any{"joinMinutes": 15, "joinFrom": "default"}}
	r := workloadsEurope
	r.ID = "shop"
	r.File = g.scheduleFile(t, `{"timeZone":"Europe/Paris","windows":[{"kind":"nap","days":["mon"],"start":"22:00","end":"23:00"}],"joinMinutes":0}`)
	code, out := g.call(t, WorkloadsScheduleSet, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if body := g.body(t, "PUT", controllerHost, "/v1/workloads/shop/schedule"); body["joinMinutes"] != float64(0) {
		t.Fatalf("sent %v", body)
	}
	contains(t, out, "  windows never join (workload)\nNext events:\n")
	r.File, r.JSON = "", true
	_, out = g.call(t, WorkloadsScheduleShow, r)
	var shown map[string]any
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if v, ok := shown["joinMinutes"]; !ok || v != float64(0) || shown["joinFrom"] != "workload" || shown["joinInherited"] == nil {
		t.Fatalf("%s", out)
	}
	// The default, and a controller that answers none of it.
	r.JSON = false
	g.s.ScheduleJoin = map[string]any{"joinMinutes": 15, "joinFrom": "default", "joinInherited": map[string]any{"joinMinutes": 15, "joinFrom": "default"}}
	_, out = g.call(t, WorkloadsScheduleShow, r)
	contains(t, out, "  join windows up to 15 min apart (default)\n")
	g.s.ScheduleJoin = nil
	if _, out = g.call(t, WorkloadsScheduleShow, r); strings.Contains(out, "join") {
		t.Fatalf("%s", out)
	}
}

func TestWorkloadsScheduleSetAJoinOnARouterBefore036SaysItNeeds036(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.ScheduleProblem = "joinMinutes: unknown field"
	r := workloadsEurope
	r.ID, r.File = "shop", g.scheduleFile(t, `{"timeZone":"Europe/Paris","maxNapDays":7,"joinMinutes":30}`)
	code, out := g.call(t, WorkloadsScheduleSet, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "joinMinutes: unknown field\n", "Recurrences and joined windows need a router of version 0.36.0 or later.")
}

func TestWorkloadsScheduleSetRefusesAMissingOrUnreadableFileBeforeAnyCall(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	mark := len(g.s.Calls())
	for name, file := range map[string]string{"none": "", "absent": filepath.Join(t.TempDir(), "nope.json"), "not JSON": g.scheduleFile(t, "windows: []"),
		"not an object": g.scheduleFile(t, "[1]")} {
		r := workloadsEurope
		r.ID, r.File = "shop", file
		if code, out := g.call(t, WorkloadsScheduleSet, r); code != 2 || out == "" {
			t.Errorf("%s: exit %d:\n%s", name, code, out)
		}
	}
	// Before the permission calls too: nothing at all is asked of Google.
	if got := g.s.Calls()[mark:]; len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestWorkloadsPauseAndResumePostToTheController(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "ASLEEP"
	r := workloadsEurope
	r.ID, r.Reason = "shop", "release freeze"
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsPause, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	body := g.body(t, "POST", controllerHost, "/v1/workloads/shop/pause")
	if body["reason"] != "release freeze" || body["by"] != "me@example.com" {
		t.Fatalf("%v", body)
	}
	// Not asked, keepAsleep and until are not sent: the controller refuses a null.
	if _, ok := body["keepAsleep"]; ok {
		t.Fatalf("%v", body)
	}
	if _, ok := body["until"]; ok {
		t.Fatalf("%v", body)
	}
	contains(t, out, "paused", "release freeze", "asleep", "woken")
	code, out = g.call(t, WorkloadsResume, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "resumed")
	if body := g.body(t, "POST", controllerHost, "/v1/workloads/shop/resume"); body["by"] != "me@example.com" {
		t.Fatalf("%v", body)
	}
	want := []string{"POST /v1/workloads/shop/pause", "POST /v1/workloads/shop/resume"}
	if got := g.controllerCalls(mark); !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestWorkloadsPauseKeepAsleepAndUntilSayWhatTheyDo(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "ASLEEP"
	r := workloadsEurope
	r.ID, r.Reason, r.KeepAsleep, r.Until = "shop", "audit", true, "2026-12-01T08:00:00+01:00"
	code, out := g.call(t, WorkloadsPause, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	body := g.body(t, "POST", controllerHost, "/v1/workloads/shop/pause")
	if body["keepAsleep"] != true || body["until"] != "2026-12-01T07:00:00Z" {
		t.Fatalf("%v", body)
	}
	contains(t, out, "a visitor still wakes the workload", "scheduled and maintenance wakes wait")
	if strings.Contains(out, "woken") {
		t.Fatalf("%s", out)
	}
}

func TestWorkloadsPauseRefusesAMissingReasonAndABadUntilBeforeAnyCall(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	mark := len(g.s.Calls())
	for _, r := range []Request{{ID: "shop"}, {ID: "shop", Reason: "  "}, {ID: "shop", Reason: strings.Repeat("x", 257)},
		{ID: "shop", Reason: "x", Until: "tomorrow"}, {ID: "shop", Reason: "x", Until: "2026-12-01"}} {
		r.Project, r.Region = workloadsEurope.Project, workloadsEurope.Region
		if code, out := g.call(t, WorkloadsPause, r); code != 2 || out == "" {
			t.Errorf("%+v: exit %d:\n%s", r, code, out)
		}
	}
	if got := g.controllerCalls(mark); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestWorkloadsNapOfAPausedWorkloadSaysItIsPaused(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	r := workloadsEurope
	r.ID, r.Reason = "shop", "freeze"
	g.call(t, WorkloadsPause, r)
	code, out := g.call(t, WorkloadsNap, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "workload shop is paused by me@example.com: freeze\n")
}

func TestWorkloadsShowAndListShowAPause(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"], g.s.Workloads["blog"] = "ASLEEP", "AWAKE"
	r := workloadsEurope
	r.ID, r.Reason, r.KeepAsleep, r.Until = "shop", "audit", true, "2027-01-01T00:00:00Z"
	g.call(t, WorkloadsPause, r)
	_, out := g.call(t, WorkloadsShow, r)
	contains(t, out, "paused by me@example.com", "audit", "2027-01-01T00:00:00Z", "keep asleep")
	_, out = g.call(t, WorkloadsList, workloadsEurope)
	var shop, blog string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "shop"):
			shop = line
		case strings.HasPrefix(line, "blog"):
			blog = line
		}
	}
	if !strings.Contains(shop, "paused") || strings.Contains(blog, "paused") || strings.Contains(shop, "audit") {
		t.Fatalf("shop %q blog %q", shop, blog)
	}
}

// An offboarding wakes inside its request: its answer is not lost to the CLI's own timeout.
func TestWorkloadsACallOfTheControllerOutlastsItsRequest(t *testing.T) {
	x := newRun(context.Background(), Deps{}, workloadsEurope)
	request := time.Duration(x.controllerSettings().timeoutSeconds) * time.Second
	if gcp.InvokeTimeout <= request {
		t.Fatalf("InvokeTimeout %s, the controller's request %s", gcp.InvokeTimeout, request)
	}
}

// A controller below 0.35.0 has no route for these: that is what is said, not its "not found".
func TestWorkloadsScheduleAndPauseOnAnOlderRouterSayItNeeds035(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.ControllerBelow035 = true
	r := workloadsEurope
	r.ID, r.Reason, r.File = "shop", "freeze", g.scheduleFile(t, parisSchedule)
	for name, f := range map[string]func(context.Context, Deps, Request) int{"show": WorkloadsScheduleShow, "set": WorkloadsScheduleSet,
		"clear": WorkloadsScheduleClear, "pause": WorkloadsPause, "resume": WorkloadsResume} {
		code, out := g.call(t, f, r)
		if code != 1 {
			t.Errorf("%s: exit %d:\n%s", name, code, out)
		}
		if !strings.Contains(out, "0.35.0") || !strings.Contains(out, "--image") || strings.Contains(out, "no route") {
			t.Errorf("%s:\n%s", name, out)
		}
	}
}

// A workload the router does not know stays that.
func TestWorkloadsScheduleOfAnUnknownWorkloadSaysSo(t *testing.T) {
	g := newWorkloadsRig(t)
	r := workloadsEurope
	r.ID = "nope"
	code, out := g.call(t, WorkloadsScheduleShow, r)
	if code != 1 || !strings.Contains(out, "no workload nope") || strings.Contains(out, "0.35.0") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// The answer's state is the one as the pause was asked: it must not read as a contradiction.
func TestWorkloadsPauseSaysWhatBecomesOfTheWorkloadByItsState(t *testing.T) {
	for state, want := range map[string]string{
		"ASLEEP":  "It was asleep: it is being woken, and then stays awake.\n",
		"FAILED":  "It had failed: it is being woken, and then stays awake.\n",
		"NAPPING": "It is napping: it wakes once its nap ends, and then stays awake.\n",
		"WAKING":  "It is waking, and then stays awake.\n",
		"AWAKE":   "It stays awake.\n",
	} {
		g := newWorkloadsRig(t)
		g.s.Workloads["shop"] = state
		r := workloadsEurope
		r.ID, r.Reason = "shop", "freeze"
		code, out := g.call(t, WorkloadsPause, r)
		if code != 0 {
			t.Fatalf("%s: exit %d:\n%s", state, code, out)
		}
		contains(t, out, want, "none of its naps runs until it is resumed.\n")
		if strings.Contains(out, "(now ") {
			t.Errorf("%s:\n%s", state, out)
		}
	}
}

func TestWorkloadsPauseWithUntilSaysTheTime(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	r := workloadsEurope
	r.ID, r.Reason, r.Until = "shop", "freeze", "2026-12-01T08:00:00+01:00"
	code, out := g.call(t, WorkloadsPause, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "none of its naps runs until 2026-12-01T07:00:00Z, unless it is resumed sooner.\n")
}

func TestWorkloadsPauseKeepAsleepNamesMaintenanceWakes(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "ASLEEP"
	r := workloadsEurope
	r.ID, r.Reason, r.KeepAsleep = "shop", "freeze", true
	code, out := g.call(t, WorkloadsPause, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "It stays as it is (asleep): a visitor still wakes the workload, and scheduled and maintenance wakes wait until the pause ends.\n")
}

// As the router: a paused workload's nap is refused only when it would act, on an AWAKE one.
func TestWorkloadsNapOfAPausedWorkloadAlreadyAsleepIsNoRefusal(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "ASLEEP"
	r := workloadsEurope
	r.ID, r.Reason, r.KeepAsleep = "shop", "freeze", true
	g.call(t, WorkloadsPause, r)
	if code, out := g.call(t, WorkloadsNap, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestWorkloadsScheduleShowSaysWhoSetItAndWhen(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	r := workloadsEurope
	r.ID, r.File = "shop", g.scheduleFile(t, parisSchedule)
	if code, out := g.call(t, WorkloadsScheduleSet, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	code, out := g.call(t, WorkloadsScheduleShow, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Schedule of workload shop (set by me@example.com, 2026-11-01T10:00:00.000Z):\n")
}

const (
	databaseParent = "//sqladmin.googleapis.com/projects/dormeto-dev/instances/shop-db"
	// The instance's region, not the router's: its tag bindings are at its own endpoint.
	databaseHost = "europe-west3-cloudresourcemanager.googleapis.com"
)

// newDatabaseRig is an installed router with the feature cloud-sql, and the instance shop-db in another region.
func newDatabaseRig(t *testing.T) *workloadsRig {
	t.Helper()
	g := newWorkloadsRig(t)
	g.plantFeatures(t, "cloud-sql")
	g.s.SQLInstances = map[string]string{"shop-db": "europe-west3"}
	return g
}

var shopWithDatabase = func() Request {
	r := shopOnboarding
	r.CloudSQL = []string{"shop-db"}
	return r
}()

// M8: Google stops a primary with read replicas and leaves them running: the onboarding goes on, saying so.
func TestWorkloadsOnboardWithADatabaseOfReadReplicasWarnsAndGoesOn(t *testing.T) {
	g := newDatabaseRig(t)
	g.s.SQLReplicas = map[string][]string{"shop-db": {"shop-db-r1", "shop-db-r2"}}
	code, out := g.call(t, WorkloadsOnboard, shopWithDatabase)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "! Cloud SQL instance shop-db has read replicas (shop-db-r1, shop-db-r2)", "Google stops only the primary", "keep running, and billing")
	if g.s.Workloads["shop"] != "AWAKE" {
		t.Fatal("not onboarded")
	}
	g2 := newDatabaseRig(t)
	if _, out := g2.call(t, WorkloadsOnboard, shopWithDatabase); strings.Contains(out, "read replicas") {
		t.Fatalf("no replica, no warning:\n%s", out)
	}
}

func TestWorkloadsOnboardBindsTheDatabasesTagOnItsParentBeforeTheOnboarding(t *testing.T) {
	g := newDatabaseRig(t)
	value := g.tagValue(t)
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, shopWithDatabase)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var order []string
	for _, c := range g.s.Calls()[mark:] {
		switch {
		case c.Host == controllerHost && c.Method == "POST":
			order = append(order, "POST "+strings.TrimPrefix(c.URL, "https://"+controllerHost))
		case c.Method == "POST" && strings.HasSuffix(c.Path, "/v3/tagBindings"):
			order = append(order, "POST "+c.Host)
		}
	}
	want := []string{"POST /v1/workloads?dryRun=true", "POST " + zoneHost, "POST " + regionHost, "POST " + databaseHost, "POST /v1/workloads"}
	if !slices.Equal(order, want) {
		t.Fatalf("order:\n%s", strings.Join(order, "\n"))
	}
	if !slices.Contains(g.bindings(), gcptest.TagBinding{Parent: databaseParent, TagValue: value}) {
		t.Fatalf("bindings %v", g.bindings())
	}
	if b := g.body(t, "POST", databaseHost, "/v3/tagBindings"); b["parent"] != databaseParent || b["tagValue"] != value {
		t.Fatalf("%v", b)
	}
	for _, in := range []map[string]any{g.body(t, "POST", controllerHost, "/v1/workloads?dryRun=true"), g.onboarding(t)} {
		if !jsonEqual(in["cloudSql"], []string{"shop-db"}) {
			t.Fatalf("%v", in)
		}
	}
	contains(t, out, "backend: Cloud SQL instance shop-db", "tagged: Cloud SQL instance shop-db")
}

func TestWorkloadsOnboardDryRunNamesTheDatabaseAndBindsNothing(t *testing.T) {
	g := newDatabaseRig(t)
	r := shopWithDatabase
	r.DryRun = true
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Cloud SQL instance shop-db")
	if len(g.bindings()) != 0 || len(g.s.Workloads) != 0 {
		t.Fatal("something was changed")
	}
}

func TestWorkloadsOnboardNeedsTheFeaturesOfItsFlagsInTheRecord(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*Request)
		has    []string
		want   string
	}{
		"cloud sql":        {func(r *Request) { r.CloudSQL = []string{"shop-db"} }, nil, "--with cloud-sql "},
		"gke":              {func(r *Request) { r.GKECluster = "europe-west1-b/shop-cluster" }, nil, "--with gke "},
		"both":             {func(r *Request) { r.CloudSQL, r.GKECluster = []string{"shop-db"}, "europe-west1-b/shop-cluster" }, nil, "--with cloud-sql,gke "},
		"gke, has the sql": {func(r *Request) { r.CloudSQL, r.GKECluster = []string{"shop-db"}, "europe-west1-b/shop-cluster" }, []string{"cloud-sql"}, "--with gke "},
	} {
		g := newWorkloadsRig(t)
		g.plantFeatures(t, c.has...)
		g.s.SQLInstances = map[string]string{"shop-db": "europe-west3"}
		r := shopOnboarding
		r.Configuration, r.Yes = "c", true
		c.change(&r)
		mark := len(g.s.Calls())
		code, out := g.call(t, WorkloadsOnboard, r)
		if code != 1 {
			t.Errorf("%s: exit %d:\n%s", name, code, out)
			continue
		}
		contains(t, out, "dormeto gcp routers install --project dormeto-dev --region europe-west1 "+c.want+"--gcloud-configuration c", "Nothing was changed.")
		if m := g.changes(mark); len(m) != 0 {
			t.Errorf("%s: changes %v", name, m)
		}
	}
}

func TestWorkloadsOnboardAsksTheFeaturesPermissionsOfItsFlagsAlone(t *testing.T) {
	missing := "cloudsql.instances.createTagBinding"
	for _, withFlag := range []bool{true, false} {
		g := newDatabaseRig(t)
		g.s.HoldsAll = false
		g.s.Held = map[string]bool{}
		for _, p := range access.NeedsWith("onboard", []string{"cloud-sql", "gke"}) {
			g.s.Held[p] = p != missing
		}
		r := shopOnboarding
		if withFlag {
			r.CloudSQL = []string{"shop-db"}
		}
		mark := len(g.s.Calls())
		code, out := g.call(t, WorkloadsOnboard, r)
		if !withFlag {
			if code != 0 {
				t.Fatalf("no flag: exit %d:\n%s", code, out)
			}
			continue
		}
		if code != 1 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		contains(t, out, "missing "+missing, "Nothing was changed.")
		if m := g.changes(mark); len(m) != 0 || len(g.controllerCalls(mark)) != 0 {
			t.Fatalf("changes %v", m)
		}
	}
}

func TestWorkloadsOnboardRefusedRemovesTheDatabasesTagItBoundAndNoOther(t *testing.T) {
	g := newDatabaseRig(t)
	g.s.OnboardingRefused = refusedOnboarding
	value := g.tagValue(t)
	before := gcptest.TagBinding{Parent: vmParent, TagValue: value}
	g.s.TagBindings = []gcptest.TagBinding{before}
	code, out := g.call(t, WorkloadsOnboard, shopWithDatabase)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, refusedOnboarding, "removed", "Cloud SQL instance shop-db")
	if got := g.bindings(); !slices.Equal(got, []gcptest.TagBinding{before}) {
		t.Fatalf("bindings %v", got)
	}
}

func TestWorkloadsOnboardRefusedKeepsADatabasesTagBoundBeforeThisRun(t *testing.T) {
	g := newDatabaseRig(t)
	g.s.OnboardingRefused = refusedOnboarding
	before := gcptest.TagBinding{Parent: databaseParent, TagValue: g.tagValue(t)}
	g.s.TagBindings = []gcptest.TagBinding{before}
	if code, out := g.call(t, WorkloadsOnboard, shopWithDatabase); code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.bindings(); !slices.Equal(got, []gcptest.TagBinding{before}) {
		t.Fatalf("bindings %v", got)
	}
}

func TestWorkloadsOnboardRefusedSaysWhichDatabaseItCouldNotUntag(t *testing.T) {
	g := newDatabaseRig(t)
	g.s.OnboardingRefused = refusedOnboarding
	g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: databaseHost, Path: "/v3/tagBindings", Times: 1, Code: 500, Status: "INTERNAL", Message: "boom"})
	code, out := g.call(t, WorkloadsOnboard, shopWithDatabase)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, refusedOnboarding, "not removed", "Cloud SQL instance shop-db", "boom")
}

func TestWorkloadsOnboardChangesNothingWhenTheDatabasesRegionIsNotRead(t *testing.T) {
	g := newDatabaseRig(t)
	g.s.Refuse(gcptest.Refusal{Method: "GET", Host: "sqladmin.googleapis.com", Path: "/v1/projects/", Code: 403, Status: "PERMISSION_DENIED", Message: "not allowed"})
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, shopWithDatabase)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Cloud SQL instance shop-db", "cloudsql.instances.get", "not allowed", "Nothing was changed.")
	if len(g.bindings()) != 0 || len(g.s.Workloads) != 0 {
		t.Fatalf("changes %v", g.changes(mark))
	}
}

func TestWorkloadsOnboardStopsWhenTheDryRunNamesNotADatabase(t *testing.T) {
	g := newDatabaseRig(t)
	g.s.ControllerBelow035 = true
	code, out := g.call(t, WorkloadsOnboard, shopWithDatabase)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "--cloud-sql shop-db", "0.35.0", "Nothing was changed.")
	if len(g.bindings()) != 0 || len(g.s.Workloads) != 0 {
		t.Fatal("something was changed")
	}
}

const (
	servicePath = "/api/v1/namespaces/shop/services/web"
	podsPath    = "/api/v1/namespaces/shop/pods"
	setsPath    = "/apis/apps/v1/namespaces/shop/replicasets"
	shopCluster = "europe-west1-b/shop-cluster"
)

var shopOnGKE = func() Request {
	r := shopOnboarding
	r.GKECluster, r.Yes = shopCluster, true
	return r
}()

// orderRecorder notes every call, Google's and the cluster's, in the order they are sent.
type orderRecorder struct {
	next http.RoundTripper
	mu   sync.Mutex
	got  []string
}

func (o *orderRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	o.mu.Lock()
	o.got = append(o.got, req.Method+" "+req.URL.Host+req.URL.RequestURI())
	o.mu.Unlock()
	return o.next.RoundTrip(req)
}

func (o *orderRecorder) calls() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.got)
}

// newGKERig is an installed router with the feature gke, a backend service whose NEGs name Service web of namespace shop on
// an Autopilot cluster, and that Service's Deployment web in the cluster. It tags nothing.
func newGKERig(t *testing.T) (*rbacRig, *orderRecorder) {
	t.Helper()
	g := newRbacRig(t)
	g.plantFeatures(t, "gke")
	g.s.DryRun = gcptest.DryRun{GKE: &gcptest.DryRunGKE{Namespace: "shop", Service: "web", Autopilot: true}}
	g.k.reads = map[string]any{
		servicePath: map[string]any{"metadata": map[string]any{"name": "web"}, "spec": map[string]any{"selector": map[string]any{"app": "web"}}},
		podsPath: map[string]any{"items": []any{map[string]any{"metadata": map[string]any{"name": "web-1", "namespace": "shop",
			"ownerReferences": []any{map[string]any{"kind": "ReplicaSet", "name": "web-abc", "controller": true}}}, "status": map[string]any{"phase": "Running"}}}},
		setsPath: map[string]any{"items": []any{map[string]any{"metadata": map[string]any{"name": "web-abc",
			"ownerReferences": []any{map[string]any{"kind": "Deployment", "name": "web", "controller": true}}}}}},
	}
	http2 := *g.d.Google.HTTP
	order := &orderRecorder{next: http2.Transport}
	http2.Transport = order
	g.d.Google.HTTP = &http2
	return g, order
}

// The order of spec §8: the dry run, the reading of the cluster, the RBAC printed, asked and applied, the onboarding.
func TestWorkloadsOnboardGKEFollowsTheOrderOfTheDesign(t *testing.T) {
	g, order := newGKERig(t)
	g.d.Terminal = true
	g.d.Prompter = install.NewPrompter(strings.NewReader("y\n"), g.out, true)
	r := shopOnGKE
	r.Yes = false
	var atFirstWrite string
	listedFirst := false
	g.k.before = func(string) {
		if atFirstWrite == "" {
			atFirstWrite = g.out.String()
			listedFirst = slices.Contains(g.rbacOf(t), shopEntry)
		}
	}
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var got []string
	for _, c := range order.calls() {
		switch method, rest, _ := strings.Cut(c, " "); {
		case method == "POST" && strings.HasPrefix(rest, controllerHost):
			got = append(got, "controller "+strings.TrimPrefix(rest, controllerHost))
		case strings.HasPrefix(rest, kubeHost) && !strings.Contains(rest, "/rbac.authorization.k8s.io/"):
			got = append(got, "read "+strings.TrimPrefix(rest, kubeHost))
		case strings.HasPrefix(rest, kubeHost) && method != "GET":
			got = append(got, method+" "+strings.TrimPrefix(rest, kubeHost))
		}
	}
	want := []string{"controller /v1/workloads?dryRun=true", "read " + servicePath, "read " + podsPath + "?labelSelector=app%3Dweb", "read " + setsPath,
		createRole, createBinding, "controller /v1/workloads"}
	if !slices.Equal(got, want) {
		t.Fatalf("order:\n%s", strings.Join(got, "\n"))
	}
	asked := strings.Index(atFirstWrite, "[y/N]")
	if asked < 0 {
		t.Fatalf("written before asking:\n%s", atFirstWrite)
	}
	for _, line := range []string{"+ Role dormeto-shop", "+ RoleBinding dormeto-shop", "User " + routerSA} {
		if i := strings.Index(atFirstWrite, line); i < 0 || i > asked {
			t.Errorf("%q not printed before asking:\n%s", line, atFirstWrite)
		}
	}
	if !listedFirst {
		t.Error("the record did not list the access before it was applied")
	}
	if !g.k.has(shopRole) || !g.k.has(shopBinding) || g.s.Workloads["shop"] != "AWAKE" {
		t.Fatalf("role %v binding %v workloads %v", g.k.has(shopRole), g.k.has(shopBinding), g.s.Workloads)
	}
	contains(t, out, "Onboarded workload shop", "Deployment web", shopCluster)
}

func TestWorkloadsOnboardGKESendsTheClusterThenTheOwner(t *testing.T) {
	g, _ := newGKERig(t)
	if code, out := g.call(t, WorkloadsOnboard, shopOnGKE); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	cluster := map[string]any{"location": "europe-west1-b", "name": "shop-cluster"}
	if got := g.body(t, "POST", controllerHost, "/v1/workloads?dryRun=true")["gke"]; !jsonEqual(got, map[string]any{"cluster": cluster}) {
		t.Errorf("the dry run's: %v", got)
	}
	want := map[string]any{"cluster": cluster, "namespace": "shop", "owner": map[string]any{"kind": "Deployment", "name": "web"}, "service": "web"}
	if got := g.onboarding(t)["gke"]; !jsonEqual(got, want) {
		t.Errorf("the onboarding's: %v", got)
	}
}

// Review focus 5: an onboarding refused after the CLI changed a cluster.
func TestWorkloadsOnboardGKERefusedAtTheOnboardingRemovesTheRbacItAdded(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.OnboardingRefused = refusedOnboarding
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, refusedOnboarding, "removed", "RoleBinding dormeto-shop", "Role dormeto-shop", "fix the refusal")
	if w := g.k.writes(0); !slices.Equal(w, []string{createRole, createBinding, "DELETE " + shopBinding, "DELETE " + shopRole}) {
		t.Fatalf("writes %v", w)
	}
	if g.k.has(shopRole) || g.k.has(shopBinding) {
		t.Fatal("the RBAC stays")
	}
	if got := g.rbacOf(t); len(got) != 0 {
		t.Fatalf("the record still lists %v", got)
	}
}

// iamOfKubeCall is the IAM permission of a call of the cluster's API on GKE, a resource by its verb: a GET of a collection is a list.
func iamOfKubeCall(t *testing.T, call string) string {
	t.Helper()
	method, path, _ := strings.Cut(call, " ")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	at := slices.Index(parts, "namespaces")
	if at < 0 || len(parts) < at+3 {
		t.Fatalf("a call out of a namespace: %s", call)
	}
	resources := map[string]string{"services": "services", "pods": "pods", "replicasets": "replicaSets", "roles": "roles", "rolebindings": "roleBindings"}
	resource, known := resources[parts[at+2]]
	verb, verbKnown := map[string]string{"GET": "get", "POST": "create", "PUT": "update", "DELETE": "delete"}[method]
	if !known || !verbKnown {
		t.Fatalf("a call with no permission known here: %s", call)
	}
	if method == http.MethodGet && len(parts) == at+3 {
		verb = "list"
	}
	return "container." + resource + "." + verb
}

// The router's manifest (a sibling checkout, as the parity test) asks of a GKE onboarding exactly what its calls take, undo included:
// each call of the cluster's API by running them, and what no response shows: clusters.get and clusters.connect, which the DNS endpoint
// asks of each call, escalate and bind, with which Kubernetes lets the Role carry rights the operator does not hold, and so reads none
// of the Role's deployments or statefulsets.
func TestWorkloadsOnboardGKERoutersManifestAsksExactlyWhatItsCallsTake(t *testing.T) {
	data, err := os.ReadFile("../../../router/gcp/access.json")
	if err != nil {
		t.Skipf("the router repository is absent: %v", err)
	}
	var manifest struct {
		Features map[string]struct {
			Operator map[string]map[string][]string `json:"operator"`
		} `json:"features"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	var asked []string
	for _, step := range manifest.Features["gke"].Operator["onboard"] {
		for _, p := range step {
			if strings.HasPrefix(p, "container.") {
				asked = append(asked, p)
			}
		}
	}
	slices.Sort(asked)

	called := []string{"container.clusters.connect", "container.clusters.get", "container.roles.bind", "container.roles.escalate"}
	// Refused by the controller: the objects created, then undone.
	refusedRun, _ := newGKERig(t)
	refusedRun.s.OnboardingRefused = refusedOnboarding
	if code, out := refusedRun.call(t, WorkloadsOnboard, shopOnGKE); code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// With Dormeto's objects there and different: updated.
	updatedRun, _ := newGKERig(t)
	updatedRun.k.put(t, shopRole, "Role", ourLabels, map[string]any{"rules": []any{}})
	updatedRun.k.put(t, shopBinding, "RoleBinding", ourLabels, map[string]any{
		"subjects": []any{map[string]any{"kind": "User", "name": "someone@example.com", "apiGroup": rbacGroup}},
		"roleRef":  map[string]any{"apiGroup": rbacGroup, "kind": "Role", "name": "dormeto-shop"},
	})
	if code, out := updatedRun.call(t, WorkloadsOnboard, shopOnGKE); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, c := range append(refusedRun.k.callList(), updatedRun.k.callList()...) {
		called = append(called, iamOfKubeCall(t, c))
	}
	slices.Sort(called)
	called = slices.Compact(called)
	if !slices.Equal(asked, called) {
		t.Fatalf("the manifest asks:\n  %s\nthe calls take:\n  %s", strings.Join(asked, "\n  "), strings.Join(called, "\n  "))
	}
}

func (g *rbacRig) grantedBefore(t *testing.T) {
	t.Helper()
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestWorkloadsOnboardGKERefusedLeavesTheRbacThatWasThereBefore(t *testing.T) {
	g, _ := newGKERig(t)
	g.grantedBefore(t)
	mark := len(g.k.writes(0))
	g.s.OnboardingRefused = refusedOnboarding
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if w := g.k.writes(0)[mark:]; len(w) != 0 {
		t.Fatalf("writes %v", w)
	}
	if !g.k.has(shopRole) || !g.k.has(shopBinding) || !slices.Contains(g.rbacOf(t), shopEntry) {
		t.Fatal("what was there is gone")
	}
	contains(t, out, refusedOnboarding, "there before this run", "stay")
}

func TestWorkloadsOnboardGKERefusedRemovesTheRoleBindingItAddedAndLeavesTheRole(t *testing.T) {
	g, _ := newGKERig(t)
	g.grantedBefore(t)
	g.k.mu.Lock()
	delete(g.k.objects, shopBinding)
	g.k.mu.Unlock()
	g.s.OnboardingRefused = refusedOnboarding
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !g.k.has(shopRole) || g.k.has(shopBinding) {
		t.Fatalf("role %v binding %v", g.k.has(shopRole), g.k.has(shopBinding))
	}
	if !slices.Contains(g.rbacOf(t), shopEntry) {
		t.Fatal("the Role stays: the record must list it")
	}
	contains(t, out, "removed", "RoleBinding dormeto-shop", "Role dormeto-shop", "there before this run")
}

func TestWorkloadsOnboardGKERefusedTakesOutTheSubjectItAddedToABindingThatWasThere(t *testing.T) {
	g, _ := newGKERig(t)
	g.grantedBefore(t)
	someone := map[string]any{"kind": "User", "name": "someone@example.com", "apiGroup": "rbac.authorization.k8s.io"}
	g.k.put(t, shopBinding, "RoleBinding", ourLabels, map[string]any{"subjects": []any{someone},
		"roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "dormeto-shop"}})
	g.s.OnboardingRefused = refusedOnboarding
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.k.object(shopBinding)["subjects"]; !reflect.DeepEqual(got, []any{someone}) {
		t.Fatalf("subjects %v", got)
	}
	if !g.k.has(shopRole) {
		t.Fatal("the Role was there")
	}
	contains(t, out, "removed", routerSA)
}

func TestWorkloadsOnboardGKERefusedSaysWhatItCouldNotRemove(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.OnboardingRefused = refusedOnboarding
	g.k.deny = func(call string) *kubeStatus {
		if call == "DELETE "+shopBinding {
			return &kubeStatus{403, "Forbidden", "rolebindings is forbidden"}
		}
		return nil
	}
	r := shopOnGKE
	r.Configuration = "c"
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, refusedOnboarding, "not removed", "RoleBinding dormeto-shop", "rolebindings is forbidden",
		"dormeto gcp workloads access revoke --project dormeto-dev --region europe-west1 --gke-cluster "+shopCluster+" --namespace shop --workload shop --gcloud-configuration c")
	if !slices.Contains(g.rbacOf(t), shopEntry) {
		t.Fatal("what is left must stay listed")
	}
	if !g.k.has(shopBinding) {
		t.Fatal("the binding went")
	}
}

func TestWorkloadsOnboardGKESaysWhatIsLeftWhenTheClusterCannotBeReadAgain(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.OnboardingRefused = refusedOnboarding
	g.k.before = func(call string) {
		if call == createBinding {
			g.denied = true
		}
	}
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, refusedOnboarding, "not removed", "access revoke", "container.clusters.get")
	if !slices.Contains(g.rbacOf(t), shopEntry) {
		t.Fatal("what is left must stay listed")
	}
}

func TestWorkloadsOnboardGKEWithALostAnswerKeepsTheRbacAndSaysSo(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.AnswerOnboarding = gcptest.Cut
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "the workload may be onboarded", "The Kubernetes access stays", "workloads show shop", "access revoke")
	if strings.Contains(out, "The tag bindings stay") {
		t.Fatalf("no tag was bound:\n%s", out)
	}
	if !g.k.has(shopRole) || !g.k.has(shopBinding) || !slices.Contains(g.rbacOf(t), shopEntry) {
		t.Fatal("the RBAC went")
	}
}

func TestWorkloadsOnboardGKEOnlyBindsNoTagAndNeedsNone(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.Lock()
	g.s.TagValues = map[string]*gcptest.Tag{}
	g.s.Unlock()
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if len(g.bindings()) != 0 {
		t.Fatalf("bindings %v", g.bindings())
	}
	for _, c := range g.s.Calls()[mark:] {
		if strings.Contains(c.Path, "/tagBindings") || strings.Contains(c.Path, "/tagValues") {
			t.Errorf("%s %s%s", c.Method, c.Host, c.Path)
		}
	}
	if strings.Contains(out, "tagged:") {
		t.Fatalf("%s", out)
	}
}

func TestWorkloadsOnboardGKEWithADatabaseBindsThatTagAndARefusalUndoesBoth(t *testing.T) {
	g, _ := newGKERig(t)
	g.plantFeatures(t, "cloud-sql", "gke")
	g.s.SQLInstances = map[string]string{"shop-db": "europe-west3"}
	r := shopOnGKE
	r.CloudSQL = []string{"shop-db"}
	g.s.OnboardingRefused = refusedOnboarding
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if len(g.bindings()) != 0 || g.k.has(shopRole) || g.k.has(shopBinding) {
		t.Fatalf("bindings %v role %v binding %v:\n%s", g.bindings(), g.k.has(shopRole), g.k.has(shopBinding), out)
	}
	g.s.OnboardingRefused = ""
	if code, out = g.call(t, WorkloadsOnboard, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.bindings(); len(got) != 1 || got[0].Parent != databaseParent {
		t.Fatalf("bindings %v", got)
	}
}

func TestWorkloadsOnboardGKEWhenATagIsRefusedRemovesTheRbacItAdded(t *testing.T) {
	g, _ := newGKERig(t)
	g.plantFeatures(t, "cloud-sql", "gke")
	g.s.SQLInstances = map[string]string{"shop-db": "europe-west3"}
	g.s.Refuse(gcptest.Refusal{Method: "POST", Host: databaseHost, Path: "/v3/tagBindings", Times: 1, Code: 403, Status: "PERMISSION_DENIED", Message: "no tag for you"})
	r := shopOnGKE
	r.CloudSQL = []string{"shop-db"}
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "the tag is not bound to Cloud SQL instance shop-db", "no tag for you", "removed")
	if g.k.has(shopRole) || g.k.has(shopBinding) || len(g.s.Workloads) != 0 {
		t.Fatalf("role %v binding %v workloads %v", g.k.has(shopRole), g.k.has(shopBinding), g.s.Workloads)
	}
}

func TestWorkloadsOnboardOnAStandardClusterWarnsAndGoesOn(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.DryRun.GKE.Autopilot = false
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "! Cluster "+shopCluster+" is a Standard cluster", "saves money only on Autopilot", "the node autoscaler freeing a node")
	if g.s.Workloads["shop"] != "AWAKE" {
		t.Fatal("not onboarded")
	}
	// The warning before the RBAC is asked for, and with --dry-run too.
	r := shopOnGKE
	r.DryRun, r.ID = true, "other"
	if code, out = g.call(t, WorkloadsOnboard, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "saves money only on Autopilot")
}

func TestWorkloadsOnboardOnAutopilotDoesNotWarn(t *testing.T) {
	g, _ := newGKERig(t)
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 0 || strings.Contains(out, "saves money") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestWorkloadsOnboardGKEWithJSONKeepsItsWordsOutOfTheAnswer(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.DryRun.GKE.Autopilot = false
	var errs bytes.Buffer
	g.d.Err = &errs
	r := shopOnGKE
	r.JSON = true
	code, out := g.call(t, WorkloadsOnboard, r)
	var w map[string]any
	if err := json.Unmarshal([]byte(out), &w); code != 0 || err != nil || w["id"] != "shop" {
		t.Fatalf("%d %v:\n%s", code, err, out)
	}
	contains(t, errs.String(), "saves money only on Autopilot", "created Role dormeto-shop", "created RoleBinding dormeto-shop", "namespace shop", shopCluster)
}

func TestWorkloadsOnboardGKEDeclinedChangesNothing(t *testing.T) {
	g, _ := newGKERig(t)
	g.d.Terminal = true
	g.d.Prompter = install.NewPrompter(strings.NewReader("n\n"), g.out, true)
	r := shopOnGKE
	r.Yes = false
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Nothing was changed.")
	if w := g.k.writes(0); len(w) != 0 || len(g.s.Workloads) != 0 {
		t.Fatalf("writes %v workloads %v", w, g.s.Workloads)
	}
	if m := g.changes(mark); len(m) != 1 {
		t.Fatalf("changes %v", m)
	}
}

func TestWorkloadsOnboardGKEWithoutATerminalNeedsYes(t *testing.T) {
	g, _ := newGKERig(t)
	r := shopOnGKE
	r.Yes = false
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 2 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "--yes", "+ Role dormeto-shop")
	if w := g.k.writes(0); len(w) != 0 || len(g.s.Workloads) != 0 {
		t.Fatalf("writes %v workloads %v", w, g.s.Workloads)
	}
}

// An onboarded workload, run again with its flags: the dry run says so, as the router's does.
func TestWorkloadsOnboardGKEOfAWorkloadOnboardedStopsAtTheDryRun(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.Workloads["shop"] = "AWAKE"
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "workload shop is onboarded already", "Nothing was changed.")
	if len(g.k.callList()) != 0 {
		t.Fatalf("the cluster was called: %v", g.k.callList())
	}
}

func TestWorkloadsOnboardGKERefusesASomeoneElsesRoleBeforeAnyChange(t *testing.T) {
	g, _ := newGKERig(t)
	g.k.put(t, shopRole, "Role", map[string]string{"team": "shop"}, map[string]any{"rules": []any{}})
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "is not Dormeto's", "Nothing was changed.")
	if w := g.k.writes(0); len(w) != 0 || len(g.s.Workloads) != 0 {
		t.Fatalf("writes %v workloads %v", w, g.s.Workloads)
	}
}

func TestWorkloadsOnboardGKESaysWhyTheOwnerCannotBeRead(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*rbacRig)
		want   []string
	}{
		"no service":   {func(g *rbacRig) { delete(g.k.reads, servicePath) }, []string{"Service web is not in namespace shop"}},
		"no pod":       {func(g *rbacRig) { g.k.reads[podsPath] = map[string]any{"items": []any{}} }, []string{"no pod behind Service web"}},
		"denied":       {func(g *rbacRig) { g.k.deny = denyCall("GET "+podsPath, 403, "Forbidden", `pods is forbidden`) }, []string{"container.pods.list", "pods is forbidden"}},
		"the endpoint": {func(g *rbacRig) { g.k.deny = denyCall("GET "+servicePath, 403, "", "IAM says no") }, []string{"container.clusters.connect", "IAM says no"}},
	} {
		g, _ := newGKERig(t)
		c.change(g)
		code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
		if code != 1 {
			t.Errorf("%s: exit %d:\n%s", name, code, out)
			continue
		}
		contains(t, out, append(c.want, "Nothing was changed.")...)
		if w := g.k.writes(0); len(w) != 0 || len(g.s.Workloads) != 0 {
			t.Errorf("%s: writes %v workloads %v", name, w, g.s.Workloads)
		}
	}
}

func denyCall(call string, code int, reason, message string) func(string) *kubeStatus {
	return func(c string) *kubeStatus {
		if c == call {
			return &kubeStatus{code, reason, message}
		}
		return nil
	}
}

func TestWorkloadsOnboardGKEStopsWhenTheDryRunSaysNothingOfTheCluster(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.DryRun.GKE = nil // as the router before its GKE
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "--gke-cluster "+shopCluster, "Nothing was changed.")
	if len(g.k.callList()) != 0 || len(g.s.Workloads) != 0 {
		t.Fatalf("calls %v workloads %v", g.k.callList(), g.s.Workloads)
	}
}

func TestWorkloadsOnboardGKEDryRunReadsNoClusterAndNamesWhatItWouldApply(t *testing.T) {
	g, _ := newGKERig(t)
	r := shopOnGKE
	r.DryRun, r.Yes = true, false
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "eligible", "Role and RoleBinding dormeto-shop", "namespace shop", shopCluster, "dormeto gcp workloads access show")
	if strings.Contains(out, "bind the tag") {
		t.Fatalf("no tag to bind:\n%s", out)
	}
	if len(g.k.callList()) != 0 || len(g.s.Workloads) != 0 {
		t.Fatalf("calls %v workloads %v", g.k.callList(), g.s.Workloads)
	}
}

func TestWorkloadsOnboardAsksTheGKEPermissionsWithItsFlag(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.HoldsAll = false
	g.s.Held = map[string]bool{}
	missing := "container.roles.escalate"
	for _, p := range access.NeedsWith("onboard", []string{"gke"}) {
		g.s.Held[p] = p != missing
	}
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "missing "+missing, "Nothing was changed.")
	if m := g.changes(mark); len(m) != 0 || len(g.k.callList()) != 0 {
		t.Fatalf("changes %v %v", m, g.k.callList())
	}
}

// onboardedDatabase is workload shop with a VM and a database, both tagged.
func (g *workloadsRig) onboardedDatabase(t *testing.T) {
	t.Helper()
	value := g.tagValue(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{
		map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"},
		map[string]any{"kind": "cloudsql", "name": "shop-db"}}}
	g.s.TagBindings = []gcptest.TagBinding{{Parent: vmParent, TagValue: value}, {Parent: databaseParent, TagValue: value}}
}

var shopOffboard = func() Request {
	r := workloadsEurope
	r.ID = "shop"
	return r
}()

func TestWorkloadsOffboardRemovesTheTagOfADatabase(t *testing.T) {
	g := newDatabaseRig(t)
	g.onboardedDatabase(t)
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOffboard, shopOffboard)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if len(g.bindings()) != 0 || len(g.s.Workloads) != 0 {
		t.Fatalf("bindings %v workloads %v", g.bindings(), g.s.Workloads)
	}
	changes := g.changes(mark)
	if len(changes) != 3 || changes[0] != "DELETE "+controllerHost+"/v1/workloads/shop" || !strings.HasPrefix(changes[2], "DELETE "+databaseHost+"/v3/tagBindings/") {
		t.Fatalf("changes:\n%s", strings.Join(changes, "\n"))
	}
	contains(t, out, "Offboarded workload shop", "untagged: vm web-1", "untagged: Cloud SQL instance shop-db")
}

func TestWorkloadsOffboardGoesOnForADatabaseThatIsGone(t *testing.T) {
	g := newDatabaseRig(t)
	g.onboardedDatabase(t)
	g.s.SQLInstances = map[string]string{}
	code, out := g.call(t, WorkloadsOffboard, shopOffboard)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(g.offboardFile()); !os.IsNotExist(err) {
		t.Fatalf("the file stays: %v", err)
	}
}

func TestWorkloadsOffboardAsksTheDatabasesPermissionsOfAWorkloadThatHasOne(t *testing.T) {
	missing := "cloudsql.instances.deleteTagBinding"
	for _, has := range []bool{true, false} {
		g := newDatabaseRig(t)
		g.onboardedDatabase(t)
		if !has {
			g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"}}}
		}
		g.s.HoldsAll = false
		g.s.Held = map[string]bool{}
		for _, p := range access.NeedsWith("offboard", []string{"cloud-sql", "gke"}) {
			g.s.Held[p] = p != missing
		}
		code, out := g.call(t, WorkloadsOffboard, shopOffboard)
		if !has {
			if code != 0 {
				t.Fatalf("no database: exit %d:\n%s", code, out)
			}
			continue
		}
		if code != 1 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		contains(t, out, "missing "+missing, "Nothing was changed.")
		if g.s.Workloads["shop"] != "AWAKE" || len(g.bindings()) != 2 {
			t.Fatalf("workloads %v bindings %v", g.s.Workloads, g.bindings())
		}
	}
}

func TestWorkloadsOffboardSaysADatabasesTagIsLeftThenARerunFinishes(t *testing.T) {
	g := newDatabaseRig(t)
	g.onboardedDatabase(t)
	g.s.Refuse(gcptest.Refusal{Method: "GET", Host: "sqladmin.googleapis.com", Path: "/v1/projects/", Times: 1, Code: 403, Status: "PERMISSION_DENIED", Message: "not allowed"})
	code, out := g.call(t, WorkloadsOffboard, shopOffboard)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "workload shop is offboarded, but the tag of Cloud SQL instance shop-db is not removed", "cloudsql.instances.get", "run the same command again")
	if code, out = g.call(t, WorkloadsOffboard, shopOffboard); code != 0 {
		t.Fatalf("rerun: exit %d:\n%s", code, out)
	}
	if len(g.bindings()) != 0 {
		t.Fatalf("bindings %v", g.bindings())
	}
}

// onboardedOnGKE is workload shop as a GKE onboarding left it: its RBAC applied and recorded, a k8s backend.
func (g *rbacRig) onboardedOnGKE(t *testing.T) {
	t.Helper()
	g.grantedBefore(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{map[string]any{"kind": "k8s", "cluster": map[string]any{"location": "europe-west1-b", "name": "shop-cluster"},
		"namespace": "shop", "service": "web", "owner": map[string]any{"kind": "Deployment", "name": "web"}}}}
}

func TestWorkloadsOffboardRemovesTheRbacAfterTheController(t *testing.T) {
	g, order := newGKERig(t)
	g.onboardedOnGKE(t)
	g.s.Lock()
	g.s.TagValues = map[string]*gcptest.Tag{}
	g.s.Unlock()
	mark := len(order.calls())
	code, out := g.call(t, WorkloadsOffboard, shopOffboard)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var got []string
	for _, c := range order.calls()[mark:] {
		if method, rest, _ := strings.Cut(c, " "); method == "DELETE" {
			got = append(got, strings.TrimPrefix(strings.TrimPrefix(rest, controllerHost), kubeHost))
		}
	}
	if !slices.Equal(got, []string{"/v1/workloads/shop", shopBinding, shopRole}) {
		t.Fatalf("order:\n%s", strings.Join(got, "\n"))
	}
	if g.k.has(shopRole) || g.k.has(shopBinding) {
		t.Fatal("the RBAC stays")
	}
	if got := g.rbacOf(t); len(got) != 0 {
		t.Fatalf("the record still lists %v", got)
	}
	contains(t, out, "Offboarded workload shop", "removed: RoleBinding dormeto-shop", "removed: Role dormeto-shop")
	for _, c := range order.calls()[mark:] {
		if strings.Contains(c, "tagValues") || strings.Contains(c, "tagBindings") {
			t.Errorf("a GKE workload has no tag: %s", c)
		}
	}
}

func TestWorkloadsOffboardJSONSaysTheRbacItRemoved(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	r := shopOffboard
	r.JSON = true
	code, out := g.call(t, WorkloadsOffboard, r)
	var got struct{ Removed []string }
	if err := json.Unmarshal([]byte(out), &got); code != 0 || err != nil || !slices.Equal(got.Removed, []string{"RoleBinding dormeto-shop", "Role dormeto-shop"}) {
		t.Fatalf("%d %v:\n%s", code, err, out)
	}
}

func TestWorkloadsOffboardAsksTheGKEPermissionsOfAWorkloadOnGKE(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	missing := "container.roles.delete"
	g.s.HoldsAll = false
	g.s.Held = map[string]bool{}
	for _, p := range access.NeedsWith("offboard", []string{"gke"}) {
		g.s.Held[p] = p != missing
	}
	code, out := g.call(t, WorkloadsOffboard, shopOffboard)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "missing "+missing, "Nothing was changed.")
	if g.s.Workloads["shop"] != "AWAKE" || !g.k.has(shopRole) {
		t.Fatal("something was changed")
	}
}

func TestWorkloadsOffboardLeavesWhatIsNotOursAndSaysSo(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	g.k.mu.Lock()
	delete(g.k.objects, shopBinding)
	delete(g.k.objects, shopRole)
	g.k.mu.Unlock()
	g.k.put(t, shopRole, "Role", map[string]string{"team": "shop"}, map[string]any{"rules": []any{}})
	code, out := g.call(t, WorkloadsOffboard, shopOffboard)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Role dormeto-shop stays")
	if !g.k.has(shopRole) || len(g.rbacOf(t)) != 0 {
		t.Fatalf("role %v record %v", g.k.has(shopRole), g.rbacOf(t))
	}
}

func TestWorkloadsOffboardGoesOnForAClusterThatIsGone(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	g.gone = true
	code, out := g.call(t, WorkloadsOffboard, shopOffboard)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if len(g.rbacOf(t)) != 0 {
		t.Fatalf("record %v", g.rbacOf(t))
	}
}

func TestWorkloadsOffboardSaysTheRbacIsLeftThenARerunFinishes(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	g.k.deny = denyCall("DELETE "+shopRole, 403, "Forbidden", "roles is forbidden")
	r := shopOffboard
	r.Configuration = "c"
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "workload shop is offboarded, but its Kubernetes access is not removed", "roles is forbidden", "run the same command again",
		"dormeto gcp workloads access revoke --project dormeto-dev --region europe-west1 --gke-cluster "+shopCluster+" --namespace shop --workload shop --gcloud-configuration c")
	if len(g.s.Workloads) != 0 || !g.k.has(shopRole) || !slices.Contains(g.rbacOf(t), shopEntry) {
		t.Fatalf("workloads %v role %v record %v", g.s.Workloads, g.k.has(shopRole), g.rbacOf(t))
	}
	g.k.deny = nil
	if code, out = g.call(t, WorkloadsOffboard, r); code != 0 {
		t.Fatalf("rerun: exit %d:\n%s", code, out)
	}
	if g.k.has(shopRole) || len(g.rbacOf(t)) != 0 {
		t.Fatalf("role %v record %v", g.k.has(shopRole), g.rbacOf(t))
	}
}

// Without a cache, the record alone names the access of a workload the controller no longer knows.
func TestWorkloadsOffboardRerunFinishesFromTheRecordAlone(t *testing.T) {
	g, _ := newGKERig(t)
	g.grantedBefore(t)
	g.d.Cache = ""
	code, out := g.call(t, WorkloadsOffboard, shopOffboard)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if g.k.has(shopRole) || len(g.rbacOf(t)) != 0 {
		t.Fatalf("role %v record %v", g.k.has(shopRole), g.rbacOf(t))
	}
}

func TestWorkloadsOffboardForceNamesEverythingItCouldNotRemove(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	g.plantFeatures(t, "cloud-sql", "gke")
	g.s.SQLInstances = map[string]string{"shop-db": "europe-west3"}
	value := g.tagValue(t)
	g.s.WorkloadDocs["shop"]["backends"] = append(g.s.WorkloadDocs["shop"]["backends"].([]any),
		map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"}, map[string]any{"kind": "cloudsql", "name": "shop-db"})
	g.s.TagBindings = []gcptest.TagBinding{{Parent: vmParent, TagValue: value}, {Parent: databaseParent, TagValue: value}}
	g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: zoneHost, Path: "/v3/tagBindings", Code: 500, Status: "INTERNAL", Message: "boom"})
	g.k.deny = denyCall("DELETE "+shopBinding, 403, "Forbidden", "rolebindings is forbidden")
	r := shopOffboard
	r.Force = true
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Offboarded workload shop", "the tag of vm web-1 (europe-west1-b) is not removed", "boom", "untagged: Cloud SQL instance shop-db",
		"Role and RoleBinding dormeto-shop, namespace shop, cluster "+shopCluster, "rolebindings is forbidden", "access revoke")
	if len(g.s.Workloads) != 0 || !slices.Contains(g.rbacOf(t), shopEntry) {
		t.Fatalf("workloads %v record %v", g.s.Workloads, g.rbacOf(t))
	}
	if _, err := os.Stat(g.offboardFile()); err != nil {
		t.Fatalf("a tag left is finished by a rerun: %v", err)
	}
}

func TestWorkloadsOffboardForceJSONNamesWhatIsLeft(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	g.k.deny = denyCall("DELETE "+shopBinding, 403, "Forbidden", "rolebindings is forbidden")
	var errs bytes.Buffer
	g.d.Err = &errs
	r := shopOffboard
	r.Force, r.JSON = true, true
	code, out := g.call(t, WorkloadsOffboard, r)
	var got struct {
		Offboarded bool
		NotRemoved []string
	}
	if err := json.Unmarshal([]byte(out), &got); code != 1 || err != nil || !got.Offboarded || len(got.NotRemoved) != 1 {
		t.Fatalf("%d %v:\n%s", code, err, out)
	}
}

func TestWorkloadsOffboardForceOfAWorkloadThatCannotWakeNamesItsPods(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	g.s.WakeFails["shop"] = true
	r := shopOffboard
	r.Force = true
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "left down: Deployment web (namespace shop, cluster "+shopCluster+")")
	if g.k.has(shopRole) {
		t.Fatal("the RBAC stays")
	}
}

const pausedLeft = "ScaledObject shop/web-scaler is still paused at 0: remove its annotation autoscaling.keda.sh/paused-replicas, or KEDA takes Deployment shop/web down again; it had 2 replicas"

func TestWorkloadsOffboardForceSaysWhatTheRouterCouldNotGiveBackAndExits1(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	g.s.WakeFails["shop"] = true
	g.s.NotGivenBack = map[string][]string{"shop": {pausedLeft}}
	r := shopOffboard
	r.Force = true
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "left down: Deployment web", "! "+pausedLeft, "yours to change")
	if strings.Contains(out, "Run the same command again") {
		t.Fatalf("a rerun gives none of it back:\n%s", out)
	}
	if g.k.has(shopRole) || g.k.has(shopBinding) {
		t.Fatal("the RBAC stays")
	}
	if _, err := os.Stat(g.offboardFile()); !os.IsNotExist(err) {
		t.Fatalf("nothing is left for a rerun, and the file stays: %v", err)
	}
}

func TestWorkloadsOffboardForceJSONSaysWhatTheRouterCouldNotGiveBack(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	g.s.WakeFails["shop"] = true
	g.s.NotGivenBack = map[string][]string{"shop": {pausedLeft}}
	r := shopOffboard
	r.Force, r.JSON = true, true
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var answer struct {
		NotGivenBack []string `json:"notGivenBack"`
	}
	line, _, _ := strings.Cut(out, "\n")
	if err := json.Unmarshal([]byte(line), &answer); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if !slices.Equal(answer.NotGivenBack, []string{pausedLeft}) {
		t.Fatalf("%v", answer)
	}
}

// A router below this version answers no such field: none, and the exit as it was.
func TestWorkloadsOffboardForceOfARouterWithoutTheFieldReadsAsNone(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	g.s.WakeFails["shop"] = true
	r := shopOffboard
	r.Force, r.JSON = true, true
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 0 || strings.Contains(out, "notGivenBack") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestWorkloadsOffboardForceWithTheControllerGoneNamesWhatIsLeft(t *testing.T) {
	for name, refuse := range map[string]gcptest.Refusal{
		"a 404 not the controller's": {Method: "GET", Host: controllerHost, Path: "/v1/workloads/shop", Code: 404, Status: "NOT_FOUND", Message: "no such service"},
		"a 503":                      {Method: "GET", Host: controllerHost, Path: "/v1/workloads/shop", Code: 503, Status: "UNAVAILABLE", Message: "down"},
	} {
		t.Run(name, func(t *testing.T) {
			g, _ := newGKERig(t)
			g.onboardedOnGKE(t)
			g.s.Refuse(refuse)
			mark := len(g.k.writes(0))
			before := len(g.s.Calls())
			r := shopOffboard
			r.Force = true
			code, out := g.call(t, WorkloadsOffboard, r)
			if code != 1 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			contains(t, out, "does not answer", refuse.Message, "not offboarded", "dmtr-europe-west1-shop", "tag dormeto=managed",
				"Role and RoleBinding dormeto-shop, namespace shop, cluster "+shopCluster, "access revoke")
			if w := g.k.writes(0)[mark:]; len(w) != 0 {
				t.Fatalf("writes %v", w)
			}
			if m := g.changes(before); len(m) != 0 {
				t.Fatalf("changes %v", m)
			}
			// Without --force, the refusal alone.
			r.Force = false
			g.s.Refuse(refuse)
			if code, out = g.call(t, WorkloadsOffboard, r); code != 1 || strings.Contains(out, "access revoke") {
				t.Fatalf("exit %d:\n%s", code, out)
			}
		})
	}
}

func TestWorkloadsOffboardForceWithTheControllerGoneMidwayNamesTheBackends(t *testing.T) {
	g := newDatabaseRig(t)
	g.onboardedDatabase(t)
	g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: controllerHost, Path: "/v1/workloads/shop", Code: 503, Status: "UNAVAILABLE", Message: "down"})
	r := shopOffboard
	r.Force = true
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "does not answer", "may not be offboarded", "the tag dormeto=managed of vm web-1 (europe-west1-b)", "the tag dormeto=managed of Cloud SQL instance shop-db",
		"installing the router again brings it back: dormeto gcp routers install --project dormeto-dev --region europe-west1 --image <a good image>")
	if len(g.bindings()) != 2 {
		t.Fatalf("bindings %v", g.bindings())
	}
}

// The instance was just read: a 403 on its bindings is no sign that it is gone, and its tag is left.
func TestWorkloadsOffboardForceNamesADatabasesTagWhoseBindingsAreDenied(t *testing.T) {
	g := newDatabaseRig(t)
	g.onboardedDatabase(t)
	g.s.Refuse(gcptest.Refusal{Method: "GET", Host: databaseHost, Path: "/v3/tagBindings", Code: 403, Status: "PERMISSION_DENIED", Message: forbidden})
	var errs bytes.Buffer
	g.d.Err = &errs
	r := shopOffboard
	r.Force, r.JSON = true, true
	code, out := g.call(t, WorkloadsOffboard, r)
	var got struct {
		Offboarded bool
		NotRemoved []string
	}
	if err := json.Unmarshal([]byte(out), &got); code != 1 || err != nil || !got.Offboarded || len(got.NotRemoved) != 1 {
		t.Fatalf("%d %v:\n%s\n%s", code, err, out, errs.String())
	}
	contains(t, got.NotRemoved[0], "the tag of Cloud SQL instance shop-db is not removed", forbidden)
	if !slices.Contains(g.bindings(), gcptest.TagBinding{Parent: databaseParent, TagValue: g.tagValue(t)}) {
		t.Fatalf("bindings %v", g.bindings())
	}
	if _, err := os.Stat(g.offboardFile()); err != nil {
		t.Fatalf("a rerun finishes from the file: %v", err)
	}
}

func TestWorkloadsOffboardStopsOnADatabasesTagWhoseBindingsAreDeniedThenARerunFinishes(t *testing.T) {
	g := newDatabaseRig(t)
	g.onboardedDatabase(t)
	g.s.Refuse(gcptest.Refusal{Method: "GET", Host: databaseHost, Path: "/v3/tagBindings", Times: 1, Code: 403, Status: "PERMISSION_DENIED", Message: forbidden})
	code, out := g.call(t, WorkloadsOffboard, shopOffboard)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "workload shop is offboarded, but the tag of Cloud SQL instance shop-db is not removed", forbidden, "run the same command again")
	if strings.Contains(out, "it may be gone") {
		t.Fatalf("it was just read:\n%s", out)
	}
	if code, out = g.call(t, WorkloadsOffboard, shopOffboard); code != 0 {
		t.Fatalf("rerun: exit %d:\n%s", code, out)
	}
	if len(g.bindings()) != 0 {
		t.Fatalf("bindings %v", g.bindings())
	}
}

func TestWorkloadsOnboardRefusedSaysADatabasesTagWhoseBindingsAreDenied(t *testing.T) {
	g := newDatabaseRig(t)
	g.s.OnboardingRefused = refusedOnboarding
	g.s.Refuse(gcptest.Refusal{Method: "GET", Host: databaseHost, Path: "/v3/tagBindings", Code: 403, Status: "PERMISSION_DENIED", Message: forbidden})
	code, out := g.call(t, WorkloadsOnboard, shopWithDatabase)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "The tag binding of Cloud SQL instance shop-db is not removed", forbidden)
}

// cloudRun404 is Cloud Run's own 404: no controller answered, so it says nothing of the workload.
func cloudRun404(method string) gcptest.Refusal {
	return gcptest.Refusal{Method: method, Host: controllerHost, Path: "/v1/workloads/shop", Code: 404, Status: "NOT_FOUND", Message: "no such service"}
}

// onboardedBoth is workload shop on GKE with a database, its tag and its RBAC there.
func (g *rbacRig) onboardedBoth(t *testing.T) {
	t.Helper()
	g.onboardedOnGKE(t)
	g.plantFeatures(t, "cloud-sql", "gke")
	g.s.SQLInstances = map[string]string{"shop-db": "europe-west3"}
	g.s.WorkloadDocs["shop"]["backends"] = append(g.s.WorkloadDocs["shop"]["backends"].([]any), map[string]any{"kind": "cloudsql", "name": "shop-db"})
	g.s.TagBindings = []gcptest.TagBinding{{Parent: databaseParent, TagValue: g.tagValue(t)}}
}

// nothingRemoved checks that the tag and the RBAC are there, with the workload.
func (g *rbacRig) nothingRemoved(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "Offboarded workload") {
		t.Errorf("it says so:\n%s", out)
	}
	if len(g.bindings()) != 1 || !g.k.has(shopRole) || !g.k.has(shopBinding) || g.s.Workloads["shop"] != "AWAKE" || !slices.Contains(g.rbacOf(t), shopEntry) {
		t.Errorf("bindings %v role %v binding %v workloads %v record %v:\n%s", g.bindings(), g.k.has(shopRole), g.k.has(shopBinding), g.s.Workloads, g.rbacOf(t), out)
	}
}

func TestWorkloadsOffboardWithAFileLeftAndA404NotTheControllersRemovesNothing(t *testing.T) {
	for _, force := range []bool{false, true} {
		g, _ := newGKERig(t)
		g.onboardedBoth(t)
		// A delete with no answer leaves the file, as it may have gone through.
		g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: controllerHost, Path: "/v1/workloads/shop", Times: 1, Code: 500, Status: "INTERNAL", Message: "boom"})
		if code, out := g.call(t, WorkloadsOffboard, shopOffboard); code != 1 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if _, err := os.Stat(g.offboardFile()); err != nil {
			t.Fatalf("no file: %v", err)
		}
		g.s.Refuse(cloudRun404("GET"))
		r := shopOffboard
		r.Force = force
		code, out := g.call(t, WorkloadsOffboard, r)
		if code != 1 {
			t.Fatalf("force %v: exit %d:\n%s", force, code, out)
		}
		contains(t, out, "no such service")
		g.nothingRemoved(t, out)
		if force {
			contains(t, out, "does not answer", "the tag dormeto=managed of Cloud SQL instance shop-db", "access revoke")
		}
	}
}

func TestWorkloadsOffboardWithADeleteAnswering404NotTheControllersRemovesNothing(t *testing.T) {
	for _, force := range []bool{false, true} {
		g, _ := newGKERig(t)
		g.onboardedBoth(t)
		g.s.Refuse(cloudRun404("DELETE"))
		r := shopOffboard
		r.Force = force
		code, out := g.call(t, WorkloadsOffboard, r)
		if code != 1 {
			t.Fatalf("force %v: exit %d:\n%s", force, code, out)
		}
		contains(t, out, "no such service")
		g.nothingRemoved(t, out)
		if force {
			contains(t, out, "is not offboarded")
		}
	}
}

// With force, the second delete: a wake failed, then Cloud Run's own 404.
func TestWorkloadsOffboardForceWithItsForcedDeleteAnswering404NotTheControllersRemovesNothing(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedBoth(t)
	g.s.Route(controllerHost, "/v1/workloads/shop", func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet:
			gcptest.Reply(w, map[string]any{"id": "shop", "state": "FAILED", "backends": g.s.WorkloadDocs["shop"]["backends"]})
		case req.URL.Query().Get("force") == "true":
			gcptest.Fail(w, 404, "NOT_FOUND", "no such service")
		default:
			w.WriteHeader(409)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "its wake failed", "reason": "wake-failed"})
		}
	})
	r := shopOffboard
	r.Force = true
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "does not answer", "no such service")
	g.nothingRemoved(t, out)
}

func TestWorkloadsOffboardRefusedAtTheControllerKeepsNoFile(t *testing.T) {
	for name, set := range map[string]func(*rbacRig){
		"a failed wake": func(g *rbacRig) { g.s.WakeFails["shop"] = true },
		"a refusal": func(g *rbacRig) {
			g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: controllerHost, Path: "/v1/workloads/shop", Code: 409, Status: "ABORTED", Message: "busy"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			g, _ := newGKERig(t)
			g.onboardedBoth(t)
			set(g)
			code, out := g.call(t, WorkloadsOffboard, shopOffboard)
			if code != 1 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			g.nothingRemoved(t, out)
			if _, err := os.Stat(g.offboardFile()); !os.IsNotExist(err) {
				t.Fatalf("the file stays: %v", err)
			}
		})
	}
}

// cancelAfter cancels once a call matched has been answered whole: the run goes on with its context cancelled, the call done.
type cancelAfter struct {
	next   http.RoundTripper
	call   string // "<method> <host><path>"
	cancel context.CancelFunc
}

func (c cancelAfter) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := c.next.RoundTrip(req)
	if err != nil || req.Method+" "+req.URL.Host+req.URL.Path != c.call {
		return res, err
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return nil, err
	}
	res.Body = io.NopCloser(bytes.NewReader(body))
	c.cancel()
	return res, nil
}

// cancelledAfter is a context cancelled once that call is answered.
func (g *installRig) cancelledAfter(t *testing.T, call string) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	http2 := *g.d.Google.HTTP
	http2.Transport = cancelAfter{http2.Transport, call, cancel}
	g.d.Google.HTTP = &http2
	return ctx
}

func (g *installRig) interrupted(t *testing.T, ctx context.Context, f func(context.Context, Deps, Request) int, r Request) string {
	t.Helper()
	g.out.Reset()
	code := f(ctx, g.d, r)
	out := g.out.String()
	if code != 130 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	return out
}

// The onboarding was never sent: what this run added is removed, whatever the interrupt.
func TestWorkloadsOnboardInterruptedAtATagRemovesWhatItAddedAndSaysSo(t *testing.T) {
	g, order := newGKERig(t)
	g.plantFeatures(t, "cloud-sql", "gke")
	g.s.SQLInstances = map[string]string{"shop-db": "europe-west3"}
	g.s.DryRun.VMs = []gcptest.Resource{{Location: "europe-west1-b", Name: "web-1", ID: "111"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.s.Route(databaseHost, "/v3/tagBindings", func(w http.ResponseWriter, _ *http.Request) { cancel(); gcptest.Cut(w) })
	r := shopOnGKE
	r.CloudSQL = []string{"shop-db"}
	out := g.interrupted(t, ctx, WorkloadsOnboard, r)
	contains(t, out, "Interrupted before the onboarding was sent", "The tag of Cloud SQL instance shop-db may be bound", "removed", "RoleBinding dormeto-shop", "vm web-1 (europe-west1-b)", "run the same command again")
	if g.k.has(shopRole) || g.k.has(shopBinding) || len(g.bindings()) != 0 {
		t.Fatalf("role %v binding %v bindings %v:\n%s", g.k.has(shopRole), g.k.has(shopBinding), g.bindings(), out)
	}
	if got := g.rbacOf(t); len(got) != 0 {
		t.Fatalf("the record still lists %v", got)
	}
	if slices.Contains(order.calls(), "POST "+controllerHost+"/v1/workloads") {
		t.Fatal("the onboarding was sent")
	}
}

// Cancelled once the RBAC is applied, the onboarding was not sent: this is no lost answer.
func TestWorkloadsOnboardInterruptedBeforeItsCallRemovesTheRbac(t *testing.T) {
	g, order := newGKERig(t)
	ctx := g.cancelledAfter(t, "POST "+kubeHost+bindingsPath)
	out := g.interrupted(t, ctx, WorkloadsOnboard, shopOnGKE)
	contains(t, out, "Interrupted before the onboarding was sent", "removed")
	if strings.Contains(out, "may be onboarded") {
		t.Fatalf("it was not sent:\n%s", out)
	}
	if g.k.has(shopRole) || g.k.has(shopBinding) {
		t.Fatalf("role %v binding %v:\n%s", g.k.has(shopRole), g.k.has(shopBinding), out)
	}
	if slices.Contains(order.calls(), "POST "+controllerHost+"/v1/workloads") {
		t.Fatal("the onboarding was sent")
	}
}

func TestWorkloadsOnboardInterruptedDuringTheUndoFinishesIt(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.OnboardingRefused = refusedOnboarding
	ctx := g.cancelledAfter(t, "DELETE "+kubeHost+shopBinding)
	out := g.interrupted(t, ctx, WorkloadsOnboard, shopOnGKE)
	contains(t, out, refusedOnboarding, "removed", "RoleBinding dormeto-shop", "Role dormeto-shop")
	if g.k.has(shopRole) || g.k.has(shopBinding) || len(g.rbacOf(t)) != 0 {
		t.Fatalf("role %v binding %v record %v:\n%s", g.k.has(shopRole), g.k.has(shopBinding), g.rbacOf(t), out)
	}
}

func TestWorkloadsOnboardInterruptedReadingTheOwnerSaysSoAlone(t *testing.T) {
	g, _ := newGKERig(t)
	ctx := g.cancelledAfter(t, "GET "+kubeHost+servicePath)
	out := g.interrupted(t, ctx, WorkloadsOnboard, shopOnGKE)
	contains(t, out, "Interrupted.")
	if strings.Contains(out, "is not read") || len(g.k.writes(0)) != 0 {
		t.Fatalf("writes %v:\n%s", g.k.writes(0), out)
	}
}

func TestWorkloadsOffboardInterruptedAtTheRbacSaysWhatIsLeft(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	ctx := g.cancelledAfter(t, "DELETE "+kubeHost+shopBinding)
	r := shopOffboard
	r.Configuration = "c"
	out := g.interrupted(t, ctx, WorkloadsOffboard, r)
	contains(t, out, "workload shop is offboarded, but its Kubernetes access is not removed: interrupted", "run the same command again", "access revoke")
	if _, err := os.Stat(g.offboardFile()); err != nil {
		t.Fatalf("a rerun finishes from the file: %v", err)
	}
}

func TestWorkloadsOffboardForceInterruptedSaysWhatIsLeftAsInterrupted(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	ctx := g.cancelledAfter(t, "DELETE "+kubeHost+shopBinding)
	r := shopOffboard
	r.Force = true
	out := g.interrupted(t, ctx, WorkloadsOffboard, r)
	contains(t, out, "Offboarded workload shop", "Role and RoleBinding dormeto-shop", "interrupted", "access revoke")
	if strings.Contains(out, "context canceled") {
		t.Fatalf("%s", out)
	}
}

func TestWorkloadsOffboardInterruptedAtTheTagsSaysTheyAreLeft(t *testing.T) {
	for _, force := range []bool{false, true} {
		g := newDatabaseRig(t)
		g.onboardedDatabase(t)
		ctx := g.cancelledAfter(t, "DELETE "+controllerHost+"/v1/workloads/shop")
		r := shopOffboard
		r.Force = force
		out := g.interrupted(t, ctx, WorkloadsOffboard, r)
		want := "workload shop is offboarded, but its tags are not removed: interrupted"
		if force {
			want = "the tag of Cloud SQL instance shop-db is not removed: interrupted"
		}
		contains(t, out, want)
		if strings.Contains(out, "context canceled") || len(g.bindings()) != 2 {
			t.Fatalf("bindings %v:\n%s", g.bindings(), out)
		}
		if _, err := os.Stat(g.offboardFile()); err != nil {
			t.Fatalf("a rerun finishes from the file: %v", err)
		}
	}
}

// The delete's outcome is unknown: nothing more is removed, and a rerun tells.
func TestWorkloadsOffboardInterruptedInTheDeleteSaysItMayBeDone(t *testing.T) {
	g := newDatabaseRig(t)
	g.onboardedDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.s.Route(controllerHost, "/v1/workloads/shop", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			gcptest.Reply(w, map[string]any{"id": "shop", "state": "AWAKE", "backends": []any{map[string]any{"kind": "cloudsql", "name": "shop-db"}}})
			return
		}
		cancel()
		gcptest.Cut(w)
	})
	out := g.interrupted(t, ctx, WorkloadsOffboard, shopOffboard)
	contains(t, out, "Interrupted", "workload shop may be offboarded", "run the same command again")
	if len(g.bindings()) != 2 {
		t.Fatalf("bindings %v", g.bindings())
	}
	if _, err := os.Stat(g.offboardFile()); err != nil {
		t.Fatalf("a rerun finishes from the file: %v", err)
	}
}

func TestWorkloadsOnboardGKERefusedSaysWhatItUpdatedOnARoleThatWasThere(t *testing.T) {
	g, _ := newGKERig(t)
	g.grantedBefore(t)
	g.k.put(t, shopRole, "Role", ourLabels, map[string]any{"rules": []any{}})
	g.s.OnboardingRefused = refusedOnboarding
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "there before this run", "this run updated: Role dormeto-shop")
	if !g.k.has(shopRole) {
		t.Fatal("the Role was there")
	}
}

func TestWorkloadsOnboardBindsNoTagOnADatabaseTheFlagsDoNotName(t *testing.T) {
	g := newDatabaseRig(t)
	g.s.SQLInstances["other-db"] = "europe-west3"
	g.s.DryRun.MoreDatabases = []string{"other-db"}
	r := shopWithDatabase
	r.DryRun = true
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 0 || strings.Contains(out, "other-db") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	code, out = g.call(t, WorkloadsOnboard, shopWithDatabase)
	if code != 0 || strings.Contains(out, "other-db") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, b := range g.bindings() {
		if strings.Contains(b.Parent, "other-db") {
			t.Fatalf("bindings %v", g.bindings())
		}
	}
}

func TestWorkloadsOnboardReadsNoClusterItWasNotAskedFor(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.DryRun.GKEUnasked, g.s.DryRun.GKE.Autopilot = true, false
	g.s.DryRun.VMs = []gcptest.Resource{{Location: "europe-west1-b", Name: "web-1", ID: "111"}}
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "Standard") || len(g.k.callList()) != 0 {
		t.Fatalf("calls %v:\n%s", g.k.callList(), out)
	}
	if _, there := g.onboarding(t)["gke"]; there {
		t.Fatalf("%v", g.onboarding(t))
	}
}

// The region names the host the operator's token goes to.
func TestWorkloadsOnboardRefusesARegionThatIsNoRegion(t *testing.T) {
	g := newDatabaseRig(t)
	g.s.SQLInstances["shop-db"] = "evil.example.com/x"
	code, out := g.call(t, WorkloadsOnboard, shopWithDatabase)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "evil.example.com/x", "Nothing was changed.")
	if len(g.bindings()) != 0 || len(g.s.Workloads) != 0 {
		t.Fatal("something was changed")
	}
}

func TestWorkloadsOnboardGKERefusedWhenTheBindingWillNotGoLeavesTheRole(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.OnboardingRefused = refusedOnboarding
	g.k.deny = denyCall("DELETE "+shopBinding, 403, "Forbidden", "rolebindings is forbidden")
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "RoleBinding dormeto-shop of namespace shop is not removed", "Role dormeto-shop stays")
	if !g.k.has(shopRole) || !g.k.has(shopBinding) || !slices.Contains(g.rbacOf(t), shopEntry) {
		t.Fatalf("role %v binding %v record %v", g.k.has(shopRole), g.k.has(shopBinding), g.rbacOf(t))
	}
}

// A binding of that name that is no longer ours may name our Role: as a revoke, the undo leaves it.
func TestWorkloadsOnboardGKERefusedLeavesTheRoleWhenABindingOfThatNameIsNotOurs(t *testing.T) {
	g, _ := newGKERig(t)
	g.s.Route(controllerHost, "/v1/workloads", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("dryRun") == "true" {
			gcptest.Reply(w, map[string]any{"eligible": true, "reasons": []any{}, "vms": []any{}, "groups": []any{}, "lb": map[string]any{"kind": "global"}, "databases": []any{},
				"gke": map[string]any{"namespace": "shop", "service": "web", "autopilot": true}})
			return
		}
		g.k.put(t, shopBinding, "RoleBinding", map[string]string{"team": "shop"}, map[string]any{"subjects": []any{},
			"roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "dormeto-shop"}})
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": refusedOnboarding, "reason": "not-eligible"})
	})
	code, out := g.call(t, WorkloadsOnboard, shopOnGKE)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "RoleBinding dormeto-shop", "not Dormeto's", "Role dormeto-shop stays")
	if !g.k.has(shopRole) || !g.k.has(shopBinding) {
		t.Fatalf("role %v binding %v", g.k.has(shopRole), g.k.has(shopBinding))
	}
}

// What the record does not list: only the file lets a rerun find it.
func TestWorkloadsOffboardForceKeepsTheFileForAnAccessTheRecordDoesNotList(t *testing.T) {
	g, _ := newGKERig(t)
	g.onboardedOnGKE(t)
	rec := g.record(t)
	rec.Rbac = nil
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	g.s.Lock()
	g.s.Documents[recordDB+"/install/record"]["data"] = map[string]any{"stringValue": string(b)}
	g.s.Unlock()
	g.k.deny = denyCall("DELETE "+shopBinding, 403, "Forbidden", "rolebindings is forbidden")
	r := shopOffboard
	r.Force = true
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(g.offboardFile()); err != nil {
		t.Fatalf("no file: %v", err)
	}
	g.k.deny = nil
	if code, out = g.call(t, WorkloadsOffboard, r); code != 0 {
		t.Fatalf("rerun: exit %d:\n%s", code, out)
	}
	if g.k.has(shopRole) || g.k.has(shopBinding) {
		t.Fatal("the RBAC stays")
	}
	if _, err := os.Stat(g.offboardFile()); !os.IsNotExist(err) {
		t.Fatalf("the file stays: %v", err)
	}
}

// On a router of Dormeto's public image, the hints to upgrade it name --version: the CLI's version, which has what they
// need. On a router of --image (the tests above), they keep --image: the customer rebuilds their copy.
func TestWorkloadsHintsOnARouterOfThePublicImageNameTheVersion(t *testing.T) {
	def := embeddedRouterVersion()
	if install.CompareVersions(def, minRouterVersion) < 0 || install.CompareVersions(def, schedule.RecurrenceRouter) < 0 {
		t.Fatalf("the CLI's router %s is older than what the hints need", def)
	}
	upgrade := "dormeto gcp routers install --project dormeto-dev --region europe-west1 --version " + def
	t.Run("recurrences", func(t *testing.T) {
		g := newPublicWorkloadsRig(t)
		g.s.Workloads["shop"] = "AWAKE"
		g.s.ScheduleProblem = "windows[0].repeat: unknown field"
		r := workloadsEurope
		r.ID, r.File = "shop", g.scheduleFile(t, recurringSchedule)
		code, out := g.call(t, WorkloadsScheduleSet, r)
		if code != 1 || strings.Contains(out, "--image") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		contains(t, out, "Recurrences and joined windows need a router of version 0.36.0 or later. Upgrade it with: "+upgrade+"\n")
	})
	t.Run("schedules", func(t *testing.T) {
		g := newPublicWorkloadsRig(t)
		g.s.Workloads["shop"] = "AWAKE"
		g.s.ControllerBelow035 = true
		r := workloadsEurope
		r.ID = "shop"
		code, out := g.call(t, WorkloadsScheduleShow, r)
		if code != 1 || strings.Contains(out, "--image") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		contains(t, out, "need a router of version 0.35.0 or later. Upgrade it with: "+upgrade+"\n")
	})
	t.Run("a flag the dry run ignores", func(t *testing.T) {
		g := newPublicWorkloadsRig(t)
		g.s.ControllerBelow035 = true
		r := shopOnboarding
		r.LoadBalancer = "classic"
		code, out := g.call(t, WorkloadsOnboard, r)
		if code != 1 || strings.Contains(out, "--image") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		contains(t, out, "Install a newer router first: "+upgrade+".\n")
	})
	t.Run("a forced offboarding the controller does not answer", func(t *testing.T) {
		g := newPublicWorkloadsRig(t)
		g.s.Workloads["shop"] = "AWAKE"
		g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"}}}
		g.s.TagBindings = []gcptest.TagBinding{{Parent: vmParent, TagValue: g.tagValue(t)}}
		g.s.Refuse(gcptest.Refusal{Method: "DELETE", Host: controllerHost, Path: "/v1/workloads/shop", Code: 503, Status: "UNAVAILABLE", Message: "down"})
		r := shopOffboard
		r.Force = true
		code, out := g.call(t, WorkloadsOffboard, r)
		if code != 1 || strings.Contains(out, "--image") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		contains(t, out, "installing the router again brings it back: "+upgrade)
	})
}

func TestWorkloadsShowSaysWhyTheLastNapWasGivenUp(t *testing.T) {
	for name, aborted := range map[string]any{
		"given up":  map[string]any{"reason": "backends-changed", "at": "2026-11-08T04:55:57Z"},
		"not given": nil,
	} {
		g := newWorkloadsRig(t)
		g.s.Workloads["shop"] = "AWAKE"
		g.s.WorkloadDocs["shop"] = map[string]any{"backendService": "shop"}
		if aborted != nil {
			g.s.WorkloadDocs["shop"]["napAborted"] = aborted
		}
		code, out := g.call(t, WorkloadsShow, offboardShop)
		if code != 0 {
			t.Fatalf("%s: exit %d:\n%s", name, code, out)
		}
		has := strings.Contains(out, "  nap given up: backends-changed, 2026-11-08T04:55:57Z\n")
		if has != (aborted != nil) || aborted == nil && strings.Contains(out, "nap given up") {
			t.Errorf("%s:\n%s", name, out)
		}
	}
}

// The wake mode is a setting of the workload's level, the fallback machine types a field of the body: both calls get them, the
// dry run too (it checks each VM against them).
func TestWorkloadsOnboardSendsTheWakeModeAndTheFallbackTypesToBothCalls(t *testing.T) {
	g := newWorkloadsRig(t)
	r := shopOnboarding
	r.WakeMode, r.FallbackTypes = "browser", []string{"e2-medium", "n2-standard-2"}
	if code, out := g.call(t, WorkloadsOnboard, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, query := range []string{"/v1/workloads?dryRun=true", "/v1/workloads"} {
		in := g.body(t, "POST", controllerHost, query)
		if !jsonEqual(in["settings"], map[string]any{"wakeMode": "browser"}) || !jsonEqual(in["fallbackInstanceTypes"], []string{"e2-medium", "n2-standard-2"}) {
			t.Errorf("%s: %v", query, in)
		}
	}
	g = newWorkloadsRig(t)
	if code, out := g.call(t, WorkloadsOnboard, shopOnboarding); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	in := g.body(t, "POST", controllerHost, "/v1/workloads")
	if _, there := in["fallbackInstanceTypes"]; there || in["settings"] != nil {
		t.Errorf("sent when not given: %v", in)
	}
}

func TestWorkloadsOnboardTakesTheInternalKinds(t *testing.T) {
	for _, kind := range []string{"internal", "cross-region-internal"} {
		g := newWorkloadsRig(t)
		r := shopOnboarding
		r.LoadBalancer = kind
		if code, out := g.call(t, WorkloadsOnboard, r); code != 0 {
			t.Fatalf("%s: exit %d:\n%s", kind, code, out)
		}
		if in := g.body(t, "POST", controllerHost, "/v1/workloads"); in["loadBalancer"] != kind {
			t.Errorf("%s: %v", kind, in)
		}
	}
}

// internal is a regional map (the router's region when it names none), cross-region-internal a global one: an older CLI read
// internal as global, and with --request-logs would change a global backend service of the same name.
func TestRegionOfTheInternalKinds(t *testing.T) {
	x := &run{n: names{region: "europe-west1"}}
	for _, c := range []struct {
		lb   *lbRef
		want string
	}{
		{nil, ""}, {&lbRef{Kind: "global"}, ""}, {&lbRef{Kind: "classic"}, ""},
		{&lbRef{Kind: "regional"}, "europe-west1"}, {&lbRef{Kind: "regional", Region: "europe-west4"}, "europe-west4"},
		{&lbRef{Kind: "internal"}, "europe-west1"}, {&lbRef{Kind: "internal", Region: "europe-west4"}, "europe-west4"},
		{&lbRef{Kind: "cross-region-internal"}, ""}, {&lbRef{Kind: "cross-region-internal", Region: "europe-west4"}, ""},
	} {
		if got := x.regionOf(c.lb); got != c.want {
			t.Errorf("%+v: %q, want %q", c.lb, got, c.want)
		}
	}
}

// What --request-logs changes is the backend service of the map's own place.
func TestWorkloadsOnboardRequestLogsOfTheInternalKindsPatchTheirOwnBackendService(t *testing.T) {
	for kind, want := range map[string]string{"internal": regionalService, "cross-region-internal": globalService} {
		g := newLogsRig(t)
		g.logging("europe-west1", nil)
		g.logging("global", nil)
		r := shopWithLogs
		r.LoadBalancer = kind
		mark := len(g.s.Calls())
		if code, out := g.call(t, WorkloadsOnboard, r); code != 0 {
			t.Fatalf("%s: exit %d:\n%s", kind, code, out)
		}
		if got := g.patches(mark); !slices.Equal(got, []string{want}) {
			t.Errorf("%s: patches %v, want %s", kind, got, want)
		}
	}
}

// The router names each VM that runs on a fallback type, in the plain offboarding and in the forced one; no VM, no line.
func TestWorkloadsOffboardNamesTheVMsOnAFallbackType(t *testing.T) {
	onFallback := map[string]map[string]string{"web-1": {"from": "e2-small", "to": "e2-medium"}, "web-2": {"from": "n2-standard-2", "to": "n2-standard-4"}}
	for _, force := range []bool{false, true} {
		g := newWorkloadsRig(t)
		if force {
			g.failingToWake(t)
		}
		g.s.Workloads["shop"] = map[bool]string{false: "AWAKE", true: "FAILED"}[force]
		g.s.OnFallback = map[string]map[string]map[string]string{"shop": onFallback}
		r := workloadsEurope
		r.ID, r.Force = "shop", force
		code, out := g.call(t, WorkloadsOffboard, r)
		if code != 0 {
			t.Fatalf("force %v: exit %d:\n%s", force, code, out)
		}
		contains(t, out, "  VM web-1 is on e2-medium, its own is e2-small: it keeps e2-medium until you change it by hand\n",
			"  VM web-2 is on n2-standard-4, its own is n2-standard-2: it keeps n2-standard-4 until you change it by hand\n")
		if strings.Index(out, "web-1") > strings.Index(out, "web-2") {
			t.Errorf("not in order:\n%s", out)
		}
		// As JSON, the controller's own field.
		g = newWorkloadsRig(t)
		g.s.OnFallback = map[string]map[string]map[string]string{"shop": onFallback}
		g.s.Workloads["shop"] = "AWAKE"
		r.Force, r.JSON = false, true
		code, out = g.call(t, WorkloadsOffboard, r)
		// The rig's stdout and stderr share a buffer: the lines come first (on stderr), the answer last.
		answer := map[string]any{}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		last := lines[len(lines)-1]
		contains(t, out, "  VM web-1 is on e2-medium, its own is e2-small: it keeps")
		if err := json.Unmarshal([]byte(last), &answer); err != nil || code != 0 || !jsonEqual(answer["fallbacks"], []map[string]string{{"instance": "web-1", "from": "e2-small", "to": "e2-medium"}, {"instance": "web-2", "from": "n2-standard-2", "to": "n2-standard-4"}}) {
			t.Errorf("force %v json: %d %s", force, code, out)
		}
	}
	g := newWorkloadsRig(t)
	r := workloadsEurope
	r.ID = "shop"
	g.s.Workloads["shop"] = "AWAKE"
	if _, out := g.call(t, WorkloadsOffboard, r); strings.Contains(out, "fallback") || strings.Contains(out, "by hand") {
		t.Errorf("a line with no VM on a fallback type:\n%s", out)
	}
}

func TestWorkloadsOnboardSendsTheNameAndTheTagsOnlyWhenGiven(t *testing.T) {
	g := newWorkloadsRig(t)
	if code, out := g.call(t, WorkloadsOnboard, shopOnboarding); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	in := g.body(t, "POST", controllerHost, "/v1/workloads")
	for _, key := range []string{"name", "tags"} {
		if _, ok := in[key]; ok {
			t.Fatalf("%s is sent only when given: %v", key, in)
		}
	}
	if _, ok := in["source"]; ok {
		t.Fatalf("the router decides the source: %v", in)
	}

	g = newWorkloadsRig(t)
	g.s.HealthVersion = "0.40.0" // a router that reads them
	r := shopOnboarding
	r.Name, r.Tags = "The shop", map[string]string{"team": "web", "env": "prod"}
	if code, out := g.call(t, WorkloadsOnboard, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, query := range []string{"/v1/workloads?dryRun=true", "/v1/workloads"} {
		in := g.body(t, "POST", controllerHost, query)
		if in["name"] != "The shop" || !jsonEqual(in["tags"], map[string]string{"env": "prod", "team": "web"}) {
			t.Errorf("%s: %v", query, in)
		}
	}
}

// A router below 0.40.0 drops the onboarding's name and tags: with --name or --set-tag, its version is read after the dry run,
// and one below 0.40.0 is refused before any change, with the install that brings a router reading them, as a flag the dry
// run does not answer is. The version's core decides (0.40.0's dev builds read them); its version unread, the run stops too.
func TestWorkloadsOnboardNameOrTagsOnARouterBelow040IsRefusedBeforeAnyChange(t *testing.T) {
	named, tagged, both := shopOnboarding, shopOnboarding, shopOnboarding
	named.Name = "The shop"
	tagged.Tags = map[string]string{"team": "web"}
	both.Name, both.Tags = "The shop", map[string]string{"team": "web"}
	for flags, r := range map[string]Request{"--name": named, "--set-tag": tagged, "--name and --set-tag": both} {
		for _, dry := range []bool{false, true} {
			g := newWorkloadsRig(t)
			g.s.HealthVersion = "0.39.9-dev.2"
			r.DryRun = dry
			mark := len(g.s.Calls())
			code, out := g.call(t, WorkloadsOnboard, r)
			if code != 1 {
				t.Fatalf("%s, dry run %t: exit %d:\n%s", flags, dry, code, out)
			}
			contains(t, out, "Router dmtr-europe-west1 is 0.39.9-dev.2, older than 0.40.0, which reads "+flags+". Install a newer router first: dormeto gcp routers install --project dormeto-dev --region europe-west1 ",
				"\nNothing was changed.\n")
			if m := g.changes(mark); len(m) != 1 || len(g.bindings()) != 0 {
				t.Errorf("%s, dry run %t: changes %v, bindings %v", flags, dry, m, g.bindings())
			}
			if got := g.controllerCalls(mark); !slices.Equal(got, []string{"POST /v1/workloads?dryRun=true", "GET /v1/health"}) {
				t.Errorf("%s, dry run %t: controller calls %v", flags, dry, got)
			}
		}
	}
	for _, version := range []string{"0.40.0", "0.40.0-dev.1"} {
		g := newWorkloadsRig(t)
		g.s.HealthVersion = version
		if code, out := g.call(t, WorkloadsOnboard, both); code != 0 || g.s.Workloads["shop"] != "AWAKE" {
			t.Fatalf("%s: exit %d:\n%s", version, code, out)
		}
	}
	// Without the flags, no version is read for them.
	g := newWorkloadsRig(t)
	mark := len(g.s.Calls())
	if code, out := g.call(t, WorkloadsOnboard, shopOnboarding); code != 0 || slices.Contains(g.controllerCalls(mark), "GET /v1/health") {
		t.Fatalf("exit %d, calls %v:\n%s", code, g.controllerCalls(mark), out)
	}
	// Its version unread: stopped, nothing changed.
	g = newWorkloadsRig(t)
	g.s.Refuse(gcptest.Refusal{Method: http.MethodGet, Host: controllerHost, Path: "/v1/health", Code: 503, Status: "UNAVAILABLE", Message: "down"})
	mark = len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, named)
	if code != 1 || len(g.changes(mark)) != 1 {
		t.Fatalf("exit %d, changes %v:\n%s", code, g.changes(mark), out)
	}
	contains(t, out, "The version of router dmtr-europe-west1 is not read, and --name needs 0.40.0 or later: ", "\nNothing was changed.\n")
}

func TestWorkloadsShowPrintsTheNameAndTheTags(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.HealthVersion = "0.40.0" // a router that reads them
	r := shopOnboarding
	r.Name, r.Tags = "The shop", map[string]string{"team": "web", "env": "prod"}
	if code, out := g.call(t, WorkloadsOnboard, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	show := workloadsEurope
	show.ID = "shop"
	code, out := g.call(t, WorkloadsShow, show)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  name: The shop\n", "  tags: env=prod, team=web\n")
	// A workload with no tag prints no tags line; its name is the router's default, the ID.
	g = newWorkloadsRig(t)
	g.call(t, WorkloadsOnboard, shopOnboarding)
	_, out = g.call(t, WorkloadsShow, show)
	contains(t, out, "  name: shop\n")
	if strings.Contains(out, "tags:") {
		t.Fatalf("%s", out)
	}
}

func TestRouterPauseAndResumePostToTheController(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"], g.s.Workloads["blog"] = "ASLEEP", "AWAKE"
	r := workloadsEurope
	r.Reason = "release freeze"
	mark := len(g.s.Calls())
	code, out := g.call(t, RouterPause, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	body := g.body(t, "POST", controllerHost, "/v1/pause")
	if body["reason"] != "release freeze" || body["by"] != "me@example.com" || len(body) != 2 {
		t.Fatalf("not asked, keepAsleep and until are not sent: %v", body)
	}
	contains(t, out, "dmtr-europe-west1: paused, woke shop\n")
	code, out = g.call(t, RouterResume, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "resumed")
	if body := g.body(t, "POST", controllerHost, "/v1/resume"); body["by"] != "me@example.com" || len(body) != 1 {
		t.Fatalf("%v", body)
	}
	if got := g.controllerCalls(mark); !slices.Equal(got, []string{"POST /v1/pause", "POST /v1/resume"}) {
		t.Fatalf("%v", got)
	}
	// Resumed again: nothing was paused.
	_, out = g.call(t, RouterResume, r)
	contains(t, out, "not paused")
}

func TestRouterPauseKeepAsleepAndUntil(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "ASLEEP"
	r := workloadsEurope
	r.Reason, r.KeepAsleep, r.Until = "audit", true, "2026-12-01T07:00:00Z"
	code, out := g.call(t, RouterPause, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	body := g.body(t, "POST", controllerHost, "/v1/pause")
	if body["keepAsleep"] != true || body["until"] != "2026-12-01T07:00:00Z" {
		t.Fatalf("%v", body)
	}
	contains(t, out, "paused until 2026-12-01 07:00 UTC, keeping asleep\n")
	if strings.Contains(out, "woke") {
		t.Fatalf("%s", out)
	}
	// --json: the router's answer by router, as AWS's {"<router>": <answer>}.
	r.JSON = true
	_, out = g.call(t, RouterPause, r)
	var a map[string]map[string]any
	if err := json.Unmarshal([]byte(out), &a); err != nil || len(a) != 1 || a["dmtr-europe-west1"]["pause"] == nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	_, out = g.call(t, RouterResume, r)
	a = nil
	if err := json.Unmarshal([]byte(out), &a); err != nil || len(a) != 1 || a["dmtr-europe-west1"]["resumed"] != true {
		t.Fatalf("%v:\n%s", err, out)
	}
}

func TestRouterPauseRefusesAMissingReasonBeforeAnyCall(t *testing.T) {
	g := newWorkloadsRig(t)
	mark := len(g.s.Calls())
	for _, reason := range []string{"", "  ", strings.Repeat("x", 257)} {
		r := workloadsEurope
		r.Reason = reason
		if code, out := g.call(t, RouterPause, r); code != 2 || out == "" {
			t.Errorf("%q: exit %d:\n%s", reason, code, out)
		}
	}
	r := workloadsEurope
	r.Reason, r.Until = "x", "tomorrow"
	if code, _ := g.call(t, RouterPause, r); code != 2 {
		t.Errorf("exit %d", code)
	}
	if got := g.controllerCalls(mark); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestRouterPauseAndResumeSayAnOlderRouterByItsVersion(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.ControllerBelow040 = true
	g.s.HealthVersion = "0.39.0"
	r := workloadsEurope
	r.Reason = "x"
	want := "router 0.39.0 doesn't have the router's pause: dormeto gcp routers install --version " + embeddedRouterVersion()
	for name, run := range map[string]func(context.Context, Deps, Request) int{"pause": RouterPause, "resume": RouterResume} {
		if code, out := g.call(t, run, r); code != 1 || !strings.Contains(out, want) {
			t.Errorf("%s: exit %d, want %q in:\n%s", name, code, want, out)
		}
	}
}

func TestRouterPauseAndResumeDryRunSendNothing(t *testing.T) {
	g := newWorkloadsRig(t)
	r := workloadsEurope
	r.Reason, r.KeepAsleep, r.Until, r.DryRun, r.Yes = "freeze", true, "2026-12-01T07:00:00Z", true, true
	mark := len(g.s.Calls())
	code, out := g.call(t, RouterPause, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "dry run", "dmtr-europe-west1", "nothing sent", `"reason":"freeze"`, `"keepAsleep":true`, `"until":"2026-12-01T07:00:00Z"`)
	code, out = g.call(t, RouterResume, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "dry run", "dmtr-europe-west1", "nothing sent")
	if got := g.controllerCalls(mark); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestRouterPauseSaysAWSLines(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"], g.s.Workloads["blog"] = "ASLEEP", "ASLEEP"
	r := workloadsEurope
	r.Reason, r.Until = "freeze", "2026-12-01T07:00:00Z"
	local := time.Date(2026, 12, 1, 7, 0, 0, 0, time.UTC).Format("2006-01-02 15:04 MST") // the rig's clock is UTC
	_, out := g.call(t, RouterPause, r)
	want := "dmtr-europe-west1: paused until " + local + ", woke blog, shop\n"
	if !strings.Contains(out, want) {
		t.Fatalf("want %q in:\n%s", want, out)
	}
	g.call(t, RouterResume, r)
	r.KeepAsleep, r.Until = true, ""
	_, out = g.call(t, RouterPause, r)
	if want := "dmtr-europe-west1: paused, keeping asleep\n"; !strings.Contains(out, want) {
		t.Fatalf("want %q in:\n%s", want, out)
	}
	r.Until, r.KeepAsleep = "2026-12-01T07:00:00Z", true
	_, out = g.call(t, RouterPause, r)
	if want := "dmtr-europe-west1: paused until " + local + ", keeping asleep\n"; !strings.Contains(out, want) {
		t.Fatalf("want %q in:\n%s", want, out)
	}
	_, out = g.call(t, RouterResume, r)
	contains(t, out, "dmtr-europe-west1: resumed\n")
	_, out = g.call(t, RouterResume, r)
	contains(t, out, "dmtr-europe-west1: not paused\n")
}

func TestWorkloadsOnboardSaysTheAccessMayStillPropagateWhenTheControllerFailsRightAfterTheInstall(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Refuse(gcptest.Refusal{Method: "POST", Host: controllerHost, Path: "/v1/workloads", Code: 500, Status: "INTERNAL", Message: "internal error"})
	g.d.Now = func() time.Time { return installNow.Add(9 * time.Minute) }
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 1 || !strings.Contains(out, "internal error") || !strings.Contains(out, propagatingHint) || !strings.Contains(out, "try again in a few minutes") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestWorkloadsOnboardSaysNothingOfPropagationWhenAPlain500ComesLongAfterTheInstall(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Refuse(gcptest.Refusal{Method: "POST", Host: controllerHost, Path: "/v1/workloads", Code: 500, Status: "INTERNAL", Message: "internal error"})
	g.d.Now = func() time.Time { return installNow.Add(11 * time.Minute) }
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 1 || !strings.Contains(out, "internal error") || strings.Contains(out, "propagating") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestWorkloadsOnboardSaysTheAccessMayStillPropagateWhenTheBodyNamesGoogles403LongAfterTheInstall(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Refuse(gcptest.Refusal{Method: "POST", Host: controllerHost, Path: "/v1/workloads", Code: 500, Status: "INTERNAL",
		Message: "7 PERMISSION_DENIED: Missing or insufficient permissions."})
	g.d.Now = func() time.Time { return installNow.Add(3 * time.Hour) }
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 1 || !strings.Contains(out, "Missing or insufficient permissions") || !strings.Contains(out, propagatingHint) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
