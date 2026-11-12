package fleet

import (
	"testing"
	"time"
)

// A workload's pause and a router's say the same words: the end in the zone, or until resumed, keeps asleep when asked, and
// what the answer woke.
func TestPauseResultIsOneWordingForAWorkloadAndARouter(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, 12, 1, 7, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		end        time.Time
		keepAsleep bool
		woken      []string
		want       string
	}{
		{time.Time{}, false, nil, "paused until resumed"},
		{end, false, nil, "paused until 2026-12-01 08:00 CET"},
		{time.Time{}, false, []string{"w-1", "w-2"}, "paused until resumed; woken w-1, w-2"},
		{end, false, []string{"shop"}, "paused until 2026-12-01 08:00 CET; woken shop"},
		{time.Time{}, true, nil, "paused until resumed, keeps asleep"},
		{end, true, []string{"w-1"}, "paused until 2026-12-01 08:00 CET, keeps asleep; woken w-1"},
	} {
		if got := PauseResult(c.end, paris, c.keepAsleep, c.woken); got != c.want {
			t.Errorf("PauseResult(%v, %v, %v) = %q, want %q", c.end, c.keepAsleep, c.woken, got, c.want)
		}
	}
}

// An active pause, a router's or a workload's, in the local time.
func TestPauseTextSaysWhoWhenWhyAndUntil(t *testing.T) {
	p := Pause{Scope: "router", By: "ann", At: "2026-11-01T10:00:00.000Z", Reason: "freeze", Until: "2026-12-01T07:00:00Z", KeepAsleep: true}
	if got, want := PauseText(p, time.UTC), "router paused by ann on 2026-11-01 10:00 UTC: freeze, until 2026-12-01 07:00 UTC, keeps asleep"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := PauseText(Pause{By: "me", Reason: "audit"}, time.UTC), "paused by me: audit"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
