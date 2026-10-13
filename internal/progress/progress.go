// Package progress is the estimate's progress lines, ported from the Node estimate's packages/estimate/src/progress.ts:
// discovery and phase 2's CloudWatch reads (requests, uptime). With a Status (a terminal), one line rewritten in place,
// redrawn every second; without (a log, CI), one line per account or region done and, every minute, those still read.
//
// This package never writes an escape code or a carriage return: the terminal Status, with those, is the entry point's.
package progress

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Dormeto/cli/internal/discover"
)

// Status is a line rewritten in place: Update redraws it, End leaves its last text.
type Status interface {
	Update(line string)
	End(line string)
}

// Every runs fn every d until the returned stop is called. The default never keeps the process alive.
type Every func(d time.Duration, fn func()) (stop func())

// Ticker is the default Every: a time.Ticker in a goroutine. Stop returns once the goroutine has ended, so fn does not
// run after it (do not call stop from fn).
func Ticker(d time.Duration, fn func()) func() {
	t := time.NewTicker(d)
	quit := make(chan struct{})
	ended := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(ended)
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				select {
				case <-quit:
					return
				default:
				}
				fn()
			}
		}
	}()
	return func() { once.Do(func() { t.Stop(); close(quit) }); <-ended }
}

const (
	barWidth = 20
	// The accounts (regions) still read are named once this few remain.
	named = 3
)

func plural(count int, word string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, word)
	}
	return fmt.Sprintf("%d %ss", count, word)
}

func clock(d time.Duration) string {
	s := int(d / time.Second)
	if s < 0 {
		s = 0
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// round is JavaScript's Math.round for the non-negative: halves go up.
func round(x float64) int { return int(math.Floor(x + 0.5)) }

func bar(share float64) string {
	filled := round(share * barWidth)
	if filled < 0 {
		filled = 0
	} else if filled > barWidth {
		filled = barWidth
	}
	return fmt.Sprintf("[%s%s] %3d%%", strings.Repeat("█", filled), strings.Repeat("░", barWidth-filled), round(share*100))
}

// stillReading is the minute's line for those (accounts or regions) still read.
func stillReading(prefix string, noun string, still []string) (string, bool) {
	if len(still) == 0 {
		return "", false
	}
	first := still
	if len(first) > 5 {
		first = first[:5]
	}
	more := ""
	if len(still) > 5 {
		more = fmt.Sprintf(" and %d more", len(still)-5)
	}
	return fmt.Sprintf("  %s: still reading %s: %s%s", prefix, plural(len(still), noun), strings.Join(first, ", "), more), true
}

// Options are what both views take. Zero Now, Log and Every get time.Now, a no-op and Ticker; a nil Status is not a
// terminal.
type Options struct {
	Now    func() time.Time
	Log    func(line string)
	Status Status
	Every  Every
}

func (o Options) fill() Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = func(string) {}
	}
	if o.Every == nil {
		o.Every = Ticker
	}
	return o
}

type account struct {
	regions, scanned, workloads, cached, failed int
	done                                        bool
	started                                     *time.Time
}

// Discovery is the discovery phase's progress. Safe for concurrent use.
type Discovery struct {
	mu       sync.Mutex
	o        Options
	accounts int
	begin    time.Time
	order    []string
	of       map[string]*account
	stop     func()
}

// DiscoveryView starts the view for the given number of accounts; call End when discovery is over.
func DiscoveryView(accounts int, o Options) *Discovery {
	o = o.fill()
	d := &Discovery{o: o, accounts: accounts, begin: o.Now(), of: map[string]*account{}}
	if o.Status != nil {
		d.stop = o.Every(time.Second, func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			o.Status.Update(d.line())
		})
	} else {
		d.stop = o.Every(time.Minute, func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			if l, ok := stillReading("discovery", "account", d.reading()); ok {
				o.Log(l)
			}
		})
	}
	return d
}

func (d *Discovery) account(id string) *account {
	a, ok := d.of[id]
	if !ok {
		a = &account{}
		d.of[id] = a
		d.order = append(d.order, id)
	}
	return a
}

// reading is the accounts started and not done, with how long each has taken.
func (d *Discovery) reading() []string {
	out := []string{}
	now := d.o.Now()
	for _, id := range d.order {
		a := d.of[id]
		if !a.done && a.started != nil {
			out = append(out, fmt.Sprintf("%s (%s)", id, clock(now.Sub(*a.started))))
		}
	}
	return out
}

func (d *Discovery) line() string {
	var scanned, workloads, failed, done int
	var progress float64
	for _, a := range d.of {
		scanned += a.scanned
		workloads += a.workloads
		failed += a.failed
		switch {
		case a.done:
			done++
			progress++
		case a.regions > 0:
			progress += math.Min(float64(a.scanned)/float64(a.regions), 1)
		}
	}
	// An account done counts whole, one started by the share of its regions scanned, one not started yet nothing.
	share := 1.0
	if d.accounts > 0 {
		share = math.Min(progress/float64(d.accounts), 1)
	}
	var waiting []string
	if d.accounts-done <= named {
		waiting = d.reading()
	}
	s := fmt.Sprintf("  Discovery %s · %d/%s · %s · %s", bar(share), done, plural(d.accounts, "account"), plural(scanned, "region"), plural(workloads, "workload"))
	if failed > 0 {
		s += fmt.Sprintf(" · %d failed", failed)
	}
	s += " · " + clock(d.o.Now().Sub(d.begin))
	if len(waiting) > 0 {
		s += " · waiting for " + strings.Join(waiting, ", ")
	}
	return s
}

// Account takes an account's started and done events (discover's AccountProgress).
func (d *Discovery) Account(e discover.AccountEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.account(e.Account)
	a.regions = e.Regions
	if e.State == discover.AccountStarted {
		t := d.o.Now()
		a.started = &t
	}
	if e.State == discover.AccountDone {
		a.done = true
		if e.Error != "" {
			a.failed++
		}
		if d.o.Status == nil {
			finished := 0
			for _, x := range d.of {
				if x.done {
					finished++
				}
			}
			position := fmt.Sprintf("%d/%d", finished, d.accounts)
			if e.Error != "" && a.scanned == 0 {
				d.o.Log(fmt.Sprintf("  discovery %s (%s): failed (%s)", e.Account, position, e.Error))
			} else {
				l := fmt.Sprintf("  discovery %s (%s): %s, %s", e.Account, position, plural(a.scanned, "region"), plural(a.workloads, "workload"))
				if a.cached > 0 {
					l += fmt.Sprintf(", %d from the cache", a.cached)
				}
				if a.failed > 0 {
					l += fmt.Sprintf(", %d failed", a.failed)
				}
				d.o.Log(l)
			}
		}
	}
	if d.o.Status != nil {
		d.o.Status.Update(d.line())
	}
}

// Region takes a region's scanned event (discover's Progress).
func (d *Discovery) Region(p discover.ProgressEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.account(p.Account)
	a.scanned++
	a.workloads += p.Workloads
	if p.From == discover.FromCache {
		a.cached++
	}
	if p.Error != "" {
		a.failed++
	}
	if d.o.Status != nil {
		d.o.Status.Update(d.line())
	}
}

// End stops the redraws and leaves the last line.
func (d *Discovery) End() {
	d.stop()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.o.Status != nil {
		d.o.Status.End(d.line())
	}
}

// Regions is phase 2's CloudWatch reads by region. Safe for concurrent use.
type Regions struct {
	mu                          sync.Mutex
	o                           Options
	label, phase                string
	begin                       time.Time
	order                       []string
	reading                     map[string]time.Time
	total, done, workloads, bad int
	stop                        func()
}

// RegionsView starts the view for label "Requests" or "Uptime"; call End when the phase is over.
func RegionsView(label string, o Options) *Regions {
	o = o.fill()
	r := &Regions{o: o, label: label, phase: strings.ToLower(label), begin: o.Now(), reading: map[string]time.Time{}}
	if o.Status != nil {
		r.stop = o.Every(time.Second, func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.total > 0 {
				o.Status.Update(r.line())
			}
		})
	} else {
		r.stop = o.Every(time.Minute, func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if l, ok := stillReading(r.phase, "region", r.waiting()); ok {
				o.Log(l)
			}
		})
	}
	return r
}

func (r *Regions) waiting() []string {
	out := []string{}
	now := r.o.Now()
	for _, k := range r.order {
		out = append(out, fmt.Sprintf("%s (%s)", k, clock(now.Sub(r.reading[k]))))
	}
	return out
}

func (r *Regions) line() string {
	var waiting []string
	if r.total-r.done <= named {
		waiting = r.waiting()
	}
	share := 1.0
	if r.total > 0 {
		share = float64(r.done) / float64(r.total)
	}
	s := fmt.Sprintf("  %s %s · %d/%s · %s", r.label, bar(share), r.done, plural(r.total, "region"), plural(r.workloads, "workload"))
	if r.bad > 0 {
		s += fmt.Sprintf(" · %d failed", r.bad)
	}
	s += " · " + clock(r.o.Now().Sub(r.begin))
	if len(waiting) > 0 {
		s += " · waiting for " + strings.Join(waiting, ", ")
	}
	return s
}

// Plan says how many regions are to be read (metrics' OnPlan).
func (r *Regions) Plan(count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total = count
	if r.total > 0 && r.o.Status != nil {
		r.o.Status.Update(r.line())
	}
}

// Start says a region's read began (metrics' OnStart).
func (r *Regions) Start(account, region string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := account + " " + region
	if _, ok := r.reading[k]; !ok {
		r.order = append(r.order, k)
	}
	r.reading[k] = r.o.Now()
	if r.o.Status != nil {
		r.o.Status.Update(r.line())
	}
}

// Done takes a region's read event (metrics' Progress).
func (r *Regions) Done(p discover.ProgressEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := p.Account + " " + p.Region
	if _, ok := r.reading[k]; ok {
		delete(r.reading, k)
		for i, x := range r.order {
			if x == k {
				r.order = append(r.order[:i], r.order[i+1:]...)
				break
			}
		}
	}
	r.done++
	r.workloads += p.Workloads
	if p.Error != "" {
		r.bad++
	}
	if r.o.Status == nil {
		what := plural(p.Workloads, "workload")
		if p.Error != "" {
			what = fmt.Sprintf("failed (%s)", p.Error)
		}
		cached := ""
		if p.From == discover.FromCache {
			cached = " (cache)"
		}
		r.o.Log(fmt.Sprintf("  %s %s %s: %s%s", p.Phase, p.Account, p.Region, what, cached))
	} else {
		r.o.Status.Update(r.line())
	}
}

// End stops the redraws and, if there was anything to read, leaves the last line.
func (r *Regions) End() {
	r.stop()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.total > 0 && r.o.Status != nil {
		r.o.Status.End(r.line())
	}
}
