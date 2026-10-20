package main

// `dormeto discovery` against a fake manager: the requests each command makes, what it prints, the exit codes, and that
// a refused argument or name sends nothing.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// disAPI is a fake manager for discovery: its candidates per status, the runs GET /discovery shows in turn, and every
// write it received.
type disAPI struct {
	mu         sync.Mutex
	candidates map[string][]string // status -> the candidates' JSON rows
	statuses   []string            // the answers of GET /discovery, in turn, the last repeated
	statusGets int
	requests   []string // "METHOD path?query body"
	writes     []string // the writes only
	config     string   // the answer of GET /discovery/config
	putStatus  int
	putAnswer  string
	access     []string // the accounts of GET /discovery/access
}

func tgARN(account, name string) string {
	return "arn:aws:elasticloadbalancing:eu-west-1:" + account + ":targetgroup/" + name + "/0123456789abcdef"
}

func row(account, name, status string) string {
	arn := tgARN(account, name)
	return fmt.Sprintf(`{"id":%q,"account":%q,"region":"eu-west-1","targetGroupArn":%q,"targetGroup":%q,"status":%q,"hosts":["%s.example.com"],"idleHours7d":120,"requests7d":42}`, arn, account, arn, name, status, name)
}

func (a *disAPI) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		a.requests = append(a.requests, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		if r.Method != http.MethodGet {
			a.writes = append(a.writes, r.Method+" "+r.URL.Path+" "+string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/iam/discovery" && r.Method == http.MethodGet:
			i := min(a.statusGets, len(a.statuses)-1)
			a.statusGets++
			io.WriteString(w, a.statuses[i])
		case r.URL.Path == "/iam/discovery/run":
			w.WriteHeader(202)
			io.WriteString(w, `{"accepted":true}`)
		case r.URL.Path == "/iam/discovery/candidates":
			rows := a.candidates[r.URL.Query().Get("status")]
			from, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
			to := min(from+200, len(rows))
			cursor := "null"
			if to < len(rows) {
				cursor = strconv.Quote(strconv.Itoa(to))
			}
			io.WriteString(w, `{"candidates":[`+strings.Join(rows[from:to], ",")+`],"cursor":`+cursor+`}`)
		case r.URL.Path == "/iam/discovery/dismiss" || r.URL.Path == "/iam/discovery/restore":
			var b struct{ IDs []string }
			json.Unmarshal(body, &b)
			var out []string
			for _, id := range b.IDs {
				out = append(out, `{"id":`+strconv.Quote(id)+`}`)
			}
			io.WriteString(w, `{"candidates":[`+strings.Join(out, ",")+`]}`)
		case r.URL.Path == "/iam/discovery/config" && r.Method == http.MethodGet:
			io.WriteString(w, a.config)
		case r.URL.Path == "/iam/discovery/config" && r.Method == http.MethodPut:
			w.WriteHeader(orInt(a.putStatus, 200))
			io.WriteString(w, orString(a.putAnswer, `{"config":{"version":1},"lint":[]}`))
		case r.URL.Path == "/iam/discovery/access":
			from, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
			size, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			to := min(from+size, len(a.access))
			cursor := "null"
			if to < len(a.access) {
				cursor = strconv.Quote(strconv.Itoa(to))
			}
			io.WriteString(w, `{"counts":{"ready":1,"discoveryMissing":2,"commandMissing":0,"unchecked":0},"accounts":[`+strings.Join(a.access[from:to], ",")+`],"cursor":`+cursor+`,"delegation":"ok","checkedAt":"2026-10-19T10:00:00Z"}`)
		default:
			w.WriteHeader(404)
		}
	}
}

func orInt(n, d int) int {
	if n != 0 {
		return n
	}
	return d
}

func runDiscovery(t *testing.T, a *disAPI, tune func(*fleetDeps), stdin string, args ...string) fleetRan {
	t.Helper()
	return runFleetWith(t, &fakeAPI{custom: a.handler(), stdin: stdin}, "eu-west-1", tune, append([]string{"discovery"}, args...)...)
}

const (
	oldRun     = `{"startedAt":"2026-10-19T09:00:00Z","finishedAt":"2026-10-19T09:00:30Z","trigger":"schedule","status":"ok","targets":[{"account":"111111111111","region":"eu-west-1","status":"ok"},{"account":"222222222222","region":"eu-west-1","status":"error","error":"AccessDenied"}],"emptyPairs":3,"counts":{},"lint":[{"code":"include-everything","level":"warning","filter":"all","include":1,"message":"matches every target group"}]}`
	newRunning = `{"startedAt":"2026-10-19T11:59:00Z","finishedAt":"2026-10-19T11:59:00Z","trigger":"manual","by":"me@x.io","status":"running","targets":[],"counts":{},"lint":[]}`
	newRunDone = `{"startedAt":"2026-10-19T11:59:00Z","finishedAt":"2026-10-19T11:59:20Z","trigger":"manual","by":"me@x.io","status":"ok","targets":[],"counts":{},"lint":[]}`
)

func statusAnswer(last string, extra string) string {
	return `{"lastRun":` + last + extra + `,"counts":{"pending-onboarding":2,"managed":5},"access":{"discoveryRole":"arn:aws:iam::1:role/d","commandRole":"arn:aws:iam::1:role/c","source":"terraform"}}`
}

func TestDiscoveryStatusText(t *testing.T) {
	a := &disAPI{statuses: []string{statusAnswer(oldRun, "")}}
	r := runDiscovery(t, a, nil, "", "status")
	if r.exit != 0 {
		t.Fatalf("exit %d, stderr %q", r.exit, r.stderr)
	}
	for _, want := range []string{"2026-10-19 09:00 UTC, schedule, ok, 30s", "4 account-regions, 0 skipped, 1 failed", "222222222222 eu-west-1: AccessDenied", "2 pending-onboarding, 5 managed", "warning: matches every target group"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
}

func TestDiscoveryStatusJSONIsTheAnswer(t *testing.T) {
	a := &disAPI{statuses: []string{statusAnswer(`null`, "")}}
	r := runDiscovery(t, a, nil, "", "status", "--json")
	var got map[string]any
	if r.exit != 0 || json.Unmarshal([]byte(r.stdout), &got) != nil || got["lastRun"] != nil || got["counts"] == nil {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
}

func TestDiscoveryRunWithoutWaitPostsOnce(t *testing.T) {
	a := &disAPI{statuses: []string{statusAnswer(oldRun, "")}}
	r := runDiscovery(t, a, nil, "", "run")
	if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != "POST /iam/discovery/run " || !strings.Contains(r.stdout, "requested") || a.statusGets != 0 {
		t.Errorf("exit %d, writes %q, gets %d, stdout %q", r.exit, a.writes, a.statusGets, r.stdout)
	}
}

func TestDiscoveryRunWaitIgnoresTheOldRun(t *testing.T) {
	a := &disAPI{statuses: []string{
		statusAnswer(oldRun, ""), // before the POST
		statusAnswer(oldRun, ""), // the old one still shown
		statusAnswer(newRunning, ""),
		statusAnswer(newRunDone, ""),
	}}
	slept := 0
	r := runDiscovery(t, a, func(d *fleetDeps) {
		d.sleep = func(_ context.Context, w time.Duration) error {
			if w != 5*time.Second {
				t.Errorf("slept %s", w)
			}
			slept++
			return nil
		}
	}, "", "run", "--wait")
	if r.exit != 0 || slept != 3 || a.statusGets != 4 || !strings.Contains(r.stdout, "2026-10-19 11:59 UTC, manual by me@x.io, ok, 20s") {
		t.Fatalf("exit %d, slept %d, gets %d, stdout %q, stderr %q", r.exit, slept, a.statusGets, r.stdout, r.stderr)
	}
	if !strings.HasPrefix(a.requests[0], "GET /iam/discovery ") || !strings.HasPrefix(a.requests[1], "POST /iam/discovery/run") {
		t.Errorf("the last run is read before the POST: %q", a.requests[:2])
	}
}

func TestDiscoveryRunWaitStopsAtARefusedRun(t *testing.T) {
	refused := `,"refused":{"startedAt":"2026-10-19T11:59:00Z","finishedAt":"2026-10-19T11:59:00Z","trigger":"manual","by":"me@x.io","status":"skipped","skippedReason":"a run is in progress","targets":[],"counts":{},"lint":[]}`
	a := &disAPI{statuses: []string{statusAnswer(oldRun, ""), statusAnswer(oldRun, refused)}}
	r := runDiscovery(t, a, nil, "", "run", "--wait")
	if r.exit != 1 || !strings.Contains(r.stderr, "refused: a run is in progress") {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestDiscoveryRunWaitTimeoutLeavesItRunning(t *testing.T) {
	a := &disAPI{statuses: []string{statusAnswer(oldRun, ""), statusAnswer(newRunning, "")}}
	r := runDiscovery(t, a, func(d *fleetDeps) {
		var stop context.CancelFunc
		d.timeout = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
			ctx, stop = context.WithCancel(ctx)
			return ctx, stop
		}
		d.sleep = func(ctx context.Context, _ time.Duration) error { stop(); return ctx.Err() }
	}, "", "run", "--wait", "--timeout", "1m")
	if r.exit != 1 || !strings.Contains(r.stderr, "still running in the manager: dormeto discovery status") {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestDiscoveryRunTimeoutMustBePositive(t *testing.T) {
	a := &disAPI{statuses: []string{statusAnswer(oldRun, "")}}
	r := runDiscovery(t, a, nil, "", "run", "--wait", "--timeout", "0s")
	if r.exit != 2 || len(a.requests) != 0 {
		t.Errorf("exit %d, requests %q", r.exit, a.requests)
	}
}

func TestDiscoveryCandidatesTable(t *testing.T) {
	a := &disAPI{candidates: map[string][]string{"pending-onboarding": {row("111111111111", "api", "pending-onboarding")}}}
	r := runDiscovery(t, a, nil, "", "candidates", "--account", "111111111111", "--candidate-region", "eu-west-1", "--limit", "10")
	if r.exit != 0 || !strings.Contains(r.stdout, "TARGET GROUP") || !strings.Contains(r.stdout, "api") || !strings.Contains(r.stdout, "api.example.com") || !strings.Contains(r.stdout, "120h") {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
	q := a.requests[0]
	for _, want := range []string{"status=pending-onboarding", "account=111111111111", "region=eu-west-1", "limit=10"} {
		if !strings.Contains(q, want) {
			t.Errorf("request %q lacks %q", q, want)
		}
	}
}

func TestDiscoveryCandidatesJSONIsTheRows(t *testing.T) {
	a := &disAPI{candidates: map[string][]string{"managed": {row("111111111111", "api", "managed")}}}
	r := runDiscovery(t, a, nil, "", "candidates", "--status", "managed", "--json")
	var got []map[string]any
	if r.exit != 0 || json.Unmarshal([]byte(r.stdout), &got) != nil || len(got) != 1 || got[0]["requests7d"] != 42.0 {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
	r = runDiscovery(t, a, nil, "", "candidates", "--status", "gone", "--json")
	if strings.TrimSpace(r.stdout) != "[]" {
		t.Errorf("no candidates: stdout %q", r.stdout)
	}
}

func TestDiscoveryCandidatesRefusedFlags(t *testing.T) {
	for _, args := range [][]string{
		{"candidates", "--status", "bogus"},
		{"candidates", "--candidate-region", "eu-west-1"},
		{"candidates", "--account", "12"},
		{"candidates", "--account", "111111111111", "--candidate-region", "mars"},
		{"candidates", "--limit", "-1"},
	} {
		a := &disAPI{}
		r := runDiscovery(t, a, nil, "", args...)
		if r.exit != 2 || len(a.requests) != 0 {
			t.Errorf("%v: exit %d, requests %q", args, r.exit, a.requests)
		}
	}
}

func TestDiscoveryDismissBatchesBy100AndSendsARNsInTheBodyOnly(t *testing.T) {
	var rows, names []string
	for i := 0; i < 150; i++ {
		name := fmt.Sprintf("tg-%03d", i)
		rows = append(rows, row("111111111111", name, "pending-onboarding"))
		names = append(names, name)
	}
	a := &disAPI{candidates: map[string][]string{"pending-onboarding": rows}}
	r := runDiscovery(t, a, nil, "", append(append([]string{"dismiss"}, names...), "--note", strings.Repeat("é", 300))...)
	if r.exit != 0 || len(a.writes) != 2 {
		t.Fatalf("exit %d, %d writes, stderr %q", r.exit, len(a.writes), r.stderr)
	}
	for i, want := range []int{100, 50} {
		path, body, _ := strings.Cut(strings.TrimPrefix(a.writes[i], "POST "), " ")
		var b struct {
			IDs  []string
			Note string
		}
		if path != "/iam/discovery/dismiss" || json.Unmarshal([]byte(body), &b) != nil || len(b.IDs) != want || len([]rune(b.Note)) != 256 || !strings.HasPrefix(b.IDs[0], "arn:") {
			t.Errorf("write %d: %s ... (%d ids, note of %d)", i, path, len(b.IDs), len([]rune(b.Note)))
		}
	}
	for _, req := range a.requests {
		if strings.Contains(strings.SplitN(req, " ", 3)[1], "arn:") {
			t.Errorf("an ARN in a path: %q", req)
		}
	}
	if !strings.Contains(r.stdout, "dismissed 150 candidates") {
		t.Errorf("stdout %q", r.stdout[max(0, len(r.stdout)-80):])
	}
}

func TestDiscoveryDismissByIDAndJSON(t *testing.T) {
	a := &disAPI{candidates: map[string][]string{"pending-onboarding": {row("111111111111", "api", "pending-onboarding")}}}
	r := runDiscovery(t, a, nil, "", "dismiss", tgARN("111111111111", "api"), "--json")
	var got []map[string]any
	if r.exit != 0 || json.Unmarshal([]byte(r.stdout), &got) != nil || len(got) != 1 || got[0]["id"] != tgARN("111111111111", "api") {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
}

func TestDiscoveryDismissResolvesEverythingBeforeSending(t *testing.T) {
	a := &disAPI{candidates: map[string][]string{"pending-onboarding": {
		row("111111111111", "api", "pending-onboarding"), row("222222222222", "api", "pending-onboarding"), row("111111111111", "web", "pending-onboarding"),
	}}}
	for name, args := range map[string][]string{
		"unknown":   {"dismiss", "web", "nope"},
		"ambiguous": {"dismiss", "web", "api"},
		"none":      {"dismiss"},
		"empty":     {"dismiss", " "},
	} {
		a.writes = nil
		r := runDiscovery(t, a, nil, "", args...)
		if r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("%s: exit %d, writes %q", name, r.exit, a.writes)
		}
		if name == "ambiguous" && (!strings.Contains(r.stderr, "api names several target groups:") || !strings.Contains(r.stderr, tgARN("111111111111", "api")) || !strings.Contains(r.stderr, tgARN("222222222222", "api"))) {
			t.Errorf("stderr %q", r.stderr)
		}
		if name == "unknown" && !strings.Contains(r.stderr, "no candidate nope in pending-onboarding") {
			t.Errorf("stderr %q", r.stderr)
		}
	}
	r := runDiscovery(t, &disAPI{}, nil, "", "dismiss", "web")
	if r.exit != 2 || !strings.Contains(r.stderr, "no candidate web in pending-onboarding") {
		t.Errorf("nothing pending: exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestDiscoveryRestoreResolvesInDismissed(t *testing.T) {
	a := &disAPI{candidates: map[string][]string{"dismissed": {row("111111111111", "api", "dismissed")}}}
	r := runDiscovery(t, a, nil, "", "restore", "api")
	if r.exit != 0 || len(a.writes) != 1 || !strings.HasPrefix(a.writes[0], "POST /iam/discovery/restore {") || !strings.Contains(a.requests[0], "status=dismissed") || !strings.Contains(r.stdout, "restored 1 candidate") {
		t.Errorf("exit %d, writes %q, requests %q, stdout %q", r.exit, a.writes, a.requests, r.stdout)
	}
	if strings.Contains(a.writes[0], "note") {
		t.Errorf("restore sends no note: %q", a.writes[0])
	}
	r = runDiscovery(t, a, nil, "", "restore", "ghost")
	if r.exit != 2 || len(a.writes) != 1 || !strings.Contains(r.stderr, "no candidate ghost in dismissed") {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestDiscoveryAccessReadsEveryPage(t *testing.T) {
	a := &disAPI{}
	for i := 0; i < 5; i++ {
		a.access = append(a.access, fmt.Sprintf(`{"account":"11111111111%d","name":"acc%d","discovery":{"ok":false,"reason":"no role"},"command":{"ok":true}}`, i, i))
	}
	r := runDiscovery(t, a, nil, "", "access", "--limit", "3")
	if r.exit != 0 || strings.Count(r.stdout, "missing: no role") != 3 || !strings.Contains(r.stdout, "missing: no role") || !strings.Contains(r.stdout, "1 ready, 2 without the discovery role") {
		t.Fatalf("exit %d, stdout %q", r.exit, r.stdout)
	}
	r = runDiscovery(t, a, nil, "", "access", "--json")
	var got struct{ Accounts []map[string]any }
	if r.exit != 0 || json.Unmarshal([]byte(r.stdout), &got) != nil || len(got.Accounts) != 5 {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
	if r := runDiscovery(t, a, nil, "", "access", "--limit", "0"); r.exit != 2 {
		t.Errorf("limit 0: exit %d", r.exit)
	}
}

const configAnswer = `{"config":{"version":1,"mode":"account","regions":["*"]},"updatedAt":"2026-10-19T08:00:00Z","updatedBy":"a@x.io","lint":[{"code":"include-everything","level":"warning","filter":"all","include":1,"message":"matches every target group"}]}`

func TestDiscoveryConfigShow(t *testing.T) {
	a := &disAPI{config: configAnswer}
	r := runDiscovery(t, a, nil, "", "config", "show")
	var cfg map[string]any
	if r.exit != 0 || json.Unmarshal([]byte(r.stdout), &cfg) != nil || cfg["mode"] != "account" || cfg["lint"] != nil {
		t.Errorf("stdout is not the configuration alone: exit %d, stdout %q", r.exit, r.stdout)
	}
	if r.stderr != "warning: matches every target group (filter all, include 1)\n" {
		t.Errorf("stderr %q", r.stderr)
	}
	r = runDiscovery(t, a, nil, "", "config", "show", "--json")
	var got map[string]any
	if r.exit != 0 || json.Unmarshal([]byte(r.stdout), &got) != nil || got["updatedBy"] != "a@x.io" {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
}

func TestDiscoveryConfigSetSendsTheFileAsItIs(t *testing.T) {
	a := &disAPI{}
	file := `{"version":1,"mode":"account","regions":["eu-west-1"],"refreshHours":6,"filters":[]}`
	r := runDiscovery(t, a, nil, file, "config", "set", "--file", "-", "--yes")
	if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != "PUT /iam/discovery/config "+file {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestDiscoveryConfigSetInvalidPrintsTheErrors(t *testing.T) {
	a := &disAPI{putStatus: 400, putAnswer: `{"error":"bad_request","message":"invalid configuration","errors":["refreshHours must be 1 to 168","filters[0].name is empty"]}`}
	r := runDiscovery(t, a, nil, `{"version":1}`, "config", "set", "--file", "-", "--yes")
	want := "invalid configuration\nrefreshHours must be 1 to 168\nfilters[0].name is empty\n"
	if r.exit != 1 || r.stderr != want {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestDiscoveryConfigSetIsAlwaysConfirmed(t *testing.T) {
	a := &disAPI{}
	r := runDiscovery(t, a, nil, `{"version":1}`, "config", "set", "--file", "-")
	if r.exit != 2 || len(a.writes) != 0 || !strings.Contains(r.stderr, "pass --yes") {
		t.Errorf("without a terminal: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	var asked []string
	dir := t.TempDir()
	path := dir + "/config.json"
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r = runDiscovery(t, a, withTerminal(false, &asked), "", "config", "set", "--file", path)
	if r.exit != 0 || len(asked) != 1 || len(a.writes) != 0 || !strings.Contains(r.stdout, "not confirmed: nothing sent") {
		t.Errorf("declined: exit %d, asked %q, writes %q, stdout %q", r.exit, asked, a.writes, r.stdout)
	}
	r = runDiscovery(t, a, withTerminal(true, &asked), "", "config", "set", "--file", path)
	if r.exit != 0 || len(a.writes) != 1 {
		t.Errorf("confirmed: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestDiscoveryConfigSetRefusesABadFileBeforeAnything(t *testing.T) {
	for name, args := range map[string][]string{"no file": {"config", "set", "--yes"}, "missing": {"config", "set", "--file", "/nonexistent/x.json", "--yes"}} {
		a := &disAPI{}
		r := runDiscovery(t, a, nil, "", args...)
		if r.exit != 2 || len(a.requests) != 0 {
			t.Errorf("%s: exit %d, requests %q", name, r.exit, a.requests)
		}
	}
	a := &disAPI{}
	r := runDiscovery(t, a, nil, `{"version":`, "config", "set", "--file", "-", "--yes")
	if r.exit != 2 || len(a.requests) != 0 || !strings.Contains(r.stderr, "not JSON") {
		t.Errorf("bad JSON: exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestDiscoveryConfigSetTakesTheShowAnswerOrABareConfiguration(t *testing.T) {
	a := &disAPI{}
	r := runDiscovery(t, a, nil, configAnswer, "config", "set", "--file", "-", "--yes")
	want := `PUT /iam/discovery/config {"version":1,"mode":"account","regions":["*"]}`
	if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != want {
		t.Errorf("show --json answer: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	a = &disAPI{}
	r = runDiscovery(t, a, nil, `{"version":1,"mode":"account"}`, "config", "set", "--file", "-", "--yes")
	if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != `PUT /iam/discovery/config {"version":1,"mode":"account"}` {
		t.Errorf("bare: exit %d, writes %q", r.exit, a.writes)
	}
	for _, file := range []string{`{"config":null}`, `{"config":"x"}`, `{"config":[1]}`} {
		a = &disAPI{}
		r = runDiscovery(t, a, nil, file, "config", "set", "--file", "-", "--yes")
		if r.exit != 2 || len(a.requests) != 0 {
			t.Errorf("%s: exit %d, requests %q", file, r.exit, a.requests)
		}
	}
}
