package schedule

// The changes of `dormeto aws schedule` and `dormeto gcp schedule` (add, remove, except, edit): a schedule read, changed and
// written whole, as the dashboard does. Each works on the JSON as the router answered it, every member it does not touch kept
// as it came, unknown ones included: nothing goes through a struct that would drop a field. The router checks the result;
// what is refused here is only what cannot be built (a window's number out of range, no schedule to change).

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
)

// Change is one change of a schedule, as the commands' flags say it: one of Add (with TimeZone, the zone of a first
// schedule), Remove (a window's number, from 1), Except, or Fields (edit: each member set to its value, or removed when
// nil).
type Change struct {
	Add      *NewWindow
	TimeZone string
	Remove   int
	Except   *Exceptions
	Fields   map[string]json.RawMessage
}

// NewWindow is a window `schedule add` appends, its members the router's (lib/schedule.mjs); an empty one is left out.
type NewWindow struct {
	Kind   string     `json:"kind"`
	Days   []string   `json:"days,omitempty"`
	Start  string     `json:"start,omitempty"`
	End    string     `json:"end,omitempty"`
	Repeat *NewRepeat `json:"repeat,omitempty"`
	From   string     `json:"from,omitempty"`
	Until  string     `json:"until,omitempty"`
	Wake   string     `json:"wake,omitempty"`
}

// NewRepeat is a new window's repeat: its pattern and the members the flags gave.
type NewRepeat struct {
	Pattern  string   `json:"pattern"`
	Interval *int     `json:"interval,omitempty"`
	Days     []string `json:"days,omitempty"`
	Day      *int     `json:"day,omitempty"`
	Nth      *int     `json:"nth,omitempty"`
	Weekday  string   `json:"weekday,omitempty"`
	Cron     string   `json:"cron,omitempty"`
	Minutes  *int     `json:"minutes,omitempty"`
}

// Exceptions are dates without a window that `schedule except` adds, or takes out with Remove: the schedule's (Window 0)
// or one window's (its number, from 1).
type Exceptions struct {
	Dates  []string
	Window int
	Remove bool
}

// LastWindowHint is what a refused removal of a schedule's last window adds: the router may refuse a schedule left with
// nothing, and clear removes it.
const LastWindowHint = "that was its last window: schedule clear removes the schedule"

// ErrNoSchedule is a change that needs a schedule, of a workload that has none.
var ErrNoSchedule = errors.New("no schedule: schedule add makes one")

// Apply is the schedule raw (the router's answer; empty or null for none) with the change, every other member as it was.
func (c Change) Apply(raw json.RawMessage) (json.RawMessage, error) {
	s, none, err := decode(raw)
	if err != nil {
		return nil, err
	}
	switch {
	case c.Add != nil:
		err = c.add(s, none)
	case c.Remove != 0:
		err = remove(s, none, c.Remove)
	case c.Except != nil:
		err = except(s, none, *c.Except)
	default:
		for k, v := range c.Fields {
			if v == nil {
				delete(s, k)
			} else {
				s[k] = v
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

// Windows is the number of windows of a schedule, 0 for none.
func Windows(raw json.RawMessage) int {
	s, _, err := decode(raw)
	if err != nil {
		return 0
	}
	ws, _ := windows(s)
	return len(ws)
}

// decode is the schedule's members, none when there is no schedule (empty, null).
func decode(raw json.RawMessage) (s map[string]json.RawMessage, none bool, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]json.RawMessage{}, true, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil || s == nil {
		return nil, false, fmt.Errorf("the schedule is not a JSON object: %s", raw)
	}
	return s, false, nil
}

// windows is the schedule's windows, each as it came (none for an absent or null member).
func windows(s map[string]json.RawMessage) ([]json.RawMessage, error) {
	var ws []json.RawMessage
	if m, ok := s["windows"]; ok && string(m) != "null" {
		if err := json.Unmarshal(m, &ws); err != nil {
			return nil, fmt.Errorf("the schedule's windows are not a list: %s", m)
		}
	}
	return ws, nil
}

func setWindows(s map[string]json.RawMessage, ws []json.RawMessage) error {
	if ws == nil {
		ws = []json.RawMessage{}
	}
	b, err := json.Marshal(ws)
	s["windows"] = b
	return err
}

func (c Change) add(s map[string]json.RawMessage, none bool) error {
	var zone string
	if m, ok := s["timeZone"]; ok {
		_ = json.Unmarshal(m, &zone)
	}
	switch {
	case none && c.TimeZone == "":
		return errors.New("--time-zone: the workload has no schedule: a first one needs its time zone")
	case c.TimeZone != "" && zone != "" && zone != c.TimeZone:
		return fmt.Errorf("--time-zone: the schedule's zone is %s: schedule edit --time-zone changes it", zone)
	case c.TimeZone != "" && zone == "":
		s["timeZone"], _ = json.Marshal(c.TimeZone)
	}
	ws, err := windows(s)
	if err != nil {
		return err
	}
	w, err := json.Marshal(c.Add)
	if err != nil {
		return err
	}
	return setWindows(s, append(ws, w))
}

// numbered is the index of window n of ws, refused out of range.
func numbered(ws []json.RawMessage, n int) (int, error) {
	if n >= 1 && n <= len(ws) {
		return n - 1, nil
	}
	has := "none"
	if len(ws) > 0 {
		has = fmt.Sprint(len(ws))
	}
	return 0, fmt.Errorf("--window: %d is not a window of the schedule, which has %s (schedule show numbers them)", n, has)
}

func remove(s map[string]json.RawMessage, none bool, n int) error {
	if none {
		return ErrNoSchedule
	}
	ws, err := windows(s)
	if err != nil {
		return err
	}
	i, err := numbered(ws, n)
	if err != nil {
		return err
	}
	return setWindows(s, slices.Delete(ws, i, i+1))
}

func except(s map[string]json.RawMessage, none bool, e Exceptions) error {
	if none {
		return ErrNoSchedule
	}
	if e.Window == 0 {
		return exceptIn(s, e)
	}
	ws, err := windows(s)
	if err != nil {
		return err
	}
	i, err := numbered(ws, e.Window)
	if err != nil {
		return err
	}
	var w map[string]json.RawMessage
	if err := json.Unmarshal(ws[i], &w); err != nil || w == nil {
		return fmt.Errorf("window %d is not a JSON object: %s", e.Window, ws[i])
	}
	if err := exceptIn(w, e); err != nil {
		return err
	}
	if ws[i], err = json.Marshal(w); err != nil {
		return err
	}
	return setWindows(s, ws)
}

// exceptIn adds the dates to the object's exceptions, each once and after those it has, or takes them out; a list left
// empty goes.
func exceptIn(o map[string]json.RawMessage, e Exceptions) error {
	var dates []json.RawMessage
	if m, ok := o["exceptions"]; ok && string(m) != "null" {
		if err := json.Unmarshal(m, &dates); err != nil {
			return fmt.Errorf("the exceptions are not a list: %s", m)
		}
	}
	has := func(d string) int {
		return slices.IndexFunc(dates, func(r json.RawMessage) bool {
			var s string
			return json.Unmarshal(r, &s) == nil && s == d
		})
	}
	for _, d := range e.Dates {
		i := has(d)
		switch {
		case e.Remove && i >= 0:
			dates = slices.Delete(dates, i, i+1)
		case !e.Remove && i < 0:
			b, _ := json.Marshal(d)
			dates = append(dates, b)
		}
	}
	if len(dates) == 0 {
		delete(o, "exceptions")
		return nil
	}
	b, err := json.Marshal(dates)
	o["exceptions"] = b
	return err
}

var (
	dateForm = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	timeForm = regexp.MustCompile(`^\d{2}:\d{2}$`)
)

// CheckDate refuses a flag's value that is not a calendar date YYYY-MM-DD.
func CheckDate(flag, value string) error {
	if _, err := time.Parse(time.DateOnly, value); err != nil || !dateForm.MatchString(value) {
		return fmt.Errorf("%s: %q is not a date YYYY-MM-DD", flag, value)
	}
	return nil
}

// CheckTime refuses a flag's value that is not a time of day HH:MM (which minutes the router takes is its to say).
func CheckTime(flag, value string) error {
	if _, err := time.Parse("15:04", value); err != nil || !timeForm.MatchString(value) {
		return fmt.Errorf("%s: %q is not a time HH:MM", flag, value)
	}
	return nil
}
