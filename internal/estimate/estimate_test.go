// Phase 3 of the estimate (spec §6): each eligible workload's R10 replay priced by the product's price book, what is
// left out and why, the totals by account, region and all, net of Dormeto's cost. Ported from manager
// packages/estimate/test/estimate.test.ts (the cases outside its describe blocks of EC2 instances, ECS services and
// companion databases, which measured_test.go and companions_test.go port).
package estimate

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/stopped"
)

func TestEachPricedWorkloadAtTheReportsWindowAndAtItsRecommendedWindow(t *testing.T) {
	e := run(t)
	if got := estimatedGroups(e.Workloads); !reflect.DeepEqual(got, []string{"shop", "docs", "crm"}) {
		t.Fatalf("workloads %q", got)
	}
	shop := nth(t, e.Workloads, 0)
	expected := advise(t, daily(), []stopped.Item{T3}, always, 60, nil)
	at := row(t, expected, 60)
	if want := (AtWindow{USDMonth: usd(t, at), WakesPerDay: at.WakesPerDay, AsleepHours: at.AsleepHours}); shop.AtWindow != want {
		t.Fatalf("atWindow %+v, want %+v", shop.AtWindow, want)
	}
	// Another window when asked (--window 15).
	quarter := run(t, func(in *Input) { in.Options.Window = 15 })
	if got, want := nth(t, quarter.Workloads, 0).AtWindow.USDMonth, usd(t, row(t, advise(t, daily(), []stopped.Item{T3}, always, 15, nil), 15)); got != want {
		t.Fatalf("at 15 minutes %v, want %v", got, want)
	}
	best := row(t, expected, expected.Recommended)
	if want := (Recommended{Minutes: expected.Recommended, USDMonth: usd(t, best), WakesPerDay: best.WakesPerDay}); shop.Recommended != want {
		t.Fatalf("recommended %+v, want %+v", shop.Recommended, want)
	}
	// No request in 14 days: estimated, asleep throughout.
	if docs := nth(t, e.Workloads, 1); !docs.Quiet || docs.AtWindow.WakesPerDay != 0 {
		t.Fatalf("docs %+v", docs)
	}
	if shop.Quiet {
		t.Fatal("shop has requests")
	}
}

func TestLeavesOutWithTheReasonWhatCannotBeEstimated(t *testing.T) {
	r := reachedOf(t)
	e := run(t, func(in *Input) { in.Reached = r })
	got := [][2]string{}
	for _, l := range e.LeftOut {
		got = append(got, [2]string{l.Workload.TargetGroup, l.Reason})
	}
	want := [][2]string{
		{"gpu", "what it stops has no AWS list price"},
		{"batch", "nothing running to stop (an Auto Scaling group at 0, an ECS service on EC2 capacity, or a stopped instance)"},
		{"blog", "requests unreadable: CloudWatch could not read the requests (Forbidden)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("left out %q, want %q", got, want)
	}
	shared := "shared"
	if want := []IneligibleAt{{Account: A, Region: "eu-west-1", IneligibleRule: discover.IneligibleRule{LoadBalancer: "web", TargetGroup: &shared, Hosts: []string{}, Reason: "its target group is shared"}}}; !reflect.DeepEqual(e.Ineligible, want) {
		t.Fatalf("ineligible %+v", e.Ineligible)
	}
	if want := []FailedRegion{{Account: A, Region: "eu-west-3", Error: "opt-in region not enabled"}}; !reflect.DeepEqual(e.FailedRegions, want) {
		t.Fatalf("failed regions %+v", e.FailedRegions)
	}
	if !reflect.DeepEqual(e.Unreachable, r.Unreachable.List()) {
		t.Fatalf("unreachable %+v, want %+v", e.Unreachable, r.Unreachable.List())
	}
}

func TestTotalsByAccountRegionAndAllNetOfDormetosCost(t *testing.T) {
	e := run(t)
	sum := func(of func(WorkloadEstimate) float64) float64 {
		n := 0.0
		for _, x := range e.Workloads {
			n += of(x)
		}
		return n
	}
	if e.Totals.Workloads != 3 {
		t.Fatalf("totals %+v", e.Totals)
	}
	closeTo(t, "usdMonthWindow", e.Totals.USDMonthWindow, sum(func(x WorkloadEstimate) float64 { return x.AtWindow.USDMonth }), 9)
	closeTo(t, "usdMonthRecommended", e.Totals.USDMonthRecommended, sum(func(x WorkloadEstimate) float64 { return x.Recommended.USDMonth }), 9)
	closeTo(t, "dormetoMonth", e.Totals.DormetoMonth, cost.Manager+2*cost.Router+3*cost.Workload, 9)
	a := totalsOf(t, e.ByAccount, A)
	if a.Workloads != 2 {
		t.Fatalf("account A %+v", a)
	}
	closeTo(t, "account A dormetoMonth", a.DormetoMonth, cost.Router+2*cost.Workload, 9)
	r := totalsOf(t, e.ByRegion, "eu-west-1")
	if r.Workloads != 3 {
		t.Fatalf("eu-west-1 %+v", r)
	}
	closeTo(t, "eu-west-1 dormetoMonth", r.DormetoMonth, 2*cost.Router+3*cost.Workload, 9)
	if e.Organization == nil || *e.Organization != "o-abc123" || e.Accounts != 2 || e.GeneratedAt != "2026-10-10T12:00:30.000Z" {
		t.Fatalf("organization %v, accounts %d, generatedAt %s", e.Organization, e.Accounts, e.GeneratedAt)
	}
	if keys := e.AccountNames.Keys(); !reflect.DeepEqual(keys, []string{A, B}) {
		t.Fatalf("account names %q", keys)
	}
	if prod, _ := e.AccountNames.Get(A); prod != "prod" {
		t.Fatalf("A is %q", prod)
	}
	if staging, _ := e.AccountNames.Get(B); staging != "staging" {
		t.Fatalf("B is %q", staging)
	}
	// Dormeto's cost by part, each with its computation: the manager once, a router per account and region with workloads.
	want := DormetoCost{
		Manager:   CostPart{Count: 1, UnitMonth: cost.Manager, USDMonth: cost.Manager},
		Routers:   Routers{CostPart: CostPart{Count: 2, UnitMonth: cost.Router, USDMonth: 2 * cost.Router}, Where: []Where{{A, "eu-west-1"}, {B, "eu-west-1"}}},
		Workloads: CostPart{Count: 3, UnitMonth: cost.Workload, USDMonth: 3 * cost.Workload},
		USDMonth:  e.Dormeto.USDMonth,
	}
	if !reflect.DeepEqual(e.Dormeto, want) {
		t.Fatalf("dormeto %+v, want %+v", e.Dormeto, want)
	}
	closeTo(t, "dormeto.usdMonth", e.Dormeto.USDMonth, e.Totals.DormetoMonth, 9)
	// Classified by backend: Auto Scaling group, EC2 instance, ECS service, Kubernetes on EKS.
	if e.Totals.Kinds != (Kinds{ASG: 3}) || a.Kinds != (Kinds{ASG: 2}) {
		t.Fatalf("kinds %+v, A's %+v", e.Totals.Kinds, a.Kinds)
	}
	if !strings.Contains(said(e.Assumptions), "Every request counts") {
		t.Fatalf("assumptions %q", e.Assumptions)
	}
}

func TestKubernetesWorkloadsCountedUnderEKSClustersUnreadListed(t *testing.T) {
	m5 := "m5.large"
	pods := stopped.Item{Kind: "k8s-pods", Count: 2, CPU: 0.25, MemoryGb: 0.5, NodeType: &m5, OS: "Linux", Autoscaled: true, Allocatable: &stopped.Allocatable{CPU: 1.93, MemoryGb: 6.886}}
	web := w(A, "eu-west-1", "web", pods)
	web.Backend = discover.WorkloadBackend{Kind: "k8s", ID: "eks-demo/demo/Deployment/web"}
	reason := "the cluster does not let the profile read it (an access entry with AmazonEKSViewPolicy): Unauthorized"
	e := run(t, func(in *Input) {
		in.Regions = []discover.RegionDiscovery{{Account: A, Region: "eu-west-1", Workloads: []discover.Workload{web}, Ineligible: []discover.IneligibleRule{},
			Clusters: []discover.UnreadableCluster{{Name: "eks-prod", Reason: reason}}}}
		in.Requests = regionRequests(map[string][][2]float64{"arn:tg/web": daily()})
		in.Hourly = func(_ context.Context, _ string, item stopped.Item) (*float64, error) {
			if item.Kind == "k8s-pods" {
				return f(0.0277), nil
			}
			return nil, nil
		}
	})
	if got := estimatedGroups(e.Workloads); !reflect.DeepEqual(got, []string{"web"}) {
		t.Fatalf("workloads %q", got)
	}
	if e.Totals.Kinds != (Kinds{EKS: 1}) {
		t.Fatalf("kinds %+v", e.Totals.Kinds)
	}
	if want := []UnreadableCluster{{Account: A, Region: "eu-west-1", Name: "eks-prod", Reason: reason}}; !reflect.DeepEqual(e.UnreadableClusters, want) {
		t.Fatalf("unreadable clusters %+v", e.UnreadableClusters)
	}
	if !strings.Contains(said(e.Assumptions), "Kubernetes pods: their share of their nodes (the larger of CPU and memory over the node's allocatable) at the node's price, saved only where a node autoscaler removes the nodes they free; the EKS control plane and the nodes' DaemonSets keep running.") {
		t.Fatalf("assumptions %q", e.Assumptions)
	}
	// Without a Kubernetes workload, nothing said of them.
	if strings.Contains(said(run(t).Assumptions), "Kubernetes") {
		t.Fatal("Kubernetes said without a Kubernetes workload")
	}
}

func TestABackendBehindTwoTargetGroupsIsEstimatedOnceWithBothTargetGroupsRequests(t *testing.T) {
	a := w(A, "eu-west-1", "shop-a", T3)
	a.Backend = discover.WorkloadBackend{Kind: "asg", ID: "shared-asg"}
	b := w(A, "eu-west-1", "shop-b", T3)
	b.Backend = discover.WorkloadBackend{Kind: "asg", ID: "shared-asg"}
	evening := [][2]float64{}
	for d := range 14 {
		evening = append(evening, [2]float64{float64(1320 + d*1440), 2})
	}
	e := run(t, func(in *Input) {
		in.Regions = region(a, b)
		in.Requests = regionRequests(map[string][][2]float64{"arn:tg/shop-a": daily(), "arn:tg/shop-b": evening})
	})
	if got := estimatedGroups(e.Workloads); !reflect.DeepEqual(got, []string{"shop-a"}) {
		t.Fatalf("workloads %q", got)
	}
	merged := append(daily(), evening...)
	closeTo(t, "shop-a at the window", nth(t, e.Workloads, 0).AtWindow.USDMonth, usd(t, row(t, advise(t, merged, []stopped.Item{T3}, always, 60, nil), 60)), 9)
	if want := []LeftOut{{Workload: b, Reason: "its backend (asg shared-asg) also serves shop-a: estimated there, with every target group's requests"}}; !reflect.DeepEqual(e.LeftOut, want) {
		t.Fatalf("left out %+v", e.LeftOut)
	}
}

func TestEachWorkloadsActualCostAMonth(t *testing.T) {
	e := run(t)
	shop := find(t, e.Workloads, "shop")
	// $0.0456 an hour (the price book's, for both instances), 30 days of 24 hours.
	closeTo(t, "actual", shop.ActualMonth, 0.0456*720, 9)
	if !(shop.AtWindow.USDMonth < shop.ActualMonth) {
		t.Fatalf("saves %v of %v", shop.AtWindow.USDMonth, shop.ActualMonth)
	}
	within := find(t, run(t, func(in *Input) { in.Baseline = paris }).Workloads, "shop")
	h, err := prices.BaselineHours(FROM, TO, paris)
	if err != nil {
		t.Fatal(err)
	}
	closeTo(t, "actual within business hours", within.ActualMonth, (0.0456*h*30)/14, 9)
}

func TestTheTotalsActualCostAndTheirCostWithDormetoEnabled(t *testing.T) {
	e := run(t)
	all, ofA := 0.0, 0.0
	for _, x := range e.Workloads {
		all += x.ActualMonth
		if x.Workload.Account == A {
			ofA += x.ActualMonth
		}
	}
	closeTo(t, "actualMonth", e.Totals.ActualMonth, all, 9)
	closeTo(t, "withDormetoMonth", e.Totals.WithDormetoMonth, e.Totals.ActualMonth-e.Totals.USDMonthWindow+e.Totals.DormetoMonth, 9)
	a := totalsOf(t, e.ByAccount, A)
	closeTo(t, "A's actualMonth", a.ActualMonth, ofA, 9)
	// An account's: its routers and workloads, the manager in the total only.
	closeTo(t, "A's withDormetoMonth", a.WithDormetoMonth, a.ActualMonth-a.USDMonthWindow+cost.Router+2*cost.Workload, 9)
}

// Waking several times a day, a workload keeps its visitors waiting more than it saves (the user, 2026-10-11).
func TestAWorkloadWakingMoreThanMaxWakesIsLeftOutApart(t *testing.T) {
	// Requests every 4 hours: about 6 wakes a day at a 1-hour window; every 8 hours: about 3.
	busy := w(A, "eu-west-1", "busy", T3)
	calm := w(A, "eu-west-1", "calm", T3)
	world := func(in *Input) {
		in.Regions = region(busy, calm)
		in.Requests = regionRequests(map[string][][2]float64{"arn:tg/busy": every(4), "arn:tg/calm": every(8)})
	}
	e := run(t, world)
	if got := estimatedGroups(e.Workloads); !reflect.DeepEqual(got, []string{"calm"}) {
		t.Fatalf("workloads %q", got)
	}
	if calm := nth(t, e.Workloads, 0); calm.Recommended.WakesPerDay > 3 {
		t.Fatalf("calm wakes %v", calm.Recommended.WakesPerDay)
	}
	if len(e.Busy) != 1 || e.Busy[0].Workload.TargetGroup != "busy" || math.Round(e.Busy[0].WakesPerDay) != 6 {
		t.Fatalf("busy %+v", e.Busy)
	}
	if !(e.Busy[0].USDMonth > 0) {
		t.Fatalf("busy saving %v", e.Busy[0].USDMonth)
	}
	// Busy at the report's window, calm at its recommended one (requests every 90 minutes): kept.
	lull := w(A, "eu-west-1", "lull", T3)
	lulls := run(t, func(in *Input) {
		in.Regions = region(lull)
		in.Requests = regionRequests(map[string][][2]float64{"arn:tg/lull": every(1.5)})
		in.Options.MinSaving = nil
	})
	if len(lulls.Workloads) != 1 || !(lulls.Workloads[0].AtWindow.WakesPerDay > 3) || lulls.Workloads[0].Recommended.WakesPerDay > 3 {
		t.Fatalf("lull %+v", lulls.Workloads)
	}
	if !reflect.DeepEqual(lulls.Busy, []Busy{}) {
		t.Fatalf("busy %+v", lulls.Busy)
	}
	if e.Totals.Workloads != 1 {
		t.Fatalf("totals %+v", e.Totals)
	}
	kept := run(t, world, func(in *Input) { in.Options.MaxWakes = nil })
	if got := estimatedGroups(kept.Workloads); !reflect.DeepEqual(got, []string{"busy", "calm"}) {
		t.Fatalf("workloads %q", got)
	}
	if !reflect.DeepEqual(kept.Busy, []Busy{}) {
		t.Fatalf("busy %+v", kept.Busy)
	}
}

// Dormeto costs something per workload and per router: a workload saving cents is not worth installing it for.
func TestAWorkloadSavingLessThanMinSavingIsLeftOutApart(t *testing.T) {
	pico := w(A, "eu-west-1", "pico", stopped.Item{Kind: "ec2", Type: "t4g.pico", Count: 1, OS: "Linux"})
	world := func(in *Input) {
		in.Regions = region(pico, wPriced)
		in.Requests = regionRequests(map[string][][2]float64{"arn:tg/pico": daily(), "arn:tg/shop": daily()})
	}
	e := run(t, world)
	if got := estimatedGroups(e.Workloads); !reflect.DeepEqual(got, []string{"shop"}) {
		t.Fatalf("workloads %q", got)
	}
	if len(e.Small) != 1 || e.Small[0].Workload.TargetGroup != "pico" || !(e.Small[0].USDMonth > 0) || !(e.Small[0].USDMonth < 1) {
		t.Fatalf("small %+v", e.Small)
	}
	if got := estimatedGroups(run(t, world, func(in *Input) { in.Options.MinSaving = nil }).Workloads); !reflect.DeepEqual(got, []string{"pico", "shop"}) {
		t.Fatalf("workloads %q", got)
	}
}

// The user, 2026-10-11: each account's excluded workloads, whatever the reason (tags, left out, too many wakes, too
// small a saving); an account whose workloads are all excluded is listed too.
func TestCountsEachAccountsExcludedWorkloadsWhateverTheReason(t *testing.T) {
	pico := w(A, "eu-west-1", "pico", stopped.Item{Kind: "ec2", Type: "t4g.pico", Count: 1, OS: "Linux"})
	busy := w(A, "eu-west-1", "busy", T3)
	dev := wPriced
	dev.TargetGroup = "dev"
	e := run(t, func(in *Input) {
		in.Regions = region(wPriced, pico, busy, wEmpty)
		in.Requests = regionRequests(map[string][][2]float64{"arn:tg/shop": daily(), "arn:tg/pico": daily(), "arn:tg/busy": every(4), "arn:tg/batch": daily()})
		in.Excluded = []Excluded{{Workload: wOther, Criterion: "env=dev"}, {Workload: dev, Criterion: "env=dev"}}
	})
	if got := []int{len(e.Workloads), len(e.Small), len(e.Busy), len(e.LeftOut), len(e.Excluded)}; !reflect.DeepEqual(got, []int{1, 1, 1, 1, 2}) {
		t.Fatalf("counts %v", got)
	}
	if a := totalsOf(t, e.ByAccount, A); a.Workloads != 1 || a.Excluded != 4 {
		t.Fatalf("A %+v", a)
	}
	if b := totalsOf(t, e.ByAccount, B); b.Workloads != 0 || b.Excluded != 1 || b.ActualMonth != 0 {
		t.Fatalf("B %+v", b)
	}
	if e.Totals.Workloads != 1 || e.Totals.Excluded != 5 {
		t.Fatalf("totals %+v", e.Totals)
	}
	if r := totalsOf(t, e.ByRegion, "eu-west-1"); r.Excluded != 5 {
		t.Fatalf("eu-west-1 %+v", r)
	}
}

func TestTheWorkloadsExcludedByTheirTagsAsGivenApartFromTheEstimate(t *testing.T) {
	other := wOther
	other.Tags = map[string][]string{"env": {"dev"}}
	excluded := []Excluded{{Workload: other, Criterion: "env=dev"}}
	if got := run(t, func(in *Input) { in.Excluded = excluded }).Excluded; !reflect.DeepEqual(got, excluded) {
		t.Fatalf("excluded %+v", got)
	}
	if got := run(t).Excluded; !reflect.DeepEqual(got, []Excluded{}) {
		t.Fatalf("excluded %#v", got)
	}
}

func TestNoWorkloadToEstimateDormetoCostsNothingNotEvenTheManager(t *testing.T) {
	e := run(t, func(in *Input) { in.Regions = []discover.RegionDiscovery{} })
	want := DormetoCost{
		Manager:   CostPart{Count: 0, UnitMonth: cost.Manager, USDMonth: 0},
		Routers:   Routers{CostPart: CostPart{Count: 0, UnitMonth: cost.Router, USDMonth: 0}, Where: []Where{}},
		Workloads: CostPart{Count: 0, UnitMonth: cost.Workload, USDMonth: 0},
		USDMonth:  0,
	}
	if !reflect.DeepEqual(e.Dormeto, want) {
		t.Fatalf("dormeto %+v", e.Dormeto)
	}
}

func TestPricesThatCouldNotBeReadAreSaidSo(t *testing.T) {
	e := run(t, func(in *Input) {
		in.Hourly = func(context.Context, string, stopped.Item) (*float64, error) { return nil, nil }
		in.PriceFailure = func() string { return "AccessDeniedException: not authorized to perform: pricing:GetProducts" }
	})
	if !reflect.DeepEqual(e.Workloads, []WorkloadEstimate{}) {
		t.Fatalf("workloads %q", estimatedGroups(e.Workloads))
	}
	for _, l := range e.LeftOut {
		if l.Workload.TargetGroup == "shop" {
			if want := "prices unreadable (AccessDeniedException: not authorized to perform: pricing:GetProducts)"; l.Reason != want {
				t.Fatalf("shop: %q", l.Reason)
			}
			return
		}
	}
	t.Fatalf("shop not left out: %+v", e.LeftOut)
}

func TestWithinTheBusinessHoursBaselineAsTheProductPricesIt(t *testing.T) {
	e := run(t, func(in *Input) { in.Baseline = paris })
	closeTo(t, "at the window", nth(t, e.Workloads, 0).AtWindow.USDMonth, usd(t, row(t, advise(t, daily(), []stopped.Item{T3}, paris, 60, nil), 60)), 9)
	if !(nth(t, e.Workloads, 0).AtWindow.USDMonth < nth(t, run(t).Workloads, 0).AtWindow.USDMonth) {
		t.Fatal("business hours save no less than every hour")
	}
}

// Go only: a price lookup that fails is no price (Node's catch), never the estimate's error.
func TestAPriceLookupThatFailsIsNoPrice(t *testing.T) {
	e := run(t, func(in *Input) {
		in.Hourly = func(ctx context.Context, region string, item stopped.Item) (*float64, error) {
			if item.Type == "t3.medium" {
				return nil, context.DeadlineExceeded
			}
			return hourly(ctx, region, item)
		}
	})
	if got := estimatedGroups(e.Workloads); !reflect.DeepEqual(got, []string{"docs"}) {
		t.Fatalf("workloads %q", got)
	}
}

// Go only: a done context is the estimate's error (a cancelled price lookup would otherwise say a list price is
// missing).
func TestADoneContextIsTheError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := All(ctx, Input{Reached: reachedOf(t), Regions: defaultRegions(), Requests: defaultRequests(), Hourly: hourly, Baseline: always, Options: defaultOptions(), Now: NOW})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v", err)
	}
}

// Go only: an unknown time zone in the baseline is the estimate's error, as Node's RangeError rejects estimateAll.
func TestAnUnknownTimeZoneIsTheError(t *testing.T) {
	_, err := All(context.Background(), Input{Reached: reachedOf(t), Regions: defaultRegions(), Requests: defaultRequests(), Hourly: hourly,
		Baseline: prices.Baseline{Kind: "business-hours", TimeZone: "Mars/Olympus"}, Options: defaultOptions(), Now: NOW})
	if err == nil {
		t.Fatal("no error")
	}
}

// Go only: the shapes Node writes: every list an array, never null; the keys in Node's order.
func TestTheEstimatesJSONAsNodeWritesIt(t *testing.T) {
	e := run(t, func(in *Input) { in.Regions = []discover.RegionDiscovery{}; in.Requests = []metrics.RegionRequests{} })
	b, err := jsonOf(e)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := topKeys(b)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"generatedAt", "organization", "why", "options", "accounts", "accountsListed", "accountNames", "totals", "dormeto", "byAccount", "byRegion",
		"workloads", "leftOut", "excluded", "busy", "small", "databasesLeftOut", "databasesUnread", "ineligible", "unreachable", "failedRegions",
		"unreadableClusters", "assumptions"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys %q", keys)
	}
	for _, empty := range []string{`"workloads":[]`, `"leftOut":[]`, `"excluded":[]`, `"busy":[]`, `"small":[]`, `"databasesLeftOut":[]`, `"databasesUnread":[]`,
		`"ineligible":[]`, `"failedRegions":[]`, `"unreadableClusters":[]`, `"byAccount":{}`, `"byRegion":{}`, `"where":[]`, `"why":null`} {
		if !strings.Contains(string(b), empty) {
			t.Fatalf("%s lacks %s", b, empty)
		}
	}
	shop := find(t, run(t).Workloads, "shop")
	b, _ = jsonOf(shop)
	if keys, _ := topKeys(b); !reflect.DeepEqual(keys, []string{"workload", "quiet", "actualMonth", "atWindow", "recommended"}) {
		t.Fatalf("workload keys %q", keys)
	}
	measured := WorkloadEstimate{Uptime: hours(12), Databases: []Companion{{Kind: "rds-instance", ID: "db", Uptime: unread("why")}}}
	b, _ = jsonOf(measured)
	if keys, _ := topKeys(b); !reflect.DeepEqual(keys, []string{"workload", "quiet", "actualMonth", "uptime", "atWindow", "recommended", "databases"}) {
		t.Fatalf("workload keys %q", keys)
	}
	if !strings.Contains(string(b), `"uptime":{"hoursPerDay":12}`) || !strings.Contains(string(b), `{"kind":"rds-instance","id":"db","usdMonth":0,"recommendedUsdMonth":0,"uptime":{"unreadable":"why"}}`) {
		t.Fatalf("workload %s", b)
	}
	b, _ = jsonOf(Routers{CostPart: CostPart{Count: 1, UnitMonth: 0.26, USDMonth: 0.26}, Where: []Where{{A, "eu-west-1"}}})
	if want := `{"count":1,"unitMonth":0.26,"usdMonth":0.26,"where":[{"account":"111111111111","region":"eu-west-1"}]}`; string(b) != want {
		t.Fatalf("routers %s", b)
	}
	shared := "shared"
	b, _ = jsonOf(IneligibleAt{Account: A, Region: "eu-west-1", IneligibleRule: discover.IneligibleRule{LoadBalancer: "web", TargetGroup: &shared, Hosts: []string{}, Reason: "why"}})
	if want := `{"account":"111111111111","region":"eu-west-1","loadBalancer":"web","targetGroup":"shared","hosts":[],"reason":"why"}`; string(b) != want {
		t.Fatalf("ineligible %s", b)
	}
}
