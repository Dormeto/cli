// The standalone resources (vision standalone workloads design §7): each kind priced from its default signals replayed
// at --standalone-window, never counted twice, never priced from a failed read, left out without a point.
package estimate

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/options"
	"github.com/Dormeto/cli/internal/stopped"
)

var RDS = stopped.Item{Kind: "rds", Class: "db.t3.medium", Engine: "mysql"}

func resource(kind, id string, items ...stopped.Item) discover.StandaloneResource {
	arn := "arn:" + kind + ":" + id
	if items == nil {
		items = []stopped.Item{}
	}
	return discover.StandaloneResource{Account: A, Region: "eu-west-1", Kind: kind, ID: id, ARN: arn, Name: id, Tags: map[string][]string{}, Stopped: items}
}

// job is a resource's 5-minute points over the 14 days: busy (90) an hour a day from 09:00 UTC, idle (0) otherwise;
// none where gone says.
func job(gone func(p int) bool) [][2]float64 {
	out := [][2]float64{}
	for p := 0; p < 14*288; p++ {
		if gone != nil && gone(p) {
			continue
		}
		v := 0.0
		// FROM is 12:00 UTC: 09:00 is the point 21 hours in.
		if at := p % 288; at >= 252 && at < 264 {
			v = 90
		}
		out = append(out, [2]float64{float64(p * 5), v})
	}
	return out
}

func signalsOf(points map[string][][2]float64) []metrics.RegionSignals {
	read := metrics.RegionSignals{Account: A, Region: "eu-west-1", From: FROM, To: TO, Points: map[string]map[string][][2]float64{}, Unreadable: map[string]string{}}
	for arn, list := range points {
		id := "cpu_0"
		if strings.HasPrefix(arn, "arn:rds") {
			id = "metric_0"
		}
		read.Points[arn] = map[string][][2]float64{id: list}
	}
	return []metrics.RegionSignals{read}
}

// standaloneWorld: one HTTP workload (shop) and a resource of each kind, each running a daily job.
func standaloneWorld(in *Input) {
	in.Options.Standalone = &options.Standalone{Window: 120}
	resources := []discover.StandaloneResource{
		resource("instance", "i-agent", T3), resource("asg", "workers", T3), resource("ecs", "arn:aws:ecs:eu-west-1:111111111111:service/jobs/worker", FARGATE),
		resource("rds-instance", "reports", RDS), resource("rds-cluster", "aurora", RDS, RDS),
	}
	in.Standalone = []discover.StandaloneRegion{{Account: A, Region: "eu-west-1", Resources: resources, Ineligible: []discover.StandaloneIneligible{}, Problems: []string{}, Unread: []string{}}}
	points := map[string][][2]float64{}
	for _, r := range resources {
		points[r.ARN] = job(nil)
	}
	in.Signals = signalsOf(points)
}

func TestEachKindOfStandaloneResourceIsPricedFromItsSignalsAtTheStandaloneWindow(t *testing.T) {
	e := run(t, standaloneWorld)
	p := e.Standalone
	if p == nil || p.Window != 120 || p.Assumption != StandaloneAssumption {
		t.Fatalf("standalone %+v", p)
	}
	var kinds []string
	for _, x := range p.Resources {
		kinds = append(kinds, x.Resource.Kind)
		// Busy an hour a day: asleep the rest beyond the 2 hours after it, a start a day.
		if x.AtWindow.USDMonth <= 0 || x.AtWindow.AsleepHours < 20 || x.AtWindow.AsleepHours > 22 || x.AtWindow.WakesPerDay < 0.9 || x.AtWindow.WakesPerDay > 1.1 {
			t.Errorf("%s: %+v", x.Resource.ID, x.AtWindow)
		}
		closeTo(t, x.Resource.ID+" ran", x.RanHoursPerDay, 24, 6)
		if x.Recommended.Minutes < 5 || x.Recommended.Minutes > 120 {
			t.Errorf("%s: recommended %d", x.Resource.ID, x.Recommended.Minutes)
		}
	}
	if !slices.Equal(kinds, []string{"instance", "asg", "ecs", "rds-instance", "rds-cluster"}) || len(p.LeftOut) != 0 {
		t.Fatalf("kinds %q, left out %+v", kinds, p.LeftOut)
	}
	// The instance's saving: T3 ($0.0456 an hour, the fake price book's) for the hours asleep.
	instance := p.Resources[0]
	closeTo(t, "the instance's actual cost", instance.ActualMonth, 0.0456*24*30, 6)
	closeTo(t, "the instance's saving", instance.AtWindow.USDMonth, 0.0456*instance.AtWindow.AsleepHours*30, 6)
	// The totals for HTTP, standalone and both; the account's figures with them, a workload each in Dormeto's cost.
	s := p.Totals
	if s.Standalone.Count != 5 || s.Both.Count != s.HTTP.Count+5 || e.Totals.Standalone != 5 {
		t.Fatalf("totals %+v, %+v", s, e.Totals)
	}
	sum := 0.0
	for _, x := range p.Resources {
		sum += x.AtWindow.USDMonth
	}
	closeTo(t, "standalone savings", s.Standalone.USDMonthWindow, sum, 6)
	closeTo(t, "both", s.Both.USDMonthWindow, s.HTTP.USDMonthWindow+sum, 6)
	closeTo(t, "the totals' savings", e.Totals.USDMonthWindow, s.Both.USDMonthWindow, 6)
	if e.Dormeto.Workloads.Count != s.HTTP.Count+5 {
		t.Fatalf("Dormeto's workloads %+v", e.Dormeto.Workloads)
	}
	// The databases sentence no longer holds: they are counted.
	if strings.Contains(said(e.Assumptions), "Databases are not attached") {
		t.Fatal(e.Assumptions)
	}
}

func TestAStandaloneResourceIsLeftOutWithWhyNeverPricedFromAFailedReadOrWithoutAPoint(t *testing.T) {
	e := run(t, standaloneWorld, func(in *Input) {
		r := &in.Standalone[0]
		r.Resources = append(r.Resources, resource("instance", "i-stopped", T3), resource("instance", "i-unread", T3), resource("instance", "i-nothing"),
			resource("instance", "i-dev", T3))
		r.Resources[len(r.Resources)-1].Tags = map[string][]string{"env": {"dev"}}
		r.Ineligible = []discover.StandaloneIneligible{{Kind: "instance", ID: "i-spot", ARN: "arn:instance:i-spot", Name: "i-spot", Reason: "instance i-spot is a Spot instance: Dormeto stops On-Demand instances only"}}
		r.Problems = []string{"RDS instances not listed: the discovery role may not call rds:DescribeDBInstances (apply the access module or the AWS CLI script of this release again)"}
		in.Signals[0].Points["arn:instance:i-stopped"] = map[string][][2]float64{"cpu_0": {}}
		in.Signals[0].Unreadable["arn:instance:i-unread"] = "CloudWatch could not read the metrics (Forbidden)"
		in.Options.ExcludeTags = []discover.TagWants{{tag("env", "dev")}}
	})
	p := e.Standalone
	why := map[string]string{}
	for _, l := range p.LeftOut {
		why[l.Resource.ID] = l.Reason
	}
	want := map[string]string{
		"i-spot":    "instance i-spot is a Spot instance: Dormeto stops On-Demand instances only",
		"i-stopped": NoData,
		"i-unread":  "signals unreadable: CloudWatch could not read the metrics (Forbidden)",
		"i-nothing": nothing,
		"i-dev":     "excluded by its tags (env=dev)",
	}
	for id, reason := range want {
		if why[id] != reason {
			t.Errorf("%s: %q, want %q", id, why[id], reason)
		}
	}
	if len(p.Resources) != 5 || len(p.Unread) != 1 || !strings.HasPrefix(p.Unread[0].Reason, "RDS instances not listed") {
		t.Fatalf("resources %d, unread %+v", len(p.Resources), p.Unread)
	}
}

// Stopped every night (no point from 20:00 to 08:00 UTC): asleep then is no saving, its actual cost its hours running.
func TestAResourceStoppedAtNightSavesNothingWhileStopped(t *testing.T) {
	// FROM is 12:00 UTC: 20:00 to 08:00 is the points 96 to 240 of each day.
	e := run(t, standaloneWorld, func(in *Input) {
		in.Signals[0].Points["arn:instance:i-agent"]["cpu_0"] = job(func(p int) bool { return (p%288) >= 96 && (p%288) < 240 })
	})
	x := e.Standalone.Resources[0]
	closeTo(t, "ran", x.RanHoursPerDay, 12, 6)
	closeTo(t, "actual", x.ActualMonth, 0.0456*12*30, 6)
	all := run(t, standaloneWorld).Standalone.Resources[0]
	if x.AtWindow.USDMonth >= all.AtWindow.USDMonth {
		t.Fatalf("stopped at night %v, running %v", x.AtWindow.USDMonth, all.AtWindow.USDMonth)
	}
}

// A database counted as an HTTP workload's companion is not counted again as a standalone resource.
func TestADatabaseCountedAsACompanionIsNotCountedAgainAsStandalone(t *testing.T) {
	e := run(t, companions, standaloneWorld, func(in *Input) {
		shop := resource("rds-instance", "shop-db", RDS)
		in.Standalone[0].Resources = append(in.Standalone[0].Resources, shop)
		in.Signals[0].Points[shop.ARN] = map[string][][2]float64{"metric_0": job(nil)}
	})
	x := find(t, e.Workloads, "shop")
	if refs := databaseRefs(x.Databases); !slices.Equal(refs, [][2]string{{"rds-instance", "shop-db"}}) {
		t.Fatalf("shop's databases %q", refs)
	}
	for _, r := range e.Standalone.Resources {
		if r.Resource.ID == "shop-db" {
			t.Fatal("shop-db counted twice")
		}
	}
	i := slices.IndexFunc(e.Standalone.LeftOut, func(l ResourceLeftOut) bool { return l.Resource.ID == "shop-db" })
	if i < 0 || e.Standalone.LeftOut[i].Reason != "the companion database of the HTTP workload shop.example.com: counted with it" {
		t.Fatalf("left out %+v", e.Standalone.LeftOut)
	}
}

// --standalone off: no standalone part, the report's JSON without its keys.
func TestWithoutTheStandaloneResourcesTheEstimateIsAsBefore(t *testing.T) {
	e := run(t, func(in *Input) {
		in.Standalone = []discover.StandaloneRegion{{Account: A, Region: "eu-west-1", Resources: []discover.StandaloneResource{resource("instance", "i-agent", T3)}}}
	})
	if e.Standalone != nil || e.Totals.Standalone != 0 {
		t.Fatalf("standalone %+v", e.Standalone)
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), `"standalone"`) {
		t.Fatalf("%s", b)
	}
	if !strings.Contains(said(e.Assumptions), "Databases are not attached") {
		t.Fatal(e.Assumptions)
	}
}

// A database any HTTP workload of its region reaches is never a standalone resource, whatever the HTTP side did with it:
// attached, refused as a companion (reached by two workloads, its workload waking too often, stopped, excluded by its
// tags, a read replica), or the database of a workload busy or left out.
func TestADatabaseAnHTTPWorkloadReachesIsNeverAStandaloneResource(t *testing.T) {
	lost := withGroup(w(A, "eu-west-1", "lost", T3), "sg-lost")
	e := run(t, companions, standaloneWorld, func(in *Input) {
		in.Regions[0].Workloads = append(in.Regions[0].Workloads, lost) // its requests unreadable: left out
		in.Requests[0].Unreadable["arn:tg/lost"] = "CloudWatch could not read the requests (Forbidden)"
		in.Regions[0].Databases = append(in.Regions[0].Databases, database("lost-db", []string{"sg-lost"}),
			database("sleepy-db", []string{"sg-shop"}, func(d *discover.Database) { d.Status = "stopped" }))
		for _, id := range []string{"shop-db", "blog-db", "prd-db", "calm-db", "shared-db", "replica", "lost-db", "sleepy-db"} {
			db := resource("rds-instance", id, RDS)
			in.Standalone[0].Resources = append(in.Standalone[0].Resources, db)
			in.Signals[0].Points[db.ARN] = map[string][][2]float64{"metric_0": job(nil)}
		}
	})
	why := map[string]string{}
	for _, l := range e.Standalone.LeftOut {
		why[l.Resource.ID] = l.Reason
	}
	for _, r := range e.Standalone.Resources {
		if r.Resource.Kind == "rds-instance" && r.Resource.ID != "reports" {
			t.Errorf("%s priced as a standalone resource", r.Resource.ID)
		}
	}
	for id, want := range map[string]string{
		"shop-db":   "the companion database of the HTTP workload shop.example.com: counted with it",
		"blog-db":   "reached by the HTTP workload blog.example.com: its companion, never a standalone resource",
		"prd-db":    "reached by the HTTP workload shop.example.com: its companion, never a standalone resource",
		"calm-db":   "reached by the HTTP workload calm.example.com: its companion, never a standalone resource",
		"shared-db": "reached by the HTTP workloads shop.example.com, calm.example.com: their companion, never a standalone resource",
		"replica":   "reached by the HTTP workload shop.example.com: its companion, never a standalone resource",
		"lost-db":   "reached by the HTTP workload lost.example.com: its companion, never a standalone resource",
		"sleepy-db": "reached by the HTTP workload shop.example.com: its companion, never a standalone resource",
	} {
		if why[id] != want {
			t.Errorf("%s: %q, want %q", id, why[id], want)
		}
	}
}

// A standalone ECS service is priced as an HTTP one is: with --ecs-uptime at its tasks measured over the period (its
// LiveTaskCount), not at its count when discovered; without it, one at 0 tasks is left out.
func TestAStandaloneServiceIsPricedAtItsMeasuredTasks(t *testing.T) {
	ten := FARGATE
	ten.Count = 10
	service := "arn:ecs:arn:aws:ecs:eu-west-1:111111111111:service/jobs/worker"
	measured := run(t, standaloneWorld, func(in *Input) {
		in.Options.EcsUptime, in.Options.MinSaving = true, nil // a tenth of its price: under $1 a month
		in.Standalone[0].Resources[2].Stopped = []stopped.Item{ten}
		in.Signals[0].Tasks = map[string]float64{service: 14 * 1440} // one task every minute of the 14 days
	})
	x := measured.Standalone.Resources[2]
	if x.Resource.Kind != "ecs" {
		t.Fatalf("resource %+v", x.Resource)
	}
	closeTo(t, "a service measured at 1 task of the 10 discovered", x.ActualMonth, 0.0099*24*30/10, 6)
	assumed := run(t, standaloneWorld, func(in *Input) { in.Standalone[0].Resources[2].Stopped = []stopped.Item{ten} })
	closeTo(t, "without --ecs-uptime, its count when discovered", assumed.Standalone.Resources[2].ActualMonth, 0.0099*24*30, 6)

	zero := 0
	idle := run(t, standaloneWorld, func(in *Input) { in.Standalone[0].Resources[2].EcsDesired = &zero })
	i := slices.IndexFunc(idle.Standalone.LeftOut, func(l ResourceLeftOut) bool { return l.Resource.Kind == "ecs" })
	if i < 0 || idle.Standalone.LeftOut[i].Reason != "an ECS service at 0 tasks: nothing running to stop (--ecs-uptime estimates it as one task from the hours it ran)" {
		t.Fatalf("left out %+v", idle.Standalone.LeftOut)
	}
}

// The assumptions say what is done for both kinds: the EC2 and ECS lines are the HTTP workloads', and one line says how
// the standalone resources are measured and priced.
func TestTheAssumptionsSayHowEachKindIsMeasured(t *testing.T) {
	e := run(t, standaloneWorld)
	text := said(e.Assumptions)
	for _, want := range []string{
		"HTTP workloads: EC2 instances without an Auto Scaling group are assumed to run 24/7",
		"HTTP workloads: ECS services are assumed to run all the time",
		"Standalone resources: they ran when their signal has 5-minute points (an EC2 instance, an RDS database, an Auto Scaling group or an ECS service publishes none while stopped or at 0); priced for those hours at their size when discovered (a group's desired capacity, an ECS service's desired count, at least 1 task), an ECS service with --ecs-uptime at its tasks measured (CloudWatch LiveTaskCount); a service at 0 tasks is left out without --ecs-uptime.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the assumptions lack %q:\n%s", want, text)
		}
	}
	if off := run(t); strings.Contains(said(off.Assumptions), "HTTP workloads:") {
		t.Errorf("--standalone off: %q", off.Assumptions)
	}
}
