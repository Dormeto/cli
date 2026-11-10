package gcponboard

// `dormeto gcp workloads schedule show|set|clear`, `pause` and `resume`: the controller's GET, PUT and DELETE
// /v1/workloads/<id>/schedule, and its POST .../pause and .../resume. The operator's gcloud account is the `by` of each
// change. The router's checks (a schedule's problems, the 366 days of a pause) are the controller's, said as it says them;
// what the CLI refuses before any call is a missing schedule or one that is not a JSON object, a missing reason and an --until
// that is not a time. --dry-run says the request and sends nothing.
// A controller below 0.35.0 has no route for these calls, which is said as that.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/schedule"

	// A time zone is looked up by name wherever the CLI runs.
	_ "time/tzdata"
)

// reasonMax is the router's (REASON_MAX of controller-core.mjs).
const reasonMax = 256

// WorkloadsScheduleShow is `dormeto gcp workloads schedule show <id>`.
func WorkloadsScheduleShow(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.schedule(http.MethodGet, nil, nil))
}

// WorkloadsScheduleSet is `dormeto gcp workloads schedule set <id> --file <schedule.json>`.
func WorkloadsScheduleSet(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.scheduleSet())
}

// WorkloadsScheduleClear is `dormeto gcp workloads schedule clear <id>`.
func WorkloadsScheduleClear(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.schedule(http.MethodDelete, nil, nil))
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

// scheduleOf and nextEvent are the controller's schedule and next events (lambda/src/lib/schedule.mjs).
type scheduleOf struct {
	TimeZone   string            `json:"timeZone"`
	Windows    []schedule.Window `json:"windows"`
	Exceptions []string          `json:"exceptions"`
	MaxNapDays *int              `json:"maxNapDays"`
	By         string            `json:"by"`
	At         string            `json:"at"`
}

type nextEvent struct {
	At     string `json:"at"`
	Kind   string `json:"kind"` // nap-start, nap-end, awake-start, awake-end
	Window int    `json:"window"`
}

// scheduleSet checks the schedule first, before any call; the body of the PUT is the schedule, `by` the operator's account.
func (x *run) scheduleSet() error {
	schedule, err := x.scheduleIn()
	if err != nil {
		return err
	}
	return x.schedule(http.MethodPut, func() (any, error) {
		by, err := x.d.Google.Tokens.Account(x.ctx)
		if err != nil {
			return nil, err
		}
		schedule["by"] = by
		return schedule, nil
	}, schedule)
}

// scheduleIn is the schedule the command read from --file (AWS's reader: a path or -, and the shape of schedule show --json):
// a JSON object.
func (x *run) scheduleIn() (map[string]any, error) {
	if len(x.r.Schedule) == 0 {
		return nil, &stop{2, "--file: needed, the schedule as a JSON file (- for standard input)"}
	}
	var schedule map[string]any
	if err := json.Unmarshal(x.r.Schedule, &schedule); err != nil || schedule == nil {
		return nil, &stop{2, "--file: not a JSON object (a schedule)"}
	}
	return schedule, nil
}

// controllerSince035 is a call of the controller that a router below 0.35.0 does not route: its 404 "no route" says the
// version it needs, unlike its 404 of a workload it does not know.
func (x *run) controllerSince035(method, path string, in, out any) error {
	err := x.controller(method, path, in, out)
	var e *gcp.Error
	if errors.As(err, &e) && e.Code == http.StatusNotFound && strings.HasPrefix(e.Message, "no route for ") {
		return &stop{1, fmt.Sprintf("Router %s does not have this call: workloads schedule, pause and resume need a router of version %s or later. Upgrade it with: %s",
			x.n.router, minRouterVersion, x.installCommand("--project "+x.n.project+" --region "+x.n.region, "<the router's image, "+minRouterVersion+" or later>"))}
	}
	return err
}

// needsRecurrenceRouter says what a refusal means when it comes from a router too old for recurrences.
func (x *run) needsRecurrenceRouter(err error) error {
	var e *gcp.Error
	if !errors.As(err, &e) || !schedule.RefusedAsUnknown(e.Message) {
		return err
	}
	return &stop{1, fmt.Sprintf("%s\nRecurrences and joined windows need a router of version %s or later. Upgrade it with: %s",
		refusal(err), schedule.RecurrenceRouter, x.installCommand("--project "+x.n.project+" --region "+x.n.region, "<the router's image, "+schedule.RecurrenceRouter+" or later>"))}
}

// schedule is the controller's GET, PUT or DELETE of the workload's schedule: body gives the PUT's, and shown is it as a dry run
// says it (without `by`).
func (x *run) schedule(method string, body func() (any, error), shown any) error {
	if err := x.require("workloads"); err != nil {
		return err
	}
	path, err := x.workloadPath()
	if err != nil {
		return err
	}
	if x.r.DryRun && method != http.MethodGet {
		return x.workloadDryRun(method, path+"/schedule", shown)
	}
	var in any
	if body != nil {
		if in, err = body(); err != nil {
			return err
		}
	}
	var raw json.RawMessage
	if err := x.controllerSince035(method, path+"/schedule", in, &raw); err != nil {
		if method == http.MethodGet {
			return x.needsRecurrenceRouter(err)
		}
		return x.refusedWrite(x.needsRecurrenceRouter(err))
	}
	if x.r.JSON && method == http.MethodGet {
		fmt.Fprintf(x.d.Out, "%s\n", raw)
		return nil
	}
	var view struct {
		Schedule   *scheduleOf `json:"schedule"`
		NextEvents []nextEvent `json:"nextEvents"`
		schedule.Join
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return fmt.Errorf("the controller's answer is not a schedule: %w", err)
	}
	// The writes: AWS's line, "<target>: schedule set" and the schedule under it, or "<target>: schedule cleared".
	target := x.target(x.r.ID)
	var b strings.Builder
	switch {
	case method == http.MethodDelete:
		return x.answered(x.r.ID, raw, target+": schedule cleared")
	case method == http.MethodPut:
		fmt.Fprintf(&b, "%s: schedule set\n", target)
		if view.Schedule != nil {
			x.printSchedule(&b, *view.Schedule, view.NextEvents, view.Line())
		}
		return x.answered(x.r.ID, raw, strings.TrimSuffix(b.String(), "\n"))
	case view.Schedule == nil:
		x.say("Workload %s has no schedule.", x.r.ID)
	default:
		fmt.Fprintf(&b, "Schedule of workload %s (set by %s, %s):\n", x.r.ID, view.Schedule.By, view.Schedule.At)
		x.printSchedule(&b, *view.Schedule, view.NextEvents, view.Line())
		fmt.Fprint(x.d.Out, b.String())
	}
	return nil
}

// printSchedule writes a schedule's lines, under the line that names it.
func (x *run) printSchedule(out io.Writer, s scheduleOf, events []nextEvent, join string) {
	say := func(format string, a ...any) { fmt.Fprintf(out, format+"\n", a...) }
	say("  time zone: %s", s.TimeZone)
	for _, w := range s.Windows {
		line := fmt.Sprintf("  %s window: %s %s-%s", w.Kind, strings.Join(w.Days, ","), w.Start, w.End)
		if !w.Plain() {
			line = fmt.Sprintf("  %s window: %s", w.Kind, w.Text())
		}
		if w.Wake != "" {
			line += ", wake " + w.Wake
		}
		say("%s", line)
	}
	if len(s.Exceptions) > 0 {
		say("  exceptions: %s", strings.Join(s.Exceptions, ", "))
	}
	if s.MaxNapDays != nil {
		say("  maxNapDays: %d", *s.MaxNapDays)
	}
	if join != "" {
		say("  %s", join)
	}
	if len(events) == 0 {
		say("No next event.")
		return
	}
	say("Next events:")
	zone, err := time.LoadLocation(s.TimeZone)
	for _, e := range events {
		at, perr := time.Parse(time.RFC3339, e.At)
		what := strings.Replace(e.Kind, "-start", " starts", 1)
		what = strings.Replace(what, "-end", " ends", 1)
		if perr != nil {
			say("  %s  %s", e.At, what)
			continue
		}
		if err != nil {
			say("  %s UTC  %s", at.UTC().Format("2006-01-02 15:04"), what)
			continue
		}
		say("  %s (%s)  %s", at.In(zone).Format("2006-01-02 15:04 MST"), s.TimeZone, what)
	}
}

// pausedOf is a workload's active pause: its own, or (scope router) its router's.
type pausedOf struct {
	Scope      string  `json:"scope"`
	By         string  `json:"by"`
	At         string  `json:"at"`
	Reason     string  `json:"reason"`
	Until      *string `json:"until"`
	KeepAsleep bool    `json:"keepAsleep"`
}

func (p pausedOf) String() string {
	text := fmt.Sprintf("paused by %s: %s", p.By, p.Reason)
	if p.Scope == "router" {
		text = "router " + text
	}
	if p.KeepAsleep {
		text += " (keep asleep)"
	}
	if p.Until != nil {
		text += ", until " + *p.Until
	}
	return text
}

// pauseIn validates --reason, --keep-asleep and --until before any call and gives the pause's body, without `by`, and how its
// end reads in the lines, as AWS's: "until resumed", or "until <the local time>".
func (x *run) pauseIn() (map[string]any, string, error) {
	reason := strings.TrimSpace(x.r.Reason)
	switch {
	case reason == "":
		return nil, "", &stop{2, "--reason: needed, the reason of the pause"}
	case utf8.RuneCountInString(reason) > reasonMax:
		return nil, "", &stop{2, fmt.Sprintf("--reason: at most %d characters", reasonMax)}
	}
	in := map[string]any{"reason": reason}
	if x.r.KeepAsleep {
		in["keepAsleep"] = true
	}
	ends := "until resumed"
	if x.r.Until != "" {
		until, err := time.Parse(time.RFC3339, x.r.Until)
		if err != nil {
			return nil, "", &stop{2, fmt.Sprintf("--until: %q is not a time in RFC 3339, such as 2026-12-01T08:00:00+01:00", x.r.Until)}
		}
		in["until"] = until.UTC().Format(time.RFC3339)
		ends = "until " + until.In(x.d.Now().Location()).Format("2006-01-02 15:04 MST")
	}
	return in, ends, nil
}

func (x *run) pauseWorkload() error {
	in, ends, err := x.pauseIn()
	if err != nil {
		return err
	}
	if err := x.require("workloads"); err != nil {
		return err
	}
	path, err := x.workloadPath()
	if err != nil {
		return err
	}
	if x.r.DryRun {
		return x.workloadDryRun(http.MethodPost, path+"/pause", in)
	}
	if in["by"], err = x.d.Google.Tokens.Account(x.ctx); err != nil {
		return err
	}
	var raw json.RawMessage
	if err := x.controllerSince035(http.MethodPost, path+"/pause", in, &raw); err != nil {
		return x.refusedWorkloadWrite(err)
	}
	w, err := decodeWorkload(raw)
	if err != nil {
		return err
	}
	// AWS's line: "<target>: paused until resumed[; woken <workloads>]", the workloads woken as the answer names them (none from
	// a router that names none: never guessed from the state).
	line := fmt.Sprintf("%s: paused %s", x.target(x.r.ID), ends)
	if len(w.Woken) > 0 {
		line += "; woken " + strings.Join(w.Woken, ", ")
	}
	return x.answeredWorkload(x.r.ID, raw, line)
}

func (x *run) resumeWorkload() error {
	if err := x.require("workloads"); err != nil {
		return err
	}
	path, err := x.workloadPath()
	if err != nil {
		return err
	}
	if x.r.DryRun {
		return x.workloadDryRun(http.MethodPost, path+"/resume", nil)
	}
	by, err := x.d.Google.Tokens.Account(x.ctx)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := x.controllerSince035(http.MethodPost, path+"/resume", map[string]any{"by": by}, &raw); err != nil {
		return x.refusedWorkloadWrite(err)
	}
	if _, err := decodeWorkload(raw); err != nil {
		return err
	}
	// AWS's line, "<target>: resumed", as AWS's says it under a router pause too (the workload's show names that one).
	return x.answeredWorkload(x.r.ID, raw, x.target(x.r.ID)+": resumed")
}
