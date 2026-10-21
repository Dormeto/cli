package main

// `dormeto workloads keep-awake`, `resume` and `schedule show|set|clear`: pauses and schedules of workloads through the
// manager's /iam routes. Targets are <router>/<workload> arguments or --tag (every workload that carries them); more
// than one target is confirmed on a terminal (--yes without one; --dry-run lists them). Exit codes as the fleet commands:
// 0; 1 when the manager refused or a workload failed or did not wake in time; 2 for a bad flag or argument, or no --yes
// without a terminal; 128 and the signal's number on a signal.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

// maxKeepAwake is the longest a keep-awake lasts (the manager's pauses accept more; the CLI is for a CI's hours).
const maxKeepAwake = 7 * 24 * time.Hour

// defaultKeepAwakeReason is the pause's reason when --reason is not given.
const defaultKeepAwakeReason = "keep-awake from the CLI"

// waitConcurrency bounds how many workloads --wait reads at once.
const waitConcurrency = 8

// targetFlags are the targets of a command: its arguments or --tag.
type targetFlags struct{ tags []string }

func (t *targetFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&t.tags, "tag", nil, "every workload with this Dormeto tag, key=value (repeatable: all must match), instead of targets")
}

// check refuses, before any call, targets that are neither arguments nor tags (or both), and a malformed target or tag.
func (t *targetFlags) check(args []string, one bool) error {
	switch {
	case len(args) > 0 && len(t.tags) > 0:
		return usagef("name the targets or use --tag, not both")
	case len(args) == 0 && len(t.tags) == 0:
		return usagef("name at least one target (<router>/<workload id>) or use --tag")
	case one && (len(args) != 1 || len(t.tags) > 0):
		return usagef("name exactly one target (<router>/<workload id>)")
	}
	for _, a := range args {
		if _, err := fleet.ParseTarget(a); err != nil {
			return usageError{err}
		}
	}
	if _, err := fleet.ParseTags(t.tags); err != nil {
		return usageError{err}
	}
	return nil
}

// resolve gives the targets: the arguments, or the workloads GET /workloads?tag=… lists, every page of it. A router
// that did not answer, or a listing that cannot be completed, is not fatal: the workloads resolved are returned and
// incomplete says what could not be resolved ("" when nothing is missing). None matching at all is a bad argument.
func (t *targetFlags) resolve(ctx context.Context, c *cobra.Command, api *fleet.Client, args []string) (targets []fleet.Target, incomplete string, err error) {
	if len(args) > 0 {
		var out []fleet.Target
		for _, a := range args {
			tg, _ := fleet.ParseTarget(a) // checked
			if !slices.Contains(out, tg) {
				out = append(out, tg)
			}
		}
		return out, "", nil
	}
	var (
		out     []fleet.Target
		silent  []string // routers that did not answer
		partial bool
		seen    = map[string]bool{}
		cursor  string
	)
	warnings := newRouterWarnings(c)
	for page := 0; page < 1000; page++ {
		q := url.Values{"tag": t.tags}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var w fleet.Workloads
		if err := api.Get(ctx, "/workloads", q, &w); err != nil {
			return nil, "", err
		}
		for _, e := range w.Errors {
			warnings.warn(e.Router, e.Message, "its workloads' tags are unknown")
			if !slices.Contains(silent, e.Router) {
				silent = append(silent, e.Router)
			}
		}
		for _, wl := range w.Rows {
			if tg := (fleet.Target{Router: wl.Router, ID: wl.WorkloadID}); !slices.Contains(out, tg) {
				out = append(out, tg)
			}
		}
		if !w.More() {
			break
		}
		var next string
		if json.Unmarshal(w.Cursor, &next) != nil || next == "" || seen[next] {
			partial = true
			break
		}
		seen[next], cursor = true, next
		if page == 999 {
			partial = true
		}
	}
	var missing []string
	if len(silent) > 0 {
		slices.Sort(silent)
		missing = append(missing, "router "+strings.Join(silent, ", ")+" did not answer")
	}
	if partial {
		missing = append(missing, "the manager's listing could not be completed")
	}
	if len(missing) > 0 {
		incomplete = "not all the workloads with these tags were resolved: " + strings.Join(missing, "; ")
	}
	if len(out) == 0 && incomplete == "" {
		return nil, "", usagef("no workload carries %s", strings.Join(t.tags, ", "))
	}
	return out, incomplete, nil
}

// confirmWorkloads lists the targets on --dry-run (done: true), else asks on a terminal when there are several, or
// always (the destructive commands), and --yes is absent (done: true when declined), the targets listed first; it
// refuses without a terminal. It is confirmNames with what in the dry run's line, and question taking the count in
// words ("Offboard %s?": "Offboard 1 workload?").
func confirmWorkloads(ctx context.Context, c *cobra.Command, deps fleetDeps, targets []fleet.Target, what, question string, always, dryRun, yes bool) (done bool, err error) {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.String()
	}
	count := fmt.Sprintf("%d %s", len(names), fleet.Plural(len(names), "workload"))
	if dryRun {
		return true, stopDryRun(c, what+" "+count, names)
	}
	return ask(ctx, c, deps, names, "workload", fmt.Sprintf(question, count), always, yes, true)
}

// applyAll runs do for each target in turn: its line goes to stdout, its failure to stderr as "target: message"; the
// others go on. A failure makes the exit 1. It returns the targets that succeeded.
func applyAll(ctx context.Context, c *cobra.Command, targets []fleet.Target, do func(fleet.Target) (string, error)) ([]fleet.Target, error) {
	var ok []fleet.Target
	failed := 0
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return ok, err
		}
		line, err := do(t)
		if err != nil {
			if ctx.Err() != nil {
				return ok, ctx.Err()
			}
			fmt.Fprintf(c.ErrOrStderr(), "%s: %s\n", t, err)
			failed++
			continue
		}
		ok = append(ok, t)
		fmt.Fprint(c.OutOrStdout(), line)
	}
	if failed > 0 {
		return ok, exitCode(1)
	}
	return ok, nil
}

// applyAnswers is applyAll for commands with --json: do also gives the API's answer for the target. Without asJSON the
// lines go to stdout as applyAll prints them; with it they go to stderr, and the answers of the targets that succeeded
// are printed on stdout, by target ({} for none), even when the exit is 1.
func applyAnswers(ctx context.Context, c *cobra.Command, targets []fleet.Target, asJSON bool, do func(fleet.Target) (line string, answer json.RawMessage, err error)) ([]fleet.Target, error) {
	answers := map[string]json.RawMessage{}
	ok, err := applyAll(ctx, c, targets, func(t fleet.Target) (string, error) {
		line, answer, err := do(t)
		if err != nil {
			return "", err
		}
		answers[t.String()] = orRaw(answer)
		if asJSON {
			fmt.Fprint(c.ErrOrStderr(), line)
			return "", nil
		}
		return line, nil
	})
	return ok, printResult(c, asJSON, answers, err)
}

// waitAwakeJSON is waitAwake, its lines on stderr under --json (stdout holds the answers alone).
func waitAwakeJSON(ctx context.Context, c *cobra.Command, deps fleetDeps, api *fleet.Client, targets []fleet.Target, timeout time.Duration, paused, asJSON bool) error {
	if asJSON {
		c.SetOut(c.ErrOrStderr())
		defer c.SetOut(nil)
	}
	return waitAwake(ctx, c, deps, api, targets, timeout, paused)
}

// writeCommand is what keep-awake, resume, schedule set and schedule clear share: the checks, the targets, the
// confirmation, then act.
type writeCommand struct {
	f       *fleetFlags
	deps    fleetDeps
	targets targetFlags
	dryRun  bool
	yes     bool
	always  bool // ask even for one target (offboard, exclude)
}

func (w *writeCommand) bindShared(cmd *cobra.Command) {
	w.targets.bind(cmd)
	w.bindConfirmation(cmd)
}

// bindConfirmation binds --dry-run and --yes alone (rename takes no --tag).
func (w *writeCommand) bindConfirmation(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&w.dryRun, "dry-run", false, "list the targets and stop")
	yes := "do not ask for confirmation of several targets (needed without a terminal)"
	if w.always {
		yes = "do not ask for confirmation (needed without a terminal)"
	}
	cmd.Flags().BoolVar(&w.yes, "yes", false, yes)
}

// run resolves, confirms and calls act with the targets.
func (w *writeCommand) run(c *cobra.Command, args []string, what, question string, act func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error) error {
	return w.f.run(c, w.deps, func(ctx context.Context, api *fleet.Client) error {
		targets, incomplete, err := w.targets.resolve(ctx, c, api, args)
		if err != nil {
			return err
		}
		// What could not be resolved is said after the action on the others, and makes the exit 1.
		report := func(err error) error {
			if incomplete == "" || ctx.Err() != nil {
				return err
			}
			fmt.Fprintln(c.ErrOrStderr(), incomplete)
			if err == nil {
				return exitCode(1)
			}
			return err
		}
		if len(targets) == 0 { // none resolved, a router did not answer: act on none, so that --json prints its empty shape
			return report(act(ctx, api, nil))
		}
		if done, err := confirmWorkloads(ctx, c, w.deps, targets, what, question, w.always, w.dryRun, w.yes); done || err != nil {
			return report(err)
		}
		return report(act(ctx, api, targets))
	})
}

func newWorkloadsKeepAwake(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var (
		timeout    time.Duration
		dur, until string
		why        string
		wait       bool
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use: "keep-awake (<router>/<workload>... | --tag k=v...) (--for <duration> | --until <time>)", Short: "Keep workloads awake for a while (a pause that does not put them to sleep)",
		SilenceErrors: true,
		Long: "Keep the workloads awake until a time, as a pause that leaves them awake: they are woken when asleep and are\n" +
			"not put to sleep before it ends. --for is a duration up to 168h (7 days; 90m, 2h, 2d); --until is an RFC 3339\n" +
			"time or HH:MM today in the local time zone, refused when already past or more than 7 days away. --wait follows\n" +
			"the workloads every 5 s until all are awake (FAILED ends it with the failure, exit 1; --timeout, exit 1; Ctrl-C,\n" +
			"exit 130: the pause stays, `dormeto workloads resume` ends it). Several targets are confirmed on a terminal.",
		RunE: func(c *cobra.Command, args []string) error {
			now := deps.now()
			end, _, err := endOf(now, dur, until, maxKeepAwake, true)
			if err == nil {
				err = w.targets.check(args, false)
			}
			if err == nil && wait && timeout <= 0 {
				err = usagef("--timeout: %s is not a positive duration", timeout)
			}
			if err != nil {
				return finish(c, err)
			}
			body := struct {
				Reason     string `json:"reason"`
				Until      string `json:"until"`
				KeepAsleep bool   `json:"keepAsleep"`
			}{why, end.UTC().Format("2006-01-02T15:04:05Z"), false}
			return w.run(c, args, "keep awake until "+end.Format("2006-01-02 15:04 MST")+": would act on", "Keep %s awake?",
				func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
					ok, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
						var raw json.RawMessage
						if err := api.Post(ctx, t.Path("/pause"), body, &raw); err != nil {
							return "", nil, err
						}
						return fmt.Sprintf("%s: kept awake until %s\n", t, end.Format("2006-01-02 15:04 MST")), raw, nil
					})
					if !wait || len(ok) == 0 {
						return err
					}
					if werr := waitAwakeJSON(ctx, c, deps, api, ok, timeout, true, asJSON); werr != nil {
						return werr
					}
					return err
				})
		},
	}
	w.bindShared(cmd)
	fl := cmd.Flags()
	fl.StringVar(&dur, "for", "", "how long, a duration up to 168h (90m, 2h, 2d)")
	fl.StringVar(&until, "until", "", "until this time: RFC 3339, or HH:MM today in the local time zone")
	fl.StringVar(&why, "reason", defaultKeepAwakeReason, "the pause's reason, shown in the manager")
	fl.BoolVar(&wait, "wait", false, "wait until the workloads are awake")
	fl.DurationVar(&timeout, "timeout", 15*time.Minute, "with --wait, stop waiting after this long (the pause stays)")
	bindJSON(cmd, &asJSON)
	return cmd
}

// workloadStatus is what GET …/{id} says that --wait reads.
type workloadStatus struct {
	State   string          `json:"state"`
	Failure json.RawMessage `json:"failure"`
}

// failureText is a workload's failure as one line: its message, whatever shape the manager gave it.
func failureText(raw json.RawMessage) string {
	var f struct{ Message string }
	if json.Unmarshal(raw, &f) == nil && f.Message != "" {
		return f.Message
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	return "no reason given"
}

// waitAwake reads each target's status every 5 s until AWAKE (a line on stdout), FAILED (its failure on stderr) or the
// end of --timeout or of the run. A failure, a timeout or an unreadable status is exit 1; with paused (keep-awake), the
// pause stays in each case and is said so.
func waitAwake(ctx context.Context, c *cobra.Command, deps fleetDeps, api *fleet.Client, targets []fleet.Target, timeout time.Duration, paused bool) error {
	followed, cancel := deps.timeout(ctx, timeout)
	defer cancel()
	var (
		mu       sync.Mutex
		pending  = map[fleet.Target]bool{}
		failures int
	)
	for _, t := range targets {
		pending[t] = true
	}
	say := func(w io.Writer, format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, format, a...)
	}
	sem := make(chan struct{}, waitConcurrency)
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs := 0
			for {
				var st workloadStatus
				var err error
				select { // a slot for the request alone, so that every target is polled
				case sem <- struct{}{}:
					err = api.Get(followed, t.Path(""), nil, &st)
					<-sem
				case <-followed.Done():
					return
				}
				switch {
				case followed.Err() != nil:
					return
				case err != nil:
					say(c.ErrOrStderr(), "%s: %s\n", t, err)
					errs++
					var ae *fleet.APIError
					if (errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != http.StatusTooManyRequests) || errs >= 3 {
						say(c.ErrOrStderr(), "%s: not followed any more\n", t)
						mu.Lock()
						delete(pending, t)
						failures++
						mu.Unlock()
						return
					}
				case st.State == "AWAKE":
					say(c.OutOrStdout(), "%s: awake\n", t)
					mu.Lock()
					delete(pending, t)
					mu.Unlock()
					return
				case st.State == "FAILED":
					say(c.ErrOrStderr(), "%s: failed to wake: %s\n", t, failureText(st.Failure))
					mu.Lock()
					delete(pending, t)
					failures++
					mu.Unlock()
					return
				default:
					errs = 0
				}
				if deps.sleep(followed, followEvery) != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if ctx.Err() != nil {
		if paused {
			fmt.Fprintf(c.ErrOrStderr(), "interrupted: the pause stays, `dormeto workloads resume` ends it; not awake: %s\n", targetNames(pending))
		} else {
			fmt.Fprintf(c.ErrOrStderr(), "interrupted; not awake: %s\n", targetNames(pending))
		}
		return nil // the run's signal exit applies
	}
	if followed.Err() != nil && len(pending) > 0 {
		fmt.Fprintf(c.ErrOrStderr(), "timeout of %s reached: not awake: %s\n", timeout, targetNames(pending))
		if paused {
			fmt.Fprintln(c.ErrOrStderr(), "the pause stays: `dormeto workloads resume` ends it")
		}
		return exitCode(1)
	}
	if failures > 0 {
		if paused {
			fmt.Fprintln(c.ErrOrStderr(), "the pause stays on the failed workloads: `dormeto workloads resume` ends it")
		}
		return exitCode(1)
	}
	return nil
}

func targetNames(set map[fleet.Target]bool) string {
	var names []string
	for t := range set {
		names = append(names, t.String())
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

func newWorkloadsResume(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var asJSON bool
	cmd := &cobra.Command{
		Use: "resume (<router>/<workload>... | --tag k=v...)", Short: "End the pause of workloads (a keep-awake included)",
		Long:          "End the pause of the workloads: they follow their schedule and idle rules again.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := w.targets.check(args, false); err != nil {
				return finish(c, err)
			}
			return w.run(c, args, "resume", "Resume %s?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				_, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
					var raw json.RawMessage
					if err := api.Post(ctx, t.Path("/resume"), nil, &raw); err != nil {
						return "", nil, err
					}
					return t.String() + ": resumed\n", raw, nil
				})
				return err
			})
		},
	}
	w.bindShared(cmd)
	bindJSON(cmd, &asJSON)
	return cmd
}

func newWorkloadsSchedule(f *fleetFlags, deps fleetDeps) *cobra.Command {
	cmd := fleetCommand("schedule", "Show, set or clear the weekly awake and nap windows of workloads")
	cmd.AddCommand(newScheduleShow(f, deps), newScheduleSet(f, deps), newScheduleClear(f, deps))
	return cmd
}

func newScheduleShow(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show <router>/<workload>", Short: "Show a workload's schedule and its next events",
		Long: "Show the schedule: the time zone, the windows, the exceptions, the maximum nap and the next events, in the\n" +
			"schedule's time zone. --json prints {schedule, next, maintenanceDue} (schedule null when none, and left\n" +
			"out when the router, before 0.31.0, does not report schedules), which\n" +
			"`dormeto workloads schedule set --file -` accepts back.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := (&targetFlags{}).check(args, true); err != nil {
				return finish(c, err)
			}
			t, _ := fleet.ParseTarget(args[0])
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var st struct {
					Schedule       json.RawMessage   `json:"schedule"`
					Next           []fleet.NextEvent `json:"next"`
					MaintenanceDue string            `json:"maintenanceDue"`
				}
				if err := api.Get(ctx, t.Path(""), nil, &st); err != nil {
					return fmt.Errorf("%s: %w", t, err)
				}
				out := c.OutOrStdout()
				// A status without a schedule key comes from a router before 0.31.0; a null one is "no schedule".
				unreported := len(st.Schedule) == 0
				none := unreported || string(st.Schedule) == "null"
				if asJSON {
					var sched *json.RawMessage
					if !unreported {
						sched = &st.Schedule
					}
					return printJSON(out, struct {
						Schedule       *json.RawMessage  `json:"schedule,omitempty"`
						Next           []fleet.NextEvent `json:"next"`
						MaintenanceDue *string           `json:"maintenanceDue"`
					}{sched, orNext(st.Next), nilIfEmpty(st.MaintenanceDue)})
				}
				if unreported {
					fmt.Fprintf(out, "%s: the router does not report schedules (upgrade it to 0.31.0 or later)\n", t)
					return nil
				}
				if none {
					fmt.Fprintf(out, "%s: no schedule\n", t)
					return nil
				}
				var s fleet.ScheduleView
				if err := json.Unmarshal(st.Schedule, &s); err != nil {
					return fmt.Errorf("decode the schedule: %w", err)
				}
				loc, note := fleet.ZoneOf(s.TimeZone)
				if len(s.Windows) > 0 {
					fmt.Fprintf(out, "%s: time zone %s\n", t, orDefault(s.TimeZone, "UTC"))
				} else {
					fmt.Fprintf(out, "%s: schedule\n", t)
					if s.TimeZone != "" {
						fmt.Fprintf(out, "  time zone %s\n", s.TimeZone)
					}
					if s.MaxNapDays == 0 {
						fmt.Fprintln(out, "  no windows")
					}
				}
				if note != "" {
					fmt.Fprintf(out, "  %s\n", note)
				}
				for _, w := range s.Windows {
					fmt.Fprintf(out, "  %s\n", fleet.WindowText(w.Kind, strings.Join(w.Days, ","), w.Start, w.End, w.Wake))
				}
				if len(s.Exceptions) > 0 {
					fmt.Fprintf(out, "  except %s\n", strings.Join(s.Exceptions, ", "))
				}
				if s.MaxNapDays > 0 {
					fmt.Fprintf(out, "  maximum nap %d %s\n", s.MaxNapDays, fleet.Plural(s.MaxNapDays, "day"))
				}
				fleet.PrintNext(out, loc, st.Next, st.MaintenanceDue)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print {schedule, next, maintenanceDue}, as schedule set --file accepts")
	return cmd
}

func newScheduleSet(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var (
		file   string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use: "set (<router>/<workload>... | --tag k=v...) --file <path or ->", Short: "Set the schedule of workloads from a JSON file",
		Long: "Set the schedule of the workloads from a JSON file (- for standard input), sent as it is: the router checks it\n" +
			"and a refusal is printed as it words it. The file is the schedule, or `schedule show --json` output (its\n" +
			"`schedule` member is sent; a null one is refused). Needs manager 0.16.0 and\n" +
			"routers 0.31.0. The schedule's JSON:\n\n" +
			"  {\"timeZone\": \"Europe/Paris\",\n" +
			"   \"windows\": [{\"kind\": \"awake\", \"days\": [\"mon\",\"tue\",\"wed\",\"thu\",\"fri\"], \"start\": \"08:30\", \"end\": \"19:00\"},\n" +
			"               {\"kind\": \"nap\", \"days\": [\"sat\"], \"start\": \"00:00\", \"end\": \"00:00\", \"wake\": \"header\"}],\n" +
			"   \"exceptions\": [\"2026-12-25\"], \"maxNapDays\": 7}",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			err := w.targets.check(args, false)
			var raw json.RawMessage
			if err == nil {
				raw, err = readScheduleFile(c, file)
			}
			if err != nil {
				return finish(c, err)
			}
			return w.run(c, args, "set the schedule of", "Set the schedule of %s?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				_, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
					var res struct {
						Schedule       json.RawMessage   `json:"schedule"`
						Next           []fleet.NextEvent `json:"next"`
						MaintenanceDue string            `json:"maintenanceDue"`
					}
					var answer json.RawMessage
					if err := api.Put(ctx, t.Path("/schedule"), raw, &answer); err != nil {
						return "", nil, err
					}
					_ = json.Unmarshal(answer, &res)
					// The next events are read in the zone as stored (its canonical name), the file's when the answer has none.
					var s fleet.ScheduleView
					if json.Unmarshal(res.Schedule, &s) != nil || s.TimeZone == "" {
						var sent fleet.ScheduleView
						_ = json.Unmarshal(raw, &sent)
						s.TimeZone = sent.TimeZone
					}
					loc, note := fleet.ZoneOf(s.TimeZone)
					var b strings.Builder
					fmt.Fprintf(&b, "%s: schedule set\n", t)
					if note != "" {
						fmt.Fprintf(&b, "  %s\n", note)
					}
					fleet.PrintNext(&b, loc, res.Next, res.MaintenanceDue)
					return b.String(), answer, nil
				})
				return err
			})
		},
	}
	w.bindShared(cmd)
	bindJSON(cmd, &asJSON)
	cmd.Flags().StringVar(&file, "file", "", "the schedule's JSON file, - for standard input")
	return cmd
}

// readScheduleFile reads the schedule's JSON from path or, for "-", the command's input; only its being a JSON object
// is checked here, the router checks the rest.
func readScheduleFile(c *cobra.Command, path string) (json.RawMessage, error) {
	if path == "" {
		return nil, usagef("--file: the schedule's JSON file is needed (- for standard input)")
	}
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(c.InOrStdin())
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, usagef("--file: %v", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, usagef("--file: not a JSON object: %v", err)
	}
	none := usagef("--file: no schedule in %s; use schedule clear", map[bool]string{true: "standard input", false: path}[path == "-"])
	if probe == nil { // null
		return nil, none
	}
	_, hasNext := probe["next"]
	_, hasDue := probe["maintenanceDue"]
	if _, ok := probe["schedule"]; !ok && (hasNext || hasDue) { // `schedule show --json` of a router that reports none
		return nil, none
	}
	if member, ok := probe["schedule"]; ok { // the shape of `schedule show --json` and of a PUT's answer
		var obj map[string]json.RawMessage
		if json.Unmarshal(member, &obj) != nil || obj == nil {
			return nil, none
		}
		return member, nil
	}
	return json.RawMessage(data), nil
}

func newScheduleClear(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var asJSON bool
	cmd := &cobra.Command{
		Use: "clear (<router>/<workload>... | --tag k=v...)", Short: "Remove the schedule of workloads",
		Long:          "Remove the schedule of the workloads: they follow their idle rules alone.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := w.targets.check(args, false); err != nil {
				return finish(c, err)
			}
			return w.run(c, args, "clear the schedule of", "Clear the schedule of %s?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				_, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
					var raw json.RawMessage
					if err := api.Delete(ctx, t.Path("/schedule"), &raw); err != nil {
						return "", nil, err
					}
					var res struct{ Cleared bool }
					_ = json.Unmarshal(raw, &res)
					if !res.Cleared {
						return t.String() + ": no schedule to clear\n", raw, nil
					}
					return t.String() + ": schedule cleared\n", raw, nil
				})
				return err
			})
		},
	}
	w.bindShared(cmd)
	bindJSON(cmd, &asJSON)
	return cmd
}

func orRaw(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}

func orNext(n []fleet.NextEvent) []fleet.NextEvent {
	if n == nil {
		return []fleet.NextEvent{}
	}
	return n
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// bindJSON binds --json of the workload writes.
func bindJSON(cmd *cobra.Command, asJSON *bool) {
	cmd.Flags().BoolVar(asJSON, "json", false, "print the manager's answers, by <router>/<workload> (lines and failures go to stderr)")
}
