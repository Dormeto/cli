package fleet

// The upgrade of routers: the plan (which are upgraded, which skipped and why) and its run, one `upgrade-router`
// operation per router started in the manager and followed until it ends.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Dormeto/cli/internal/install"
)

// Step is a router the plan upgrades.
type Step struct{ Router, From, To string }

// Skip is a router the plan leaves alone, and why.
type Skip struct {
	Router string `json:"router"`
	Reason string `json:"reason"`
}

// Plan is what an upgrade does to the routers selected.
type Plan struct {
	Available string
	Upgrade   []Step
	Skipped   []Skip
}

// PlanUpgrade decides for each router, in order: written in Terraform, an operation running, its version unknown,
// up to date, newer than the version available (a pre-release is below its release), else upgraded.
func PlanUpgrade(rs []Router, available string) Plan {
	p := Plan{Available: available}
	skip := func(r Router, reason string) { p.Skipped = append(p.Skipped, Skip{r.Name, reason}) }
	for _, r := range rs {
		switch {
		case r.Source == "terraform":
			skip(r, "written in Terraform (upgraded with its Terraform)")
		case r.Operation != nil:
			skip(r, fmt.Sprintf("an operation running on it (%s %s)", r.Operation.Type, r.Operation.ID))
		case r.Version == "" && r.Error != "":
			skip(r, fmt.Sprintf("its version is unknown (%s)", r.Error))
		case r.Version == "":
			skip(r, "its version is unknown")
		case r.Outdated(available):
			p.Upgrade = append(p.Upgrade, Step{r.Name, r.Version, available})
		case install.CompareVersions(r.Version, available) == 0:
			skip(r, "up to date")
		default:
			skip(r, "newer than the version available")
		}
	}
	return p
}

// Text is the plan as printed: the routers to upgrade with from → to, then the skipped ones with their reasons.
func (p Plan) Text() string {
	var b strings.Builder
	if len(p.Upgrade) == 0 {
		b.WriteString("nothing to upgrade\n")
	} else {
		fmt.Fprintf(&b, "%d %s to upgrade to %s\n", len(p.Upgrade), Plural(len(p.Upgrade), "router"), p.Available)
		var rows [][]string
		for _, s := range p.Upgrade {
			rows = append(rows, []string{"  " + s.Router, s.From + " → " + s.To})
		}
		b.WriteString(Table(rows))
	}
	if len(p.Skipped) > 0 {
		fmt.Fprintf(&b, "%d skipped\n", len(p.Skipped))
		var rows [][]string
		for _, s := range p.Skipped {
			rows = append(rows, []string{"  " + s.Router, s.Reason})
		}
		b.WriteString(Table(rows))
	}
	return b.String()
}

// Operations starts and reads the manager's operations.
type Operations interface {
	Start(ctx context.Context, router string) (Operation, error)
	Get(ctx context.Context, id string) (Operation, error)
}

// NewOperations is the manager's operations through c: POST /operations, GET /operations/<id>.
func NewOperations(c *Client) Operations { return apiOperations{c} }

type apiOperations struct{ c *Client }

func (a apiOperations) Start(ctx context.Context, router string) (Operation, error) {
	return StartOperation(ctx, a.c, map[string]any{"type": "upgrade-router", "router": router})
}

func (a apiOperations) Get(ctx context.Context, id string) (Operation, error) {
	return GetOperation(a.c)(ctx, id)
}

// Outcome is what became of an upgrade. Status: succeeded, failed (it did not start, or ended failed), running (the
// run stopped following it), unknown (the operation could not be read) or "not started" (the run stopped first).
type Outcome struct {
	Router    string        `json:"router"`
	From      string        `json:"from"`
	To        string        `json:"to"`
	Operation string        `json:"operation,omitempty"`
	Status    string        `json:"status"`
	Error     string        `json:"error,omitempty"`
	Duration  time.Duration `json:"-"`
}

// Line is the outcome as printed when it ends: ✓ from → to and its duration, or ✗ the failed step and its detail.
func (o Outcome) Line() string {
	id := ""
	if o.Operation != "" {
		id = fmt.Sprintf(" (operation %s)", o.Operation)
	}
	if o.Status == "succeeded" {
		return fmt.Sprintf("✓ %s %s → %s in %s%s", o.Router, o.From, o.To, o.Duration.Round(time.Second), id)
	}
	return fmt.Sprintf("✗ %s: %s%s", o.Router, o.Error, id)
}

// Event is reported as an upgrade starts and as it ends.
type Event struct {
	Ended bool
	Outcome
}

// Result is what an upgrade run did: the outcome of each router of the plan, in its order, and the routers skipped.
type Result struct {
	Available string    `json:"available"`
	Upgraded  []Outcome `json:"upgraded"`
	Skipped   []Skip    `json:"skipped"`
	Stopped   bool      `json:"-"` // the context ended before every upgrade did
}

func (r Result) count(status ...string) (n int) {
	for _, o := range r.Upgraded {
		for _, s := range status {
			if o.Status == s {
				n++
			}
		}
	}
	return n
}

// Failed counts the upgrades that failed or whose operation could not be read.
func (r Result) Failed() int { return r.count("failed", "unknown") }

// Unfinished counts the upgrades with an operation in the manager whose end the run did not see: they may go on there.
func (r Result) Unfinished() (n int) {
	for _, o := range r.Upgraded {
		if o.Operation != "" && o.Status != "succeeded" && o.Status != "failed" {
			n++
		}
	}
	return n
}

// Running counts those the run stopped following; NotStarted those it stopped before starting.
func (r Result) Running() int    { return r.count("running") }
func (r Result) NotStarted() int { return r.count("not started") }

// Summary counts the outcomes: "2 upgraded, 1 failed, 1 still running, 3 not started" (only those there are).
func (r Result) Summary() string {
	var parts []string
	for _, c := range []struct {
		n     int
		label string
	}{
		{r.count("succeeded"), "upgraded"}, {r.count("failed"), "failed"}, {r.count("unknown"), "unknown"},
		{r.Running(), "still running"}, {r.NotStarted(), "not started"},
	} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.label))
		}
	}
	return strings.Join(parts, ", ")
}

// UpgradeOptions are how Upgrade runs; Sleep and Now are injected so that tests need no real time.
type UpgradeOptions struct {
	Concurrency int // at most this many upgrades run at once
	Poll        time.Duration
	Sleep       func(context.Context, time.Duration) error // ends with the context's error when it is done
	Now         func() time.Time
	Progress    func(Event) // called one at a time
}

// startUnknown is how a start whose outcome is not known is reported.
func startUnknown(err error) string {
	return "the manager may have started it, check its Operations view (" + err.Error() + ")"
}

// Upgrade starts the plan's upgrades, at most Concurrency at once, and follows each every Poll until it ends. When ctx
// ends, it starts no more and stops following: the upgrades started go on in the manager, reported as running.
func Upgrade(ctx context.Context, api Operations, plan Plan, o UpgradeOptions) Result {
	res := Result{Available: plan.Available, Upgraded: make([]Outcome, len(plan.Upgrade)), Skipped: plan.Skipped}
	var mu sync.Mutex
	report := func(e Event) {
		if o.Progress != nil {
			mu.Lock()
			defer mu.Unlock()
			o.Progress(e)
		}
	}
	slots := make(chan struct{}, max(o.Concurrency, 1))
	var wg sync.WaitGroup
	for i, st := range plan.Upgrade {
		res.Upgraded[i] = Outcome{Router: st.Router, From: st.From, To: st.To, Status: "not started"}
		if ctx.Err() != nil {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			res.Upgraded[i] = follow(ctx, api, st, o, report)
		}()
	}
	wg.Wait()
	for _, u := range res.Upgraded {
		if ctx.Err() != nil && u.Status != "succeeded" && u.Status != "failed" {
			res.Stopped = true
		}
	}
	return res
}

// follow starts one router's upgrade and follows its operation to its end, or until ctx ends.
func follow(ctx context.Context, api Operations, st Step, o UpgradeOptions, report func(Event)) Outcome {
	out := Outcome{Router: st.Router, From: st.From, To: st.To}
	began := o.Now()
	ended := func(status, errMsg string) Outcome {
		out.Status, out.Error, out.Duration = status, errMsg, o.Now().Sub(began)
		report(Event{Ended: true, Outcome: out})
		return out
	}
	if ctx.Err() != nil { // the slot was given before the run was cut short
		out.Status = "not started"
		return out
	}
	op, err := api.Start(ctx, st.Router)
	if err != nil {
		if errors.Is(err, ErrNotSent) { // nothing was sent: safe to run again
			if ctx.Err() != nil { // the run was cut short: like the starts the stop pre-empted
				out.Status = "not started"
				return out
			}
			return ended("failed", "not started: "+err.Error())
		}
		if ctx.Err() != nil { // cut short in the call: the manager may or may not have started it
			return ended("unknown", "interrupted while starting: "+startUnknown(err))
		}
		if MayHaveStarted(err) {
			return ended("unknown", startUnknown(err))
		}
		return ended("failed", err.Error())
	}
	if op.ID == "" { // answered 2xx without naming the operation: it cannot be followed
		return ended("unknown", startUnknown(errors.New("the answer names no operation")))
	}
	out.Operation = op.ID
	report(Event{Outcome: out})
	if op.Status != "succeeded" && op.Status != "failed" {
		var err error
		if op, err = Follow(ctx, api.Get, op.ID, FollowOptions{Poll: o.Poll, Sleep: o.Sleep}); err != nil {
			if ctx.Err() != nil { // the run stopped following: the upgrade goes on in the manager
				out.Status, out.Duration = "running", o.Now().Sub(began)
				return out
			}
			return ended("unknown", "cannot read the operation: "+err.Error())
		}
	}
	switch op.Status {
	case "succeeded":
		return ended("succeeded", "")
	case "failed":
		return ended("failed", failure(op))
	}
	return ended("unknown", fmt.Sprintf("the operation is %q", op.Status))
}

// failure is the operation's failed step and its detail.
func failure(op Operation) string {
	for _, s := range op.Steps {
		if s.Status == "failed" {
			if s.Detail == "" {
				return s.Name + ": failed"
			}
			return s.Name + ": " + s.Detail
		}
	}
	return "the operation failed"
}
