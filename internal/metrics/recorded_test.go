package metrics

// The requests and the uptime driven by AWS-shaped recorded answers: the
// cloudwatch.getMetricData entries of testdata/parity/estimate.json (the Node
// estimate's world), which the end-to-end case replays. The results must be
// those the Node estimate wrote in testdata/parity/cache.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/parity"
)

// recordedCW answers GetMetricData from the recorded entries of an account and region.
type recordedCW struct {
	t               *testing.T
	a               *parity.Answers
	account, region string
}

func (r recordedCW) GetMetricData(_ context.Context, in GetMetricDataInput) (out GetMetricDataOutput, err error) {
	r.t.Helper()
	if liveTasksOnly(in) {
		// Node never read the tasks (Go only, the user, 2026-10-15): CloudWatch's answer for a service without the
		// metric, so the uptime is priced as Node's.
		return empty(in, 0)
	}
	return out, parity.AWSError(r.a.Call("cloudwatch", "getMetricData", map[string]any{"account": r.account, "region": r.region, "input": in}, &out))
}

func kept[T any](t *testing.T, dir, name string) T {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "parity", "cache", "o-a1b2c3d4e5", dir, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Value T `json:"value"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env.Value
}

func TestTheRequestsAndTheUptimeDrivenByTheEstimateCasesRecordedAnswersAreThoseNodeWrote(t *testing.T) {
	c := parity.Load(t, "estimate").Cases[0]
	var in struct {
		Now     time.Time       `json:"now"`
		Answers json.RawMessage `json:"answers"`
	}
	if err := json.Unmarshal(c.Input, &in); err != nil {
		t.Fatal(err)
	}
	answers := parity.Recorded(t, in.Answers)
	var regions []discover.RegionDiscovery
	for _, name := range []string{"111111111111-eu-west-1", "222222222222-eu-west-1"} {
		region := kept[discover.RegionDiscovery](t, "discovery", name)
		// The case's --exclude-tags env=dev, applied before the requests are read (cli.ts excludeByTags).
		var workloads []discover.Workload
		for _, w := range region.Workloads {
			if !discover.ResourceTagsMatch(map[string][]string{"env": {"dev"}}, w.Tags) {
				workloads = append(workloads, w)
			}
		}
		region.Workloads = workloads
		regions = append(regions, region)
	}
	cloudwatchOf := func(_ context.Context, account, region string) (CloudWatchPort, error) {
		return recordedCW{t, answers, account, region}, nil
	}
	cache := newCache(t, fixed(in.Now))

	requests, err := RequestsAll(context.Background(), RequestsOptions{Regions: regions, CloudWatchOf: cloudwatchOf, Cache: cache, Now: fixed(in.Now)})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("%d regions", len(requests))
	}
	for i, name := range []string{"111111111111-eu-west-1", "222222222222-eu-west-1"} {
		want := kept[json.RawMessage](t, "requests", name)
		got, _ := json.Marshal(requests[i])
		parity.Equal(t, want, got)
	}

	uptime, err := UptimeAll(context.Background(), UptimeOptions{Regions: regions, Requests: requests, CloudWatchOf: cloudwatchOf, Cache: cache, Now: fixed(in.Now), Kinds: &UptimeKinds{EC2: true, ECS: true, RDS: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(uptime) != 2 {
		t.Fatalf("%d regions", len(uptime))
	}
	for i, name := range []string{"111111111111-eu-west-1", "222222222222-eu-west-1"} {
		want := kept[json.RawMessage](t, "uptime", name)
		if !uptime[i].Counted || len(uptime[i].Tasks) != 0 {
			t.Fatalf("counted %v, tasks %v", uptime[i].Counted, uptime[i].Tasks)
		}
		uptime[i].Counted = false // Go only
		got, _ := json.Marshal(uptime[i])
		parity.Equal(t, want, got)
	}
}

// liveTasksOnly says whether a call asks only for services' LiveTaskCount (Go only).
func liveTasksOnly(in GetMetricDataInput) bool {
	for _, q := range in.MetricDataQueries {
		if q.MetricStat.Metric.MetricName != "LiveTaskCount" {
			return false
		}
	}
	return len(in.MetricDataQueries) > 0
}
