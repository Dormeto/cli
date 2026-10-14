// The totals, with Dormeto's cost: a router per account and region with workloads, each workload, the manager once;
// each account's and region's workloads not estimated, whatever the reason; the estimate as the reports read it.
// Dormeto already installed (Go only, the user, 2026-10-13): its cost now and what the recommendation changes in it.
//
// Ports manager packages/estimate/src/estimate.ts (estimateAll: its totals and its result).
package estimate

import (
	"github.com/Dormeto/cli/internal/accounts"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/prices"
	"time"
)

// kindOf: the Kinds counter of a backend kind; none for a kind discovery does not give (Node would count it under an
// undefined key).
func kindOf(k *Kinds, kind string) *int {
	switch kind {
	case discover.KindInstance:
		return &k.EC2
	case discover.KindASG:
		return &k.ASG
	case discover.KindECS:
		return &k.ECS
	case discover.KindK8s:
		return &k.EKS
	}
	return nil
}

// tally is totals by key (an account, a region), in the order the keys came.
type tally struct {
	keys []string
	by   map[string]*Totals
}

func (t *tally) of(key string) *Totals {
	if t.by == nil {
		t.by = map[string]*Totals{}
	}
	if p, ok := t.by[key]; ok {
		return p
	}
	p := &Totals{}
	t.by[key] = p
	t.keys = append(t.keys, key)
	return p
}

func (t *tally) each() []*Totals {
	out := make([]*Totals, len(t.keys))
	for i, k := range t.keys {
		out[i] = t.by[k]
	}
	return out
}

func (t *tally) ordered() Ordered[Totals] {
	var o Ordered[Totals]
	for _, k := range t.keys {
		o.Set(k, *t.by[k])
	}
	return o
}

// estimate is the estimate of what the regions added.
func (s *state) estimate() Estimate {
	cost := prices.DormetoCost
	totals := &Totals{}
	var byAccount, byRegion tally
	routed := []Where{}
	seen := map[string]bool{}
	for _, x := range s.workloads {
		where := x.Workload.Account + "#" + x.Workload.Region
		router := cost.Router
		if seen[where] {
			router = 0
		} else {
			seen[where] = true
			routed = append(routed, Where{Account: x.Workload.Account, Region: x.Workload.Region})
		}
		for _, t := range []*Totals{totals, byAccount.of(x.Workload.Account), byRegion.of(x.Workload.Region)} {
			t.Workloads += 1
			if n := kindOf(&t.Kinds, x.Workload.Backend.Kind); n != nil {
				*n += 1
			}
			t.Databases += len(x.Databases)
			t.ActualMonth += x.ActualMonth
			t.USDMonthWindow += x.AtWindow.USDMonth
			t.USDMonthRecommended += x.Recommended.USDMonth
			t.DormetoMonth += router + cost.Workload
		}
	}
	// Each account's (and region's) workloads not estimated, whatever the reason: an account whose all are is listed too.
	var notEstimated []discover.Workload
	for _, x := range s.in.Excluded {
		notEstimated = append(notEstimated, x.Workload)
	}
	for _, x := range s.leftOut {
		notEstimated = append(notEstimated, x.Workload)
	}
	for _, x := range s.busy {
		notEstimated = append(notEstimated, x.Workload)
	}
	for _, x := range s.small {
		notEstimated = append(notEstimated, x.Workload)
	}
	for _, x := range notEstimated {
		for _, t := range []*Totals{totals, byAccount.of(x.Account), byRegion.of(x.Region)} {
			t.Excluded += 1
		}
	}
	// The onboarded workloads staying though not estimated: in Dormeto's cost of their account and region, not in the
	// savings.
	installed := s.installation()
	for _, w := range installed.staying {
		where := w.Account + "#" + w.Region
		router := cost.Router
		if seen[where] {
			router = 0
		} else {
			seen[where] = true
		}
		for _, t := range []*Totals{totals, byAccount.of(w.Account), byRegion.of(w.Region)} {
			t.DormetoMonth += router + cost.Workload
		}
	}
	if len(s.workloads) > 0 || len(installed.staying) > 0 {
		totals.DormetoMonth += cost.Manager
	}
	for _, t := range append(append([]*Totals{totals}, byAccount.each()...), byRegion.each()...) {
		t.WithDormetoMonth = t.ActualMonth - t.USDMonthWindow + t.DormetoMonth
	}
	part := func(count int, unitMonth float64) CostPart {
		return CostPart{Count: count, UnitMonth: unitMonth, USDMonth: float64(count) * unitMonth}
	}
	managers := 0
	if len(s.workloads) > 0 {
		managers = 1
	}
	dormeto := DormetoCost{
		Manager:   part(managers, cost.Manager),
		Routers:   Routers{CostPart: part(len(routed), cost.Router), Where: routed},
		Workloads: part(len(s.workloads), cost.Workload),
		USDMonth:  totals.DormetoMonth,
	}
	if len(installed.now) > 0 {
		// The installation now, then the recommendation's change: the routers of the accounts and regions with workloads
		// once it is applied (seen) against those with workloads onboarded now.
		now := []Where{}
		onboardedIn := map[string]bool{}
		for _, w := range installed.now {
			if where := w.Account + "#" + w.Region; !onboardedIn[where] {
				onboardedIn[where] = true
				now = append(now, Where{Account: w.Account, Region: w.Region})
			}
		}
		c := Change{Excluded: installed.excluded, Busy: installed.busy, Small: installed.small}
		for _, x := range s.workloads {
			if !x.Workload.Managed {
				c.Onboard++
			}
		}
		for where := range seen {
			if !onboardedIn[where] {
				c.RoutersAdded++
			}
		}
		for where := range onboardedIn {
			if !seen[where] {
				c.RoutersRemoved++
			}
		}
		c.ManagerRemoved = len(s.workloads) == 0 && len(installed.staying) == 0
		c.USDMonth = float64(c.Onboard-c.Excluded-c.Busy-c.Small)*cost.Workload + float64(c.RoutersAdded-c.RoutersRemoved)*cost.Router
		if c.ManagerRemoved {
			c.USDMonth -= cost.Manager
		}
		dormeto.Manager = part(1, cost.Manager)
		dormeto.Routers = Routers{CostPart: part(len(now), cost.Router), Where: now}
		dormeto.Workloads = part(len(installed.now), cost.Workload)
		dormeto.Change = &c
	}
	return s.result(*totals, dormeto, byAccount.ordered(), byRegion.ordered())
}

// installation is Dormeto already installed: the workloads onboarded now in the accounts and regions estimated (those
// excluded by tags too), how many of them each filter leaves, and those staying though not estimated (left out: asleep,
// nothing running to stop, requests unreadable...).
type installation struct {
	now                   []discover.Workload
	excluded, busy, small int
	staying               []discover.Workload
}

func (s *state) installation() installation {
	var in installation
	key := func(w discover.Workload) string { return w.Account + "#" + w.Region + "#" + w.TargetGroupArn }
	gone := map[string]bool{}
	for _, x := range s.in.Excluded {
		if x.Workload.Managed {
			in.excluded++
			gone[key(x.Workload)] = true
		}
	}
	for _, x := range s.busy {
		if x.Workload.Managed {
			in.busy++
			gone[key(x.Workload)] = true
		}
	}
	for _, x := range s.small {
		if x.Workload.Managed {
			in.small++
			gone[key(x.Workload)] = true
		}
	}
	for _, x := range s.workloads {
		gone[key(x.Workload)] = true // estimated: in the recommendation's figures already
	}
	for _, region := range s.in.Regions {
		for _, w := range region.Workloads {
			if !w.Managed {
				continue
			}
			in.now = append(in.now, w)
			if !gone[key(w)] {
				in.staying = append(in.staying, w)
			}
		}
	}
	for _, x := range s.in.Excluded {
		if x.Workload.Managed {
			in.now = append(in.now, x.Workload)
		}
	}
	return in
}

// result is the estimate with what the reached accounts and the regions say.
func (s *state) result(totals Totals, dormeto DormetoCost, byAccount, byRegion Ordered[Totals]) Estimate {
	r := s.in.Reached
	var names Ordered[string]
	for _, a := range r.Accounts {
		names.Set(a.Account, a.Name)
	}
	unreachable := []accounts.Unreachable{}
	if r.Unreachable != nil {
		unreachable = r.Unreachable.List()
	}
	excluded := s.in.Excluded
	if excluded == nil {
		excluded = []Excluded{}
	}
	e := Estimate{
		GeneratedAt: isoOf(s.in.Now), Organization: r.Organization, Why: r.Why, Options: s.in.Options, Period: s.period(), Accounts: len(r.Accounts), AccountsListed: r.Listed,
		AccountNames: names, Totals: totals, Dormeto: dormeto, ByAccount: byAccount, ByRegion: byRegion, Workloads: s.workloads, LeftOut: s.leftOut,
		Excluded: excluded, Busy: s.busy, Small: s.small, DatabasesLeftOut: s.databasesLeftOut, DatabasesUnread: []DatabasesUnread{},
		Ineligible: []IneligibleAt{}, Unreachable: unreachable, FailedRegions: []FailedRegion{}, UnreadableClusters: []UnreadableCluster{},
	}
	kubernetes := false
	for _, region := range s.in.Regions {
		if region.DatabasesError != "" {
			e.DatabasesUnread = append(e.DatabasesUnread, DatabasesUnread{Account: region.Account, Region: region.Region, Reason: region.DatabasesError})
		}
		for _, i := range region.Ineligible {
			e.Ineligible = append(e.Ineligible, IneligibleAt{Account: region.Account, Region: region.Region, IneligibleRule: i})
		}
		if region.Error != "" {
			e.FailedRegions = append(e.FailedRegions, FailedRegion{Account: region.Account, Region: region.Region, Error: region.Error})
		}
		for _, c := range region.Clusters {
			e.UnreadableClusters = append(e.UnreadableClusters, UnreadableCluster{Account: region.Account, Region: region.Region, Name: c.Name, Reason: c.Reason})
		}
		for _, w := range region.Workloads {
			kubernetes = kubernetes || w.Backend.Kind == discover.KindK8s
		}
	}
	e.Assumptions = assumptionsOf(s.in.Baseline, s.in.Options, kubernetes)
	return e
}

// period is the requests' period: the reads' earliest start and latest end (ISO strings, compared as text); no read,
// the days before the run, to the minute.
func (s *state) period() Period {
	p := Period{Days: s.in.Options.DaysRead()}
	for _, r := range s.in.Requests {
		if p.From == "" || r.From < p.From {
			p.From = r.From
		}
		if r.To > p.To {
			p.To = r.To
		}
	}
	if p.From == "" {
		to := s.in.Now.UTC().Truncate(time.Minute)
		p.From, p.To = isoOf(to.Add(-time.Duration(p.Days)*24*time.Hour)), isoOf(to)
	}
	return p
}
