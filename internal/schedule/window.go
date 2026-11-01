// Package schedule reads a window of a workload's schedule (the router's lambda/src/lib/schedule.mjs), for `dormeto aws
// workloads schedule` and `dormeto gcp workloads schedule`: a recurrence in the dashboard's words. The router checks a
// schedule; nothing here does.
package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// RecurrenceRouter is the first router that takes repeat, from, until and joinMinutes.
const RecurrenceRouter = "0.36.0"

// Window is a window as the router stores it. Repeat stays raw until it is read: a stored one that does not read has no
// occurrence and raises no error, so it cannot fail the decoding of the schedule around it. null on a field is its absence.
type Window struct {
	Kind   string          `json:"kind"`
	Days   []string        `json:"days"`
	Start  string          `json:"start"`
	End    string          `json:"end"`
	Repeat json.RawMessage `json:"repeat"`
	From   string          `json:"from"`
	Until  string          `json:"until"`
	Wake   string          `json:"wake"`
}

type repeat struct {
	Pattern  string   `json:"pattern"`
	Interval int      `json:"interval"`
	Days     []string `json:"days"`
	Day      int      `json:"day"`
	Nth      int      `json:"nth"`
	Weekday  string   `json:"weekday"`
	Cron     string   `json:"cron"`
	Minutes  int      `json:"minutes"`
}

func (w Window) hasRepeat() bool { return len(w.Repeat) > 0 && string(w.Repeat) != "null" }

// Plain is a window with only weekly days and times, as there were before recurrences.
func (w Window) Plain() bool { return !w.hasRepeat() && w.From == "" && w.Until == "" }

// FarReach is a window that takes the router's next events as far as 400 days: a repeat or a from; an until alone does not.
func (w Window) FarReach() bool { return w.hasRepeat() || w.From != "" }

var (
	weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	labels   = map[string]string{"mon": "Mon", "tue": "Tue", "wed": "Wed", "thu": "Thu", "fri": "Fri", "sat": "Sat", "sun": "Sun"}
	nths     = map[int]string{1: "first", 2: "second", 3: "third", 4: "fourth", -1: "last"}
)

// Text is the window's recurrence in words, without its kind and wake mode: "Every 2 weeks on Mon, Thu, 20:00–07:00 next
// day, from 2027-01-04, no end date", "Monthly on day 15, 08:00–18:00", "Cron 30 8 * * 1-5, 10 h 30 min".
func (w Window) Text() string {
	r := repeat{Pattern: "weekly", Days: w.Days}
	if w.hasRepeat() {
		r = repeat{}
		if json.Unmarshal(w.Repeat, &r) != nil {
			return unreadable
		}
	}
	if r.Pattern == "daily" && r.Interval <= 1 && w.From != "" && w.From == w.Until { // the dashboard's Do not repeat
		return "Once on " + w.From + ", " + w.hours()
	}
	how := r.text(w.From)
	if how == "" {
		return unreadable
	}
	if r.Pattern != "cron" {
		how += ", " + w.hours()
	}
	return how + w.dates()
}

const unreadable = "repeat unreadable, no occurrence"

// text is the pattern in words, "" when it does not read.
func (r repeat) text(from string) string {
	switch r.Pattern {
	case "daily":
		return r.every("Every day", "day")
	case "weekly":
		if days := daysText(r.Days); days == "every day" {
			if r.Interval <= 1 {
				return "Every day"
			}
			return r.every("", "week") + ", every day"
		} else if days != "" {
			return r.every("Weekly", "week") + " on " + days
		}
	case "monthly":
		every := r.every("Monthly", "month")
		if r.Interval == 12 { // yearly is not a pattern of its own: from gives its month
			every = "Yearly"
		}
		month := ""
		if t, err := time.Parse(time.DateOnly, from); err == nil && r.Interval == 12 {
			month = t.Format("Jan")
		}
		if r.Day > 0 && month != "" {
			return fmt.Sprintf("%s on %d %s", every, r.Day, month)
		}
		if r.Day > 0 {
			return fmt.Sprintf("%s on day %d", every, r.Day)
		}
		if nth, day := nths[r.Nth], labels[r.Weekday]; nth != "" && day != "" && month != "" {
			return fmt.Sprintf("%s on the %s %s of %s", every, nth, day, month)
		} else if nth != "" && day != "" {
			return fmt.Sprintf("%s on the %s %s", every, nth, day)
		}
	case "cron":
		if r.Cron != "" {
			return "Cron " + r.Cron + durationText(r.Minutes)
		}
	}
	return ""
}

func (r repeat) every(one, unit string) string {
	if r.Interval > 1 {
		return fmt.Sprintf("Every %d %ss", r.Interval, unit)
	}
	return one
}

// daysText is the dashboard's: runs of three or more days are ranges, and two when there are other runs, so that "Sat, Sun"
// alone stays plain.
func daysText(days []string) string {
	var runs [][]string
	for i, d := range weekdays {
		if !slices.Contains(days, d) {
			continue
		}
		if i > 0 && slices.Contains(days, weekdays[i-1]) {
			runs[len(runs)-1] = append(runs[len(runs)-1], d)
		} else {
			runs = append(runs, []string{d})
		}
	}
	if len(runs) == 1 && len(runs[0]) == len(weekdays) {
		return "every day"
	}
	var parts []string
	for _, run := range runs {
		if len(run) >= 3 || (len(run) == 2 && len(runs) > 1) {
			parts = append(parts, labels[run[0]]+"–"+labels[run[len(run)-1]])
			continue
		}
		for _, d := range run {
			parts = append(parts, labels[d])
		}
	}
	return strings.Join(parts, ", ")
}

// hours is the dashboard's, but for an end equal to a start other than midnight: those 24 hours say where they begin.
func (w Window) hours() string {
	if w.Start == w.End && w.Start == "00:00" {
		return "all day (24 hours)"
	}
	if w.End <= w.Start { // HH:MM compares as text
		return w.Start + "–" + w.End + " next day"
	}
	return w.Start + "–" + w.End
}

// durationText is ", 10 h 30 min", ", 1 h" or ", 45 min", "" without a duration.
func durationText(minutes int) string {
	if minutes <= 0 {
		return ""
	}
	var parts []string
	if days := minutes / 1440; days == 1 {
		parts = append(parts, "1 day")
	} else if days > 1 {
		parts = append(parts, fmt.Sprintf("%d days", days))
	}
	if hours := minutes % 1440 / 60; hours > 0 {
		parts = append(parts, fmt.Sprintf("%d h", hours))
	}
	if rest := minutes % 60; rest > 0 {
		parts = append(parts, fmt.Sprintf("%d min", rest))
	}
	return ", " + strings.Join(parts, " ")
}

// dates is the range: both days included, nothing said when there is none.
func (w Window) dates() string {
	switch {
	case w.From != "" && w.Until != "":
		return ", from " + w.From + ", until " + w.Until
	case w.From != "":
		return ", from " + w.From + ", no end date"
	case w.Until != "":
		return ", until " + w.Until
	}
	return ""
}

// unknownNewField is an older router's refusal: of a window's field or joinMinutes on a schedule, and of a setting in its
// own words.
var unknownNewField = regexp.MustCompile(`(^|; )((windows\[\d+\]\.(repeat|from|until)|joinMinutes): unknown field|settings: unknown setting joinMinutes)`)

// RefusedAsUnknown says a router's refusal is a router older than RecurrenceRouter's: it names repeat, from, until or
// joinMinutes as unknown. A router that has them only says that of a field inside repeat.
func RefusedAsUnknown(message string) bool { return unknownNewField.MatchString(message) }

// JoinDefault is the largest gap that joins two windows when no level sets it.
const JoinDefault = 15

// Join is what a router's status and schedule answers add beside the schedule: the value in force, its level and what
// removing the workload's own would give. Raw, so it goes through --json as answered, 0 included, and an older router's
// answer adds nothing.
type Join struct {
	JoinMinutes   json.RawMessage `json:"joinMinutes,omitempty"`
	JoinFrom      json.RawMessage `json:"joinFrom,omitempty"`
	JoinInherited json.RawMessage `json:"joinInherited,omitempty"`
}

// Line is "join windows up to 15 min apart (organization)" or "windows never join (workload)", "" without a value.
func (j Join) Line() string {
	var minutes *int
	if json.Unmarshal(j.JoinMinutes, &minutes) != nil || minutes == nil {
		return ""
	}
	var from string
	if json.Unmarshal(j.JoinFrom, &from) != nil || from == "" {
		return JoinText(*minutes)
	}
	return JoinText(*minutes) + " (" + from + ")"
}

// JoinText is "join windows up to 15 min apart", or "windows never join" for 0.
func JoinText(minutes int) string {
	if minutes == 0 {
		return "windows never join"
	}
	return "join windows " + JoinGap(minutes)
}

// JoinGap is "up to 15 min apart", or "never" for 0.
func JoinGap(minutes int) string {
	if minutes == 0 {
		return "never"
	}
	return fmt.Sprintf("up to %d min apart", minutes)
}

// ParseJoinMinutes reads a --join-minutes value: a whole number from 0 to 120, a multiple of 5, or inherit (nil).
func ParseJoinMinutes(s string) (*int, error) {
	if s == "inherit" {
		return nil, nil
	}
	if n, err := strconv.ParseUint(s, 10, 8); err == nil && n <= 120 && n%5 == 0 {
		minutes := int(n)
		return &minutes, nil
	}
	return nil, errors.New("a whole number from 0 to 120, a multiple of 5, or inherit")
}
