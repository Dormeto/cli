package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/discover"
)

// countingCW answers every query with no point and counts the calls.
type countingCW struct{ calls *int }

func (c countingCW) GetMetricData(_ context.Context, in GetMetricDataInput) (GetMetricDataOutput, error) {
	*c.calls++
	out := GetMetricDataOutput{}
	for _, q := range in.MetricDataQueries {
		out.MetricDataResults = append(out.MetricDataResults, MetricDataResult{ID: q.ID, StatusCode: "Complete"})
	}
	return out, nil
}

// Signals kept without their tasks are read again by a run that counts them (--ecs-uptime), as an uptime is.
func TestStandaloneSignalsKeptWithoutTheirTasksAreReadAgainToCountThem(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	calls := 0
	o := StandaloneOptions{
		Regions: []discover.StandaloneRegion{{Account: "111122223333", Region: "eu-west-1", Resources: []discover.StandaloneResource{
			{Kind: discover.KindECS, ID: "arn:aws:ecs:eu-west-1:111122223333:service/jobs/worker", ARN: "arn:aws:ecs:eu-west-1:111122223333:service/jobs/worker"}}}},
		CloudWatchOf: func(context.Context, string, string) (CloudWatchPort, error) { return countingCW{&calls}, nil },
		Cache:        newCache(t, fixed(now)), Now: fixed(now), Days: 14,
	}
	for _, tasks := range []bool{false, false, true, true} {
		o.Tasks = tasks
		if _, err := StandaloneAll(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	// The first run reads the signals; the second keeps them; the third reads them again and the tasks; the fourth keeps.
	if calls != 3 {
		t.Fatalf("%d calls, want 3", calls)
	}
}
