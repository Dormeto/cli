// Package metrics is phase 2 of the estimate (vision
// docs/specs/2026-10-10-savings-estimate-design.md §5): each eligible
// workload's requests per minute over 14 days, and the measured uptime of EC2
// instances, ECS services and companion databases, read from CloudWatch.
//
// This file ports manager packages/estimate/src/requests.ts: each workload's
// target group RequestCount (CloudWatch keeps one-minute data 15 days), five
// metrics per GetMetricData call (20,160 values each, under the 100,800 a call
// returns), every page, by each value's timestamp. CloudWatch publishes nothing
// for a minute without request: an empty series is a workload no one called; a
// metric it could not read is unreadable, never silence. Kept per account and
// region the same UTC day.
package metrics

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/cache"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/limit"
)

// Phases of the progress events this package emits.
const (
	PhaseRequests = "requests"
	PhaseUptime   = "uptime"
)

// Days is the period the estimate reads (CloudWatch keeps one-minute data 15 days).
const Days = 14

const (
	perCall         = 5
	defaultParallel = 5
)

// RegionRequests is a region's requests: by target group ARN, [minute, count]
// pairs (minutes from From, ascending; only minutes with a request) or why its
// metric could not be read. JSON names as Node's (the cache and the report).
type RegionRequests struct {
	Account    string                  `json:"account"`
	Region     string                  `json:"region"`
	From       string                  `json:"from"`
	To         string                  `json:"to"`
	Minutes    map[string][][2]float64 `json:"minutes"`
	Unreadable map[string]string       `json:"unreadable"`
}

// roundMinutes is Math.round((ms) / 60000).
func roundMinutes(d time.Duration) int {
	return int(math.Floor(float64(d.Milliseconds())/60000 + 0.5))
}

func iso(t time.Time) string { return t.UTC().Format(isoMillis) }

// queryIndex is Number(id.slice(1)): the index a query's id ends with, whatever
// its first character (an empty rest is 0, as Number(”)).
func queryIndex(id string, n int) (int, bool) {
	_, size := utf8.DecodeRuneInString(id)
	rest := id[size:]
	i := 0
	if rest != "" {
		var err error
		if i, err = strconv.Atoi(rest); err != nil {
			return 0, false
		}
	}
	return i, i >= 0 && i < n
}

// unreadableWhy is "(<status>[: <messages>])" for a result CloudWatch could not
// read, or "" for one it could (no status, Complete or PartialData).
func unreadableWhy(r MetricDataResult) (string, bool) {
	if r.StatusCode == "" || r.StatusCode == "Complete" || r.StatusCode == "PartialData" {
		return "", false
	}
	var why []string
	for _, m := range r.Messages {
		text := m.Value
		if text == "" {
			text = m.Code
		}
		if text != "" {
			why = append(why, text)
		}
	}
	if len(why) > 0 {
		return r.StatusCode + ": " + strings.Join(why, "; "), true
	}
	return r.StatusCode, true
}

// pages calls GetMetricData for the queries and hands every page's results to
// each, following NextToken.
func pages(ctx context.Context, cw CloudWatchPort, from, to time.Time, queries []MetricDataQuery, each func(MetricDataResult)) error {
	token := ""
	for {
		page, err := cw.GetMetricData(ctx, GetMetricDataInput{StartTime: from, EndTime: to, MetricDataQueries: queries, ScanBy: ScanByAscending, NextToken: token})
		if err != nil {
			return err
		}
		for _, r := range page.MetricDataResults {
			each(r)
		}
		if token = page.NextToken; token == "" {
			return nil
		}
	}
}

// Requests reads the requests per minute of the workloads' target groups over
// the days before now (to the minute): one metric per target group, a managed
// workload's waker too, added up to its own (R10). An error is a call that
// failed, whole.
func Requests(ctx context.Context, cw CloudWatchPort, workloads []discover.Workload, days int, now time.Time) (RegionRequests, error) {
	to := now.UTC().Truncate(time.Minute)
	from := to.Add(-time.Duration(days) * 24 * time.Hour)
	minutes := map[string][][2]float64{}
	unreadable := map[string]string{}
	type metric struct {
		i  int
		tg string
	}
	var metrics []metric
	for i, w := range workloads {
		metrics = append(metrics, metric{i, w.TargetGroupArn})
		if w.WakerTargetGroupArn != "" {
			metrics = append(metrics, metric{i, w.WakerTargetGroupArn})
		}
	}
	counts := make([]map[int]float64, len(workloads))
	for i := range counts {
		counts[i] = map[int]float64{}
	}
	for start := 0; start < len(metrics); start += perCall {
		batch := metrics[start:min(start+perCall, len(metrics))]
		queries := make([]MetricDataQuery, len(batch))
		for k, m := range batch {
			queries[k] = MetricDataQuery{
				ID: "m" + strconv.Itoa(k), ReturnData: true,
				MetricStat: MetricStat{
					Metric: Metric{Namespace: "AWS/ApplicationELB", MetricName: "RequestCount", Dimensions: []Dimension{
						{Name: "LoadBalancer", Value: discover.LBDimension(workloads[m.i].LoadBalancerArn)},
						{Name: "TargetGroup", Value: discover.TGDimension(m.tg)},
					}},
					Period: 60, Stat: "Sum",
				},
			}
		}
		err := pages(ctx, cw, from, to, queries, func(result MetricDataResult) {
			k, ok := queryIndex(result.ID, len(batch))
			if !ok {
				return
			}
			m := batch[k]
			w := workloads[m.i]
			if why, bad := unreadableWhy(result); bad {
				unreadable[w.TargetGroupArn] = "CloudWatch could not read the requests (" + why + ")"
				return
			}
			for j, timestamp := range result.Timestamps {
				value := 0.0
				if j < len(result.Values) {
					value = result.Values[j]
				}
				minute := roundMinutes(timestamp.Sub(from))
				if value > 0 && minute >= 0 && minute < days*1440 {
					counts[m.i][minute] += value
				}
			}
		})
		if err != nil {
			return RegionRequests{}, err
		}
	}
	for i, w := range workloads {
		if _, bad := unreadable[w.TargetGroupArn]; bad {
			continue
		}
		pairs := make([][2]float64, 0, len(counts[i]))
		for minute, n := range counts[i] {
			pairs = append(pairs, [2]float64{float64(minute), n})
		}
		slices.SortFunc(pairs, func(a, b [2]float64) int { return int(a[0] - b[0]) })
		minutes[w.TargetGroupArn] = pairs
	}
	out := RegionRequests{From: iso(from), To: iso(to), Minutes: minutes, Unreadable: unreadable}
	if len(workloads) > 0 {
		out.Account, out.Region = workloads[0].Account, workloads[0].Region
	}
	return out, nil
}

// RequestsOptions are the inputs of RequestsAll.
type RequestsOptions struct {
	Regions      []discover.RegionDiscovery
	CloudWatchOf CloudWatchOf
	Cache        *cache.Cache
	// Refresh is "discovery" or "metrics" (read again) or "".
	Refresh string
	Now     func() time.Time
	// Progress, OnPlan and OnStart are called one at a time (a mutex), though
	// regions run at once. OnPlan comes first, before any read.
	Progress func(discover.ProgressEvent)
	// Concurrency is the regions read at once (--parallel; 5 when < 1).
	Concurrency int
	// OnPlan says how many regions are read; OnStart says when each starts.
	OnPlan  func(count int)
	OnStart func(account, region string)
}

// events serializes the callbacks of a phase.
type events struct {
	mu       sync.Mutex
	progress func(discover.ProgressEvent)
	start    func(account, region string)
}

func (e *events) Progress(ev discover.ProgressEvent) {
	if e.progress != nil {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.progress(ev)
	}
}

func (e *events) Start(account, region string) {
	if e.start != nil {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.start(account, region)
	}
}

// RequestsAll reads the requests of every region with workloads and no error,
// --parallel regions at a time, in the regions' order. A region it cannot read
// at all is a RegionRequests whose workloads are unreadable with AWS's reason;
// the error is the context's.
func RequestsAll(ctx context.Context, o RequestsOptions) ([]RegionRequests, error) {
	toRead := []discover.RegionDiscovery{}
	for _, r := range o.Regions {
		if r.Error == "" && len(r.Workloads) > 0 {
			toRead = append(toRead, r)
		}
	}
	if o.OnPlan != nil {
		o.OnPlan(len(toRead))
	}
	say := &events{progress: o.Progress, start: o.OnStart}
	return limit.Map(ctx, toRead, concurrencyOf(o.Concurrency), func(ctx context.Context, r discover.RegionDiscovery) (RegionRequests, error) {
		say.Start(r.Account, r.Region)
		return regionRequests(ctx, r, o, say)
	})
}

func concurrencyOf(n int) int {
	if n < 1 {
		return defaultParallel
	}
	return n
}

// regionRequests is one region's requests: kept, read, or unreadable with AWS's reason.
func regionRequests(ctx context.Context, region discover.RegionDiscovery, o RequestsOptions, say *events) (RegionRequests, error) {
	path := fmt.Sprintf("requests/%s-%s.json", region.Account, region.Region)
	now := o.Now()
	if o.Refresh == "" {
		var kept RegionRequests
		ok, err := o.Cache.Get(path, cache.FreshRequests(now), &kept)
		if err != nil {
			return RegionRequests{}, err
		}
		// Kept only while it covers every workload discovery found (a new one since: read again).
		if ok && coversAll(region.Workloads, kept) {
			say.Progress(discover.ProgressEvent{Phase: PhaseRequests, Account: region.Account, Region: region.Region, From: discover.FromCache, Workloads: len(region.Workloads)})
			return kept, nil
		}
	}
	read, err := func() (RegionRequests, error) {
		cw, err := o.CloudWatchOf(ctx, region.Account, region.Region)
		if err != nil {
			return RegionRequests{}, err
		}
		read, err := Requests(ctx, cw, region.Workloads, Days, now)
		if err != nil {
			return RegionRequests{}, err
		}
		return read, o.Cache.Put(path, read)
	}()
	if err != nil {
		if ctx.Err() != nil {
			return RegionRequests{}, ctx.Err()
		}
		// A region whose requests cannot be read at all (a denial, throttling,
		// expired credentials): its workloads are unreadable with AWS's reason,
		// nothing is kept, and the run goes on.
		why := awserr.Said(err)
		to := now.UTC().Truncate(time.Minute)
		read = RegionRequests{Account: region.Account, Region: region.Region, From: iso(to.Add(-Days * 24 * time.Hour)), To: iso(to), Minutes: map[string][][2]float64{}, Unreadable: map[string]string{}}
		for _, w := range region.Workloads {
			read.Unreadable[w.TargetGroupArn] = why
		}
		say.Progress(discover.ProgressEvent{Phase: PhaseRequests, Account: region.Account, Region: region.Region, From: discover.FromAWS, Workloads: len(region.Workloads), Error: why})
		return read, nil
	}
	say.Progress(discover.ProgressEvent{Phase: PhaseRequests, Account: region.Account, Region: region.Region, From: discover.FromAWS, Workloads: len(region.Workloads)})
	return read, nil
}

func coversAll(workloads []discover.Workload, kept RegionRequests) bool {
	for _, w := range workloads {
		_, minutes := kept.Minutes[w.TargetGroupArn]
		_, unreadable := kept.Unreadable[w.TargetGroupArn]
		if !minutes && !unreadable {
			return false
		}
	}
	return true
}
