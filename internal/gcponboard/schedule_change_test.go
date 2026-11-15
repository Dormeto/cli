package gcponboard

// `dormeto gcp schedule list|add|remove|except|edit` against the fake controller: a change reads the workload's schedule, writes
// it whole with the change and every other field kept, and prints AWS's lines; the list reads each workload's schedule.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/schedule"
)

// keptStored is shop's schedule as the controller answers it: who set it and when, and a field no CLI knows.
func keptStored() map[string]any {
	return map[string]any{"timeZone": "Europe/Paris", "windows": []any{
		map[string]any{"kind": "awake", "days": []any{"mon", "tue"}, "start": "08:00", "end": "19:00", "future": map[string]any{"a": 1.0}},
	}, "maxNapDays": 7.0, "unknown": "kept", "by": "ops@example.com", "at": "2026-10-01T00:00:00.000Z"}
}

func changeRig(t *testing.T) (*workloadsRig, Request) {
	t.Helper()
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.Schedules["shop"] = keptStored()
	g.s.NextEvents = parisEvents
	r := workloadsEurope
	r.ID = "shop"
	return g, r
}

func TestScheduleChangeWritesTheScheduleReadWithTheChange(t *testing.T) {
	g, r := changeRig(t)
	r.ScheduleChange = &schedule.Change{Add: &schedule.NewWindow{Kind: "nap", Days: []string{"sat"}, Start: "00:00", End: "00:00", Wake: "header"}}
	code, out := g.call(t, WorkloadsScheduleChange, r)
	want := shopTarget + ": schedule set\n" +
		"  next\n" +
		"    Mon 2 Nov 22:00 CET nap window starts\n" +
		"    Tue 3 Nov 06:00 CET nap window ends\n" +
		shopTarget + ": time zone Europe/Paris\n" +
		"  1. awake mon,tue 08:00–19:00\n" +
		"  2. nap sat 00:00–00:00 (next day), wake: header\n" +
		"  maximum nap 7 days\n"
	if code != 0 || out != want {
		t.Fatalf("exit %d:\n%s\nwant:\n%s", code, out, want)
	}
	// The body is the schedule read, the window appended, by the operator.
	body := g.body(t, "PUT", controllerHost, "/v1/workloads/shop/schedule")
	expected := keptStored()
	expected["windows"] = append(expected["windows"].([]any), map[string]any{"kind": "nap", "days": []any{"sat"}, "start": "00:00", "end": "00:00", "wake": "header"})
	expected["by"] = "me@example.com"
	if !reflect.DeepEqual(body, expected) {
		t.Errorf("sent %v\nwant %v", body, expected)
	}
}

func TestScheduleChangeOfEachKind(t *testing.T) {
	for name, c := range map[string]struct {
		change schedule.Change
		edit   func(m map[string]any)
	}{
		"remove": {schedule.Change{Remove: 1}, func(m map[string]any) { m["windows"] = []any{} }},
		"except": {schedule.Change{Except: &schedule.Exceptions{Dates: []string{"2026-12-25"}}}, func(m map[string]any) { m["exceptions"] = []any{"2026-12-25"} }},
		"except a window": {schedule.Change{Except: &schedule.Exceptions{Dates: []string{"2026-12-25"}, Window: 1}}, func(m map[string]any) {
			m["windows"].([]any)[0].(map[string]any)["exceptions"] = []any{"2026-12-25"}
		}},
		"edit": {schedule.Change{Fields: map[string]json.RawMessage{"maxNapDays": nil, "joinMinutes": json.RawMessage("30")}}, func(m map[string]any) {
			delete(m, "maxNapDays")
			m["joinMinutes"] = 30.0
		}},
	} {
		g, r := changeRig(t)
		r.ScheduleChange = &c.change
		if code, out := g.call(t, WorkloadsScheduleChange, r); code != 0 || !strings.HasPrefix(out, shopTarget+": schedule set\n") {
			t.Errorf("%s: exit %d:\n%s", name, code, out)
			continue
		}
		expected := keptStored()
		c.edit(expected)
		expected["by"] = "me@example.com"
		if body := g.body(t, "PUT", controllerHost, "/v1/workloads/shop/schedule"); !reflect.DeepEqual(body, expected) {
			t.Errorf("%s: sent %v\nwant %v", name, body, expected)
		}
	}
}

// A window's number out of range is exit 2, AWS's line, nothing written; a refusal is set's; the last window's says what clear does.
func TestScheduleChangeRefusals(t *testing.T) {
	g, r := changeRig(t)
	r.ScheduleChange = &schedule.Change{Remove: 2}
	mark := len(g.s.Calls())
	code, stdout, stderr := g.split(t, WorkloadsScheduleChange, r)
	if code != 2 || stdout != "" || stderr != shopTarget+": --window: 2 is not a window of the schedule, which has 1 (schedule show numbers them)\n" {
		t.Errorf("out of range: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, c := range g.controllerCalls(mark) {
		if strings.HasPrefix(c, "PUT") {
			t.Errorf("wrote %s", c)
		}
	}
	g.s.ScheduleProblem = "schedule: windows or maxNapDays required"
	r.ScheduleChange = &schedule.Change{Remove: 1}
	code, stdout, stderr = g.split(t, WorkloadsScheduleChange, r)
	if code != 1 || stdout != "" || stderr != shopTarget+": schedule: windows or maxNapDays required (that was its last window: schedule clear removes the schedule)\n" {
		t.Errorf("last window: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	r.ScheduleChange = &schedule.Change{Fields: map[string]json.RawMessage{"maxNapDays": json.RawMessage("99")}}
	g.s.ScheduleProblem = "maxNapDays: a whole number from 1 to 60"
	code, _, stderr = g.split(t, WorkloadsScheduleChange, r)
	if code != 1 || stderr != shopTarget+": maxNapDays: a whole number from 1 to 60\n" {
		t.Errorf("refused: exit %d, stderr %q", code, stderr)
	}
	// Under --json, AWS's map of no answer.
	r.JSON = true
	if code, stdout, _ := g.split(t, WorkloadsScheduleChange, r); code != 1 || compacted(stdout) != "{}" {
		t.Errorf("--json: exit %d, stdout %q", code, stdout)
	}
}

// --dry-run reads the schedule and says the PUT it would send, nothing written.
func TestScheduleChangeDryRun(t *testing.T) {
	g, r := changeRig(t)
	r.DryRun = true
	r.ScheduleChange = &schedule.Change{Fields: map[string]json.RawMessage{"maxNapDays": json.RawMessage("14")}}
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsScheduleChange, r)
	if code != 0 || !strings.HasPrefix(out, "dry run: "+shopTarget+": would PUT /v1/workloads/shop/schedule, nothing sent\n  {") || !strings.Contains(out, `"maxNapDays":14`) ||
		!strings.Contains(out, `"unknown":"kept"`) {
		t.Errorf("exit %d:\n%s", code, out)
	}
	if calls := g.controllerCalls(mark); len(calls) != 1 || calls[0] != "GET /v1/workloads/shop/schedule" {
		t.Errorf("calls %q", calls)
	}
}

func TestScheduleListReadsEachWorkloadsSchedule(t *testing.T) {
	g, r := changeRig(t)
	g.s.Workloads["api"] = "ASLEEP"
	code, out := g.call(t, WorkloadsScheduleList, r)
	want := "ROUTER                         WORKLOAD  NAME  CLOUD  STATE  ZONE          WINDOWS  NEXT\n" +
		shopRouter + "  shop      —     gcp    AWAKE  Europe/Paris  1        Mon 2 Nov 22:00 CET nap window starts\n" +
		"1 workload with a schedule\n"
	if code != 0 || out != want {
		t.Fatalf("exit %d:\n%s\nwant:\n%s", code, out, want)
	}
	r.All = true
	code, out = g.call(t, WorkloadsScheduleList, r)
	if code != 0 || !strings.Contains(out, shopRouter+"  api       —     gcp    ASLEEP  —") || !strings.HasSuffix(out, "2 workloads, 1 with a schedule\n") {
		t.Fatalf("--all: exit %d:\n%s", code, out)
	}
	r.All, r.JSON, r.States = false, true, []string{"ASLEEP"}
	code, out = g.call(t, WorkloadsScheduleList, r)
	if code != 0 || compacted(out) != `{"workloads":[]}` {
		t.Fatalf("--state ASLEEP --json: exit %d:\n%s", code, out)
	}
	r.States = nil
	code, out = g.call(t, WorkloadsScheduleList, r)
	var got struct {
		Workloads []struct {
			Router, WorkloadID, Cloud string
			Schedule                  map[string]any
		}
	}
	if err := json.Unmarshal([]byte(out), &got); code != 0 || err != nil || len(got.Workloads) != 1 || got.Workloads[0].WorkloadID != "shop" ||
		got.Workloads[0].Router != shopRouter || got.Workloads[0].Schedule["unknown"] != "kept" {
		t.Fatalf("--json: exit %d, %v:\n%s", code, err, out)
	}
}
