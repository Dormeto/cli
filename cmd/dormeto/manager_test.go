package main

// `dormeto manager` against a fake manager: the update's state and daily check, the release notes, and the update
// planned, confirmed, applied and followed (its records read through GET /operations), with what is sent and printed.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// managerAPI is a fake manager for `dormeto manager`. A plan or apply request is recorded as an update-manager operation
// that GET /operations reports running, then ended; GET /update answers state, then planned once the plan has ended, then
// applied once the apply has; /health answers 0.17.1 until the apply has ended, then runs.
type managerAPI struct {
	mu       sync.Mutex
	requests []string // "METHOD requestURI body"
	state    string   // GET /update before the plan ends
	planned  string   // GET /update once the plan has ended (state when "")
	applied  string   // GET /update once the apply has ended (planned when "")
	check    string   // GET /update/check ("" answers 404)
	changes  string   // GET /update/changes ("" answers 404, as a manager installed with Terraform does)
	planEnd  string   // how the plan's operation ends: succeeded (default) or failed
	applyEnd string   // how the apply's operation ends: succeeded (default) or failed
	outage   int      // reads of GET /operations answered 502 once the apply is requested
	busy     int      // reads answered 503 (Retry-After: 0, so that the client gives up at once) after the 502s
	runs     string   // the version /health answers once the apply has ended (0.18.1 when "")
	refusal  string   // POST /update/plan is refused (400) with this message when set

	phase   string // "", planning, planned, applying, applied
	version string // the version requested
	reads   int    // reads of the current operation
	gone    int    // 502s and 503s answered
}

func (m *managerAPI) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		m.requests = append(m.requests, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /iam/health":
			v := "0.17.1"
			if m.phase == "applied" {
				v = orString(m.runs, "0.18.1")
			}
			fmt.Fprintf(w, `{"status":"ok","version":%q}`, v)
		case "GET /iam/update":
			io.WriteString(w, m.stateNow())
		case "GET /iam/update/check":
			if m.check == "" {
				w.WriteHeader(404)
				io.WriteString(w, `{"error":"not_found","message":"no update check in this manager"}`)
				return
			}
			io.WriteString(w, m.check)
		case "PUT /iam/update/check":
			var b struct{ Enabled, Alert bool }
			_ = json.Unmarshal(body, &b)
			fmt.Fprintf(w, `{"enabled":%t,"alert":%t,"updatedAt":"2026-10-19T12:00:00Z","updatedBy":"arn:aws:sts::1:assumed-role/Ci/run-42","newer":false,"result":null}`, b.Enabled, b.Alert)
		case "GET /iam/update/changes":
			if m.changes == "" { // the route of a manager installed with the CLI only
				w.WriteHeader(404)
				io.WriteString(w, `{"error":"not_found","message":"no route for GET /iam/update/changes"}`)
				return
			}
			io.WriteString(w, m.changes)
		case "POST /iam/update/plan":
			if m.refusal != "" {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error":"bad_request","message":%q}`, m.refusal)
				return
			}
			var b struct{ Version string }
			_ = json.Unmarshal(body, &b)
			m.version, m.phase, m.reads = b.Version, "planning", 0
			w.WriteHeader(202)
			fmt.Fprintf(w, `{"key":"prod/requests/r1.json","id":"r1","etag":"e1","action":"plan","version":%q,"at":"2026-10-19T12:00:00Z","who":"arn","operation":"op-plan"}`, b.Version)
		case "POST /iam/update/apply":
			var b struct{ PlanBuild string }
			_ = json.Unmarshal(body, &b)
			m.phase, m.reads = "applying", 0
			w.WriteHeader(202)
			fmt.Fprintf(w, `{"key":"prod/requests/r2.json","id":"r2","etag":"e2","action":"apply","version":%q,"planBuild":%q,"at":"2026-10-19T12:05:00Z","who":"arn","operation":"op-apply"}`, m.version, b.PlanBuild)
		case "GET /iam/operations":
			m.operations(w)
		default:
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error":"not_found","message":"no route for %s %s"}`, r.Method, r.URL.Path)
		}
	}
}

func (m *managerAPI) stateNow() string {
	switch m.phase {
	case "planned", "applying":
		return orString(m.planned, m.state)
	case "applied":
		return orString(m.applied, orString(m.planned, m.state))
	}
	return m.state
}

// operations answers GET /operations with the current request's operation: running at the first read, ended at the
// next; once the apply is requested, its first reads answer 502 while outage lasts, then 503 while busy does.
func (m *managerAPI) operations(w http.ResponseWriter) {
	id, step, end, action := "op-plan", "Plan manager "+m.version, orString(m.planEnd, "succeeded"), "plan"
	if m.phase == "applying" || m.phase == "applied" {
		switch {
		case m.phase == "applying" && m.outage > 0:
			m.outage--
			m.gone++
			w.WriteHeader(502)
			io.WriteString(w, `{"message":"Internal server error"}`)
			return
		case m.phase == "applying" && m.busy > 0:
			m.busy--
			m.gone++
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
			io.WriteString(w, `{"message":"Service Unavailable"}`)
			return
		}
		id, step, end, action = "op-apply", "Apply manager "+m.version, orString(m.applyEnd, "succeeded"), "apply"
	}
	m.reads++
	status, stepStatus, stepEnd, opEnd := "running", "running", "", ""
	if m.reads >= 2 {
		status, stepStatus = end, "done"
		stepEnd = `,"finishedAt":"2026-10-19T12:02:00Z"`
		opEnd = stepEnd
		if end == "failed" {
			stepStatus = "failed"
			opEnd += `,"error":"terraform ` + action + ` failed"`
		}
		switch m.phase {
		case "planning":
			m.phase = "planned"
		case "applying":
			m.phase = "applied"
		}
	}
	fmt.Fprintf(w, `{"operations":[{"id":"other","type":"onboard","status":"running","target":{},"steps":[]},
	 {"id":%q,"type":"update-manager","status":%q,"target":{"account":"111111111111","region":"eu-west-1","version":%q},
	  "steps":[{"name":%q,"status":%q,"startedAt":"2026-10-19T12:00:00Z"%s}],"by":"arn","createdAt":"2026-10-19T12:00:00Z"%s}]}`,
		id, status, m.version, step, stepStatus, stepEnd, opEnd)
}

// sent is the requests other than reads: "METHOD requestURI body".
func (m *managerAPI) sent() []string {
	var out []string
	for _, r := range m.requests {
		if !strings.HasPrefix(r, "GET ") {
			out = append(out, r)
		}
	}
	return out
}

func (m *managerAPI) has(prefix string) bool {
	for _, r := range m.requests {
		if strings.HasPrefix(r, prefix) {
			return true
		}
	}
	return false
}

func runManager(t *testing.T, m *managerAPI, tune func(*fleetDeps), args ...string) fleetRan {
	t.Helper()
	return runFleetWith(t, &fakeAPI{custom: m.handler()}, "eu-west-1", tune, append([]string{"manager"}, args...)...)
}

// updateState is a CLI install's GET /update answer, with plan and build as JSON (null for none).
func updateState(plan, build string) string {
	return `{"installedWithCli":true,"version":"0.17.1","env":"prod","region":"eu-west-1",
	 "applied":{"version":"0.17.1","build":"dmt-prod-install:0b1c0b1c-0000-4000-8000-000000000001","at":"2026-10-18T10:05:00Z"},"cli":"0.5.0",
	 "versions":[{"version":"0.18.1","router":"0.30.0","minCli":null,"published":"2026-10-19"},{"version":"0.18.0","router":"0.29.0","minCli":null,"published":"2026-10-10"}],
	 "build":` + build + `,"pending":null,"plan":` + plan + `}`
}

const (
	planBuild     = "dmt-prod-install:11111111-2222-4333-8444-555555555555"
	planOf0181    = `{"build":"` + planBuild + `","version":"0.18.1","summary":{"add":2,"change":1,"destroy":0,"replaced":["module.m.aws_lambda_function.api"],"destroyed":[]}}`
	planBuildDone = `{"id":"` + planBuild + `","by":"dashboard","action":"plan","version":"0.18.1","status":"SUCCEEDED","phase":"COMPLETED","startedAt":"2026-10-19T12:00:00Z","endedAt":"2026-10-19T12:02:00Z","steps":["plan"],"tail":[],"consoleUrl":null}`
	terraformOnly = `{"installedWithCli":false,"version":"0.17.1","env":"prod","region":"eu-west-1"}`
)

// updating is a fake manager whose update to 0.18.1 plans 2 to add, 1 to change, then applies.
func updating() *managerAPI {
	return &managerAPI{state: updateState("null", "null"), planned: updateState(planOf0181, planBuildDone), applied: strings.ReplaceAll(updateState("null", "null"), `"version":"0.17.1","env"`, `"version":"0.18.1","env"`)}
}

func TestManagerStatusOfACLIInstallWithAPlanPending(t *testing.T) {
	m := &managerAPI{
		state: strings.Replace(updateState(planOf0181, planBuildDone), `"pending":null`,
			`"pending":{"key":"k","id":"r","etag":"e","action":"plan","version":"0.18.0","at":"2026-10-19T11:58:00Z","who":"arn:aws:sts::1:assumed-role/Ci/run-42","operation":"op-9"}`, 1),
		check: `{"enabled":true,"alert":true,"newer":true,"result":{"checkedAt":"2026-10-19T06:00:00Z","running":"0.17.1","latest":"0.18.1"}}`,
	}
	r := runManager(t, m, nil, "status")
	t.Logf("\n%s", r.stdout)
	if r.exit != 0 {
		t.Fatalf("exit %d, stderr %q", r.exit, r.stderr)
	}
	for _, want := range []string{
		"version      0.17.1\n", "installed    with the CLI 0.5.0\n", "applied      0.17.1 (build dmt-prod-install:0b1c0b1c-0000-4000-8000-000000000001, 2026-10-18 10:05 UTC)\n",
		"newer        0.18.1  2026-10-19  router 0.30.0\n             0.18.0  2026-10-10  router 0.29.0\n",
		"pending      plan 0.18.0, requested by Ci/run-42 2026-10-19 11:58 UTC (operation op-9)\n",
		"plan         0.18.1, build " + planBuild + "\n", "changes      2 to add, 1 to change, 0 to destroy\n", "replaced     module.m.aws_lambda_function.api\n",
		"daily check  on, alert on\n", "last check   2026-10-19 06:00 UTC: 0.18.1 available\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	if got := strings.Join(m.requests, "\n"); got != "GET /iam/health \nGET /iam/update \nGET /iam/update/check " {
		t.Errorf("requests:\n%s", got)
	}

	r = runManager(t, m, nil, "status", "--json")
	var got struct {
		Version string
		Update  struct{ Plan struct{ Build string } }
		Check   struct{ Alert bool }
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || r.exit != 0 || got.Version != "0.17.1" || got.Update.Plan.Build != planBuild || !got.Check.Alert {
		t.Errorf("%v, exit %d: %q", err, r.exit, r.stdout)
	}
}

func TestManagerStatusOfATerraformInstall(t *testing.T) {
	m := &managerAPI{state: terraformOnly}
	r := runManager(t, m, nil, "status")
	want := "version      0.17.1\n" +
		"installed    with Terraform: change its module's source to update it\n" +
		"env          prod\n" +
		"region       eu-west-1\n" +
		"daily check  not available\n"
	if r.exit != 0 || r.stdout != want || r.stderr != "" {
		t.Errorf("exit %d, stdout\n%s\nstderr %q", r.exit, r.stdout, r.stderr)
	}
	r = runManager(t, m, nil, "status", "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || got["check"] != nil || got["update"].(map[string]any)["installedWithCli"] != false {
		t.Errorf("%v: %q", err, r.stdout)
	}
}

func TestManagerCheckRefusalsSendNothing(t *testing.T) {
	for _, args := range [][]string{
		{"check", "--disable", "--alert"},
		{"check", "--disable", "--no-alert"},
		{"check"},
		{"check", "--alert"},
		{"check", "--enable", "--disable"},
		{"check", "--enable", "--alert", "--no-alert"},
		{"check", "--enable", "extra"},
	} {
		m := &managerAPI{check: `{"enabled":true,"alert":true,"result":null}`}
		r := runManager(t, m, nil, args...)
		if r.exit != 2 || r.stderr == "" || len(m.requests) != 0 {
			t.Errorf("%v: exit %d, stderr %q, requests %q", args, r.exit, r.stderr, m.requests)
		}
	}
	r := runManager(t, &managerAPI{}, nil, "check", "--disable", "--alert")
	if r.stderr != "--alert and --no-alert go with --enable\n" {
		t.Errorf("stderr %q", r.stderr)
	}
}

func TestManagerCheckEnableKeepsTheAlert(t *testing.T) {
	m := &managerAPI{check: `{"enabled":true,"alert":true,"newer":false,"result":null}`}
	r := runManager(t, m, nil, "check", "--enable")
	if r.exit != 0 || strings.Join(m.requests, "\n") != "GET /iam/update/check \nPUT /iam/update/check {\"alert\":true,\"enabled\":true}" {
		t.Fatalf("exit %d, requests %q, stderr %q", r.exit, m.requests, r.stderr)
	}
	if r.stdout != "daily check  on, alert on\nlast check   not checked yet\n" {
		t.Errorf("stdout %q", r.stdout)
	}
	for args, body := range map[string]string{
		"--enable --no-alert": `{"alert":false,"enabled":true}`,
		"--enable --alert":    `{"alert":true,"enabled":true}`,
		"--disable":           `{"alert":false,"enabled":false}`,
	} {
		m := &managerAPI{check: `{"enabled":true,"alert":true,"result":null}`}
		r := runManager(t, m, nil, append([]string{"check"}, strings.Fields(args)...)...)
		if r.exit != 0 || strings.Join(m.requests, "\n") != "PUT /iam/update/check "+body {
			t.Errorf("%s: exit %d, requests %q, stderr %q", args, r.exit, m.requests, r.stderr)
		}
	}
	m = &managerAPI{}
	r = runManager(t, m, nil, "check", "--disable", "--json")
	var got struct{ Enabled, Alert bool }
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || r.exit != 0 || got.Enabled || got.Alert {
		t.Errorf("%v, exit %d: %q", err, r.exit, r.stdout)
	}
	r = runManager(t, &managerAPI{}, nil, "check", "--enable")
	if r.exit != 1 || r.stderr != "no update check in this manager\n" {
		t.Errorf("no check: exit %d, stderr %q", r.exit, r.stderr)
	}
}

const sections = `[{"version":"0.18.1","notes":"## 0.18.1 — 2026-10-19\n\n### Fixed\n- a fix","access":null,"accessNeeded":false},{"version":"0.18.0","notes":null,"access":null,"accessNeeded":false}]`

func TestManagerChangesDefaultsToTheNewest(t *testing.T) {
	m := &managerAPI{state: updateState("null", "null"), changes: sections}
	r := runManager(t, m, nil, "changes")
	if r.exit != 0 || strings.Join(m.requests, "\n") != "GET /iam/update \nGET /iam/update/changes?to=0.18.1 " {
		t.Fatalf("exit %d, requests %q, stderr %q", r.exit, m.requests, r.stderr)
	}
	if r.stdout != "## 0.18.1 — 2026-10-19\n\n### Fixed\n- a fix\n\n## 0.18.0\nrelease notes unavailable\n" {
		t.Errorf("stdout %q", r.stdout)
	}
	m = &managerAPI{state: updateState("null", "null"), changes: sections}
	r = runManager(t, m, nil, "changes", "--to", "0.18.0", "--json")
	var got []map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || r.exit != 0 || len(got) != 2 || strings.Join(m.requests, "\n") != "GET /iam/update/changes?to=0.18.0 " {
		t.Errorf("%v, exit %d, requests %q: %q", err, r.exit, m.requests, r.stdout)
	}
	m = &managerAPI{state: strings.Replace(updateState("null", "null"), `"versions":[`, `"versions":[],"was":[`, 1)}
	r = runManager(t, m, nil, "changes")
	if r.exit != 0 || r.stdout != "the manager is up to date (0.17.1)\n" || len(m.requests) != 1 {
		t.Errorf("up to date: exit %d, stdout %q, requests %q", r.exit, r.stdout, m.requests)
	}
	m = &managerAPI{state: terraformOnly}
	r = runManager(t, m, nil, "changes")
	if r.exit != 1 || !strings.Contains(r.stderr, "installed with Terraform") || len(m.requests) != 1 {
		t.Errorf("terraform: exit %d, stderr %q, requests %q", r.exit, r.stderr, m.requests)
	}
	m = &managerAPI{state: terraformOnly} // no /update/changes route in a manager installed with Terraform
	r = runManager(t, m, nil, "changes", "--to", "0.18.1")
	if r.exit != 1 || r.stderr != "installed with Terraform: its release notes are at "+managerChangelog+"\n" {
		t.Errorf("terraform --to: exit %d, stderr %q", r.exit, r.stderr)
	}
	m = &managerAPI{}
	r = runManager(t, m, nil, "changes", "--to", "v1")
	if r.exit != 2 || r.stderr != "--to: \"v1\" is not a version such as 1.2.3\n" || len(m.requests) != 0 {
		t.Errorf("bad --to: exit %d, stderr %q, requests %q", r.exit, r.stderr, m.requests)
	}
}

func TestManagerUpdateDryRunPlansPrintsTheSummaryNeverApplies(t *testing.T) {
	m := updating()
	r := runManager(t, m, nil, "update", "--dry-run")
	t.Logf("\n%s\n%s", r.stdout, r.stderr)
	if r.exit != 0 {
		t.Fatalf("exit %d, stderr %q", r.exit, r.stderr)
	}
	if got := strings.Join(m.sent(), "\n"); got != `POST /iam/update/plan {"version":"0.18.1"}` {
		t.Errorf("sent %q", got)
	}
	want := "started update-manager op-plan for plan 0.18.1\n" +
		"✓ plan 0.18.1: update-manager succeeded in 2m0s\n" +
		"plan      0.18.1, build " + planBuild + "\n" +
		"changes   2 to add, 1 to change, 0 to destroy\n" +
		"replaced  module.m.aws_lambda_function.api\n" +
		"dry run: the plan of 0.18.1 is not applied\n"
	if r.stdout != want {
		t.Errorf("stdout\n%s\nwant\n%s", r.stdout, want)
	}
	if r.stderr != "  … Plan manager 0.18.1\n  ✓ Plan manager 0.18.1 (2m0s)\n" {
		t.Errorf("stderr %q", r.stderr)
	}
	if !m.has("GET /iam/operations") || m.has("GET /iam/operations/") {
		t.Errorf("the operation is not read through the list: %q", m.requests)
	}

	m = updating()
	r = runManager(t, m, nil, "update", "--dry-run", "--json")
	var got struct {
		Update     struct{ Plan struct{ Version string } }
		Operations []struct{ ID, Status string }
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || r.exit != 0 || got.Update.Plan.Version != "0.18.1" || len(got.Operations) != 1 || got.Operations[0].Status != "succeeded" {
		t.Errorf("%v, exit %d: %q", err, r.exit, r.stdout)
	}
	if !strings.Contains(r.stderr, "changes   2 to add") {
		t.Errorf("the summary is not on stderr: %q", r.stderr)
	}
}

func TestManagerUpdateYesAppliesThePlansBuildAndWaitsOutTwo502s(t *testing.T) {
	m := updating()
	m.outage = 2
	r := runManager(t, m, nil, "update", "--yes")
	t.Logf("\n%s\n%s", r.stdout, r.stderr)
	if r.exit != 0 {
		t.Fatalf("exit %d, stderr %q", r.exit, r.stderr)
	}
	want := []string{`POST /iam/update/plan {"version":"0.18.1"}`, `POST /iam/update/apply {"planBuild":"` + planBuild + `"}`}
	if got := m.sent(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sent %q", got)
	}
	if m.gone != 2 {
		t.Errorf("%d 502s answered", m.gone)
	}
	for _, want := range []string{"started update-manager op-apply for apply 0.18.1\n", "✓ apply 0.18.1: update-manager succeeded in 2m0s\n"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	if !strings.HasSuffix(r.stdout, "manager updated to 0.18.1\n") || !strings.HasSuffix(m.requests[len(m.requests)-1], "GET /iam/health ") {
		t.Errorf("stdout %q, last request %q", r.stdout, m.requests[len(m.requests)-1])
	}

	m = updating()
	r = runManager(t, m, nil, "update", "--yes", "--version", "0.18.1", "--json")
	var got struct {
		Update     struct{ Version string }
		Operations []struct{ ID, Status string }
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || r.exit != 0 || got.Update.Version != "0.18.1" || len(got.Operations) != 2 || got.Operations[1].ID != "op-apply" || got.Operations[1].Status != "succeeded" {
		t.Errorf("%v, exit %d: %q", err, r.exit, r.stdout)
	}
}

// Six failed reads in a row (two 503s more than the client's own three tries): the following waits them out.
func TestManagerUpdateWaitsOutAnOutageLongerThanTheClientsRetries(t *testing.T) {
	m := updating()
	m.busy = 6
	sleeps := 0
	r := runManager(t, m, func(d *fleetDeps) {
		d.sleep = func(context.Context, time.Duration) error { sleeps++; return nil }
	}, "update", "--yes")
	if r.exit != 0 || m.gone != 6 || !strings.HasSuffix(r.stdout, "manager updated to 0.18.1\n") {
		t.Fatalf("exit %d, %d failed reads, stdout %q, stderr %q", r.exit, m.gone, r.stdout, r.stderr)
	}
	if sleeps != 4 { // plan: 1; apply: 2 reads given up, then 1 between running and succeeded
		t.Errorf("%d waits between reads", sleeps)
	}
}

func TestManagerUpdateSaysWhenTheOldVersionStillRuns(t *testing.T) {
	m := updating()
	m.runs = "0.17.1"
	r := runManager(t, m, nil, "update", "--yes")
	if r.exit != 1 || !strings.Contains(r.stderr, "the apply succeeded but the manager still runs 0.17.1") || strings.Contains(r.stdout, "manager updated") {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
}

func TestManagerUpdateFailedPlanExits1WithoutApplying(t *testing.T) {
	m := updating()
	m.planEnd = "failed"
	m.planned = updateState("null", `{"id":"dmt-prod-install:f","by":"dashboard","action":"plan","version":"0.18.1","status":"FAILED","phase":"BUILD","startedAt":"2026-10-19T12:00:00Z","endedAt":"2026-10-19T12:01:00Z","steps":["init","plan"],"tail":["Error: no provider"],"consoleUrl":"https://eu-west-1.console.aws.amazon.com/f"}`)
	r := runManager(t, m, nil, "update", "--yes")
	if r.exit != 1 || m.has("POST /iam/update/apply") {
		t.Fatalf("exit %d, requests %q", r.exit, m.requests)
	}
	for _, want := range []string{
		"✗ plan 0.18.1: update-manager failed at Plan manager 0.18.1: terraform plan failed (operation op-plan)\n",
		"the step that failed: plan\n  Error: no provider\nthe build: https://eu-west-1.console.aws.amazon.com/f\n",
	} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q: %q", want, r.stderr)
		}
	}
}

func TestManagerUpdateFailedApplySaysWhatItLeft(t *testing.T) {
	m := updating()
	m.applyEnd = "failed"
	m.applied = updateState("null", `{"id":"dmt-prod-install:a","by":"dashboard","action":"apply","version":"0.18.1","status":"FAILED","phase":"BUILD","startedAt":"2026-10-19T12:05:00Z","endedAt":"2026-10-19T12:09:00Z","steps":["apply"],"tail":["Error: timeout"],"consoleUrl":"https://eu-west-1.console.aws.amazon.com/a"}`)
	r := runManager(t, m, nil, "update", "--yes")
	if r.exit != 1 || m.has("GET /iam/health") || strings.Contains(r.stdout, "manager updated") {
		t.Fatalf("exit %d, stdout %q, requests %q", r.exit, r.stdout, m.requests)
	}
	want := "✗ apply 0.18.1: update-manager failed at Apply manager 0.18.1: terraform apply failed (operation op-apply)\n" +
		"the step that failed: apply\n  Error: timeout\nthe build: https://eu-west-1.console.aws.amazon.com/a\n" +
		"the manager is as the failure left it: run dormeto manager update again, or dormeto install from a laptop\n"
	if !strings.HasSuffix(r.stderr, want) {
		t.Errorf("stderr %q", r.stderr)
	}
}

func TestManagerUpdateNeverAppliesAPlanOfAnotherVersion(t *testing.T) {
	m := updating()
	m.planned = updateState(strings.Replace(planOf0181, `"version":"0.18.1"`, `"version":"0.18.0"`, 1), planBuildDone)
	r := runManager(t, m, nil, "update", "--yes")
	if r.exit != 1 || m.has("POST /iam/update/apply") || !strings.Contains(r.stderr, "the manager's plan is of 0.18.0, not 0.18.1: nothing applied") {
		t.Errorf("exit %d, stderr %q, requests %q", r.exit, r.stderr, m.requests)
	}
	m = updating()
	m.planned = updateState("null", planBuildDone)
	r = runManager(t, m, nil, "update", "--yes")
	if r.exit != 1 || m.has("POST /iam/update/apply") || !strings.Contains(r.stderr, "the manager shows no plan of 0.18.1 to apply") {
		t.Errorf("no plan: exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestManagerUpdateConfirmsNamingTheVersionAndTheCounts(t *testing.T) {
	var asked string
	m := updating()
	r := runManager(t, m, func(d *fleetDeps) {
		d.terminal = func(io.Reader) bool { return true }
		d.confirm = func(_ context.Context, _ io.Reader, _ io.Writer, q string) (bool, error) {
			asked = q
			return false, nil
		}
	}, "update")
	if r.exit != 0 || m.has("POST /iam/update/apply") || !strings.HasSuffix(r.stdout, "not confirmed: nothing sent\n") {
		t.Errorf("exit %d, stdout %q, requests %q", r.exit, r.stdout, m.requests)
	}
	if asked != "Update the manager from 0.17.1 to 0.18.1 (2 to add, 1 to change, 0 to destroy)?" {
		t.Errorf("asked %q", asked)
	}
}

func TestManagerUpdateOfATerraformInstall(t *testing.T) {
	for args, want := range map[string]string{
		"update --yes":                     "installed with Terraform: change its module's source to the version you want\n",
		"update --yes --version 0.18.1":    "installed with Terraform: change its module's source to 0.18.1\n",
		"update --dry-run --version 1.0.0": "installed with Terraform: change its module's source to 1.0.0\n",
	} {
		m := &managerAPI{state: terraformOnly}
		r := runManager(t, m, nil, strings.Fields(args)...)
		if r.exit != 1 || r.stderr != want || len(m.sent()) != 0 {
			t.Errorf("%s: exit %d, stderr %q, sent %q", args, r.exit, r.stderr, m.sent())
		}
	}
}

func TestManagerUpdateUpToDate(t *testing.T) {
	m := &managerAPI{state: strings.Replace(updateState("null", "null"), `"versions":[`, `"versions":[{"version":"0.17.0"}],"was":[`, 1)}
	r := runManager(t, m, nil, "update", "--yes")
	if r.exit != 0 || r.stdout != "the manager is up to date (0.17.1)\n" || len(m.sent()) != 0 {
		t.Errorf("exit %d, stdout %q, sent %q", r.exit, r.stdout, m.sent())
	}
	m = &managerAPI{state: `{"installedWithCli":true,"version":"0.17.1","env":"prod","region":"eu-west-1","versions":[],"releasesUnreachable":true}`}
	r = runManager(t, m, nil, "update", "--yes")
	if r.exit != 1 || r.stderr != "the releases could not be read: try again later\n" || len(m.sent()) != 0 {
		t.Errorf("unreachable: exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestManagerUpdateRefusalsSendNothing(t *testing.T) {
	for args, want := range map[string]string{
		"update":                          "confirmation needed: pass --yes to update the manager without a terminal\n",
		"update --yes --version 1.2":      "--version: \"1.2\" is not a version such as 1.2.3\n",
		"update --yes --timeout 0s":       "--timeout: 0s is not a positive duration\n",
		"update --yes extra":              "unexpected argument \"extra\"\n",
		"update --dry-run --version v1.0": "--version: \"v1.0\" is not a version such as 1.2.3\n",
	} {
		m := updating()
		r := runManager(t, m, nil, strings.Fields(args)...)
		if r.exit != 2 || r.stderr != want || len(m.requests) != 0 {
			t.Errorf("%s: exit %d, stderr %q, requests %q", args, r.exit, r.stderr, m.requests)
		}
	}
	// The manager's own refusal of the plan is printed as it words it.
	m := updating()
	m.refusal = "9.9.9 is not a version to update to"
	r := runManager(t, m, nil, "update", "--version", "9.9.9", "--dry-run")
	if r.exit != 1 || r.stderr != "✗ plan 9.9.9: 9.9.9 is not a version to update to\n" || m.has("GET /iam/operations") {
		t.Errorf("refused plan: exit %d, stderr %q, requests %q", r.exit, r.stderr, m.requests)
	}
}
