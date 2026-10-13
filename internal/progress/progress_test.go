package progress

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/discover"
)

const (
	A = "111111111111"
	B = "222222222222"
)

func region(account, r string, workloads int, from, err string) discover.ProgressEvent {
	if from == "" {
		from = discover.FromAWS
	}
	return discover.ProgressEvent{Phase: "discovery", Account: account, Region: r, From: from, Workloads: workloads, Error: err}
}

type status struct{ updates, ends []string }

func (s *status) Update(l string) { s.updates = append(s.updates, l) }
func (s *status) End(l string)    { s.ends = append(s.ends, l) }
func (s *status) last() string    { return s.updates[len(s.updates)-1] }

// fake is a scheduler that runs when told: the views' timers, advanced by hand.
type fake struct {
	t      time.Time
	timers []*timer
}
type timer struct {
	d  time.Duration
	fn func()
}

func newFake() *fake           { return &fake{t: time.Unix(0, 0)} }
func (c *fake) now() time.Time { return c.t }
func (c *fake) every(d time.Duration, fn func()) func() {
	t := &timer{d, fn}
	c.timers = append(c.timers, t)
	return func() {
		for i, x := range c.timers {
			if x == t {
				c.timers = append(c.timers[:i], c.timers[i+1:]...)
				return
			}
		}
	}
}
func (c *fake) advance(d time.Duration) {
	c.t = c.t.Add(d)
	for _, t := range append([]*timer(nil), c.timers...) {
		t.fn()
	}
}
func (c *fake) periods() []time.Duration {
	out := []time.Duration{}
	for _, t := range c.timers {
		out = append(out, t.d)
	}
	return out
}
func eq(t *testing.T, got, want any) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}
func s(sec int) time.Duration { return time.Duration(sec) * time.Second }

func TestDiscoveryTerminalRedrawnAndLastNamed(t *testing.T) {
	c := newFake()
	st := &status{}
	v := DiscoveryView(3, Options{Now: c.now, Every: c.every, Status: st})
	eq(t, c.periods(), []time.Duration{s(1)})
	v.Account(discover.AccountEvent{Account: A, State: "started", Regions: 1})
	v.Region(region(A, "eu-west-1", 2, "", ""))
	v.Account(discover.AccountEvent{Account: A, State: "done", Regions: 1})
	c.advance(s(20))
	v.Account(discover.AccountEvent{Account: B, State: "started", Regions: 1})
	c.advance(s(40))
	v.Account(discover.AccountEvent{Account: "333333333333", State: "started", Regions: 1})
	c.advance(s(5))
	eq(t, st.last(), "  Discovery [███████░░░░░░░░░░░░░]  33% · 1/3 accounts · 1 region · 2 workloads · 1:05 · waiting for "+B+" (0:45), 333333333333 (0:05)")
	v.End()
	eq(t, len(c.timers), 0)
}

func TestDiscoveryElsewhereMinuteStillReading(t *testing.T) {
	c := newFake()
	var logs []string
	v := DiscoveryView(8, Options{Now: c.now, Every: c.every, Log: func(l string) { logs = append(logs, l) }})
	eq(t, c.periods(), []time.Duration{time.Minute})
	for i := 1; i <= 7; i++ {
		v.Account(discover.AccountEvent{Account: strings.Repeat(fmt.Sprint(i), 12), State: "started", Regions: 1})
	}
	c.advance(time.Minute)
	one := func(d string) string { return strings.Repeat(d, 12) + " (1:00)" }
	eq(t, logs, []string{"  discovery: still reading 7 accounts: " + strings.Join([]string{one("1"), one("2"), one("3"), one("4"), one("5")}, ", ") + " and 2 more"})
	v.End()
	eq(t, len(c.timers), 0)
}

func TestDiscoveryTerminalLine(t *testing.T) {
	c := newFake()
	st := &status{}
	var logs []string
	v := DiscoveryView(2, Options{Now: c.now, Every: c.every, Status: st, Log: func(l string) { logs = append(logs, l) }})
	v.Account(discover.AccountEvent{Account: A, State: "started", Regions: 2})
	c.t = c.t.Add(s(5))
	v.Region(region(A, "eu-west-1", 3, "", ""))
	eq(t, st.last(), "  Discovery [█████░░░░░░░░░░░░░░░]  25% · 0/2 accounts · 1 region · 3 workloads · 0:05 · waiting for "+A+" (0:05)")
	v.Region(region(A, "eu-west-3", 0, "", "opt-in region not enabled"))
	v.Account(discover.AccountEvent{Account: A, State: "done", Regions: 2})
	v.Account(discover.AccountEvent{Account: B, State: "started", Regions: 1})
	c.t = c.t.Add(s(67))
	v.Region(region(B, "eu-west-1", 2, discover.FromCache, ""))
	v.Account(discover.AccountEvent{Account: B, State: "done", Regions: 1})
	want := "  Discovery [████████████████████] 100% · 2/2 accounts · 3 regions · 5 workloads · 1 failed · 1:12"
	eq(t, st.last(), want)
	v.End()
	eq(t, st.ends, []string{want})
	eq(t, len(logs), 0)
}

func TestDiscoveryAccountWithoutRegionsIsDoneAndFailed(t *testing.T) {
	st := &status{}
	v := DiscoveryView(1, Options{Now: newFake().now, Every: newFake().every, Status: st})
	v.Account(discover.AccountEvent{Account: A, State: "done", Regions: 0, Error: "UnauthorizedOperation: not authorized"})
	v.End()
	eq(t, st.ends[0], "  Discovery [████████████████████] 100% · 1/1 account · 0 regions · 0 workloads · 1 failed · 0:00")
}

func TestDiscoveryElsewhereLinePerAccount(t *testing.T) {
	c := newFake()
	var logs []string
	v := DiscoveryView(2, Options{Now: c.now, Every: c.every, Log: func(l string) { logs = append(logs, l) }})
	v.Account(discover.AccountEvent{Account: A, State: "started", Regions: 2})
	v.Region(region(A, "eu-west-1", 3, discover.FromCache, ""))
	v.Region(region(A, "eu-west-3", 0, "", "opt-in region not enabled"))
	v.Account(discover.AccountEvent{Account: A, State: "done", Regions: 2})
	v.Account(discover.AccountEvent{Account: B, State: "done", Regions: 0, Error: "UnauthorizedOperation: not authorized"})
	v.End()
	eq(t, logs, []string{
		"  discovery " + A + " (1/2): 2 regions, 3 workloads, 1 from the cache, 1 failed",
		"  discovery " + B + " (2/2): failed (UnauthorizedOperation: not authorized)",
	})
}

func done(phase, account string, workloads int, from, err string) discover.ProgressEvent {
	if from == "" {
		from = discover.FromAWS
	}
	return discover.ProgressEvent{Phase: phase, Account: account, Region: "eu-west-1", From: from, Workloads: workloads, Error: err}
}

func TestRegionsTerminal(t *testing.T) {
	c := newFake()
	st := &status{}
	v := RegionsView("Requests", Options{Now: c.now, Every: c.every, Status: st})
	eq(t, c.periods(), []time.Duration{s(1)})
	v.Plan(3)
	v.Start(A, "eu-west-1")
	c.advance(s(10))
	v.Done(done("requests", A, 5, "", ""))
	v.Start(B, "eu-west-1")
	v.Start("333333333333", "eu-west-1")
	c.advance(s(30))
	eq(t, st.last(), "  Requests [███████░░░░░░░░░░░░░]  33% · 1/3 regions · 5 workloads · 0:40 · waiting for "+B+" eu-west-1 (0:30), 333333333333 eu-west-1 (0:30)")
	v.Done(done("requests", B, 2, "", "Throttling: rate exceeded"))
	v.Done(done("requests", "333333333333", 1, discover.FromCache, ""))
	v.End()
	eq(t, st.ends, []string{"  Requests [████████████████████] 100% · 3/3 regions · 8 workloads · 1 failed · 0:40"})
	eq(t, len(c.timers), 0)
}

func TestRegionsElsewhere(t *testing.T) {
	c := newFake()
	var logs []string
	v := RegionsView("Uptime", Options{Now: c.now, Every: c.every, Log: func(l string) { logs = append(logs, l) }})
	v.Plan(2)
	v.Start(A, "eu-west-1")
	v.Start(B, "eu-west-1")
	v.Done(done("uptime", A, 1, discover.FromCache, ""))
	c.advance(time.Minute)
	v.End()
	eq(t, logs, []string{"  uptime " + A + " eu-west-1: 1 workload (cache)", "  uptime: still reading 1 region: " + B + " eu-west-1 (1:00)"})
}

func TestRegionsNothingToRead(t *testing.T) {
	c := newFake()
	st := &status{}
	v := RegionsView("Uptime", Options{Now: c.now, Every: c.every, Status: st})
	v.Plan(0)
	v.End()
	eq(t, st.ends, []string(nil))
}

// Review focus 3: without a Status, no line has a carriage return or an escape code.
func TestNonTerminalLinesHaveNoControlCodes(t *testing.T) {
	c := newFake()
	var logs []string
	log := func(l string) { logs = append(logs, l) }
	d := DiscoveryView(1, Options{Now: c.now, Every: c.every, Log: log})
	d.Account(discover.AccountEvent{Account: A, State: "started", Regions: 1})
	c.advance(time.Minute)
	d.Region(region(A, "eu-west-1", 1, "", ""))
	d.Account(discover.AccountEvent{Account: A, State: "done", Regions: 1})
	d.End()
	r := RegionsView("Requests", Options{Now: c.now, Every: c.every, Log: log})
	r.Plan(1)
	r.Start(A, "eu-west-1")
	c.advance(time.Minute)
	r.Done(done("requests", A, 1, "", "boom"))
	r.End()
	if len(logs) < 4 {
		t.Fatalf("logs: %q", logs)
	}
	for _, l := range logs {
		if strings.ContainsAny(l, "\r\x1b") {
			t.Fatalf("control code in %q", l)
		}
	}
}

func TestTickerStopsAndRunsFn(t *testing.T) {
	var mu sync.Mutex
	n := 0
	stop := Ticker(5*time.Millisecond, func() { mu.Lock(); n++; mu.Unlock() })
	time.Sleep(40 * time.Millisecond)
	stop()
	stop()
	mu.Lock()
	seen := n
	mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if seen == 0 || n != seen {
		t.Fatalf("ticks %d then %d", seen, n)
	}
}

func TestConcurrentEvents(t *testing.T) {
	st := &status{}
	v := DiscoveryView(4, Options{Status: st, Every: newFake().every})
	r := RegionsView("Requests", Options{Every: newFake().every})
	r.Plan(40)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("acct%d", i%4)
			v.Account(discover.AccountEvent{Account: id, State: "started", Regions: 10})
			v.Region(region(id, "r", 1, "", ""))
			r.Start(id, fmt.Sprint(i))
			r.Done(done("requests", id, 1, "", ""))
		}()
	}
	wg.Wait()
	v.End()
	r.End()
}

func TestPluralsAndClock(t *testing.T) {
	eq(t, plural(1, "region"), "1 region")
	eq(t, plural(0, "region"), "0 regions")
	eq(t, clock(3600*time.Second+5*time.Second), "60:05")
	eq(t, bar(0.125), "[███░░░░░░░░░░░░░░░░░]  13%")
}
