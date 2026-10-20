package main

// What the manager commands share: the operations they start, followed until they end (their steps printed as they
// change); the confirmation of the things they act on; durations with days, and the end of a pause given by --for or
// --until.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

// waitFlags say whether and how long the operations started are followed: --no-wait and --timeout.
type waitFlags struct {
	noWait  bool
	timeout time.Duration
}

func (w *waitFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.BoolVar(&w.noWait, "no-wait", false, "print the operation's id instead of following it (dormeto operations watch <id> follows it)")
	f.DurationVar(&w.timeout, "timeout", 60*time.Minute, "stop following after this long (the operation goes on in the manager)")
}

// check refuses, before any call, a --timeout that is not positive.
func (w *waitFlags) check() error {
	if w.timeout <= 0 {
		return usagef("--timeout: %s is not a positive duration", w.timeout)
	}
	return nil
}

// opItem is an operation for runOperations: label names its target in what is printed, start starts it, get reads it
// (fleet.GetOperation, or fleet.ListedOperation for the manager's update).
type opItem struct {
	label string
	start func(context.Context) (fleet.Operation, error)
	get   func(context.Context, string) (fleet.Operation, error)
}

// opResult is what became of an item: the operation's record as last read (started), whether it failed (said when it
// happened), and what is said of it when the following stopped first (unfinished).
type opResult struct {
	op         fleet.Operation
	started    bool
	failed     bool
	unfinished string
}

// runOperations starts the items' operations, at most concurrency at once, each followed until it ends unless --no-wait.
// On stdout (stderr with the command's --json): each start, "started <type> <id> for <label>", and each success. On
// stderr: the steps as they change, each failure (a refused start with the manager's message: the others go on), and,
// when --timeout (no limit when not positive) or a signal stops the following, each operation still running in the
// manager with how to follow it. It gives the records of the operations started, in the items' order, as last read; the
// error is exit 1 when one failed or was not followed to its end (the run's signal exit wins).
func runOperations(ctx context.Context, c *cobra.Command, deps fleetDeps, items []opItem, concurrency int, w waitFlags) ([]fleet.Operation, error) {
	o := opOutput{info: c.OutOrStdout(), errs: c.ErrOrStderr(), mu: &sync.Mutex{}, named: len(items) > 1}
	if asJSON, _ := c.Flags().GetBool("json"); asJSON {
		o.info = c.ErrOrStderr()
	}
	followed, cancel := ctx, context.CancelFunc(func() {})
	if w.timeout > 0 { // none is no limit: the context alone ends the following
		followed, cancel = deps.timeout(ctx, w.timeout)
	}
	defer cancel()
	results := make([]opResult, len(items))
	slots := make(chan struct{}, max(concurrency, 1))
	var wg sync.WaitGroup
	for i, it := range items {
		select {
		case <-followed.Done():
		case slots <- struct{}{}:
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				results[i] = runOperation(followed, deps, it, w, o)
			}()
			continue
		}
		results[i] = opResult{unfinished: "not started"}
	}
	wg.Wait()
	var (
		ops                []fleet.Operation
		failed, unfinished bool
	)
	for _, r := range results {
		if r.started {
			ops = append(ops, r.op)
		}
		failed = failed || r.failed
		unfinished = unfinished || r.unfinished != ""
	}
	if unfinished {
		cause := fmt.Sprintf("timeout of %s reached", w.timeout)
		if ctx.Err() != nil { // a signal or the caller's end, not --timeout
			cause = "interrupted"
		}
		fmt.Fprintln(o.errs, cause)
		for i, r := range results {
			if r.unfinished != "" {
				fmt.Fprintf(o.errs, "%s: %s\n", items[i].label, r.unfinished)
			}
		}
	}
	if failed || unfinished {
		return ops, exitCode(1)
	}
	return ops, nil
}

// opOutput is where runOperations prints, one line at a time; named step lines carry their operation's label.
type opOutput struct {
	info, errs io.Writer
	mu         *sync.Mutex
	named      bool
}

func (o opOutput) say(w io.Writer, format string, a ...any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Fprintf(w, format, a...)
}

// runOperation starts one item's operation and follows it to its end, or until ctx ends.
func runOperation(ctx context.Context, deps fleetDeps, it opItem, w waitFlags, o opOutput) opResult {
	fail := func(format string, a ...any) opResult {
		o.say(o.errs, "✗ %s: %s\n", it.label, fmt.Sprintf(format, a...))
		return opResult{failed: true}
	}
	if ctx.Err() != nil { // the slot was given as the run was cut short
		return opResult{unfinished: "not started"}
	}
	began := deps.now()
	op, err := it.start(ctx)
	switch {
	case err == nil && op.ID == "": // answered 2xx without naming the operation: it cannot be followed
		return fail("%s", mayHaveStarted(errors.New("the answer names no operation")))
	case err == nil:
	case errors.Is(err, fleet.ErrNotSent) && ctx.Err() != nil: // nothing was sent, and the run is cut short
		return opResult{unfinished: "not started"}
	case errors.Is(err, fleet.ErrNotSent):
		return fail("not started: %s", err)
	case ctx.Err() != nil: // cut short in the call: the manager may or may not have started it
		return opResult{unfinished: "interrupted while starting: " + mayHaveStarted(err)}
	case fleet.MayHaveStarted(err):
		return fail("%s", mayHaveStarted(err))
	default:
		return fail("%s", err)
	}
	res := opResult{op: op, started: true}
	o.say(o.info, "started %s %s for %s\n", op.Type, op.ID, it.label)
	if w.noWait {
		o.say(o.info, "follow it: dormeto operations watch %s\n", op.ID)
		return res
	}
	if op.Status != "succeeded" && op.Status != "failed" {
		printed := map[int]string{}
		last, err := fleet.Follow(ctx, it.get, op.ID, fleet.FollowOptions{Poll: followEvery, Sleep: deps.sleep, Progress: func(op fleet.Operation) {
			for _, line := range stepLines(printed, op) {
				if o.named {
					line = it.label + ": " + line
				}
				o.say(o.errs, "  %s\n", line)
			}
		}})
		if last.ID != "" {
			res.op = last
		}
		if err != nil {
			if ctx.Err() != nil { // the run stopped following: the operation goes on in the manager
				res.unfinished = "still running in the manager: dormeto operations watch " + op.ID
				return res
			}
			o.say(o.errs, "✗ %s: cannot read the operation: %s (operation %s)\n", it.label, err, op.ID)
			res.failed = true
			return res
		}
	}
	op = res.op
	switch op.Status {
	case "succeeded":
		o.say(o.info, "✓ %s: %s succeeded in %s\n", it.label, op.Type, took(op, began, deps.now()))
	case "failed":
		o.say(o.errs, "✗ %s: %s (operation %s)\n", it.label, failedText(op), op.ID)
		res.failed = true
	default:
		o.say(o.errs, "✗ %s: the operation is %q (operation %s)\n", it.label, op.Status, op.ID)
		res.failed = true
	}
	return res
}

// mayHaveStarted is how a start whose outcome is not known is reported.
func mayHaveStarted(err error) string {
	return "the manager may have started it: dormeto operations list --running (" + err.Error() + ")"
}

// stepLines are the lines of the steps whose status changed since printed (a step not printed yet counts as pending),
// recorded there: "… name" running, "✓ name (12s)" done, "– name (skipped)", "✗ name: detail" failed.
func stepLines(printed map[int]string, op fleet.Operation) []string {
	var lines []string
	for i, s := range op.Steps {
		before, ok := printed[i]
		if !ok {
			before = "pending"
		}
		if s.Status == before {
			continue
		}
		printed[i] = s.Status
		switch s.Status {
		case "running":
			lines = append(lines, "… "+s.Name)
		case "done":
			line := "✓ " + s.Name
			if d, ok := between(s.StartedAt, s.FinishedAt); ok {
				line += " (" + d.String() + ")"
			}
			lines = append(lines, line)
		case "skipped":
			lines = append(lines, "– "+s.Name+" (skipped)")
		case "failed":
			line := "✗ " + s.Name
			if s.Detail != "" {
				line += ": " + s.Detail
			}
			lines = append(lines, line)
		}
	}
	return lines
}

// between is the time from one RFC 3339 instant to another, to the second; false when either is not a time.
func between(from, to string) (time.Duration, bool) {
	a, err1 := time.Parse(time.RFC3339, from)
	b, err2 := time.Parse(time.RFC3339, to)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return b.Sub(a).Round(time.Second), true
}

// took is how long the operation ran: from its creation to its end as recorded, else as the CLI saw it.
func took(op fleet.Operation, began, now time.Time) time.Duration {
	if d, ok := between(op.CreatedAt, op.FinishedAt); ok {
		return d
	}
	return now.Sub(began).Round(time.Second)
}

// failedText is a failed operation as one line: "<type> failed at <step>: <detail>", the detail the step's, else the
// operation's error.
func failedText(op fleet.Operation) string {
	s := op.Type + " failed"
	detail := op.Error
	if st := op.Current(); st != nil && st.Status == "failed" {
		s += " at " + st.Name
		if st.Detail != "" {
			detail = st.Detail
		}
	}
	if detail != "" {
		s += ": " + detail
	}
	return s
}

// confirmNames is asked before acting on names (each a noun: "router", "workload"): on --dry-run it lists them instead
// (done: true); else, on a terminal, question is asked when there are several names, or always (done: true when it is
// declined: nothing is sent); --yes skips the question, and is needed without a terminal. question is asked as it is
// written: it names the targets and what happens.
func confirmNames(ctx context.Context, c *cobra.Command, deps fleetDeps, names []string, noun, question string, always, dryRun, yes bool) (done bool, err error) {
	out := c.OutOrStdout()
	n := len(names)
	if dryRun {
		listDryRun(out, fmt.Sprintf("%d %s", n, fleet.Plural(n, noun)), names)
		return true, nil
	}
	if (n < 2 && !always) || yes {
		return false, nil
	}
	if !deps.terminal(c.InOrStdin()) {
		return false, usagef("confirmation needed: pass --yes to act on %d %s without a terminal", n, fleet.Plural(n, noun))
	}
	ok, err := deps.confirm(ctx, c.InOrStdin(), out, question)
	if err != nil {
		return false, err
	}
	if !ok {
		fmt.Fprintln(out, "not confirmed: nothing sent")
	}
	return !ok, nil
}

// listDryRun prints what a dry run would act on: "dry run: <what>, nothing sent", then each name.
func listDryRun(out io.Writer, what string, names []string) {
	fmt.Fprintf(out, "dry run: %s, nothing sent\n", what)
	for _, n := range names {
		fmt.Fprintf(out, "  %s\n", n)
	}
}

// daysFirst is a duration that begins with whole days: 3d, 1d12h.
var daysFirst = regexp.MustCompile(`^([0-9]+)d(.*)$`)

// parseFor reads a duration: Go's (90m, 2h), or whole days first (3d, 1d12h); a negative one is refused.
func parseFor(s string) (time.Duration, error) {
	bad := fmt.Errorf("%q is not a duration (90m, 2h, 3d)", s)
	var days time.Duration
	rest := s
	if m := daysFirst.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n > int64(math.MaxInt64/(24*time.Hour)) {
			return 0, bad
		}
		days, rest = time.Duration(n)*24*time.Hour, m[2]
		if rest == "" {
			return days, nil
		}
		if rest[0] < '0' || rest[0] > '9' { // no sign after the days
			return 0, bad
		}
	}
	d, err := time.ParseDuration(rest)
	if err != nil || d < 0 || d > math.MaxInt64-days {
		return 0, bad
	}
	return days + d, nil
}

// endOf is the end given by --for (parseFor) or --until (RFC 3339, or HH:MM today in now's zone), refused (a usage
// error) when not within (now, now + max], when both are given, or when neither is and one is required; set is false
// when neither is given.
func endOf(now time.Time, forText, until string, max time.Duration, required bool) (end time.Time, set bool, err error) {
	switch {
	case forText == "" && until == "" && !required:
		return time.Time{}, false, nil
	case required && (forText != "") == (until != ""):
		return time.Time{}, false, usagef("give exactly one of --for and --until")
	case forText != "" && until != "":
		return time.Time{}, false, usagef("give --for or --until, not both")
	}
	if forText != "" {
		d, err := parseFor(forText)
		if err != nil || d < time.Second || d > max {
			return time.Time{}, false, usagef("--for: %q is not a duration (90m, 2h, 3d) from 1s up to %s", forText, limitText(max))
		}
		return now.Add(d), true, nil
	}
	if t, err := time.Parse(time.RFC3339, until); err == nil {
		end = t
	} else if hm, err := time.Parse("15:04", until); err == nil {
		end = time.Date(now.Year(), now.Month(), now.Day(), hm.Hour(), hm.Minute(), 0, 0, now.Location())
	} else {
		return time.Time{}, false, usagef("--until: %q is neither an RFC 3339 time nor HH:MM", until)
	}
	if !end.After(now) {
		return time.Time{}, false, usagef("--until: %s is already past (it is %s now)", until, now.Format("15:04 MST"))
	}
	if end.Sub(now) > max {
		return time.Time{}, false, usagef("--until: %s is more than %s away", until, limitText(max))
	}
	return end, true, nil
}

// limitText is a limit as the messages write it: 168h (7 days).
func limitText(d time.Duration) string {
	day := 24 * time.Hour
	if d%day != 0 {
		return d.String()
	}
	days := int(d / day)
	return fmt.Sprintf("%dh (%d %s)", int(d/time.Hour), days, fleet.Plural(days, "day"))
}
