package gcponboard

// Google Cloud's standalone workloads in the gcp subtree (vision standalone workloads design §2, §5, §8): the kind in the
// list and its filter, show as dormeto aws workloads show shows the same workload.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/gcp/gcptest"
	"slices"

	"github.com/Dormeto/cli/internal/fleet"
)

const agentPath = "projects/dormeto-dev/zones/europe-west1-b/instances/build-agent"

// standaloneStatus is the router's status of the standalone workload build-agent, as AWS's status names its keys (the
// router's POST /v1/actions status), with resource as the cloud's id: path on Google Cloud, resourceArn on AWS.
func standaloneStatus(resourceKey, resource string) map[string]any {
	var status map[string]any
	_ = json.Unmarshal([]byte(`{"workloadId":"build-agent","name":"build-agent","kind":"standalone","state":"FAILED","stateAt":"2026-11-14T08:40:00.000Z",
"updatedAt":"2026-11-14T08:40:00.000Z","failedAt":"2026-11-14T08:40:00.000Z","failure":{"kind":"permission","step":"wake","message":"VM build-agent: Google refused the change"},
"consecutiveFailures":2,"nextAttemptAt":"2026-11-14T10:10:00.000Z","pauses":[],"tags":{"team":"ci"},
"idle":[{"signal":"cpu","below":5,"value":0.8,"underSince":"2026-11-14T06:35:00.000Z","applies":true,"from":"default"},
 {"signal":"network","below":null,"value":null,"underSince":null,"applies":false,"reason":"off","from":"default"},
 {"signal":"metric","below":1,"value":3,"underSince":null,"applies":true,"from":"type"}],
"nap":{"eligibleAt":"2026-11-14T12:20:00.000Z","latestAt":"2026-11-14T12:35:00.000Z","inSeconds":4800},
"router":{"version":"0.42.0","build":"unknown"},"backends":[{"kind":"instance","id":"build-agent","status":null,"location":"europe-west1-b"}],"resourceMissing":true,
"settings":{"idleMinutes":120,"wakeTimeoutSeconds":600,"idleCpuPercent":5,"idleNetworkBytes":"off","idleMetric":"off"},
"settingsFrom":{"idleMinutes":"default","wakeTimeoutSeconds":"default","idleCpuPercent":"default","idleNetworkBytes":"default","idleMetric":"type"},
"type":{"name":"ci","from":"tags","missing":""},"schedule":null}`), &status)
	status[resourceKey] = resource
	return status
}

func (g *workloadsRig) standaloneAgent() {
	g.s.Lock()
	defer g.s.Unlock()
	g.s.Workloads["shop"], g.s.Workloads["build-agent"] = "AWAKE", "FAILED"
	g.s.WorkloadDocs["build-agent"] = map[string]any{"kind": "standalone", "resource": agentPath, "name": "build-agent", "tags": map[string]any{"team": "ci"},
		"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "build-agent", "id": "333"}}, "resourceMissing": true,
		"settings": map[string]any{"idleMinutes": 120, "wakeTimeoutSeconds": 600, "idleCpuPercent": 5, "idleNetworkBytes": "off", "idleMetric": "off"}}
	g.s.Statuses = map[string]map[string]any{"build-agent": standaloneStatus("resource", agentPath)}
}

// The list says each workload's kind (http for a row without one), --kind keeps one, and --json passes the router's keys.
func TestWorkloadsListSaysTheKindAndKeepsOne(t *testing.T) {
	g := newWorkloadsRig(t)
	g.standaloneAgent()
	code, out := g.call(t, WorkloadsList, workloadsEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	lines := strings.Split(out, "\n")
	if f := strings.Fields(lines[0]); len(f) < 4 || f[3] != "KIND" {
		t.Fatalf("header %q", lines[0])
	}
	if f := strings.Fields(lines[1]); f[1] != "build-agent" || f[3] != "standalone" {
		t.Fatalf("row %q", lines[1])
	}
	if f := strings.Fields(lines[2]); f[1] != "shop" || f[3] != "http" {
		t.Fatalf("row %q", lines[2])
	}
	r := workloadsEurope
	r.Kind = "standalone"
	if code, out = g.call(t, WorkloadsList, r); code != 0 || strings.Contains(out, "shop") || !strings.Contains(out, "1 workload") {
		t.Fatalf("--kind standalone: exit %d:\n%s", code, out)
	}
	r.Kind = "http"
	if code, out = g.call(t, WorkloadsList, r); code != 0 || strings.Contains(out, "build-agent") {
		t.Fatalf("--kind http: exit %d:\n%s", code, out)
	}
	r.Kind = "vm"
	if code, out = g.call(t, WorkloadsList, r); code != 2 || !strings.Contains(out, "--kind: give http or standalone") {
		t.Fatalf("--kind vm: exit %d:\n%s", code, out)
	}
	r = workloadsEurope
	r.JSON = true
	_, out = g.call(t, WorkloadsList, r)
	var list struct{ Workloads []map[string]any }
	if err := json.Unmarshal([]byte(out), &list); err != nil || list.Workloads[0]["kind"] != "standalone" || list.Workloads[0]["resource"] != agentPath {
		t.Fatalf("%v:\n%s", err, out)
	}
	if _, has := list.Workloads[1]["kind"]; has {
		t.Fatalf("an HTTP row with a kind:\n%s", out)
	}
}

// show of a standalone workload reads the router's status and prints it as dormeto aws workloads show prints the same
// workload, the cloud's own words aside: its kind, its resource (missing), its idle signals with their levels and last
// values, its nap, its kept failure and when it is retried; no URL.
func TestWorkloadsShowOfAStandaloneWorkloadIsAWSs(t *testing.T) {
	g := newWorkloadsRig(t)
	g.standaloneAgent()
	g.d.Now = func() time.Time { return time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC) }
	mark := len(g.s.Calls())
	r := workloadsEurope
	r.ID = "build-agent"
	code, out := g.call(t, WorkloadsShow, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.controllerCalls(mark); len(got) != 2 || got[1] != "POST /v1/actions" {
		t.Fatalf("calls %v", got)
	}
	// AWS's text of the same workload: its status as the manager passes it, with its ARN, account and cloud.
	aws := standaloneStatus("resourceArn", "arn:aws:ec2:eu-west-1:111122223333:instance/i-0b7c")
	aws["cloud"], aws["account"], aws["region"] = "aws", "111122223333", "eu-west-1"
	b, _ := json.Marshal(aws)
	var d fleet.WorkloadDetails
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	want := d.Text("prod-eu", time.UTC)
	// The cloud's own words: its router's name, cloud, account or project, region and resource.
	for _, w := range [][2]string{{"prod-eu/", "dmtr-dormeto-dev-europe-west1/"}, {"cloud           aws", "cloud           gcp"},
		{"account         111122223333", "project         dormeto-dev"}, {"region          eu-west-1", "region          europe-west1"},
		{"arn:aws:ec2:eu-west-1:111122223333:instance/i-0b7c", agentPath}} {
		want = strings.Replace(want, w[0], w[1], 1)
	}
	if out != want {
		t.Fatalf("gcp:\n%s\naws, the cloud's words replaced:\n%s", out, want)
	}
	for _, w := range []string{"kind            standalone", agentPath + " (resource missing)", "CPU      0.8, idle under 5", "nap      from",
		"failure         wake failed (permission): VM build-agent: Google refused the change",
		"retry           next attempt 2026-11-14 10:10 UTC, until it succeeds or a person naps it"} {
		contains(t, out, w)
	}
	if strings.Contains(out, "url") {
		t.Fatalf("a URL:\n%s", out)
	}
}

const (
	migPath = "projects/dormeto-dev/regions/europe-west1/instanceGroupManagers/batch"
	sqlPath = "projects/dormeto-dev/instances/report-db"
)

// newStandaloneRig is an installed router with the feature cloud-sql whose onboarding by resource finds the VM build-agent,
// the regional group batch and the Cloud SQL instances report-db and agent-db, none of them behind a backend service.
func newStandaloneRig(t *testing.T) *workloadsRig {
	t.Helper()
	g := newDatabaseRig(t)
	g.s.SQLInstances = map[string]string{"report-db": "europe-west3", "agent-db": "europe-west1"}
	g.s.Standalone = map[string]gcptest.StandaloneResource{
		agentPath: {Kind: "vm", Location: "europe-west1-b", Name: "build-agent", ID: "333"},
		migPath:   {Kind: "mig", Location: "europe-west1", Name: "batch", ID: "444"},
		sqlPath:   {Kind: "cloudsql", Name: "report-db"},
	}
	g.s.ServiceBackends = map[string][]string{"global/shop": {"https://www.googleapis.com/compute/v1/projects/dormeto-dev/zones/europe-west1-b/instanceGroups/web"}}
	g.s.GroupMembers = map[string][]string{"zones/europe-west1-b/instanceGroups/web": {"web-1"}}
	return g
}

func agentOnboarding() Request {
	r := workloadsEurope
	r.Resource, r.CloudSQL = agentPath, []string{"agent-db"}
	return r
}

// The onboarding by resource: the router's dry run with the resource in place of the URL map and the backend service, the
// project's backend services listed, the tag bound to the resource and its companions, then the onboarding; the ID is the
// resource's name. No call names a URL map or a backend service.
func TestWorkloadsOnboardByResourceDryRunThenTagThenOnboard(t *testing.T) {
	g := newStandaloneRig(t)
	mark := len(g.s.Calls())
	r := agentOnboarding()
	r.Idle = map[string]any{"idleCpuPercent": 3.0, "idleNetworkBytes": "off", "idleMetric": map[string]any{"type": "custom.googleapis.com/ci/queued", "aligner": "ALIGN_MAX", "below": 1.0}}
	r.Hibernate = ptr(true)
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.controllerCalls(mark); len(got) != 2 || got[0] != "POST /v1/workloads?dryRun=true" || got[1] != "POST /v1/workloads" {
		t.Fatalf("controller calls %v", got)
	}
	in := g.onboarding(t)
	want := map[string]any{"resource": agentPath, "id": "build-agent", "cloudSql": []any{"agent-db"},
		"settings": map[string]any{"hibernate": true, "idleCpuPercent": 3.0, "idleNetworkBytes": "off", "idleMetric": map[string]any{"type": "custom.googleapis.com/ci/queued", "aligner": "ALIGN_MAX", "below": 1.0}}}
	if !jsonEqual(in, want) {
		t.Fatalf("onboarding %v", in)
	}
	parents := []string{}
	for _, b := range g.bindings() {
		parents = append(parents, b.Parent)
	}
	if !slices.Equal(parents, []string{"//compute.googleapis.com/projects/" + g.s.ProjectNumber + "/zones/europe-west1-b/instances/333",
		"//sqladmin.googleapis.com/projects/dormeto-dev/instances/agent-db"}) {
		t.Fatalf("bindings %v", parents)
	}
	for _, c := range g.s.Calls()[mark:] {
		if strings.Contains(c.URL, "urlMaps") || strings.Contains(c.URL, "/backendServices/") || strings.Contains(c.URL, "forwardingRules") {
			t.Fatalf("a call of an HTTP workload's: %s %s", c.Method, c.URL)
		}
	}
	contains(t, out, "dmtr-dormeto-dev-europe-west1/build-agent: onboarded (AWAKE)", "  kind: standalone", "  resource: "+agentPath,
		"  tagged: instance build-agent (europe-west1-b)", "  tagged: cloudsql-instance agent-db")
	if g.s.WorkloadDocs["build-agent"]["kind"] != "standalone" {
		t.Fatalf("%v", g.s.WorkloadDocs["build-agent"])
	}
}

// A regional managed group's tag goes to its region's endpoint; a Cloud SQL instance as the resource is tagged as a companion is.
func TestWorkloadsOnboardByResourceTagsAGroupOrADatabase(t *testing.T) {
	for path, parent := range map[string]string{
		migPath: "//compute.googleapis.com/projects/{n}/regions/europe-west1/instanceGroupManagers/444",
		sqlPath: "//sqladmin.googleapis.com/projects/dormeto-dev/instances/report-db"} {
		g := newStandaloneRig(t)
		r := workloadsEurope
		r.Resource = path
		code, out := g.call(t, WorkloadsOnboard, r)
		parent = strings.Replace(parent, "{n}", g.s.ProjectNumber, 1)
		if b := g.bindings(); code != 0 || len(b) != 1 || b[0].Parent != parent {
			t.Fatalf("%s: exit %d, %v:\n%s", path, code, b, out)
		}
	}
}

// One form or the other: --resource, or --url-map and --backend-service; the HTTP form's own flags and the idle signals of the
// other form refused; a path that is none of the router's three; all before any call (exit 2).
func TestWorkloadsOnboardTakesOneFormOrTheOther(t *testing.T) {
	cases := map[string]struct {
		set  func(*Request)
		want string
	}{
		"both":          {func(r *Request) { r.Resource, r.URLMap, r.BackendService = agentPath, "m", "be" }, "--resource: give it or --url-map and --backend-service, not both"},
		"neither":       {func(r *Request) {}, "give --resource <path> (a standalone workload) or --url-map and --backend-service (an HTTP workload)"},
		"url map alone": {func(r *Request) { r.URLMap = "m" }, "--backend-service: needed"},
		"bad path":      {func(r *Request) { r.Resource = "projects/dormeto-dev/zones/europe-west1-b/disks/d" }, "--resource: \"projects/dormeto-dev/zones/europe-west1-b/disks/d\" is not the path of a Compute Engine VM (projects/<project>/zones/<zone>/instances/<name>)"},
		"load balancer": {func(r *Request) { r.Resource, r.LoadBalancer = agentPath, "global" }, "--load-balancer: an HTTP workload's: a standalone workload has no load balancer"},
		"request logs":  {func(r *Request) { r.Resource, r.RequestLogs = agentPath, true }, "--request-logs: an HTTP workload's"},
		"gke":           {func(r *Request) { r.Resource, r.GKECluster = agentPath, "europe-west1/c" }, "--gke-cluster: an HTTP workload's"},
		"idle without": {func(r *Request) {
			r.URLMap, r.BackendService, r.Idle = "m", "be", map[string]any{"idleCpuPercent": 5.0}
		}, "--idle-cpu, --idle-network and --idle-metric: a standalone workload's (give --resource)"},
		"unchecked alone": {func(r *Request) { r.URLMap, r.BackendService, r.Unchecked = "m", "be", true }, "--unchecked: a standalone workload's (give --resource)"},
		"long name": {func(r *Request) {
			r.Resource = "projects/dormeto-dev/zones/europe-west1-b/instances/a-very-long-build-agent-name-past-forty-chars"
		}, "--id: needed, the resource's name \"a-very-long-build-agent-name-past-forty-chars\" is not a valid workload ID"},
	}
	for name, c := range cases {
		g := newStandaloneRig(t)
		mark := len(g.s.Calls())
		r := workloadsEurope
		c.set(&r)
		code, out := g.call(t, WorkloadsOnboard, r)
		if code != 2 || !strings.Contains(out, c.want) || len(g.s.Calls()) != mark {
			t.Errorf("%s: exit %d, %d calls:\n%s", name, code, len(g.s.Calls())-mark, out)
		}
	}
}

// The router's reasons, as it says them; nothing tagged.
func TestWorkloadsOnboardByResourceSaysTheRoutersReasons(t *testing.T) {
	g := newStandaloneRig(t)
	g.s.StandaloneReasons = map[string][]string{agentPath: {"VM build-agent has a local SSD: a stop would lose its data"}}
	code, out := g.call(t, WorkloadsOnboard, agentOnboarding())
	if code != 1 || len(g.bindings()) != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Resource "+agentPath+" is not eligible:\n  - VM build-agent has a local SSD: a stop would lose its data\nNothing was changed.")
}

// The dry run says what would be tagged, and makes the backend-service check; nothing is bound.
func TestWorkloadsOnboardByResourceDryRun(t *testing.T) {
	g := newStandaloneRig(t)
	r := agentOnboarding()
	r.DryRun = true
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 0 || len(g.bindings()) != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Resource "+agentPath+" is eligible as standalone workload build-agent; onboarding would bind the tag dormeto=managed to:\n  instance build-agent (europe-west1-b)\n  cloudsql-instance agent-db\n")
	g.s.GroupMembers["zones/europe-west1-b/instanceGroups/web"] = []string{"web-1", "build-agent"}
	if code, out = g.call(t, WorkloadsOnboard, r); code != 1 || !strings.Contains(out, "backend service shop (global)") {
		t.Fatalf("the dry run's check: exit %d:\n%s", code, out)
	}
}

// The load-balancer check (the plan's ruling, as wide as the manager's): a VM or a group a backend service uses (through an
// instance group, managed or not, or a zonal NEG holding it), a group with target pools, a VM in a target pool or behind a
// target instance, is refused by name. Each is a case.
func TestWorkloadsOnboardByResourceRefusesWhatALoadBalancerUses(t *testing.T) {
	const compute = "https://www.googleapis.com/compute/v1/projects/dormeto-dev/"
	agentURL := compute + "zones/europe-west1-b/instances/build-agent"
	httpOne := ": it belongs to an HTTP workload: onboard its backend service."
	noBackendService := ": it serves a load balancer that uses no backend service, which Dormeto does not onboard."
	cases := map[string]struct {
		resource string
		set      func(*workloadsRig)
		want     string
	}{
		"group, global": {migPath, func(g *workloadsRig) {
			g.s.ServiceBackends["global/batch-be"] = []string{compute + "regions/europe-west1/instanceGroups/batch"}
		}, "Managed group batch (europe-west1) backs backend service batch-be (global)" + httpOne},
		"group, regional": {migPath, func(g *workloadsRig) {
			g.s.ServiceBackends["europe-west1/batch-be"] = []string{compute + "regions/europe-west1/instanceGroups/batch"}
		}, "Managed group batch (europe-west1) backs backend service batch-be (europe-west1)" + httpOne},
		"group, target pools": {migPath, func(g *workloadsRig) {
			g.s.GroupTargetPools = map[string][]string{"regions/europe-west1/instanceGroupManagers/batch": {compute + "regions/europe-west1/targetPools/batch-pool"}}
		}, "Managed group batch (europe-west1) feeds target pool batch-pool" + noBackendService},
		"vm, unmanaged group": {agentPath, func(g *workloadsRig) {
			g.s.GroupMembers["zones/europe-west1-b/instanceGroups/web"] = []string{"web-1", "build-agent"}
		},
			"VM build-agent (europe-west1-b) backs backend service shop (global), through instance group web" + httpOne},
		"vm, regional group": {agentPath, func(g *workloadsRig) {
			g.s.ServiceBackends["europe-west1/internal"] = []string{compute + "regions/europe-west1/instanceGroups/pool"}
			g.s.GroupMembers["regions/europe-west1/instanceGroups/pool"] = []string{"build-agent"}
		}, "VM build-agent (europe-west1-b) backs backend service internal (europe-west1), through instance group pool" + httpOne},
		"vm, NEG": {agentPath, func(g *workloadsRig) {
			g.s.ServiceBackends["europe-west1/api"] = []string{compute + "zones/europe-west1-b/networkEndpointGroups/api-neg"}
			g.s.NEGEndpoints = map[string][]string{"zones/europe-west1-b/networkEndpointGroups/api-neg": {"build-agent"}}
		}, "VM build-agent (europe-west1-b) backs backend service api (europe-west1), through network endpoint group api-neg" + httpOne},
		"vm, target pool": {agentPath, func(g *workloadsRig) {
			g.s.TargetPools = map[string][]string{"europe-west1/agents": {agentURL}}
		}, "VM build-agent (europe-west1-b) is in target pool agents (europe-west1)" + noBackendService},
		"vm, target instance": {agentPath, func(g *workloadsRig) {
			g.s.TargetInstances = map[string]string{"europe-west1-b/agent-ti": agentURL}
		}, "VM build-agent (europe-west1-b) is the VM of target instance agent-ti (europe-west1-b)" + noBackendService},
	}
	for name, c := range cases {
		g := newStandaloneRig(t)
		c.set(g)
		r := workloadsEurope
		r.Resource = c.resource
		code, out := g.call(t, WorkloadsOnboard, r)
		if code != 1 || !strings.Contains(out, c.want+"\nNothing was changed.") || len(g.bindings()) != 0 || len(g.s.Workloads) != 0 {
			t.Errorf("%s: exit %d:\n%s", name, code, out)
		}
	}
	// Not used: onboarded (the VM is in no group, pool or target instance; the group feeds none).
	for _, path := range []string{agentPath, migPath} {
		g := newStandaloneRig(t)
		g.s.TargetPools = map[string][]string{"europe-west1/web-pool": {compute + "zones/europe-west1-b/instances/web-1"}}
		r := workloadsEurope
		r.Resource = path
		if code, out := g.call(t, WorkloadsOnboard, r); code != 0 {
			t.Fatalf("%s, not used: exit %d:\n%s", path, code, out)
		}
	}
}

// A listing the caller may not make, or one that answers unreachable scopes, is not checked: the onboarding stops, naming the
// listings not made, unless --unchecked, which says them and what it risks and goes on. A use found stops it all the same.
func TestWorkloadsOnboardByResourceStopsWhenNotChecked(t *testing.T) {
	cases := map[string]struct {
		resource string
		set      func(*workloadsRig)
		want     string
	}{
		"backend services refused": {agentPath, func(g *workloadsRig) { g.s.Refused = map[string]bool{"backendServices": true} },
			"backend services (compute.backendServices.list, compute.regionBackendServices.list: "},
		"target pools refused": {agentPath, func(g *workloadsRig) { g.s.Refused = map[string]bool{"targetPools": true} },
			"target pools (compute.targetPools.list: "},
		"target instances refused": {agentPath, func(g *workloadsRig) { g.s.Refused = map[string]bool{"targetInstances": true} },
			"target instances (compute.targetInstances.list: "},
		"group members refused": {agentPath, func(g *workloadsRig) { g.s.Refused = map[string]bool{"listInstances": true} },
			"instance group web (zones/europe-west1-b) (compute.instanceGroups.list: "},
		"managed group refused": {migPath, func(g *workloadsRig) { g.s.Refused = map[string]bool{"instanceGroupManagers": true} },
			"managed group batch (compute.instanceGroupManagers.get: "},
		"unreachable": {agentPath, func(g *workloadsRig) {
			g.s.Unreachables = map[string][]string{"backendServices": {"regions/asia-east1"}}
		},
			"backend services (unreachable: regions/asia-east1)"},
	}
	for name, c := range cases {
		g := newStandaloneRig(t)
		c.set(g)
		r := workloadsEurope
		r.Resource = c.resource
		label := "VM build-agent (europe-west1-b)"
		if c.resource == migPath {
			label = "managed group batch (europe-west1)"
		}
		code, out := g.call(t, WorkloadsOnboard, r)
		if code != 1 || len(g.bindings()) != 0 || !strings.Contains(out, "Whether "+label+" serves a load balancer is not checked: these listings were not made:\n  - "+c.want) ||
			!strings.Contains(out, "Give --unchecked to onboard it unchecked: if it serves one, Dormeto stops it when its metrics are idle.\nNothing was changed.") {
			t.Errorf("%s: exit %d:\n%s", name, code, out)
			continue
		}
		r.Unchecked = true
		code, out = g.call(t, WorkloadsOnboard, r)
		if code != 0 || len(g.bindings()) != 1 || !strings.Contains(out, "! Not checked (--unchecked): "+c.want) ||
			!strings.Contains(out, "if "+label+" serves a load balancer, Dormeto stops it when its metrics are idle.") {
			t.Errorf("%s, --unchecked: exit %d:\n%s", name, code, out)
		}
	}
	// A use found in a listing made: refused, --unchecked or not.
	g := newStandaloneRig(t)
	g.s.Refused = map[string]bool{"targetPools": true}
	g.s.GroupMembers["zones/europe-west1-b/instanceGroups/web"] = []string{"build-agent"}
	r := workloadsEurope
	r.Resource, r.Unchecked = agentPath, true
	if code, out := g.call(t, WorkloadsOnboard, r); code != 1 || !strings.Contains(out, "backs backend service shop (global)") {
		t.Fatalf("found: exit %d:\n%s", code, out)
	}
}

// The idle signals on a workload's and a router's levels go to the router as given (Google Cloud's metric shape), and its
// refusals are printed as it says them.
func TestWorkloadsAndRouterSettingsSendTheIdleSignals(t *testing.T) {
	g, r := settingsRig(t)
	g.standaloneAgent()
	metric := map[string]any{"type": "custom.googleapis.com/ci/queued", "filter": `metric.labels.agent = "${resource}"`, "aligner": "ALIGN_MAX", "below": 1.0}
	r.IDs = []string{"build-agent"}
	r.Levels = map[string]any{"idleCpuPercent": 3.0, "idleNetworkBytes": "off", "idleMetric": metric}
	if code, out := g.call(t, WorkloadsSettings, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// The router's flat body, with the operator's identity beside it (by).
	if in := withoutBy(g.body(t, "PUT", controllerHost, "/v1/workloads/build-agent/settings")); !jsonEqual(in, r.Levels) {
		t.Fatalf("%v", in)
	}
	r.IDs = nil
	r.Levels = map[string]any{"defaults": map[string]any{"idleCpuPercent": nil, "idleMetric": "off"}}
	if code, out := g.call(t, RouterSettings, r); code != 0 {
		t.Fatalf("router: exit %d:\n%s", code, out)
	}
	if in := withoutBy(g.body(t, "PUT", controllerHost, "/v1/settings")); !jsonEqual(in, r.Levels) {
		t.Fatalf("%v", in)
	}
	g.s.SettingsProblem = "idleMetric: aligner must be ALIGN_MAX, ALIGN_MEAN or ALIGN_SUM"
	r.IDs = []string{"build-agent"}
	r.Levels = map[string]any{"idleMetric": map[string]any{"type": "t", "aligner": "ALIGN_MIN", "below": 1}}
	code, out := g.call(t, WorkloadsSettings, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "build-agent: ", "idleMetric: aligner must be ALIGN_MAX, ALIGN_MEAN or ALIGN_SUM")
}

func withoutBy(m map[string]any) map[string]any {
	delete(m, "by")
	return m
}
