// Dormeto already installed (the user, 2026-10-13): the workloads onboarded that the estimate saw, Dormeto's cost now,
// and what the recommendation changes in it, signed, so that Dormeto's cost is the installation's once the
// recommendation is applied.
package estimate

import (
	"reflect"
	"testing"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/stopped"
)

func onboarded(x discover.Workload) discover.Workload {
	x.Managed = true
	return x
}

// parts: Dormeto's cost now, by part (the installation's manager, routers and workloads).
func parts(t *testing.T, e Estimate, routers []Where, workloads int) {
	t.Helper()
	got := []any{e.Dormeto.Manager, e.Dormeto.Routers, e.Dormeto.Workloads}
	want := []any{
		CostPart{Count: 1, UnitMonth: cost.Manager, USDMonth: cost.Manager},
		Routers{CostPart: CostPart{Count: len(routers), UnitMonth: cost.Router, USDMonth: float64(len(routers)) * cost.Router}, Where: routers},
		CostPart{Count: workloads, UnitMonth: cost.Workload, USDMonth: float64(workloads) * cost.Workload},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parts %+v, want %+v", got, want)
	}
}

// changed: the recommendation's change, its amount to 1e-9.
func changed(t *testing.T, e Estimate, want Change) {
	t.Helper()
	if e.Dormeto.Change == nil {
		t.Fatalf("no change: %+v", e.Dormeto)
	}
	got := *e.Dormeto.Change
	closeTo(t, "change.usdMonth", got.USDMonth, want.USDMonth, 9)
	got.USDMonth, want.USDMonth = 0, 0
	if got != want {
		t.Fatalf("change %+v, want %+v", got, want)
	}
}

// An onboarded workload asleep has nothing running to stop: left out of the estimate, yet onboarded, its cost stays.
func TestAnOnboardedWorkloadAsleepStaysInDormetosCost(t *testing.T) {
	api := w(A, "eu-west-1", "api", T3)
	e := run(t, func(in *Input) {
		in.Regions = region(onboarded(wPriced), onboarded(wEmpty), api)
		in.Requests = regionRequests(map[string][][2]float64{"arn:tg/shop": daily(), "arn:tg/batch": daily(), "arn:tg/api": daily()})
	})
	if got := estimatedGroups(e.Workloads); !reflect.DeepEqual(got, []string{"shop", "api"}) {
		t.Fatalf("workloads %q", got)
	}
	if len(e.LeftOut) != 1 || e.LeftOut[0].Workload.TargetGroup != "batch" {
		t.Fatalf("left out %+v", e.LeftOut)
	}
	// Now: shop and batch; the recommendation onboards api.
	parts(t, e, []Where{{A, "eu-west-1"}}, 2)
	changed(t, e, Change{Onboard: 1, USDMonth: cost.Workload})
	closeTo(t, "dormeto.usdMonth", e.Dormeto.USDMonth, cost.Manager+cost.Router+3*cost.Workload, 9)
	closeTo(t, "dormetoMonth", e.Totals.DormetoMonth, e.Dormeto.USDMonth, 9)
	closeTo(t, "withDormetoMonth", e.Totals.WithDormetoMonth, e.Totals.ActualMonth-e.Totals.USDMonthWindow+e.Totals.DormetoMonth, 9)
	a := totalsOf(t, e.ByAccount, A)
	closeTo(t, "A's dormetoMonth", a.DormetoMonth, cost.Router+3*cost.Workload, 9)
	closeTo(t, "A's withDormetoMonth", a.WithDormetoMonth, a.ActualMonth-a.USDMonthWindow+a.DormetoMonth, 9)
	// The estimate's own counts: the workloads estimated, batch not estimated.
	if e.Totals.Workloads != 2 || e.Totals.Excluded != 1 {
		t.Fatalf("totals %+v", e.Totals)
	}

	// Asleep in a region of its own: its router stays, a router comes with the workload onboarded elsewhere.
	asleep := onboarded(w(A, "eu-west-3", "batch"))
	e = run(t, func(in *Input) {
		in.Regions = []discover.RegionDiscovery{
			{Account: A, Region: "eu-west-1", Workloads: []discover.Workload{api}, Ineligible: []discover.IneligibleRule{}},
			{Account: A, Region: "eu-west-3", Workloads: []discover.Workload{asleep}, Ineligible: []discover.IneligibleRule{}},
		}
		in.Requests = regionRequests(map[string][][2]float64{"arn:tg/api": daily()})
	})
	parts(t, e, []Where{{A, "eu-west-3"}}, 1)
	changed(t, e, Change{Onboard: 1, RoutersAdded: 1, USDMonth: cost.Workload + cost.Router})
	closeTo(t, "dormetoMonth", e.Totals.DormetoMonth, cost.Manager+2*cost.Router+2*cost.Workload, 9)
	closeTo(t, "eu-west-3's dormetoMonth", totalsOf(t, e.ByRegion, "eu-west-3").DormetoMonth, cost.Router+cost.Workload, 9)
}

// The onboarded workloads the filters leave (--exclude-tags, --max-wakes, --min-saving) come off Dormeto's cost, and
// the router of a region left without workloads.
func TestTheOnboardedWorkloadsTheFiltersLeaveComeOffDormetosCost(t *testing.T) {
	pico := onboarded(w(A, "eu-west-1", "pico", stopped.Item{Kind: "ec2", Type: "t4g.pico", Count: 1, OS: "Linux"}))
	busy := onboarded(w(A, "eu-west-1", "busy", T3))
	legacy := onboarded(w(A, "eu-west-3", "legacy", T3))
	e := run(t, func(in *Input) {
		in.Regions = region(onboarded(wPriced), pico, busy)
		in.Requests = regionRequests(map[string][][2]float64{"arn:tg/shop": daily(), "arn:tg/pico": daily(), "arn:tg/busy": every(4)})
		in.Excluded = []Excluded{{Workload: legacy, Criterion: "env=dev"}}
	})
	if got := []int{len(e.Workloads), len(e.Small), len(e.Busy), len(e.Excluded)}; !reflect.DeepEqual(got, []int{1, 1, 1, 1}) {
		t.Fatalf("counts %v", got)
	}
	parts(t, e, []Where{{A, "eu-west-1"}, {A, "eu-west-3"}}, 4)
	changed(t, e, Change{Excluded: 1, Busy: 1, Small: 1, RoutersRemoved: 1, USDMonth: -3*cost.Workload - cost.Router})
	closeTo(t, "dormeto.usdMonth", e.Dormeto.USDMonth, cost.Manager+cost.Router+cost.Workload, 9)
	closeTo(t, "dormetoMonth", e.Totals.DormetoMonth, e.Dormeto.USDMonth, 9)
	closeTo(t, "A's dormetoMonth", totalsOf(t, e.ByAccount, A).DormetoMonth, cost.Router+cost.Workload, 9)
}

// Every onboarded workload left by the filters and none to onboard: nothing left to manage, the manager goes too.
func TestAnInstallationTheFiltersLeaveEmptyCostsNothing(t *testing.T) {
	legacy := onboarded(wOther)
	e := run(t, func(in *Input) {
		in.Regions = region()
		in.Requests = regionRequests(map[string][][2]float64{})
		in.Excluded = []Excluded{{Workload: legacy, Criterion: "env=dev"}}
	})
	parts(t, e, []Where{{B, "eu-west-1"}}, 1)
	changed(t, e, Change{Excluded: 1, RoutersRemoved: 1, ManagerRemoved: true, USDMonth: -(cost.Manager + cost.Router + cost.Workload)})
	if e.Dormeto.USDMonth != 0 || e.Totals.DormetoMonth != 0 {
		t.Fatalf("dormeto %v, totals %v", e.Dormeto.USDMonth, e.Totals.DormetoMonth)
	}
}

// The change is written in the JSON, after the parts; nothing onboarded, none.
func TestTheChangeInTheJSON(t *testing.T) {
	e := run(t, func(in *Input) {
		in.Regions = region(onboarded(wEmpty))
		in.Requests = regionRequests(map[string][][2]float64{})
	})
	b, err := jsonOf(e.Dormeto)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := topKeys(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, []string{"manager", "routers", "workloads", "change", "usdMonth"}) {
		t.Fatalf("keys %q", keys)
	}
	change, _ := jsonOf(e.Dormeto.Change)
	if string(change) != `{"onboard":0,"excluded":0,"busy":0,"small":0,"routersAdded":0,"routersRemoved":0,"managerRemoved":false,"usdMonth":0}` {
		t.Fatalf("change %s", change)
	}
	b, _ = jsonOf(run(t).Dormeto)
	if keys, _ := topKeys(b); !reflect.DeepEqual(keys, []string{"manager", "routers", "workloads", "usdMonth"}) {
		t.Fatalf("keys without an installation %q", keys)
	}
}
