package metrics

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/cache"
)

// A CloudWatch fake from a function; calls are counted.
type fakeCW struct {
	mu    sync.Mutex
	calls []GetMetricDataInput
	fn    func(in GetMetricDataInput, call int) (GetMetricDataOutput, error)
}

func (f *fakeCW) GetMetricData(_ context.Context, in GetMetricDataInput) (GetMetricDataOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, in)
	n := len(f.calls)
	f.mu.Unlock()
	return f.fn(in, n)
}

func (f *fakeCW) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// empty answers every query with no value.
func empty(in GetMetricDataInput, _ int) (GetMetricDataOutput, error) {
	out := GetMetricDataOutput{}
	for _, q := range in.MetricDataQueries {
		out.MetricDataResults = append(out.MetricDataResults, MetricDataResult{ID: q.ID, StatusCode: "Complete"})
	}
	return out, nil
}

func dim(q MetricDataQuery, name string) string {
	for _, d := range q.MetricStat.Metric.Dimensions {
		if d.Name == name {
			return d.Value
		}
	}
	return ""
}

func of(cw CloudWatchPort) CloudWatchOf {
	return func(context.Context, string, string) (CloudWatchPort, error) { return cw, nil }
}

func newCache(t *testing.T, now func() time.Time) *cache.Cache {
	t.Helper()
	return cache.New(t.TempDir(), now)
}

func fixed(t time.Time) func() time.Time { return func() time.Time { return t } }

// concurrent measures the most calls in flight.
type concurrent struct{ active, most atomic.Int32 }

func (c *concurrent) enter() {
	n := c.active.Add(1)
	for {
		m := c.most.Load()
		if n <= m || c.most.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	c.active.Add(-1)
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %s\nwant %s", what, show(got), show(want))
	}
}

func show(v any) string { return strings.ReplaceAll(fmt.Sprintf("%#v", v), "metrics.", "") }
