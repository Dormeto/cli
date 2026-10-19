package main

// `dormeto workloads keep-awake`, `resume` and `schedule show|set|clear` against a fake manager API: the requests they
// make, what they print, the exit codes.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// wlAPI is a fake manager for the workloads commands: it records the writes and answers the statuses in order.
type wlAPI struct {
	mu       sync.Mutex
	writes   []string // "POST /iam/workloads/prod-eu/w-1/pause {body}"
	statuses []string // the status answers of GET …/{id}, the last one repeated
	reads    int
	putCode  int
	putBody  string
	schedule string // the schedule GET …/{id} reports ("" for null)
}

func (a *wlAPI) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			a.writes = append(a.writes, r.Method+" "+r.URL.Path+" "+string(body))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/iam/workloads":
			tag := r.URL.Query()["tag"]
			switch {
			case len(tag) == 1 && tag[0] == "team=payments":
				io.WriteString(w, `{"workloads":[{"workloadId":"w-1","router":"prod-eu","state":"ASLEEP"},{"workloadId":"w-2","router":"prod-eu","state":"ASLEEP"}],"total":2,"cursor":null}`)
			case len(tag) == 1 && tag[0] == "team=none":
				io.WriteString(w, `{"workloads":[],"total":0,"cursor":null}`)
			default:
				io.WriteString(w, `{"workloads":[],"total":0}`)
			}
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/iam/workloads/"):
			st := `{"state":"AWAKE"}`
			if len(a.statuses) > 0 {
				st = a.statuses[min(a.reads, len(a.statuses)-1)]
			}
			a.reads++
			if a.schedule != "" || len(a.statuses) == 0 {
				sched := orString(a.schedule, "null")
				if a.schedule != "" {
					st = `{"state":"ASLEEP","schedule":` + sched + `,"next":[{"at":"2026-10-19T17:00:00.000Z","kind":"awake-end","window":0},{"at":"2026-10-19T18:00:00.000Z","kind":"nap-start","window":1}],"maintenanceDue":"2026-10-26T07:00:00.000Z"}`
				} else {
					st = `{"state":"AWAKE","schedule":null,"next":[]}`
				}
			}
			io.WriteString(w, st)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/schedule"):
			if a.putCode != 0 {
				w.WriteHeader(a.putCode)
				io.WriteString(w, a.putBody)
				return
			}
			io.WriteString(w, `{"schedule":{},"next":[{"at":"2026-10-19T17:00:00.000Z","kind":"awake-end","window":0}],"maintenanceDue":null}`)
		case r.Method == http.MethodDelete:
			io.WriteString(w, `{"cleared":true}`)
		default:
			io.WriteString(w, `{}`)
		}
	}
}

func runWL(t *testing.T, a *wlAPI, tune func(*fleetDeps), args ...string) fleetRan {
	t.Helper()
	return runFleetWith(t, &fakeAPI{custom: a.handler(t)}, "eu-west-1", tune, append([]string{"workloads"}, args...)...)
}

func TestKeepAwakeForPostsUntilAndKeepAsleepFalse(t *testing.T) {
	a := &wlAPI{}
	r := runWL(t, a, nil, "keep-awake", "prod-eu/w-1", "prod-eu/w-2", "--for", "2h", "--yes")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	want := []string{
		`POST /iam/workloads/prod-eu/w-1/pause {"reason":"keep-awake from the CLI","until":"2026-10-19T14:00:00Z","keepAsleep":false}`,
		`POST /iam/workloads/prod-eu/w-2/pause {"reason":"keep-awake from the CLI","until":"2026-10-19T14:00:00Z","keepAsleep":false}`,
	}
	if !reflect.DeepEqual(a.writes, want) {
		t.Errorf("writes %q", a.writes)
	}
	if !strings.Contains(r.stdout, "prod-eu/w-1: kept awake until 2026-10-19 14:00 UTC") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestKeepAwakeReasonAndUntilClock(t *testing.T) {
	a := &wlAPI{}
	r := runWL(t, a, nil, "keep-awake", "prod-eu/w-1", "--until", "17:30", "--reason", "release ci")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	if want := `POST /iam/workloads/prod-eu/w-1/pause {"reason":"release ci","until":"2026-10-19T17:30:00Z","keepAsleep":false}`; len(a.writes) != 1 || a.writes[0] != want {
		t.Errorf("writes %q", a.writes)
	}
	a = &wlAPI{}
	r = runWL(t, a, nil, "keep-awake", "prod-eu/w-1", "--until", "2026-10-20T08:00:00+02:00")
	if r.exit != 0 || !strings.Contains(a.writes[0], `"until":"2026-10-20T06:00:00Z"`) {
		t.Errorf("exit %d writes %q", r.exit, a.writes)
	}
}

func TestKeepAwakeTimeRefusals(t *testing.T) {
	for name, args := range map[string][]string{
		"8d":            {"--for", "8d"},
		"192h":          {"--for", "192h"},
		"zero":          {"--for", "0s"},
		"past clock":    {"--until", "11:59"},
		"now":           {"--until", "12:00"},
		"past rfc":      {"--until", "2026-10-19T11:00:00Z"},
		"beyond 7 days": {"--until", "2026-10-27T12:00:01Z"},
		"neither":       {},
		"both":          {"--for", "1h", "--until", "17:00"},
		"bad clock":     {"--until", "25:00"},
	} {
		a := &wlAPI{}
		r := runWL(t, a, nil, append([]string{"keep-awake", "prod-eu/w-1"}, args...)...)
		if r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("%s: exit %d, writes %q, stderr %q", name, r.exit, a.writes, r.stderr)
		}
	}
	r := runWL(t, &wlAPI{}, nil, "keep-awake", "prod-eu/w-1", "--for", "8d")
	if !strings.Contains(r.stderr, "up to 168h") {
		t.Errorf("stderr %q", r.stderr)
	}
	r = runWL(t, &wlAPI{}, nil, "keep-awake", "prod-eu/w-1", "--until", "11:00")
	if !strings.Contains(r.stderr, "already past") {
		t.Errorf("stderr %q", r.stderr)
	}
}

func TestKeepAwakeUntilExactlySevenDaysIsAccepted(t *testing.T) {
	a := &wlAPI{}
	r := runWL(t, a, nil, "keep-awake", "prod-eu/w-1", "--for", "168h")
	if r.exit != 0 || !strings.Contains(a.writes[0], `"until":"2026-10-26T12:00:00Z"`) {
		t.Errorf("exit %d writes %q %s", r.exit, a.writes, r.stderr)
	}
}

func TestKeepAwakeWaitReturnsWhenAwake(t *testing.T) {
	a := &wlAPI{statuses: []string{`{"state":"ASLEEP"}`, `{"state":"WAKING"}`, `{"state":"AWAKE"}`}}
	var sleeps []time.Duration
	r := runWL(t, a, func(d *fleetDeps) {
		d.sleep = func(_ context.Context, x time.Duration) error { sleeps = append(sleeps, x); return nil }
	}, "keep-awake", "prod-eu/w-1", "--for", "1h", "--wait")
	if r.exit != 0 || a.reads != 3 {
		t.Fatalf("exit %d, reads %d: %s", r.exit, a.reads, r.stderr)
	}
	if len(sleeps) != 2 || sleeps[0] != 5*time.Second {
		t.Errorf("sleeps %v", sleeps)
	}
	if !strings.Contains(r.stdout, "prod-eu/w-1: awake") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestKeepAwakeWaitFailedExits1WithTheFailure(t *testing.T) {
	a := &wlAPI{statuses: []string{`{"state":"WAKING"}`, `{"state":"FAILED","failure":{"kind":"backend","step":"wake","message":"instance limit exceeded","at":"2026-10-19T12:01:00Z"}}`}}
	r := runWL(t, a, nil, "keep-awake", "prod-eu/w-1", "--for", "1h", "--wait")
	if r.exit != 1 || !strings.Contains(r.stderr, "prod-eu/w-1: failed to wake: instance limit exceeded") {
		t.Errorf("exit %d stderr %q", r.exit, r.stderr)
	}
	if len(a.writes) != 1 { // the pause stays
		t.Errorf("writes %q", a.writes)
	}
}

func TestKeepAwakeWaitTimeoutNamesTheWorkloadsNotAwake(t *testing.T) {
	a := &wlAPI{statuses: []string{`{"state":"WAKING"}`}}
	r := runWL(t, a, func(d *fleetDeps) {
		var cancel context.CancelFunc
		n := 0
		d.timeout = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
			var c context.Context
			c, cancel = context.WithCancel(ctx)
			return c, cancel
		}
		d.sleep = func(ctx context.Context, _ time.Duration) error {
			if n++; n >= 2 {
				cancel()
			}
			return ctx.Err()
		}
	}, "keep-awake", "prod-eu/w-1", "--for", "1h", "--wait", "--timeout", "10s")
	if r.exit != 1 || !strings.Contains(r.stderr, "timeout of 10s reached: not awake: prod-eu/w-1") || !strings.Contains(r.stderr, "the pause stays") {
		t.Errorf("exit %d stderr %q", r.exit, r.stderr)
	}
}

func TestKeepAwakeTagTwoMatchesNeedsYesWithoutTerminal(t *testing.T) {
	a := &wlAPI{}
	r := runWL(t, a, nil, "keep-awake", "--tag", "team=payments", "--for", "1h")
	if r.exit != 2 || len(a.writes) != 0 || !strings.Contains(r.stderr, "--yes") {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	r = runWL(t, a, nil, "keep-awake", "--tag", "team=payments", "--for", "1h", "--yes")
	if r.exit != 0 || len(a.writes) != 2 {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestKeepAwakeAsksOnATerminalAndDryRunPostsNothing(t *testing.T) {
	a := &wlAPI{}
	var asked []string
	r := runWL(t, a, withTerminal(false, &asked), "keep-awake", "--tag", "team=payments", "--for", "1h")
	if r.exit != 0 || len(a.writes) != 0 || len(asked) != 1 || asked[0] != "Keep 2 workloads awake?" {
		t.Errorf("exit %d, writes %q, asked %q", r.exit, a.writes, asked)
	}
	r = runWL(t, a, nil, "keep-awake", "--tag", "team=payments", "--for", "1h", "--dry-run")
	if r.exit != 0 || len(a.writes) != 0 || !strings.Contains(r.stdout, "prod-eu/w-1") || !strings.Contains(r.stdout, "prod-eu/w-2") {
		t.Errorf("exit %d, writes %q, stdout %q", r.exit, a.writes, r.stdout)
	}
}

func TestTargetsBadUsageExits2(t *testing.T) {
	for name, args := range map[string][]string{
		"bad target":   {"resume", "nope"},
		"none":         {"resume"},
		"both":         {"resume", "prod-eu/w-1", "--tag", "team=payments"},
		"bad tag":      {"resume", "--tag", "nokey"},
		"no match":     {"resume", "--tag", "team=none"},
		"show two":     {"schedule", "show", "prod-eu/w-1", "prod-eu/w-2"},
		"set no file":  {"schedule", "set", "prod-eu/w-1"},
		"clear no arg": {"schedule", "clear"},
	} {
		a := &wlAPI{}
		r := runWL(t, a, nil, args...)
		if r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("%s: exit %d, writes %q, stderr %q", name, r.exit, a.writes, r.stderr)
		}
	}
}

func TestResumePostsResume(t *testing.T) {
	a := &wlAPI{}
	r := runWL(t, a, nil, "resume", "prod-eu/w-1")
	if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != "POST /iam/workloads/prod-eu/w-1/resume " {
		t.Errorf("exit %d writes %q stderr %q", r.exit, a.writes, r.stderr)
	}
	if !strings.Contains(r.stdout, "prod-eu/w-1: resumed") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestResumeRefusalOfOneDoesNotStopTheOthers(t *testing.T) {
	a := &wlAPI{}
	r := runFleetWith(t, &fakeAPI{custom: func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "w-1") {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"not_found","message":"unknown workload"}`)
			return
		}
		a.handler(t)(w, req)
	}}, "eu-west-1", nil, "workloads", "resume", "prod-eu/w-1", "prod-eu/w-2", "--yes")
	if r.exit != 1 || !strings.Contains(r.stderr, "prod-eu/w-1: unknown workload") || len(a.writes) != 1 {
		t.Errorf("exit %d stderr %q writes %q", r.exit, r.stderr, a.writes)
	}
}

const scheduleFile = `{"timeZone":"Europe/Paris","windows":[{"kind":"awake","days":["mon","tue"],"start":"08:30","end":"19:00"}],"maxNapDays":7,"by":"someone","at":"2026-10-01T00:00:00Z"}`

func TestScheduleSetSendsTheFileAsIsAndPrintsNextEvents(t *testing.T) {
	a := &wlAPI{}
	path := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(path, []byte(scheduleFile), 0o600)
	r := runWL(t, a, nil, "schedule", "set", "prod-eu/w-1", "--file", path)
	if r.exit != 0 || len(a.writes) != 1 {
		t.Fatalf("exit %d writes %q stderr %q", r.exit, a.writes, r.stderr)
	}
	body := strings.TrimPrefix(a.writes[0], "PUT /iam/workloads/prod-eu/w-1/schedule ")
	var got, want any
	json.Unmarshal([]byte(body), &got)
	json.Unmarshal([]byte(scheduleFile), &want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sent %s", body)
	}
	if !strings.Contains(r.stdout, "prod-eu/w-1: schedule set") || !strings.Contains(r.stdout, "awake window ends") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestScheduleSetReadsStdin(t *testing.T) {
	a := &wlAPI{}
	srvTune := func(d *fleetDeps) {}
	srv := runWLStdin(t, a, srvTune, scheduleFile, "schedule", "set", "prod-eu/w-1", "--file", "-")
	if srv.exit != 0 || len(a.writes) != 1 || !strings.Contains(a.writes[0], `"maxNapDays":7`) {
		t.Errorf("exit %d writes %q stderr %q", srv.exit, a.writes, srv.stderr)
	}
}

func TestScheduleSetRefusesBadJSONBeforeSending(t *testing.T) {
	a := &wlAPI{}
	r := runWLStdin(t, a, nil, `{"timeZone":`, "schedule", "set", "prod-eu/w-1", "--file", "-")
	if r.exit != 2 || len(a.writes) != 0 {
		t.Errorf("exit %d writes %q stderr %q", r.exit, a.writes, r.stderr)
	}
	r = runWL(t, a, nil, "schedule", "set", "prod-eu/w-1", "--file", "/nonexistent/s.json")
	if r.exit != 2 || len(a.writes) != 0 {
		t.Errorf("exit %d writes %q stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestScheduleSet400PrintsTheTextAndExits1(t *testing.T) {
	a := &wlAPI{putCode: 400, putBody: `{"error":"bad_request","message":"windows[0].end: not a multiple of 5; windows[1].days: empty"}`}
	r := runWLStdin(t, a, nil, scheduleFile, "schedule", "set", "prod-eu/w-1", "--file", "-")
	if r.exit != 1 || !strings.Contains(r.stderr, "prod-eu/w-1: windows[0].end: not a multiple of 5; windows[1].days: empty") {
		t.Errorf("exit %d stderr %q", r.exit, r.stderr)
	}
}

func TestScheduleSet409SaysUpgradeTheRouter(t *testing.T) {
	a := &wlAPI{putCode: 409, putBody: `{"error":"conflict","message":"upgrade the router to 0.31.0 or later"}`}
	r := runWLStdin(t, a, nil, scheduleFile, "schedule", "set", "prod-eu/w-1", "--file", "-")
	if r.exit != 1 || !strings.Contains(r.stderr, "upgrade the router to 0.31.0 or later") {
		t.Errorf("exit %d stderr %q", r.exit, r.stderr)
	}
}

func TestScheduleClear(t *testing.T) {
	a := &wlAPI{}
	r := runWL(t, a, nil, "schedule", "clear", "prod-eu/w-1")
	if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != "DELETE /iam/workloads/prod-eu/w-1/schedule " || !strings.Contains(r.stdout, "prod-eu/w-1: schedule cleared") {
		t.Errorf("exit %d writes %q stdout %q", r.exit, a.writes, r.stdout)
	}
}

const shownSchedule = `{"timeZone":"Europe/Paris","windows":[{"kind":"awake","days":["mon","tue","wed","thu","fri"],"start":"08:30","end":"19:00"},{"kind":"nap","days":["mon","tue","wed","thu","fri"],"start":"20:00","end":"07:00","wake":"header"},{"kind":"nap","days":["sat","sun"],"start":"00:00","end":"00:00"}],"exceptions":["2026-12-25"],"maxNapDays":7,"by":"me","at":"2026-10-01T00:00:00Z"}`

func TestScheduleShowPrintsWindowsExceptionsAndNextEventsInTheZone(t *testing.T) {
	a := &wlAPI{schedule: shownSchedule}
	r := runWL(t, a, nil, "schedule", "show", "prod-eu/w-1")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	t.Logf("\n%s", r.stdout)
	for _, want := range []string{
		"time zone Europe/Paris",
		"awake mon–fri 08:30–19:00",
		"nap mon–fri 20:00–07:00 (next day), wake: header",
		"nap sat,sun 00:00–00:00 (next day), wake: never",
		"except 2026-12-25",
		"maximum nap 7 days",
		"Mon 19 Oct 19:00 awake window ends", // 17:00Z is 19:00 in Paris (CEST)
		"Mon 19 Oct 20:00 nap window starts",
		"maintenance wake due Mon 26 Oct 08:00", // 07:00Z, CET after the change on 25 Oct
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	if strings.HasSuffix(strings.TrimRight(r.stdout, "\n"), ".") {
		t.Errorf("final period: %q", r.stdout)
	}
}

func TestScheduleShowUnknownZoneFallsBackToUTC(t *testing.T) {
	a := &wlAPI{schedule: `{"timeZone":"Mars/Olympus","windows":[]}`}
	r := runWL(t, a, nil, "schedule", "show", "prod-eu/w-1")
	if r.exit != 0 || !strings.Contains(r.stdout, "Mon 19 Oct 17:00 awake window ends") || !strings.Contains(r.stdout, `unknown time zone "Mars/Olympus"; times in UTC`) {
		t.Errorf("exit %d stdout %q", r.exit, r.stdout)
	}
}

func TestScheduleShowNone(t *testing.T) {
	r := runWL(t, &wlAPI{}, nil, "schedule", "show", "prod-eu/w-1")
	if r.exit != 0 || strings.TrimSpace(r.stdout) != "prod-eu/w-1: no schedule" {
		t.Errorf("exit %d stdout %q", r.exit, r.stdout)
	}
}

// show --json prints the schedule alone, as stored, so that it goes back into `schedule set --file -`.
func TestScheduleShowJSONRoundTripsIntoSet(t *testing.T) {
	a := &wlAPI{schedule: shownSchedule}
	shown := runWL(t, a, nil, "schedule", "show", "prod-eu/w-1", "--json")
	if shown.exit != 0 {
		t.Fatalf("exit %d: %s", shown.exit, shown.stderr)
	}
	var got, want any
	if err := json.Unmarshal([]byte(shown.stdout), &got); err != nil {
		t.Fatalf("not JSON: %v: %q", err, shown.stdout)
	}
	json.Unmarshal([]byte(shownSchedule), &want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shown %s", shown.stdout)
	}
	b := &wlAPI{}
	set := runWLStdin(t, b, nil, shown.stdout, "schedule", "set", "prod-eu/w-1", "--file", "-")
	if set.exit != 0 || len(b.writes) != 1 {
		t.Fatalf("exit %d: %s", set.exit, set.stderr)
	}
	var sent any
	json.Unmarshal([]byte(strings.TrimPrefix(b.writes[0], "PUT /iam/workloads/prod-eu/w-1/schedule ")), &sent)
	if !reflect.DeepEqual(sent, want) {
		t.Errorf("sent %v", b.writes[0])
	}
}

func runWLStdin(t *testing.T, a *wlAPI, tune func(*fleetDeps), stdin string, args ...string) fleetRan {
	t.Helper()
	return runFleetWith(t, &fakeAPI{custom: a.handler(t), stdin: stdin}, "eu-west-1", tune, append([]string{"workloads"}, args...)...)
}
