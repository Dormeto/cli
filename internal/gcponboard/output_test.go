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
	if code != 0 || strings.TrimSpace(stdout) != `{"dryRun":true,"targets":["`+shopTarget+`"]}` ||
		!strings.HasPrefix(stderr, "dry run: ") {
		t.Fatalf("--json: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// A router's, and several workloads' settings: one request each.
	r = workloadsEurope
	r.DryRun, r.JSON, r.Reason = true, true, "freeze"
	if _, stdout, _ = g.split(t, RouterPause, r); strings.TrimSpace(stdout) != `{"dryRun":true,"targets":["`+shopRouter+`"]}` {
		t.Fatalf("router: %q", stdout)
	}
	r.IDs, r.Levels = []string{"shop", "api"}, map[string]any{"idleMinutes": 30}
	if _, stdout, _ = g.split(t, WorkloadsSettings, r); strings.TrimSpace(stdout) != `{"dryRun":true,"targets":["`+shopTarget+`","`+shopRouter+`/api"]}` {
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
		// Under --json, the question needs --yes as before, said as AWS's.
		r.JSON = true
		if code, out := g.call(t, WorkloadsAccessGrant, r); code != 2 || !strings.Contains(out, "confirmation needed: pass --yes to act on 1 workload with --json\n") {
			t.Fatalf("--json: exit %d:\n%s", code, out)
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
	if code, stdout, stderr := g.split(t, WorkloadsOnboard, r); code != 1 || strings.TrimSpace(stdout) != "{}" || !strings.Contains(stderr, "the backend service changed") {
		t.Fatalf("onboard: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	r = workloadsEurope
	r.ID, r.JSON = "ghost", true
	if code, stdout, stderr := g.split(t, WorkloadsOffboard, confirmed(r)); code != 1 || strings.TrimSpace(stdout) != "{}" || !strings.Contains(stderr, "no workload ghost") {
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
		r = workloadsEurope
		r.ID, r.JSON = "shop", true
		if code, stdout, stderr := g.split(t, WorkloadsOffboard, r); code != 2 || stdout != "" || !strings.HasSuffix(stderr, noTerminal("workload")) {
			t.Fatalf("offboard: exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	})
	t.Run("onboard on GKE", func(t *testing.T) {
		g, _ := newGKERig(t)
		r := shopOnGKE
		r.Yes, r.JSON = false, true
		if code, stdout, stderr := g.split(t, WorkloadsOnboard, r); code != 2 || stdout != "" || !strings.Contains(stderr, "confirmation needed: pass --yes to act on 1 workload with --json\n") {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	})
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
