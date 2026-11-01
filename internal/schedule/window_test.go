package schedule

import (
	"encoding/json"
	"testing"
)

func window(t *testing.T, text string) Window {
	t.Helper()
	var w Window
	if err := json.Unmarshal([]byte(text), &w); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return w
}

// The dashboard's words, the dates as the JSON has them.
func TestTextSaysTheRecurrenceInWords(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"start":"08:00","end":"12:00","repeat":{"pattern":"daily"}}`, "Every day, 08:00–12:00"},
		{`{"start":"08:00","end":"12:00","repeat":{"pattern":"daily","interval":1}}`, "Every day, 08:00–12:00"},
		// Do not repeat: daily on one day.
		{`{"start":"08:00","end":"12:00","repeat":{"pattern":"daily"},"from":"2027-01-04","until":"2027-01-04"}`, "Once on 2027-01-04, 08:00–12:00"},
		{`{"start":"20:00","end":"07:00","repeat":{"pattern":"daily","interval":1},"from":"2027-01-04","until":"2027-01-04"}`, "Once on 2027-01-04, 20:00–07:00 next day"},
		{`{"start":"08:00","end":"12:00","repeat":{"pattern":"daily","interval":2},"from":"2027-01-04","until":"2027-01-04"}`, "Every 2 days, 08:00–12:00, from 2027-01-04, until 2027-01-04"},
		{`{"start":"08:00","end":"12:00","repeat":{"pattern":"daily"},"from":"2027-01-04","until":"2027-01-05"}`, "Every day, 08:00–12:00, from 2027-01-04, until 2027-01-05"},
		{`{"start":"08:00","end":"12:00","repeat":{"pattern":"daily","interval":3},"from":"2027-01-04"}`, "Every 3 days, 08:00–12:00, from 2027-01-04, no end date"},
		{`{"start":"20:00","end":"07:00","repeat":{"pattern":"weekly","interval":2,"days":["mon","thu"]},"from":"2027-01-04"}`, "Every 2 weeks on Mon, Thu, 20:00–07:00 next day, from 2027-01-04, no end date"},
		{`{"start":"20:00","end":"07:00","repeat":{"pattern":"weekly","days":["thu","mon"]},"from":"2027-01-04","until":"2027-06-30"}`, "Weekly on Mon, Thu, 20:00–07:00 next day, from 2027-01-04, until 2027-06-30"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"weekly","days":["mon","tue","wed","thu","fri"]}}`, "Weekly on Mon–Fri, 08:00–18:00"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"weekly","days":["mon","tue","wed","thu","fri","sat","sun"]}}`, "Every day, 08:00–18:00"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"weekly","interval":2,"days":["mon","tue","wed","thu","fri","sat","sun"]},"from":"2027-01-04"}`, "Every 2 weeks, every day, 08:00–18:00, from 2027-01-04, no end date"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"monthly","interval":1,"day":15}}`, "Monthly on day 15, 08:00–18:00"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"monthly","day":15,"nth":null,"weekday":null}}`, "Monthly on day 15, 08:00–18:00"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"monthly","interval":2,"day":31},"from":"2027-01-01"}`, "Every 2 months on day 31, 08:00–18:00, from 2027-01-01, no end date"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"monthly","interval":3,"nth":1,"weekday":"mon"},"from":"2027-01-01"}`, "Every 3 months on the first Mon, 08:00–18:00, from 2027-01-01, no end date"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"monthly","nth":2,"weekday":"tue"}}`, "Monthly on the second Tue, 08:00–18:00"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"monthly","nth":3,"weekday":"wed"}}`, "Monthly on the third Wed, 08:00–18:00"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"monthly","nth":4,"weekday":"sat"}}`, "Monthly on the fourth Sat, 08:00–18:00"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"monthly","nth":-1,"weekday":"fri"}}`, "Monthly on the last Fri, 08:00–18:00"},
		{`{"start":"00:00","end":"00:00","repeat":{"pattern":"monthly","interval":12,"day":25},"from":"2026-12-01"}`, "Yearly on 25 Dec, all day (24 hours), from 2026-12-01, no end date"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"monthly","interval":12,"nth":-1,"weekday":"sun"},"from":"2027-03-01"}`, "Yearly on the last Sun of Mar, 08:00–18:00, from 2027-03-01, no end date"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"monthly","interval":24,"day":1},"from":"2027-03-01"}`, "Every 24 months on day 1, 08:00–18:00, from 2027-03-01, no end date"},
		{`{"repeat":{"pattern":"cron","cron":"30 8 * * 1-5","minutes":630}}`, "Cron 30 8 * * 1-5, 10 h 30 min"},
		{`{"repeat":{"pattern":"cron","cron":"0 8 * * *","minutes":60}}`, "Cron 0 8 * * *, 1 h"},
		{`{"repeat":{"pattern":"cron","cron":"0 8 * * *","minutes":45}}`, "Cron 0 8 * * *, 45 min"},
		{`{"repeat":{"pattern":"cron","cron":"0 8 * * *","minutes":1445}}`, "Cron 0 8 * * *, 1 day 5 min"},
		{`{"repeat":{"pattern":"cron","cron":"0 8 * * 1","minutes":10080},"until":"2027-06-30"}`, "Cron 0 8 * * 1, 7 days, until 2027-06-30"},
		{`{"start":"08:00","end":"08:00","repeat":{"pattern":"daily"}}`, "Every day, 08:00–08:00 next day"},
		// A plain weekly window that gained a range reads as the weekly pattern it is.
		{`{"days":["mon","fri"],"start":"08:30","end":"19:00","from":"2027-01-04"}`, "Weekly on Mon, Fri, 08:30–19:00, from 2027-01-04, no end date"},
		{`{"days":["mon","fri"],"start":"08:30","end":"19:00","until":"2027-06-30"}`, "Weekly on Mon, Fri, 08:30–19:00, until 2027-06-30"},
		// As the dashboard's daysText: a pair alone stays a list, a pair among others is a range.
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"weekly","days":["sat","sun"]}}`, "Weekly on Sat, Sun, 08:00–18:00"},
		{`{"start":"08:00","end":"18:00","repeat":{"pattern":"weekly","days":["mon","tue","thu","fri","sat"]}}`, "Weekly on Mon–Tue, Thu–Sat, 08:00–18:00"},
	} {
		if got := window(t, c.in).Text(); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// A stored window with an unreadable pattern has no occurrence and raises no error (rulings of the design), so reading it
// neither fails nor hides the rest of the schedule.
func TestAnUnreadableRepeatIsSaidAndDecodes(t *testing.T) {
	for _, in := range []string{
		`{"repeat":"weekly"}`, `{"repeat":["weekly"]}`, `{"repeat":5}`, `{"repeat":{}}`,
		`{"start":"08:00","end":"18:00","repeat":{"pattern":"hourly"}}`,
		`{"start":"08:00","end":"18:00","repeat":{"pattern":"daily","interval":"3"}}`,
		`{"start":"08:00","end":"18:00","repeat":{"pattern":"weekly"}}`,
		`{"start":"08:00","end":"18:00","repeat":{"pattern":"monthly"}}`,
		`{"start":"08:00","end":"18:00","repeat":{"pattern":"monthly","nth":7,"weekday":"mon"}}`,
		`{"start":"08:00","end":"18:00","repeat":{"pattern":"monthly","nth":1,"weekday":"someday"}}`,
		`{"repeat":{"pattern":"cron"}}`,
		`{"repeat":{"pattern":"cron","cron":5,"minutes":60}}`,
	} {
		w := window(t, in)
		if w.Plain() {
			t.Errorf("%s reads as plain", in)
		}
		if got := w.Text(); got != "repeat unreadable, no occurrence" {
			t.Errorf("%s: %q", in, got)
		}
	}
	// A cron the CLI does not read itself: the router says what it makes of it.
	if got := window(t, `{"repeat":{"pattern":"cron","cron":"not a cron"}}`).Text(); got != "Cron not a cron" {
		t.Errorf("%q", got)
	}
}

func TestPlainIsAWindowThatSaysNothingNew(t *testing.T) {
	for in, want := range map[string]bool{
		`{"days":["mon"],"start":"08:30","end":"19:00"}`:                                                    true,
		`{"days":["mon"],"start":"08:30","end":"19:00","repeat":null,"from":null,"until":null}`:             true,
		`{"days":["mon"],"start":"08:30","end":"19:00","from":"2027-01-04"}`:                                false,
		`{"days":["mon"],"start":"08:30","end":"19:00","until":"2027-01-04"}`:                               false,
		`{"days":null,"start":"08:30","end":"19:00","repeat":{"pattern":"daily"}}`:                          false,
		`{"days":null,"repeat":{"pattern":"cron","cron":"0 8 * * *","minutes":60},"start":null,"end":null}`: false,
	} {
		if got := window(t, in).Plain(); got != want {
			t.Errorf("%s: %v", in, got)
		}
	}
}

func TestFarReachIsTheRoutersLongHorizon(t *testing.T) {
	for in, want := range map[string]bool{
		`{"days":["mon"],"start":"08:30","end":"19:00"}`:                      false,
		`{"days":["mon"],"start":"08:30","end":"19:00","until":"2027-01-04"}`: false, // until alone keeps the 8 days
		`{"days":["mon"],"start":"08:30","end":"19:00","from":"2027-01-04"}`:  true,
		`{"start":"08:30","end":"19:00","repeat":{"pattern":"daily"}}`:        true,
		`{"days":["mon"],"start":"08:30","end":"19:00","repeat":null}`:        false,
	} {
		if got := window(t, in).FarReach(); got != want {
			t.Errorf("%s: %v", in, got)
		}
	}
}

func TestRefusedAsUnknownIsARouterBefore036(t *testing.T) {
	for message, want := range map[string]bool{
		"windows[0].repeat: unknown field":                                                true,
		"windows[0].days: one or more of mon; windows[12].repeat: unknown field":          true,
		"windows[1].from: unknown field":                                                  true,
		"windows[1].until: unknown field; windows[1].from: unknown field":                 true,
		"windows[0].repeat.frequency: unknown field":                                      false, // this router, a field of repeat it does not know
		"windows[0].foo: unknown field":                                                   false,
		"windows[0].from: a date YYYY-MM-DD":                                              false,
		"windows[0].repeat.cron: five fields: minute hour day-of-month month day-of-week": false,
		"repeat: unknown field":                                                           false,
		"":                                                                                false,
	} {
		if got := RefusedAsUnknown(message); got != want {
			t.Errorf("%q: %v", message, got)
		}
	}
}
