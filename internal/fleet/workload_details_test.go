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

// A cloud's own rows take the form of the shared ones: after the workload's rows and after its settings, aligned with them.
func TestWorkloadDetailsTextWithMoreRowsKeepsTheLayout(t *testing.T) {
	idle := 30
	d := WorkloadDetails{WorkloadID: "shop", Name: "The shop", State: "AWAKE", UpdatedAt: "2026-11-08T05:00:00Z", Cloud: "gcp", Account: "p", Region: "europe-west1",
		Pauses: []Pause{{Scope: "workload", By: "me", Reason: "audit"}}}
	d.Settings.IdleMinutes = &idle
	d.SettingsFrom = map[string]string{"idleMinutes": "router"}
	got := d.TextWith("r", time.UTC, DetailsMore{
		Rows:     [][2]string{{"URL map", "shop-map"}, {"backends", "instance web-1 (europe-west1-b)\nasg web-mig (europe-west1)"}, {"idle check", ""}},
		Settings: [][2]string{{"wake mode", "browser (fixed on Google Cloud)"}},
	})
	want := "workload  r/shop\n" +
		"name      The shop\n" +
		"state     AWAKE since 2026-11-08 05:00 UTC\n" +
		"cloud     gcp\n" +
		"project   p\n" +
		"region    europe-west1\n" +
		"URL map   shop-map\n" +
		"backends  instance web-1 (europe-west1-b)\n" +
		"          asg web-mig (europe-west1)\n" +
		"\nsettings\n" +
		"  idle window  30 min (router)\n" +
		"  wake mode    browser (fixed on Google Cloud)\n" +
		"\npauses\n" +
		"  paused by me: audit\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if d.Text("r", time.UTC) != d.TextWith("r", time.UTC, DetailsMore{}) {
		t.Error("Text is TextWith without more")
	}
}

func TestHoursIsSingularForOneAlone(t *testing.T) {
	for in, want := range map[float64]string{1: "1 hour", 1.5: "1.5 hours", 0.5: "0.5 hours", 300: "300 hours", 0: "0 hours", 1.02: "1 hour"} {
		if got := hours(in); got != want {
			t.Errorf("hours(%v) = %q, want %q", in, got, want)
		}
	}
}

// `workloads show` reads a window as `workloads schedule show` does, a recurrence in words, its dates with their year.
func TestWorkloadDetailsScheduleSaysARecurrence(t *testing.T) {
	var d WorkloadDetails
	raw := `{"workloadId":"w-1","state":"AWAKE","schedule":{"timeZone":"Europe/Paris","windows":[
		{"kind":"nap","start":"20:00","end":"07:00","repeat":{"pattern":"weekly","interval":2,"days":["mon","thu"]},"from":"2027-01-04","wake":"header"},
		{"kind":"awake","repeat":{"pattern":"cron","cron":"30 8 * * 1-5","minutes":630}},
		{"kind":"awake","start":"08:00","end":"18:00","repeat":{"pattern":"hourly"}},
		{"kind":"awake","days":["sat"],"start":"08:30","end":"19:00"}]},
		"next":[{"at":"2027-01-04T19:00:00.000Z","kind":"nap-start","window":0}]}`
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	text := d.Text("prod", time.UTC)
	for _, want := range []string{
		"  nap Every 2 weeks on Mon, Thu, 20:00–07:00 next day, from 2027-01-04, no end date, wake: header\n",
		"  awake Cron 30 8 * * 1-5, 10 h 30 min\n",
		"  awake repeat unreadable, no occurrence\n",
		"  awake sat 08:30–19:00\n",
		"Mon 4 Jan 2027 20:00 CET nap window starts",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}

func TestWorkloadDetailsScheduleSaysTheJoin(t *testing.T) {
	text := func(extra string) string {
		var d WorkloadDetails
		raw := `{"workloadId":"w-1","state":"AWAKE","schedule":{"timeZone":"Europe/Paris","windows":[{"kind":"awake","days":["sat"],"start":"08:30","end":"19:00"}],"maxNapDays":7}` + extra + `}`
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatal(err)
		}
		return d.Text("prod", time.UTC)
	}
	if got := text(`,"joinMinutes":0,"joinFrom":"workload","joinInherited":{"joinMinutes":15,"joinFrom":"default"}`); !strings.Contains(got, "  maximum nap 7 days\n  windows never join (workload)\n") {
		t.Errorf("%s", got)
	}
	if got := text(""); strings.Contains(got, "join") {
		t.Errorf("an older router: %s", got)
	}
}
