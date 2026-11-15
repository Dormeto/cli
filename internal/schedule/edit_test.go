package schedule

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// stored is a schedule as a router stores it, with a field no version of the CLI knows, on the schedule and on a window.
const stored = `{"timeZone":"Europe/Paris","windows":[` +
	`{"kind":"awake","days":["mon","tue"],"start":"08:30","end":"19:00","future":{"a":1}},` +
	`{"kind":"nap","start":"20:00","end":"07:00","repeat":{"pattern":"weekly","interval":2,"days":["mon"],"later":true},"from":"2027-01-04","wake":"header","exceptions":["2027-02-01"]}` +
	`],"exceptions":["2026-12-25"],"maxNapDays":7,"joinMinutes":0,"by":"me","at":"2026-10-01T00:00:00Z","unknown":"kept"}`

// sameJSON fails unless got is want once both are decoded.
func sameJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// with is stored with its windows' list replaced, or a member changed (old → new, both written as in stored).
func with(old, new string) string {
	if !strings.Contains(stored, old) {
		panic("not in stored: " + old)
	}
	return strings.Replace(stored, old, new, 1)
}

func TestAddAppendsTheWindowAndKeepsEveryOtherField(t *testing.T) {
	got, err := Change{Add: &NewWindow{Kind: "nap", Days: []string{"sat", "sun"}, Start: "00:00", End: "00:00", Wake: "header"}}.Apply(json.RawMessage(stored))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, got, with(`"exceptions":["2027-02-01"]}`, `"exceptions":["2027-02-01"]},{"kind":"nap","days":["sat","sun"],"start":"00:00","end":"00:00","wake":"header"}`))
}

func TestAddARecurrenceWritesItsRepeat(t *testing.T) {
	two, last := 2, -1
	w := NewWindow{Kind: "awake", Start: "08:00", End: "18:00", From: "2027-01-01", Until: "2027-12-31",
		Repeat: &NewRepeat{Pattern: "monthly", Interval: &two, Nth: &last, Weekday: "fri"}}
	got, err := Change{Add: &w}.Apply(json.RawMessage(`{"timeZone":"UTC","windows":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, got, `{"timeZone":"UTC","windows":[{"kind":"awake","start":"08:00","end":"18:00","from":"2027-01-01","until":"2027-12-31",`+
		`"repeat":{"pattern":"monthly","interval":2,"nth":-1,"weekday":"fri"}}]}`)
	minutes := 630
	got, _ = Change{Add: &NewWindow{Kind: "awake", Repeat: &NewRepeat{Pattern: "cron", Cron: "30 8 * * 1-5", Minutes: &minutes}}}.Apply(json.RawMessage(`{"timeZone":"UTC"}`))
	sameJSON(t, got, `{"timeZone":"UTC","windows":[{"kind":"awake","repeat":{"pattern":"cron","cron":"30 8 * * 1-5","minutes":630}}]}`)
}

func TestAddAFirstScheduleNeedsItsTimeZone(t *testing.T) {
	w := &NewWindow{Kind: "awake", Days: []string{"mon"}, Start: "08:00", End: "18:00"}
	for _, none := range []string{"", "null"} {
		if _, err := (Change{Add: w}).Apply(json.RawMessage(none)); err == nil || !strings.Contains(err.Error(), "--time-zone") {
			t.Errorf("%q: %v", none, err)
		}
		got, err := Change{Add: w, TimeZone: "Europe/Paris"}.Apply(json.RawMessage(none))
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, got, `{"timeZone":"Europe/Paris","windows":[{"kind":"awake","days":["mon"],"start":"08:00","end":"18:00"}]}`)
	}
	// A schedule without a zone takes it; another zone than its own is edit's.
	got, err := Change{Add: w, TimeZone: "UTC"}.Apply(json.RawMessage(`{"maxNapDays":3}`))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, got, `{"maxNapDays":3,"timeZone":"UTC","windows":[{"kind":"awake","days":["mon"],"start":"08:00","end":"18:00"}]}`)
	if _, err := (Change{Add: w, TimeZone: "UTC"}).Apply(json.RawMessage(stored)); err == nil || !strings.Contains(err.Error(), "schedule edit --time-zone") {
		t.Errorf("another zone: %v", err)
	}
	if _, err := (Change{Add: w, TimeZone: "Europe/Paris"}).Apply(json.RawMessage(stored)); err != nil {
		t.Errorf("its own zone: %v", err)
	}
}

func TestRemoveTakesTheWindowOfItsNumber(t *testing.T) {
	got, err := Change{Remove: 1}.Apply(json.RawMessage(stored))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, got, with(`{"kind":"awake","days":["mon","tue"],"start":"08:30","end":"19:00","future":{"a":1}},`, ""))
	got, _ = Change{Remove: 2}.Apply(json.RawMessage(stored))
	sameJSON(t, got, with(`,{"kind":"nap","start":"20:00","end":"07:00","repeat":{"pattern":"weekly","interval":2,"days":["mon"],"later":true},"from":"2027-01-04","wake":"header","exceptions":["2027-02-01"]}`, ""))
}

func TestAWindowNumberOutOfRangeIsRefused(t *testing.T) {
	for _, c := range []struct {
		change Change
		raw    string
		want   string
	}{
		{Change{Remove: 3}, stored, "--window: 3 is not a window of the schedule, which has 2 (schedule show numbers them)"},
		{Change{Remove: -1}, stored, "--window: -1 is not a window of the schedule, which has 2"},
		{Change{Remove: 1}, `{"timeZone":"UTC","maxNapDays":3}`, "--window: 1 is not a window of the schedule, which has none"},
		{Change{Except: &Exceptions{Dates: []string{"2027-01-01"}, Window: 3}}, stored, "--window: 3 is not a window of the schedule, which has 2"},
		{Change{Remove: 1}, "null", "no schedule"},
		{Change{Except: &Exceptions{Dates: []string{"2027-01-01"}}}, "", "no schedule"},
	} {
		if _, err := c.change.Apply(json.RawMessage(c.raw)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.change, err, c.want)
		}
	}
}

func TestExceptAddsAndRemovesDatesOfTheScheduleOrOfAWindow(t *testing.T) {
	got, err := Change{Except: &Exceptions{Dates: []string{"2026-12-25", "2027-01-01"}}}.Apply(json.RawMessage(stored))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, got, with(`"exceptions":["2026-12-25"]`, `"exceptions":["2026-12-25","2027-01-01"]`))
	got, _ = Change{Except: &Exceptions{Dates: []string{"2027-03-01"}, Window: 2}}.Apply(json.RawMessage(stored))
	sameJSON(t, got, with(`"exceptions":["2027-02-01"]`, `"exceptions":["2027-02-01","2027-03-01"]`))
	got, _ = Change{Except: &Exceptions{Dates: []string{"2027-03-01"}, Window: 1}}.Apply(json.RawMessage(stored))
	sameJSON(t, got, with(`"future":{"a":1}}`, `"future":{"a":1},"exceptions":["2027-03-01"]}`))
	// --remove: the dates go, and a list left empty with them.
	got, _ = Change{Except: &Exceptions{Dates: []string{"2026-12-25", "2030-01-01"}, Remove: true}}.Apply(json.RawMessage(stored))
	sameJSON(t, got, with(`"exceptions":["2026-12-25"],`, ""))
	got, _ = Change{Except: &Exceptions{Dates: []string{"2027-02-01"}, Window: 2, Remove: true}}.Apply(json.RawMessage(stored))
	sameJSON(t, got, with(`,"exceptions":["2027-02-01"]`, ""))
}

func TestEditSetsOrRemovesTheSchedulesOwnFields(t *testing.T) {
	got, err := Change{Fields: map[string]json.RawMessage{"timeZone": json.RawMessage(`"UTC"`), "joinMinutes": nil, "maxNapDays": json.RawMessage(`14`)}}.Apply(json.RawMessage(stored))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(with(`"maxNapDays":7,"joinMinutes":0`, `"maxNapDays":14`), `"timeZone":"Europe/Paris"`, `"timeZone":"UTC"`, 1)
	sameJSON(t, got, want)
	// No schedule: one made of the fields alone, as the router then judges it.
	got, _ = Change{Fields: map[string]json.RawMessage{"maxNapDays": json.RawMessage(`3`)}}.Apply(json.RawMessage("null"))
	sameJSON(t, got, `{"maxNapDays":3}`)
}

func TestWindowsCountsTheStoredWindows(t *testing.T) {
	for raw, want := range map[string]int{stored: 2, "null": 0, "": 0, `{"maxNapDays":3}`: 0, `{"windows":[{}]}`: 1} {
		if got := Windows(json.RawMessage(raw)); got != want {
			t.Errorf("%s: %d, want %d", raw, got, want)
		}
	}
}

func TestFormsOfTheFlags(t *testing.T) {
	for _, ok := range []string{"2027-01-01", "2028-02-29"} {
		if err := CheckDate("--from", ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"2027-1-1", "2027-02-30", "tomorrow", ""} {
		if err := CheckDate("--from", bad); err == nil || !strings.HasPrefix(err.Error(), "--from: ") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	for _, ok := range []string{"00:00", "23:55", "08:31"} { // a multiple of 5 is the router's to check
		if err := CheckTime("--start", ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"8:30", "24:00", "08:60", "0830"} {
		if err := CheckTime("--start", bad); err == nil || !strings.HasPrefix(err.Error(), "--start: ") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
