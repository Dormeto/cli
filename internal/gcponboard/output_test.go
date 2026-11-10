package gcponboard

// The writes' lines and --json as AWS's (the AWS and Google Cloud review's §4.4): "<router>/<workload>: <what happened>", the
// router by its name in the manager; under --json AWS's map by <router>/<workload> and AWS's list object, their rows with the
// shared names (workloadId, pauses each with its scope, the shared kinds), whatever the router's version.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/install"
)

// shopRouter is the router of the rig as the manager names it, the key of a router's line and of its --json.
const shopRouter = "dmtr-dormeto-dev-europe-west1"

// split is call with the standard output and the standard error apart.
func (g *workloadsRig) split(t *testing.T, f func(context.Context, Deps, Request) int, r Request) (code int, stdout, stderr string) {
	t.Helper()
	var o, e strings.Builder
	d := g.d
	d.Out, d.Err = &o, &e
	return f(context.Background(), d, r), o.String(), e.String()
}

// bothRouters runs the test on a router before 0.40.0 (id and paused alone) and on one with the shared names.
func bothRouters(t *testing.T, test func(t *testing.T, g *workloadsRig)) {
	for name, shared := range map[string]bool{"before 0.40.0": false, "0.40.0": true} {
		t.Run(name, func(t *testing.T) {
			g := newWorkloadsRig(t)
			g.s.SharedNames = shared
			test(t, g)
		})
	}
}

func TestWorkloadWritesPrintAWSLineByTarget(t *testing.T) {
	bothRouters(t, func(t *testing.T, g *workloadsRig) {
		g.s.Workloads["shop"] = "AWAKE"
		r := workloadsEurope
		r.ID = "shop"
		for _, c := range []struct {
			run  func(context.Context, Deps, Request) int
			r    func(Request) Request
			want string
		}{
			{WorkloadsNap, nil, shopTarget + ": nap requested\n"},
			{WorkloadsNap, nil, shopTarget + ": already asleep: nothing requested\n"},
			{WorkloadsWake, nil, shopTarget + ": wake requested (ASLEEP)\n"},
			{WorkloadsWake, nil, shopTarget + ": already awake: nothing requested\n"},
			{WorkloadsPause, func(r Request) Request { r.Reason = "freeze"; return r }, shopTarget + ": paused until resumed\n"},
			{WorkloadsResume, nil, shopTarget + ": resumed\n"},
			{WorkloadsScheduleClear, nil, shopTarget + ": schedule cleared\n"},
		} {
			rc := r
			if c.r != nil {
				rc = c.r(r)
			}
			if code, out := g.call(t, c.run, rc); code != 0 || out != c.want {
				t.Errorf("exit %d, out %q, want %q", code, out, c.want)
			}
		}
	})
}

func TestWorkloadsPauseSaysItsEndInLocalTimeAndTheWake(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "ASLEEP"
	r := workloadsEurope
	r.ID, r.Reason, r.Until = "shop", "freeze", "2026-12-01T07:00:00Z"
	local := time.Date(2026, 12, 1, 7, 0, 0, 0, time.UTC).In(g.d.Now().Location()).Format("2006-01-02 15:04 MST")
	if code, out := g.call(t, WorkloadsPause, r); code != 0 || out != shopTarget+": paused until "+local+"; woken shop\n" {
		t.Fatalf("exit %d, out %q", code, out)
	}
	// Kept asleep, nothing is woken.
	r.KeepAsleep = true
	if code, out := g.call(t, WorkloadsPause, r); code != 0 || out != shopTarget+": paused until "+local+"\n" {
		t.Fatalf("keep asleep: exit %d, out %q", code, out)
	}
}

// Under --json the answer is AWS's map, by <router>/<workload>, with the shared names; the line goes to the standard error. A
// router before 0.40.0 reads the same.
func TestWorkloadWritesJSONIsAWSMapByTarget(t *testing.T) {
	answers := map[string]string{}
	bothRouters(t, func(t *testing.T, g *workloadsRig) {
		g.s.Workloads["shop"] = "AWAKE"
		g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{
			map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"},
			map[string]any{"kind": "mig", "location": "europe-west1", "name": "web-mig", "id": "222"},
			map[string]any{"kind": "cloudsql", "name": "shop-db"},
			map[string]any{"kind": "k8s", "namespace": "shop", "service": "web"}}}
		r := workloadsEurope
		r.ID, r.JSON, r.Reason = "shop", true, "freeze"
		var all []string
		for _, c := range []struct {
			run  func(context.Context, Deps, Request) int
			line string
		}{{WorkloadsPause, shopTarget + ": paused until resumed\n"}, {WorkloadsNap, ""}, {WorkloadsResume, shopTarget + ": resumed\n"}, {WorkloadsNap, shopTarget + ": nap requested\n"}} {
			code, stdout, stderr := g.split(t, c.run, r)
			if c.line == "" { // a paused workload's nap is refused: AWS's map of no answer
				if code != 1 || strings.TrimSpace(stdout) != "{}" || !strings.Contains(stderr, "paused") {
					t.Fatalf("refused: exit %d, stdout %q, stderr %q", code, stdout, stderr)
				}
				continue
			}
			if code != 0 || stderr != c.line {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			var got map[string]map[string]any
			if err := json.Unmarshal([]byte(stdout), &got); err != nil || len(got) != 1 || got[shopTarget] == nil {
				t.Fatalf("%v: %s", err, stdout)
			}
			w := got[shopTarget]
			if w["workloadId"] != "shop" || w["id"] != nil || w["paused"] != nil || w["pauses"] == nil {
				t.Fatalf("not the shared names: %s", stdout)
			}
			var kinds []string
			for _, b := range w["backends"].([]any) {
				kinds = append(kinds, b.(map[string]any)["kind"].(string))
			}
			if !slices.Equal(kinds, []string{"instance", "asg", "cloudsql-instance", "k8s"}) {
				t.Fatalf("kinds %v", kinds)
			}
			all = append(all, stdout)
		}
		var paused map[string]struct {
			Pauses []map[string]any
		}
		if json.Unmarshal([]byte(all[0]), &paused) != nil || len(paused[shopTarget].Pauses) != 1 || paused[shopTarget].Pauses[0]["scope"] != "workload" ||
			paused[shopTarget].Pauses[0]["reason"] != "freeze" {
			t.Fatalf("the pause: %s", all[0])
		}
		answers[t.Name()] = strings.Join(all, "")
	})
	if old, now := answers[t.Name()+"/before_0.40.0"], answers[t.Name()+"/0.40.0"]; old == "" || old != now {
		t.Fatalf("a router before 0.40.0 reads differently:\n%s\n%s", old, now)
	}
}

// `workloads list --json` is AWS's list object: the rows the filters keep with the shared names, their total, the router's counts
// by state, no cursor and no error.
func TestWorkloadsListJSONIsAWSListObject(t *testing.T) {
	answers := map[string]string{}
	bothRouters(t, func(t *testing.T, g *workloadsRig) {
		g.s.Workloads["shop"], g.s.Workloads["api"] = "AWAKE", "ASLEEP"
		g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"},
			map[string]any{"kind": "mig", "location": "europe-west1", "name": "web-mig", "id": "222"}}}
		g.s.WorkloadDocs["api"] = map[string]any{"backends": []any{map[string]any{"kind": "cloudsql", "name": "api-db"}},
			"paused": map[string]any{"by": "me@example.com", "at": "2026-11-01T10:00:00.000Z", "reason": "freeze", "until": nil, "keepAsleep": false}}
		r := workloadsEurope
		r.JSON = true
		code, stdout, _ := g.split(t, WorkloadsList, r)
		var got struct {
			Workloads []struct {
				WorkloadID string `json:"workloadId"`
				ID         *string
				Paused     any
				Pauses     []map[string]any
				Backends   []struct{ Kind string }
			}
			Total   int
			ByState map[string]int
			Cursor  *string
			Errors  []any
		}
		if err := json.Unmarshal([]byte(stdout), &got); code != 0 || err != nil || got.Total != 2 || len(got.Workloads) != 2 ||
			got.ByState["AWAKE"] != 1 || got.ByState["ASLEEP"] != 1 || got.Cursor != nil || got.Errors == nil || len(got.Errors) != 0 ||
			!strings.Contains(stdout, `"cursor":null`) {
			t.Fatalf("exit %d, %v: %s", code, err, stdout)
		}
		api, shop := got.Workloads[0], got.Workloads[1]
		if api.WorkloadID != "api" || shop.WorkloadID != "shop" || api.ID != nil || api.Paused != nil || len(api.Pauses) != 1 ||
			api.Pauses[0]["scope"] != "workload" || shop.Pauses == nil || len(shop.Pauses) != 0 {
			t.Fatalf("rows: %s", stdout)
		}
		if api.Backends[0].Kind != "cloudsql-instance" || shop.Backends[0].Kind != "instance" || shop.Backends[1].Kind != "asg" {
			t.Fatalf("kinds: %s", stdout)
		}
		// Filtered, the total counts the rows kept; the counts by state stay the router's, as the manager's do.
		r.States = []string{"AWAKE"}
		code, stdout, _ = g.split(t, WorkloadsList, r)
		if err := json.Unmarshal([]byte(stdout), &got); code != 0 || err != nil || got.Total != 1 || len(got.Workloads) != 1 || got.ByState["ASLEEP"] != 1 {
			t.Fatalf("filtered: exit %d, %v: %s", code, err, stdout)
		}
		answers[t.Name()] = stdout
	})
	if old, now := answers[t.Name()+"/before_0.40.0"], answers[t.Name()+"/0.40.0"]; old == "" || old != now {
		t.Fatalf("a router before 0.40.0 reads differently:\n%s\n%s", old, now)
	}
	g := newWorkloadsRig(t)
	r := workloadsEurope
	r.JSON = true
	if _, stdout, _ := g.split(t, WorkloadsList, r); strings.TrimSpace(stdout) != `{"workloads":[],"total":0,"byState":{},"cursor":null,"errors":[]}` {
		t.Fatalf("none: %q", stdout)
	}
}

func TestWorkloadsShowJSONHasTheSharedNames(t *testing.T) {
	bothRouters(t, func(t *testing.T, g *workloadsRig) {
		g.s.Workloads["shop"] = "AWAKE"
		g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"}}}
		r := workloadsEurope
		r.ID, r.JSON = "shop", true
		code, stdout, _ := g.split(t, WorkloadsShow, r)
		var w map[string]any
		if err := json.Unmarshal([]byte(stdout), &w); code != 0 || err != nil || w["workloadId"] != "shop" || w["id"] != nil || w["pauses"] == nil ||
			w["backends"].([]any)[0].(map[string]any)["kind"] != "instance" {
			t.Fatalf("exit %d, %v: %s", code, err, stdout)
		}
	})
}

// The text names a backend by its shared kind too.
func TestWorkloadsShowNamesTheBackendsByTheirSharedKinds(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"},
		map[string]any{"kind": "mig", "location": "europe-west1", "name": "web-mig", "id": "222"}, map[string]any{"kind": "cloudsql", "name": "shop-db"}}}
	r := workloadsEurope
	r.ID = "shop"
	code, out := g.call(t, WorkloadsShow, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  backend: instance web-1 (europe-west1-b)\n", "  backend: asg web-mig (europe-west1)\n", "  backend: cloudsql-instance shop-db\n")
}

func TestWorkloadsOnboardAndOffboardPrintAWSLineAndMap(t *testing.T) {
	g := newWorkloadsRig(t)
	code, out := g.call(t, WorkloadsOnboard, shopOnboarding)
	if code != 0 || !strings.HasPrefix(out, shopTarget+": onboarded (AWAKE)\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  tagged: instance web-1 (europe-west1-b)\n")
	r := workloadsEurope
	r.ID = "shop"
	code, out = g.call(t, WorkloadsOffboard, confirmed(r))
	if code != 0 || !strings.HasPrefix(out, shopTarget+": offboarded\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  untagged: instance web-1 (europe-west1-b)\n")

	g = newWorkloadsRig(t)
	g.s.SharedNames = true
	ob := shopOnboarding
	ob.JSON = true
	code, stdout, stderr := g.split(t, WorkloadsOnboard, ob)
	var onboarded map[string]map[string]any
	if err := json.Unmarshal([]byte(stdout), &onboarded); code != 0 || err != nil || onboarded[shopTarget]["workloadId"] != "shop" || onboarded[shopTarget]["id"] != nil ||
		!strings.HasPrefix(stderr, shopTarget+": onboarded (AWAKE)\n") {
		t.Fatalf("exit %d, %v, stdout %s, stderr %q", code, err, stdout, stderr)
	}
	r.JSON = true
	code, stdout, stderr = g.split(t, WorkloadsOffboard, confirmed(r))
	var offboarded map[string]struct {
		WorkloadID string `json:"workloadId"`
		ID         *string
		Offboarded bool
		Untagged   []string
	}
	if err := json.Unmarshal([]byte(stdout), &offboarded); code != 0 || err != nil || offboarded[shopTarget].WorkloadID != "shop" || offboarded[shopTarget].ID != nil ||
		!offboarded[shopTarget].Offboarded || len(offboarded[shopTarget].Untagged) != 2 || !strings.HasPrefix(stderr, shopTarget+": offboarded\n") {
		t.Fatalf("exit %d, %v, stdout %s, stderr %q", code, err, stdout, stderr)
	}
}

func TestWorkloadsScheduleSetPrintsAWSLineThenTheSchedule(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	r := workloadsEurope
	r.ID, r.Schedule = "shop", json.RawMessage(parisSchedule)
	code, out := g.call(t, WorkloadsScheduleSet, r)
	if code != 0 || !strings.HasPrefix(out, shopTarget+": schedule set\n  time zone: Europe/Paris\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	r.JSON = true
	code, stdout, stderr := g.split(t, WorkloadsScheduleSet, r)
	var got map[string]struct{ Schedule map[string]any }
	if err := json.Unmarshal([]byte(stdout), &got); code != 0 || err != nil || got[shopTarget].Schedule["timeZone"] != "Europe/Paris" ||
		!strings.HasPrefix(stderr, shopTarget+": schedule set\n") {
		t.Fatalf("exit %d, %v, stdout %s, stderr %q", code, err, stdout, stderr)
	}
}

func TestWorkloadsSettingsPrintsAndKeysByTarget(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"], g.s.Workloads["api"] = "AWAKE", "AWAKE"
	r := workloadsEurope
	r.IDs, r.Yes, r.Levels = []string{"shop", "api"}, true, map[string]any{"idleMinutes": 30}
	code, out := g.call(t, WorkloadsSettings, r)
	if code != 0 || out != shopTarget+": its settings\n"+shopRouter+"/api: its settings\n" {
		t.Fatalf("exit %d, out %q", code, out)
	}
	r.JSON = true
	code, stdout, _ := g.split(t, WorkloadsSettings, r)
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); code != 0 || err != nil || len(got) != 2 || got[shopTarget] == nil || got[shopRouter+"/api"] == nil {
		t.Fatalf("exit %d, %v: %s", code, err, stdout)
	}
	// Several, declined on a terminal: AWS's line, and AWS's JSON of the targets.
	g.d.Terminal = true
	g.d.Prompter = install.NewPrompter(strings.NewReader("n\n"), g.out, true)
	r.Yes = false
	code, stdout, stderr := g.split(t, WorkloadsSettings, r)
	if code != 0 || strings.TrimSpace(stdout) != `{"confirmed":false,"targets":["`+shopTarget+`","`+shopRouter+`/api"]}` || !strings.Contains(stderr, "not confirmed: nothing sent\n") {
		t.Fatalf("declined: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// A router's lines and --json key name it as the manager does.
func TestRouterWritesNameTheRouterAsTheManager(t *testing.T) {
	g := newWorkloadsRig(t)
	r := workloadsEurope
	r.Reason = "freeze"
	if code, out := g.call(t, RouterPause, r); code != 0 || out != shopRouter+": paused\n" {
		t.Fatalf("pause: exit %d, out %q", code, out)
	}
	r.JSON = true
	code, stdout, _ := g.split(t, RouterResume, r)
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); code != 0 || err != nil || len(got) != 1 || got[shopRouter] == nil {
		t.Fatalf("resume --json: exit %d, %v: %s", code, err, stdout)
	}
	r.JSON, r.Page = false, &fleet.PageDoc{Mode: "template", Title: str("Back soon")}
	if code, out := g.call(t, RouterPageSet, r); code != 0 || out != shopRouter+": waiting page set (template)\n" {
		t.Fatalf("page: exit %d, out %q", code, out)
	}
}

// One dry run shape on both subtrees: each request said "dry run: <target>: would <method> <path>, nothing sent", its body on the
// next line, and under --json {"dryRun":true,"requests":[{target, method, path, body}]}.
func TestDryRunsSayEachRequestByTarget(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	r := workloadsEurope
	r.ID, r.DryRun, r.Reason = "shop", true, "freeze"
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsPause, r)
	if code != 0 || out != "dry run: "+shopTarget+": would POST /v1/workloads/shop/pause, nothing sent\n  "+`{"reason":"freeze"}`+"\n" {
		t.Fatalf("exit %d, out %q", code, out)
	}
	r.JSON = true
	code, stdout, stderr := g.split(t, WorkloadsPause, r)
	if code != 0 || strings.TrimSpace(stdout) != `{"dryRun":true,"requests":[{"target":"`+shopTarget+`","method":"POST","path":"/v1/workloads/shop/pause","body":{"reason":"freeze"}}]}` ||
		!strings.HasPrefix(stderr, "dry run: ") {
		t.Fatalf("--json: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// A router's, and several workloads' settings: one request each.
	r = workloadsEurope
	r.DryRun, r.JSON, r.Reason = true, true, "freeze"
	if _, stdout, _ = g.split(t, RouterPause, r); strings.TrimSpace(stdout) != `{"dryRun":true,"requests":[{"target":"`+shopRouter+`","method":"POST","path":"/v1/pause","body":{"reason":"freeze"}}]}` {
		t.Fatalf("router: %q", stdout)
	}
	r.IDs, r.Levels = []string{"shop", "api"}, map[string]any{"idleMinutes": 30}
	var got struct {
		DryRun   bool
		Requests []struct{ Target, Method, Path string }
	}
	if _, stdout, _ = g.split(t, WorkloadsSettings, r); json.Unmarshal([]byte(stdout), &got) != nil || !got.DryRun || len(got.Requests) != 2 ||
		got.Requests[1].Target != shopRouter+"/api" || got.Requests[1].Method != "PUT" || got.Requests[1].Path != "/v1/workloads/api/settings" {
		t.Fatalf("settings: %q", stdout)
	}
	if m := g.changes(mark); len(m) != 0 {
		t.Fatalf("changes: %v", m)
	}
}

// byTarget is the answer of target in AWS's map that a write prints under --json.
func byTarget(t *testing.T, stdout, target string) json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &m); err != nil || m[target] == nil {
		t.Fatalf("not a map by %s (%v): %s", target, err, stdout)
	}
	return m[target]
}
