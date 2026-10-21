package main

// `dormeto workloads wake|nap|pause|rename|tags` against a fake manager API: the requests they make, what they print,
// the exit codes, and that a refused argument sends nothing.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// wrAPI is a fake manager for the workload writes: it records the writes, serves each workload's tags and states and
// refuses the writes of the paths in refuse.
type wrAPI struct {
	mu       sync.Mutex
	writes   []string
	tags     map[string]string // "prod-eu/w-1" -> the tags JSON GET reports ("" for no tags key)
	statuses []string          // the states GET …/{id} reports, the last one repeated
	reads    int
	refuse   map[string]string // write path -> 409 message
}

func (a *wrAPI) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			a.writes = append(a.writes, r.Method+" "+r.URL.Path+" "+string(body))
			if msg, ok := a.refuse[r.URL.Path]; ok {
				w.WriteHeader(409)
				io.WriteString(w, `{"error":"conflict","message":"`+msg+`"}`)
				return
			}
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/iam/workloads":
			io.WriteString(w, `{"workloads":[{"workloadId":"w-1","router":"prod-eu","state":"ASLEEP"},{"workloadId":"w-2","router":"prod-eu","state":"ASLEEP"}],"total":2,"cursor":null}`)
		case r.Method == http.MethodGet:
			key := strings.TrimPrefix(r.URL.Path, "/iam/workloads/")
			st := `{"state":"AWAKE"`
			if len(a.statuses) > 0 {
				st = `{"state":"` + a.statuses[min(a.reads, len(a.statuses)-1)] + `"`
				a.reads++
			}
			if t := a.tags[key]; t != "" {
				st += `,"tags":` + t
			}
			io.WriteString(w, st+"}")
		case strings.HasSuffix(r.URL.Path, "/nap"):
			w.WriteHeader(202)
			io.WriteString(w, `{"accepted":true}`)
		case strings.HasSuffix(r.URL.Path, "/pause"):
			io.WriteString(w, `{"woken":["w-1"]}`)
		case strings.HasSuffix(r.URL.Path, "/rename") && r.Method == http.MethodPost: // the manager routes POST alone
			io.WriteString(w, `{"workloadId":"w-1","name":"checkout api"}`)
		case strings.HasSuffix(r.URL.Path, "/rename"):
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"not_found","message":"no route for `+r.Method+` `+r.URL.Path+`"}`)
		case strings.HasSuffix(r.URL.Path, "/tags"):
			io.WriteString(w, `{"workloadId":"w-1","tags":{}}`)
		default:
			io.WriteString(w, `{}`)
		}
	}
}

func runWR(t *testing.T, a *wrAPI, tune func(*fleetDeps), args ...string) fleetRan {
	t.Helper()
	return runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", tune, append([]string{"workloads"}, args...)...)
}

func TestWakeWaitFollowsToAwake(t *testing.T) {
	a := &wrAPI{statuses: []string{"ASLEEP", "WAKING", "AWAKE"}}
	var sleeps []time.Duration
	r := runWR(t, a, func(d *fleetDeps) {
		d.sleep = func(_ context.Context, x time.Duration) error { sleeps = append(sleeps, x); return nil }
	}, "wake", "prod-eu/w-1", "--wait")
	if r.exit != 0 || a.reads != 3 || len(sleeps) != 2 {
		t.Fatalf("exit %d, reads %d, sleeps %v: %s", r.exit, a.reads, sleeps, r.stderr)
	}
	if want := []string{"POST /iam/workloads/prod-eu/w-1/wake "}; !reflect.DeepEqual(a.writes, want) {
		t.Errorf("writes %q", a.writes)
	}
	if !strings.Contains(r.stdout, "prod-eu/w-1: wake requested") || !strings.Contains(r.stdout, "prod-eu/w-1: awake") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestWakeWithoutWaitReadsNothing(t *testing.T) {
	a := &wrAPI{}
	r := runWR(t, a, nil, "wake", "prod-eu/w-1")
	if r.exit != 0 || a.reads != 0 || len(a.writes) != 1 {
		t.Errorf("exit %d, reads %d, writes %q", r.exit, a.reads, a.writes)
	}
}

func TestWakeWaitTimeoutDoesNotTalkOfAPause(t *testing.T) {
	a := &wrAPI{statuses: []string{"WAKING"}}
	r := runWR(t, a, func(d *fleetDeps) {
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
	}, "wake", "prod-eu/w-1", "--wait", "--timeout", "10s")
	if r.exit != 1 || !strings.Contains(r.stderr, "timeout of 10s reached: not awake: prod-eu/w-1") || strings.Contains(r.stderr, "pause") {
		t.Errorf("exit %d stderr %q", r.exit, r.stderr)
	}
}

func TestNapOfTwoOneRefusedExits1(t *testing.T) {
	a := &wrAPI{refuse: map[string]string{"/iam/workloads/prod-eu/w-2/nap": "paused"}}
	r := runWR(t, a, nil, "nap", "prod-eu/w-1", "prod-eu/w-2", "--yes")
	if r.exit != 1 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	if len(a.writes) != 2 || !strings.Contains(r.stdout, "prod-eu/w-1: nap requested") || !strings.Contains(r.stderr, "prod-eu/w-2: ") || !strings.Contains(r.stderr, "paused") {
		t.Errorf("writes %q stdout %q stderr %q", a.writes, r.stdout, r.stderr)
	}
}

func TestPauseWithoutEndSendsNoUntil(t *testing.T) {
	a := &wrAPI{}
	r := runWR(t, a, nil, "pause", "prod-eu/w-1", "--reason", "  maintenance ")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	if want := []string{`POST /iam/workloads/prod-eu/w-1/pause {"reason":"maintenance"}`}; !reflect.DeepEqual(a.writes, want) {
		t.Errorf("writes %q", a.writes)
	}
	if !strings.Contains(r.stdout, "prod-eu/w-1: paused until resumed") || !strings.Contains(r.stdout, "woken w-1") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestPauseForAndKeepAsleep(t *testing.T) {
	a := &wrAPI{}
	r := runWR(t, a, nil, "pause", "prod-eu/w-1", "--reason", "freeze", "--for", "2d", "--keep-asleep")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	want := []string{`POST /iam/workloads/prod-eu/w-1/pause {"reason":"freeze","until":"2026-10-21T12:00:00Z","keepAsleep":true}`}
	if !reflect.DeepEqual(a.writes, want) {
		t.Errorf("writes %q", a.writes)
	}
	if !strings.Contains(r.stdout, "paused until 2026-10-21 12:00 UTC") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestPauseRefusalsSendNothing(t *testing.T) {
	long := strings.Repeat("x", 257)
	for _, args := range [][]string{
		{"pause", "prod-eu/w-1"},
		{"pause", "prod-eu/w-1", "--reason", "  "},
		{"pause", "prod-eu/w-1", "--reason", long},
		{"pause", "prod-eu/w-1", "--reason", "x", "--for", "367d"},
		{"pause", "prod-eu/w-1", "--reason", "x", "--for", "1h", "--until", "23:59"},
	} {
		a := &wrAPI{}
		r := runWR(t, a, nil, args...)
		if r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("%q: exit %d, writes %q, stderr %q", args, r.exit, a.writes, r.stderr)
		}
	}
	a := &wrAPI{}
	if r := runWR(t, a, nil, "pause", "prod-eu/w-1", "--reason", "x", "--for", "366d"); r.exit != 0 {
		t.Errorf("366d: exit %d: %s", r.exit, r.stderr)
	}
	// The manager's rule of a pause's reason is the routers' (pauseRequest): the same check, the same words.
	for _, reason := range []string{"  ", long} {
		if r := runWR(t, &wrAPI{}, nil, "pause", "prod-eu/w-1", "--reason", reason); r.stderr != "--reason: give 1 to 256 characters\n" {
			t.Errorf("reason of %d characters: stderr %q", len(reason), r.stderr)
		}
	}
}

func TestRenameSendsTheTrimmedName(t *testing.T) {
	a := &wrAPI{}
	r := runWR(t, a, nil, "rename", "prod-eu/w-1", "  checkout api ")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	if want := []string{`POST /iam/workloads/prod-eu/w-1/rename {"name":"checkout api"}`}; !reflect.DeepEqual(a.writes, want) {
		t.Errorf("writes %q", a.writes)
	}
	if !strings.Contains(r.stdout, "prod-eu/w-1: renamed to checkout api") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestRenameRefusalsSendNothing(t *testing.T) {
	for _, args := range [][]string{
		{"rename", "prod-eu/w-1", "prod-eu/w-2", "name"},
		{"rename", "prod-eu/w-1"},
		{"rename", "--tag", "env=dev", "name"},
		{"rename", "prod-eu/w-1", "--tag", "env=dev", "name"},
		{"rename", "prod-eu/w-1", "   "},
		{"rename", "prod-eu/w-1", strings.Repeat("x", 65)},
		{"rename", "prod-eu/w-1", "a\tb"},
		{"rename", "w-1", "name"},
	} {
		a := &wrAPI{}
		r := runWR(t, a, nil, args...)
		if r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("%q: exit %d, writes %q, stderr %q", args, r.exit, a.writes, r.stderr)
		}
	}
}

func TestTagsSetAndUnsetKeepTheOthers(t *testing.T) {
	a := &wrAPI{tags: map[string]string{"prod-eu/w-1": `{"b":"2","c":"3"}`}}
	r := runWR(t, a, nil, "tags", "prod-eu/w-1", "--set", "a=1", "--unset", "b")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	if want := []string{`PUT /iam/workloads/prod-eu/w-1/tags {"tags":{"a":"1","c":"3"}}`}; !reflect.DeepEqual(a.writes, want) {
		t.Errorf("writes %q", a.writes)
	}
	if !strings.Contains(r.stdout, "prod-eu/w-1: a=1, c=3") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestTagsClearThenSet(t *testing.T) {
	a := &wrAPI{tags: map[string]string{"prod-eu/w-1": `{"b":"2"}`}}
	r := runWR(t, a, nil, "tags", "prod-eu/w-1", "--clear", "--set", "a=1")
	if want := []string{`PUT /iam/workloads/prod-eu/w-1/tags {"tags":{"a":"1"}}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Errorf("exit %d writes %q: %s", r.exit, a.writes, r.stderr)
	}
	a = &wrAPI{tags: map[string]string{"prod-eu/w-1": `{"b":"2"}`}}
	r = runWR(t, a, nil, "tags", "prod-eu/w-1", "--clear")
	if want := []string{`PUT /iam/workloads/prod-eu/w-1/tags {"tags":{}}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) || !strings.Contains(r.stdout, "prod-eu/w-1: no tags") {
		t.Errorf("exit %d writes %q stdout %q", r.exit, a.writes, r.stdout)
	}
}

func TestTagsUnchangedSendsNothing(t *testing.T) {
	a := &wrAPI{tags: map[string]string{"prod-eu/w-1": `{"a":"1"}`, "prod-eu/w-2": ""}}
	r := runWR(t, a, nil, "tags", "prod-eu/w-1", "prod-eu/w-2", "--set", "a=1", "--unset", "zz", "--yes")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	if want := []string{`PUT /iam/workloads/prod-eu/w-2/tags {"tags":{"a":"1"}}`}; !reflect.DeepEqual(a.writes, want) {
		t.Errorf("writes %q", a.writes)
	}
	if !strings.Contains(r.stdout, "prod-eu/w-1: unchanged") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestTagsRefusalsSendNothing(t *testing.T) {
	fifty := `{`
	for i := 0; i < 50; i++ {
		if i > 0 {
			fifty += ","
		}
		fifty += `"k` + string(rune('a'+i/26)) + string(rune('a'+i%26)) + `":"v"`
	}
	fifty += `}`
	for name, c := range map[string]struct {
		args []string
		tags string
	}{
		"no flag":           {[]string{"tags", "prod-eu/w-1"}, ""},
		"tag argument":      {[]string{"tags", "prod-eu/w-1", "a=1"}, ""},
		"reserved":          {[]string{"tags", "prod-eu/w-1", "--set", "dormeto:x=1"}, ""},
		"reserved case":     {[]string{"tags", "prod-eu/w-1", "--set", "Dormeto:x=1"}, ""},
		"no equal":          {[]string{"tags", "prod-eu/w-1", "--set", "a"}, ""},
		"bad character":     {[]string{"tags", "prod-eu/w-1", "--set", "a=1,2"}, ""},
		"long key":          {[]string{"tags", "prod-eu/w-1", "--set", strings.Repeat("k", 129) + "=1"}, ""},
		"long value":        {[]string{"tags", "prod-eu/w-1", "--set", "a=" + strings.Repeat("v", 257)}, ""},
		"twice":             {[]string{"tags", "prod-eu/w-1", "--set", "a=1", "--set", "a=2"}, ""},
		"set and unset":     {[]string{"tags", "prod-eu/w-1", "--set", "a=1", "--unset", "a"}, ""},
		"empty unset":       {[]string{"tags", "prod-eu/w-1", "--unset", ""}, ""},
		"fifty-first":       {[]string{"tags", "prod-eu/w-1", "--set", "extra=1"}, fifty},
		"no target":         {[]string{"tags", "--set", "a=1"}, ""},
		"targets and --tag": {[]string{"tags", "prod-eu/w-1", "--tag", "env=dev", "--set", "a=1"}, ""},
	} {
		a := &wrAPI{tags: map[string]string{"prod-eu/w-1": orString(c.tags, "{}")}}
		r := runWR(t, a, nil, c.args...)
		if r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("%s: exit %d, writes %q, stderr %q", name, r.exit, a.writes, r.stderr)
		}
	}
}

func TestTagsKeyMayHoldASlashAndSpaces(t *testing.T) {
	a := &wrAPI{}
	r := runWR(t, a, nil, "tags", "prod-eu/w-1", "--set", "team/name=Pay ments@eu")
	if want := []string{`PUT /iam/workloads/prod-eu/w-1/tags {"tags":{"team/name":"Pay ments@eu"}}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Errorf("exit %d writes %q: %s", r.exit, a.writes, r.stderr)
	}
}

func TestTagsOfTheWorkloadsOfATagSelection(t *testing.T) {
	a := &wrAPI{tags: map[string]string{"prod-eu/w-1": `{"env":"dev"}`, "prod-eu/w-2": `{"env":"dev"}`}}
	r := runWR(t, a, nil, "tags", "--tag", "env=dev", "--set", "owner=me", "--yes")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	want := []string{
		`PUT /iam/workloads/prod-eu/w-1/tags {"tags":{"env":"dev","owner":"me"}}`,
		`PUT /iam/workloads/prod-eu/w-2/tags {"tags":{"env":"dev","owner":"me"}}`,
	}
	if !reflect.DeepEqual(a.writes, want) {
		t.Errorf("writes %q", a.writes)
	}
}

func TestTagsDryRunSendsNothing(t *testing.T) {
	a := &wrAPI{}
	r := runWR(t, a, nil, "tags", "--tag", "env=dev", "--set", "a=1", "--dry-run")
	if r.exit != 0 || len(a.writes) != 0 {
		t.Errorf("exit %d writes %q", r.exit, a.writes)
	}
}

func TestTagsRefusedByTheRouterExits1(t *testing.T) {
	a := &wrAPI{refuse: map[string]string{"/iam/workloads/prod-eu/w-1/tags": "router too old"}}
	r := runWR(t, a, nil, "tags", "prod-eu/w-1", "prod-eu/w-2", "--set", "a=1", "--yes")
	if r.exit != 1 || len(a.writes) != 2 || !strings.Contains(r.stderr, "prod-eu/w-1: ") || !strings.Contains(r.stdout, "prod-eu/w-2: a=1") {
		t.Errorf("exit %d writes %q stdout %q stderr %q", r.exit, a.writes, r.stdout, r.stderr)
	}
}

// jsonOut decodes a --json stdout: the answers by target.
func jsonOut(t *testing.T, stdout string) map[string]map[string]any {
	t.Helper()
	var m map[string]map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("stdout is not the answers by target: %v: %q", err, stdout)
	}
	return m
}

func TestWriteJSONPrintsTheAnswersOfTheSucceededOnStdoutAndTheLinesOnStderr(t *testing.T) {
	a := &wrAPI{refuse: map[string]string{"/iam/workloads/prod-eu/w-2/nap": "paused"}}
	r := runWR(t, a, nil, "nap", "prod-eu/w-1", "prod-eu/w-2", "--yes", "--json")
	m := jsonOut(t, r.stdout)
	if r.exit != 1 || len(m) != 1 || m["prod-eu/w-1"]["accepted"] != true {
		t.Errorf("exit %d stdout %q", r.exit, r.stdout)
	}
	if !strings.Contains(r.stderr, "prod-eu/w-1: nap requested") || !strings.Contains(r.stderr, "prod-eu/w-2: ") {
		t.Errorf("stderr %q", r.stderr)
	}
}

func TestWriteJSONOfEveryWorkloadWrite(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		key  string
	}{
		"wake":           {[]string{"wake", "prod-eu/w-1"}, ""},
		"pause":          {[]string{"pause", "prod-eu/w-1", "--reason", "x"}, "woken"},
		"rename":         {[]string{"rename", "prod-eu/w-1", "n"}, "name"},
		"tags":           {[]string{"tags", "prod-eu/w-1", "--set", "a=1"}, "tags"},
		"keep-awake":     {[]string{"keep-awake", "prod-eu/w-1", "--for", "1h"}, "woken"},
		"resume":         {[]string{"resume", "prod-eu/w-1"}, ""},
		"schedule clear": {[]string{"schedule", "clear", "prod-eu/w-1"}, "cleared"},
	} {
		a := &wrAPI{}
		r := runWR(t, a, nil, append(c.args, "--json")...)
		m := jsonOut(t, r.stdout)
		if r.exit != 0 || len(m) != 1 || m["prod-eu/w-1"] == nil {
			t.Errorf("%s: exit %d stdout %q stderr %q", name, r.exit, r.stdout, r.stderr)
		}
		if c.key != "" && m["prod-eu/w-1"][c.key] == nil && name != "schedule clear" {
			t.Errorf("%s: no %q in %q", name, c.key, r.stdout)
		}
		if !strings.Contains(r.stderr, "prod-eu/w-1: ") {
			t.Errorf("%s: stderr %q", name, r.stderr)
		}
	}
}

func TestScheduleSetJSON(t *testing.T) {
	a := &wrAPI{}
	f := t.TempDir() + "/s.json"
	if err := os.WriteFile(f, []byte(`{"maxNapDays":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runWR(t, a, nil, "schedule", "set", "prod-eu/w-1", "--file", f, "--json")
	if m := jsonOut(t, r.stdout); r.exit != 0 || m["prod-eu/w-1"] == nil || strings.Contains(r.stdout, "schedule set") {
		t.Errorf("exit %d stdout %q stderr %q", r.exit, r.stdout, r.stderr)
	}
}

func TestTagsJSONHoldsTheCurrentTagsOfTheUnchanged(t *testing.T) {
	a := &wrAPI{tags: map[string]string{"prod-eu/w-1": `{"a":"1"}`}}
	r := runWR(t, a, nil, "tags", "prod-eu/w-1", "--set", "a=1", "--json")
	m := jsonOut(t, r.stdout)
	tags, _ := m["prod-eu/w-1"]["tags"].(map[string]any)
	if r.exit != 0 || len(a.writes) != 0 || tags["a"] != "1" || !strings.Contains(r.stderr, "unchanged") {
		t.Errorf("exit %d writes %q stdout %q stderr %q", r.exit, a.writes, r.stdout, r.stderr)
	}
	if m["prod-eu/w-1"]["workloadId"] != "w-1" { // the shape of the PUT's answer
		t.Errorf("the unchanged answer lacks the workload's id: %q", r.stdout)
	}
}

// The help says what each command does: offboard and exclude always ask, schedule show's --json takes no value.
func TestWorkloadWritesHelp(t *testing.T) {
	for _, cmd := range []string{"offboard", "exclude"} {
		r := runWR(t, &wrAPI{}, nil, cmd, "--help")
		if !strings.Contains(r.stdout, "--yes ") || !strings.Contains(r.stdout, "do not ask for confirmation (needed without a terminal)") || strings.Contains(r.stdout, "of several targets") {
			t.Errorf("%s --help: %q", cmd, r.stdout)
		}
	}
	r := runWR(t, &wrAPI{}, nil, "schedule", "show", "--help")
	if !strings.Contains(r.stdout, "      --json   print {schedule, next, maintenanceDue}") {
		t.Errorf("schedule show --help: %q", r.stdout)
	}
	r = runWR(t, &wrAPI{}, nil, "tags", "--help")
	if !strings.Contains(r.stdout, `{"workloadId", "tags"}`) && !strings.Contains(r.stdout, "{workloadId, tags}") {
		t.Errorf("tags --help says nothing of --json: %q", r.stdout)
	}
}

func TestWakeWaitJSONKeepsStdoutToTheAnswers(t *testing.T) {
	a := &wrAPI{statuses: []string{"WAKING", "AWAKE"}}
	r := runWR(t, a, func(d *fleetDeps) {
		d.sleep = func(context.Context, time.Duration) error { return nil }
	}, "wake", "prod-eu/w-1", "--wait", "--json")
	jsonOut(t, r.stdout)
	if r.exit != 0 || !strings.Contains(r.stderr, "prod-eu/w-1: awake") {
		t.Errorf("exit %d stderr %q", r.exit, r.stderr)
	}
}
