package fleet

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSecondsUSDAndShares(t *testing.T) {
	for in, want := range map[int]string{0: "0 s", 59: "59 s", 60: "1 min", 185: "3 min 5 s", 3600: "1 h", 7800: "2 h 10 min"} {
		if got := Seconds(in); got != want {
			t.Errorf("Seconds(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{0: "$0.00", 1234.5: "$1,234.50", 1234567.891: "$1,234,567.89", 12: "$12.00"} {
		if got := USD(in); got != want {
			t.Errorf("USD(%v) = %q, want %q", in, got, want)
		}
	}
	if got := StatesShare(map[string]int{"UNKNOWN": 10, "AWAKE": 30, "ASLEEP": 60, "ODD": 0}); got != "ASLEEP 60%, AWAKE 30%, UNKNOWN 10%" {
		t.Errorf("StatesShare = %q", got)
	}
	if got := StatesShare(nil); got != "" {
		t.Errorf("StatesShare(nil) = %q", got)
	}
}

func TestWorkloadDetailsTextOfAFailedWorkload(t *testing.T) {
	var d WorkloadDetails
	if err := json.Unmarshal([]byte(`{"workloadId":"w-1","state":"FAILED","failure":{"kind":"quota","step":"wake","message":"vCPU quota reached"},"schedule":null}`), &d); err != nil {
		t.Fatal(err)
	}
	got := d.Text("prod", time.UTC)
	for _, want := range []string{"prod/w-1", "wake failed (quota): vCPU quota reached", "schedule\n  none"} {
		if !strings.Contains(got, want) {
			t.Errorf("text lacks %q:\n%s", want, got)
		}
	}
}

func TestHistoryCauseText(t *testing.T) {
	from := "ASLEEP"
	for _, c := range []struct {
		e    HistoryEntry
		want string
	}{
		{HistoryEntry{To: "ASLEEP"}, "first seen by the manager"},
		{HistoryEntry{From: &from, To: "AWAKE", Inferred: true}, "seen at a check (its event was lost)"},
		{HistoryEntry{From: &from, To: "WAKING", Cause: "manager", By: "ann"}, "woken from the manager by ann"},
		{HistoryEntry{From: &from, To: "NAPPING", Cause: "manager"}, "napped from the manager"},
		{HistoryEntry{From: &from, To: "WAKING", Cause: "pause", By: "dormeto"}, "woken for a pause"},
		{HistoryEntry{From: &from, To: "WAKING", Cause: "rule health"}, "the wake rule health"},
		{HistoryEntry{From: &from, To: "WAKING", Cause: "new-cause"}, "new-cause"},
		{HistoryEntry{From: &from, To: "WAKING"}, ""},
	} {
		if got := causeText(c.e); got != c.want {
			t.Errorf("causeText(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}

func TestHoursIsSingularForOneAlone(t *testing.T) {
	for in, want := range map[float64]string{1: "1 hour", 1.5: "1.5 hours", 0.5: "0.5 hours", 300: "300 hours", 0: "0 hours", 1.02: "1 hour"} {
		if got := hours(in); got != want {
			t.Errorf("hours(%v) = %q, want %q", in, got, want)
		}
	}
}
