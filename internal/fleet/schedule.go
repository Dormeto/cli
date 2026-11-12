package fleet

// The schedule of a workload as `workloads schedule show` and `workloads show` print it: windows, zone, next events.

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/schedule"
)

// ScheduleView is the part of a schedule the display reads (the rest is kept as the API sent it).
type ScheduleView struct {
	TimeZone   string            `json:"timeZone"`
	Windows    []schedule.Window `json:"windows"`
	Exceptions []string          `json:"exceptions"`
	MaxNapDays int               `json:"maxNapDays"`
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

// ScheduleWindowText is a plain weekly window as WindowText writes it, any other as "nap Every 2 weeks on Mon, Thu,
// 20:00–07:00 next day, from 2027-01-04, no end date, wake: header".
func ScheduleWindowText(w schedule.Window) string {
	if w.Plain() {
		return WindowText(w.Kind, strings.Join(w.Days, ","), w.Start, w.End, w.Wake)
	}
	s := w.Kind + " " + w.Text()
	if w.Kind == "nap" {
		s += ", wake: " + orElse(w.Wake, "never")
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

// timeLayout labels every time with its zone (the abbreviation, UTC for no zone); farLayout also says the year, for events
// that may be more than a year away.
const (
	timeLayout = "Mon 2 Jan 15:04 MST"
	farLayout  = "Mon 2 Jan 2006 15:04 MST"
)

// ScheduleShown is a workload's schedule as `workloads schedule show` prints it on both clouds, from the router's answer: the
// schedule as answered (null for none, empty from a router that does not report schedules, AWS's before 0.31.0), the next
// events, the maintenance wake due ("" for none) and the join in force (router 0.36.0). NoDue is an answer that has no
// maintenance wake at all (Google Cloud's REST answer): unknown, not none.
type ScheduleShown struct {
	Schedule       json.RawMessage
	Next           []NextEvent
	MaintenanceDue string
	NoDue          bool
	schedule.Join
}

// JSON is what --json prints, {schedule, next, maintenanceDue, joinMinutes, joinFrom, joinInherited}, as `schedule set --file`
// takes it: schedule left out when not reported, next [] for none, maintenanceDue null for none and left out when unknown,
// the join's keys left out by a router before 0.36.0.
func (s ScheduleShown) JSON() any {
	var shown struct {
		Schedule       *json.RawMessage `json:"schedule,omitempty"`
		Next           []NextEvent      `json:"next"`
		MaintenanceDue json.RawMessage  `json:"maintenanceDue,omitempty"`
		schedule.Join
	}
	if len(s.Schedule) > 0 {
		shown.Schedule = &s.Schedule
	}
	shown.Next, shown.Join = s.Next, s.Join
	if shown.Next == nil {
		shown.Next = []NextEvent{}
	}
	switch {
	case s.NoDue:
	case s.MaintenanceDue == "":
		shown.MaintenanceDue = json.RawMessage("null")
	default:
		shown.MaintenanceDue, _ = json.Marshal(s.MaintenanceDue)
	}
	return shown
}

// Text is the schedule in words under its target's line: "<target>: time zone <zone>" then the windows, "<target>: schedule"
// for one without windows, "<target>: no schedule", or what a router that does not report schedules needs.
func (s ScheduleShown) Text(target string) (string, error) {
	switch {
	case len(s.Schedule) == 0:
		return target + ": the router does not report schedules (upgrade it to 0.31.0 or later)\n", nil
	case string(s.Schedule) == "null":
		return target + ": no schedule\n", nil
	}
	var v ScheduleView
	if err := json.Unmarshal(s.Schedule, &v); err != nil {
		return "", fmt.Errorf("decode the schedule: %w", err)
	}
	loc, note := ZoneOf(v.TimeZone)
	var b strings.Builder
	if len(v.Windows) > 0 {
		fmt.Fprintf(&b, "%s: time zone %s\n", target, orElse(v.TimeZone, "UTC"))
	} else {
		fmt.Fprintf(&b, "%s: schedule\n", target)
		if v.TimeZone != "" {
			fmt.Fprintf(&b, "  time zone %s\n", v.TimeZone)
		}
		if v.MaxNapDays == 0 {
			fmt.Fprintln(&b, "  no windows")
		}
	}
	v.lines(&b, note, s.Line())
	v.PrintNext(&b, loc, s.Next, s.MaintenanceDue)
	return b.String(), nil
}

// SetText is what `workloads schedule set` says of a schedule set: "<target>: schedule set", the zone's note and the next
// events.
func (s ScheduleView) SetText(target string, next []NextEvent, due string) string {
	loc, note := ZoneOf(s.TimeZone)
	var b strings.Builder
	fmt.Fprintf(&b, "%s: schedule set\n", target)
	if note != "" {
		fmt.Fprintf(&b, "  %s\n", note)
	}
	s.PrintNext(&b, loc, next, due)
	return b.String()
}

// lines writes what follows the schedule's head: the zone's note, the windows, the exceptions, the maximum nap and the join.
func (s ScheduleView) lines(w io.Writer, note, join string) {
	if note != "" {
		fmt.Fprintf(w, "  %s\n", note)
	}
	for _, win := range s.Windows {
		fmt.Fprintf(w, "  %s\n", ScheduleWindowText(win))
	}
	if len(s.Exceptions) > 0 {
		fmt.Fprintf(w, "  except %s\n", strings.Join(s.Exceptions, ", "))
	}
	if s.MaxNapDays > 0 {
		fmt.Fprintf(w, "  maximum nap %d %s\n", s.MaxNapDays, Plural(s.MaxNapDays, "day"))
	}
	if join != "" {
		fmt.Fprintf(w, "  %s\n", join)
	}
}

// PrintNext prints the schedule's next events and the maintenance wake due, in the zone. The router reads up to 400 days
// ahead for a window with a repeat or a from, so those events say their year; the others are within 8 days.
func (s ScheduleView) PrintNext(w io.Writer, loc *time.Location, next []NextEvent, due string) {
	layout := timeLayout
	if slices.ContainsFunc(s.Windows, schedule.Window.FarReach) {
		layout = farLayout
	}
	if len(next) > 0 {
		fmt.Fprintln(w, "  next")
	}
	for _, e := range next {
		at, err := time.Parse(time.RFC3339, e.At)
		if err != nil {
			fmt.Fprintf(w, "    %s %s\n", e.At, orElse(eventTexts[e.Kind], e.Kind))
			continue
		}
		fmt.Fprintf(w, "    %s %s\n", at.In(loc).Format(layout), orElse(eventTexts[e.Kind], e.Kind))
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
