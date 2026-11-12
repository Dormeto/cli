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
	"net/http"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/output"
	"github.com/Dormeto/cli/internal/schedule"

	// A time zone is looked up by name wherever the CLI runs.
	_ "time/tzdata"
)

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
		return x.refusedWorkloadWrite(x.needsRecurrenceRouter(err))
	}
	// The controller's answer, {schedule, nextEvents, joinMinutes, …} (onboarding.mjs), read as AWS's {schedule, next, …}; it has no
	// maintenanceDue (unknown: left out, not null).
	var answer struct {
		Schedule   json.RawMessage   `json:"schedule"`
		NextEvents []fleet.NextEvent `json:"nextEvents"`
		schedule.Join
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return fmt.Errorf("the controller's answer is not a schedule: %w", err)
	}
	if len(answer.Schedule) == 0 {
		answer.Schedule = json.RawMessage("null")
	}
	target := x.target(x.r.ID)
	switch method {
	case http.MethodDelete:
		return x.answered(x.r.ID, raw, target+": schedule cleared")
	case http.MethodPut: // AWS's lines: "<target>: schedule set", the zone's note and the next events
		var s fleet.ScheduleView
		if json.Unmarshal(answer.Schedule, &s) != nil || s.TimeZone == "" { // the zone as stored, else the file's
			s = fleet.ScheduleView{}
			_ = json.Unmarshal(x.r.Schedule, &s)
		}
		return x.answered(x.r.ID, raw, strings.TrimSuffix(s.SetText(target, answer.NextEvents, ""), "\n"))
	}
	// AWS's text and --json (the cloud duplication design's B10).
	view := fleet.ScheduleShown{Schedule: answer.Schedule, Next: answer.NextEvents, NoDue: true, Join: answer.Join}
	if x.r.JSON {
		return output.JSON(x.d.Out, view.JSON())
	}
	text, err := view.Text(target)
	if err != nil {
		return err
	}
	fmt.Fprint(x.d.Out, text)
	return nil
}

// pauseIn is the pause's body of --reason, --keep-asleep and --until, without `by`, and its end (zero for none). The command
// checked them before any call (pauseFlags.gcpEnd: AWS's pauseReason and endOf): the reason trimmed, 1 to 256 characters, and
// --until an RFC 3339 time, sent in UTC; one that is not is refused here too, before any call (exit 2).
func (x *run) pauseIn() (map[string]any, time.Time, error) {
	in := map[string]any{"reason": x.r.Reason}
	if x.r.KeepAsleep {
		in["keepAsleep"] = true
	}
	var end time.Time
	if x.r.Until != "" {
		var err error
		if end, err = time.Parse(time.RFC3339, x.r.Until); err != nil {
			return nil, end, &stop{2, fmt.Sprintf("--until: %q is not an RFC 3339 time", x.r.Until)}
		}
		in["until"] = end.UTC().Format(time.RFC3339)
	}
	return in, end, nil
}

func (x *run) pauseWorkload() error {
	in, end, err := x.pauseIn()
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
	return x.answeredWorkload(x.r.ID, raw, x.target(x.r.ID)+": "+fleet.PauseResult(end, x.d.Now().Location(), x.r.KeepAsleep, w.Woken))
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
