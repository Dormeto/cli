package gcponboard

import (
	"bytes"
	"encoding/json"
	"os"
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

func TestWorkloadsOnboardRequestLogsTurnsOnTheLogsOfASampledBackendService(t *testing.T) {
	g := newWorkloadsRig(t)
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
	contains(t, out, "Onboarded workload shop", "  request logs: on, sample rate 1.0 (before: sample rate 0.5)\n")
	if strings.Contains(out, "! the idle check") {
		t.Fatalf("warned:\n%s", out)
	}
}

func TestWorkloadsOnboardRequestLogsOfARegionalLoadBalancerPatchesTheRegionalBackendService(t *testing.T) {
	g := newWorkloadsRig(t)
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
			g := newWorkloadsRig(t)
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
		g := newWorkloadsRig(t)
		g.logging("global", logConfig)
		mark := len(g.s.Calls())
		code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
		if code != 0 {
			t.Fatalf("%s: exit %d:\n%s", name, code, out)
		}
		if !strings.HasSuffix(out, countWarningLine) || !strings.Contains(out, "Onboarded workload shop") {
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
	g := newWorkloadsRig(t)
	g.logging("global", nil)
	var stderr bytes.Buffer
	g.d.Err = &stderr
	r := shopOnboarding
	r.JSON = true
	code, out := g.call(t, WorkloadsOnboard, r)
	var w map[string]any
	if err := json.Unmarshal([]byte(out), &w); code != 0 || err != nil || w["id"] != "shop" {
		t.Fatalf("%d %v:\n%s", code, err, out)
	}
	if stderr.String() != countWarningLine {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestWorkloadsOnboardDryRunSaysItWouldTurnTheRequestLogsOnAndChangesNothing(t *testing.T) {
	g := newWorkloadsRig(t)
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
	g := newWorkloadsRig(t)
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
	g := newWorkloadsRig(t)
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
		g := newWorkloadsRig(t)
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
	g := newWorkloadsRig(t)
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
	g := newWorkloadsRig(t)
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
	g := newWorkloadsRig(t)
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
	g := newWorkloadsRig(t)
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

func TestWorkloadsOffboardSetsTheRequestLogsBackAfterTheController(t *testing.T) {
	g := newWorkloadsRig(t)
	g.onboardedWithLogs(t)
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOffboard, offboardShop)
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
	contains(t, out, "Offboarded workload shop", "  restored: the request logs of backend service shop (sample rate 0.5)\n")
	if _, err := os.Stat(g.offboardFile()); !os.IsNotExist(err) {
		t.Fatalf("the file stays: %v", err)
	}
}

func TestWorkloadsOffboardLeavesRequestLogsChangedSinceAndSaysSo(t *testing.T) {
	g := newWorkloadsRig(t)
	g.onboardedWithLogs(t)
	g.logging("global", map[string]any{"enable": true, "sampleRate": 0.2})
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOffboard, offboardShop)
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
	g := newWorkloadsRig(t)
	g.onboardedWithLogs(t)
	r := offboardShop
	r.JSON = true
	code, out := g.call(t, WorkloadsOffboard, r)
	var got struct{ Restored []string }
	if err := json.Unmarshal([]byte(out), &got); code != 0 || err != nil || !slices.Equal(got.Restored, []string{"the request logs of backend service shop (sample rate 0.5)"}) {
		t.Fatalf("%d %v:\n%s", code, err, out)
	}
}

// A stop after the controller's delete: the rerun finds the workload gone, and sets the logs back from what the cache kept.
func TestWorkloadsOffboardRerunSetsTheRequestLogsBackFromTheCache(t *testing.T) {
	g := newWorkloadsRig(t)
	g.onboardedWithLogs(t)
	g.s.Refuse(gcptest.Refusal{Method: "PATCH", Host: "compute.googleapis.com", Path: "/compute/v1/projects/dormeto-dev/global/backendServices/shop",
		Times: 1, Code: 500, Status: "INTERNAL", Message: "boom"})
	code, out := g.call(t, WorkloadsOffboard, offboardShop)
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
	code, out = g.call(t, WorkloadsOffboard, offboardShop)
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
	g := newWorkloadsRig(t)
	value := g.tagValue(t)
	g.s.TagBindings = []gcptest.TagBinding{{Parent: vmParent, TagValue: value}}
	if err := os.MkdirAll(strings.TrimSuffix(g.offboardFile(), "/dormeto-dev-europe-west1-offboard-shop.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.offboardFile(), []byte(`[{"kind":"vm","location":"europe-west1-b","name":"web-1","id":"111"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := g.call(t, WorkloadsOffboard, offboardShop)
	if code != 0 || len(g.bindings()) != 0 {
		t.Fatalf("exit %d, bindings %v:\n%s", code, g.bindings(), out)
	}
}

func TestUninstallForceSetsTheRequestLogsBack(t *testing.T) {
	g := newWorkloadsRig(t)
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

func TestWorkloadsShowSaysWhatTheIdleCheckReads(t *testing.T) {
	for reads, want := range map[string]string{"request logs": "  idle check: request logs\n", "request count": "  idle check: request count\n", "": ""} {
		g := newWorkloadsRig(t)
		g.s.Workloads["shop"] = "AWAKE"
		g.s.WorkloadDocs["shop"] = map[string]any{"backendService": "shop"}
		if reads != "" {
			g.s.WorkloadDocs["shop"]["idleReads"] = reads
		}
		code, out := g.call(t, WorkloadsShow, offboardShop)
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if want == "" && strings.Contains(out, "idle check") || !strings.Contains(out, want) {
			t.Errorf("%q:\n%s", reads, out)
		}
	}
}
