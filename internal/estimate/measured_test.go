// EC2 instances without an Auto Scaling group (--ec2-uptime) and ECS services (--ecs-uptime): measured, their actual
// cost counts the hours they ran, their savings only those hours. Ported from manager
// packages/estimate/test/estimate.test.ts (its describe blocks "EC2 instances without an Auto Scaling group" and
// "ECS services").
package estimate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/stopped"
)

func vm(name string, change ...func(*discover.Workload)) discover.Workload {
	x := w(A, "eu-west-1", name, T3)
	x.Backend = discover.WorkloadBackend{Kind: "instance", ID: "i-" + name}
	x.InstanceState = "running"
	for _, c := range change {
		c(&x)
	}
	return x
}

// only: A in eu-west-1 with these workloads, each with a request a day.
func only(workloads ...discover.Workload) func(*Input) {
	return func(in *Input) {
		in.Regions = region(workloads...)
		minutes := map[string][][2]float64{}
		for _, x := range workloads {
			minutes[x.TargetGroupArn] = daily()
		}
		in.Requests = regionRequests(minutes)
	}
}

func uptimeOf(running map[string][][2]int, unreadable map[string]string) []metrics.RegionUptime {
	if unreadable == nil {
		unreadable = map[string]string{}
	}
	return []metrics.RegionUptime{{Account: A, Region: "eu-west-1", From: FROM, To: TO, Running: running, Unreadable: unreadable}}
}

func uptimes(workloads []WorkloadEstimate) []*Uptime {
	out := []*Uptime{}
	for _, x := range workloads {
		out = append(out, x.Uptime)
	}
	return out
}

// measuredVMs: --ec2-uptime when an uptime is given.
func measuredVMs(t *testing.T, up []metrics.RegionUptime, workloads ...discover.Workload) Estimate {
	t.Helper()
	return run(t, only(workloads...), func(in *Input) { in.Options.Ec2Uptime = up != nil; in.Uptime = up })
}

func TestEC2MeasuredTheActualCostOfTheHoursItRanTheSavingsOnlyWhileItRan(t *testing.T) {
	e := measuredVMs(t, uptimeOf(map[string][][2]int{"i-app": half()}, nil), vm("app"))
	x := nth(t, e.Workloads, 0)
	if !reflect.DeepEqual(x.Uptime, hours(12)) {
		t.Fatalf("uptime %+v", x.Uptime)
	}
	closeTo(t, "actual", x.ActualMonth, 0.0456*12*30, 9)
	closeTo(t, "at the window", x.AtWindow.USDMonth, usd(t, row(t, advise(t, daily(), []stopped.Item{T3}, always, 60, periodsOf(half())), 60)), 9)
	if !(x.AtWindow.USDMonth < nth(t, run(t, only(vm("app"))).Workloads, 0).AtWindow.USDMonth) {
		t.Fatal("measured saves no less than 24/7")
	}
}

func TestEC2StoppedWhenDiscoveredLeftOutWithoutEc2UptimeEstimatedWithIt(t *testing.T) {
	stoppedVM := vm("night", func(x *discover.Workload) { x.InstanceState = "stopped" })
	if got := reasons(run(t, only(stoppedVM)).LeftOut); !reflect.DeepEqual(got, []string{"a stopped instance: nothing running to stop (--ec2-uptime estimates it from the hours it ran)"}) {
		t.Fatalf("left out %q", got)
	}
	e := measuredVMs(t, uptimeOf(map[string][][2]int{"i-night": half()}, nil), stoppedVM)
	if got := estimatedGroups(e.Workloads); !reflect.DeepEqual(got, []string{"night"}) || !reflect.DeepEqual(e.Workloads[0].Uptime, hours(12)) {
		t.Fatalf("workloads %q, uptimes %+v", got, uptimes(e.Workloads))
	}
}

func TestEC2MeasuredNeverRunningInThe14DaysLeftOut(t *testing.T) {
	e := measuredVMs(t, uptimeOf(map[string][][2]int{"i-idle": {}}, nil), vm("idle", func(x *discover.Workload) { x.InstanceState = "stopped" }))
	if got := reasons(e.LeftOut); !reflect.DeepEqual(got, []string{"it did not run in the last 14 days (its measured uptime)"}) {
		t.Fatalf("left out %q", got)
	}
}

func TestEC2ItsUptimeUnreadable247AsWithoutSayingWhy(t *testing.T) {
	e := measuredVMs(t, uptimeOf(map[string][][2]int{}, map[string]string{"i-app": "CloudWatch could not read the uptime (Forbidden)"}), vm("app"))
	x := nth(t, e.Workloads, 0)
	if !reflect.DeepEqual(x.Uptime, unread("CloudWatch could not read the uptime (Forbidden)")) {
		t.Fatalf("uptime %+v", x.Uptime)
	}
	closeTo(t, "actual", x.ActualMonth, 0.0456*24*30, 9)
}

func TestTheAssumptionsSayHowEC2InstancesRan(t *testing.T) {
	if !strings.Contains(said(run(t).Assumptions), "EC2 instances without an Auto Scaling group are assumed to run 24/7 (--ec2-uptime measures it)") {
		t.Fatal("not said unmeasured")
	}
	if !strings.Contains(said(measuredVMs(t, uptimeOf(map[string][][2]int{"i-app": half()}, nil), vm("app")).Assumptions), "EC2 instances without an Auto Scaling group: their uptime measured over the 14 days") {
		t.Fatal("not said measured")
	}
}

func TestEC2ManagedByDormetoOrBehindAnAutoScalingGroupNotMeasured(t *testing.T) {
	napped := vm("napped", func(x *discover.Workload) { x.Managed = true; x.InstanceState = "" })
	e := measuredVMs(t, uptimeOf(map[string][][2]int{}, nil), napped, w(A, "eu-west-1", "fleet", T3))
	if got := uptimes(e.Workloads); !reflect.DeepEqual(got, []*Uptime{nil, nil}) {
		t.Fatalf("uptimes %+v", got)
	}
	if e.Totals.Kinds != (Kinds{ASG: 1, EC2: 1}) {
		t.Fatalf("kinds %+v", e.Totals.Kinds)
	}
}

func service(name string, change ...func(*discover.Workload)) discover.Workload {
	x := w(A, "eu-west-1", name, FARGATE)
	x.Backend = discover.WorkloadBackend{Kind: "ecs", ID: serviceArn(name)}
	one := 1
	x.EcsDesired = &one
	for _, c := range change {
		c(&x)
	}
	return x
}

func serviceArn(name string) string { return "arn:aws:ecs:eu-west-1:" + A + ":service/prod/" + name }

// measuredServices: with an uptime given, --ecs-uptime alone, or the options given.
func measuredServices(t *testing.T, up []metrics.RegionUptime, kinds *metrics.UptimeKinds, workloads ...discover.Workload) Estimate {
	t.Helper()
	return run(t, only(workloads...), func(in *Input) {
		if up != nil {
			k := metrics.UptimeKinds{ECS: true}
			if kinds != nil {
				k = *kinds
			}
			in.Options.Ec2Uptime, in.Options.EcsUptime = k.EC2, k.ECS
		}
		in.Uptime = up
	})
}

func TestECSMeasuredTheActualCostOfTheHoursItHadATask(t *testing.T) {
	e := measuredServices(t, uptimeOf(map[string][][2]int{serviceArn("api"): half()}, nil), nil, service("api"))
	x := nth(t, e.Workloads, 0)
	if !reflect.DeepEqual(x.Uptime, hours(12)) {
		t.Fatalf("uptime %+v", x.Uptime)
	}
	closeTo(t, "actual", x.ActualMonth, 0.0099*12*30, 9)
	if !(x.AtWindow.USDMonth < nth(t, run(t, only(service("api"))).Workloads, 0).AtWindow.USDMonth) {
		t.Fatal("measured saves no less than all the time")
	}
}

func TestECSAt0TasksLeftOutWithoutEcsUptimeEstimatedAsOneTaskWithIt(t *testing.T) {
	night := service("night", func(x *discover.Workload) { zero := 0; x.EcsDesired = &zero })
	if got := reasons(run(t, only(night)).LeftOut); !reflect.DeepEqual(got, []string{"an ECS service at 0 tasks: nothing running to stop (--ecs-uptime estimates it as one task from the hours it ran)"}) {
		t.Fatalf("left out %q", got)
	}
	e := measuredServices(t, uptimeOf(map[string][][2]int{serviceArn("night"): half()}, nil), nil, night)
	if got := estimatedGroups(e.Workloads); !reflect.DeepEqual(got, []string{"night"}) || !reflect.DeepEqual(e.Workloads[0].Uptime, hours(12)) {
		t.Fatalf("workloads %q, uptimes %+v", got, uptimes(e.Workloads))
	}
	idle := measuredServices(t, uptimeOf(map[string][][2]int{serviceArn("night"): {}}, nil), nil, night)
	if got := reasons(idle.LeftOut); !reflect.DeepEqual(got, []string{"it did not run in the last 14 days (its measured uptime)"}) {
		t.Fatalf("left out %q", got)
	}
}

func TestEc2UptimeAloneMeasuresNoECSServiceNorAStoppedInstanceWithEcsUptimeAlone(t *testing.T) {
	e := measuredServices(t, uptimeOf(map[string][][2]int{serviceArn("api"): half()}, nil), &metrics.UptimeKinds{EC2: true}, service("api"))
	x := nth(t, e.Workloads, 0)
	if x.Uptime != nil {
		t.Fatalf("uptime %+v", x.Uptime)
	}
	closeTo(t, "actual", x.ActualMonth, 0.0099*24*30, 9)
	stoppedVM := w(A, "eu-west-1", "vm", T3)
	stoppedVM.Backend = discover.WorkloadBackend{Kind: "instance", ID: "i-vm"}
	stoppedVM.InstanceState = "stopped"
	got := reasons(measuredServices(t, uptimeOf(map[string][][2]int{"i-vm": half()}, nil), nil, stoppedVM).LeftOut)
	if !reflect.DeepEqual(got, []string{"a stopped instance: nothing running to stop (--ec2-uptime estimates it from the hours it ran)"}) {
		t.Fatalf("left out %q", got)
	}
}

func TestECSManagedByDormetoNotMeasured(t *testing.T) {
	demo := service("demo", func(x *discover.Workload) { x.Managed = true; x.EcsDesired = nil })
	e := measuredServices(t, uptimeOf(map[string][][2]int{}, nil), nil, demo)
	if got := uptimes(e.Workloads); !reflect.DeepEqual(got, []*Uptime{nil}) {
		t.Fatalf("uptimes %+v", got)
	}
}

func TestTheAssumptionsSayHowECSServicesRan(t *testing.T) {
	if !strings.Contains(said(run(t).Assumptions), "ECS services are assumed to run all the time at their desired count when discovered (--ecs-uptime measures when they had a task)") {
		t.Fatal("not said unmeasured")
	}
	e := measuredServices(t, uptimeOf(map[string][][2]int{serviceArn("api"): half()}, nil), nil, service("api"))
	if !strings.Contains(said(e.Assumptions), "ECS services: their uptime measured over the 14 days") {
		t.Fatal("not said measured")
	}
	// Their tasks counted (the user, 2026-10-15).
	if !strings.Contains(said(e.Assumptions), "and how many tasks they had (CloudWatch LiveTaskCount, every minute), the count they are priced at; a service without it, at its desired count when discovered (one task for a service found at 0)") {
		t.Fatalf("not said counted: %s", said(e.Assumptions))
	}
}

// The days said are the days read (--days, the user, 2026-10-14): the reason a measured workload is left out and the
// assumptions of the uptimes measured.
func TestTheDaysSaidAreTheDaysRead(t *testing.T) {
	idle := vm("idle", func(x *discover.Workload) { x.InstanceState = "stopped" })
	e := run(t, only(idle), func(in *Input) {
		in.Options.Ec2Uptime, in.Options.EcsUptime, in.Options.Days = true, true, 7
		in.Uptime = uptimeOf(map[string][][2]int{"i-idle": {}}, nil)
	})
	if got := reasons(e.LeftOut); !reflect.DeepEqual(got, []string{"it did not run in the last 7 days (its measured uptime)"}) {
		t.Fatalf("left out %q", got)
	}
	all := said(e.Assumptions)
	if strings.Contains(all, "14 days") || !strings.Contains(all, "EC2 instances without an Auto Scaling group: their uptime measured over the 7 days") ||
		!strings.Contains(all, "ECS services: their uptime measured over the 7 days") {
		t.Fatalf("assumptions %s", all)
	}
}

// Its tasks counted (the user, 2026-10-15): a service measured at an average of 2.5 tasks while it ran is priced at 2.5
// tasks, its actual cost and its savings, whatever its count when discovered (0 at night: one task); its uptime says
// the tasks. A service found at 3 tasks and measured at 1.5 is priced at 1.5. Without a count, its count when
// discovered.
func TestECSPricedAtItsTasksCountedWhileItRan(t *testing.T) {
	running := 0
	for _, p := range half() {
		running += p[1] - p[0]
	}
	counted := func(name string, tasks float64) []metrics.RegionUptime {
		up := uptimeOf(map[string][][2]int{serviceArn(name): half()}, nil)
		up[0].Tasks, up[0].Counted = map[string]float64{serviceArn(name): tasks * float64(running)}, true
		return up
	}
	night := service("night", func(x *discover.Workload) { zero := 0; x.EcsDesired = &zero })
	base := nth(t, measuredServices(t, uptimeOf(map[string][][2]int{serviceArn("night"): half()}, nil), nil, night).Workloads, 0)
	x := nth(t, measuredServices(t, counted("night", 2.5), nil, night).Workloads, 0)
	if base.Uptime.Tasks != nil || x.Uptime.Tasks == nil || *x.Uptime.Tasks != 2.5 || *x.Uptime.HoursPerDay != 12 {
		t.Fatalf("uptime %+v, without the count %+v", x.Uptime, base.Uptime)
	}
	if x.Recommended.Minutes != base.Recommended.Minutes {
		t.Fatalf("recommended %d, without the count %d", x.Recommended.Minutes, base.Recommended.Minutes)
	}
	closeTo(t, "actual", x.ActualMonth, 2.5*base.ActualMonth, 9)
	closeTo(t, "at the window", x.AtWindow.USDMonth, 2.5*base.AtWindow.USDMonth, 9)
	closeTo(t, "recommended", x.Recommended.USDMonth, 2.5*base.Recommended.USDMonth, 9)

	three := FARGATE
	three.Count = 3
	busy := service("busy", func(x *discover.Workload) { n := 3; x.EcsDesired, x.Stopped = &n, []stopped.Item{three} })
	at3 := nth(t, measuredServices(t, uptimeOf(map[string][][2]int{serviceArn("busy"): half()}, nil), nil, busy).Workloads, 0)
	y := nth(t, measuredServices(t, counted("busy", 1.5), nil, busy).Workloads, 0)
	closeTo(t, "actual at 1.5 of 3", y.ActualMonth, at3.ActualMonth/2, 9)
}
