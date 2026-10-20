package main

// `dormeto routers show|install|uninstall|pause|resume` against the fake manager API: the requests they make, what
// they print, the refusals that send nothing.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const routerDetailsAnswer = `{"name":"prod-eu","region":"eu-west-1","account":"111111111111","source":"manager",
 "router":{"name":"prod-eu","version":"0.31.0","build":"b7"},
 "configuration":{"checkSchedule":{"value":"rate(5 minutes)","source":"manager"},
  "ipLists":{"value":[{"name":"bots","url":"https://x/bots.txt","action":"block","refreshHours":6}],"source":"terraform"},
  "noWakeRules":{"value":[{"name":"health","level":"router","conditions":{"pathPrefixes":["/health"],"sourceIps":["10.0.0.0/8"]}}],"source":"terraform"},
  "metrics":{"value":true,"source":"terraform"}},
 "pauses":[{"scope":"router","by":"arn:aws:iam::1:user/ann","at":"2026-10-19T08:00:00.000Z","reason":"release freeze","until":"2026-10-21T08:00:00.000Z","keepAsleep":true}],
 "workloads":{"total":4,"byState":{"ASLEEP":3,"AWAKE":1},"paused":4,"lastActivityAt":"2026-10-19T11:30:00.000Z"},
 "health":{"status":"warning","problems":[{"level":"warning","code":"drift","text":"2 drift repairs in 24 hours"}],
  "checkSchedule":"rate(5 minutes)","lastScheduledCheckAt":"2026-10-19T11:55:00.000Z",
  "lambda":{"controller":{"errors":0,"throttles":1},"waker":{"errors":2,"throttles":0,"peakConcurrency":3}},
  "queues":{"check":0,"controller":5},"ipLists":[],"concurrency":{"limit":1000,"needed":40}}}`

// routersAPI is a fake manager's router routes.
type routersAPI struct {
	mu        sync.Mutex
	posted    []string // "path body"
	notPaused map[string]bool
	refuse    map[string]string // router -> the pause's 400 message
	noAccess  bool
	ops       []string
}

func (a *routersAPI) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	defer a.mu.Unlock()
	path := r.URL.EscapedPath()
	switch {
	case r.Method == http.MethodGet && path == "/iam/routers/prod-eu":
		io.WriteString(w, routerDetailsAnswer)
	case r.Method == http.MethodGet && path == "/iam/routers/odd%20one":
		io.WriteString(w, `{"name":"odd one","region":"eu-west-1","router":{"version":"0.31.0"},"workloads":{"total":0,"byState":{},"paused":0},"health":{"status":"ok","problems":[]},"pauses":[]}`)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/iam/routers/"):
		w.WriteHeader(404)
		io.WriteString(w, `{"error":"not_found","message":"unknown router"}`)
	case r.Method == http.MethodPost && path == "/iam/operations":
		a.posted = append(a.posted, "/operations "+string(body))
		var req struct{ Type, Router, Account string }
		_ = json.Unmarshal(body, &req)
		if a.noAccess {
			w.WriteHeader(409)
			io.WriteString(w, `{"error":"no_access","message":"no command role in account 123456789012"}`)
			return
		}
		a.ops = append(a.ops, req.Type)
		w.WriteHeader(202)
		fmt.Fprintf(w, `{"operation":{"id":"op-1","type":%q,"status":"running","target":{"router":%q,"account":%q,"region":"eu-west-1"},"steps":[]}}`, req.Type, req.Router, req.Account)
	case r.Method == http.MethodGet && path == "/iam/operations/op-1":
		fmt.Fprintf(w, `{"id":"op-1","type":%q,"status":"succeeded","target":{},"steps":[],"createdAt":"2026-10-19T08:00:00.000Z","finishedAt":"2026-10-19T08:01:00.000Z"}`, a.ops[0])
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/pause"):
		a.posted = append(a.posted, path+" "+string(body))
		router := strings.TrimSuffix(strings.TrimPrefix(path, "/iam/routers/"), "/pause")
		if msg := a.refuse[router]; msg != "" {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error":"bad_request","message":%q}`, msg)
			return
		}
		io.WriteString(w, `{"woken":["w-1","w-2"]}`)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/resume"):
		a.posted = append(a.posted, path+" ")
		router := strings.TrimSuffix(strings.TrimPrefix(path, "/iam/routers/"), "/resume")
		fmt.Fprintf(w, `{"resumed":%v}`, !a.notPaused[router])
	default:
		w.WriteHeader(404)
	}
}

func runRouters(t *testing.T, a *routersAPI, tune func(*fleetDeps), args ...string) fleetRan {
	t.Helper()
	return runFleetWith(t, &fakeAPI{custom: a.handler}, "eu-west-1", tune, append([]string{"routers"}, args...)...)
}

func TestRoutersShowPrintsFiguresPausesAndConfigurationWithSources(t *testing.T) {
	zone := time.FixedZone("CEST", 2*3600)
	tune := func(d *fleetDeps) { d.now = func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, zone) } }
	r := runRouters(t, &routersAPI{}, tune, "show", "prod-eu")
	t.Logf("\n%s", r.stdout)
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	for _, want := range []string{
		"prod-eu", "111111111111", "eu-west-1", "0.31.0", "b7", "warning", "2 drift repairs in 24 hours",
		"rate(5 minutes)", "2026-10-19 13:55 CEST", // the last scheduled check, in the zone of now
		"4 (3 ASLEEP, 1 AWAKE)", "4 paused", "release freeze", "2026-10-21 10:00 CEST", "keeps asleep",
		"bots (block, every 6 h)", "health: source 10.0.0.0/8, path /health*", "(terraform)", "(manager)",
		"controller-failures 5", "limit 1000",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
}

func TestRoutersShowPathEscapesTheNameAndJSONKeepsTheAnswer(t *testing.T) {
	r := runRouters(t, &routersAPI{}, nil, "show", "odd one", "--json")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || got["name"] != "odd one" {
		t.Fatalf("%v: %q", err, r.stdout)
	}
	if p := r.api.requests[len(r.api.requests)-1].URL.EscapedPath(); p != "/iam/routers/odd%20one" {
		t.Errorf("path %q", p)
	}
}

func TestRoutersShowUnknownRouterIsRefused(t *testing.T) {
	r := runRouters(t, &routersAPI{}, nil, "show", "nope")
	if r.exit != 1 || !strings.Contains(r.stderr, "unknown router") {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
	if r := runRouters(t, &routersAPI{}, nil, "show"); r.exit != 2 {
		t.Errorf("no router: exit %d", r.exit)
	}
}

func TestRoutersInstallPostsTheOperationAndFollowsIt(t *testing.T) {
	a := &routersAPI{}
	r := runRouters(t, a, nil, "install", "--account", "123456789012", "--router-region", "eu-west-1")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	if len(a.posted) != 1 || a.posted[0] != `/operations {"account":"123456789012","region":"eu-west-1","type":"install-router"}` {
		t.Errorf("posted %q", a.posted)
	}
	if !strings.Contains(r.stdout, "started install-router op-1") || !strings.Contains(r.stdout, "succeeded") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestRoutersInstallBadAccountOrRegionIsRefusedBeforeSending(t *testing.T) {
	for _, args := range [][]string{
		{"--account", "1234", "--router-region", "eu-west-1"},
		{"--account", "12345678901a", "--router-region", "eu-west-1"},
		{"--account", "123456789012", "--router-region", "europe"},
		{"--account", "123456789012", "--router-region", "EU-west-1"},
		{"--account", "123456789012"},
		{"--router-region", "eu-west-1"},
	} {
		a := &routersAPI{}
		r := runRouters(t, a, nil, append([]string{"install"}, args...)...)
		if r.exit != 2 || len(a.posted) != 0 || len(r.api.requests) != 0 {
			t.Errorf("%v: exit %d, posted %v, requests %d, stderr %q", args, r.exit, a.posted, len(r.api.requests), r.stderr)
		}
	}
}

func TestRoutersInstallNoAccessIsPrintedExit1(t *testing.T) {
	r := runRouters(t, &routersAPI{noAccess: true}, nil, "install", "--account", "123456789012", "--router-region", "eu-west-1")
	if r.exit != 1 || !strings.Contains(r.stderr, "no command role in account 123456789012") {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestRoutersUninstallNeedsYesWithoutATerminal(t *testing.T) {
	a := &routersAPI{}
	r := runRouters(t, a, nil, "uninstall", "prod-eu")
	if r.exit != 2 || len(a.posted) != 0 || !strings.Contains(r.stderr, "--yes") {
		t.Errorf("exit %d, posted %v, stderr %q", r.exit, a.posted, r.stderr)
	}
}

func TestRoutersUninstallDryRunSendsNothing(t *testing.T) {
	a := &routersAPI{}
	r := runRouters(t, a, nil, "uninstall", "prod-eu", "--dry-run")
	if r.exit != 0 || len(a.posted) != 0 || !strings.Contains(r.stdout, "dry run: 1 router") {
		t.Errorf("exit %d, posted %v, stdout %q", r.exit, a.posted, r.stdout)
	}
}

func TestRoutersUninstallAsksEvenForOneRouter(t *testing.T) {
	var asked []string
	a := &routersAPI{}
	r := runRouters(t, a, withTerminal(false, &asked), "uninstall", "prod-eu")
	if r.exit != 0 || len(asked) != 1 || len(a.posted) != 0 || !strings.Contains(r.stdout, "not confirmed: nothing sent") {
		t.Errorf("exit %d, asked %q, posted %v, stdout %q", r.exit, asked, a.posted, r.stdout)
	}
	r = runRouters(t, a, withTerminal(true, &asked), "uninstall", "prod-eu")
	if r.exit != 0 || len(a.posted) != 1 || a.posted[0] != `/operations {"router":"prod-eu","type":"uninstall-router"}` {
		t.Errorf("exit %d, posted %v, stderr %q", r.exit, a.posted, r.stderr)
	}
}

func TestRoutersUninstallYesStartsEachRouter(t *testing.T) {
	a := &routersAPI{}
	r := runRouters(t, a, nil, "uninstall", "prod-eu", "prod-us", "--yes", "--no-wait")
	if r.exit != 0 || len(a.posted) != 2 || !strings.Contains(r.stdout, "operations watch") {
		t.Errorf("exit %d, posted %v, stdout %q, stderr %q", r.exit, a.posted, r.stdout, r.stderr)
	}
}

func TestRoutersPausePostsReasonUntilAndKeepAsleep(t *testing.T) {
	a := &routersAPI{}
	r := runRouters(t, a, nil, "pause", "prod-eu", "--reason", "freeze", "--for", "2d", "--keep-asleep")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	want := `/iam/routers/prod-eu/pause {"reason":"freeze","until":"2026-10-21T12:00:00Z","keepAsleep":true}`
	if len(a.posted) != 1 || a.posted[0] != want {
		t.Errorf("posted %q, want %q", a.posted, want)
	}
	if !strings.Contains(r.stdout, "prod-eu: paused until 2026-10-21 12:00 UTC, keeping asleep") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestRoutersPauseWithoutEndPrintsTheWokenWorkloads(t *testing.T) {
	a := &routersAPI{}
	r := runRouters(t, a, nil, "pause", "prod-eu", "--reason", "freeze")
	if r.exit != 0 || a.posted[0] != `/iam/routers/prod-eu/pause {"reason":"freeze","keepAsleep":false}` {
		t.Fatalf("exit %d, posted %q, stderr %q", r.exit, a.posted, r.stderr)
	}
	if !strings.Contains(r.stdout, "prod-eu: paused, woke w-1, w-2") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestRoutersPauseBadReasonOrEndIsRefusedBeforeSending(t *testing.T) {
	for _, args := range [][]string{
		{"prod-eu"},
		{"prod-eu", "--reason", ""},
		{"prod-eu", "--reason", "   "},
		{"prod-eu", "--reason", strings.Repeat("x", 257)},
		{"prod-eu", "--reason", "x", "--for", "367d"},
		{"prod-eu", "--reason", "x", "--for", "1h", "--until", "23:00"},
		{"--reason", "x"},
	} {
		a := &routersAPI{}
		r := runRouters(t, a, nil, append([]string{"pause"}, args...)...)
		if r.exit != 2 || len(a.posted) != 0 || len(r.api.requests) != 0 {
			t.Errorf("%v: exit %d, posted %v, stderr %q", args, r.exit, a.posted, r.stderr)
		}
	}
	a := &routersAPI{}
	if r := runRouters(t, a, nil, "pause", "prod-eu", "--reason", strings.Repeat("x", 256), "--for", "366d"); r.exit != 0 {
		t.Errorf("256 characters and 366 days: exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestRoutersPauseSeveralOneRefusedTheOthersGoOn(t *testing.T) {
	a := &routersAPI{refuse: map[string]string{"r1": "router r1 does not support pause"}}
	r := runRouters(t, a, nil, "pause", "r1", "r2", "--reason", "x", "--yes")
	if r.exit != 1 || len(a.posted) != 2 {
		t.Fatalf("exit %d, posted %q", r.exit, a.posted)
	}
	if !strings.Contains(r.stderr, "r1: router r1 does not support pause") || !strings.Contains(r.stdout, "r2: paused") {
		t.Errorf("stdout %q, stderr %q", r.stdout, r.stderr)
	}
	b := &routersAPI{}
	if r := runRouters(t, b, nil, "pause", "r1", "r2", "--reason", "x"); r.exit != 2 || len(b.posted) != 0 {
		t.Errorf("without a terminal nor --yes: exit %d, posted %q", r.exit, b.posted)
	}
	if r := runRouters(t, b, nil, "pause", "r1", "r2", "--reason", "x", "--dry-run"); r.exit != 0 || len(b.posted) != 0 {
		t.Errorf("dry run: exit %d, posted %q", r.exit, b.posted)
	}
}

func TestRoutersResumeSaysResumedOrNotPaused(t *testing.T) {
	a := &routersAPI{notPaused: map[string]bool{"r2": true}}
	r := runRouters(t, a, nil, "resume", "r1", "r2", "--yes")
	if r.exit != 0 || len(a.posted) != 2 {
		t.Fatalf("exit %d, posted %q, stderr %q", r.exit, a.posted, r.stderr)
	}
	if r.stdout != "r1: resumed\nr2: not paused\n" {
		t.Errorf("stdout %q", r.stdout)
	}
	if r := runRouters(t, a, nil, "resume"); r.exit != 2 {
		t.Errorf("no router: exit %d", r.exit)
	}
}
