// The totals, with Dormeto's cost: a router per account and region with workloads, each workload, the manager once;
// each account's and region's workloads not estimated, whatever the reason; the estimate as the reports read it.
//
// Ports manager packages/estimate/src/estimate.ts (estimateAll: its totals and its result).
package estimate

import (
	"github.com/Dormeto/cli/internal/accounts"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/prices"
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
	if len(s.workloads) > 0 {
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
	return s.result(*totals, dormeto, byAccount.ordered(), byRegion.ordered())
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
		GeneratedAt: isoOf(s.in.Now), Organization: r.Organization, Why: r.Why, Options: s.in.Options, Accounts: len(r.Accounts), AccountsListed: r.Listed,
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
