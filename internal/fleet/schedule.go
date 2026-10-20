package fleet

// The weekly schedule of a workload as `workloads schedule show` and `workloads show` print it: windows, zone, next events.

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// ScheduleView is the part of a schedule the display reads (the rest is kept as the API sent it).
type ScheduleView struct {
	TimeZone string `json:"timeZone"`
	Windows  []struct {
		Kind  string   `json:"kind"`
		Days  []string `json:"days"`
		Start string   `json:"start"`
		End   string   `json:"end"`
		Wake  string   `json:"wake"`
	} `json:"windows"`
	Exceptions []string `json:"exceptions"`
	MaxNapDays int      `json:"maxNapDays"`
}

type NextEvent struct {
	At     string `json:"at"`
	Kind   string `json:"kind"`
	Window int    `json:"window"`
}

var weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// DaysText writes days in week order, three or more in a row as a range: mon–fri, sat,sun.
func DaysText(days []string) string {
	set := map[string]bool{}
	for _, d := range days {
		set[strings.ToLower(d)] = true
	}
	var parts []string
	for i := 0; i < len(weekdays); {
		if !set[weekdays[i]] {
			i++
			continue
		}
		j := i
		for j+1 < len(weekdays) && set[weekdays[j+1]] {
			j++
		}
		if j-i >= 2 {
			parts = append(parts, weekdays[i]+"–"+weekdays[j])
		} else {
			parts = append(parts, weekdays[i:j+1]...)
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// WindowText is "awake mon–fri 08:30–19:00" or "nap mon–fri 20:00–07:00 (next day), wake: header".
func WindowText(kind, days, start, end, wake string) string {
	s := fmt.Sprintf("%s %s %s–%s", kind, DaysText(strings.Split(days, ",")), start, end)
	if end <= start { // HH:MM compares as text
		s += " (next day)"
	}
	if kind == "nap" {
		s += ", wake: " + orElse(wake, "never")
	}
	return s
}

var eventTexts = map[string]string{
	"awake-start": "awake window starts", "awake-end": "awake window ends",
	"nap-start": "nap window starts", "nap-end": "nap window ends",
}

// ZoneOf is the schedule's zone for display; when it cannot be loaded, UTC and the sentence that says so.
func ZoneOf(name string) (*time.Location, string) {
	if name == "" {
		return time.UTC, ""
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC, fmt.Sprintf("unknown time zone %q; times in UTC", name)
	}
	return loc, ""
}

// timeLayout labels every time with its zone (the abbreviation, UTC for no zone).
const timeLayout = "Mon 2 Jan 15:04 MST"

// PrintNext prints the next events and the maintenance wake due, in the zone.
func PrintNext(w io.Writer, loc *time.Location, next []NextEvent, due string) {
	if len(next) > 0 {
		fmt.Fprintln(w, "  next")
	}
	for _, e := range next {
		at, err := time.Parse(time.RFC3339, e.At)
		if err != nil {
			fmt.Fprintf(w, "    %s %s\n", e.At, orElse(eventTexts[e.Kind], e.Kind))
			continue
		}
		fmt.Fprintf(w, "    %s %s\n", at.In(loc).Format(timeLayout), orElse(eventTexts[e.Kind], e.Kind))
	}
	if due != "" {
		if at, err := time.Parse(time.RFC3339, due); err == nil {
			fmt.Fprintf(w, "  maintenance wake due %s\n", at.In(loc).Format(timeLayout))
		}
	}
}

// orElse is s, or d when s is empty.
func orElse(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
