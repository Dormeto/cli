package gcponboard

// The writing commands' --dry-run, --yes and confirmations, and wake --wait and list's filters, as AWS's (the AWS and Google
// Cloud review's §4.2).

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

// shopTarget is the workload shop as AWS's commands name a target: <router>/<workload>, the router by the manager's name.
const shopTarget = "dmtr-dormeto-dev-europe-west1/shop"

// confirmed is r with --yes: the offboarding asks nothing.
func confirmed(r Request) Request {
	r.Yes = true
	return r
}

func TestWorkloadWritesDryRunSendNothingAndSayTheRequest(t *testing.T) {
	page := &fleet.PageDoc{Mode: "template", Title: str("Back soon")}
	for name, c := range map[string]struct {
		run  func(context.Context, Deps, Request) int
		r    func(r Request) Request
		want []string
	}{
		"nap":            {WorkloadsNap, nil, []string{"dry run: would nap 1 workload, nothing sent\n", "  " + shopTarget + ": POST /v1/workloads/shop/nap\n"}},
		"wake":           {WorkloadsWake, nil, []string{"dry run: would wake 1 workload, nothing sent\n", "  " + shopTarget + ": POST /v1/workloads/shop/wake\n"}},
		"pause":          {WorkloadsPause, func(r Request) Request { r.Reason, r.Until = "freeze", "2026-12-01T07:00:00Z"; return r }, []string{"dry run: would pause 1 workload, nothing sent\n", `  ` + shopTarget + `: POST /v1/workloads/shop/pause {"reason":"freeze","until":"2026-12-01T07:00:00Z"}` + "\n"}},
		"resume":         {WorkloadsResume, nil, []string{"dry run: would resume 1 workload, nothing sent\n", "  " + shopTarget + ": POST /v1/workloads/shop/resume\n"}},
		"schedule set":   {WorkloadsScheduleSet, func(r Request) Request { r.Schedule = json.RawMessage(parisSchedule); return r }, []string{"dry run: would set the schedule of 1 workload, nothing sent\n", "  " + shopTarget + `: PUT /v1/workloads/shop/schedule {"maxNapDays":14,"timeZone":"Europe/Paris"`}},
		"schedule clear": {WorkloadsScheduleClear, nil, []string{"dry run: would clear the schedule of 1 workload, nothing sent\n", "  " + shopTarget + ": DELETE /v1/workloads/shop/schedule\n"}},
		"page set":       {WorkloadsPageSet, func(r Request) Request { r.Page = page; return r }, []string{"dry run: would set the waiting page of 1 workload, nothing sent\n", "  " + shopTarget + `: PUT /v1/workloads/shop/waiting-page {"page":{"mode":"template","title":"Back soon"}}` + "\n"}},
		"page clear":     {WorkloadsPageClear, nil, []string{"dry run: would clear the waiting page of 1 workload, nothing sent\n", "  " + shopTarget + ": DELETE /v1/workloads/shop/waiting-page\n"}},
		"offboard":       {WorkloadsOffboard, nil, []string{"dry run: would offboard 1 workload, nothing sent\n", "  " + shopTarget + ": DELETE /v1/workloads/shop\n", "  then untag: vm web-1 (europe-west1-b)\n"}},
	} {
		t.Run(name, func(t *testing.T) {
			g := newWorkloadsRig(t)
			g.s.Workloads["shop"] = "AWAKE"
			g.s.WorkloadDocs["shop"] = map[string]any{"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "web-1", "id": "111"}}}
			r := workloadsEurope
			r.ID, r.DryRun = "shop", true
			if c.r != nil {
				r = c.r(r)
			}
			mark := len(g.s.Calls())
			code, out := g.call(t, c.run, r)
			if code != 0 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			contains(t, out, c.want...)
			if m := g.changes(mark); len(m) != 0 {
				t.Fatalf("changes: %v", m)
			}
			// --json: AWS's {"dryRun":true,"targets":[…]} on stdout, the words on stderr.
			var stdout, stderr strings.Builder
			g.d.Out, g.d.Err = &stdout, &stderr
			r.JSON = true
			if code := c.run(context.Background(), g.d, r); code != 0 || strings.TrimSpace(stdout.String()) != `{"dryRun":true,"targets":["`+shopTarget+`"]}` {
				t.Fatalf("--json: exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "dry run: ") || len(g.changes(mark)) != 0 {
				t.Fatalf("--json: stderr %q, changes %v", stderr.String(), g.changes(mark))
			}
		})
	}
}

func TestRouterWaitingPageDryRunSendsNothingAndSaysTheRequest(t *testing.T) {
	g := newWorkloadsRig(t)
	r := workloadsEurope
	r.DryRun, r.Page = true, &fleet.PageDoc{Mode: "template", Title: str("Back soon")}
	mark := len(g.s.Calls())
	code, out := g.call(t, RouterPageSet, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "dry run: would set the waiting page of router, nothing sent\n", `  dmtr-europe-west1: PUT /v1/waiting-page {"page":{"mode":"template","title":"Back soon"}}`+"\n")
	r.Page = nil
	if code, out = g.call(t, RouterPageClear, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "dry run: would clear the waiting page of router, nothing sent\n", "  dmtr-europe-west1: DELETE /v1/waiting-page\n")
	if m := g.changes(mark); len(m) != 0 {
		t.Fatalf("changes: %v", m)
	}
}

// An icon or a custom page is said by its size in a dry run, never its content, as `waiting-page show` says them.
func TestWaitingPageDryRunSaysALongValueByItsSize(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	r := workloadsEurope
	html := "<h1>" + strings.Repeat("z", 300) + "</h1>"
	r.ID, r.DryRun, r.Page = "shop", true, &fleet.PageDoc{Mode: "custom", HTML: &html}
	code, out := g.call(t, WorkloadsPageSet, r)
	if code != 0 || strings.Contains(out, "zzzz") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, `"html":"(309 characters)"`)
}

func TestWorkloadsOffboardWithoutATerminalNeedsYesAndChangesNothing(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	r := workloadsEurope
	r.ID = "shop"
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 2 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "confirmation needed: pass --yes to act on 1 workload without a terminal\n")
	if m := g.changes(mark); len(m) != 0 || g.s.Workloads["shop"] != "AWAKE" {
		t.Fatalf("changes: %v", m)
	}
	if code, out = g.call(t, WorkloadsOffboard, confirmed(r)); code != 0 || len(g.s.Workloads) != 0 {
		t.Fatalf("--yes: exit %d:\n%s", code, out)
	}
}

func TestWorkloadsOffboardAsksOnATerminalAndADeclineSendsNothing(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.d.Terminal = true
	g.d.Prompter = install.NewPrompter(strings.NewReader("n\n"), g.out, true)
	r := workloadsEurope
	r.ID = "shop"
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsOffboard, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// AWS's: the targets listed, the question, and the decline's line.
	contains(t, out, "  "+shopTarget+"\nOffboard 1 workload?", "not confirmed: nothing sent\n")
	if m := g.changes(mark); len(m) != 0 || g.s.Workloads["shop"] != "AWAKE" {
		t.Fatalf("changes: %v", m)
	}
	var stdout, stderr strings.Builder
	g.d.Out, g.d.Err = &stdout, &stderr
	g.d.Prompter = install.NewPrompter(strings.NewReader("n\n"), &stderr, true)
	r.JSON = true
	if code := WorkloadsOffboard(context.Background(), g.d, r); code != 0 || strings.TrimSpace(stdout.String()) != `{"confirmed":false,"targets":["`+shopTarget+`"]}` {
		t.Fatalf("--json: exit %d, stdout %q", code, stdout.String())
	}
	g.d.Out, g.d.Err = g.out, g.out
	g.d.Prompter = install.NewPrompter(strings.NewReader("y\n"), g.out, true)
	r.JSON = false
	if code, out = g.call(t, WorkloadsOffboard, r); code != 0 || len(g.s.Workloads) != 0 {
		t.Fatalf("yes: exit %d:\n%s", code, out)
	}
	contains(t, out, "Offboarded workload shop")
}

// waitRig is a rig whose clock moves by what it sleeps.
func waitRig(t *testing.T, states ...string) (*workloadsRig, Request) {
	t.Helper()
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "ASLEEP"
	g.s.Waking = map[string][]string{"shop": states}
	now := installNow
	g.d.Now = func() time.Time { return now }
	g.d.Sleep = func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }
	r := workloadsEurope
	r.ID, r.Wait, r.Timeout = "shop", true, 15*time.Minute
	return g, r
}

func TestWorkloadsWakeWaitReturnsAtAwake(t *testing.T) {
	g, r := waitRig(t, "WAKING", "WAKING", "AWAKE")
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsWake, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, shopTarget+": awake\n")
	want := []string{"POST /v1/workloads/shop/wake", "GET /v1/workloads/shop", "GET /v1/workloads/shop", "GET /v1/workloads/shop"}
	if got := g.controllerCalls(mark); !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestWorkloadsWakeWaitTimesOutAsAws(t *testing.T) {
	g, r := waitRig(t, "WAKING")
	r.Timeout = time.Minute
	code, out := g.call(t, WorkloadsWake, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "timeout of 1m0s reached: not awake: "+shopTarget+"\n")
}

func TestWorkloadsWakeWaitEndsOnAFailureAsAws(t *testing.T) {
	g, r := waitRig(t, "FAILED")
	g.s.WorkloadDocs["shop"] = map[string]any{"failure": map[string]any{"message": "the VM web-1 did not start"}}
	code, out := g.call(t, WorkloadsWake, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, shopTarget+": failed to wake: the VM web-1 did not start\n")
}

func TestWorkloadsWakeWithoutWaitDoesNotFollow(t *testing.T) {
	g, r := waitRig(t, "WAKING", "AWAKE")
	r.Wait = false
	mark := len(g.s.Calls())
	if code, out := g.call(t, WorkloadsWake, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.controllerCalls(mark); !slices.Equal(got, []string{"POST /v1/workloads/shop/wake"}) {
		t.Fatalf("%v", got)
	}
}

// listRig has three workloads of a router 0.40.0, which answers their tags.
func listRig(t *testing.T) (*workloadsRig, Request) {
	t.Helper()
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"], g.s.Workloads["blog"], g.s.Workloads["api"] = "AWAKE", "ASLEEP", "ASLEEP"
	g.s.WorkloadDocs["shop"] = map[string]any{"name": "Shop", "tags": map[string]any{"team": "web"}}
	g.s.WorkloadDocs["blog"] = map[string]any{"name": "The blog", "tags": map[string]any{"team": "web", "env": "prod"}}
	g.s.WorkloadDocs["api"] = map[string]any{"name": "api", "tags": map[string]any{}}
	return g, workloadsEurope
}

func TestWorkloadsListFiltersByStateSearchAndTag(t *testing.T) {
	for name, c := range map[string]struct {
		r    func(r Request) Request
		want []string
	}{
		"state":       {func(r Request) Request { r.States = []string{"ASLEEP"}; return r }, []string{"api", "blog"}},
		"two states":  {func(r Request) Request { r.States = []string{"AWAKE", "ASLEEP"}; return r }, []string{"api", "blog", "shop"}},
		"search id":   {func(r Request) Request { r.Search = "SHO"; return r }, []string{"shop"}},
		"search name": {func(r Request) Request { r.Search = "the b"; return r }, []string{"blog"}},
		"tag":         {func(r Request) Request { r.HasTags = map[string]string{"team": "web"}; return r }, []string{"blog", "shop"}},
		"tags":        {func(r Request) Request { r.HasTags = map[string]string{"team": "web", "env": "prod"}; return r }, []string{"blog"}},
		"all of them": {func(r Request) Request {
			r.States, r.HasTags = []string{"ASLEEP"}, map[string]string{"env": "prod"}
			return r
		}, []string{"blog"}},
		"none matches": {func(r Request) Request { r.Search = "nothing-here"; return r }, nil},
	} {
		t.Run(name, func(t *testing.T) {
			g, r := listRig(t)
			r = c.r(r)
			code, out := g.call(t, WorkloadsList, r)
			if code != 0 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			var listed []string
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				if f := strings.Fields(line); len(f) > 0 && g.s.Workloads[f[0]] != "" {
					listed = append(listed, f[0])
				}
			}
			if !slices.Equal(listed, c.want) {
				t.Fatalf("listed %v, want %v:\n%s", listed, c.want, out)
			}
			// --json: the controller's rows, those that match alone.
			r.JSON = true
			_, out = g.call(t, WorkloadsList, r)
			var rows []map[string]any
			if err := json.Unmarshal([]byte(out), &rows); err != nil {
				t.Fatalf("%v:\n%s", err, out)
			}
			var ids []string
			for _, row := range rows {
				ids = append(ids, row["id"].(string))
			}
			if !slices.Equal(ids, c.want) {
				t.Fatalf("--json %v, want %v", ids, c.want)
			}
		})
	}
}

func TestWorkloadsListSaysWhenNoneMatchesTheFilters(t *testing.T) {
	g, r := listRig(t)
	r.States = []string{"FAILED"}
	code, out := g.call(t, WorkloadsList, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "no workload of router dmtr-europe-west1 matches")
}

// A router before 0.40.0 answers no tags: --tag says so, rather than list none.
func TestWorkloadsListTagOnARouterThatAnswersNoTagsSaysSo(t *testing.T) {
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	r := workloadsEurope
	r.HasTags = map[string]string{"team": "web"}
	code, out := g.call(t, WorkloadsList, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "--tag needs router 0.40.0 or later")
}

func TestRbacGrantAndRevokeDryRunPrintTheirPlanAndChangeNothing(t *testing.T) {
	g := newRbacRig(t)
	r := shopAccess
	r.DryRun, r.Yes = true, false
	mark, changes := len(g.k.callList()), len(g.s.Calls())
	code, out := g.call(t, WorkloadsAccessGrant, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "+ Role dormeto-shop", "+ RoleBinding dormeto-shop", "dry run: would apply these to namespace shop of cluster europe-west1-b/shop-cluster, nothing sent\n")
	if w := g.k.writes(mark); len(w) != 0 || len(g.rbacOf(t)) != 0 || len(g.changes(changes)) != 0 || strings.Contains(out, "up to date") {
		t.Fatalf("written: %v, record %v, changes %v:\n%s", w, g.rbacOf(t), g.changes(changes), out)
	}
	// --json takes --dry-run without --yes: AWS's {"dryRun":true,"targets":[…]} alone on stdout.
	var stdout, stderr strings.Builder
	g.d.Out, g.d.Err = &stdout, &stderr
	r.JSON = true
	if code := WorkloadsAccessGrant(context.Background(), g.d, r); code != 0 || strings.TrimSpace(stdout.String()) != `{"dryRun":true,"targets":["shop/dormeto-shop"]}` {
		t.Fatalf("--json: exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	g.d.Out, g.d.Err = g.out, g.out
	r.JSON = false
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	mark, changes = len(g.k.callList()), len(g.s.Calls())
	code, out = g.call(t, WorkloadsAccessRevoke, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "- RoleBinding dormeto-shop", "- Role dormeto-shop", "dry run: would remove these from namespace shop of cluster europe-west1-b/shop-cluster, nothing sent\n")
	if w := g.k.writes(mark); len(w) != 0 || len(g.rbacOf(t)) != 1 || len(g.changes(changes)) != 0 {
		t.Fatalf("written: %v, record %v, changes %v", w, g.rbacOf(t), g.changes(changes))
	}
}
