package fleet

// The plan of an upgrade and its run against a fake Operations, with injected time: the skip reasons, how many run at
// once, the failures and a context cancelled while following.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPlanUpgradeSkipReasons(t *testing.T) {
	rs := []Router{
		{Name: "old", Version: "0.29.0"},
		{Name: "dev", Version: "0.30.0-dev.1"},
		{Name: "tf", Source: "terraform", Version: "0.28.0"},
		{Name: "same", Version: "0.30.0"},
		{Name: "ahead", Version: "0.31.0-dev.1"},
		{Name: "lost", Error: "timed out"},
		{Name: "blind"},
		{Name: "busy", Version: "0.29.0", Operation: &OpRef{ID: "op-9", Type: "upgrade-router"}},
	}
	p := PlanUpgrade(rs, "0.30.0")
	var up []string
	for _, s := range p.Upgrade {
		up = append(up, fmt.Sprintf("%s %s>%s", s.Router, s.From, s.To))
	}
	if got := strings.Join(up, ","); got != "old 0.29.0>0.30.0,dev 0.30.0-dev.1>0.30.0" {
		t.Errorf("upgraded %s", got)
	}
	want := map[string]string{
		"tf":    "written in Terraform (upgraded with its Terraform)",
		"same":  "up to date",
		"ahead": "newer than the version available",
		"lost":  "its version is unknown (timed out)",
		"blind": "its version is unknown",
		"busy":  "an operation running on it (upgrade-router op-9)",
	}
	if len(p.Skipped) != len(want) {
		t.Fatalf("skipped %+v", p.Skipped)
	}
	for _, s := range p.Skipped {
		if want[s.Router] != s.Reason {
			t.Errorf("%s: %q, want %q", s.Router, s.Reason, want[s.Router])
		}
	}
}

func TestPlanText(t *testing.T) {
	p := PlanUpgrade([]Router{{Name: "old", Version: "0.29.0"}, {Name: "same", Version: "0.30.0"}}, "0.30.0")
	got := p.Text()
	t.Logf("\n%s", got)
	for _, w := range []string{"1 router to upgrade to 0.30.0", "old", "0.29.0 → 0.30.0", "1 skipped", "same", "up to date"} {
		if !strings.Contains(got, w) {
			t.Errorf("lacks %q in %q", w, got)
		}
	}
	if !strings.Contains(PlanUpgrade(nil, "0.30.0").Text(), "nothing to upgrade") {
		t.Error("empty plan")
	}
}

// fakeOps is an Operations whose operations end after `polls` follow-ups. It counts those running at once.
type fakeOps struct {
	mu          sync.Mutex
	polls       int
	started     []string
	seen        map[string]int
	running     int
	peak        int
	failWith    map[string]Operation // router -> the final operation it ends with
	refuse      map[string]error
	getErr      error
	getErrOnce  error // the error of the first getErrOnceN reads
	getErrOnceN int
	startFn     func(context.Context) error // called by Start, its error refuses the start
	gets        int
	getErrs     int           // the first reads that fail with a transient error
	barrier     int           // Get waits until this many routers have started
	ready       chan struct{} // closed at the barrier
	onStart     func(n int)   // called with the number started, after a start
	onGet       func()
}

func (f *fakeOps) Start(ctx context.Context, router string) (Operation, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, err
	}
	if f.startFn != nil {
		if err := f.startFn(ctx); err != nil {
			return Operation{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.refuse[router]; err != nil {
		return Operation{}, err
	}
	f.started = append(f.started, router)
	if f.seen == nil {
		f.seen = map[string]int{}
	}
	f.running++
	f.peak = max(f.peak, f.running)
	if f.barrier > 0 && len(f.started) == f.barrier {
		close(f.ready)
	}
	if f.onStart != nil {
		f.onStart(len(f.started))
	}
	return Operation{ID: "op-" + router, Status: "running"}, nil
}

func (f *fakeOps) Get(ctx context.Context, id string) (Operation, error) {
	if f.onGet != nil {
		f.onGet()
	}
	if f.ready != nil {
		select {
		case <-f.ready:
		case <-ctx.Done():
			return Operation{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.getErrOnceN > 0 {
		f.getErrOnceN--
		return Operation{}, f.getErrOnce
	}
	if f.getErrs > 0 {
		f.getErrs--
		return Operation{}, &url.Error{Op: "Get", URL: "https://x", Err: errors.New("connection reset")}
	}
	if f.getErr != nil {
		return Operation{}, f.getErr
	}
	f.seen[id]++
	if f.seen[id] < f.polls {
		return Operation{ID: id, Status: "running"}, nil
	}
	f.running--
	if op, ok := f.failWith[strings.TrimPrefix(id, "op-")]; ok {
		op.ID = id
		return op, nil
	}
	return Operation{ID: id, Status: "succeeded"}, nil
}

// clock is a fake time: Sleep advances it.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return nil
}

func options(c *clock, n int, events *[]Event) UpgradeOptions {
	var mu sync.Mutex
	return UpgradeOptions{Concurrency: n, Poll: 5 * time.Second, Sleep: c.sleep, Now: c.now,
		Progress: func(e Event) { mu.Lock(); *events = append(*events, e); mu.Unlock() }}
}

func planOf(names ...string) Plan {
	p := Plan{Available: "0.30.0"}
	for _, n := range names {
		p.Upgrade = append(p.Upgrade, Step{Router: n, From: "0.29.0", To: "0.30.0"})
	}
	return p
}

func TestUpgradeNeverRunsMoreThanConcurrency(t *testing.T) {
	ops := &fakeOps{polls: 3, barrier: 2, ready: make(chan struct{})} // none ends before two have started
	var ev []Event
	c := &clock{t: time.Unix(0, 0)}
	res := Upgrade(t.Context(), ops, planOf("a", "b", "c", "d", "e"), options(c, 2, &ev))
	if ops.peak != 2 || len(ops.started) != 5 {
		t.Errorf("peak %d, started %v", ops.peak, ops.started)
	}
	if res.Failed() != 0 || len(res.Upgraded) != 5 || res.Stopped {
		t.Fatalf("%+v", res)
	}
	for _, o := range res.Upgraded {
		if o.Status != "succeeded" || o.Operation != "op-"+o.Router || o.From != "0.29.0" || o.To != "0.30.0" {
			t.Errorf("%+v", o)
		}
	}
	lines := res.Upgraded[0].Line()
	if !strings.HasPrefix(lines, "✓ a 0.29.0 → 0.30.0 in ") || !strings.Contains(lines, "(operation op-a)") {
		t.Errorf("line %q", lines)
	}
	t.Logf("%s", lines)
}

func TestUpgradeFailedKeepsTheStepAndDetail(t *testing.T) {
	ops := &fakeOps{polls: 1, failWith: map[string]Operation{"b": {Status: "failed", Steps: []OperationStep{
		{Name: "backup", Status: "done"}, {Name: "update-stack", Status: "failed", Detail: "role missing"}}}}}
	var ev []Event
	res := Upgrade(t.Context(), ops, planOf("a", "b"), options(&clock{}, 4, &ev))
	if res.Failed() != 1 {
		t.Fatalf("%+v", res)
	}
	b := res.Upgraded[1]
	if b.Status != "failed" || b.Error != "update-stack: role missing" {
		t.Errorf("%+v", b)
	}
	if got := b.Line(); got != "✗ b: update-stack: role missing (operation op-b)" {
		t.Errorf("line %q", got)
	}
	ended := 0
	for _, e := range ev {
		if e.Ended {
			ended++
		}
	}
	if ended != 2 {
		t.Errorf("events %+v", ev)
	}
}

func TestUpgradeRefusedStartIsFailedWithItsMessage(t *testing.T) {
	ops := &fakeOps{polls: 1, refuse: map[string]error{"a": &APIError{Status: 409, Code: "conflict", Message: "an operation runs on router a"}}}
	res := Upgrade(t.Context(), ops, planOf("a", "b"), options(&clock{}, 4, new([]Event)))
	a := res.Upgraded[0]
	if a.Status != "failed" || a.Error != "an operation runs on router a" || a.Operation != "" || res.Failed() != 1 {
		t.Errorf("%+v", res)
	}
	if got := a.Line(); got != "✗ a: an operation runs on router a" {
		t.Errorf("line %q", got)
	}
	if res.Upgraded[1].Status != "succeeded" {
		t.Errorf("b: %+v", res.Upgraded[1])
	}
}

func TestUpgradeCancelledWhileFollowingReportsStartedAsRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ops := &fakeOps{polls: 100, barrier: 2, ready: make(chan struct{})}
	ops.onStart = func(n int) { // at the second start
		if n == 2 {
			cancel()
		}
	}
	res := Upgrade(ctx, ops, planOf("a", "b", "c"), options(&clock{}, 2, new([]Event)))
	if !res.Stopped {
		t.Fatal("not stopped")
	}
	got := map[string]string{}
	for _, o := range res.Upgraded {
		got[o.Router] = o.Status
	}
	if got["a"] != "running" || got["b"] != "running" || got["c"] != "not started" {
		t.Errorf("%v", got)
	}
	if res.Failed() != 0 || res.Running() != 2 || res.NotStarted() != 1 || res.Unfinished() != 2 {
		t.Errorf("failed %d, running %d, not started %d, unfinished %d", res.Failed(), res.Running(), res.NotStarted(), res.Unfinished())
	}
	if len(ops.started) != 2 {
		t.Errorf("started %v", ops.started)
	}
}

// A goroutine that got its slot just before the cancellation starts nothing, and that is no failure.
func TestFollowOnADoneContextStartsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ops := &fakeOps{polls: 1}
	o := follow(ctx, ops, Step{"a", "0.29.0", "0.30.0"}, options(&clock{}, 1, new([]Event)), func(Event) {})
	if o.Status != "not started" || o.Error != "" || len(ops.started) != 0 {
		t.Errorf("%+v, started %v", o, ops.started)
	}
}

func TestUpgradeFollowingSurvivesTransientErrors(t *testing.T) {
	ops := &fakeOps{polls: 1, getErrs: 10}
	res := Upgrade(t.Context(), ops, planOf("a"), options(&clock{}, 1, new([]Event)))
	if res.Upgraded[0].Status != "succeeded" || res.Failed() != 0 {
		t.Errorf("%+v", res.Upgraded[0])
	}
}

func TestUpgradeFollowingTransientErrorsEndWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ops := &fakeOps{polls: 1, getErrs: 1 << 30}
	ops.onGet = func() {
		ops.mu.Lock()
		n := ops.getErrs
		ops.mu.Unlock()
		if n < 1<<30-20 {
			cancel()
		}
	}
	res := Upgrade(ctx, ops, planOf("a"), options(&clock{}, 1, new([]Event)))
	if res.Upgraded[0].Status != "running" || res.Unfinished() != 1 || !res.Stopped {
		t.Errorf("%+v", res)
	}
}

func TestUpgradeLosingTheOperationIsNotASuccess(t *testing.T) {
	ops := &fakeOps{polls: 1, getErr: &APIError{Status: 403, Message: "boom"}} // a refusal will not pass
	res := Upgrade(t.Context(), ops, planOf("a"), options(&clock{}, 1, new([]Event)))
	o := res.Upgraded[0]
	if o.Status != "unknown" || !strings.Contains(o.Error, "boom") || res.Failed() != 1 || res.Unfinished() != 1 {
		t.Errorf("%+v", o)
	}
	if got := res.Summary(); got != "1 unknown" {
		t.Errorf("summary %q", got)
	}
}

func TestUpgradeStartFailingWith5xxOrTransportErrorMayHaveStarted(t *testing.T) {
	for name, err := range map[string]error{
		"5xx":       &APIError{Status: 502, Message: "bad gateway"},
		"transport": &url.Error{Op: "Post", URL: "https://x", Err: errors.New("i/o timeout")},
	} {
		t.Run(name, func(t *testing.T) {
			ops := &fakeOps{polls: 1, refuse: map[string]error{"a": err}}
			res := Upgrade(t.Context(), ops, planOf("a"), options(&clock{}, 1, new([]Event)))
			a := res.Upgraded[0]
			if a.Status != "unknown" || !strings.Contains(a.Error, "the manager may have started it: check its Operations view (") ||
				!strings.Contains(a.Error, err.Error()) || res.Failed() != 1 {
				t.Errorf("%+v", a)
			}
		})
	}
}

func TestUpgradeStartRefusedWith4xxDidNotStart(t *testing.T) {
	for _, status := range []int{403, 404, 409} {
		ops := &fakeOps{polls: 1, refuse: map[string]error{"a": &APIError{Status: status, Message: "no"}}}
		a := Upgrade(t.Context(), ops, planOf("a"), options(&clock{}, 1, new([]Event)))
		if o := a.Upgraded[0]; o.Status != "failed" || o.Error != "no" {
			t.Errorf("%d: %+v", status, o)
		}
	}
}

func TestUpgradeCutShortStartUsesTheSameWording(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ops := &fakeOps{polls: 1, startFn: func(context.Context) error { cancel(); return context.Canceled }}
	res := Upgrade(ctx, ops, planOf("a"), options(&clock{}, 1, new([]Event)))
	want := "interrupted while starting: the manager may have started it: check its Operations view (context canceled)"
	if o := res.Upgraded[0]; o.Status != "unknown" || o.Error != want {
		t.Errorf("%+v", o)
	}
}

func TestUpgradeFollowingGivesUpOnANonAPIError(t *testing.T) {
	for _, err := range []error{
		errors.New("AWS credentials: the SSO session has expired"),
		errors.New("decode the manager's answer: invalid character"),
		&APIError{Status: 404, Message: "no such operation"},
	} {
		ops := &fakeOps{polls: 1, getErr: err}
		res := Upgrade(t.Context(), ops, planOf("a"), options(&clock{}, 1, new([]Event)))
		o := res.Upgraded[0]
		if o.Status != "unknown" || !strings.Contains(o.Error, err.Error()) {
			t.Errorf("%v: %+v", err, o)
		}
		if ops.gets != refusedAfter {
			t.Errorf("%v: %d reads, want %d", err, ops.gets, refusedAfter)
		}
	}
}

func TestUpgradeFollowingWaitsOutNetworkErrors429And5xx(t *testing.T) {
	for _, err := range []error{
		&url.Error{Op: "Get", URL: "https://x", Err: errors.New("reset")},
		&APIError{Status: 429}, &APIError{Status: 503},
	} {
		ops := &fakeOps{polls: 1, getErrOnce: err, getErrOnceN: 10}
		res := Upgrade(t.Context(), ops, planOf("a"), options(&clock{}, 1, new([]Event)))
		if res.Upgraded[0].Status != "succeeded" {
			t.Errorf("%v: %+v", err, res.Upgraded[0])
		}
	}
}
