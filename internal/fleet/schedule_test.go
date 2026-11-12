package fleet

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/schedule"
)

const parisSchedule = `{"timeZone":"Europe/Paris","windows":[{"kind":"nap","days":["mon"],"start":"22:00","end":"06:00","wake":"header"}],"exceptions":["2026-12-25"],"maxNapDays":14,"by":"me","at":"2026-11-01T10:00:00.000Z"}`

var parisNext = []NextEvent{{At: "2026-11-02T21:00:00.000Z", Kind: "nap-start"}, {At: "2026-11-03T05:00:00.000Z", Kind: "nap-end"}}

// `schedule show --json` is AWS's shape on both clouds: the schedule as answered, next ([] for none), maintenanceDue (null for
// none, left out when the router's answer has none), and the join's keys when the router answers them.
func TestScheduleShownJSONIsAWSShape(t *testing.T) {
	join := `{"joinMinutes":0,"joinFrom":"workload","joinInherited":{"joinMinutes":15,"joinFrom":"default"}}`
	var j schedule.Join
	if err := json.Unmarshal([]byte(join), &j); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		s    ScheduleShown
		want string
	}{
		{"none due", ScheduleShown{Schedule: json.RawMessage(`{"timeZone":"UTC"}`)}, `{"schedule":{"timeZone":"UTC"},"next":[],"maintenanceDue":null}`},
		{"due", ScheduleShown{Schedule: json.RawMessage(`{"timeZone":"UTC"}`), Next: parisNext[:1], MaintenanceDue: "2026-11-09T07:00:00.000Z"},
			`{"schedule":{"timeZone":"UTC"},"next":[{"at":"2026-11-02T21:00:00.000Z","kind":"nap-start","window":0}],"maintenanceDue":"2026-11-09T07:00:00.000Z"}`},
		{"not reported", ScheduleShown{}, `{"next":[],"maintenanceDue":null}`},
		{"no schedule", ScheduleShown{Schedule: json.RawMessage("null")}, `{"schedule":null,"next":[],"maintenanceDue":null}`},
		{"no due in the answer", ScheduleShown{Schedule: json.RawMessage("null"), NoDue: true}, `{"schedule":null,"next":[]}`},
		{"join", ScheduleShown{Schedule: json.RawMessage("null"), NoDue: true, Join: j},
			`{"schedule":null,"next":[],"joinMinutes":0,"joinFrom":"workload","joinInherited":{"joinMinutes":15,"joinFrom":"default"}}`},
	} {
		b, err := json.Marshal(c.s.JSON())
		if err != nil || string(b) != c.want {
			t.Errorf("%s: %v %s, want %s", c.name, err, b, c.want)
		}
	}
}

// The text is AWS's: the target's line, the windows in words, except, maximum nap, the join and the next events in the zone.
func TestScheduleShownTextIsAWSWords(t *testing.T) {
	var join schedule.Join
	_ = json.Unmarshal([]byte(`{"joinMinutes":15,"joinFrom":"router"}`), &join)
	got, err := ScheduleShown{Schedule: json.RawMessage(parisSchedule), Next: parisNext, MaintenanceDue: "2026-11-16T05:00:00Z", Join: join}.Text("r/shop")
	want := "r/shop: time zone Europe/Paris\n" +
		"  nap mon 22:00–06:00 (next day), wake: header\n" +
		"  except 2026-12-25\n" +
		"  maximum nap 14 days\n" +
		"  join windows up to 15 min apart (router)\n" +
		"  next\n" +
		"    Mon 2 Nov 22:00 CET nap window starts\n" +
		"    Tue 3 Nov 06:00 CET nap window ends\n" +
		"  maintenance wake due Mon 16 Nov 06:00 CET\n"
	if err != nil || got != want {
		t.Errorf("%v:\n%s\nwant:\n%s", err, got, want)
	}
	for raw, want := range map[string]string{
		"":                         "r/shop: the router does not report schedules (upgrade it to 0.31.0 or later)\n",
		"null":                     "r/shop: no schedule\n",
		`{"timeZone":"Mars/Base"}`: "r/shop: schedule\n  time zone Mars/Base\n  no windows\n  unknown time zone \"Mars/Base\"; times in UTC\n",
	} {
		if got, err := (ScheduleShown{Schedule: json.RawMessage(raw)}).Text("r/shop"); err != nil || got != want {
			t.Errorf("%q: %v %q, want %q", raw, err, got, want)
		}
	}
	if _, err := (ScheduleShown{Schedule: json.RawMessage(`[1]`)}).Text("r/shop"); err == nil || !strings.Contains(err.Error(), "decode the schedule") {
		t.Errorf("not a schedule: %v", err)
	}
}

// A schedule set says its target, the zone's note and the next events, as AWS's.
func TestScheduleSetTextSaysTheNextEvents(t *testing.T) {
	var s ScheduleView
	_ = json.Unmarshal([]byte(parisSchedule), &s)
	want := "r/shop: schedule set\n  next\n    Mon 2 Nov 22:00 CET nap window starts\n    Tue 3 Nov 06:00 CET nap window ends\n"
	if got := s.SetText("r/shop", parisNext, ""); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got, want := (ScheduleView{TimeZone: "Mars/Base"}).SetText("r/shop", nil, ""), "r/shop: schedule set\n  unknown time zone \"Mars/Base\"; times in UTC\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
