package fleet

// What the manager says of one workload (GET /iam/workloads/{router}/{id}, …/history, …/idle-window) and the text
// `dormeto workloads show|history|idle-window` print, as the dashboard's workload view and its History and Idle window
// cards show it. The commands print the answers as the API sent them under --json.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// WorkloadDetails is the answer of GET /iam/workloads/{router}/{id}: the router's `status` action spread with the
// manager's fields; what the CLI prints of it (the rest is in --json).
type WorkloadDetails struct {
	WorkloadID string            `json:"workloadId"`
	Name       string            `json:"name"`
	State      string            `json:"state"`
	UpdatedAt  string            `json:"updatedAt"`
	URL        string            `json:"url"`
	Account    string            `json:"account"`
	Region     string            `json:"region"`
	Tags       map[string]string `json:"tags"`
	Router     struct {
		Version string `json:"version"`
		Build   string `json:"build"`
	} `json:"router"`
	Failure *struct {
		Kind    string `json:"kind"`
		Step    string `json:"step"`
		Message string `json:"message"`
	} `json:"failure"`
	Settings struct {
		IdleMinutes        *int `json:"idleMinutes"`
		WaitSeconds        *int `json:"waitSeconds"`
		DrainSeconds       *int `json:"drainSeconds"`
		WakeTimeoutSeconds *int `json:"wakeTimeoutSeconds"`
		MaxHolders         *int `json:"maxHolders"`
	} `json:"settings"`
	Pauses         []Pause         `json:"pauses"`
	Schedule       json.RawMessage `json:"schedule"`
	Next           []NextEvent     `json:"next"`
	MaintenanceDue string          `json:"maintenanceDue"`
	States7d       *struct {
		Totals map[string]int `json:"totals"`
	} `json:"states7d"`
	IdleHours7d *float64 `json:"idleHours7d"`
	Requests7d  *int     `json:"requests7d"`
	Costs       *struct {
		SavedShare *float64 `json:"savedShare"`
		Items      []struct {
			Hourly *float64 `json:"hourly"`
		} `json:"items"`
		Saved struct {
			USD           float64 `json:"usd"`
			Hours         float64 `json:"hours"`
			UnpricedHours float64 `json:"unpricedHours"`
		} `json:"saved"`
		Discounts *struct {
			Compute   float64 `json:"compute"`
			Databases float64 `json:"databases"`
		} `json:"discounts"`
	} `json:"costs"`
	Energy *struct {
		KWh       float64 `json:"kwh"`
		CO2Kg     float64 `json:"co2Kg"`
		Estimated int     `json:"estimated"`
	} `json:"energy"`
}

// monthHours is the hours of a month the dashboard prices a saving by.
const monthHours = 730

// Text is the workload as labelled lines (times in loc), then its settings, pauses, schedule, last 7 days and costs.
func (d WorkloadDetails) Text(router string, loc *time.Location) string {
	state := d.State
	if d.UpdatedAt != "" {
		state += " since " + When(d.UpdatedAt, loc)
	}
	var failure string
	if f := d.Failure; f != nil && d.State == "FAILED" {
		failure = fmt.Sprintf("%s failed (%s): %s", orElse(f.Step, "wake"), orElse(f.Kind, "unknown"), f.Message)
	}
	version := d.Router.Version
	if version != "" && d.Router.Build != "" {
		version += " (build " + d.Router.Build + ")"
	}
	var tags []string
	for _, k := range sortedKeys(d.Tags) {
		tags = append(tags, k+"="+d.Tags[k])
	}
	var b strings.Builder
	b.WriteString(Labelled([][2]string{
		{"workload", router + "/" + d.WorkloadID}, {"name", d.Name}, {"state", state}, {"failure", failure}, {"url", d.URL},
		{"account", d.Account}, {"region", d.Region}, {"router version", version}, {"tags", strings.Join(tags, ", ")},
	}))
	section := func(title, text string) {
		if text != "" {
			b.WriteString("\n" + title + "\n" + text)
		}
	}
	section("settings", d.settingsText())
	var pauses []string
	for _, p := range d.Pauses {
		pauses = append(pauses, "  "+pauseText(p, loc)+"\n")
	}
	section("pauses", strings.Join(pauses, ""))
	section("schedule", d.scheduleText())
	section("last 7 days", d.weekText())
	section("costs", d.costsText())
	return b.String()
}

func (d WorkloadDetails) settingsText() string {
	s := d.Settings
	n := func(v *int, format func(int) string) string {
		if v == nil {
			return ""
		}
		return format(*v)
	}
	return block(Labelled([][2]string{
		{"idle window", n(s.IdleMinutes, func(v int) string { return fmt.Sprintf("%d min", v) })},
		{"hold", n(s.WaitSeconds, func(v int) string { return Seconds(v) })},
		{"drain", n(s.DrainSeconds, func(v int) string { return Seconds(v) })},
		{"wake timeout", n(s.WakeTimeoutSeconds, func(v int) string { return Seconds(v) })},
		{"held requests", n(s.MaxHolders, func(v int) string { return fmt.Sprintf("≤ %d", v) })},
	}))
}

// scheduleText is the schedule as `workloads schedule show` prints it, without the target's line; "" when the router
// does not report one.
func (d WorkloadDetails) scheduleText() string {
	if len(d.Schedule) == 0 {
		return ""
	}
	if string(d.Schedule) == "null" {
		return "  none\n"
	}
	var s ScheduleView
	if json.Unmarshal(d.Schedule, &s) != nil {
		return ""
	}
	loc, note := ZoneOf(s.TimeZone)
	var b strings.Builder
	if s.TimeZone != "" {
		fmt.Fprintf(&b, "  time zone %s\n", s.TimeZone)
	}
	if note != "" {
		fmt.Fprintf(&b, "  %s\n", note)
	}
	for _, w := range s.Windows {
		fmt.Fprintf(&b, "  %s\n", WindowText(w.Kind, strings.Join(w.Days, ","), w.Start, w.End, w.Wake))
	}
	if len(s.Exceptions) > 0 {
		fmt.Fprintf(&b, "  except %s\n", strings.Join(s.Exceptions, ", "))
	}
	if s.MaxNapDays > 0 {
		fmt.Fprintf(&b, "  maximum nap %d %s\n", s.MaxNapDays, Plural(s.MaxNapDays, "day"))
	}
	PrintNext(&b, loc, d.Next, d.MaintenanceDue)
	return b.String()
}

func (d WorkloadDetails) weekText() string {
	var rows [][2]string
	if d.States7d != nil {
		rows = append(rows, [2]string{"states", StatesShare(d.States7d.Totals)})
	}
	if d.IdleHours7d != nil {
		idle := strconv.FormatFloat(*d.IdleHours7d, 'f', -1, 64)
		if d.Requests7d != nil {
			idle += fmt.Sprintf(" (%d %s)", *d.Requests7d, Plural(*d.Requests7d, "request"))
		}
		rows = append(rows, [2]string{"idle hours", idle})
	}
	return block(Labelled(rows))
}

func (d WorkloadDetails) costsText() string {
	var rows [][2]string
	if c := d.Costs; c != nil {
		saved := fmt.Sprintf("%s, %s asleep", USD(c.Saved.USD), hours(c.Saved.Hours))
		if c.Saved.UnpricedHours > 0 {
			saved += ", " + hours(c.Saved.UnpricedHours) + " not priced"
		}
		var hourly float64
		priced := false
		for _, i := range c.Items {
			if i.Hourly != nil {
				hourly += *i.Hourly
				priced = true
			}
		}
		var base, month string
		if priced {
			base = fmt.Sprintf("$%.4f an hour", hourly)
			if c.SavedShare != nil {
				month = USD(hourly*monthHours**c.SavedShare) + " a month at the rate of the last 30 days"
			}
		}
		var discounts string
		if c.Discounts != nil {
			discounts = fmt.Sprintf("compute %s%%, databases %s%%", num(c.Discounts.Compute), num(c.Discounts.Databases))
		}
		rows = append(rows, [2]string{"saved (30 days)", saved}, [2]string{"base cost", base}, [2]string{"saving", month}, [2]string{"discounts", discounts})
	}
	if e := d.Energy; e != nil && e.Estimated > 0 {
		rows = append(rows, [2]string{"energy not used", fmt.Sprintf("%.1f kWh, %.2f kg CO2 (estimate)", e.KWh, e.CO2Kg)})
	}
	return block(Labelled(rows))
}

// WorkloadHistory is the answer of GET /iam/workloads/{router}/{id}/history: the manager's historyView with the entries
// it was computed from.
type WorkloadHistory struct {
	From   string         `json:"from"`
	To     string         `json:"to"`
	Totals map[string]int `json:"totals"`
	Wakes  struct {
		Count    int  `json:"count"`
		Failed   int  `json:"failed"`
		MedianMs *int `json:"medianMs"`
		MaxMs    *int `json:"maxMs"`
	} `json:"wakes"`
	Entries   []HistoryEntry `json:"entries"`
	Truncated bool           `json:"truncated"`
	Savings   *struct {
		USD       float64           `json:"usd"`
		Items     []json.RawMessage `json:"items"`
		Unpriced  []json.RawMessage `json:"unpriced"`
		Discounts json.RawMessage   `json:"discounts"`
	} `json:"savings"`
}

// HistoryEntry is a change of state: what it was, what started it, how long a wake took.
type HistoryEntry struct {
	From       *string `json:"from"`
	To         string  `json:"to"`
	At         string  `json:"at"`
	Cause      string  `json:"cause"`
	By         string  `json:"by"`
	Inferred   bool    `json:"inferred"`
	DurationMs int     `json:"durationMs"`
	Failure    *struct {
		Message string `json:"message"`
	} `json:"failure"`
}

// maxTransitions is how many changes the history prints, the latest.
const maxTransitions = 50

var causes = map[string]string{
	"normal": "a request", "probe": "the waiting page's check", "header": "a request with the x-dormeto-wake header",
	"admin": "the router's admin page", "rule-issues": "its rules, which the router could not switch",
	"idle": "the idle check (no request for its idle window)", "pending-wake": "a request during the nap",
	"paused": "a pause during the nap", "wake-failed": "the wake failed", "nap-failed": "the nap failed",
	"wake-stuck": "the wake stopped (its invocation ended)", "nap-stuck": "the nap stopped (its invocation ended)",
	"invoke-failed": "the controller could not be invoked", "failed-released": "released after its idle window (the wake had failed)",
	"schedule": "a scheduled window", "maintenance": "the maximum nap (a maintenance wake)", "recovered": "recovered, seen at a check",
	"external-start": "started outside Dormeto", "external-stop": "stopped outside Dormeto",
}

// causeText is what started a state in words, as the dashboard's history writes it ("" when it does not say).
func causeText(e HistoryEntry) string {
	switch {
	case e.From == nil:
		return "first seen by the manager"
	case e.Inferred:
		return "seen at a check (its event was lost)"
	case e.Cause == "":
		return ""
	}
	who := ""
	if e.By != "" && e.By != "dormeto" {
		who = " by " + e.By
	}
	switch {
	case e.Cause == "manager":
		return map[bool]string{true: "napped", false: "woken"}[e.To == "NAPPING"] + " from the manager" + who
	case e.Cause == "pause":
		return "woken for a pause" + who
	case strings.HasPrefix(e.Cause, "rule "):
		return "the wake rule " + strings.TrimPrefix(e.Cause, "rule ")
	}
	return orElse(causes[e.Cause], e.Cause) + who
}

// Text is the period, the time in each state, the wakes and the savings, then the latest changes (newest first) with
// what started each and, for a wake, how long it took.
func (h WorkloadHistory) Text(loc *time.Location) string {
	var known int
	for state, s := range h.Totals {
		if state != "UNKNOWN" {
			known += s
		}
	}
	states := StatesShare(h.Totals)
	if known == 0 {
		states = "no history in this period yet: it starts when the manager first sees the workload"
	}
	wakes := "no wake"
	if w := h.Wakes; w.Count > 0 {
		wakes = fmt.Sprintf("%d %s", w.Count, Plural(w.Count, "wake"))
		if w.MedianMs != nil {
			wakes += ", median " + Seconds(*w.MedianMs/1000)
		}
		if w.MaxMs != nil {
			wakes += ", longest " + Seconds(*w.MaxMs/1000)
		}
		if w.Failed > 0 {
			wakes += fmt.Sprintf(", %d failed", w.Failed)
		}
	}
	var saved string
	if s := h.Savings; s != nil {
		switch {
		case len(s.Items) > 0:
			saved = fmt.Sprintf("saved about %s (estimate", USD(s.USD))
			if len(s.Discounts) > 0 && string(s.Discounts) != "null" {
				saved += ", after discounts"
			}
			saved += ")"
			if len(s.Unpriced) > 0 {
				saved += ", part not priced"
			}
		case len(s.Unpriced) > 0:
			saved = "savings not priced yet"
		}
	}
	var truncated string
	if h.Truncated {
		truncated = "more than 5000 changes in this period: only the latest are known, the time before them is not"
	}
	out := Labelled([][2]string{{"period", When(h.From, loc) + " to " + When(h.To, loc)}, {"states", states}, {"wakes", wakes}, {"savings", saved}, {"note", truncated}})
	if len(h.Entries) == 0 {
		return out
	}
	rows := [][]string{{"TIME", "STATE", "CAUSE", "DURATION"}}
	entries := slices.Clone(h.Entries)
	slices.Reverse(entries)
	for i, e := range entries {
		if i == maxTransitions {
			break
		}
		cause := causeText(e)
		if e.Failure != nil && e.Failure.Message != "" {
			cause += ": " + e.Failure.Message
		}
		var took string
		if e.DurationMs > 0 {
			took = Seconds(e.DurationMs / 1000)
		}
		rows = append(rows, []string{When(e.At, loc), e.To, orNone(cause), took})
	}
	out += "\n" + Table(rows)
	if n := len(entries) - maxTransitions; n > 0 {
		out += fmt.Sprintf("%d earlier %s: --json has them\n", n, Plural(n, "change"))
	}
	return out
}

// IdleWindowAdvice is the answer of GET /iam/workloads/{router}/{id}/idle-window: the last 14 days of requests replayed
// against idle windows, priced as savings.
type IdleWindowAdvice struct {
	ComputedAt  string   `json:"computedAt"`
	To          string   `json:"to"`
	Current     int      `json:"current"`
	Recommended int      `json:"recommended"`
	WakeSeconds *float64 `json:"wakeSeconds"`
	Priced      bool     `json:"priced"`
	Windows     []struct {
		Minutes           int      `json:"minutes"`
		AsleepHours       float64  `json:"asleepHours"`
		USDMonth          *float64 `json:"usdMonth"`
		WakesPerDay       float64  `json:"wakesPerDay"`
		WaitSecondsPerDay *float64 `json:"waitSecondsPerDay"`
	} `json:"windows"`
	Actual struct {
		Wakes         int  `json:"wakes"`
		ReplayedWakes int  `json:"replayedWakes"`
		Noisy         bool `json:"noisy"`
	} `json:"actual"`
	WakerTargetGroup bool `json:"wakerTargetGroup"`
}

// perDay is a number a day: whole from 10, one decimal below.
func perDay(n float64) string {
	if n >= 10 {
		return strconv.FormatFloat(float64(int(n+0.5)), 'f', -1, 64)
	}
	return strconv.FormatFloat(float64(int(n*10+0.5))/10, 'f', -1, 64)
}

// sentence compares the recommended window with the current one, as the dashboard writes it.
func (a IdleWindowAdvice) sentence() string {
	if a.Recommended == a.Current {
		return fmt.Sprintf("the current window, %d minutes, is the recommended one", a.Current)
	}
	var r, c *int
	for i, w := range a.Windows {
		if w.Minutes == a.Recommended {
			r = &i
		}
		if w.Minutes == a.Current {
			c = &i
		}
	}
	if r == nil || c == nil {
		return fmt.Sprintf("recommended: %d minutes", a.Recommended)
	}
	rw, cw := a.Windows[*r], a.Windows[*c]
	var gain string
	if a.Priced && rw.USDMonth != nil && cw.USDMonth != nil {
		diff := *rw.USDMonth - *cw.USDMonth
		gain = fmt.Sprintf("saves about %s a month %s than now", USD(abs(diff)), map[bool]string{true: "more", false: "less"}[diff >= 0])
	} else {
		diff := rw.AsleepHours - cw.AsleepHours
		gain = fmt.Sprintf("about %s %s hours asleep a day", perDay(abs(diff)), map[bool]string{true: "more", false: "fewer"}[diff >= 0])
	}
	more := rw.WakesPerDay - cw.WakesPerDay
	wakes := "the same wakes"
	if more != 0 {
		wakes = fmt.Sprintf("%s %s wakes a day", perDay(abs(more)), map[bool]string{true: "more", false: "fewer"}[more > 0])
	}
	var wait string
	if more > 0 && a.WakeSeconds != nil {
		wait = fmt.Sprintf(" (about %d seconds each)", int(*a.WakeSeconds+0.5))
	}
	return fmt.Sprintf("recommended: %d minutes, %s, with %s%s", a.Recommended, gain, wakes, wait)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// Text is the sentence of the advice, the windows tried (savings a month, or hours asleep a day when nothing is priced,
// wakes and wait a day) and the notes the dashboard gives under them.
func (a IdleWindowAdvice) Text(loc *time.Location) string {
	measure := "ASLEEP A DAY"
	if a.Priced {
		measure = "SAVINGS A MONTH"
	}
	rows := [][]string{{"WINDOW", measure, "WAKES A DAY", "WAIT A DAY"}}
	quiet := len(a.Windows) > 0
	for _, w := range a.Windows {
		name := fmt.Sprintf("%d min", w.Minutes)
		var marks []string
		if w.Minutes == a.Current {
			marks = append(marks, "current")
		}
		if w.Minutes == a.Recommended {
			marks = append(marks, "recommended")
		}
		if len(marks) > 0 {
			name += " (" + strings.Join(marks, ", ") + ")"
		}
		value := strconv.FormatFloat(float64(int(w.AsleepHours*10+0.5))/10, 'f', -1, 64) + " h"
		if a.Priced {
			value = none
			if w.USDMonth != nil {
				value = USD(*w.USDMonth)
			}
		}
		wait := none
		if w.WaitSecondsPerDay != nil {
			wait = Seconds(int(*w.WaitSecondsPerDay + 0.5))
		}
		rows = append(rows, []string{name, value, perDay(w.WakesPerDay), wait})
		quiet = quiet && w.WakesPerDay == 0 && w.AsleepHours >= 23.5
	}
	out := Labelled([][2]string{{"idle window", fmt.Sprintf("current %d min, recommended %d min", a.Current, a.Recommended)}}) +
		a.sentence() + "\n\n" + Table(rows)
	var notes []string
	if quiet {
		notes = append(notes, "no request in 14 days: the workload sleeps whatever the window")
	}
	if a.Actual.Noisy {
		notes = append(notes, fmt.Sprintf("the replay counts %d wakes over the 14 days for %d that happened: requests the waker ignores (scanners, no-wake rules, IP lists) count here as wakes, so real wakes are fewer", a.Actual.ReplayedWakes, a.Actual.Wakes))
	}
	if a.WakeSeconds == nil {
		notes = append(notes, "no wake measured yet: the visitors' wait is unknown")
	}
	if !a.WakerTargetGroup {
		notes = append(notes, "the waker target group was not found: only the requests while awake are replayed")
	}
	if !a.Priced {
		notes = append(notes, "nothing priced yet (no nap described, or an item without a price): hours asleep instead of dollars")
	}
	notes = append(notes, "to change it: idle_minutes in the workload's Terraform module, or offboard the workload and onboard it again with the new window",
		"every request counts as activity and as a wake; computed "+When(a.ComputedAt, loc)+", from the 14 days to "+When(a.To, loc))
	return out + "\n" + strings.Join(notes, "\n") + "\n"
}

// stateOrder is the order the states are listed in, the others (and UNKNOWN) after it.
var stateOrder = []string{"ASLEEP", "AWAKE", "WAKING", "NAPPING", "FAILED"}

// StatesShare is the seconds per state as shares of the total, in whole percent: "ASLEEP 71%, AWAKE 24%".
func StatesShare(totals map[string]int) string {
	total := 0
	for _, s := range totals {
		total += s
	}
	if total == 0 {
		return ""
	}
	keys := sortedKeys(totals)
	slices.SortStableFunc(keys, func(a, b string) int {
		rank := func(s string) int {
			if i := slices.Index(stateOrder, s); i >= 0 {
				return i
			}
			if s == "UNKNOWN" {
				return len(stateOrder) + 1
			}
			return len(stateOrder)
		}
		return rank(a) - rank(b)
	})
	var parts []string
	for _, k := range keys {
		if totals[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d%%", k, int(float64(totals[k])/float64(total)*100+0.5)))
		}
	}
	return strings.Join(parts, ", ")
}

// Seconds is a duration as the dashboard writes it: 45 s, 3 min 5 s, 2 h 10 min.
func Seconds(s int) string {
	switch m := s / 60; {
	case s < 60:
		return fmt.Sprintf("%d s", s)
	case m < 60 && s%60 != 0:
		return fmt.Sprintf("%d min %d s", m, s%60)
	case m < 60:
		return fmt.Sprintf("%d min", m)
	case m%60 != 0:
		return fmt.Sprintf("%d h %d min", m/60, m%60)
	default:
		return fmt.Sprintf("%d h", m/60)
	}
}

// USD is dollars with thousands grouped and cents: $1,234.50.
func USD(v float64) string {
	s := fmt.Sprintf("%.2f", abs(v))
	whole, cents, _ := strings.Cut(s, ".")
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	return map[bool]string{true: "-", false: ""}[v < 0] + "$" + whole + "." + cents
}

// hours is a number of hours, "300 hours", whole when it is.
func hours(h float64) string {
	return num(h) + " " + Plural(int(h), "hour")
}

// num is a number without trailing zeros, one decimal at most.
func num(f float64) string {
	return strconv.FormatFloat(float64(int(f*10+0.5))/10, 'f', -1, 64)
}

// block is a section's lines indented, "" for none.
func block(lines string) string {
	if lines == "" {
		return ""
	}
	return indent(lines)
}
