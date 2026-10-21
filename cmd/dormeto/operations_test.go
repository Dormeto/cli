package main

// `dormeto operations list|show|watch` and the readings (`overview`, `alerts`, `ip-lists`, `savings`, `audit`) against
// a fake manager: the requests they make, what they print, the exit codes, and that a refused argument sends nothing.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// readAPI is a fake manager for the readings: the JSON each GET route answers (404 for a route not named), the reads
// of an operation in turn, and every request received.
type readAPI struct {
	mu        sync.Mutex
	routes    map[string]string   // path (without /iam) -> answer; "status:<path>" -> the status to answer it with
	operation map[string][]string // GET /operations/<id> -> the answers, in turn, the last repeated
	requests  []string            // "METHOD path?query body"
	writes    []string
}

func (a *readAPI) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		a.requests = append(a.requests, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		if r.Method != http.MethodGet {
			a.writes = append(a.writes, r.Method+" "+r.URL.Path+" "+string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		path := strings.TrimPrefix(r.URL.Path, "/iam")
		if answers, ok := a.operation[path]; ok && r.Method == http.MethodGet {
			io.WriteString(w, answers[0])
			if len(answers) > 1 {
				a.operation[path] = answers[1:]
			}
			return
		}
		if s := a.routes["status:"+path]; s != "" {
			w.WriteHeader(map[string]int{"500": 500, "403": 403}[s])
			io.WriteString(w, `{"error":"x","message":"the route failed"}`)
			return
		}
		answer, ok := a.routes[r.Method+" "+path]
		if !ok && r.Method == http.MethodGet {
			answer, ok = a.routes[path]
		}
		if !ok {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"not_found","message":"no route"}`)
			return
		}
		io.WriteString(w, answer)
	}
}

func runReading(t *testing.T, a *readAPI, tune func(*fleetDeps), args ...string) fleetRan {
	t.Helper()
	return runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", tune, args...)
}

const operationsList = `{"operations":[
 {"id":"op-1","type":"onboard","status":"succeeded","target":{"targetGroupArn":"arn:aws:elasticloadbalancing:eu-west-1:111111111111:targetgroup/api/0123456789abcdef"},"steps":[{"name":"onboard","status":"done"},{"name":"verify","status":"done"}],"by":"arn:aws:sts::1:assumed-role/Ci/run-42","createdAt":"2026-10-19T08:00:00Z"},
 {"id":"op-2","type":"upgrade-router","status":"running","target":{"router":"prod-eu"},"steps":[{"name":"install","status":"running"}],"createdAt":"2026-10-19T11:00:00Z"},
 {"id":"op-3","type":"onboard","status":"running","target":{"router":"prod-eu","workloadId":"w-9"},"steps":[{"name":"preview","status":"done"},{"name":"onboard","status":"running"}],"by":"arn:aws:sts::1:assumed-role/Ops/ana","createdAt":"2026-10-19T11:30:00Z"},
 {"id":"op-4","type":"onboard","status":"failed","target":{"router":"prod-eu","workloadId":"w-8"},"steps":[],"createdAt":"2026-10-18T11:30:00Z"}
]}`

func TestOperationsListFiltersAndSortsNewestFirst(t *testing.T) {
	a := &readAPI{routes: map[string]string{"/operations": operationsList}}
	r := runReading(t, a, nil, "operations", "list", "--running", "--type", "onboard")
	t.Logf("\n%s", r.stdout)
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[1], "op-3") || !strings.Contains(lines[1], "prod-eu/w-9") ||
		!strings.Contains(lines[1], "onboard") || !strings.Contains(lines[1], "Ops/ana") || !strings.Contains(lines[1], "30m") {
		t.Errorf("stdout %q", r.stdout)
	}
	if strings.Contains(r.stdout, "op-1") || strings.Contains(r.stdout, "op-2") || strings.Contains(r.stdout, "op-4") {
		t.Errorf("stdout has operations that do not match: %q", r.stdout)
	}
}

func TestOperationsListDefaultsAndLimit(t *testing.T) {
	a := &readAPI{routes: map[string]string{"/operations": operationsList}}
	r := runReading(t, a, nil, "operations", "list")
	order := []int{strings.Index(r.stdout, "op-3"), strings.Index(r.stdout, "op-2"), strings.Index(r.stdout, "op-1"), strings.Index(r.stdout, "op-4")}
	if r.exit != 0 || order[0] < 0 || !(order[0] < order[1] && order[1] < order[2] && order[2] < order[3]) {
		t.Errorf("exit %d, order %v, stdout %q", r.exit, order, r.stdout)
	}
	if !strings.Contains(r.stdout, "Ci/run-42") || strings.Contains(r.stdout, "arn:aws:sts") || !strings.Contains(r.stdout, "api") {
		t.Errorf("stdout %q", r.stdout)
	}
	r = runReading(t, a, nil, "operations", "list", "--limit", "2", "--json")
	var got struct{ Operations []struct{ ID string } } // the API's object, its rows after the filters
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || len(got.Operations) != 2 || got.Operations[0].ID != "op-3" || got.Operations[1].ID != "op-2" {
		t.Errorf("%v: %q", err, r.stdout)
	}
	r = runReading(t, a, nil, "operations", "list", "--limit", "0", "--type", "onboard,upgrade-router", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || len(got.Operations) != 4 {
		t.Errorf("%v: %q", err, r.stdout)
	}
	r = runReading(t, a, nil, "operations", "list", "--type", "lift-exclusion", "--json")
	if strings.TrimSpace(r.stdout) != "{\n  \"operations\": []\n}" {
		t.Errorf("none: %q", r.stdout)
	}
	for _, args := range [][]string{{"operations", "list", "--limit", "-1"}, {"operations", "list", "extra"}} {
		b := &readAPI{routes: map[string]string{"/operations": operationsList}}
		if r := runReading(t, b, nil, args...); r.exit != 2 || len(b.requests) != 0 {
			t.Errorf("%v: exit %d, %d requests", args, r.exit, len(b.requests))
		}
	}
}

const failedOperation = `{"id":"op-9","type":"offboard","status":"failed","target":{"router":"prod-eu","workloadId":"w-1"},"error":"offboard failed",
 "steps":[{"name":"locate","status":"done","startedAt":"2026-10-19T10:00:00Z","finishedAt":"2026-10-19T10:00:04Z"},{"name":"pause","status":"failed","detail":"the router did not answer"},{"name":"candidate","status":"pending"}],
 "by":"arn:aws:sts::1:assumed-role/Ci/run-42","createdAt":"2026-10-19T10:00:00Z","finishedAt":"2026-10-19T10:01:00Z"}`

func TestOperationsWatchOfAFailedOneExits1WithTheStepsDetail(t *testing.T) {
	a := &readAPI{routes: map[string]string{"/operations/op-9": failedOperation}}
	r := runReading(t, a, nil, "operations", "watch", "op-9")
	if r.exit != 1 || !strings.Contains(r.stderr, "offboard failed at pause: the router did not answer (operation op-9)") {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
	if len(a.requests) != 1 {
		t.Errorf("requests %v: a finished operation is read once", a.requests)
	}
}

func TestOperationsWatchFollowsARunningOneToItsEnd(t *testing.T) {
	running := `{"id":"op-5","type":"install-router","status":"running","target":{"account":"111111111111","region":"eu-west-1"},"steps":[{"name":"find router","status":"running"}],"createdAt":"2026-10-19T11:58:00Z"}`
	later := `{"id":"op-5","type":"install-router","status":"running","target":{"account":"111111111111","region":"eu-west-1"},"steps":[{"name":"find router","status":"done","startedAt":"2026-10-19T11:58:00Z","finishedAt":"2026-10-19T11:58:12Z"},{"name":"lock router","status":"running"}]}`
	done := `{"id":"op-5","type":"install-router","status":"succeeded","target":{"account":"111111111111","region":"eu-west-1"},"steps":[{"name":"find router","status":"done","startedAt":"2026-10-19T11:58:00Z","finishedAt":"2026-10-19T11:58:12Z"},{"name":"lock router","status":"done"}],"createdAt":"2026-10-19T11:58:00Z","finishedAt":"2026-10-19T12:00:00Z"}`
	a := &readAPI{operation: map[string][]string{"/operations/op-5": {running, running, later, done}}}
	r := runReading(t, a, nil, "operations", "watch", "op-5")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	for _, want := range []string{"  … find router", "  ✓ find router (12s)", "  … lock router"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q: %q", want, r.stderr)
		}
	}
	if !strings.Contains(r.stdout, "✓ 111111111111/eu-west-1: install-router succeeded in 2m0s") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestOperationsWatchOfAManagerUpdateReadsTheList(t *testing.T) {
	update := `{"id":"op-u","type":"update-manager","status":"running","target":{"version":"0.20.0"},"steps":[{"name":"build","status":"running"}]}`
	a := &readAPI{
		operation: map[string][]string{"/operations/op-u": {update}},
		routes:    map[string]string{"/operations": `{"operations":[{"id":"op-u","type":"update-manager","status":"succeeded","target":{"version":"0.20.0"},"steps":[{"name":"build","status":"done"}],"createdAt":"2026-10-19T11:50:00Z","finishedAt":"2026-10-19T11:55:00Z"}]}`},
	}
	r := runReading(t, a, nil, "operations", "watch", "op-u")
	if r.exit != 0 || !strings.Contains(r.stdout, "update-manager succeeded in 5m0s") {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
	if len(a.requests) != 2 || !strings.HasPrefix(a.requests[0], "GET /iam/operations/op-u") || !strings.HasPrefix(a.requests[1], "GET /iam/operations ") {
		t.Errorf("requests %q", a.requests)
	}
}

func TestOperationsWatchStopsAtTheTimeoutAndSaysTheOperationGoesOn(t *testing.T) {
	running := `{"id":"op-5","type":"upgrade-router","status":"running","target":{"router":"prod-eu"},"steps":[{"name":"install","status":"running"}]}`
	a := &readAPI{operation: map[string][]string{"/operations/op-5": {running}}}
	tune := func(d *fleetDeps) {
		d.sleep = func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() }
		d.timeout = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
			c, cancel := context.WithCancel(ctx)
			go func() {
				time.Sleep(20 * time.Millisecond)
				cancel()
			}()
			return c, cancel
		}
	}
	r := runReading(t, a, tune, "operations", "watch", "op-5", "--timeout", "1m")
	if r.exit != 1 || !strings.Contains(r.stderr, "timeout of 1m0s reached") || !strings.Contains(r.stderr, "still running in the manager: dormeto operations watch op-5") {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
	b := &readAPI{}
	if r := runReading(t, b, nil, "operations", "watch", "op-5", "--timeout", "0s"); r.exit != 2 || len(b.requests) != 0 {
		t.Errorf("--timeout 0s: exit %d, %d requests", r.exit, len(b.requests))
	}
	if r := runReading(t, b, nil, "operations", "watch"); r.exit != 2 || len(b.requests) != 0 {
		t.Errorf("no id: exit %d, %d requests", r.exit, len(b.requests))
	}
}

func TestOperationsShowPrintsTheRecordAndItsSteps(t *testing.T) {
	a := &readAPI{routes: map[string]string{"/operations/op-9": failedOperation}}
	r := runReading(t, a, func(d *fleetDeps) {
		d.now = func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.FixedZone("CEST", 2*3600)) }
	}, "operations", "show", "op-9")
	t.Logf("\n%s", r.stdout)
	for _, want := range []string{"offboard", "prod-eu/w-1", "failed", "Ci/run-42", "2026-10-19 12:00 CEST", "offboard failed", "STEP", "pause", "the router did not answer", "4s"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	r = runReading(t, a, nil, "operations", "show", "op-9", "--json")
	var got map[string]any
	if r.exit != 0 || json.Unmarshal([]byte(r.stdout), &got) != nil || got["id"] != "op-9" {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
}
