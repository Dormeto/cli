package main

// `dormeto aws schedule list|add|remove|except|edit` against a fake manager that holds each workload's schedule: the JSON
// each change writes is the one it read plus the change, every other field kept; what they print; the exit codes. show, set
// and clear, moved from `workloads schedule`, keep their tests in workloads_test.go.

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// scAPI is a fake manager holding the schedules, by "<router>/<id>": GET …/{id} answers a workload's (null for none), PUT
// …/schedule stores its body and answers it with one next event, unless refuse holds the 400 message it answers instead.
type scAPI struct {
	mu        sync.Mutex
	schedules map[string]string
	writes    []string // "PUT /iam/workloads/prod-eu/w-1/schedule {body}"
	reads     []string // the workloads' statuses read, by path
	refuse    string
}

// scListing is the manager's list: w-1 with a schedule, w-2 without, and a Google Cloud workload with one.
const scListing = `{"workloads":[` +
	`{"workloadId":"w-1","name":"Web shop","router":"prod-eu","state":"ASLEEP","scheduled":true},` +
	`{"workloadId":"w-2","router":"prod-eu","state":"AWAKE"},` +
	`{"workloadId":"shop","router":"dmtr-europe-west1","cloud":"gcp","state":"AWAKE","scheduled":true}` +
	`],"total":3,"cursor":null}`

const scNext = `[{"at":"2026-10-19T17:00:00.000Z","kind":"awake-end","window":0}]`

func (a *scAPI) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/iam/workloads/"), "/schedule")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/iam/workloads" && r.URL.Query().Get("tag") != "":
			io.WriteString(w, `{"workloads":[{"workloadId":"w-1","router":"prod-eu","state":"ASLEEP"},{"workloadId":"w-2","router":"prod-eu","state":"AWAKE"}],"total":2,"cursor":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/iam/workloads":
			io.WriteString(w, scListing)
		case r.Method == http.MethodGet:
			a.reads = append(a.reads, r.URL.Path)
			s := orString(a.schedules[key], "null")
			io.WriteString(w, `{"state":"ASLEEP","schedule":`+s+`,"next":`+scNext+`,"maintenanceDue":null}`)
		case r.Method == http.MethodPut:
			a.writes = append(a.writes, r.Method+" "+r.URL.Path+" "+string(body))
			if a.refuse != "" {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"bad_request","message":"`+a.refuse+`"}`)
				return
			}
			a.schedules[key] = string(body)
			io.WriteString(w, `{"schedule":`+string(body)+`,"next":`+scNext+`,"maintenanceDue":null}`)
		default:
			a.writes = append(a.writes, r.Method+" "+r.URL.Path)
			io.WriteString(w, `{"cleared":true}`)
		}
	}
}

func runSC(t *testing.T, a *scAPI, args ...string) fleetRan {
	t.Helper()
	if a.schedules == nil {
		a.schedules = map[string]string{}
	}
	return runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", nil, append([]string{"schedule"}, args...)...)
}

// keptSchedule has a field no CLI knows, on the schedule and on a window: a change keeps them.
const keptSchedule = `{"timeZone":"Europe/Paris","windows":[` +
	`{"kind":"awake","days":["mon","tue","wed","thu","fri"],"start":"08:00","end":"19:00","future":{"a":1}},` +
	`{"kind":"nap","days":["sat","sun"],"start":"00:00","end":"00:00","wake":"header","exceptions":["2027-02-06"]}` +
	`],"exceptions":["2026-12-25"],"maxNapDays":7,"joinMinutes":0,"by":"ops@example.com","at":"2026-10-01T00:00:00Z","unknown":"kept"}`

// written is the body of the one write, which must be the PUT of the workload's schedule.
func written(t *testing.T, a *scAPI, target string) string {
	t.Helper()
	if len(a.writes) != 1 {
		t.Fatalf("writes %q", a.writes)
	}
	prefix := "PUT /iam/workloads/" + target + "/schedule "
	if !strings.HasPrefix(a.writes[0], prefix) {
		t.Fatalf("write %q", a.writes[0])
	}
	return strings.TrimPrefix(a.writes[0], prefix)
}

// sameSchedule fails unless got is want once both are decoded.
func sameSchedule(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want: %v: %s", err, want)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("wrote %s\nwant  %s", got, want)
	}
}

// replaced is s with old (which it holds) replaced by new.
func replaced(t *testing.T, s, old, new string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("%q is not in %s", old, s)
	}
	return strings.Replace(s, old, new, 1)
}

func TestScheduleAddWritesTheScheduleReadWithTheWindowAppended(t *testing.T) {
	a := &scAPI{schedules: map[string]string{"prod-eu/w-1": keptSchedule}}
	r := runSC(t, a, "add", "prod-eu/w-1", "--kind", "nap", "--days", "mon,tue,wed,thu,fri", "--start", "20:00", "--end", "07:00", "--wake", "header")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	sameSchedule(t, written(t, a, "prod-eu/w-1"), replaced(t, keptSchedule, `"exceptions":["2027-02-06"]}`,
		`"exceptions":["2027-02-06"]},{"kind":"nap","days":["mon","tue","wed","thu","fri"],"start":"20:00","end":"07:00","wake":"header"}`))
	want := "prod-eu/w-1: schedule set\n" +
		"  next\n" +
		"    Mon 19 Oct 19:00 CEST awake window ends\n" +
		"prod-eu/w-1: time zone Europe/Paris\n" +
		"  1. awake mon–fri 08:00–19:00\n" +
		"  2. nap Weekly on Sat, Sun, all day (24 hours), except 2027-02-06, wake: header\n" +
		"  3. nap mon–fri 20:00–07:00 (next day), wake: header\n" +
		"  except 2026-12-25\n" +
		"  maximum nap 7 days\n"
	if r.stdout != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", r.stdout, want)
	}
}

func TestScheduleAddARecurrenceAndAFirstSchedule(t *testing.T) {
	a := &scAPI{}
	r := runSC(t, a, "add", "prod-eu/w-2", "--kind", "awake", "--repeat", "monthly", "--interval", "2", "--nth", "-1", "--weekday", "fri",
		"--start", "08:00", "--end", "18:00", "--from", "2027-01-01", "--until", "2027-12-31", "--time-zone", "Europe/Paris")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	sameSchedule(t, written(t, a, "prod-eu/w-2"), `{"timeZone":"Europe/Paris","windows":[{"kind":"awake","start":"08:00","end":"18:00",`+
		`"repeat":{"pattern":"monthly","interval":2,"nth":-1,"weekday":"fri"},"from":"2027-01-01","until":"2027-12-31"}]}`)
	// With --repeat weekly, --days are the repeat's; a cron's duration is --minutes.
	a = &scAPI{}
	runSC(t, a, "add", "prod-eu/w-2", "--kind", "nap", "--repeat", "weekly", "--days", "mon", "--days", "thu", "--interval", "2", "--from", "2027-01-04",
		"--start", "20:00", "--end", "07:00", "--time-zone", "UTC")
	sameSchedule(t, written(t, a, "prod-eu/w-2"), `{"timeZone":"UTC","windows":[{"kind":"nap","start":"20:00","end":"07:00","from":"2027-01-04",`+
		`"repeat":{"pattern":"weekly","interval":2,"days":["mon","thu"]}}]}`)
	a = &scAPI{}
	runSC(t, a, "add", "prod-eu/w-2", "--kind", "awake", "--repeat", "cron", "--cron", "30 8 * * 1-5", "--minutes", "630", "--time-zone", "UTC")
	sameSchedule(t, written(t, a, "prod-eu/w-2"), `{"timeZone":"UTC","windows":[{"kind":"awake","repeat":{"pattern":"cron","cron":"30 8 * * 1-5","minutes":630}}]}`)
}

// add takes several targets, or --tag, as set: each schedule is read and written on its own.
func TestScheduleAddToSeveralWorkloads(t *testing.T) {
	a := &scAPI{schedules: map[string]string{"prod-eu/w-1": `{"timeZone":"UTC","windows":[],"maxNapDays":3}`}}
	r := runSC(t, a, "add", "prod-eu/w-1", "prod-eu/w-2", "--kind", "awake", "--days", "mon", "--start", "08:00", "--end", "18:00", "--time-zone", "UTC", "--yes")
	if r.exit != 0 || len(a.writes) != 2 {
		t.Fatalf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	window := `{"kind":"awake","days":["mon"],"start":"08:00","end":"18:00"}`
	sameSchedule(t, strings.TrimPrefix(a.writes[0], "PUT /iam/workloads/prod-eu/w-1/schedule "), `{"timeZone":"UTC","windows":[`+window+`],"maxNapDays":3}`)
	sameSchedule(t, strings.TrimPrefix(a.writes[1], "PUT /iam/workloads/prod-eu/w-2/schedule "), `{"timeZone":"UTC","windows":[`+window+`]}`)
}

func TestScheduleRemoveTakesTheNumberedWindow(t *testing.T) {
	a := &scAPI{schedules: map[string]string{"prod-eu/w-1": keptSchedule}}
	r := runSC(t, a, "remove", "prod-eu/w-1", "--window", "1")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	sameSchedule(t, written(t, a, "prod-eu/w-1"), replaced(t, keptSchedule, `{"kind":"awake","days":["mon","tue","wed","thu","fri"],"start":"08:00","end":"19:00","future":{"a":1}},`, ""))
	if !strings.Contains(r.stdout, "prod-eu/w-1: schedule set\n") || !strings.Contains(r.stdout, "  1. nap Weekly on Sat, Sun, all day (24 hours), except 2027-02-06") || strings.Contains(r.stdout, "2. ") {
		t.Errorf("stdout %q", r.stdout)
	}
}

// A window's number out of range exits 2, nothing written; so does a change of a workload without a schedule.
func TestScheduleWindowNumbersOutOfRangeExit2(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"remove 3":        {[]string{"remove", "prod-eu/w-1", "--window", "3"}, "prod-eu/w-1: --window: 3 is not a window of the schedule, which has 2 (schedule show numbers them)\n"},
		"remove 0":        {[]string{"remove", "prod-eu/w-1", "--window", "0"}, "--window: a window's number, from 1 (schedule show numbers them)\n"},
		"except window 9": {[]string{"except", "prod-eu/w-1", "--date", "2027-01-01", "--window", "9"}, "prod-eu/w-1: --window: 9 is not a window of the schedule, which has 2"},
		"no schedule":     {[]string{"remove", "prod-eu/w-2", "--window", "1"}, "prod-eu/w-2: no schedule: schedule add makes one\n"},
	} {
		a := &scAPI{schedules: map[string]string{"prod-eu/w-1": keptSchedule}}
		r := runSC(t, a, c.args...)
		if r.exit != 2 || len(a.writes) != 0 || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%s: exit %d, writes %q, stderr %q", name, r.exit, a.writes, r.stderr)
		}
	}
}

// The router's refusal is printed as set prints it, exit 1; the last window's removal says what clear does.
func TestScheduleChangeRefusedKeepsSetsMessage(t *testing.T) {
	a := &scAPI{schedules: map[string]string{"prod-eu/w-1": keptSchedule}, refuse: "windows[2].start: HH:MM, minutes a multiple of 5"}
	r := runSC(t, a, "add", "prod-eu/w-1", "--kind", "awake", "--days", "mon", "--start", "08:03", "--end", "09:00")
	if r.exit != 1 || r.stderr != "prod-eu/w-1: windows[2].start: HH:MM, minutes a multiple of 5\n" || r.stdout != "" {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
	one := `{"timeZone":"UTC","windows":[{"kind":"awake","days":["mon"],"start":"08:00","end":"18:00"}]}`
	a = &scAPI{schedules: map[string]string{"prod-eu/w-1": one}, refuse: "schedule: windows or maxNapDays required"}
	r = runSC(t, a, "remove", "prod-eu/w-1", "--window", "1")
	if r.exit != 1 || r.stderr != "prod-eu/w-1: schedule: windows or maxNapDays required (that was its last window: schedule clear removes the schedule)\n" {
		t.Errorf("last window: exit %d, stderr %q", r.exit, r.stderr)
	}
	sameSchedule(t, written(t, a, "prod-eu/w-1"), `{"timeZone":"UTC","windows":[]}`)
}

func TestScheduleExceptAddsOrRemovesDates(t *testing.T) {
	a := &scAPI{schedules: map[string]string{"prod-eu/w-1": keptSchedule}}
	r := runSC(t, a, "except", "prod-eu/w-1", "--date", "2026-12-25,2027-01-01")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	sameSchedule(t, written(t, a, "prod-eu/w-1"), replaced(t, keptSchedule, `"exceptions":["2026-12-25"]`, `"exceptions":["2026-12-25","2027-01-01"]`))
	a = &scAPI{schedules: map[string]string{"prod-eu/w-1": keptSchedule}}
	runSC(t, a, "except", "prod-eu/w-1", "--date", "2027-02-13", "--window", "2")
	sameSchedule(t, written(t, a, "prod-eu/w-1"), replaced(t, keptSchedule, `"exceptions":["2027-02-06"]`, `"exceptions":["2027-02-06","2027-02-13"]`))
	a = &scAPI{schedules: map[string]string{"prod-eu/w-1": keptSchedule}}
	runSC(t, a, "except", "prod-eu/w-1", "--date", "2026-12-25", "--remove")
	sameSchedule(t, written(t, a, "prod-eu/w-1"), replaced(t, keptSchedule, `"exceptions":["2026-12-25"],`, ""))
}

func TestScheduleEditSetsOrRemovesTheSchedulesFields(t *testing.T) {
	a := &scAPI{schedules: map[string]string{"prod-eu/w-1": keptSchedule}}
	r := runSC(t, a, "edit", "prod-eu/w-1", "--time-zone", "UTC", "--join-minutes", "inherit", "--max-nap-days", "14")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	want := replaced(t, replaced(t, keptSchedule, `"maxNapDays":7,"joinMinutes":0`, `"maxNapDays":14`), `"Europe/Paris"`, `"UTC"`)
	sameSchedule(t, written(t, a, "prod-eu/w-1"), want)
	// An empty value removes a field the JSON may lack; a number sets it.
	a = &scAPI{schedules: map[string]string{"prod-eu/w-1": keptSchedule}}
	runSC(t, a, "edit", "prod-eu/w-1", "--max-nap-days", "", "--join-minutes", "30")
	sameSchedule(t, written(t, a, "prod-eu/w-1"), replaced(t, keptSchedule, `"maxNapDays":7,"joinMinutes":0`, `"joinMinutes":30`))
	// Several targets, by --tag as set takes them.
	a = &scAPI{schedules: map[string]string{"prod-eu/w-1": keptSchedule}}
	r = runSC(t, a, "edit", "--tag", "team=payments", "--max-nap-days", "3", "--yes")
	if r.exit != 0 || len(a.writes) != 2 {
		t.Errorf("by tag: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

// --dry-run reads the schedule and says the PUT it would send, the resulting schedule as its body; nothing is written.
func TestScheduleChangeDryRunSaysTheScheduleItWouldWrite(t *testing.T) {
	a := &scAPI{schedules: map[string]string{"prod-eu/w-1": `{"timeZone":"UTC","windows":[],"maxNapDays":3,"x":1}`}}
	r := runSC(t, a, "edit", "prod-eu/w-1", "--max-nap-days", "5", "--dry-run")
	want := "dry run: prod-eu/w-1: would PUT /iam/workloads/prod-eu/w-1/schedule, nothing sent\n" +
		`  {"maxNapDays":5,"timeZone":"UTC","windows":[],"x":1}` + "\n"
	if r.exit != 0 || len(a.writes) != 0 || r.stdout != want || len(a.reads) != 1 {
		t.Errorf("exit %d, writes %q, reads %q, stdout %q, stderr %q", r.exit, a.writes, a.reads, r.stdout, r.stderr)
	}
}

// A flag of the wrong form exits 2 before any call; the values the router owns (a multiple of 5, the kinds of repeat) are its.
func TestScheduleFlagsOfTheWrongFormExit2(t *testing.T) {
	for name, args := range map[string][]string{
		"add no kind":            {"add", "prod-eu/w-1", "--days", "mon", "--start", "08:00", "--end", "09:00"},
		"add bad kind":           {"add", "prod-eu/w-1", "--kind", "sleep"},
		"add bad start":          {"add", "prod-eu/w-1", "--kind", "nap", "--start", "8:00"},
		"add bad until":          {"add", "prod-eu/w-1", "--kind", "nap", "--until", "2027-02-30"},
		"add interval no repeat": {"add", "prod-eu/w-1", "--kind", "nap", "--interval", "2"},
		"add no target":          {"add", "--kind", "nap"},
		"remove no window":       {"remove", "prod-eu/w-1"},
		"remove two":             {"remove", "prod-eu/w-1", "prod-eu/w-2", "--window", "1"},
		"except no date":         {"except", "prod-eu/w-1"},
		"except bad date":        {"except", "prod-eu/w-1", "--date", "25/12"},
		"edit nothing":           {"edit", "prod-eu/w-1"},
		"edit bad join":          {"edit", "prod-eu/w-1", "--join-minutes", "soon"},
		"edit bad nap":           {"edit", "prod-eu/w-1", "--max-nap-days", "-1"},
		"list argument":          {"list", "prod-eu/w-1"},
	} {
		a := &scAPI{}
		r := runSC(t, a, args...)
		if r.exit != 2 || len(a.writes) != 0 || len(a.reads) != 0 {
			t.Errorf("%s: exit %d, writes %q, reads %q, stderr %q", name, r.exit, a.writes, a.reads, r.stderr)
		}
	}
}

func TestScheduleListShowsTheCalendar(t *testing.T) {
	a := &scAPI{schedules: map[string]string{"prod-eu/w-1": keptSchedule, "dmtr-europe-west1/shop": `{"timeZone":"UTC","windows":[],"maxNapDays":3}`}}
	r := runSC(t, a, "list")
	want := "ROUTER             WORKLOAD  NAME      CLOUD  STATE   ZONE          WINDOWS  NEXT\n" +
		"prod-eu            w-1       Web shop  aws    ASLEEP  Europe/Paris  2        Mon 19 Oct 19:00 CEST awake window ends\n" +
		"dmtr-europe-west1  shop      —         gcp    ASLEEP  UTC           0        Mon 19 Oct 17:00 UTC awake window ends\n" +
		"2 workloads with a schedule\n"
	if r.exit != 0 || r.stdout != want || r.stderr != "" {
		t.Errorf("exit %d, stdout:\n%s\nwant:\n%s\nstderr %q", r.exit, r.stdout, want, r.stderr)
	}
	// Only the workloads with a schedule are read.
	if len(a.reads) != 2 || strings.Contains(strings.Join(a.reads, " "), "w-2") {
		t.Errorf("reads %q", a.reads)
	}
	// --all adds those without, unread; --cloud keeps one cloud's rows.
	a.reads = nil
	r = runSC(t, a, "list", "--all")
	if r.exit != 0 || !strings.Contains(r.stdout, "prod-eu            w-2       —         aws    AWAKE   —             —        —\n") ||
		!strings.HasSuffix(r.stdout, "3 workloads, 2 with a schedule\n") || len(a.reads) != 2 {
		t.Errorf("--all: exit %d, reads %q, stdout:\n%s", r.exit, a.reads, r.stdout)
	}
	r = runSC(t, a, "list", "--cloud", "gcp")
	if r.exit != 0 || strings.Contains(r.stdout, "prod-eu") || !strings.HasSuffix(r.stdout, "1 workload with a schedule\n") {
		t.Errorf("--cloud gcp: exit %d, stdout:\n%s", r.exit, r.stdout)
	}
	r = runSC(t, a, "list", "--json")
	var got struct {
		Workloads []map[string]any `json:"workloads"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || r.exit != 0 || len(got.Workloads) != 2 {
		t.Fatalf("--json: exit %d, %v: %s", r.exit, err, r.stdout)
	}
	if s, _ := got.Workloads[0]["schedule"].(map[string]any); got.Workloads[0]["workloadId"] != "w-1" || s["unknown"] != "kept" || got.Workloads[0]["next"] == nil {
		t.Errorf("--json row %v", got.Workloads[0])
	}
}

// `workloads schedule` is gone, with no alias: `schedule` is a method of its own, and the old path sends nothing.
func TestWorkloadsScheduleIsGone(t *testing.T) {
	for _, c := range managerCommands(fleetDeps{}) {
		if c.Name() == "workloads" {
			if sub, _, err := c.Find([]string{"schedule"}); err == nil && sub != c {
				t.Errorf("workloads still has %s", sub.CommandPath())
			}
		}
	}
	a := &scAPI{}
	r := runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", nil, "workloads", "schedule", "clear", "prod-eu/w-1")
	if len(a.writes) != 0 || len(a.reads) != 0 || strings.Contains(r.stdout, "cleared") {
		t.Errorf("workloads schedule clear: exit %d, writes %q, stdout %q", r.exit, a.writes, r.stdout)
	}
}

// A word that is no command of a group exits 2 with cobra's words, whatever group, and sends nothing; a bare group prints its help.
func TestUnknownCommandsOfAGroupExit2AndSendNothing(t *testing.T) {
	for _, args := range [][]string{{"workloads", "schedule", "clear", "a/b"}, {"schedule", "bogus"}, {"workloads", "schedule", "set", "a/b", "--file", "x"}} {
		a := &scAPI{}
		r := runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", nil, args...)
		want := "unknown command \"" + args[1] + "\" for \"dormeto aws " + args[0] + "\"\n"
		if r.exit != 2 || r.stderr != want || r.stdout != "" || len(a.writes)+len(a.reads) != 0 || len(r.api.requests) != 0 {
			t.Errorf("%v: exit %d, stdout %q, stderr %q (want %q), requests %d", args, r.exit, r.stdout, r.stderr, want, len(r.api.requests))
		}
	}
	for _, args := range [][]string{
		{"gcp", "workloads", "schedule", "show", "w", "--project", "p", "--region", "europe-west1"},
		{"gcp", "schedule", "bogus", "--project", "p"},
	} {
		calls, errs, exit := gcpRan(t, 0, args...)
		want := "unknown command \"" + args[2] + "\" for \"dormeto gcp " + args[1] + "\"\n"
		if exit != 2 || errs != want || len(calls) != 0 {
			t.Errorf("%v: exit %d, stderr %q (want %q), calls %v", args, exit, errs, want, calls)
		}
	}
	for _, args := range [][]string{{"aws", "schedule"}, {"gcp", "schedule"}, {"aws", "workloads", "waiting-page"}, {"gcp", "workloads", "access"}, {"aws", "schedule", "--help"}} {
		out, errs, code := executed(t, build{}, args...)
		if code != 0 || errs != "" || !strings.Contains(out, "Available Commands:") {
			t.Errorf("%v: exit %d, stderr %q, stdout %q", args, code, errs, out)
		}
	}
	out, errs, code := executed(t, build{}, "aws", "schedule", "bogus")
	if code != 2 || out != "" || errs != "unknown command \"bogus\" for \"dormeto aws schedule\"\n" {
		t.Errorf("newRoot: exit %d, stdout %q, stderr %q", code, out, errs)
	}
}

// With several targets, a target refused before its write exits 2 only when every target was; one written makes it 1.
func TestScheduleChangeOfSeveralExits1WhenOneWasWritten(t *testing.T) {
	a := &scAPI{schedules: map[string]string{"prod-eu/w-1": keptSchedule}}
	r := runSC(t, a, "add", "prod-eu/w-1", "prod-eu/w-2", "--kind", "awake", "--days", "mon", "--start", "06:00", "--end", "07:00", "--yes")
	if r.exit != 1 || len(a.writes) != 1 || !strings.Contains(a.writes[0], "/prod-eu/w-1/") ||
		r.stderr != "prod-eu/w-2: --time-zone: the workload has no schedule: a first one needs its time zone\n" {
		t.Errorf("one written: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	a = &scAPI{}
	r = runSC(t, a, "remove", "prod-eu/w-1", "--window", "1")
	if r.exit != 2 || len(a.writes) != 0 {
		t.Errorf("none written: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	a = &scAPI{}
	r = runSC(t, a, "add", "prod-eu/w-1", "prod-eu/w-2", "--kind", "awake", "--days", "mon", "--start", "06:00", "--end", "07:00", "--yes")
	if r.exit != 2 || len(a.writes) != 0 || strings.Count(r.stderr, "--time-zone: the workload has no schedule") != 2 {
		t.Errorf("all refused: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

// The last window's hint is added to the router's refusal of an empty schedule alone.
func TestScheduleRemoveHintsAtClearOnlyForTheEmptyScheduleRefusal(t *testing.T) {
	one := `{"timeZone":"UTC","windows":[{"kind":"awake","days":["mon"],"start":"08:00","end":"18:00"}]}`
	a := &scAPI{schedules: map[string]string{"prod-eu/w-1": one}, refuse: "you may not change this workload"}
	r := runSC(t, a, "remove", "prod-eu/w-1", "--window", "1")
	if r.exit != 1 || r.stderr != "prod-eu/w-1: you may not change this workload\n" {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
}

// A nap window's wake on a standalone workload: the router's refusal, printed as it comes, exit 1 (the CLI does not
// know the workload's kind before its write).
func TestScheduleAddWakeOnAStandaloneWorkloadPrintsTheRoutersRefusal(t *testing.T) {
	refusal := "windows[2].wake: a standalone workload has no waiting page: nothing arrives to wake it in a nap window"
	a := &scAPI{schedules: map[string]string{"prod-eu/w-1": keptSchedule}, refuse: refusal}
	r := runSC(t, a, "add", "prod-eu/w-1", "--kind", "nap", "--days", "sat", "--start", "00:00", "--end", "00:00", "--wake", "header")
	if r.exit != 1 || r.stderr != "prod-eu/w-1: "+refusal+"\n" || r.stdout != "" {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
}
