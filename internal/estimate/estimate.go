// Each eligible workload of a region elected or left out: its requests replayed by the product's idle window advisor
// at the report's window and at its recommended one, priced by the price book; what cannot be estimated, with the
// reason; those waking more than --max-wakes a day apart.
//
// Ports manager packages/estimate/src/estimate.ts (estimateAll: its loop over the regions and their workloads).
package estimate

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/Dormeto/cli/internal/accounts"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/options"
	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/replay"
	"github.com/Dormeto/cli/internal/stopped"
)

// wakeMs: a wake lasts 60 seconds in the replay.
const wakeMs = 60_000

const dayMs = 86_400_000

const nothing = "nothing running to stop (an Auto Scaling group at 0, an ECS service on EC2 capacity, or a stopped instance)"

// Input is what All estimates (estimateAll's argument).
type Input struct {
	Reached  accounts.Reached
	Regions  []discover.RegionDiscovery
	Requests []metrics.RegionRequests
	// The price book's hourly price of an item in a region (its discounts closed over); nil: no list price. An error
	// is no price either, as Node's catch.
	Hourly func(ctx context.Context, region string, item stopped.Item) (*float64, error)
	// A price lookup AWS refused (the Price List API, Spot price history): unpriced workloads say so. Nil or "": none.
	PriceFailure func() string
	Baseline     prices.Baseline
	// The options (Out and Profiles are left out of the estimate's JSON).
	Options options.Options
	Now     time.Time
	// The workloads excluded by their tags (ExcludeByTags).
	Excluded []Excluded
	// With --ec2-uptime, --ecs-uptime or --rds-uptime: when the workloads and databases ran. Nil: not measured (Node's
	// null); a slice, even empty: measured.
	Uptime []metrics.RegionUptime
}

// state is an estimate being made: what each region adds.
type state struct {
	ctx              context.Context
	in               Input
	workloads        []WorkloadEstimate
	leftOut          []LeftOut
	busy             []Busy
	small            []Small
	databasesLeftOut []DatabaseLeftOut
}

// elected is a workload kept at its recommended window, its companion databases attached once every workload of its
// region is known.
type elected struct {
	index    int // in its region's workloads: Node's object identity
	workload discover.Workload
	read     *metrics.RegionRequests
	minutes  [][2]int
	quiet    bool
	days     float64
	// When it ran (measured), else its requests' period: its actual cost's.
	running     []replay.Period
	measured    *Uptime
	advice      replay.Result
	wakesPerDay float64
	companions  []discover.Database
}

// All is the estimate of the regions' workloads. Its error is the context's when done (a price lookup it cancelled
// would otherwise read as a missing list price), or a replay's (an unknown time zone, a date that is not one).
func All(ctx context.Context, in Input) (Estimate, error) {
	s := &state{ctx: ctx, in: in, workloads: []WorkloadEstimate{}, leftOut: []LeftOut{}, busy: []Busy{}, small: []Small{}, databasesLeftOut: []DatabaseLeftOut{}}
	for _, region := range in.Regions {
		if err := s.region(region); err != nil {
			return Estimate{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Estimate{}, err
	}
	return s.estimate(), nil
}

func (s *state) leave(w discover.Workload, reason string) {
	s.leftOut = append(s.leftOut, LeftOut{w, reason})
}

// region estimates a region's workloads.
func (s *state) region(region discover.RegionDiscovery) error {
	var read *metrics.RegionRequests
	if i := slices.IndexFunc(s.in.Requests, func(r metrics.RegionRequests) bool { return r.Account == region.Account && r.Region == region.Region }); i >= 0 {
		read = &s.in.Requests[i]
	}
	var ran *metrics.RegionUptime
	if i := slices.IndexFunc(s.in.Uptime, func(u metrics.RegionUptime) bool { return u.Account == region.Account && u.Region == region.Region }); i >= 0 {
		ran = &s.in.Uptime[i]
	}
	// A backend behind several target groups is estimated once, at its first, with every target group's requests: it
	// can nap only when none of them is called.
	key := func(w discover.Workload) string { return w.Backend.Kind + "#" + w.Backend.ID }
	sharing := map[string][]int{}
	for i, w := range region.Workloads {
		sharing[key(w)] = append(sharing[key(w)], i)
	}
	chosen := []*elected{}
	for i, w := range region.Workloads {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		group := sharing[key(w)]
		if group[0] != i {
			s.leave(w, fmt.Sprintf("its backend (%s %s) also serves %s: estimated there, with every target group's requests", w.Backend.Kind, w.Backend.ID, region.Workloads[group[0]].TargetGroup))
			continue
		}
		e, err := s.elect(region, i, group, read, ran)
		if err != nil {
			return err
		}
		if e != nil {
			chosen = append(chosen, e)
		}
	}
	s.attach(region, chosen, ran)
	for _, e := range chosen {
		if err := s.price(e, ran); err != nil {
			return err
		}
	}
	return nil
}

// elect replays a workload: nil when it is left out, or busy.
func (s *state) elect(region discover.RegionDiscovery, i int, group []int, read *metrics.RegionRequests, ran *metrics.RegionUptime) (*elected, error) {
	w := region.Workloads[i]
	o := s.in.Options
	measure := s.in.Uptime != nil && metrics.Measured(w, metrics.UptimeKinds{EC2: o.Ec2Uptime, ECS: o.EcsUptime})
	stoppedNow := w.InstanceState == "stopped" || w.InstanceState == "stopping"
	if stoppedNow && !measure {
		s.leave(w, "a stopped instance: nothing running to stop (--ec2-uptime estimates it from the hours it ran)")
		return nil, nil
	}
	if w.EcsDesired != nil && *w.EcsDesired == 0 && !measure {
		s.leave(w, "an ECS service at 0 tasks: nothing running to stop (--ecs-uptime estimates it as one task from the hours it ran)")
		return nil, nil
	}
	if len(w.Stopped) == 0 {
		s.leave(w, nothing)
		return nil, nil
	}
	// Measured: its running periods; unreadable, 24/7 (nil).
	var running []replay.Period
	var measured *Uptime
	if measure {
		var periods [][2]int
		has := false
		if ran != nil {
			periods, has = ran.Running[w.Backend.ID]
		}
		if has {
			if len(periods) == 0 {
				s.leave(w, fmt.Sprintf("it did not run in the last %d days (its measured uptime)", s.in.Options.DaysRead()))
				return nil, nil
			}
			from, err := parseISO(ran.From)
			if err != nil {
				return nil, err
			}
			to, err := parseISO(ran.To)
			if err != nil {
				return nil, err
			}
			running = periodsFrom(from, periods)
			measured = hoursPerDay(periods, float64(to.UnixMilli()-from.UnixMilli())/dayMs)
		} else {
			why := "not read"
			if u, ok := unreadableOf(ran, w.Backend.ID); ok {
				why = u
			}
			measured = &Uptime{Unreadable: &why}
		}
	}
	// Every target group's requests, read (an empty series too).
	unreadable := ""
	series := make([][][2]float64, 0, len(group))
	complete := read != nil
	for _, j := range group {
		if read == nil {
			break
		}
		arn := region.Workloads[j].TargetGroupArn
		if u := read.Unreadable[arn]; u != "" && unreadable == "" {
			unreadable = u
		}
		m, ok := read.Minutes[arn]
		complete = complete && ok
		series = append(series, m)
	}
	if unreadable != "" || !complete {
		if unreadable == "" {
			unreadable = "not read"
		}
		s.leave(w, "requests unreadable: "+unreadable)
		return nil, nil
	}
	minutes := merge(series)
	if i := slices.IndexFunc(w.Stopped, func(item stopped.Item) bool { return item.Kind == stopped.KindUnknown }); i >= 0 {
		reason := w.Stopped[i].Reason
		if reason == "" {
			reason = "unknown"
		}
		s.leave(w, "its backend could not be described ("+reason+")")
		return nil, nil
	}
	advice, err := s.replay(w, read, minutes, w.Stopped, running)
	if err != nil {
		return nil, err
	}
	if !advice.Priced {
		failure := ""
		if s.in.PriceFailure != nil {
			failure = s.in.PriceFailure()
		}
		if failure != "" {
			s.leave(w, "prices unreadable ("+failure+")")
		} else {
			s.leave(w, "what it stops has no AWS list price")
		}
		return nil, nil
	}
	best, err := rowOf(advice, advice.Recommended)
	if err != nil {
		return nil, err
	}
	// Judged at its recommended window, the one a customer would set.
	if o.MaxWakes != nil && best.WakesPerDay > *o.MaxWakes {
		s.busy = append(s.busy, Busy{Workload: w, WakesPerDay: best.WakesPerDay, USDMonth: usdOf(best), Minutes: advice.Recommended})
		return nil, nil
	}
	from, err := parseISO(read.From)
	if err != nil {
		return nil, err
	}
	to, err := parseISO(read.To)
	if err != nil {
		return nil, err
	}
	if running == nil {
		running = []replay.Period{{From: read.From, To: read.To}}
	}
	return &elected{
		index: i, workload: w, read: read, minutes: minutes, quiet: len(minutes) == 0, days: float64(to.UnixMilli()-from.UnixMilli()) / dayMs,
		running: running, measured: measured, advice: advice, wakesPerDay: best.WakesPerDay,
	}, nil
}

// unreadableOf is why an id's uptime could not be read, when the region's uptime says so.
func unreadableOf(ran *metrics.RegionUptime, id string) (string, bool) {
	if ran == nil {
		return "", false
	}
	why, ok := ran.Unreadable[id]
	return why, ok
}

// replay is the product's advice for a workload's requests, with what a nap stops and when it ran (nil: all the time).
func (s *state) replay(w discover.Workload, read *metrics.RegionRequests, minutes [][2]int, items []stopped.Item, running []replay.Period) (replay.Result, error) {
	wake := float64(wakeMs)
	return replay.Advice(replay.Input{
		Ctx: s.ctx, Requests: replay.RequestMinutes{From: read.From, To: read.To, Minutes: minutes}, Current: s.in.Options.ReplayWindow(), CheckMinutes: 5,
		WakeMs: &wake, StartAsleep: false, ActualWakes: 0, ActualFrom: nil, Stopped: items, Hourly: s.priceOf(w.Region), Baseline: s.in.Baseline,
		WakerTargetGroup: false, Now: s.in.Now, Running: running,
	})
}

// priceOf is the price book in a region: a lookup that fails is no price.
func (s *state) priceOf(region string) func(context.Context, stopped.Item) (*float64, error) {
	return func(ctx context.Context, item stopped.Item) (*float64, error) {
		if s.in.Hourly == nil {
			return nil, nil
		}
		usd, err := s.in.Hourly(ctx, region, item)
		if err != nil {
			return nil, nil
		}
		return usd, nil
	}
}

// merge adds up the target groups' requests minute by minute, sorted by minute. A count is only asked whether it is
// above 0 (the replay's [2]int keeps it so, rounded up).
func merge(series [][][2]float64) [][2]int {
	merged := map[int]float64{}
	order := []int{}
	for _, minutes := range series {
		for _, m := range minutes {
			minute := int(m[0])
			if _, seen := merged[minute]; !seen {
				order = append(order, minute)
			}
			merged[minute] += m[1]
		}
	}
	slices.Sort(order)
	out := make([][2]int, len(order))
	for i, minute := range order {
		out[i] = [2]int{minute, int(math.Ceil(merged[minute]))}
	}
	return out
}

// periodsFrom are periods in minutes from a date ([start, end)) as dates.
func periodsFrom(from time.Time, periods [][2]int) []replay.Period {
	out := make([]replay.Period, len(periods))
	for i, p := range periods {
		out[i] = replay.Period{From: isoOf(from.Add(time.Duration(p[0]) * time.Minute)), To: isoOf(from.Add(time.Duration(p[1]) * time.Minute))}
	}
	return out
}

// hoursPerDay is the hours a day the periods (in minutes) last over the days, added as Node does.
func hoursPerDay(periods [][2]int, days float64) *Uptime {
	n := 0.0
	for _, p := range periods {
		n = n + float64(p[1]) - float64(p[0])
	}
	v := n / 60 / days
	return &Uptime{HoursPerDay: &v}
}

// rowOf is the advice's row of a window.
func rowOf(a replay.Result, minutes int) (replay.Row, error) {
	if i := slices.IndexFunc(a.Windows, func(r replay.Row) bool { return r.Minutes == minutes }); i >= 0 {
		return a.Windows[i], nil
	}
	return replay.Row{}, fmt.Errorf("estimate: no %d-minute window in the advice", minutes)
}

// usdOf is a priced row's saving a month.
func usdOf(r replay.Row) float64 {
	if r.USDMonth == nil {
		return 0
	}
	return *r.USDMonth
}
