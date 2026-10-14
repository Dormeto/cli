package metrics

// Ports manager packages/estimate/test/uptime.test.ts (11 cases).

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/Dormeto/cli/internal/discover"
)

const (
	fromS = "2026-09-26T12:00:00.000Z"
	toS   = "2026-10-10T12:00:00.000Z"
)

// every5 is every 5 minutes from one minute offset to another (end excluded).
func every5(from, to int) []time.Time {
	var out []time.Time
	for m := from; m < to; m += 5 {
		out = append(out, minute(m))
	}
	return out
}

func ones(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func uptimeCW(answer func(id string) MetricDataResult) *fakeCW {
	return &fakeCW{fn: func(in GetMetricDataInput, _ int) (GetMetricDataOutput, error) {
		var out GetMetricDataOutput
		for _, q := range in.MetricDataQueries {
			r := answer(dim(q, "InstanceId"))
			r.ID = q.ID
			out.MetricDataResults = append(out.MetricDataResults, r)
		}
		return out, nil
	}}
}

// 08:00 to 20:00 UTC each day: the period starts at 12:00, so 20:00 is minute 480, the next 08:00 minute 1200.
func daytime() [][2]int {
	var out [][2]int
	for d := range 14 {
		out = append(out, [2]int{max(0, d*1440-240), d*1440 + 480})
	}
	return out
}

func TestEachInstancesRunningPeriodsInMinutesOfThePeriodFromItsFiveMinuteCPUUtilizationNoneForOneThatNeverRan(t *testing.T) {
	var stamps []time.Time
	for _, p := range daytime() {
		stamps = append(stamps, every5(p[0], p[1])...)
	}
	f := uptimeCW(func(id string) MetricDataResult {
		if id == "i-day" {
			return MetricDataResult{Timestamps: stamps, Values: ones(len(stamps), 1), StatusCode: "Complete"}
		}
		return MetricDataResult{StatusCode: "Complete"}
	})
	read, err := Uptime(context.Background(), f, []string{"i-day", "i-off"}, fromS, toS)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "i-day", read.Running["i-day"], daytime())
	if r := read.Running["i-off"]; r == nil || len(r) != 0 {
		t.Errorf("i-off %#v", r)
	}
	if read.Unreadable == nil || len(read.Unreadable) != 0 {
		t.Errorf("unreadable %#v", read.Unreadable)
	}
	c := f.calls[0]
	if !c.StartTime.Equal(minute(0)) || !c.EndTime.Equal(toT) || c.ScanBy != "TimestampAscending" {
		t.Errorf("call %+v", c)
	}
	eq(t, "stat", c.MetricDataQueries[0].MetricStat, MetricStat{
		Metric: Metric{Namespace: "AWS/EC2", MetricName: "CPUUtilization", Dimensions: []Dimension{{Name: "InstanceId", Value: "i-day"}}}, Period: 300, Stat: "SampleCount",
	})
}

func TestAGapOfOneMissingDatapointIsAStopDatapointsOutOfOrderAreSorted(t *testing.T) {
	stamps := append(every5(0, 30), every5(35, 60)...)
	slices.Reverse(stamps)
	f := uptimeCW(func(string) MetricDataResult {
		return MetricDataResult{Timestamps: stamps, Values: ones(len(stamps), 1), StatusCode: "Complete"}
	})
	read, err := Uptime(context.Background(), f, []string{"i-1"}, fromS, toS)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "i-1", read.Running["i-1"], [][2]int{{0, 30}, {35, 60}})
}

func TestTwentyInstancesPerCallAnInstanceCloudWatchCouldNotReadIsUnreadableNeverStopped(t *testing.T) {
	var ids []string
	for i := range 25 {
		ids = append(ids, fmt.Sprintf("i-%d", i))
	}
	f := uptimeCW(func(id string) MetricDataResult {
		if id == "i-3" {
			return MetricDataResult{StatusCode: "Forbidden", Messages: []Message{{Value: "denied"}}}
		}
		return MetricDataResult{StatusCode: "Complete"}
	})
	read, err := Uptime(context.Background(), f, ids, fromS, toS)
	if err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for _, c := range f.calls {
		sizes = append(sizes, len(c.MetricDataQueries))
	}
	eq(t, "calls", sizes, []int{20, 5})
	eq(t, "unreadable", read.Unreadable, map[string]string{"i-3": "CloudWatch could not read the uptime (Forbidden: denied)"})
	if _, ok := read.Running["i-3"]; ok {
		t.Error("i-3 is in running")
	}
}

// asked records every query's namespace and dimensions as "ns name=value,...".
func asking(stamps []time.Time, value float64, asked *[]string) *fakeCW {
	return &fakeCW{fn: func(in GetMetricDataInput, _ int) (GetMetricDataOutput, error) {
		var out GetMetricDataOutput
		for _, q := range in.MetricDataQueries {
			var ds []string
			for _, d := range q.MetricStat.Metric.Dimensions {
				ds = append(ds, d.Name+"="+d.Value)
			}
			*asked = append(*asked, q.MetricStat.Metric.Namespace+" "+strings.Join(ds, ","))
			out.MetricDataResults = append(out.MetricDataResults, MetricDataResult{ID: q.ID, Timestamps: stamps, Values: ones(len(stamps), value), StatusCode: "Complete"})
		}
		return out, nil
	}}
}

func TestEachServicesRunningPeriodsFromItsCPUUtilizationByClusterAndServiceAnOldARNNamesTheDefaultCluster(t *testing.T) {
	var asked []string
	api := "arn:aws:ecs:" + reg + ":" + acct + ":service/prod/api"
	old := "arn:aws:ecs:" + reg + ":" + acct + ":service/legacy"
	read, err := Uptime(context.Background(), asking(every5(0, 60), 5, &asked), []string{api, old, "i-web"}, fromS, toS)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "running", read.Running, map[string][][2]int{api: {{0, 60}}, old: {{0, 60}}, "i-web": {{0, 60}}})
	eq(t, "asked", asked, []string{
		"AWS/ECS ClusterName=prod,ServiceName=api",
		"AWS/ECS ClusterName=default,ServiceName=legacy",
		"AWS/EC2 InstanceId=i-web",
		// Then the services' tasks.
		"AWS/ECS ClusterName=prod,ServiceName=api",
		"AWS/ECS ClusterName=default,ServiceName=legacy",
	})
}

func TestAnRDSInstanceByItsIdentifierAnAuroraClusterByItsClusterIdentifier(t *testing.T) {
	var asked []string
	read, err := Uptime(context.Background(), asking(every5(0, 30), 5, &asked), []string{"rds-instance:shop-db", "rds-cluster:aurora"}, fromS, toS)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "running", read.Running, map[string][][2]int{"rds-instance:shop-db": {{0, 30}}, "rds-cluster:aurora": {{0, 30}}})
	eq(t, "asked", asked, []string{"AWS/RDS DBInstanceIdentifier=shop-db", "AWS/RDS DBClusterIdentifier=aurora"})
}

// The region of the uptime cases.
func w(name string, over ...func(*discover.Workload)) discover.Workload {
	x := discover.Workload{Account: acct, Region: reg, LoadBalancer: "web", LoadBalancerArn: "arn:lb", TargetGroup: name, TargetGroupArn: "arn:tg/" + name, Hosts: []string{},
		Backend: discover.WorkloadBackend{Kind: "instance", ID: "i-" + name}, InstanceState: "running", Tags: map[string][]string{}}
	for _, o := range over {
		o(&x)
	}
	return x
}

func uptimeRegions() []discover.RegionDiscovery {
	return []discover.RegionDiscovery{{Account: acct, Region: reg, Workloads: []discover.Workload{
		w("web"),
		w("napped", func(x *discover.Workload) { x.Managed, x.InstanceState = true, "" }),
		w("fleet", func(x *discover.Workload) {
			x.Backend, x.InstanceState = discover.WorkloadBackend{Kind: "asg", ID: "fleet"}, ""
		}),
		w("night", func(x *discover.Workload) { x.InstanceState = "stopped" }),
	}}}
}

func periods(regions ...discover.RegionDiscovery) []RegionRequests {
	var out []RegionRequests
	for _, r := range regions {
		out = append(out, RegionRequests{Account: r.Account, Region: r.Region, From: fromS, To: toS, Minutes: map[string][][2]float64{}, Unreadable: map[string]string{}})
	}
	return out
}

func instancesOf(f *fakeCW) []string {
	var out []string
	for _, c := range f.calls {
		for _, q := range c.MetricDataQueries {
			out = append(out, dim(q, "InstanceId"))
		}
	}
	return out
}

func TestTheInstancesWithoutAnAutoScalingGroupDormetoDoesNotManageOverTheirRegionsRequestsPeriodKeptTheSameDay(t *testing.T) {
	regions := uptimeRegions()
	f := uptimeCW(func(string) MetricDataResult { return MetricDataResult{StatusCode: "Complete"} })
	c := newCache(t, fixed(now))
	run := func(refresh string) []RegionUptime {
		out, err := UptimeAll(context.Background(), UptimeOptions{Regions: regions, Requests: periods(regions...), CloudWatchOf: of(f), Cache: c, Refresh: refresh, Now: fixed(now)})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	read := run("")[0]
	eq(t, "instances", instancesOf(f), []string{"i-web", "i-night"})
	if read.Account != acct || read.Region != reg || read.From != fromS || read.To != toS || len(read.Running) != 2 || read.Running["i-web"] == nil || read.Running["i-night"] == nil {
		t.Errorf("read %+v", read)
	}
	run("")
	if f.count() != 1 {
		t.Errorf("reads %d", f.count())
	}
	run("metrics")
	if f.count() != 2 {
		t.Errorf("reads %d", f.count())
	}
}

func TestECSUptimeMeasuresTheECSServicesDormetoDoesNotManageEC2UptimeTheInstancesBothWithUptime(t *testing.T) {
	api := "arn:aws:ecs:" + reg + ":" + acct + ":service/prod/api"
	regions := uptimeRegions()
	regions[0].Workloads = append(regions[0].Workloads,
		w("api", func(x *discover.Workload) {
			x.Backend, x.InstanceState = discover.WorkloadBackend{Kind: "ecs", ID: api}, ""
			zero := 0
			x.EcsDesired = &zero
		}),
		w("demo-ecs", func(x *discover.Workload) {
			x.Backend = discover.WorkloadBackend{Kind: "ecs", ID: "arn:aws:ecs:" + reg + ":" + acct + ":service/prod/demo"}
			x.InstanceState, x.Managed = "", true
		}))
	measured := func(kinds UptimeKinds) []string {
		var asked []string
		f := &fakeCW{fn: func(in GetMetricDataInput, n int) (GetMetricDataOutput, error) {
			for _, q := range in.MetricDataQueries {
				var vs []string
				for _, d := range q.MetricStat.Metric.Dimensions {
					vs = append(vs, d.Value)
				}
				asked = append(asked, strings.Join(vs, "/"))
			}
			return empty(in, n)
		}}
		if _, err := UptimeAll(context.Background(), UptimeOptions{Regions: regions, Requests: periods(regions...), CloudWatchOf: of(f), Cache: newCache(t, fixed(now)), Now: fixed(now), Kinds: &kinds}); err != nil {
			t.Fatal(err)
		}
		return asked
	}
	// A service twice: its CPUUtilization, then its tasks.
	eq(t, "ecs", measured(UptimeKinds{ECS: true}), []string{"prod/api", "prod/api"})
	eq(t, "ec2", measured(UptimeKinds{EC2: true}), []string{"i-web", "i-night"})
	eq(t, "both", measured(UptimeKinds{EC2: true, ECS: true}), []string{"i-web", "i-night", "prod/api", "prod/api"})
}

func TestRDSUptimeMeasuresTheRegionsCompanionDatabasesThatANapCouldStopStoppedOnesIncluded(t *testing.T) {
	regions := uptimeRegions()
	db := func(kind, id, status, notStoppable string) discover.Database {
		return discover.Database{Kind: kind, ID: id, Status: status, AllowedFrom: []string{"sg-a"}, Tags: map[string][]string{}, NotStoppable: notStoppable}
	}
	regions[0].Databases = []discover.Database{
		db("rds-instance", "shop-db", "available", ""), db("rds-instance", "night-db", "stopped", ""),
		db("rds-instance", "replica", "available", "a read replica: cannot be stopped"), db("rds-cluster", "aurora", "available", ""),
	}
	var asked []string
	f := &fakeCW{fn: func(in GetMetricDataInput, n int) (GetMetricDataOutput, error) {
		for _, q := range in.MetricDataQueries {
			asked = append(asked, q.MetricStat.Metric.Dimensions[0].Value)
		}
		return empty(in, n)
	}}
	out, err := UptimeAll(context.Background(), UptimeOptions{Regions: regions, Requests: periods(regions...), CloudWatchOf: of(f), Cache: newCache(t, fixed(now)), Now: fixed(now), Kinds: &UptimeKinds{RDS: true}})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "asked", asked, []string{"shop-db", "night-db", "aurora"})
	var keys []string
	for k := range out[0].Running {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	eq(t, "keys", keys, []string{"rds-cluster:aurora", "rds-instance:night-db", "rds-instance:shop-db"})
	if DatabaseKey(regions[0].Databases[0]) != "rds-instance:shop-db" {
		t.Error("databaseKey")
	}
}

func TestUptimeReadsParallelRegionsAtATimeFiveByDefaultInTheirOrder(t *testing.T) {
	var accounts []string
	var many []discover.RegionDiscovery
	for i := range 8 {
		a := strings.Repeat(fmt.Sprint(i+1), 12)
		accounts = append(accounts, a)
		many = append(many, discover.RegionDiscovery{Account: a, Region: reg, Workloads: []discover.Workload{w("web", func(x *discover.Workload) { x.Account = a })}})
	}
	measure := func(concurrency int) (int32, []string) {
		var c concurrent
		f := &fakeCW{fn: func(in GetMetricDataInput, n int) (GetMetricDataOutput, error) { c.enter(); return empty(in, n) }}
		out, err := UptimeAll(context.Background(), UptimeOptions{Regions: many, Requests: periods(many...), CloudWatchOf: of(f), Cache: newCache(t, fixed(now)), Now: fixed(now), Concurrency: concurrency})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range out {
			got = append(got, r.Account)
		}
		return c.most.Load(), got
	}
	most, got := measure(0)
	if most != 5 {
		t.Errorf("most %d", most)
	}
	eq(t, "order", got, accounts)
	if most, _ := measure(2); most != 2 {
		t.Errorf("most %d", most)
	}
}

func TestUptimeSaysHowManyRegionsItMeasuresAndWhenEachStarts(t *testing.T) {
	regions := uptimeRegions()
	nothing := discover.RegionDiscovery{Account: acct, Region: "eu-west-3", Workloads: []discover.Workload{
		w("fleet", func(x *discover.Workload) {
			x.Region, x.Backend, x.InstanceState = "eu-west-3", discover.WorkloadBackend{Kind: "asg", ID: "fleet"}, ""
		})}}
	all := append(regions, nothing)
	var planned []int
	var started []string
	f := uptimeCW(func(string) MetricDataResult { return MetricDataResult{StatusCode: "Complete"} })
	_, err := UptimeAll(context.Background(), UptimeOptions{Regions: all, Requests: periods(all...), CloudWatchOf: of(f), Cache: newCache(t, fixed(now)), Now: fixed(now),
		OnPlan: func(n int) { planned = append(planned, n) }, OnStart: func(a, r string) { started = append(started, a+" "+r) }})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "planned", planned, []int{1})
	eq(t, "started", started, []string{acct + " " + reg})
}

func TestARegionWhoseUptimeCannotBeReadAtAllItsInstancesUnreadableWithAWSsReasonTheRunGoesOn(t *testing.T) {
	regions := uptimeRegions()
	f := &fakeCW{fn: func(GetMetricDataInput, int) (GetMetricDataOutput, error) {
		return GetMetricDataOutput{}, &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized to perform: cloudwatch:GetMetricData"}
	}}
	out, err := UptimeAll(context.Background(), UptimeOptions{Regions: regions, Requests: periods(regions...), CloudWatchOf: of(f), Cache: newCache(t, fixed(now)), Now: fixed(now)})
	if err != nil {
		t.Fatal(err)
	}
	why := "AccessDenied: not authorized to perform: cloudwatch:GetMetricData"
	eq(t, "unreadable", out[0].Unreadable, map[string]string{"i-web": why, "i-night": why})
	if out[0].Running == nil || len(out[0].Running) != 0 {
		t.Errorf("running %#v", out[0].Running)
	}
}

// --ecs-uptime counts the tasks too (the user, 2026-10-15: services found at 0 at night were priced as one task, and
// autoscaled ones at their count when discovered): after the CPUUtilization calls, unchanged, each service's
// LiveTaskCount (published every minute while it has a task), its maximum by 5 minutes times 5, its task-minutes over
// the period as its running time counts them (a period's Sum or Average undercounts the minutes a task starts or
// drains: 0.90 and 0.94 for a service of one task, the maximum 0.98). An instance or a database has none; a service
// without the metric, no count.
func TestEachServicesTaskMinutesFromItsLiveTaskCountAfterTheCPUCalls(t *testing.T) {
	api := "arn:aws:ecs:" + reg + ":" + acct + ":service/prod/api"
	web := "arn:aws:ecs:" + reg + ":" + acct + ":service/prod/web"
	var calls [][]string
	f := &fakeCW{fn: func(in GetMetricDataInput, _ int) (GetMetricDataOutput, error) {
		var out GetMetricDataOutput
		var asked []string
		for _, q := range in.MetricDataQueries {
			m := q.MetricStat.Metric
			who := dim(q, "ServiceName") + dim(q, "InstanceId")
			asked = append(asked, fmt.Sprintf("%s %s %d %s", m.MetricName, q.MetricStat.Stat, q.MetricStat.Period, who))
			r := MetricDataResult{ID: q.ID, StatusCode: "Complete"}
			switch {
			case m.MetricName == "CPUUtilization":
				r.Timestamps, r.Values = every5(0, 60), ones(12, 5)
			case m.MetricName == "LiveTaskCount" && who == "api":
				// Three tasks: 15 task-minutes every 5 minutes.
				r.Timestamps, r.Values = every5(0, 60), ones(12, 3)
			}
			out.MetricDataResults = append(out.MetricDataResults, r)
		}
		calls = append(calls, asked)
		return out, nil
	}}
	read, err := Uptime(context.Background(), f, []string{api, web, "i-web"}, fromS, toS)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "running", read.Running, map[string][][2]int{api: {{0, 60}}, web: {{0, 60}}, "i-web": {{0, 60}}})
	eq(t, "tasks", read.Tasks, map[string]float64{api: 180})
	eq(t, "calls", calls, [][]string{
		{"CPUUtilization SampleCount 300 api", "CPUUtilization SampleCount 300 web", "CPUUtilization SampleCount 300 i-web"},
		{"LiveTaskCount Maximum 300 api", "LiveTaskCount Maximum 300 web"},
	})
	// No service: no task call.
	calls = nil
	if _, err := Uptime(context.Background(), f, []string{"i-web"}, fromS, toS); err != nil || len(calls) != 1 {
		t.Fatalf("calls %q, %v", calls, err)
	}
}

// An uptime kept from before the tasks were counted is read again; one that counted them is kept.
func TestAnUptimeKeptWithoutTheTasksCountedIsReadAgain(t *testing.T) {
	api := "arn:aws:ecs:" + reg + ":" + acct + ":service/prod/api"
	regions := []discover.RegionDiscovery{{Account: acct, Region: reg, Workloads: []discover.Workload{
		w("api", func(x *discover.Workload) {
			x.Backend, x.InstanceState = discover.WorkloadBackend{Kind: "ecs", ID: api}, ""
		})}}}
	c := newCache(t, fixed(now))
	requests := periods(regions...)
	old := RegionUptime{Account: acct, Region: reg, From: requests[0].From, To: requests[0].To, Running: map[string][][2]int{api: {{0, 60}}}, Unreadable: map[string]string{}}
	if err := c.Put(fmt.Sprintf("uptime/%s-%s.json", acct, reg), old); err != nil {
		t.Fatal(err)
	}
	cw := &fakeCW{fn: empty}
	run := func() RegionUptime {
		out, err := UptimeAll(context.Background(), UptimeOptions{Regions: regions, Requests: requests, CloudWatchOf: of(cw), Cache: c, Now: fixed(now), Kinds: &UptimeKinds{ECS: true}})
		if err != nil {
			t.Fatal(err)
		}
		return out[0]
	}
	if r := run(); cw.count() != 2 || !r.Counted {
		t.Fatalf("calls %d, counted %v: the kept uptime without the tasks read again", cw.count(), r.Counted)
	}
	run()
	if cw.count() != 2 {
		t.Fatalf("calls %d: the uptime with the tasks kept", cw.count())
	}
}
