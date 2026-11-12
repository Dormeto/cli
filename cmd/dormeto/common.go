package main

// What the manager commands share: the operations they start, followed until they end (their steps printed as they
// change); the confirmation of the things they act on; durations with days, and the end of a pause given by --for or
// --until.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/output"
)

// waitFlags say whether and how long the operations started are followed: --no-wait and --timeout.
type waitFlags struct {
	noWait  bool
	timeout time.Duration
}

func (w *waitFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.BoolVar(&w.noWait, "no-wait", false, "print the operation's id instead of following it (dormeto aws operations watch <id> follows it)")
	f.DurationVar(&w.timeout, "timeout", 60*time.Minute, "stop following after this long (the operation goes on in the manager)")
}

// check refuses, before any call, a --timeout that is not positive.
func (w *waitFlags) check() error { return positiveTimeout(w.timeout) }

// positiveTimeout refuses a --timeout that is not positive (a usage error), the operations' and a wake's.
func positiveTimeout(d time.Duration) error {
	if d <= 0 {
		return usagef("--timeout: %s is not a positive duration", d)
	}
	return nil
}

// The flags both subtrees have, defined once: a pause's, a wake's --wait and --timeout, --yes, the list's filters, a level's
// --join-minutes and where the manager is. Each command keeps the help it released, given where it binds them.

// listFilters are `workloads list`'s --state, --tag and --search.
type listFilters struct {
	states, tags []string
	search       string
}

func (l *listFilters) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringSliceVar(&l.states, "state", nil, "only the workloads in these states (repeatable or comma-separated; case-insensitive)")
	fl.StringArrayVar(&l.tags, "tag", nil, "only the workloads with this Dormeto tag, key=value (repeatable: all must match)")
	fl.StringVar(&l.search, "search", "", "only the workloads whose name or id contains this text")
}

// joinMinutesHelp is a router level's --join-minutes, AWS's adding what inherit follows.
const joinMinutesHelp = "the largest gap that joins two windows, 0 to 120, a multiple of 5, or inherit"

// bindManager binds where the manager is beyond the AWS profile and region: --env and --api-url.
func bindManager(fl *pflag.FlagSet, env, apiURL *string) {
	fl.StringVar(env, "env", "prod", "the manager's environment")
	fl.StringVar(apiURL, "api-url", "",
		"the manager API's execute-api endpoint (not the dashboard's URL), skipping the lookup by name (needs no apigateway:GET)")
}

// The help of --yes: a command that asks for several targets (several routers), and one that always asks.
const (
	yesSeveral        = "do not ask for confirmation of several targets (needed without a terminal)"
	yesSeveralRouters = "do not ask for confirmation of several routers (needed without a terminal)"
	yesAlways         = "do not ask for confirmation (needed without a terminal)"
)

// pauseFlags are a pause's --reason, --for, --until and --keep-asleep, a workload's or a router's.
type pauseFlags struct {
	reason, dur, until string
	keepAsleep         bool
}

// pauseHelp is a command's help of --reason, --for and --keep-asleep (--until's is every pause's).
type pauseHelp struct{ reason, dur, keepAsleep string }

// The help of a pause's --reason that the command needs.
const reasonRequired = "why, 1 to 256 characters, shown in the manager (required)"

func (p *pauseFlags) bind(cmd *cobra.Command, h pauseHelp) {
	fl := cmd.Flags()
	fl.StringVar(&p.reason, "reason", "", h.reason)
	fl.StringVar(&p.dur, "for", "", h.dur)
	fl.StringVar(&p.until, "until", "", "until this time: RFC 3339, or HH:MM today in the local time zone")
	fl.BoolVar(&p.keepAsleep, "keep-asleep", false, h.keepAsleep)
}

// end is the pause's end, --for or --until within 366 days (endOf); set is false for neither.
func (p *pauseFlags) end(now time.Time) (end time.Time, set bool, err error) {
	return endOf(now, p.dur, p.until, maxPause, false)
}

// wakeWait is a wake's --wait and --timeout: follow the workloads until they are awake, for at most that long.
type wakeWait struct {
	wait    bool
	timeout time.Duration
}

func (w *wakeWait) bind(cmd *cobra.Command, waitHelp, timeoutHelp string) {
	cmd.Flags().BoolVar(&w.wait, "wait", false, waitHelp)
	cmd.Flags().DurationVar(&w.timeout, "timeout", 15*time.Minute, timeoutHelp)
}

// check refuses, before any call, a --timeout that is not positive with --wait.
func (w *wakeWait) check() error {
	if !w.wait {
		return nil
	}
	return positiveTimeout(w.timeout)
}

// opItem is an operation for runOperations: label names its target in what is printed, start starts it, get reads it
// (fleet.GetOperation, or fleet.ListedOperation for the manager's update). A patient one has every failed read waited
// out until --timeout, each new error said once on stderr (the manager's apply, which replaces the API).
type opItem struct {
	label   string
	start   func(context.Context) (fleet.Operation, error)
	get     func(context.Context, string) (fleet.Operation, error)
	patient bool
}

// opResult is what became of an item: the operation's record as last read (started), whether it failed (said when it
// happened), and what is said of it when the following stopped first (unfinished).
type opResult struct {
	op         fleet.Operation
	started    bool
	failed     bool
	unfinished string
	dryRun     bool // its start was a dry run's: said, not sent
}

// runOperations starts the items' operations, at most concurrency at once, each followed until it ends unless --no-wait.
// On stdout (stderr with the command's --json): each start, "started <type> <id> for <label>", and each success. On
// stderr: the steps as they change, each failure (a refused start with the manager's message: the others go on), and,
// when --timeout (no limit when not positive) or a signal stops the following, each operation still running in the
// manager with how to follow it. It gives the records of the operations started, in the items' order, as last read; the
// error is exit 1 when one failed or was not followed to its end (the run's signal exit wins). A dry run starts them one
// at a time, so that it says its requests in the items' order.
func runOperations(ctx context.Context, c *cobra.Command, deps fleetDeps, items []opItem, concurrency int, w waitFlags) ([]fleet.Operation, error) {
	if dryRunning(c) {
		concurrency = 1
	}
	o := opOutput{info: textOut(c), errs: c.ErrOrStderr(), mu: &sync.Mutex{}, named: len(items) > 1}
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
		ops                        []fleet.Operation
		failed, unfinished, dryRun bool
	)
	for _, r := range results {
		if r.started {
			ops = append(ops, r.op)
		}
		failed = failed || r.failed
		unfinished = unfinished || r.unfinished != ""
		dryRun = dryRun || r.dryRun
	}
	if dryRun && !failed && !unfinished {
		return ops, fleet.ErrDryRun
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
	case errors.Is(err, fleet.ErrDryRun): // said, not sent
		return opResult{dryRun: true}
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
		o.say(o.info, "follow it: dormeto aws operations watch %s\n", op.ID)
		return res
	}
	if op.Status != "succeeded" && op.Status != "failed" {
		printed := map[int]string{}
		named := func(line string) string {
			if o.named {
				return it.label + ": " + line
			}
			return line
		}
		follow := fleet.FollowOptions{Poll: followEvery, Sleep: deps.sleep, Progress: func(op fleet.Operation) {
			for _, line := range stepLines(printed, op) {
				o.say(o.errs, "  %s\n", named(line))
			}
		}}
		if it.patient {
			follow.WaitOut = func(err error) {
				o.say(o.errs, "  %s\n", named(fmt.Sprintf("… the manager does not answer (%s), waiting", err)))
			}
		}
		last, err := fleet.Follow(ctx, it.get, op.ID, follow)
		if last.ID != "" {
			res.op = last
		}
		if err != nil {
			if ctx.Err() != nil { // the run stopped following: the operation goes on in the manager
				res.unfinished = "still running in the manager: dormeto aws operations watch " + op.ID
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
	return "the manager may have started it: dormeto aws operations list --running (" + err.Error() + ")"
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

// textOut is where a command says what it does and asks its questions: stdout, or stderr under its --json, whose
// stdout holds the JSON alone (output.Words).
func textOut(c *cobra.Command) io.Writer {
	return output.Words(jsonFlag(c), c.OutOrStdout(), c.ErrOrStderr())
}

// jsonFlag tells whether the command runs with --json (false for a command without it).
func jsonFlag(c *cobra.Command) bool {
	asJSON, _ := c.Flags().GetBool("json")
	return asJSON
}

// confirmNames is asked before acting on names (each a noun: "router", "workload"): on --dry-run it asks nothing and the
// writes that follow are said, not sent (sayRequests); else it asks question (see ask). question is asked as it is
// written: it names the targets and what happens.
func confirmNames(ctx context.Context, c *cobra.Command, deps fleetDeps, api *fleet.Client, names []string, noun, question string, always, dryRun, yes bool) (done bool, err error) {
	if dryRun {
		sayRequests(c, api, names)
		return false, nil
	}
	return ask(ctx, c, deps, names, noun, question, always, yes, false)
}

// sayRequests makes the rest of a command's --dry-run its client's, as the client-level dry runs and gcp's are: each write
// it goes on to make is said, "dry run: <target>: would <method> <path>, nothing sent" and its body on the next line
// (fleet.DryRunText), on textOut, and kept, none sent; its reads are sent. fleetFlags.run then ends it, exit 0, --json
// printing the released {"dryRun": true, "targets": names}.
func sayRequests(c *cobra.Command, api *fleet.Client, names []string) {
	dry := api.DryRun()
	if dry == nil {
		dry = &fleet.DryRun{Out: textOut(c)}
		api.SetDryRun(dry)
	}
	dry.Targets = output.EmptyIfNil(names)
}

// dryRunning tells whether the command runs with --dry-run (false for a command without it).
func dryRunning(c *cobra.Command) bool {
	dry, _ := c.Flags().GetBool("dry-run")
	return dry
}

// ask asks question on a terminal when there are several names, or always (done: true when it is declined: nothing is
// sent), the names listed first when listed; --yes skips it, and is needed without a terminal (a usage error). It is
// asked on textOut, and under --json a declined question prints {"confirmed":false,"targets":[…]} on stdout
// (output.Asker).
func ask(ctx context.Context, c *cobra.Command, deps fleetDeps, names []string, noun, question string, always, yes, listed bool) (done bool, err error) {
	// A client's dry run asks nothing: it sends nothing, and says what it would send.
	if clientDryRun(c) {
		return false, nil
	}
	in := c.InOrStdin()
	done, err = output.Asker{JSON: jsonFlag(c), Yes: yes, Stdout: c.OutOrStdout(), Stderr: c.ErrOrStderr(),
		Terminal: func() bool { return deps.terminal(in) },
		Confirm:  func(words io.Writer, q string) (bool, error) { return deps.confirm(ctx, in, words, q) },
	}.Ask(output.Question{Names: names, Noun: noun, Text: question, Always: always, Listed: listed})
	if errors.Is(err, output.ErrNeedsYes) {
		return false, usageError{err}
	}
	return done, err
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

// checkLimit refuses a negative --limit; 0 is all.
func checkLimit(limit int) error {
	if limit < 0 {
		return usagef("--limit: %d is negative (0 lists all)", limit)
	}
	return nil
}

// regionName is an AWS region as the manager accepts one (eu-west-1).
var regionName = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`)

// printStarted prints, under --json, the operations that started (whatever err is) as an array, [] when none did: the
// shape of a command that takes several targets. Then it returns err.
func printStarted(c *cobra.Command, asJSON bool, ops []fleet.Operation, err error) error {
	if ops == nil {
		ops = []fleet.Operation{}
	}
	return printResult(c, asJSON, ops, err)
}

// printStartedOne is printStarted for a command that takes one target: its operation as an object, {} when it did not
// start.
func printStartedOne(c *cobra.Command, asJSON bool, ops []fleet.Operation, err error) error {
	var v any = struct{}{}
	if len(ops) > 0 {
		v = ops[0]
	}
	return printResult(c, asJSON, v, err)
}

// printResult prints v under --json, whatever err is, then returns err (or the printing's error when err is nil). A dry run
// that stopped at its write prints its own JSON (fleetFlags.run).
func printResult(c *cobra.Command, asJSON bool, v any, err error) error {
	if !asJSON || errors.Is(err, fleet.ErrDryRun) {
		return err
	}
	if perr := output.JSON(c.OutOrStdout(), v); perr != nil && err == nil {
		err = perr
	}
	return err
}

// answer is what the user routes say back, of which each prints its part.
type answer struct {
	Email        string   `json:"email"`
	Role         string   `json:"role"`
	Groups       []string `json:"groups"`
	Invitation   string   `json:"invitation"`
	TokenDeleted bool     `json:"tokenDeleted"`
}

// callAPI sends a write to path with body (nil for none) and returns the manager's answer, raw for --json.
func callAPI(ctx context.Context, api *fleet.Client, method, path string, body any) (json.RawMessage, error) {
	var raw json.RawMessage
	var err error
	switch method {
	case http.MethodPost:
		err = api.Post(ctx, path, body, &raw)
	case http.MethodPut:
		err = api.Put(ctx, path, body, &raw)
	case http.MethodDelete:
		err = api.Delete(ctx, path, &raw)
	default:
		err = fmt.Errorf("callAPI: unsupported method %q", method)
	}
	return raw, err
}

// printAnswer prints the manager's answer under --json, else text(its decoded form).
func printAnswer(c *cobra.Command, asJSON bool, raw json.RawMessage, text func(answer) string) error {
	if asJSON {
		if len(raw) == 0 {
			raw = json.RawMessage("{}")
		}
		return output.JSON(c.OutOrStdout(), raw)
	}
	var a answer
	_ = json.Unmarshal(raw, &a)
	fmt.Fprintln(c.OutOrStdout(), text(a))
	return nil
}

// argCount checks the number of arguments (max -1: any): a wrong one is a usage error naming what is expected.
func argCount(min, max int, want string) cobra.PositionalArgs {
	return func(c *cobra.Command, args []string) error {
		if len(args) < min || (max >= 0 && len(args) > max) {
			fmt.Fprintf(c.ErrOrStderr(), "%s takes %s\n", c.CommandPath(), want)
			return exitCode(2)
		}
		return nil
	}
}
