package gcponboard

// The writes' lines and --json as AWS's (the AWS and Google Cloud review's §4.4): "<router>/<workload>: <what happened>", the
// router by its name in the manager; under --json AWS's map by <router>/<workload> and AWS's list object, their rows with the
// shared names (workloadId, pauses each with its scope, the shared kinds), whatever the router's version.

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
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
			{WorkloadsNap, nil, shopTarget + ": not awake (ASLEEP)\n"},
			{WorkloadsWake, nil, shopTarget + ": wake requested (ASLEEP)\n"},
			{WorkloadsWake, nil, shopTarget + ": wake requested (AWAKE)\n"},
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

// The end in the local time, as AWS's; "; woken" names what the answer's own list names, never what a state suggests.
func TestWorkloadsPauseSaysItsEndInLocalTimeAndWhatTheAnswerWoke(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "ASLEEP"
	r := workloadsEurope
	r.ID, r.Reason, r.Until = "shop", "freeze", "2026-12-01T07:00:00Z"
	local := time.Date(2026, 12, 1, 7, 0, 0, 0, time.UTC).In(g.d.Now().Location()).Format("2006-01-02 15:04 MST")
	if code, out := g.call(t, WorkloadsPause, r); code != 0 || out != shopTarget+": paused until "+local+"\n" {
		t.Fatalf("exit %d, out %q", code, out)
	}
	g.s.WorkloadDocs["shop"]["woken"] = []any{"shop"}
	if code, out := g.call(t, WorkloadsPause, r); code != 0 || out != shopTarget+": paused until "+local+"; woken shop\n" {
		t.Fatalf("an answer naming what it woke: exit %d, out %q", code, out)
	}
}

// AWS's line for a resume, whatever pause stays (a router's).
func TestWorkloadsResumeSaysAWSLineUnderARouterPause(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	// The router's pause, which the workload's resume leaves.
	g.s.WorkloadDocs["shop"] = map[string]any{"pauses": []any{map[string]any{"scope": "router", "by": "ops@example.com", "at": "2026-11-01T10:00:00.000Z", "reason": "freeze", "until": nil, "keepAsleep": false}}}
	r := workloadsEurope
	r.ID = "shop"
	if code, out := g.call(t, WorkloadsResume, r); code != 0 || out != shopTarget+": resumed\n" {
		t.Fatalf("exit %d, out %q", code, out)
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
				if code != 1 || compacted(stdout) != "{}" || !strings.Contains(stderr, "paused") {
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
			!strings.Contains(stdout, `"cursor": null`) {
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
	if _, stdout, _ := g.split(t, WorkloadsList, r); compacted(stdout) != `{"workloads":[],"total":0,"byState":{},"cursor":null,"errors":[]}` {
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
	if !hasRow(out, "backends", "instance web-1 (europe-west1-b)") || !hasRow(out, "", "asg web-mig (europe-west1)") || !hasRow(out, "", "cloudsql-instance shop-db") {
		t.Fatalf("%s", out)
	}
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
	if code != 0 || out != shopTarget+": schedule set\n" {
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
	if code != 0 || compacted(stdout) != `{"confirmed":false,"targets":["`+shopTarget+`","`+shopRouter+`/api"]}` || !strings.Contains(stderr, "not confirmed: nothing sent\n") {
		t.Fatalf("declined: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// A router's lines and --json key name it as the manager does.
func TestRouterWritesNameTheRouterAsTheManager(t *testing.T) {
	g := newWorkloadsRig(t)
	r := workloadsEurope
	r.Reason = "freeze"
	if code, out := g.call(t, RouterPause, r); code != 0 || out != shopRouter+": paused until resumed\n" {
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
// next line, and under --json the released {"dryRun":true,"targets":[…]}.
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
	if code != 0 || compacted(stdout) != `{"dryRun":true,"targets":["`+shopTarget+`"]}` ||
		!strings.HasPrefix(stderr, "dry run: ") {
		t.Fatalf("--json: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// A router's, and several workloads' settings: one request each.
	r = workloadsEurope
	r.DryRun, r.JSON, r.Reason = true, true, "freeze"
	if _, stdout, _ = g.split(t, RouterPause, r); compacted(stdout) != `{"dryRun":true,"targets":["`+shopRouter+`"]}` {
		t.Fatalf("router: %q", stdout)
	}
	r.IDs, r.Levels = []string{"shop", "api"}, map[string]any{"idleMinutes": 30}
	if _, stdout, _ = g.split(t, WorkloadsSettings, r); compacted(stdout) != `{"dryRun":true,"targets":["`+shopTarget+`","`+shopRouter+`/api"]}` {
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

// The confirmations say AWS's lines: without a terminal "confirmation needed: pass --yes to act on 1 <noun> without a terminal"
// (exit 2), declined "not confirmed: nothing sent" (exit 0), nothing changed either way.
func TestConfirmationsSayAWSLines(t *testing.T) {
	noTerminal := func(noun string) string {
		return "confirmation needed: pass --yes to act on 1 " + noun + " without a terminal\n"
	}
	declining := func(d *Deps, out *strings.Builder) {
		d.Terminal = true
		d.Prompter = install.NewPrompter(strings.NewReader("n\n"), out, true)
	}
	t.Run("routers install", func(t *testing.T) {
		g := newInstallRig(t)
		code, out := g.run(t, Request{Project: "dormeto-dev", Region: "europe-west1", AccessOnly: true})
		if code != 2 || !strings.HasSuffix(out, noTerminal("router")) || len(g.s.Mutations()) != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		g.d.Terminal = true
		g.d.Prompter = install.NewPrompter(strings.NewReader("n\n"), g.out, true)
		code, out = g.run(t, Request{Project: "dormeto-dev", Region: "europe-west1", AccessOnly: true})
		if code != 0 || !strings.HasSuffix(out, "Apply these changes to project dormeto-dev? [y/N]: not confirmed: nothing sent\n") || len(g.s.Mutations()) != 0 {
			t.Fatalf("declined: exit %d:\n%s", code, out)
		}
	})
	t.Run("routers uninstall", func(t *testing.T) {
		g := routerInstalled(t)
		mark := len(g.s.Calls())
		code, out := g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1"})
		if code != 2 || !strings.HasSuffix(out, noTerminal("router")) || len(g.changes(mark)) != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		g.d.Terminal = true
		g.d.Prompter = install.NewPrompter(strings.NewReader("n\n"), g.out, true)
		code, out = g.uninstall(t, Request{Project: "dormeto-dev", Region: "europe-west1"})
		if code != 0 || !strings.HasSuffix(out, "not confirmed: nothing sent\n") || len(g.changes(mark)) != 0 {
			t.Fatalf("declined: exit %d:\n%s", code, out)
		}
	})
	t.Run("workloads access", func(t *testing.T) {
		g := newRbacRig(t)
		r := shopAccess
		r.Yes = false
		for _, run := range []func(context.Context, Deps, Request) int{WorkloadsAccessGrant} {
			code, out := g.call(t, run, r)
			if code != 2 || !strings.HasSuffix(out, noTerminal("workload")) || len(g.k.writes(0)) != 0 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			g.d.Terminal = true
			g.d.Prompter = install.NewPrompter(strings.NewReader("n\n"), g.out, true)
			code, out = g.call(t, run, r)
			if code != 0 || !strings.HasSuffix(out, "not confirmed: nothing sent\n") || len(g.k.writes(0)) != 0 {
				t.Fatalf("declined: exit %d:\n%s", code, out)
			}
			g.d.Terminal = false
		}
		// Under --json, as offboard and AWS's commands: the question, after what it is about, on stderr, and stdout JSON alone.
		r.JSON = true
		if code, stdout, stderr := g.split(t, WorkloadsAccessGrant, r); code != 2 || stdout != "" || !strings.HasSuffix(stderr, noTerminal("workload")) {
			t.Fatalf("--json without a terminal: exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		code, stdout, stderr := g.asked(t, WorkloadsAccessGrant, r, "n")
		if code != 0 || compacted(stdout) != `{"confirmed":false,"targets":["shop/dormeto-shop"]}` || len(g.k.writes(0)) != 0 ||
			!strings.Contains(stderr, "Kubernetes access of workload shop, namespace shop") || !strings.Contains(stderr, "Apply these to namespace shop of cluster europe-west1-b/shop-cluster? [y/N]: ") ||
			!strings.HasSuffix(stderr, "not confirmed: nothing sent\n") {
			t.Fatalf("--json declined: exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		code, stdout, stderr = g.asked(t, WorkloadsAccessGrant, r, "y")
		var granted map[string]any
		if err := json.Unmarshal([]byte(stdout), &granted); code != 0 || err != nil || granted["namespace"] != "shop" || len(g.k.writes(0)) == 0 {
			t.Fatalf("--json confirmed: exit %d, %v, stdout %q, stderr %q", code, err, stdout, stderr)
		}
		mark := len(g.k.writes(0))
		code, stdout, stderr = g.asked(t, WorkloadsAccessRevoke, r, "n")
		if code != 0 || compacted(stdout) != `{"confirmed":false,"targets":["shop/dormeto-shop"]}` || len(g.k.writes(0)) != mark ||
			!strings.Contains(stderr, "Remove these from namespace shop of cluster europe-west1-b/shop-cluster? [y/N]: ") {
			t.Fatalf("revoke --json declined: exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	})
	t.Run("connect and disconnect", func(t *testing.T) {
		s := connectServer(t)
		m := newFakeManager()
		r := connectRequest(m)
		r.Yes = false
		d, out, errs := connectDeps(s)
		if code := Connect(context.Background(), d, r); code != 2 || !strings.HasSuffix(errs.String(), noTerminal("connection")) || len(s.Mutations()) != 0 {
			t.Fatalf("connect: exit %d:\n%s%s", code, out, errs)
		}
		d, out, _ = connectDeps(s)
		var said strings.Builder
		declining(&d, &said)
		if code := Connect(context.Background(), d, r); code != 0 || !strings.HasSuffix(out.String(), "not confirmed: nothing sent\n") || len(s.Mutations()) != 0 {
			t.Fatalf("connect declined: exit %d:\n%s", code, out)
		}
		if code, out := runConnect(t, s, connectRequest(m)); code != 0 {
			t.Fatalf("connect: exit %d:\n%s", code, out)
		}
		mark := len(s.Mutations())
		d, out, errs = connectDeps(s)
		if code := Disconnect(context.Background(), d, r); code != 2 || !strings.HasSuffix(errs.String(), noTerminal("connection")) || len(s.Mutations()) != mark {
			t.Fatalf("disconnect: exit %d:\n%s%s", code, out, errs)
		}
		d, out, _ = connectDeps(s)
		declining(&d, &said)
		if code := Disconnect(context.Background(), d, r); code != 0 || !strings.HasSuffix(out.String(), "not confirmed: nothing sent\n") || len(s.Mutations()) != mark {
			t.Fatalf("disconnect declined: exit %d:\n%s", code, out)
		}
	})
}

// A row names its target and its cloud as the manager's rows do (aws workloads list --json): router (the manager's name),
// account (the project), region and cloud gcp; its router and workloadId are the key its write's map uses.
func TestWorkloadRowsNameTheirTargetAndCloudAsTheManagersRows(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	r := workloadsEurope
	r.JSON = true
	_, stdout, _ := g.split(t, WorkloadsList, r)
	var list struct{ Workloads []fleet.Workload }
	if err := json.Unmarshal([]byte(stdout), &list); err != nil || len(list.Workloads) != 1 {
		t.Fatalf("%v: %s", err, stdout)
	}
	row := list.Workloads[0]
	if row.Router != shopRouter || row.Account != "dormeto-dev" || row.Region != "europe-west1" || row.Cloud != "gcp" || row.WorkloadID != "shop" {
		t.Fatalf("row %+v", row)
	}
	r.ID = "shop"
	_, stdout, _ = g.split(t, WorkloadsNap, r)
	var answered fleet.Workload
	if err := json.Unmarshal(byTarget(t, stdout, row.Router+"/"+row.WorkloadID), &answered); err != nil || answered.Router != shopRouter || answered.Cloud != "gcp" {
		t.Fatalf("%v: %s", err, stdout)
	}
	_, stdout, _ = g.split(t, WorkloadsShow, r)
	var shown fleet.Workload
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil || shown.Router != shopRouter || shown.Account != "dormeto-dev" || shown.Region != "europe-west1" || shown.Cloud != "gcp" {
		t.Fatalf("show: %v: %s", err, stdout)
	}
}

// A refused onboarding or offboarding prints AWS's map of no answer under --json, as the other writes do.
func TestRefusedOnboardAndOffboardPrintNoAnswerUnderJSON(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.OnboardingRefused = "the backend service changed"
	r := shopOnboarding
	r.JSON = true
	if code, stdout, stderr := g.split(t, WorkloadsOnboard, r); code != 1 || compacted(stdout) != "{}" || !strings.Contains(stderr, "the backend service changed") {
		t.Fatalf("onboard: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	r = workloadsEurope
	r.ID, r.JSON = "ghost", true
	if code, stdout, stderr := g.split(t, WorkloadsOffboard, confirmed(r)); code != 1 || compacted(stdout) != "{}" || !strings.Contains(stderr, "no workload ghost") {
		t.Fatalf("offboard: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// Under --json stdout is JSON alone, as AWS's: a refusal without a terminal says AWS's line on stderr and prints nothing; a
// declined question prints {"confirmed":false,…} and says AWS's line on stderr.
func TestConfirmationsUnderJSONKeepStdoutJSON(t *testing.T) {
	noTerminal := func(noun string) string {
		return "confirmation needed: pass --yes to act on 1 " + noun + " without a terminal\n"
	}
	t.Run("routers install and uninstall", func(t *testing.T) {
		for name, run := range map[string]func(context.Context, Deps, Request) int{"install": Install, "uninstall": Uninstall} {
			r := Request{Project: "dormeto-dev", Region: "europe-west1", JSON: true}
			var g *installRig
			if name == "install" {
				g, r.AccessOnly = newInstallRig(t), true
			} else {
				g = routerInstalled(t)
			}
			var o, e strings.Builder
			d := g.d
			d.Out, d.Err = &o, &e
			if code := run(context.Background(), d, r); code != 2 || o.Len() != 0 || !strings.HasSuffix(e.String(), noTerminal("router")) {
				t.Fatalf("%s: exit %d, stdout %q, stderr %q", name, code, o.String(), e.String())
			}
			o.Reset()
			e.Reset()
			d.Terminal, d.Prompter = true, install.NewPrompter(strings.NewReader("n\n"), &e, true)
			var got struct {
				Confirmed *bool
				Targets   []string
				Plan      []string
			}
			if code := run(context.Background(), d, r); code != 0 || json.Unmarshal([]byte(o.String()), &got) != nil || got.Confirmed == nil || *got.Confirmed ||
				!slices.Equal(got.Targets, []string{shopRouter}) || len(got.Plan) == 0 || !strings.HasSuffix(e.String(), "not confirmed: nothing sent\n") {
				t.Fatalf("%s declined: exit %d, stdout %q, stderr %q", name, code, o.String(), e.String())
			}
			// The dry run: the released shape, its plan beside it.
			o.Reset()
			e.Reset()
			r.DryRun = true
			var dry struct {
				DryRun  bool
				Targets []string
				Plan    []string
			}
			if code := run(context.Background(), d, r); code != 0 || json.Unmarshal([]byte(o.String()), &dry) != nil || !dry.DryRun ||
				!slices.Equal(dry.Targets, []string{shopRouter}) || len(dry.Plan) == 0 || !strings.Contains(e.String(), "Plan for project dormeto-dev") {
				t.Fatalf("%s dry run: exit %d, stdout %q, stderr %q", name, code, o.String(), e.String())
			}
		}
	})
	t.Run("connect", func(t *testing.T) {
		s := connectServer(t)
		r := connectRequest(newFakeManager())
		r.Yes, r.JSON = false, true
		d, out, errs := connectDeps(s)
		if code := Connect(context.Background(), d, r); code != 2 || out.Len() != 0 || !strings.HasSuffix(errs.String(), noTerminal("connection")) {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
		d, out, errs = connectDeps(s)
		d.Terminal, d.Prompter = true, install.NewPrompter(strings.NewReader("n\n"), errs, true)
		var got struct {
			Confirmed *bool
			Targets   []string
			Plan      []string
		}
		if code := Connect(context.Background(), d, r); code != 0 || json.Unmarshal(out.Bytes(), &got) != nil || got.Confirmed == nil || *got.Confirmed ||
			!slices.Equal(got.Targets, []string{"dormeto-dev"}) || len(got.Plan) == 0 || !strings.HasSuffix(errs.String(), "not confirmed: nothing sent\n") {
			t.Fatalf("declined: exit %d, stdout %q, stderr %q", code, out, errs)
		}
	})
	t.Run("workloads settings and offboard", func(t *testing.T) {
		g := newWorkloadsRig(t)
		g.s.Workloads["shop"], g.s.Workloads["api"] = "AWAKE", "AWAKE"
		r := workloadsEurope
		r.IDs, r.JSON, r.Levels = []string{"shop", "api"}, true, map[string]any{"idleMinutes": 30}
		if code, stdout, stderr := g.split(t, WorkloadsSettings, r); code != 2 || stdout != "" || !strings.Contains(stderr, "confirmation needed: pass --yes to act on 2 workloads without a terminal\n") {
			t.Fatalf("settings: exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		// offboard --json without a terminal and without --yes: exit 2, stdout clean, nothing changed (the workload still held).
		r = workloadsEurope
		r.ID, r.JSON = "shop", true
		mark := len(g.s.Calls())
		if code, stdout, stderr := g.split(t, WorkloadsOffboard, r); code != 2 || stdout != "" || !strings.HasSuffix(stderr, noTerminal("workload")) {
			t.Fatalf("offboard: exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		if m := g.changes(mark); len(m) != 0 || g.s.Workloads["shop"] != "AWAKE" {
			t.Fatalf("offboard changed: %v, the workload %q", m, g.s.Workloads["shop"])
		}
	})
	t.Run("onboard on GKE", func(t *testing.T) {
		g, _ := newGKERig(t)
		r := shopOnGKE
		r.Yes, r.JSON = false, true
		mark := len(g.s.Calls())
		if code, stdout, stderr := g.split(t, WorkloadsOnboard, r); code != 2 || stdout != "" || !strings.HasSuffix(stderr, noTerminal("workload")) {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		code, stdout, stderr := g.asked(t, WorkloadsOnboard, r, "n")
		changed := slices.DeleteFunc(g.changes(mark), func(c string) bool { return strings.HasSuffix(c, "/v1/workloads?dryRun=true") }) // the controller's dry runs
		if code != 0 || compacted(stdout) != `{"confirmed":false,"targets":["shop/dormeto-shop"]}` || len(g.k.writes(0)) != 0 || len(changed) != 0 ||
			!strings.Contains(stderr, "Apply these to namespace shop of cluster") || !strings.HasSuffix(stderr, "not confirmed: nothing sent\n") {
			t.Fatalf("declined: exit %d, stdout %q, stderr %q, changes %v", code, stdout, stderr, changed)
		}
	})
}

// asked runs f on a terminal that answers answer, its question on the standard error as the CLI's prompter writes it under
// --json: the standard output and the standard error apart.
func (g *rbacRig) asked(t *testing.T, f func(context.Context, Deps, Request) int, r Request, answer string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e strings.Builder
	d := g.d
	d.Out, d.Err, d.Terminal = &o, &e, true
	d.Prompter = install.NewPrompter(strings.NewReader(answer+"\n"), &e, true)
	return f(context.Background(), d, r), o.String(), e.String()
}

// The onboarding's dry run is the released shape too, the controller's eligibility beside it.
func TestWorkloadsOnboardDryRunJSONIsTheReleasedShape(t *testing.T) {
	g := newWorkloadsRig(t)
	r := shopOnboarding
	r.DryRun, r.JSON = true, true
	code, stdout, _ := g.split(t, WorkloadsOnboard, r)
	var got struct {
		DryRun      bool
		Targets     []string
		Eligibility struct {
			Eligible bool
			VMs      []any
		}
	}
	if err := json.Unmarshal([]byte(stdout), &got); code != 0 || err != nil || !got.DryRun || !slices.Equal(got.Targets, []string{shopTarget}) ||
		!got.Eligibility.Eligible || len(got.Eligibility.VMs) != 1 {
		t.Fatalf("exit %d, %v: %s", code, err, stdout)
	}
}

// A refused nap, wake, pause or resume prints AWS's head, "<router>/<workload>: <refusal>", as AWS's applyAll does, on the
// standard error, exit 1; the paused workload's nap says it too.
func TestWorkloadsWritesRefusedPrintTheTargetAsTheirHead(t *testing.T) {
	g := newWorkloadsRig(t)
	var out, errs strings.Builder
	g.d.Out, g.d.Err = &out, &errs
	r := workloadsEurope
	r.ID, r.Reason = "nope", "freeze"
	for name, f := range map[string]func(context.Context, Deps, Request) int{"nap": WorkloadsNap, "wake": WorkloadsWake,
		"pause": WorkloadsPause, "resume": WorkloadsResume} {
		out.Reset()
		errs.Reset()
		code := f(context.Background(), g.d, r)
		if want := "dmtr-dormeto-dev-europe-west1/nope: no workload nope\n"; code != 1 || errs.String() != want || out.String() != "" {
			t.Errorf("%s: exit %d, stderr %q (want %q), stdout %q", name, code, errs.String(), want, out.String())
		}
	}
	g.s.Workloads["shop"] = "AWAKE"
	r.ID = "shop"
	g.call(t, WorkloadsPause, r)
	out.Reset()
	errs.Reset()
	if code := WorkloadsNap(context.Background(), g.d, r); code != 1 || errs.String() != shopTarget+": workload shop is paused by me@example.com: freeze\n" {
		t.Errorf("exit %d, stderr %q", code, errs.String())
	}
}

// A refused schedule set or clear is headed by its target too, as AWS's are; a router too old for recurrences keeps its words.
func TestWorkloadsScheduleRefusedPrintTheTargetAsTheirHead(t *testing.T) {
	g := newWorkloadsRig(t)
	r := workloadsEurope
	r.ID, r.Schedule = "nope", json.RawMessage(parisSchedule)
	for name, f := range map[string]func(context.Context, Deps, Request) int{"set": WorkloadsScheduleSet, "clear": WorkloadsScheduleClear} {
		code, stdout, stderr := g.split(t, f, r)
		if want := shopRouter + "/nope: no workload nope\n"; code != 1 || stderr != want || stdout != "" {
			t.Errorf("%s: exit %d, stderr %q (want %q), stdout %q", name, code, stderr, want, stdout)
		}
	}
	g.s.Workloads["shop"] = "AWAKE"
	g.s.ScheduleProblem = "windows[0].repeat: unknown field"
	r.ID, r.Schedule = "shop", json.RawMessage(recurringSchedule)
	code, stdout, stderr := g.split(t, WorkloadsScheduleSet, r)
	if code != 1 || stdout != "" || !strings.HasPrefix(stderr, shopTarget+": windows[0].repeat: unknown field\nRecurrences and joined windows need a router of version 0.36.0") {
		t.Errorf("recurrence: exit %d, stderr %q, stdout %q", code, stderr, stdout)
	}
	// Under --json, AWS's map of no answer.
	r.JSON = true
	if code, stdout, stderr := g.split(t, WorkloadsScheduleSet, r); code != 1 || compacted(stdout) != "{}" || !strings.HasPrefix(stderr, shopTarget+": ") {
		t.Errorf("--json: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// compacted is the JSON s compact, as a test compares it (s trimmed when it is not JSON).
func compacted(s string) string {
	var b bytes.Buffer
	if json.Compact(&b, []byte(s)) != nil {
		return strings.TrimSpace(s)
	}
	return b.String()
}

// indentedAsAWS tells whether s is JSON as AWS's commands print theirs: indented by two spaces, a line feed after it.
func indentedAsAWS(s string) bool {
	var compact, indented bytes.Buffer
	if json.Compact(&compact, []byte(s)) != nil || json.Indent(&indented, compact.Bytes(), "", "  ") != nil {
		return false
	}
	return indented.String()+"\n" == s
}

// One JSON style on both subtrees: every --json of gcp's is indented as AWS's.
func TestJSONIsIndentedAsAWSs(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	r := workloadsEurope
	r.JSON = true
	shop := r
	shop.ID = "shop"
	dry := shop
	dry.DryRun, dry.Reason = true, "freeze"
	settings := r
	settings.IDs, settings.Levels = []string{"shop"}, map[string]any{"idleMinutes": 30}
	pause := r
	pause.Reason = "freeze"
	for _, c := range []struct {
		name string
		run  func(context.Context, Deps, Request) int
		r    Request
	}{
		{"list", WorkloadsList, r}, {"show", WorkloadsShow, shop}, {"nap", WorkloadsNap, shop}, {"pause --dry-run", WorkloadsPause, dry},
		{"settings", WorkloadsSettings, settings}, {"routers pause", RouterPause, pause}, {"routers settings", RouterSettings, r},
		{"schedule show", WorkloadsScheduleShow, shop},
	} {
		code, stdout, stderr := g.split(t, c.run, c.r)
		if code != 0 || !indentedAsAWS(stdout) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", c.name, code, stdout, stderr)
		}
	}
}

// hasRow tells whether text has the labelled row "<label>  <value>" as fleet.Labelled writes it (the label padded, then two
// spaces at least), at any indent.
func hasRow(text, label, value string) bool {
	return regexp.MustCompile(`(?m)^ *` + regexp.QuoteMeta(label) + ` {2,}` + regexp.QuoteMeta(value) + `$`).MatchString(text)
}

// The reads print what AWS's print (the cloud duplication design's §4, B10 to B14): the list's table (B11).
func TestWorkloadsListPrintsAWSTable(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.SharedNames = true
	g.s.Workloads["shop"], g.s.Workloads["blog"] = "AWAKE", "ASLEEP"
	g.s.WorkloadDocs["shop"] = map[string]any{"name": "The shop", "tags": map[string]any{"team": "web", "env": "prod"},
		"backendService": "shop-be", "awakeSince": installNow.Add(-3 * time.Hour).Format(time.RFC3339),
		"paused": map[string]any{"by": "me@example.com", "at": "2026-10-29T08:00:00Z", "reason": "audit", "until": nil, "keepAsleep": false}}
	g.s.WorkloadDocs["blog"] = map[string]any{"name": "blog"}
	code, out := g.call(t, WorkloadsList, workloadsEurope)
	want := "ROUTER                         WORKLOAD  NAME      CLOUD  STATE   TAGS               CHANGED\n" +
		"dmtr-dormeto-dev-europe-west1  blog      blog      gcp    ASLEEP  —                  —\n" +
		"dmtr-dormeto-dev-europe-west1  shop      The shop  gcp    AWAKE   env=prod,team=web  3h\n" +
		"2 workloads (1 ASLEEP, 1 AWAKE)\n"
	if code != 0 || out != want {
		t.Fatalf("exit %d:\n%s\nwant:\n%s", code, out, want)
	}
	// None: AWS's count alone, the filters' too.
	g = newWorkloadsRig(t)
	if code, out := g.call(t, WorkloadsList, workloadsEurope); code != 0 || out != "0 workloads\n" {
		t.Fatalf("none: exit %d, %q", code, out)
	}
}

// `workloads show` is AWS's labelled block from the router's answer (B13): what Google Cloud lacks is left out, what only it has
// is a row of the same form, and a pause reads as AWS's, in the local time (B12).
func TestWorkloadsShowIsAWSLayout(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.SharedNames = true
	g.s.Workloads["shop"] = "FAILED"
	g.s.WorkloadDocs["shop"] = map[string]any{"name": "The shop", "tags": map[string]any{"team": "web"},
		"lb": map[string]any{"kind": "regional", "region": "europe-west1"}, "urlMap": "shop-map", "backendService": "shop-be",
		"wakerBackendService": "dmtr-europe-west1-shop", "hosts": []any{"shop.example.com"},
		"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"},
			map[string]any{"kind": "cloudsql", "name": "shop-db"}},
		"failure":        map[string]any{"kind": "quota", "step": "wake", "message": "CPUS quota reached", "at": "2026-10-29T09:00:00Z"},
		"lastWake":       map[string]any{"durationMs": 41200},
		"idleReads":      "request logs",
		"napAborted":     map[string]any{"reason": "backends-changed", "at": "2026-10-29T04:55:57Z"},
		"companionsGone": []any{"shop-db"},
		"settings":       map[string]any{"idleMinutes": 30, "waitSeconds": 0, "wakeTimeoutSeconds": 900, "wakeMode": "browser"},
		"settingsFrom":   map[string]any{"idleMinutes": "router", "waitSeconds": "fixed", "wakeTimeoutSeconds": "workload", "wakeMode": "default"},
		"type":           map[string]any{"name": "instance", "from": "backends"},
		"hibernation":    map[string]any{"hibernate": true, "from": "router"},
		"noWakeRules":    []any{map[string]any{"name": "scanners", "level": "router", "conditions": map[string]any{"userAgentPattern": "bot"}}},
		"paused":         map[string]any{"by": "me@example.com", "at": "2026-10-29T08:00:00Z", "reason": "audit", "until": "2026-12-01T07:00:00Z", "keepAsleep": true}}
	r := workloadsEurope
	r.ID = "shop"
	code, out := g.call(t, WorkloadsShow, r)
	want := "workload               " + shopTarget + "\n" +
		"name                   The shop\n" +
		"state                  FAILED\n" +
		"failure                wake failed (quota): CPUS quota reached\n" +
		"cloud                  gcp\n" +
		"project                dormeto-dev\n" +
		"region                 europe-west1\n" +
		"tags                   team=web\n" +
		"load balancer          regional (europe-west1)\n" +
		"URL map                shop-map\n" +
		"backend service        shop-be\n" +
		"waker backend service  dmtr-europe-west1-shop\n" +
		"hosts                  shop.example.com\n" +
		"backends               instance web-1 (europe-west1-b)\n" +
		"                       cloudsql-instance shop-db\n" +
		"last wake              41.2 s\n" +
		"idle check             request logs\n" +
		"nap given up           backends-changed, 2026-10-29 04:55 UTC\n" +
		"problems               Cloud SQL instance shop-db no longer exists: its workload no longer naps; offboard and onboard it again\n" +
		"\nsettings\n" +
		"  type           instance (from its backends)\n" +
		"  idle window    30 min (router)\n" +
		"  hold           0 s (fixed on Google Cloud)\n" +
		"  wake timeout   15 min (set on this workload)\n" +
		"  hibernate      on, from the router\n" +
		"  wake mode      browser (built-in)\n" +
		"  no-wake rules  scanners: user agent ~ bot (router)\n" +
		"  wake rules     none\n" +
		"\npauses\n" +
		"  paused by me@example.com on 2026-10-29 08:00 UTC: audit, until 2026-12-01 07:00 UTC, keeps asleep\n"
	if code != 0 || out != want {
		t.Fatalf("exit %d:\n%s\nwant:\n%s", code, out, want)
	}
	// AWAKE: since when, in the state's row, as AWS's.
	g.s.Workloads["shop"] = "AWAKE"
	// A failure the router still holds of a workload no longer FAILED is not printed, as AWS's show does not (--json has it).
	g.s.WorkloadDocs["shop"] = map[string]any{"awakeSince": "2026-10-29T07:00:00Z",
		"failure": map[string]any{"kind": "quota", "step": "wake", "message": "CPUS quota reached", "at": "2026-10-29T06:00:00Z"}}
	if _, out = g.call(t, WorkloadsShow, r); !hasRow(out, "state", "AWAKE since 2026-10-29 07:00 UTC") || strings.Contains(out, "failure") || strings.Contains(out, "quota") || strings.Contains(out, "pauses") {
		t.Fatalf("awake:\n%s", out)
	}
}

// `schedule show` is AWS's text and --json AWS's shape, {schedule, next, …} (B10); the router's maintenanceDue, which its answer
// does not have, is left out rather than said none.
func TestWorkloadsScheduleShowIsAWSShapeAndText(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.NextEvents = parisEvents
	g.s.ScheduleJoin = map[string]any{"joinMinutes": 15, "joinFrom": "router", "joinInherited": map[string]any{"joinMinutes": 15, "joinFrom": "router"}}
	r := workloadsEurope
	r.ID, r.Schedule = "shop", json.RawMessage(parisSchedule)
	code, out := g.call(t, WorkloadsScheduleSet, r)
	if want := shopTarget + ": schedule set\n  next\n    Mon 2 Nov 22:00 CET nap window starts\n    Tue 3 Nov 06:00 CET nap window ends\n"; code != 0 || out != want {
		t.Fatalf("set: exit %d:\n%s\nwant:\n%s", code, out, want)
	}
	r.Schedule = nil
	code, out = g.call(t, WorkloadsScheduleShow, r)
	want := shopTarget + ": time zone Europe/Paris\n" +
		"  nap mon,tue 22:00–06:00 (next day), wake: never\n" +
		"  maximum nap 14 days\n" +
		"  join windows up to 15 min apart (router)\n" +
		"  next\n" +
		"    Mon 2 Nov 22:00 CET nap window starts\n" +
		"    Tue 3 Nov 06:00 CET nap window ends\n"
	if code != 0 || out != want {
		t.Fatalf("show: exit %d:\n%s\nwant:\n%s", code, out, want)
	}
	r.JSON = true
	code, out = g.call(t, WorkloadsScheduleShow, r)
	var shown map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &shown); code != 0 || err != nil || !indentedAsAWS(out) {
		t.Fatalf("exit %d, %v:\n%s", code, err, out)
	}
	var keys []string
	for k := range shown {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"joinFrom", "joinInherited", "joinMinutes", "next", "schedule"}) ||
		compacted(string(shown["next"])) != `[{"at":"2026-11-02T21:00:00.000Z","kind":"nap-start","window":0},{"at":"2026-11-03T05:00:00.000Z","kind":"nap-end","window":0}]` {
		t.Fatalf("not AWS's shape: %s", out)
	}
	// The schedule as the router answered it, who set it and when too.
	var s map[string]any
	if json.Unmarshal(shown["schedule"], &s) != nil || s["timeZone"] != "Europe/Paris" || s["by"] != "me@example.com" || s["at"] != "2026-11-01T10:00:00.000Z" {
		t.Fatalf("schedule: %s", shown["schedule"])
	}
	// None: AWS's words, and null with no next event.
	g.call(t, WorkloadsScheduleClear, Request{Project: r.Project, Region: r.Region, ID: "shop"})
	if _, out = g.call(t, WorkloadsScheduleShow, r); compacted(out) != `{"schedule":null,"next":[],"joinMinutes":15,"joinFrom":"router","joinInherited":{"joinFrom":"router","joinMinutes":15}}` {
		t.Fatalf("none: %s", out)
	}
	r.JSON = false
	if _, out = g.call(t, WorkloadsScheduleShow, r); out != shopTarget+": no schedule\n" {
		t.Fatalf("none: %q", out)
	}
}

// A workload's pause and a router's say one wording, on both subtrees (B14): "paused until …", ", keeps asleep" when asked, then
// "; woken …".
func TestRouterPauseSaysTheWorkloadsWording(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"], g.s.Workloads["blog"] = "ASLEEP", "ASLEEP"
	r := workloadsEurope
	r.Reason = "freeze"
	if code, out := g.call(t, RouterPause, r); code != 0 || out != shopRouter+": paused until resumed; woken blog, shop\n" {
		t.Fatalf("exit %d, %q", code, out)
	}
	g.call(t, RouterResume, r)
	r.Until, r.KeepAsleep = "2026-12-01T07:00:00Z", true
	if code, out := g.call(t, RouterPause, r); code != 0 || out != shopRouter+": paused until 2026-12-01 07:00 UTC, keeps asleep\n" {
		t.Fatalf("exit %d, %q", code, out)
	}
	// A workload's alike.
	g.s.Workloads["shop"] = "ASLEEP"
	r.ID = "shop"
	if code, out := g.call(t, WorkloadsPause, r); code != 0 || out != shopTarget+": paused until 2026-12-01 07:00 UTC, keeps asleep\n" {
		t.Fatalf("exit %d, %q", code, out)
	}
}

// What reaches the run is the command's --until, RFC 3339: anything else is refused before any call (exit 2), a workload's
// pause and a router's.
func TestPauseRefusesAnUntilThatIsNotRFC3339BeforeAnyCall(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	r := workloadsEurope
	r.ID, r.Reason, r.Until = "shop", "freeze", "2026-12-01"
	mark := len(g.s.Calls())
	for name, run := range map[string]func(context.Context, Deps, Request) int{"workload": WorkloadsPause, "router": RouterPause} {
		if code, out := g.call(t, run, r); code != 2 || out != "--until: \"2026-12-01\" is not an RFC 3339 time\n" {
			t.Errorf("%s: exit %d, %q", name, code, out)
		}
	}
	if got := g.s.Calls()[mark:]; len(got) != 0 {
		t.Fatalf("calls %v", got)
	}
}
