package gcponboard

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/gcp/access"
	"github.com/Dormeto/cli/internal/gcp/gcptest"
)

// The design's §5, verbatim.
const countWarningLine = "  ! the idle check counts every request, scanners included: --request-logs reads the requests instead (Cloud Logging's price past its free tier)\n"

const (
	globalService   = "compute.googleapis.com/compute/v1/projects/dormeto-dev/global/backendServices/shop"
	regionalService = "compute.googleapis.com/compute/v1/projects/dormeto-dev/regions/europe-west1/backendServices/shop"
)

// logging sets the logConfig of backend service shop, global or in the router's region: nil, none.
func (g *workloadsRig) logging(location string, logConfig map[string]any) {
	g.s.Lock()
	defer g.s.Unlock()
	g.s.BackendServices[location+"/shop"] = &gcptest.BackendService{LogConfig: logConfig, Fingerprint: "fp-" + location}
}

func (g *workloadsRig) logConfig(location string) map[string]any {
	g.s.Lock()
	defer g.s.Unlock()
	return g.s.BackendServices[location+"/shop"].LogConfig
}

// patches are the PATCH calls of backend services since mark, as "<host><path>".
func (g *workloadsRig) patches(mark int) []string {
	var out []string
	for _, c := range g.s.Calls()[mark:] {
		if c.Method == "PATCH" && strings.Contains(c.Path, "/backendServices/") {
			out = append(out, c.Host+c.Path)
		}
	}
	return out
}

var shopWithLogs = func() Request {
	r := shopOnboarding
	r.RequestLogs = true
	return r
}()

// newLogsRig is newWorkloadsRig with a router that keeps an onboarding's requestLogs: its controller's health answers 0.40.0.
func newLogsRig(t *testing.T) *workloadsRig {
	t.Helper()
	g := newWorkloadsRig(t)
	g.s.HealthVersion = "0.40.0"
	return g
}

// The final review's Important 1: a router below 0.40.0 drops the onboarding's requestLogs, so the offboarding would never
// set the logs back. --request-logs is refused there before any change, the dry run's too. The version's core decides: a
// 0.39.9 and its dev builds are below.
func TestWorkloadsOnboardRequestLogsOnARouterBelow040IsRefusedBeforeAnyChange(t *testing.T) {
	for _, version := range []string{"0.37.0", "0.38.0", "0.39.0", "0.39.9", "0.39.9-dev.2"} {
		for _, dry := range []bool{false, true} {
			g := newWorkloadsRig(t)
			g.s.HealthVersion = version
			g.logging("global", map[string]any{"enable": true, "sampleRate": 0.5})
			r := shopWithLogs
			r.DryRun = dry
			mark := len(g.s.Calls())
			code, out := g.call(t, WorkloadsOnboard, r)
			if code != 2 {
				t.Fatalf("%s, dry run %t: exit %d:\n%s", version, dry, code, out)
			}
			contains(t, out, "--request-logs needs router 0.40.0 or later: dormeto gcp routers install --version "+embeddedRouterVersion()+"\n", "Nothing was changed.\n")
			if m := g.changes(mark); len(m) != 1 || len(g.bindings()) != 0 || len(g.patches(mark)) != 0 {
				t.Errorf("%s, dry run %t: changes %v, bindings %v", version, dry, m, g.bindings())
			}
			if got := g.controllerCalls(mark); !slices.Equal(got, []string{"POST /v1/workloads?dryRun=true", "GET /v1/health"}) {
				t.Errorf("%s, dry run %t: controller calls %v", version, dry, got)
			}
			if got := g.logConfig("global"); !jsonEqual(got, map[string]any{"enable": true, "sampleRate": 0.5}) {
				t.Errorf("%s, dry run %t: logConfig %v", version, dry, got)
			}
		}
	}
}

// Such a router counts every request whatever the logs: the warning that names --request-logs is not said.
func TestWorkloadsOnboardWithoutRequestLogsOnARouterBelow040DoesNotWarn(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.HealthVersion = "0.37.0"
	g.logging("global", map[string]any{"enable": true, "sampleRate": 0.5})
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 0 || g.s.Workloads["shop"] != "AWAKE" {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "--request-logs") || strings.Contains(out, "! the idle check") {
		t.Fatalf("warned:\n%s", out)
	}
}

// Its version unread, the flag stops before any change; without the flag, the warning stays.
func TestWorkloadsOnboardRequestLogsStopsWhenTheRoutersVersionIsNotRead(t *testing.T) {
	for _, flag := range []bool{true, false} {
		g := newLogsRig(t)
		g.logging("global", map[string]any{"enable": true, "sampleRate": 0.5})
		g.s.Refuse(gcptest.Refusal{Method: "GET", Host: controllerHost, Path: "/v1/health", Code: 503, Status: "UNAVAILABLE", Message: "down"})
		r := shopOnboarding
		r.RequestLogs = flag
		mark := len(g.s.Calls())
		code, out := g.call(t, WorkloadsOnboard, r)
		if !flag {
			if code != 0 || !strings.HasSuffix(out, countWarningLine) {
				t.Errorf("without the flag: exit %d:\n%s", code, out)
			}
			continue
		}
		if code != 1 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		contains(t, out, "The version of router dmtr-europe-west1 is not read, and --request-logs needs 0.40.0 or later: ", "down", "Nothing was changed.\n")
		if m := g.changes(mark); len(m) != 1 || len(g.bindings()) != 0 || len(g.patches(mark)) != 0 {
			t.Errorf("changes %v", m)
		}
	}
}

// A router at 0.40.0 or later takes the flag. The version's core decides, so the dev builds and release candidates of 0.40.0
// (package.json 0.40.0-dev.<n>, 0.40.0-rc.<n>), which SemVer orders below 0.40.0, take it too; one whose version is no
// release's is not refused.
func TestWorkloadsOnboardRequestLogsOnARouterAt040OrLaterTurnsTheLogsOn(t *testing.T) {
	for _, version := range []string{"0.40.0", "0.40.0-dev.1", "0.40.0-dev.3", "0.40.0-rc.1", "0.41.2", "dev"} {
		g := newWorkloadsRig(t)
		g.s.HealthVersion = version
		g.logging("global", map[string]any{"enable": true, "sampleRate": 0.5})
		code, out := g.call(t, WorkloadsOnboard, shopWithLogs)
		if code != 0 {
			t.Fatalf("%s: exit %d:\n%s", version, code, out)
		}
		contains(t, out, "  request logs: on, sample rate 1.0 (before: sample rate 0.5)\n")
	}
}

func TestWorkloadsOnboardRequestLogsTurnsOnTheLogsOfASampledBackendService(t *testing.T) {
	g := newLogsRig(t)
	g.logging("global", map[string]any{"enable": true, "sampleRate": 0.5, "optionalMode": "EXCLUDE_ALL_OPTIONAL"})
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, shopWithLogs)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// After the dry run and the tags, before the onboarding; its other fields kept, and the fingerprint read.
	var order []string
	for _, c := range g.s.Calls()[mark:] {
		switch {
		case c.Host == controllerHost && c.Method == "POST":
			order = append(order, "POST "+strings.TrimPrefix(c.URL, "https://"+controllerHost))
		case c.Method == "POST" && strings.HasSuffix(c.Path, "/v3/tagBindings"):
			order = append(order, "POST "+c.Host+c.Path)
		case c.Method == "PATCH":
			order = append(order, "PATCH "+c.Host+c.Path)
		}
	}
	want := []string{"POST /v1/workloads?dryRun=true", "POST " + zoneHost + "/v3/tagBindings", "POST " + regionHost + "/v3/tagBindings",
		"PATCH " + globalService, "POST /v1/workloads"}
	if !slices.Equal(order, want) {
		t.Fatalf("order:\n%s", strings.Join(order, "\n"))
	}
	patch := g.body(t, "PATCH", "compute.googleapis.com", "/compute/v1/projects/dormeto-dev/global/backendServices/shop")
	if !jsonEqual(patch, map[string]any{"fingerprint": "fp-global", "logConfig": map[string]any{"enable": true, "sampleRate": 1, "optionalMode": "EXCLUDE_ALL_OPTIONAL"}}) {
		t.Fatalf("patch %v", patch)
	}
	if got := g.logConfig("global"); !jsonEqual(got, map[string]any{"enable": true, "sampleRate": 1, "optionalMode": "EXCLUDE_ALL_OPTIONAL"}) {
		t.Fatalf("logConfig %v", got)
	}
	if got := g.onboarding(t)["requestLogs"]; !jsonEqual(got, map[string]any{"before": map[string]any{"enable": true, "sampleRate": 0.5}}) {
		t.Fatalf("requestLogs %v", got)
	}
	if _, there := g.body(t, "POST", controllerHost, "/v1/workloads?dryRun=true")["requestLogs"]; there {
		t.Fatal("the dry run is sent requestLogs")
	}
	contains(t, out, shopTarget+": onboarded", "  request logs: on, sample rate 1.0 (before: sample rate 0.5)\n")
	if strings.Contains(out, "! the idle check") {
		t.Fatalf("warned:\n%s", out)
	}
}

func TestWorkloadsOnboardRequestLogsOfARegionalLoadBalancerPatchesTheRegionalBackendService(t *testing.T) {
	g := newLogsRig(t)
	g.logging("europe-west1", nil)
	r := shopWithLogs
	r.LoadBalancer = "regional"
	mark := len(g.s.Calls())
	if code, out := g.call(t, WorkloadsOnboard, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.patches(mark); !slices.Equal(got, []string{regionalService}) {
		t.Fatalf("patches %v", got)
	}
	if got := g.onboarding(t)["requestLogs"]; !jsonEqual(got, map[string]any{"before": map[string]any{"enable": false, "sampleRate": 1}}) {
		t.Fatalf("requestLogs %v", got)
	}
}

// Already logging every request, with or without the flag: nothing changes, nothing is sent, nothing is warned of.
func TestWorkloadsOnboardLeavesABackendServiceLoggingEveryRequest(t *testing.T) {
	for _, logConfig := range []map[string]any{{"enable": true}, {"enable": true, "sampleRate": 1.0}} {
		for _, flag := range []bool{true, false} {
			g := newLogsRig(t)
			g.logging("global", logConfig)
			r := shopOnboarding
			r.RequestLogs = flag
			mark := len(g.s.Calls())
			code, out := g.call(t, WorkloadsOnboard, r)
			if code != 0 {
				t.Fatalf("%v %t: exit %d:\n%s", logConfig, flag, code, out)
			}
			if got := g.patches(mark); len(got) != 0 {
				t.Errorf("%v %t: patches %v", logConfig, flag, got)
			}
			if _, there := g.onboarding(t)["requestLogs"]; there {
				t.Errorf("%v %t: requestLogs sent", logConfig, flag)
			}
			if strings.Contains(out, "! the idle check") || strings.Contains(out, "request logs:") {
				t.Errorf("%v %t:\n%s", logConfig, flag, out)
			}
		}
	}
}

func TestWorkloadsOnboardWithoutRequestLogsWarnsAfterItsOutput(t *testing.T) {
	for name, logConfig := range map[string]map[string]any{"none": nil, "off": {"enable": false}, "sampled": {"enable": true, "sampleRate": 0.1}} {
		g := newLogsRig(t)
		g.logging("global", logConfig)
		mark := len(g.s.Calls())
		code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
		if code != 0 {
			t.Fatalf("%s: exit %d:\n%s", name, code, out)
		}
		if !strings.HasSuffix(out, countWarningLine) || !strings.Contains(out, shopTarget+": onboarded") {
			t.Errorf("%s:\n%s", name, out)
		}
		if got := g.patches(mark); len(got) != 0 {
			t.Errorf("%s: patches %v", name, got)
		}
		if _, there := g.onboarding(t)["requestLogs"]; there {
			t.Errorf("%s: requestLogs sent", name)
		}
	}
}

func TestWorkloadsOnboardJSONWarnsOnTheErrorStream(t *testing.T) {
	g := newLogsRig(t)
	g.logging("global", nil)
	r := shopOnboarding
	r.JSON = true
	code, out, stderr := g.split(t, WorkloadsOnboard, r)
	var w map[string]any
	if err := json.Unmarshal(byTarget(t, out, shopTarget), &w); code != 0 || err != nil || w["workloadId"] != "shop" {
		t.Fatalf("%d %v:\n%s", code, err, out)
	}
	// The onboarding's lines, then the warning.
	if !strings.HasPrefix(stderr, shopTarget+": onboarded (AWAKE)\n") || !strings.HasSuffix(stderr, countWarningLine) {
		t.Fatalf("stderr %q", stderr)
	}
}

func TestWorkloadsOnboardDryRunSaysItWouldTurnTheRequestLogsOnAndChangesNothing(t *testing.T) {
	g := newLogsRig(t)
	g.logging("global", nil)
	r := shopWithLogs
	r.DryRun = true
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Onboarding would turn on the request logs of backend service shop: sample rate 1.0 (now: off)\n")
	if m := g.changes(mark); len(m) != 1 {
		t.Fatalf("changes %v", m)
	}
}

func TestWorkloadsOnboardStopsBeforeAnyChangeWhenTheBackendServiceIsNotRead(t *testing.T) {
	g := newLogsRig(t)
	g.s.Refuse(gcptest.Refusal{Method: "GET", Host: "compute.googleapis.com", Path: "/compute/v1/projects/dormeto-dev/global/backendServices/shop",
		Code: 403, Status: "PERMISSION_DENIED", Message: "Required 'compute.backendServices.get' permission"})
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, shopWithLogs)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "The request logs of backend service shop are not read: ", "compute.backendServices.get", "Nothing was changed.")
	if m := g.changes(mark); len(m) != 1 || len(g.bindings()) != 0 {
		t.Fatalf("changes %v", m)
	}
}

// Without the flag, the read decides the warning alone: a failed read is a notice, and the onboarding goes on.
func TestWorkloadsOnboardWithoutRequestLogsGoesOnWhenTheBackendServiceIsNotRead(t *testing.T) {
	g := newLogsRig(t)
	g.s.Refuse(gcptest.Refusal{Method: "GET", Host: "compute.googleapis.com", Path: "/compute/v1/projects/dormeto-dev/global/backendServices/shop",
		Code: 500, Status: "INTERNAL", Message: "boom"})
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 0 || g.s.Workloads["shop"] != "AWAKE" {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.HasSuffix(out, "  ! the request logs of backend service shop are not read: google: 500 INTERNAL: boom\n") || strings.Contains(out, "! the idle check") {
		t.Fatalf("%s", out)
	}
}

// The design's §5 and the controller's ruling: an onboarding without --request-logs asks the backend service's read alone of
// the step, as install-router's image step is asked with --image alone; with the flag, the whole step.
func TestWorkloadsOnboardAsksTheRequestLogsUpdateWithItsFlagAlone(t *testing.T) {
	update := []string{"compute.backendServices.update", "compute.globalOperations.get", "compute.regionBackendServices.update", "compute.regionOperations.get"}
	for _, flag := range []bool{false, true} {
		g := newLogsRig(t)
		g.logging("global", nil)
		g.s.HoldsAll = false
		g.s.Held = map[string]bool{}
		for _, p := range access.Needs("onboard") {
			g.s.Held[p] = !slices.Contains(update, p)
		}
		r := shopOnboarding
		r.RequestLogs = flag
		code, out := g.call(t, WorkloadsOnboard, r)
		switch {
		case !flag && (code != 0 || g.s.Workloads["shop"] != "AWAKE"):
			t.Errorf("without the flag: exit %d:\n%s", code, out)
		case flag && (code != 1 || !strings.Contains(out, "  ✗ onboard: missing "+strings.Join(update, ", ")+"\n") || len(g.s.Workloads) != 0):
			t.Errorf("with the flag: exit %d:\n%s", code, out)
		}
	}
	// The read is asked without the flag too: it decides the warning.
	g := newLogsRig(t)
	g.s.HoldsAll = false
	g.s.Held = map[string]bool{}
	for _, p := range access.Needs("onboard") {
		g.s.Held[p] = p != "compute.regionBackendServices.get"
	}
	if code, out := g.call(t, WorkloadsOnboard, shopOnboarding); code != 1 || !strings.Contains(out, "  ✗ onboard: missing compute.regionBackendServices.get\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// As a tag binding is: the onboarding refused after the PATCH, the logConfig is set back.
func TestWorkloadsOnboardRefusedSetsTheRequestLogsBack(t *testing.T) {
	g := newLogsRig(t)
	g.logging("global", map[string]any{"enable": false, "optionalMode": "EXCLUDE_ALL_OPTIONAL"})
	g.s.OnboardingRefused = refusedOnboarding
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, shopWithLogs)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, refusedOnboarding, "The request logs of backend service shop are set back (off).", "fix the refusal")
	if got := g.patches(mark); len(got) != 2 {
		t.Fatalf("patches %v", got)
	}
	if got := g.logConfig("global"); got["enable"] != false || got["optionalMode"] != "EXCLUDE_ALL_OPTIONAL" {
		t.Fatalf("logConfig %v", got)
	}
	if len(g.bindings()) != 0 {
		t.Fatalf("bindings %v", g.bindings())
	}
}

func TestWorkloadsOnboardRefusedPatchUndoesTheTags(t *testing.T) {
	g := newLogsRig(t)
	g.logging("global", nil)
	g.s.Refuse(gcptest.Refusal{Method: "PATCH", Host: "compute.googleapis.com", Path: "/compute/v1/projects/dormeto-dev/global/backendServices/shop",
		Code: 403, Status: "PERMISSION_DENIED", Message: "Required 'compute.backendServices.update' permission"})
	code, out := g.call(t, WorkloadsOnboard, shopWithLogs)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "the request logs of backend service shop are not turned on: ", "compute.backendServices.update", "The tag bindings this run made are removed")
	if len(g.bindings()) != 0 || len(g.s.Workloads) != 0 {
		t.Fatalf("bindings %v workloads %v", g.bindings(), g.s.Workloads)
	}
	if strings.Contains(out, "set back") {
		t.Fatalf("set back what was not changed:\n%s", out)
	}
}

// The update taken, its operation not read (compute.globalOperations.get is the manifest's for that): the logs may be on, and
// are set back with the tags.
func TestWorkloadsOnboardSetsTheRequestLogsBackWhenTheirUpdatesOperationIsNotRead(t *testing.T) {
	g := newLogsRig(t)
	g.logging("global", nil)
	g.s.Refuse(gcptest.Refusal{Method: "GET", Host: "www.googleapis.com", Path: "/compute/v1/projects/dormeto-dev/global/operations/",
		Times: 1, Code: 403, Status: "PERMISSION_DENIED", Message: "Required 'compute.globalOperations.get' permission"})
	code, out := g.call(t, WorkloadsOnboard, shopWithLogs)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "the request logs of backend service shop are not turned on: ", "compute.globalOperations.get",
		"The request logs of backend service shop are set back (off).", "The tag bindings this run made are removed")
	if got := g.logConfig("global"); got["enable"] != false {
		t.Fatalf("logConfig %v", got)
	}
	if len(g.s.Workloads) != 0 {
		t.Fatalf("workloads %v", g.s.Workloads)
	}
}

// hook runs then once, after the first answered call that match tells.
type hook struct {
	next  http.RoundTripper
	match func(*http.Request) bool
	then  func()
	done  bool
}

func (h *hook) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := h.next.RoundTrip(req)
	if err == nil && !h.done && h.match(req) {
		h.done = true
		h.then()
	}
	return res, err
}

func (g *workloadsRig) after(match func(*http.Request) bool, then func()) {
	client := *g.d.Google.HTTP
	client.Transport = &hook{next: client.Transport, match: match, then: then}
	g.d.Google.HTTP = &client
}

// patchOperation is the poll of the backend service's update: the PATCH is done, the onboarding not sent yet.
func patchOperation(req *http.Request) bool {
	return req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/global/operations/")
}

func (g *workloadsRig) onboardingFile() string {
	return filepath.Join(g.cache, "dormeto", "gcp", "dormeto-dev-europe-west1-onboard-shop.json")
}

// onboardingsSince are the bodies of the onboardings (not their dry runs) since mark.
func (g *workloadsRig) onboardingsSince(t *testing.T, mark int) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, c := range g.s.Calls()[mark:] {
		if c.Method == "POST" && c.URL == "https://"+controllerHost+"/v1/workloads" {
			var m map[string]any
			if err := json.Unmarshal([]byte(c.Body), &m); err != nil {
				t.Fatalf("%s: %v", c.Body, err)
			}
			out = append(out, m)
		}
	}
	return out
}

// The review's Important 1: a run that stopped after the PATCH, the controller's answer lost before it onboarded anything; the
// rerun finds the logs on, and sends the before the first run kept in the cache.
func TestWorkloadsOnboardRerunAfterALostAnswerSendsTheBeforeTheFirstRunFound(t *testing.T) {
	g := newLogsRig(t)
	g.logging("global", map[string]any{"enable": true, "sampleRate": 0.5})
	g.after(patchOperation, func() {
		g.s.Refuse(gcptest.Refusal{Method: "POST", Host: controllerHost, Path: "/v1/workloads", Times: 1, Code: 502, Message: "bad gateway"})
	})
	code, out := g.call(t, WorkloadsOnboard, shopWithLogs)
	if code != 1 || len(g.s.Workloads) != 0 {
		t.Fatalf("exit %d, workloads %v:\n%s", code, g.s.Workloads, out)
	}
	contains(t, out, "the workload may be onboarded", "The request logs of backend service shop stay on (before: sample rate 0.5).")
	if _, err := os.Stat(g.onboardingFile()); err != nil {
		t.Fatalf("what the PATCH changed is not kept for a rerun: %v", err)
	}
	mark := len(g.s.Calls())
	code, out = g.call(t, WorkloadsOnboard, shopWithLogs)
	if code != 0 || g.s.Workloads["shop"] != "AWAKE" {
		t.Fatalf("rerun: exit %d:\n%s", code, out)
	}
	if got := g.patches(mark); len(got) != 0 {
		t.Fatalf("patches %v", got)
	}
	sent := g.onboardingsSince(t, mark)
	if len(sent) != 1 || !jsonEqual(sent[0]["requestLogs"], map[string]any{"before": map[string]any{"enable": true, "sampleRate": 0.5}}) {
		t.Fatalf("onboardings %v", sent)
	}
	contains(t, out, "  request logs: on, sample rate 1.0 (before: sample rate 0.5)\n")
	if _, err := os.Stat(g.onboardingFile()); !os.IsNotExist(err) {
		t.Fatalf("the file stays: %v", err)
	}
	// And the offboarding sets them back.
	if code, out := g.call(t, WorkloadsOffboard, confirmed(offboardShop)); code != 0 {
		t.Fatalf("offboard: exit %d:\n%s", code, out)
	}
	if got := g.logConfig("global"); !jsonEqual(got, map[string]any{"enable": true, "sampleRate": 0.5}) {
		t.Fatalf("logConfig %v", got)
	}
}

// The answer lost after the controller onboarded: the controller holds the before, the offboarding sets it back, and the cache's
// file goes with the workload.
func TestWorkloadsOffboardOfAnOnboardingWhoseAnswerWasLostForgetsWhatTheCacheKept(t *testing.T) {
	g := newLogsRig(t)
	g.logging("global", nil)
	g.s.AnswerOnboarding = func(w http.ResponseWriter) { gcptest.Fail(w, 502, "", "bad gateway") }
	if code, out := g.call(t, WorkloadsOnboard, shopWithLogs); code != 1 || g.s.Workloads["shop"] != "AWAKE" {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.s.AnswerOnboarding = nil
	if code, out := g.call(t, WorkloadsOffboard, confirmed(offboardShop)); code != 0 {
		t.Fatalf("offboard: exit %d:\n%s", code, out)
	}
	if got := g.logConfig("global"); got["enable"] != false {
		t.Fatalf("logConfig %v", got)
	}
	if _, err := os.Stat(g.onboardingFile()); !os.IsNotExist(err) {
		t.Fatalf("the file stays: %v", err)
	}
}

// Without the flag, a rerun still sends what Dormeto's earlier run changed, and warns of nothing: the logs are on.
func TestWorkloadsOnboardRerunWithoutTheFlagSendsTheBeforeOfAnEarlierRun(t *testing.T) {
	g := newLogsRig(t)
	g.logging("global", nil)
	g.after(patchOperation, func() {
		g.s.Refuse(gcptest.Refusal{Method: "POST", Host: controllerHost, Path: "/v1/workloads", Times: 1, Code: 502, Message: "bad gateway"})
	})
	if code, out := g.call(t, WorkloadsOnboard, shopWithLogs); code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 0 || strings.Contains(out, "! the idle check") {
		t.Fatalf("rerun: exit %d:\n%s", code, out)
	}
	if sent := g.onboardingsSince(t, mark); len(sent) != 1 || !jsonEqual(sent[0]["requestLogs"], map[string]any{"before": map[string]any{"enable": false, "sampleRate": 1}}) {
		t.Fatalf("onboardings %v", sent)
	}
}

// The review's Minor 1: an interrupt after the PATCH undoes it, and sends nothing.
func TestWorkloadsOnboardInterruptedAfterThePatchSetsTheLogsBackAndSendsNothing(t *testing.T) {
	g := newLogsRig(t)
	g.logging("global", map[string]any{"enable": false})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.after(patchOperation, cancel)
	mark := len(g.s.Calls())
	g.out.Reset()
	code := WorkloadsOnboard(ctx, g.d, shopWithLogs)
	out := g.out.String()
	if code != 130 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, notSent, "The request logs of backend service shop are set back (off).", "The tag bindings this run made are removed")
	if strings.Contains(out, "may be onboarded") || len(g.onboardingsSince(t, mark)) != 0 {
		t.Fatalf("the onboarding is said sent:\n%s", out)
	}
	if got := g.logConfig("global"); got["enable"] != false {
		t.Fatalf("logConfig %v", got)
	}
	if _, err := os.Stat(g.onboardingFile()); !os.IsNotExist(err) {
		t.Fatalf("the file stays: %v", err)
	}
}

// Interrupted after the PATCH, the logs not set back: the cache keeps what was found, and the rerun sends it.
func TestWorkloadsOnboardInterruptedAfterThePatchKeepsTheBeforeWhenTheLogsAreNotSetBack(t *testing.T) {
	g := newLogsRig(t)
	g.logging("global", map[string]any{"enable": true, "sampleRate": 0.25})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.after(patchOperation, func() {
		cancel()
		g.s.Refuse(gcptest.Refusal{Method: "PATCH", Host: "compute.googleapis.com", Path: "/compute/v1/projects/dormeto-dev/global/backendServices/shop",
			Times: 1, Code: 403, Status: "PERMISSION_DENIED", Message: "Required 'compute.backendServices.update' permission"})
	})
	g.out.Reset()
	if code := WorkloadsOnboard(ctx, g.d, shopWithLogs); code != 130 {
		t.Fatalf("exit %d:\n%s", code, g.out.String())
	}
	contains(t, g.out.String(), "The request logs of backend service shop are not set back (sample rate 0.25): ")
	if _, err := os.Stat(g.onboardingFile()); err != nil {
		t.Fatalf("the before is not kept for a rerun: %v", err)
	}
	mark := len(g.s.Calls())
	if code, out := g.call(t, WorkloadsOnboard, shopWithLogs); code != 0 {
		t.Fatalf("rerun: exit %d:\n%s", code, out)
	}
	if sent := g.onboardingsSince(t, mark); len(sent) != 1 || !jsonEqual(sent[0]["requestLogs"], map[string]any{"before": map[string]any{"enable": true, "sampleRate": 0.25}}) {
		t.Fatalf("onboardings %v", sent)
	}
}

// The controller's ruling on the review's Minor 4: the update names the fingerprint of a read just before it, not of the read
// before the tags and a cluster's prompt.
func TestWorkloadsOnboardPatchesWithTheFingerprintReadLast(t *testing.T) {
	g := newLogsRig(t)
	g.logging("global", nil)
	g.after(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/global/backendServices/shop")
	}, func() {
		g.s.Lock()
		g.s.BackendServices["global/shop"].Fingerprint = "fp-later"
		g.s.Unlock()
	})
	code, out := g.call(t, WorkloadsOnboard, shopWithLogs)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if patch := g.body(t, "PATCH", "compute.googleapis.com", "/compute/v1/projects/dormeto-dev/global/backendServices/shop"); patch["fingerprint"] != "fp-later" {
		t.Fatalf("patch %v", patch)
	}
}

// onboardedWithLogs onboards shop with --request-logs, its backend service logging at 0.5 before.
func (g *workloadsRig) onboardedWithLogs(t *testing.T) {
	t.Helper()
	g.logging("global", map[string]any{"enable": true, "sampleRate": 0.5})
	if code, out := g.call(t, WorkloadsOnboard, shopWithLogs); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

var offboardShop = func() Request {
	r := workloadsEurope
	r.ID = "shop"
	return r
}()

// The controller's ruling: the offboarding asks the requestLogs step only of a workload whose onboarding turned the logs on,
// as read from the controller before any change; another is offboarded without those permissions, and its backend service is
// not read.
func TestWorkloadsOffboardAsksTheRequestLogsStepOfAWorkloadWhoseLogsDormetoTurnedOnAlone(t *testing.T) {
	step := []string{"compute.backendServices.get", "compute.backendServices.update", "compute.globalOperations.get",
		"compute.regionBackendServices.get", "compute.regionBackendServices.update", "compute.regionOperations.get"}
	withheld := func(g *workloadsRig) {
		g.s.HoldsAll = false
		g.s.Held = map[string]bool{}
		for _, p := range access.Needs("offboard") {
			g.s.Held[p] = !slices.Contains(step, p)
		}
	}
	// Onboarded without --request-logs: offboarded, nothing of the step asked nor read.
	g := newLogsRig(t)
	if code, out := g.call(t, WorkloadsOnboard, shopOnboarding); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	withheld(g)
	mark := len(g.s.Calls())
	if code, out := g.call(t, WorkloadsOffboard, confirmed(offboardShop)); code != 0 || len(g.s.Workloads) != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, c := range g.s.Calls()[mark:] {
		if strings.Contains(c.Path, "/backendServices/") {
			t.Fatalf("the backend service is read: %s %s", c.Method, c.URL)
		}
	}
	// Onboarded with --request-logs: refused before any change, the step's permissions named.
	g = newLogsRig(t)
	g.onboardedWithLogs(t)
	withheld(g)
	mark = len(g.s.Calls())
	code, out := g.call(t, WorkloadsOffboard, confirmed(offboardShop))
	if code != 1 || !strings.Contains(out, "  ✗ offboard: missing "+strings.Join(step, ", ")+"\n") || !strings.Contains(out, "Nothing was changed.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if m := g.changes(mark); len(m) != 0 || g.s.Workloads["shop"] != "AWAKE" {
		t.Fatalf("changes %v", m)
	}
}

func TestWorkloadsOffboardSetsTheRequestLogsBackAfterTheController(t *testing.T) {
	g := newLogsRig(t)
	g.onboardedWithLogs(t)
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOffboard, confirmed(offboardShop))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	deleted, patched := -1, -1
	for i, c := range g.s.Calls()[mark:] {
		switch {
		case c.Method == "DELETE" && c.Host == controllerHost:
			deleted = i
		case c.Method == "PATCH" && strings.Contains(c.Path, "/backendServices/"):
			patched = i
		}
	}
	if deleted < 0 || patched < deleted {
		t.Fatalf("the controller's delete at %d, the PATCH at %d", deleted, patched)
	}
	if got := g.logConfig("global"); !jsonEqual(got, map[string]any{"enable": true, "sampleRate": 0.5}) {
		t.Fatalf("logConfig %v", got)
	}
	contains(t, out, shopTarget+": offboarded", "  restored: the request logs of backend service shop (sample rate 0.5)\n")
	if _, err := os.Stat(g.offboardFile()); !os.IsNotExist(err) {
		t.Fatalf("the file stays: %v", err)
	}
}

func TestWorkloadsOffboardLeavesRequestLogsChangedSinceAndSaysSo(t *testing.T) {
	g := newLogsRig(t)
	g.onboardedWithLogs(t)
	g.logging("global", map[string]any{"enable": true, "sampleRate": 0.2})
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOffboard, confirmed(offboardShop))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.patches(mark); len(got) != 0 {
		t.Fatalf("patches %v", got)
	}
	if got := g.logConfig("global"); !jsonEqual(got, map[string]any{"enable": true, "sampleRate": 0.2}) {
		t.Fatalf("logConfig %v", got)
	}
	contains(t, out, "  ! the request logs of backend service shop changed since the onboarding turned them on: left as they are (before it: sample rate 0.5)\n")
}

func TestWorkloadsOffboardJSONSaysTheRequestLogsRestored(t *testing.T) {
	g := newLogsRig(t)
	g.onboardedWithLogs(t)
	r := offboardShop
	r.JSON = true
	code, out, _ := g.split(t, WorkloadsOffboard, confirmed(r))
	var got struct{ Restored []string }
	if err := json.Unmarshal(byTarget(t, out, shopTarget), &got); code != 0 || err != nil || !slices.Equal(got.Restored, []string{"the request logs of backend service shop (sample rate 0.5)"}) {
		t.Fatalf("%d %v:\n%s", code, err, out)
	}
}

// A stop after the controller's delete: the rerun finds the workload gone, and sets the logs back from what the cache kept.
func TestWorkloadsOffboardRerunSetsTheRequestLogsBackFromTheCache(t *testing.T) {
	g := newLogsRig(t)
	g.onboardedWithLogs(t)
	g.s.Refuse(gcptest.Refusal{Method: "PATCH", Host: "compute.googleapis.com", Path: "/compute/v1/projects/dormeto-dev/global/backendServices/shop",
		Times: 1, Code: 500, Status: "INTERNAL", Message: "boom"})
	code, out := g.call(t, WorkloadsOffboard, confirmed(offboardShop))
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "workload shop is offboarded, but the request logs of backend service shop are not set back: ", "boom", "run the same command again")
	if len(g.s.Workloads) != 0 {
		t.Fatalf("workloads %v", g.s.Workloads)
	}
	if _, err := os.Stat(g.offboardFile()); err != nil {
		t.Fatalf("the request logs are not kept for a rerun: %v", err)
	}
	code, out = g.call(t, WorkloadsOffboard, confirmed(offboardShop))
	if code != 0 {
		t.Fatalf("rerun: exit %d:\n%s", code, out)
	}
	if got := g.logConfig("global"); !jsonEqual(got, map[string]any{"enable": true, "sampleRate": 0.5}) {
		t.Fatalf("logConfig %v", got)
	}
	if _, err := os.Stat(g.offboardFile()); !os.IsNotExist(err) {
		t.Fatalf("the file stays: %v", err)
	}
}

// An older CLI's file holds the backends alone, as a list: a rerun still finishes from it.
func TestWorkloadsOffboardRerunReadsAnOlderCLIsFile(t *testing.T) {
	g := newLogsRig(t)
	value := g.tagValue(t)
	g.s.TagBindings = []gcptest.TagBinding{{Parent: vmParent, TagValue: value}}
	if err := os.MkdirAll(strings.TrimSuffix(g.offboardFile(), "/dormeto-dev-europe-west1-offboard-shop.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.offboardFile(), []byte(`[{"kind":"vm","location":"europe-west1-b","name":"web-1","id":"111"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := g.call(t, WorkloadsOffboard, confirmed(offboardShop))
	if code != 0 || len(g.bindings()) != 0 {
		t.Fatalf("exit %d, bindings %v:\n%s", code, g.bindings(), out)
	}
}

func TestUninstallForceSetsTheRequestLogsBack(t *testing.T) {
	g := newLogsRig(t)
	g.onboardedWithLogs(t)
	r := uninstallEurope
	r.Force = true
	code, out := g.uninstall(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.logConfig("global"); !jsonEqual(got, map[string]any{"enable": true, "sampleRate": 0.5}) {
		t.Fatalf("logConfig %v", got)
	}
	contains(t, out, "  workload shop: restored the request logs of backend service shop (sample rate 0.5)\n")
}

// The workloads an uninstall --force offboards are read after its checks: it asks the whole requestLogs step.
func TestUninstallForceAsksTheRequestLogsStep(t *testing.T) {
	g := routerInstalled(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.HoldsAll = false
	g.s.Held = map[string]bool{}
	for _, c := range []string{"uninstall", "offboard"} {
		for _, p := range access.Needs(c) {
			g.s.Held[p] = p != "compute.backendServices.update"
		}
	}
	mark := len(g.s.Calls())
	code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, Force: true})
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ offboard: missing compute.backendServices.update\n", "Nothing was changed.\n")
	if m := g.changes(mark); len(m) != 0 {
		t.Fatalf("changes: %v", m)
	}
}

func TestWorkloadsShowSaysWhatTheIdleCheckReads(t *testing.T) {
	for reads, want := range map[string]string{"request logs": "request logs", "request count": "request count", "": ""} {
		g := newLogsRig(t)
		g.s.Workloads["shop"] = "AWAKE"
		g.s.WorkloadDocs["shop"] = map[string]any{"backendService": "shop"}
		if reads != "" {
			g.s.WorkloadDocs["shop"]["idleReads"] = reads
		}
		code, out := g.call(t, WorkloadsShow, offboardShop)
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if want == "" && strings.Contains(out, "idle check") || want != "" && !hasRow(out, "idle check", want) {
			t.Errorf("%q:\n%s", reads, out)
		}
	}
}
