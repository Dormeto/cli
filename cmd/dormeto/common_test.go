package main

// What the manager commands share, against the fake manager API: operations started and followed (how many at once,
// refusals, --no-wait, --timeout, --json), the confirmation of named things, durations with days.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

// opsAPI is a fake manager's operations: each one started runs two reads, a step changing at each, then ends succeeded
// (or failed with end, or never with stay). It counts those running at once.
type opsAPI struct {
	mu      sync.Mutex
	refuse  map[string]string // router -> the 409's message
	broken  string            // the router whose start answers 502
	stay    bool              // the operations never end
	end     string            // "failed": they end failed
	barrier int               // the reads wait until this many are started
	ready   chan struct{}
	started []string
	reads   map[string]int
	running int
	peak    int
	gets    int
}

// record is the operation of router after n reads.
func (a *opsAPI) record(router string, n int) string {
	steps := `[{"name":"find router","status":"pending"},{"name":"install router","status":"pending"}]`
	status, end := "running", ""
	switch {
	case n == 1:
		steps = `[{"name":"find router","status":"running","startedAt":"2026-10-19T08:00:00.000Z"},{"name":"install router","status":"pending"}]`
	case n == 2 || (n > 2 && a.stay):
		steps = `[{"name":"find router","status":"done","startedAt":"2026-10-19T08:00:00.000Z","finishedAt":"2026-10-19T08:00:12.000Z"},` +
			`{"name":"install router","status":"running","startedAt":"2026-10-19T08:00:12.000Z"}]`
	case n > 2 && a.end == "failed":
		status, end = "failed", `,"error":"install router: role missing","finishedAt":"2026-10-19T08:01:00.000Z"`
		steps = `[{"name":"find router","status":"done"},{"name":"install router","status":"failed","detail":"role missing"}]`
	case n > 2:
		status, end = "succeeded", `,"finishedAt":"2026-10-19T08:03:12.000Z"`
		steps = `[{"name":"find router","status":"done","startedAt":"2026-10-19T08:00:00.000Z","finishedAt":"2026-10-19T08:00:12.000Z"},` +
			`{"name":"install router","status":"done","startedAt":"2026-10-19T08:00:12.000Z","finishedAt":"2026-10-19T08:03:12.000Z"}]`
	}
	return fmt.Sprintf(`{"id":"op-%s","type":"upgrade-router","target":{"account":"1","region":"eu-west-1"},"status":%q,"steps":%s,`+
		`"holds":{"locks":[],"slots":[]},"createdAt":"2026-10-19T08:00:00.000Z"%s}`, router, status, steps, end)
}

func (a *opsAPI) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/iam/operations":
			var body struct{ Router string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			a.mu.Lock()
			defer a.mu.Unlock()
			if msg := a.refuse[body.Router]; msg != "" {
				w.WriteHeader(409)
				fmt.Fprintf(w, `{"error":"conflict","message":%q,"operationId":"op-other"}`, msg)
				return
			}
			if body.Router == a.broken {
				w.WriteHeader(502)
				io.WriteString(w, `{"message":"Internal Server Error"}`)
				return
			}
			a.started = append(a.started, body.Router)
			a.running++
			a.peak = max(a.peak, a.running)
			if a.barrier > 0 && len(a.started) == a.barrier {
				close(a.ready)
			}
			w.WriteHeader(202)
			fmt.Fprintf(w, `{"operation":%s}`, a.record(body.Router, 0))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/iam/operations/"):
			if a.ready != nil {
				select {
				case <-a.ready:
				case <-r.Context().Done():
					return
				case <-time.After(5 * time.Second):
					t.Error("the barrier was never reached")
				}
			}
			router := strings.TrimPrefix(r.URL.Path, "/iam/operations/op-")
			a.mu.Lock()
			defer a.mu.Unlock()
			a.gets++
			if a.reads == nil {
				a.reads = map[string]int{}
			}
			a.reads[router]++
			n := a.reads[router]
			if n == 3 && !a.stay {
				a.running--
			}
			io.WriteString(w, a.record(router, n))
		default:
			w.WriteHeader(404)
		}
	}
}

// newOpsTest is a command for the tests of runOperations: an upgrade-router operation per router named, through the
// shared flags.
func newOpsTest(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	var (
		w           waitFlags
		concurrency int
		asJSON      bool
	)
	cmd := fleetCommand("ops-test", "test runOperations")
	f.bind(cmd)
	w.bind(cmd)
	cmd.Flags().IntVar(&concurrency, "concurrency", 4, "")
	cmd.Flags().BoolVar(&asJSON, "json", false, "")
	cmd.RunE = func(c *cobra.Command, routers []string) error {
		if err := w.check(); err != nil {
			return finish(c, err)
		}
		return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
			var items []opItem
			for _, r := range routers {
				items = append(items, opItem{label: r, get: fleet.GetOperation(api), start: func(ctx context.Context) (fleet.Operation, error) {
					return fleet.StartOperation(ctx, api, map[string]any{"type": "upgrade-router", "router": r})
				}})
			}
			ops, err := runOperations(ctx, c, deps, items, concurrency, w)
			if asJSON {
				if jerr := printJSON(c.OutOrStdout(), ops); jerr != nil {
					return jerr
				}
			}
			return err
		})
	}
	return cmd
}

func runOps(t *testing.T, a *opsAPI, tune func(*fleetDeps), args ...string) fleetRan {
	t.Helper()
	return runCommands(t, &fakeAPI{custom: a.handler(t)}, "eu-west-1", tune,
		func(d fleetDeps) []*cobra.Command { return []*cobra.Command{newOpsTest(d)} }, append([]string{"ops-test"}, args...)...)
}

func TestRunOperationsNeverHasMoreThanConcurrencyRunning(t *testing.T) {
	a := &opsAPI{barrier: 2, ready: make(chan struct{})} // none ends before two are started
	r := runOps(t, a, nil, "a", "b", "c", "--concurrency", "2")
	t.Logf("\n%s\n%s", r.stdout, r.stderr)
	if r.exit != 0 || a.peak != 2 || !sameSet(a.started, "a", "b", "c") {
		t.Fatalf("exit %d, peak %d, started %v, stderr %q", r.exit, a.peak, a.started, r.stderr)
	}
	for _, want := range []string{"started upgrade-router op-a for a\n", "✓ a: upgrade-router succeeded in 3m12s\n", "✓ c: upgrade-router succeeded in 3m12s\n"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	// Several run at once: each step line names its operation.
	for _, want := range []string{"  a: … find router\n", "  b: ✓ find router (12s)\n", "  c: ✓ install router (3m0s)\n"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q", want)
		}
	}
}

func TestRunOperationsRefusedStartFailsAndTheOthersAreFollowed(t *testing.T) {
	a := &opsAPI{refuse: map[string]string{"b": "another operation runs on this target"}}
	r := runOps(t, a, nil, "a", "b", "c")
	if r.exit != 1 || !sameSet(a.started, "a", "c") {
		t.Fatalf("exit %d, started %v, stderr %q", r.exit, a.started, r.stderr)
	}
	if want := "✗ b: another operation runs on this target (operation op-other: dormeto operations watch op-other)\n"; !strings.Contains(r.stderr, want) {
		t.Errorf("stderr %q lacks %q", r.stderr, want)
	}
	if !strings.Contains(r.stdout, "✓ a: upgrade-router succeeded") || !strings.Contains(r.stdout, "✓ c: upgrade-router succeeded") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestRunOperationsStartThatMayHaveStartedIsAFailureNamingTheListing(t *testing.T) {
	a := &opsAPI{broken: "a"}
	r := runOps(t, a, nil, "a")
	if r.exit != 1 || !strings.Contains(r.stderr, "✗ a: the manager may have started it: dormeto operations list --running (") {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestRunOperationsNoWaitPrintsTheWatchHintAndReadsNothing(t *testing.T) {
	a := &opsAPI{}
	r := runOps(t, a, nil, "a", "--no-wait")
	if r.exit != 0 || a.gets != 0 || len(a.started) != 1 {
		t.Fatalf("exit %d, %d reads, started %v, stderr %q", r.exit, a.gets, a.started, r.stderr)
	}
	if want := "started upgrade-router op-a for a\nfollow it: dormeto operations watch op-a\n"; r.stdout != want {
		t.Errorf("stdout %q, want %q", r.stdout, want)
	}
}

// fireOnSleep makes --timeout end the following as soon as an operation is waited for.
func fireOnSleep(d *fleetDeps) {
	var fire context.CancelFunc
	d.timeout = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		ctx, fire = context.WithCancel(ctx)
		return ctx, fire
	}
	d.sleep = func(ctx context.Context, _ time.Duration) error { fire(); <-ctx.Done(); return ctx.Err() }
}

func TestRunOperationsTimeoutSaysWhatStillRunsAndExits1(t *testing.T) {
	a := &opsAPI{stay: true}
	r := runOps(t, a, fireOnSleep, "a", "--timeout", "50ms")
	t.Logf("\n%s\n%s", r.stdout, r.stderr)
	if r.exit != 1 || len(a.started) != 1 {
		t.Fatalf("exit %d, started %v, stderr %q", r.exit, a.started, r.stderr)
	}
	for _, want := range []string{"timeout of 50ms reached\n", "a: still running in the manager: dormeto operations watch op-a\n"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q: %q", want, r.stderr)
		}
	}
	if strings.Contains(r.stdout+r.stderr, "✗") || strings.Contains(r.stderr, "exit code") {
		t.Errorf("reported as failed: %q %q", r.stdout, r.stderr)
	}
}

func TestRunOperationsFailedNamesTheStepAndOperation(t *testing.T) {
	a := &opsAPI{end: "failed"}
	r := runOps(t, a, nil, "a")
	if r.exit != 1 || !strings.Contains(r.stderr, "✗ a: upgrade-router failed at install router: role missing (operation op-a)\n") {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "  ✗ install router: role missing\n") {
		t.Errorf("stderr lacks the failed step: %q", r.stderr)
	}
}

func TestRunOperationsJSONOnStdoutProgressOnStderr(t *testing.T) {
	a := &opsAPI{}
	r := runOps(t, a, nil, "a", "--json")
	if r.exit != 0 {
		t.Fatalf("exit %d, stderr %q", r.exit, r.stderr)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("%v: %q", err, r.stdout)
	}
	if len(got) != 1 || got[0]["id"] != "op-a" || got[0]["status"] != "succeeded" || got[0]["holds"] == nil {
		t.Errorf("%v", got)
	}
	// One operation: its step lines are not prefixed.
	for _, want := range []string{"started upgrade-router op-a for a\n", "  … find router\n", "  ✓ find router (12s)\n", "  … install router\n",
		"  ✓ install router (3m0s)\n", "✓ a: upgrade-router succeeded in 3m12s\n"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q: %q", want, r.stderr)
		}
	}
}

func TestRunOperationsBadTimeoutExits2(t *testing.T) {
	a := &opsAPI{}
	r := runOps(t, a, nil, "a", "--timeout", "0s")
	if r.exit != 2 || len(a.started) != 0 {
		t.Errorf("exit %d, started %v, stderr %q", r.exit, a.started, r.stderr)
	}
}

// confirmRun calls confirmNames on a bare command; the terminal answers answer when it is one.
func confirmRun(t *testing.T, terminal, answer bool, names []string, always, dryRun, yes bool) (done bool, err error, out string, asked []string) {
	t.Helper()
	deps := fleetDeps{
		terminal: func(io.Reader) bool { return terminal },
		confirm: func(_ context.Context, _ io.Reader, _ io.Writer, q string) (bool, error) {
			asked = append(asked, q)
			return answer, nil
		},
	}
	c := &cobra.Command{}
	var b bytes.Buffer
	c.SetOut(&b)
	c.SetIn(strings.NewReader(""))
	done, err = confirmNames(t.Context(), c, deps, names, "router", "Uninstall these routers?", always, dryRun, yes)
	return done, err, b.String(), asked
}

func TestConfirmNames(t *testing.T) {
	one, two := []string{"r1"}, []string{"r1", "r2"}
	if done, err, _, asked := confirmRun(t, true, true, one, false, false, false); done || err != nil || len(asked) != 0 {
		t.Errorf("one name: done %v, err %v, asked %q", done, err, asked)
	}
	if done, err, _, asked := confirmRun(t, true, true, one, true, false, false); done || err != nil || !slices.Equal(asked, []string{"Uninstall these routers?"}) {
		t.Errorf("always: done %v, err %v, asked %q", done, err, asked)
	}
	if done, err, out, _ := confirmRun(t, true, false, two, false, false, false); !done || err != nil || out != "not confirmed: nothing sent\n" {
		t.Errorf("declined: done %v, err %v, out %q", done, err, out)
	}
	_, err, _, _ := confirmRun(t, false, true, one, true, false, false)
	if !errorsAsUsage(err) || err.Error() != "confirmation needed: pass --yes to act on 1 router without a terminal" {
		t.Errorf("no terminal: %v", err)
	}
	if done, err, _, asked := confirmRun(t, false, true, two, true, false, true); done || err != nil || len(asked) != 0 {
		t.Errorf("--yes: done %v, err %v, asked %q", done, err, asked)
	}
	if done, err, out, asked := confirmRun(t, true, true, two, true, true, false); !done || err != nil || len(asked) != 0 || out != "dry run: 2 routers, nothing sent\n  r1\n  r2\n" {
		t.Errorf("dry run: done %v, err %v, out %q, asked %q", done, err, out, asked)
	}
}

// askingTerminal is a terminal that writes each question on the writer the command gives it, then answers answer.
func askingTerminal(answer bool) func(*fleetDeps) {
	return func(d *fleetDeps) {
		d.terminal = func(io.Reader) bool { return true }
		d.confirm = func(_ context.Context, _ io.Reader, out io.Writer, q string) (bool, error) {
			fmt.Fprintf(out, "%s [y/N]\n", q)
			return answer, nil
		}
	}
}

// Under --json, stdout holds JSON alone: the question, the dry run's list and "not confirmed" go to stderr, and a dry run
// or a declined question prints what it stopped at.
func TestConfirmNamesUnderJSON(t *testing.T) {
	run := func(answer, dryRun bool) (done bool, err error, stdout, stderr string) {
		var deps fleetDeps
		askingTerminal(answer)(&deps)
		c := &cobra.Command{}
		c.Flags().Bool("json", true, "")
		var o, e bytes.Buffer
		c.SetOut(&o)
		c.SetErr(&e)
		c.SetIn(strings.NewReader(""))
		done, err = confirmNames(t.Context(), c, deps, []string{"r1", "r2"}, "router", "Uninstall r1, r2?", true, dryRun, false)
		return done, err, o.String(), e.String()
	}
	done, err, stdout, stderr := run(true, true)
	if !done || err != nil || stdout != "{\n  \"dryRun\": true,\n  \"targets\": [\n    \"r1\",\n    \"r2\"\n  ]\n}\n" || stderr != "dry run: 2 routers, nothing sent\n  r1\n  r2\n" {
		t.Errorf("dry run: done %v, err %v, stdout %q, stderr %q", done, err, stdout, stderr)
	}
	done, err, stdout, stderr = run(false, false)
	var declined struct {
		Confirmed *bool
		Targets   []string
	}
	if !done || err != nil || json.Unmarshal([]byte(stdout), &declined) != nil || declined.Confirmed == nil || *declined.Confirmed || !slices.Equal(declined.Targets, []string{"r1", "r2"}) ||
		stderr != "Uninstall r1, r2? [y/N]\nnot confirmed: nothing sent\n" {
		t.Errorf("declined: done %v, err %v, stdout %q, stderr %q", done, err, stdout, stderr)
	}
	done, err, stdout, stderr = run(true, false)
	if done || err != nil || stdout != "" || stderr != "Uninstall r1, r2? [y/N]\n" {
		t.Errorf("confirmed: done %v, err %v, stdout %q, stderr %q", done, err, stdout, stderr)
	}
}

func errorsAsUsage(err error) bool {
	_, ok := err.(usageError)
	return ok
}

func TestParseFor(t *testing.T) {
	for in, want := range map[string]time.Duration{"3d": 72 * time.Hour, "1d12h": 36 * time.Hour, "90m": 90 * time.Minute, "2h30m": 150 * time.Minute} {
		if got, err := parseFor(in); err != nil || got != want {
			t.Errorf("parseFor(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"-1d", "d", "1.5d", "", "-1h", "1d-2h", "1D", "2h1d", "99999999999999d"} {
		if got, err := parseFor(bad); err == nil {
			t.Errorf("parseFor(%q) = %v, accepted", bad, got)
		}
	}
}

func TestEndOf(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	if _, set, err := endOf(now, "", "", 366*24*time.Hour, false); set || err != nil {
		t.Errorf("neither, not required: set %v, err %v", set, err)
	}
	if end, set, err := endOf(now, "30d", "", 366*24*time.Hour, false); !set || err != nil || !end.Equal(now.Add(720*time.Hour)) {
		t.Errorf("30d: %v %v %v", end, set, err)
	}
	if _, _, err := endOf(now, "1h", "17:00", 366*24*time.Hour, false); !errorsAsUsage(err) {
		t.Errorf("both: %v", err)
	}
	_, _, err := endOf(now, "367d", "", 366*24*time.Hour, false)
	if !errorsAsUsage(err) || !strings.Contains(err.Error(), "up to 8784h (366 days)") {
		t.Errorf("367d: %v", err)
	}
	if _, _, err := endOf(now, "", "", 168*time.Hour, true); !errorsAsUsage(err) || err.Error() != "give exactly one of --for and --until" {
		t.Errorf("neither, required: %v", err)
	}
}

// A command that builds its waitFlags by hand, with no --timeout checked, follows its operations without a limit.
func TestRunOperationsWithoutTimeoutFollowsToTheEnd(t *testing.T) {
	reads := 0
	item := opItem{
		label: "a",
		start: func(context.Context) (fleet.Operation, error) {
			return fleet.Operation{ID: "op-a", Type: "install-router", Status: "running"}, nil
		},
		get: func(context.Context, string) (fleet.Operation, error) {
			if reads++; reads < 2 {
				return fleet.Operation{ID: "op-a", Type: "install-router", Status: "running"}, nil
			}
			return fleet.Operation{ID: "op-a", Type: "install-router", Status: "succeeded"}, nil
		},
	}
	deps := fleetDeps{
		now:     func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) },
		sleep:   func(context.Context, time.Duration) error { return nil },
		timeout: context.WithTimeout,
	}
	c := &cobra.Command{}
	var out, errs bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errs)
	ops, err := runOperations(t.Context(), c, deps, []opItem{item}, 1, waitFlags{timeout: 0})
	if err != nil || len(ops) != 1 || ops[0].Status != "succeeded" || reads != 2 || !strings.Contains(out.String(), "✓ a: install-router succeeded") {
		t.Errorf("err %v, ops %+v, %d reads, stdout %q, stderr %q", err, ops, reads, out.String(), errs.String())
	}
}
