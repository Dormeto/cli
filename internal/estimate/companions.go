// Companion databases (the user, 2026-10-11): a database whose security group lets in an elected workload's naps with
// it, unless another workload of the region reaches it too (excluded ones included: it would cut them off), its tags
// are excluded, it cannot be stopped, or its workload wakes more than --max-wakes-rds a day (it takes minutes to
// start). Each elected workload priced: its actual cost, each companion replayed with the workload's requests while it
// was available and added, then those saving less than --min-saving a month apart.
//
// Ports manager packages/estimate/src/estimate.ts (estimateAll: the companion databases and the elected workloads'
// loop).
package estimate

import (
	"fmt"
	"slices"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/replay"
	"github.com/Dormeto/cli/internal/stopped"
)

// attach gives each database of the region to the elected workload its security group lets in, or leaves it out with
// why.
func (s *state) attach(region discover.RegionDiscovery, chosen []*elected, ran *metrics.RegionUptime) {
	o := s.in.Options
	// The region's workloads (their index, Node's identity), then those excluded by their tags.
	inRegion := slices.Clone(region.Workloads)
	for _, x := range s.in.Excluded {
		if x.Workload.Account == region.Account && x.Workload.Region == region.Region {
			inRegion = append(inRegion, x.Workload)
		}
	}
	for _, d := range region.Databases {
		users := []int{}
		for j, w := range inRegion {
			if slices.ContainsFunc(d.AllowedFrom, func(g string) bool { return slices.Contains(w.SecurityGroups, g) }) {
				users = append(users, j)
			}
		}
		i := slices.IndexFunc(chosen, func(e *elected) bool { return slices.Contains(users, e.index) })
		if i < 0 {
			continue
		}
		owner := chosen[i]
		leave := func(code LeftOutCode, reason string) {
			s.databasesLeftOut = append(s.databasesLeftOut, DatabaseLeftOut{Workload: owner.workload, Database: DatabaseRef{Kind: d.Kind, ID: d.ID}, Reason: reason, Code: code})
		}
		matched, excluded := matching(o.ExcludeTags, d.Tags)
		// Measured (--rds-uptime): when it was available; one stopped when discovered is estimated only so.
		key := metrics.DatabaseKey(d)
		periods, measured := databasePeriods(o.RdsUptime, ran, key)
		unread, _ := unreadableOf(ran, key)
		switch {
		case len(users) > 1:
			leave(CodeShared, fmt.Sprintf("reached by %d workloads: stopping it with one would cut the others off", len(users)))
		case excluded:
			leave(CodeTags, "excluded by its tags ("+DescribeTags(matched)+")")
		case d.NotStoppable != "":
			leave(CodeStoppable, d.NotStoppable)
		case o.MaxWakesRds != nil && owner.wakesPerDay > *o.MaxWakesRds:
			leave(CodeWakes, fmt.Sprintf("its workload wakes %s times a day at its recommended window, more than --max-wakes-rds %s (a database takes minutes to start)",
				toFixed1(owner.wakesPerDay), JSNumber(*o.MaxWakesRds)))
		case !measured && (d.Status == "stopped" || d.Status == "stopping") && unread == "":
			leave(CodeStopped, "stopped when discovered (--rds-uptime estimates it from the hours it ran)")
		case measured && len(periods) == 0:
			leave(CodeIdle, "it did not run in the last 14 days (its measured uptime)")
		default:
			owner.companions = append(owner.companions, d)
		}
	}
}

// databasePeriods are when a database was available, with --rds-uptime and when its region's uptime has them.
func databasePeriods(rdsUptime bool, ran *metrics.RegionUptime, key string) ([][2]int, bool) {
	if !rdsUptime || ran == nil {
		return nil, false
	}
	periods, ok := ran.Running[key]
	return periods, ok
}

// price adds an elected workload with its companions to the estimate, or apart when it saves less than --min-saving.
func (s *state) price(e *elected, ran *metrics.RegionUptime) error {
	o := s.in.Options
	w := e.workload
	at, err := rowOf(e.advice, o.Window)
	if err != nil {
		return err
	}
	best, err := rowOf(e.advice, e.advice.Recommended)
	if err != nil {
		return err
	}
	// Its cost now: asleep never (while it ran, measured), priced as the savings are (the same baseline, prices and
	// month).
	actual, err := s.cost(w.Region, e.running, w.Stopped)
	if err != nil {
		return err
	}
	usdWindow := usdOf(at)
	usdRecommended := usdOf(best)
	// Each companion replayed with the workload's requests while it was available (measured, else all the time): its
	// saving and its cost added to the workload's.
	databases := []Companion{}
	readFrom, err := parseISO(e.read.From)
	if err != nil {
		return err
	}
	for _, d := range e.companions {
		key := metrics.DatabaseKey(d)
		periods, measured := databasePeriods(o.RdsUptime, ran, key)
		var available []replay.Period // nil: all the time
		if measured {
			available = periodsFrom(readFrom, periods)
		}
		own, err := s.replay(w, e.read, e.minutes, d.Stopped, available)
		if err != nil {
			return err
		}
		if !own.Priced {
			s.databasesLeftOut = append(s.databasesLeftOut, DatabaseLeftOut{Workload: w, Database: DatabaseRef{Kind: d.Kind, ID: d.ID}, Reason: "what it stops has no AWS list price", Code: CodePrice})
			continue
		}
		var uptime *Uptime
		if measured {
			uptime = hoursPerDay(periods, e.days)
		} else if why, _ := unreadableOf(ran, key); o.RdsUptime && why != "" {
			uptime = &Uptime{Unreadable: &why}
		}
		ownAt, err := rowOf(own, o.Window)
		if err != nil {
			return err
		}
		ownBest, err := rowOf(own, e.advice.Recommended)
		if err != nil {
			return err
		}
		usdAt, usdBest := usdOf(ownAt), usdOf(ownBest)
		databases = append(databases, Companion{Kind: d.Kind, ID: d.ID, USDMonth: usdAt, RecommendedUSDMonth: usdBest, Uptime: uptime})
		usdWindow += usdAt
		usdRecommended += usdBest
		if available == nil {
			available = []replay.Period{{From: e.read.From, To: e.read.To}}
		}
		c, err := s.cost(w.Region, available, d.Stopped)
		if err != nil {
			return err
		}
		actual += c
	}
	// With its databases: worth installing Dormeto for?
	if o.MinSaving != nil && usdRecommended < *o.MinSaving {
		s.small = append(s.small, Small{Workload: w, USDMonth: usdRecommended, Minutes: e.advice.Recommended})
		return nil
	}
	x := WorkloadEstimate{
		Workload: w, Quiet: e.quiet, ActualMonth: (actual * 30) / e.days, Uptime: e.measured,
		AtWindow:    AtWindow{USDMonth: usdWindow, WakesPerDay: at.WakesPerDay, AsleepHours: at.AsleepHours},
		Recommended: Recommended{Minutes: e.advice.Recommended, USDMonth: usdRecommended, WakesPerDay: best.WakesPerDay},
	}
	if len(databases) > 0 {
		x.Databases = databases
	}
	s.workloads = append(s.workloads, x)
	return nil
}

// cost is what the items cost while running over the periods (all asleep), priced as the savings are.
func (s *state) cost(region string, periods []replay.Period, items []stopped.Item) (float64, error) {
	phases := make([]prices.Phase, len(periods))
	for i, p := range periods {
		phases[i] = prices.Phase{State: "ASLEEP", From: p.From, To: p.To, Stopped: items}
	}
	savings, err := prices.SavingsOf(s.ctx, phases, s.priceOf(region), s.in.Baseline)
	return savings.USD, err
}
