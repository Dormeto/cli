package main

// `dormeto aws workloads keep-awake` and `resume`: pauses of workloads through the
// manager's /iam routes. Targets are <router>/<workload> arguments or --tag (every workload that carries them); more
// than one target is confirmed on a terminal (--yes without one; --dry-run says each request). Exit codes as the fleet commands:
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

// confirmWorkloads, on --dry-run, asks nothing: the writes that follow are said, not sent (sayRequests). Else it asks on a
// terminal when there are several targets, or always (the destructive commands), and --yes is absent (done: true when
// declined), the targets listed first; it refuses without a terminal. It is confirmNames with question taking the count
// in words ("Offboard %s?": "Offboard 1 workload?").
func confirmWorkloads(ctx context.Context, c *cobra.Command, deps fleetDeps, api *fleet.Client, targets []fleet.Target, question string, always, dryRun, yes bool) (done bool, err error) {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.String()
	}
	if dryRun {
		sayRequests(c, api, names)
		return false, nil
	}
	count := fmt.Sprintf("%d %s", len(names), fleet.Plural(len(names), "workload"))
	return ask(ctx, c, deps, names, "workload", fmt.Sprintf(question, count), always, yes, true)
}

// applyAll runs do for each target in turn: its line goes to stdout, its failure to stderr as "target: message"; the
// others go on. A failure makes the exit 1. It returns the targets that succeeded. Under a dry run, a write do said
// (fleet.ErrDryRun) is neither a line nor a failure, and a run without a failure ends as the dry run it is (ErrDryRun).
func applyAll(ctx context.Context, c *cobra.Command, targets []fleet.Target, do func(fleet.Target) (string, error)) ([]fleet.Target, error) {
	var ok []fleet.Target
	failed := 0
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return ok, err
		}
		line, err := do(t)
		if errors.Is(err, fleet.ErrDryRun) { // said, not sent
			continue
		}
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
	switch {
	case failed > 0:
		return ok, exitCode(1)
	case dryRunning(c):
		return ok, fleet.ErrDryRun
	}
	return ok, nil
}

// applyAnswers is applyAll for commands with --json: do also gives the API's answer for the target. Without asJSON the
// lines go to stdout as applyAll prints them; with it they go to stderr, and the answers of the targets that succeeded
// are printed on stdout, by target ({} for none), even when the exit is 1; a dry run's --json is the dry run's
// (fleetFlags.run).
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
	return ok, printResult(c, asJSON && !dryRunning(c), answers, err)
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
	// wording, when set, words what is asked after the targets are known (the clouds of their routers).
	wording func(ctx context.Context, api *fleet.Client, targets []fleet.Target, question string) string
}

func (w *writeCommand) bindShared(cmd *cobra.Command) {
	w.targets.bind(cmd)
	w.bindConfirmation(cmd)
}

// bindConfirmation binds --dry-run and --yes alone (rename takes no --tag).
func (w *writeCommand) bindConfirmation(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&w.dryRun, "dry-run", false, dryRunUsage)
	yes := yesSeveral
	if w.always {
		yes = yesAlways
	}
	cmd.Flags().BoolVar(&w.yes, "yes", false, yes)
}

// run resolves, confirms and calls act with the targets; under --dry-run act's writes are said, not sent.
func (w *writeCommand) run(c *cobra.Command, args []string, question string, act func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error) error {
	return w.f.run(c, w.deps, func(ctx context.Context, api *fleet.Client) error {
		targets, incomplete, err := w.targets.resolve(ctx, c, api, args)
		if err != nil {
			return err
		}
		// What could not be resolved is said after the action on the others, and makes the exit 1 (a dry run's too, its
		// --json printed first).
		report := func(err error) error {
			if incomplete == "" || ctx.Err() != nil {
				return err
			}
			if dry := api.DryRun(); dry != nil && errors.Is(err, fleet.ErrDryRun) {
				err = endDryRun(c, dry)
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
		if w.wording != nil && !w.dryRun && !w.yes { // the routers are read only to word a question
			question = w.wording(ctx, api, targets, question)
		}
		if done, err := confirmWorkloads(ctx, c, w.deps, api, targets, question, w.always, w.dryRun, w.yes); done || err != nil {
			return report(err)
		}
		return report(act(ctx, api, targets))
	})
}

func newWorkloadsKeepAwake(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var (
		dur, until string
		why        string
		wake       wakeWait
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use: "keep-awake (<router>/<workload>... | --tag k=v...) (--for <duration> | --until <time>)", Short: "Keep workloads awake for a while (a pause that does not put them to sleep)",
		SilenceErrors: true,
		Long: "Keep the workloads awake until a time, as a pause that leaves them awake: they are woken when asleep and are\n" +
			"not put to sleep before it ends. --for is a duration up to 168h (7 days; 90m, 2h, 2d); --until is an RFC 3339\n" +
			"time or HH:MM today in the local time zone, refused when already past or more than 7 days away. --wait follows\n" +
			"the workloads every 5 s until all are awake (FAILED ends it with the failure, exit 1; --timeout, exit 1; Ctrl-C,\n" +
			"exit 130: the pause stays, `dormeto aws workloads resume` ends it). Several targets are confirmed on a terminal.",
		RunE: func(c *cobra.Command, args []string) error {
			now := deps.now()
			end, _, err := endOf(now, dur, until, maxKeepAwake, true)
			if err == nil {
				err = w.targets.check(args, false)
			}
			if err == nil {
				err = wake.check()
			}
			if err == nil { // a pause's, as every pause's: 1 to 256 characters
				why, err = pauseReason(why)
			}
			if err != nil {
				return finish(c, err)
			}
			body := struct {
				Reason     string `json:"reason"`
				Until      string `json:"until"`
				KeepAsleep bool   `json:"keepAsleep"`
			}{why, end.UTC().Format("2006-01-02T15:04:05Z"), false}
			return w.run(c, args, "Keep %s awake?",
				func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
					ok, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
						var raw json.RawMessage
						if err := api.Post(ctx, t.Path("/pause"), body, &raw); err != nil {
							return "", nil, err
						}
						return fmt.Sprintf("%s: kept awake until %s\n", t, end.Format("2006-01-02 15:04 MST")), raw, nil
					})
					if !wake.wait || len(ok) == 0 {
						return err
					}
					if werr := waitAwakeJSON(ctx, c, deps, api, ok, wake.timeout, true, asJSON); werr != nil {
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
	fl.StringVar(&why, "reason", defaultKeepAwakeReason, "the pause's reason, 1 to 256 characters, shown in the manager")
	wake.bind(cmd, "wait until the workloads are awake", "with --wait, stop waiting after this long (the pause stays)")
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
			fmt.Fprintf(c.ErrOrStderr(), "interrupted: the pause stays, `dormeto aws workloads resume` ends it; not awake: %s\n", targetNames(pending))
		} else {
			fmt.Fprintf(c.ErrOrStderr(), "interrupted; not awake: %s\n", targetNames(pending))
		}
		return nil // the run's signal exit applies
	}
	if followed.Err() != nil && len(pending) > 0 {
		fmt.Fprintf(c.ErrOrStderr(), "timeout of %s reached: not awake: %s\n", timeout, targetNames(pending))
		if paused {
			fmt.Fprintln(c.ErrOrStderr(), "the pause stays: `dormeto aws workloads resume` ends it")
		}
		return exitCode(1)
	}
	if failures > 0 {
		if paused {
			fmt.Fprintln(c.ErrOrStderr(), "the pause stays on the failed workloads: `dormeto aws workloads resume` ends it")
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
			return w.run(c, args, "Resume %s?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
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

func orRaw(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}

// bindJSON binds --json of the workload writes.
func bindJSON(cmd *cobra.Command, asJSON *bool) {
	cmd.Flags().BoolVar(asJSON, "json", false, "print the manager's answers, by <router>/<workload> (lines and failures go to stderr)")
}
