package metrics

// Ports manager packages/estimate/test/requests.test.ts (8 cases).

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/Dormeto/cli/internal/discover"
)

const (
	acct = "111111111111"
	reg  = "eu-west-1"
)

var (
	now   = time.Date(2026, 10, 10, 12, 0, 30, 0, time.UTC)
	toT   = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	fromT = toT.Add(-14 * 24 * time.Hour)
	lbArn = "arn:aws:elasticloadbalancing:" + reg + ":" + acct + ":loadbalancer/app/web/0123456789abcdef"
)

func minute(m int) time.Time { return fromT.Add(time.Duration(m) * time.Minute) }

func tg(i int) string {
	return fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:targetgroup/app-%d/fedcba98765432%02d", reg, acct, i, i)
}

func workload(i int) discover.Workload {
	return discover.Workload{Account: acct, Region: reg, LoadBalancer: "web", LoadBalancerArn: lbArn, TargetGroup: fmt.Sprintf("app-%d", i), TargetGroupArn: tg(i), Hosts: []string{},
		Backend: discover.WorkloadBackend{Kind: "asg", ID: fmt.Sprintf("asg-%d", i)}, Tags: map[string][]string{}}
}

var appN = regexp.MustCompile(`app-(\d+)`)

// cloudwatch answers each query by its target group; the first call in two pages.
func cloudwatch(answer func(tgArn string) MetricDataResult) (*fakeCW, *[][]string) {
	var seen [][]string // per call: the queries' target groups, then the token
	f := &fakeCW{}
	f.fn = func(in GetMetricDataInput, call int) (GetMetricDataOutput, error) {
		row := []string{}
		for _, q := range in.MetricDataQueries {
			row = append(row, dim(q, "TargetGroup"))
		}
		seen = append(seen, append([]string{strconv.Itoa(len(in.MetricDataQueries)), in.NextToken}, row...))
		var results []MetricDataResult
		for _, q := range in.MetricDataQueries {
			n, _ := strconv.Atoi(appN.FindStringSubmatch(dim(q, "TargetGroup"))[1])
			r := answer(tg(n))
			r.ID = q.ID
			results = append(results, r)
		}
		cut := func(from, to int) []MetricDataResult {
			var out []MetricDataResult
			for _, r := range results {
				n := len(r.Timestamps)
				lo, hi := min(from, n), n
				if to >= 0 {
					hi = min(to, n)
				}
				r.Timestamps, r.Values = r.Timestamps[lo:hi], r.Values[lo:hi]
				out = append(out, r)
			}
			return out
		}
		switch {
		case call == 1:
			return GetMetricDataOutput{MetricDataResults: cut(0, 1), NextToken: "page-2"}, nil
		case in.NextToken == "page-2":
			return GetMetricDataOutput{MetricDataResults: cut(1, -1)}, nil
		}
		return GetMetricDataOutput{MetricDataResults: results}, nil
	}
	return f, &seen
}

func complete(ts []time.Time, vs []float64) MetricDataResult {
	return MetricDataResult{Timestamps: ts, Values: vs, StatusCode: "Complete"}
}

func TestFiveWorkloadsPerCallEveryPageByEachValuesTimestampATargetGroupsValuesNeverAnothers(t *testing.T) {
	f, seen := cloudwatch(func(arn string) MetricDataResult {
		switch arn {
		case tg(0):
			return complete([]time.Time{minute(9), minute(2)}, []float64{4, 1})
		case tg(1):
			return complete([]time.Time{minute(9)}, []float64{7})
		}
		return complete(nil, nil)
	})
	var ws []discover.Workload
	for i := range 12 {
		ws = append(ws, workload(i))
	}
	out, err := Requests(context.Background(), f, ws, 14, now)
	if err != nil {
		t.Fatal(err)
	}
	var shape [][2]string
	for _, row := range *seen {
		shape = append(shape, [2]string{row[0], row[1]})
	}
	eq(t, "calls (queries, token)", shape, [][2]string{{"5", ""}, {"5", "page-2"}, {"5", ""}, {"2", ""}})
	if out.Account != acct || out.Region != reg || out.From != "2026-09-26T12:00:00.000Z" || out.To != "2026-10-10T12:00:00.000Z" || len(out.Unreadable) != 0 {
		t.Errorf("out %+v", out)
	}
	eq(t, "tg0", out.Minutes[tg(0)], [][2]float64{{2, 1}, {9, 4}})
	eq(t, "tg1", out.Minutes[tg(1)], [][2]float64{{9, 7}})
	// No value at all: no request in 14 days, not unreadable.
	if m, ok := out.Minutes[tg(11)]; !ok || m == nil || len(m) != 0 {
		t.Errorf("tg11 %#v", m)
	}
	for _, c := range f.calls {
		if c.ScanBy != "TimestampAscending" || !c.StartTime.Equal(fromT) || !c.EndTime.Equal(toT) {
			t.Errorf("call %+v", c)
		}
		q := c.MetricDataQueries[0]
		if q.MetricStat.Period != 60 || q.MetricStat.Stat != "Sum" || q.MetricStat.Metric.Namespace != "AWS/ApplicationELB" || q.MetricStat.Metric.MetricName != "RequestCount" || !q.ReturnData {
			t.Errorf("query %+v", q)
		}
		eq(t, "dimensions", q.MetricStat.Metric.Dimensions[0], Dimension{Name: "LoadBalancer", Value: "app/web/0123456789abcdef"})
	}
}

func TestAWorkloadDormetoManagesItsApplicationAndWakerTargetGroupsAddedUpFiveMetricsACall(t *testing.T) {
	managed := workload(0)
	managed.Managed, managed.WakerTargetGroupArn = true, tg(9)
	f, seen := cloudwatch(func(arn string) MetricDataResult {
		switch arn {
		case tg(0):
			return complete([]time.Time{minute(9), minute(4)}, []float64{2, 1})
		case tg(9):
			return complete([]time.Time{minute(9), minute(5)}, []float64{3, 6})
		}
		return complete(nil, nil)
	})
	out, err := Requests(context.Background(), f, []discover.Workload{managed, workload(1), workload(2), workload(3), workload(4)}, 14, now)
	if err != nil {
		t.Fatal(err)
	}
	// Six metrics: five in the first call (two pages), one in the next.
	var sizes []string
	for _, row := range *seen {
		sizes = append(sizes, row[0])
	}
	eq(t, "queries per call", sizes, []string{"5", "5", "1"})
	eq(t, "tg0", out.Minutes[tg(0)], [][2]float64{{4, 1}, {5, 6}, {9, 5}})
	if _, ok := out.Minutes[tg(9)]; ok {
		t.Error("the waker is not a workload of its own")
	}
}

func TestAMetricCloudWatchCouldNotReadIsUnreadableTheOthersAreRead(t *testing.T) {
	f, _ := cloudwatch(func(arn string) MetricDataResult {
		if arn == tg(1) {
			return MetricDataResult{StatusCode: "Forbidden", Messages: []Message{{Code: "Forbidden", Value: "not authorized"}}}
		}
		return complete([]time.Time{minute(3)}, []float64{2})
	})
	out, err := Requests(context.Background(), f, []discover.Workload{workload(0), workload(1)}, 14, now)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "unreadable", out.Unreadable, map[string]string{tg(1): "CloudWatch could not read the requests (Forbidden: not authorized)"})
	eq(t, "tg0", out.Minutes[tg(0)], [][2]float64{{3, 2}})
	if _, ok := out.Minutes[tg(1)]; ok {
		t.Error("an unreadable workload has no minutes")
	}
}

func region(ws []discover.Workload, over ...func(*discover.RegionDiscovery)) discover.RegionDiscovery {
	r := discover.RegionDiscovery{Account: acct, Region: reg, Workloads: ws}
	for _, o := range over {
		o(&r)
	}
	return r
}

func TestRequestsOfEveryRegionReadParallelRegionsAtATimeFiveByDefaultInTheirOrder(t *testing.T) {
	var regions []discover.RegionDiscovery
	for i := range 8 {
		a := fmt.Sprintf("%d", i+1)
		a = a + a + a + a + a + a + a + a + a + a + a + a
		w := workload(i)
		w.Account = a
		regions = append(regions, region([]discover.Workload{w}, func(r *discover.RegionDiscovery) { r.Account = a }))
	}
	measure := func(concurrency int) (int32, []string) {
		var c concurrent
		cw := &fakeCW{fn: func(in GetMetricDataInput, n int) (GetMetricDataOutput, error) { c.enter(); return empty(in, n) }}
		out, err := RequestsAll(context.Background(), RequestsOptions{Regions: regions, CloudWatchOf: of(cw), Cache: newCache(t, fixed(now)), Now: fixed(now), Concurrency: concurrency})
		if err != nil {
			t.Fatal(err)
		}
		var accounts []string
		for _, r := range out {
			accounts = append(accounts, r.Account)
		}
		return c.most.Load(), accounts
	}
	var want []string
	for _, r := range regions {
		want = append(want, r.Account)
	}
	most, accounts := measure(0)
	if most != 5 {
		t.Errorf("default most %d", most)
	}
	eq(t, "order", accounts, want)
	if most, _ := measure(2); most != 2 {
		t.Errorf("most %d", most)
	}
}

func TestItSaysHowManyRegionsItReadsAndWhenEachStarts(t *testing.T) {
	var planned []int
	var started []string
	planFirst := true
	f, _ := cloudwatch(func(string) MetricDataResult { return complete(nil, nil) })
	_, err := RequestsAll(context.Background(), RequestsOptions{
		Regions: []discover.RegionDiscovery{
			region([]discover.Workload{workload(1)}),
			region(nil, func(r *discover.RegionDiscovery) { r.Region = "eu-west-3" }),
			region([]discover.Workload{workload(2)}, func(r *discover.RegionDiscovery) { r.Region, r.Error = "eu-central-1", "opt-in region not enabled" }),
		},
		CloudWatchOf: func(context.Context, string, string) (CloudWatchPort, error) {
			if len(planned) == 0 {
				planFirst = false
			}
			return f, nil
		},
		Cache: newCache(t, fixed(now)), Now: fixed(now),
		OnPlan:  func(n int) { planned = append(planned, n) },
		OnStart: func(a, r string) { started = append(started, a+" "+r) },
	})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "planned", planned, []int{1})
	eq(t, "started", started, []string{acct + " " + reg})
	if !planFirst {
		t.Error("onPlan comes before any read")
	}
}

func TestRequestsKeptPerAccountAndRegionTheSameUTCDayReadAgainTheNextDayOrWhenRefreshed(t *testing.T) {
	at := now
	cw := &fakeCW{fn: empty}
	c := newCache(t, func() time.Time { return at })
	run := func(refresh string) {
		if _, err := RequestsAll(context.Background(), RequestsOptions{Regions: []discover.RegionDiscovery{region([]discover.Workload{workload(0)})}, CloudWatchOf: of(cw), Cache: c, Refresh: refresh, Now: func() time.Time { return at }}); err != nil {
			t.Fatal(err)
		}
	}
	run("")
	run("")
	if cw.count() != 1 {
		t.Fatalf("reads %d", cw.count())
	}
	run("metrics")
	run("discovery")
	if cw.count() != 3 {
		t.Fatalf("reads %d", cw.count())
	}
	at = at.Add(12 * time.Hour)
	run("")
	if cw.count() != 4 {
		t.Fatalf("reads %d", cw.count())
	}
}

func TestARegionWhoseRequestsCannotBeReadAtAllItsWorkloadsUnreadableWithAWSsReasonTheOthersReadNothingKept(t *testing.T) {
	c := newCache(t, fixed(now))
	cw := &fakeCW{fn: empty}
	w1 := workload(1)
	w1.Region = "eu-west-3"
	regions := []discover.RegionDiscovery{region([]discover.Workload{workload(0)}), region([]discover.Workload{w1}, func(r *discover.RegionDiscovery) { r.Region = "eu-west-3" })}
	run := func() []RegionRequests {
		out, err := RequestsAll(context.Background(), RequestsOptions{Regions: regions, Cache: c, Now: fixed(now),
			CloudWatchOf: func(_ context.Context, _, where string) (CloudWatchPort, error) {
				if where == reg {
					return nil, &smithy.GenericAPIError{Code: "AccessDenied", Message: "User is not authorized to perform: cloudwatch:GetMetricData with an explicit deny in a service control policy"}
				}
				return cw, nil
			}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := run()
	eq(t, "unreadable", out[0].Unreadable, map[string]string{tg(0): "AccessDenied: User is not authorized to perform: cloudwatch:GetMetricData with an explicit deny in a service control policy"})
	if out[0].Region != reg || out[0].Minutes == nil || len(out[0].Minutes) != 0 {
		t.Errorf("out0 %+v", out[0])
	}
	if m := out[1].Minutes[tg(1)]; m == nil || len(m) != 0 {
		t.Errorf("out1 %+v", out[1])
	}
	run()
	if cw.count() != 1 {
		t.Errorf("reads %d", cw.count())
	}
}

func TestARegionWithoutWorkloadOrThatFailedAsksNothingAKeptFileMissingANewWorkloadIsReadAgain(t *testing.T) {
	cw := &fakeCW{fn: empty}
	c := newCache(t, fixed(now))
	run := func(rs ...discover.RegionDiscovery) []RegionRequests {
		out, err := RequestsAll(context.Background(), RequestsOptions{Regions: rs, CloudWatchOf: of(cw), Cache: c, Now: fixed(now)})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := run(region(nil), region(nil, func(r *discover.RegionDiscovery) { r.Region, r.Error = "eu-west-3", "opt-in region" }))
	if out == nil || len(out) != 0 || cw.count() != 0 {
		t.Errorf("out %#v reads %d", out, cw.count())
	}
	run(region([]discover.Workload{workload(0)}))
	run(region([]discover.Workload{workload(0), workload(1)}))
	if cw.count() != 2 {
		t.Errorf("reads %d", cw.count())
	}
}
