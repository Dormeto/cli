package main

// `dormeto workloads show|history|idle-window` against the fake manager API: the requests they make, what they print,
// the refusals that send nothing.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

const workloadShowAnswer = `{"workloadId":"w-1","name":"Shop","state":"ASLEEP","updatedAt":"2026-10-19T09:00:00.000Z",
 "router":{"version":"0.31.0","build":"b7"},"tags":{"team":"payments","env":"prod"},"url":"https://shop.example.com",
 "account":"111111111111","region":"eu-west-1","can":{"wake":true,"nap":true},
 "settings":{"idleMinutes":15,"waitSeconds":20,"drainSeconds":30,"wakeTimeoutSeconds":300,"maxHolders":50},
 "pauses":[{"scope":"workload","by":"ann@x.io","at":"2026-10-19T08:00:00.000Z","reason":"release freeze","until":"2026-10-21T08:00:00.000Z","keepAsleep":false}],
 "schedule":` + shownSchedule + `,
 "next":[{"at":"2026-10-19T17:00:00.000Z","kind":"awake-end","window":0}],"maintenanceDue":"2026-10-26T07:00:00.000Z",
 "states7d":{"from":"2026-10-12T12:00:00.000Z","to":"2026-10-19T12:00:00.000Z","totals":{"ASLEEP":432000,"AWAKE":144000,"UNKNOWN":28800}},
 "idleHours7d":96.5,"requests7d":1234,
 "costs":{"baseline":{"kind":"always"},"from":"a","to":"b","napAt":null,"savedShare":0.5,"items":[{"item":{"kind":"ec2","type":"m5.large","count":2,"os":"linux"},"hourly":0.2},{"item":{"kind":"rds","class":"db.t3.small","engine":"postgres"},"hourly":null}],
  "saved":{"usd":123.4,"hours":300,"unpricedHours":12},"discounts":{"compute":10,"databases":5}},
 "energy":{"kwh":12.34,"co2Kg":3.45,"estimated":1}}`

const workloadHistoryAnswer = `{"from":"2026-10-12T12:00:00.000Z","to":"2026-10-19T12:00:00.000Z",
 "phases":[{"state":"ASLEEP","from":"2026-10-12T12:00:00.000Z","to":"2026-10-19T09:00:00.000Z"},{"state":"AWAKE","from":"2026-10-19T09:00:00.000Z","to":"2026-10-19T12:00:00.000Z"}],
 "totals":{"ASLEEP":594000,"AWAKE":10800},
 "wakes":{"count":3,"failed":1,"medianMs":12000,"maxMs":95000},
 "before":null,"truncated":false,
 "entries":[
  {"from":"ASLEEP","to":"WAKING","at":"2026-10-19T08:59:30.000Z","cause":"normal"},
  {"from":"WAKING","to":"AWAKE","at":"2026-10-19T09:00:00.000Z","cause":"normal","durationMs":30000},
  {"from":"AWAKE","to":"NAPPING","at":"2026-10-19T10:00:00.000Z","cause":"idle"},
  {"from":"NAPPING","to":"FAILED","at":"2026-10-19T10:01:00.000Z","cause":"nap-failed","failure":{"kind":"timeout","step":"nap","message":"drain timed out"}},
  {"from":"FAILED","to":"AWAKE","at":"2026-10-19T11:00:00.000Z","cause":"manager","by":"ann@x.io"}],
 "savings":{"usd":42.5,"hours":100,"items":[{"item":{"kind":"ec2"},"hourly":1,"hours":100,"usd":42.5}],"unpriced":[],"baseline":{"kind":"always"}}}`

const idleWindowAnswer = `{"computedAt":"2026-10-19T03:00:00.000Z","from":"2026-10-05T03:00:00.000Z","to":"2026-10-19T03:00:00.000Z","current":15,"recommended":30,
 "checkMinutes":5,"wakeSeconds":40,"priced":true,
 "windows":[{"minutes":5,"asleepHours":20.5,"usdMonth":210.5,"wakesPerDay":14.2,"waitSecondsPerDay":568},
  {"minutes":15,"asleepHours":19,"usdMonth":190,"wakesPerDay":6,"waitSecondsPerDay":240},
  {"minutes":30,"asleepHours":18,"usdMonth":175.25,"wakesPerDay":2.5,"waitSecondsPerDay":null}],
 "actual":{"wakes":10,"replayedWakes":40,"noisy":true},"wakerTargetGroup":true}`

// showAPI is a fake manager's workload read routes.
type showAPI struct {
	mu       sync.Mutex
	requests []string // "path?query"
	status   int
	body     string // the answer of every read when status is set
}

func (a *showAPI) handler(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	w.Header().Set("Content-Type", "application/json")
	a.mu.Lock()
	a.requests = append(a.requests, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
	a.mu.Unlock()
	if a.status != 0 {
		w.WriteHeader(a.status)
		io.WriteString(w, a.body)
		return
	}
	switch p := r.URL.EscapedPath(); {
	case p == "/iam/workloads/prod-eu/w-1":
		io.WriteString(w, workloadShowAnswer)
	case p == "/iam/workloads/prod-eu/w-1/history":
		io.WriteString(w, workloadHistoryAnswer)
	case p == "/iam/workloads/prod-eu/w-1/idle-window":
		io.WriteString(w, idleWindowAnswer)
	case p == "/iam/workloads/prod-eu/w%2F2":
		io.WriteString(w, `{"workloadId":"w/2","state":"AWAKE","settings":{}}`)
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"error":"not_found","message":"unknown workload: nope"}`)
	}
}

func runShow(t *testing.T, a *showAPI, args ...string) fleetRan {
	t.Helper()
	return runFleet(t, &fakeAPI{custom: a.handler}, "eu-west-1", append([]string{"workloads"}, args...)...)
}

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

func TestWorkloadsShowPrintsTheWorkload(t *testing.T) {
	a := &showAPI{}
	r := runShow(t, a, "show", "prod-eu/w-1")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	t.Logf("\n%s", r.stdout)
	mustContain(t, r.stdout,
		"prod-eu/w-1", "Shop", "ASLEEP since 2026-10-19 09:00 UTC", "https://shop.example.com",
		"111111111111", "eu-west-1", "0.31.0",
		"env=prod, team=payments", // sorted
		"idle window", "15 min", "hold", "20 s", "drain", "30 s", "wake timeout", "5 min", "held requests", "50",
		"paused by ann@x.io on 2026-10-19 08:00 UTC: release freeze, until 2026-10-21 08:00 UTC",
		"time zone Europe/Paris", "awake mon–fri 08:30–19:00", "nap mon–fri 20:00–07:00 (next day), wake: header",
		"Mon 19 Oct 19:00 CEST awake window ends", "maintenance wake due Mon 26 Oct 08:00 CET",
		"ASLEEP 71%", "AWAKE 24%", "UNKNOWN 5%",
		"idle hours", "96.5", "1234 requests",
		"saved", "$123.40", "300 hours", "12 hours not priced", "$0.2000 an hour", "$73.00 a month", "compute 10%, databases 5%",
		"12.3 kWh", "3.45 kg CO2",
	)
	if len(a.requests) != 1 || a.requests[0] != "/iam/workloads/prod-eu/w-1?" {
		t.Errorf("requests %q", a.requests)
	}
	if strings.HasSuffix(strings.TrimRight(r.stdout, "\n"), ".") {
		t.Errorf("final period: %q", r.stdout)
	}
}

func TestWorkloadsShowPathEscapesTheID(t *testing.T) {
	a := &showAPI{}
	r := runShow(t, a, "show", "prod-eu/w/2")
	if r.exit != 0 || len(a.requests) != 1 || a.requests[0] != "/iam/workloads/prod-eu/w%2F2?" {
		t.Errorf("exit %d requests %q stderr %q", r.exit, a.requests, r.stderr)
	}
}

func TestWorkloadsShowWithoutTheOptionalParts(t *testing.T) {
	a := &showAPI{status: 200, body: `{"workloadId":"w-1","state":"AWAKE","settings":{"idleMinutes":15}}`}
	r := runShow(t, a, "show", "prod-eu/w-1")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	mustContain(t, r.stdout, "prod-eu/w-1", "AWAKE", "idle window", "15 min")
	for _, no := range []string{"pauses", "schedule", "costs", "energy", "<nil>", "null", "0001"} {
		if strings.Contains(r.stdout, no) {
			t.Errorf("output has %q:\n%s", no, r.stdout)
		}
	}
}

func TestWorkloadsShowJSONIsTheAnswer(t *testing.T) {
	r := runShow(t, &showAPI{}, "show", "prod-eu/w-1", "--json")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	var got, want map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("stdout %q: %v", r.stdout, err)
	}
	json.Unmarshal([]byte(workloadShowAnswer), &want)
	if len(got) != len(want) || got["idleHours7d"] != want["idleHours7d"] || got["can"] == nil {
		t.Errorf("got %v", got)
	}
}

func TestWorkloadReadsShowTheManagersRefusal(t *testing.T) {
	for _, sub := range []string{"show", "history", "idle-window"} {
		a := &showAPI{}
		r := runShow(t, a, sub, "prod-eu/nope")
		if r.exit != 1 || !strings.Contains(r.stderr, "unknown workload: nope") || r.stdout != "" {
			t.Errorf("%s: exit %d stdout %q stderr %q", sub, r.exit, r.stdout, r.stderr)
		}
	}
}

func TestWorkloadReadsNeedExactlyOneTarget(t *testing.T) {
	for _, sub := range []string{"show", "history", "idle-window"} {
		for name, args := range map[string][]string{
			"none": {sub}, "two": {sub, "prod-eu/w-1", "prod-eu/w-2"}, "no slash": {sub, "w-1"}, "empty id": {sub, "prod-eu/"},
		} {
			a := &showAPI{}
			r := runShow(t, a, args...)
			if r.exit != 2 || len(a.requests) != 0 || r.stderr == "" {
				t.Errorf("%s %s: exit %d requests %q stderr %q", sub, name, r.exit, a.requests, r.stderr)
			}
		}
	}
}

func TestWorkloadsHistoryPrintsTransitionsAndWakes(t *testing.T) {
	a := &showAPI{}
	r := runShow(t, a, "history", "prod-eu/w-1")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	t.Logf("\n%s", r.stdout)
	if len(a.requests) != 1 || a.requests[0] != "/iam/workloads/prod-eu/w-1/history?days=7" {
		t.Errorf("requests %q", a.requests)
	}
	mustContain(t, r.stdout,
		"TIME", "STATE", "CAUSE", "DURATION",
		"2026-10-19 11:00 UTC", "woken from the manager by ann@x.io",
		"2026-10-19 10:01 UTC", "the nap failed: drain timed out",
		"2026-10-19 10:00 UTC", "the idle check (no request for its idle window)",
		"2026-10-19 09:00 UTC", "AWAKE", "30 s",
		"3 wakes, median 12 s, longest 1 min 35 s, 1 failed",
		"ASLEEP 98%", "saved about $42.50",
	)
	// newest first
	if strings.Index(r.stdout, "11:00 UTC") > strings.Index(r.stdout, "10:00 UTC") {
		t.Errorf("not newest first:\n%s", r.stdout)
	}
}

func TestWorkloadsHistoryDays(t *testing.T) {
	a := &showAPI{}
	if r := runShow(t, a, "history", "prod-eu/w-1", "--days", "31"); r.exit != 0 || a.requests[0] != "/iam/workloads/prod-eu/w-1/history?days=31" {
		t.Errorf("exit %d requests %q", r.exit, a.requests)
	}
	for _, days := range []string{"32", "0", "-1"} {
		b := &showAPI{}
		r := runShow(t, b, "history", "prod-eu/w-1", "--days", days)
		if r.exit != 2 || len(b.requests) != 0 || !strings.Contains(r.stderr, "--days: "+days+" is not a whole number from 1 to 31") {
			t.Errorf("--days %s: exit %d requests %q stderr %q", days, r.exit, b.requests, r.stderr)
		}
	}
}

func TestWorkloadsHistoryWithoutHistory(t *testing.T) {
	a := &showAPI{status: 200, body: `{"from":"2026-10-12T12:00:00.000Z","to":"2026-10-19T12:00:00.000Z","phases":[{"state":"UNKNOWN","from":"2026-10-12T12:00:00.000Z","to":"2026-10-19T12:00:00.000Z"}],"totals":{"UNKNOWN":604800},"wakes":{"count":0,"failed":0,"medianMs":null,"maxMs":null},"before":null,"entries":[],"truncated":true}`}
	r := runShow(t, a, "history", "prod-eu/w-1")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	mustContain(t, r.stdout, "no history in this period yet", "no wake", "more than 5000 changes")
}

func TestWorkloadsHistoryJSONIsTheAnswer(t *testing.T) {
	r := runShow(t, &showAPI{}, "history", "prod-eu/w-1", "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); r.exit != 0 || err != nil || got["entries"] == nil || got["savings"] == nil {
		t.Errorf("exit %d err %v stdout %q", r.exit, err, r.stdout)
	}
}

func TestWorkloadsIdleWindowPrintsTheCandidates(t *testing.T) {
	a := &showAPI{}
	r := runShow(t, a, "idle-window", "prod-eu/w-1")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	t.Logf("\n%s", r.stdout)
	if len(a.requests) != 1 || a.requests[0] != "/iam/workloads/prod-eu/w-1/idle-window?" {
		t.Errorf("requests %q", a.requests)
	}
	mustContain(t, r.stdout,
		"current 15 min", "recommended 30 min",
		"saves about $14.75 a month less than now, with 3.5 fewer wakes a day",
		"WINDOW", "SAVINGS A MONTH", "WAKES A DAY", "WAIT A DAY",
		"5 min", "$210.50", "14", "9 min 28 s",
		"15 min (current)", "30 min (recommended)", "$175.25", "2.5",
		"replay counts 40 wakes", "10 that happened",
		"2026-10-19 03:00 UTC",
	)
	if lines := strings.Split(r.stdout, "\n"); !strings.Contains(r.stdout, "—") {
		t.Errorf("unknown wait not a dash: %q", lines)
	}
}

func TestWorkloadsIdleWindowUnpricedShowsHours(t *testing.T) {
	body := strings.Replace(strings.Replace(idleWindowAnswer, `"priced":true`, `"priced":false`, 1), `"noisy":true`, `"noisy":false`, 1)
	r := runShow(t, &showAPI{status: 200, body: body}, "idle-window", "prod-eu/w-1")
	mustContain(t, r.stdout, "ASLEEP A DAY", "20.5 h", "nothing priced yet")
	if strings.Contains(r.stdout, "$") {
		t.Errorf("dollars in %q", r.stdout)
	}
}

func TestWorkloadsIdleWindowJSONIsTheAnswer(t *testing.T) {
	r := runShow(t, &showAPI{}, "idle-window", "prod-eu/w-1", "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); r.exit != 0 || err != nil || got["recommended"] != float64(30) {
		t.Errorf("exit %d err %v stdout %q", r.exit, err, r.stdout)
	}
}
