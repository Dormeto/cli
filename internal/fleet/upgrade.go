package fleet

// The upgrade of routers: the plan (which are upgraded, which skipped and why) and its run, one `upgrade-router`
// operation per router started in the manager and followed until it ends.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
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
		b.WriteString(table(rows))
	}
	if len(p.Skipped) > 0 {
		fmt.Fprintf(&b, "%d skipped\n", len(p.Skipped))
		var rows [][]string
		for _, s := range p.Skipped {
			rows = append(rows, []string{"  " + s.Router, s.Reason})
		}
		b.WriteString(table(rows))
	}
	return b.String()
}

// Operation is the manager's operation, as far as the upgrade reads it.
type Operation struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Status string          `json:"status"` // running, succeeded or failed
	Steps  []OperationStep `json:"steps"`
}

// OperationStep is one step of an operation: pending, running, done, skipped or failed.
type OperationStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
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
	var out struct {
		Operation Operation `json:"operation"`
	}
	err := a.c.Post(ctx, "/operations", map[string]string{"type": "upgrade-router", "router": router}, &out)
	return out.Operation, err
}

func (a apiOperations) Get(ctx context.Context, id string) (Operation, error) {
	var op Operation
	err := a.c.Get(ctx, "/operations/"+id, nil, &op)
	return op, err
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

// refusedAfter is how many reads of an operation in a row may fail for a reason that will not pass (a 403 or 404, the
// credentials, an unreadable answer) before it is given up as unknown. A network error, 429 or 5xx is waited out: the
// upgrade runs in the manager, and the context bounds the wait.
const refusedAfter = 3

// transient tells whether err is worth waiting out: a network error, 429 or 5xx. Any other error (the credentials, an
// answer that cannot be read, a 4xx refusal) will not pass by itself.
func transient(err error) bool {
	var ne net.Error
	var ue *url.Error
	var ae *APIError
	switch {
	case errors.As(err, &ae):
		return ae.Status == http.StatusTooManyRequests || ae.Status >= 500
	case errors.As(err, &ue), errors.As(err, &ne):
		return true
	}
	return false
}

// mayHaveStarted tells whether a failed start leaves the operation possibly created in the manager: a 5xx, a transport
// error, or a 2xx answer that cannot be read (a 4xx refusal, or an error before the request is sent, did not start it).
func mayHaveStarted(err error) bool {
	var ue2 *unreadableError
	if errors.Is(err, ErrNotSent) {
		return false
	}
	if errors.As(err, &ue2) {
		return true
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status >= 500
	}
	var ne net.Error
	var ue *url.Error
	return errors.As(err, &ue) || errors.As(err, &ne)
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
		if ctx.Err() != nil { // cut short in the call: the manager may or may not have started it
			return ended("unknown", "interrupted while starting: "+startUnknown(err))
		}
		if mayHaveStarted(err) {
			return ended("unknown", startUnknown(err))
		}
		if errors.Is(err, ErrNotSent) {
			return ended("failed", "not started: "+err.Error())
		}
		return ended("failed", err.Error())
	}
	out.Operation = op.ID
	report(Event{Outcome: out})
	for failures := 0; ; {
		switch op.Status {
		case "succeeded":
			return ended("succeeded", "")
		case "failed":
			return ended("failed", failure(op))
		}
		if err := o.Sleep(ctx, o.Poll); err != nil {
			out.Status, out.Duration = "running", o.Now().Sub(began)
			return out
		}
		next, err := api.Get(ctx, op.ID)
		switch {
		case err == nil:
			failures, op = 0, next
		case ctx.Err() != nil:
			out.Status, out.Duration = "running", o.Now().Sub(began)
			return out
		case !transient(err):
			if failures++; failures >= refusedAfter {
				return ended("unknown", "cannot read the operation: "+err.Error())
			}
		}
	}
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
