package gcponboard

// `dormeto gcp workloads schedule show|set|clear`, `pause` and `resume`: the controller's GET, PUT and DELETE
// /v1/workloads/<id>/schedule, and its POST .../pause and .../resume. The operator's gcloud account is the `by` of each
// change. The router's checks (a schedule's problems, the 366 days of a pause) are the controller's, said as it says them;
// what the CLI refuses before any call is a missing or unreadable file, a missing reason and an --until that is not a time.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	// A time zone is looked up by name wherever the CLI runs.
	_ "time/tzdata"
)

// reasonMax is the router's (REASON_MAX of controller-core.mjs).
const reasonMax = 256

// WorkloadsScheduleShow is `dormeto gcp workloads schedule show <id>`.
func WorkloadsScheduleShow(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.schedule(http.MethodGet, nil))
}

// WorkloadsScheduleSet is `dormeto gcp workloads schedule set <id> --file <schedule.json>`.
func WorkloadsScheduleSet(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.schedule(http.MethodPut, x.scheduleFromFile))
}

// WorkloadsScheduleClear is `dormeto gcp workloads schedule clear <id>`.
func WorkloadsScheduleClear(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.schedule(http.MethodDelete, nil))
}

// WorkloadsPause is `dormeto gcp workloads pause <id> --reason <text>`.
func WorkloadsPause(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.pauseWorkload())
}

// WorkloadsResume is `dormeto gcp workloads resume <id>`.
func WorkloadsResume(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.resumeWorkload())
}

// scheduleWindow, scheduleOf and nextEvent are the controller's schedule and next events (lambda/src/lib/schedule.mjs).
type scheduleWindow struct {
	Kind  string   `json:"kind"`
	Days  []string `json:"days"`
	Start string   `json:"start"`
	End   string   `json:"end"`
	Wake  string   `json:"wake"`
}

type scheduleOf struct {
	TimeZone   string           `json:"timeZone"`
	Windows    []scheduleWindow `json:"windows"`
	Exceptions []string         `json:"exceptions"`
	MaxNapDays *int             `json:"maxNapDays"`
	By         string           `json:"by"`
	At         string           `json:"at"`
}

type nextEvent struct {
	At     string `json:"at"`
	Kind   string `json:"kind"` // nap-start, nap-end, awake-start, awake-end
	Window int    `json:"window"`
}

// scheduleFromFile is the body of a PUT: the file's schedule, `by` the operator's account.
func (x *run) scheduleFromFile() (any, error) {
	if x.r.File == "" {
		return nil, &stop{2, "--file: needed, the file of the schedule as JSON"}
	}
	raw, err := os.ReadFile(x.r.File)
	if err != nil {
		return nil, &stop{2, "--file: " + err.Error()}
	}
	var schedule map[string]any
	if err := json.Unmarshal(raw, &schedule); err != nil || schedule == nil {
		return nil, &stop{2, fmt.Sprintf("--file: %s is not a JSON object (a schedule)", x.r.File)}
	}
	by, err := x.d.Google.Tokens.Account(x.ctx)
	if err != nil {
		return nil, err
	}
	schedule["by"] = by
	return schedule, nil
}

func (x *run) schedule(method string, body func() (any, error)) error {
	if err := x.require("workloads"); err != nil {
		return err
	}
	path, err := x.workloadPath()
	if err != nil {
		return err
	}
	var in any
	if body != nil {
		if in, err = body(); err != nil {
			return err
		}
	}
	var raw json.RawMessage
	if err := x.controller(method, path+"/schedule", in, &raw); err != nil {
		return err
	}
	if x.r.JSON {
		fmt.Fprintf(x.d.Out, "%s\n", raw)
		return nil
	}
	var view struct {
		Schedule   *scheduleOf `json:"schedule"`
		NextEvents []nextEvent `json:"nextEvents"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return fmt.Errorf("the controller's answer is not a schedule: %w", err)
	}
	switch {
	case view.Schedule == nil && method == http.MethodDelete:
		x.say("The schedule of workload %s is cleared.", x.r.ID)
	case view.Schedule == nil:
		x.say("Workload %s has no schedule.", x.r.ID)
	default:
		if method == http.MethodPut {
			x.say("The schedule of workload %s is set.", x.r.ID)
		}
		x.printSchedule(*view.Schedule, view.NextEvents)
	}
	return nil
}

func (x *run) printSchedule(s scheduleOf, events []nextEvent) {
	x.say("Schedule of workload %s (set by %s, %s):", x.r.ID, s.By, s.At)
	x.say("  time zone: %s", s.TimeZone)
	for _, w := range s.Windows {
		line := fmt.Sprintf("  %s window: %s %s-%s", w.Kind, strings.Join(w.Days, ","), w.Start, w.End)
		if w.Wake != "" {
			line += ", wake " + w.Wake
		}
		x.say("%s", line)
	}
	if len(s.Exceptions) > 0 {
		x.say("  exceptions: %s", strings.Join(s.Exceptions, ", "))
	}
	if s.MaxNapDays != nil {
		x.say("  maxNapDays: %d", *s.MaxNapDays)
	}
	if len(events) == 0 {
		x.say("No next event.")
		return
	}
	x.say("Next events:")
	zone, err := time.LoadLocation(s.TimeZone)
	for _, e := range events {
		at, perr := time.Parse(time.RFC3339, e.At)
		what := strings.Replace(e.Kind, "-start", " starts", 1)
		what = strings.Replace(what, "-end", " ends", 1)
		if perr != nil {
			x.say("  %s  %s", e.At, what)
			continue
		}
		if err != nil {
			x.say("  %s UTC  %s", at.UTC().Format("2006-01-02 15:04"), what)
			continue
		}
		x.say("  %s (%s)  %s", at.In(zone).Format("2006-01-02 15:04 MST"), s.TimeZone, what)
	}
}

// pausedOf is a workload's active pause.
type pausedOf struct {
	By         string  `json:"by"`
	At         string  `json:"at"`
	Reason     string  `json:"reason"`
	Until      *string `json:"until"`
	KeepAsleep bool    `json:"keepAsleep"`
}

func (p pausedOf) String() string {
	text := fmt.Sprintf("paused by %s: %s", p.By, p.Reason)
	if p.KeepAsleep {
		text += " (keep asleep)"
	}
	if p.Until != nil {
		text += ", until " + *p.Until
	}
	return text
}

func (x *run) pauseWorkload() error {
	reason := strings.TrimSpace(x.r.Reason)
	switch {
	case reason == "":
		return &stop{2, "--reason: needed, the reason of the pause"}
	case utf8.RuneCountInString(reason) > reasonMax:
		return &stop{2, fmt.Sprintf("--reason: at most %d characters", reasonMax)}
	}
	in := map[string]any{"reason": reason}
	if x.r.KeepAsleep {
		in["keepAsleep"] = true
	}
	if x.r.Until != "" {
		until, err := time.Parse(time.RFC3339, x.r.Until)
		if err != nil {
			return &stop{2, fmt.Sprintf("--until: %q is not a time in RFC 3339, such as 2026-12-01T08:00:00+01:00", x.r.Until)}
		}
		in["until"] = until.UTC().Format(time.RFC3339)
	}
	if err := x.require("workloads"); err != nil {
		return err
	}
	path, err := x.workloadPath()
	if err != nil {
		return err
	}
	if in["by"], err = x.d.Google.Tokens.Account(x.ctx); err != nil {
		return err
	}
	var raw json.RawMessage
	if err := x.controller(http.MethodPost, path+"/pause", in, &raw); err != nil {
		return err
	}
	return x.print(raw, func(w workload) {
		x.say("Workload %s is paused (%s): none of its naps runs until it is resumed.", w.ID, reason)
		if x.r.KeepAsleep {
			x.say("It stays as it is (%s): a visitor still wakes the workload, and scheduled wakes wait until the pause ends.", strings.ToLower(w.State))
		} else {
			x.say("A workload asleep is woken (now %s), and kept awake.", strings.ToLower(w.State))
		}
		x.say("End it with: %s", strings.Replace(x.showCommand(w.ID), " show ", " resume ", 1))
	})
}

func (x *run) resumeWorkload() error {
	if err := x.require("workloads"); err != nil {
		return err
	}
	path, err := x.workloadPath()
	if err != nil {
		return err
	}
	by, err := x.d.Google.Tokens.Account(x.ctx)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := x.controller(http.MethodPost, path+"/resume", map[string]any{"by": by}, &raw); err != nil {
		return err
	}
	return x.print(raw, func(w workload) {
		if w.Paused != nil {
			x.say("Workload %s is still %s.", w.ID, w.Paused)
			return
		}
		x.say("Workload %s is resumed: its naps run again.", w.ID)
	})
}
