package metrics

// Phase 2 of the estimate's standalone part (vision standalone workloads design §7): each standalone resource's
// default signals (discover.DefaultSignals: the router's built-ins, CPU under 5 %, a database's connections under 1),
// their 5-minute points over the days read, one GetMetricData call (and its pages) a resource. A resource whose metrics
// CloudWatch could not read is unreadable with the reason, never idle. Kept per account and region the same UTC day,
// apart from the requests (signals/<account>-<region>.json).

import (
	"context"
	"fmt"
	"time"

	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/cache"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/limit"
)

// PhaseStandalone is the Phase of the events the standalone signals' read emits.
const PhaseStandalone = "standalone"

// StandalonePlan is a standalone resource's default signals as the activity's plan: each signal a query of its own,
// its series that query alone.
func StandalonePlan(r discover.StandaloneResource) ActivityPlanOut {
	out := ActivityPlanOut{Signals: []PlannedSignal{}, Queries: []ActivityQuery{}}
	for i, s := range discover.DefaultSignals(discover.Backend{Kind: r.Kind, ID: r.ID, ARN: r.ARN}) {
		id := fmt.Sprintf("%s_%d", s.Signal, i)
		below := s.Below
		out.Queries = append(out.Queries, ActivityQuery{ID: id, Namespace: s.Metric.Namespace, Name: s.Metric.Name, Dimensions: s.Metric.Dimensions, Stat: s.Metric.Stat})
		out.Signals = append(out.Signals, PlannedSignal{Signal: s.Signal, Below: &below, Applies: true, Series: []Series{{IDs: []string{id}, Scale: ScaleSame}}})
	}
	return out
}

// RegionSignals are a region's standalone resources' points (by resource ARN, then by query id: minutes from From and
// values), or why their metrics could not be read.
type RegionSignals struct {
	Account    string                             `json:"account"`
	Region     string                             `json:"region"`
	From       string                             `json:"from"`
	To         string                             `json:"to"`
	Points     map[string]map[string][][2]float64 `json:"points"`
	Unreadable map[string]string                  `json:"unreadable"`
	// With --ecs-uptime: each ECS service's task-minutes over the window (its LiveTaskCount), as the HTTP services' are
	// counted; none when CloudWatch has none.
	Tasks map[string]float64 `json:"tasks,omitempty"`
}

// Of is a resource's points as the activity reads them.
func (r RegionSignals) Of(arn string) ActivityPoints {
	return ActivityPoints{From: r.From, To: r.To, Points: r.Points[arn]}
}

// StandaloneOptions are StandaloneAll's inputs (RequestsOptions' for the standalone resources).
type StandaloneOptions struct {
	Regions      []discover.StandaloneRegion
	CloudWatchOf CloudWatchOf
	Cache        *cache.Cache
	Refresh      string
	Now          func() time.Time
	Progress     func(discover.ProgressEvent)
	Concurrency  int
	OnPlan       func(count int)
	OnStart      func(account, region string)
	Days         int
	// Tasks (--ecs-uptime): each ECS service's tasks are counted too (its LiveTaskCount), as an HTTP service's are.
	Tasks bool
}

// StandaloneAll reads the signals of every region with standalone resources, --parallel regions at a time, in the
// regions' order; the error is the context's.
func StandaloneAll(ctx context.Context, o StandaloneOptions) ([]RegionSignals, error) {
	toRead := []discover.StandaloneRegion{}
	for _, r := range o.Regions {
		if len(r.Resources) > 0 {
			toRead = append(toRead, r)
		}
	}
	if o.OnPlan != nil {
		o.OnPlan(len(toRead))
	}
	say := &events{progress: o.Progress, start: o.OnStart}
	return limit.Map(ctx, toRead, concurrencyOf(o.Concurrency), func(ctx context.Context, r discover.StandaloneRegion) (RegionSignals, error) {
		say.Start(r.Account, r.Region)
		return regionSignals(ctx, r, o, say)
	})
}

// regionSignals is one region's signals: kept, read, or unreadable with AWS's reason.
func regionSignals(ctx context.Context, region discover.StandaloneRegion, o StandaloneOptions, say *events) (RegionSignals, error) {
	path := fmt.Sprintf("signals/%s-%s.json", region.Account, region.Region)
	now := o.Now()
	days := o.Days
	if days < 1 {
		days = Days
	}
	done := func(from string, err string) {
		say.Progress(discover.ProgressEvent{Phase: PhaseStandalone, Account: region.Account, Region: region.Region, From: from, Workloads: len(region.Resources), Error: err})
	}
	if o.Refresh == "" {
		var kept RegionSignals
		ok, err := o.Cache.Get(path, cache.FreshRequests(now), &kept)
		if err != nil {
			return RegionSignals{}, err
		}
		// Kept only while it covers the days asked and every resource discovery found.
		if ok && signalsSpan(kept, days) && signalsCoverAll(region.Resources, kept) {
			done(discover.FromCache, "")
			return kept, nil
		}
	}
	read := RegionSignals{Account: region.Account, Region: region.Region, Points: map[string]map[string][][2]float64{}, Unreadable: map[string]string{}}
	cw, err := o.CloudWatchOf(ctx, region.Account, region.Region)
	failed := ""
	if err != nil {
		if ctx.Err() != nil {
			return RegionSignals{}, ctx.Err()
		}
		failed = awserr.Said(err)
	}
	for _, r := range region.Resources {
		if failed != "" {
			read.Unreadable[r.ARN] = failed
			continue
		}
		points, err := readActivity(ctx, cw, StandalonePlan(r).Queries, days, now)
		if ctx.Err() != nil {
			return RegionSignals{}, ctx.Err()
		}
		if read.From == "" {
			// Every read has the same window (the days before now's 5-minute boundary, 10 minutes behind).
			read.From, read.To = activityWindow(days, now)
		}
		if err != nil {
			if Unreadable(err) {
				read.Unreadable[r.ARN] = err.Error()
			} else {
				read.Unreadable[r.ARN] = awserr.Said(err)
			}
			continue
		}
		read.Points[r.ARN] = points.Points
		if o.Tasks && r.Kind == discover.KindECS {
			from, _ := time.Parse(time.RFC3339Nano, read.From)
			to, _ := time.Parse(time.RFC3339Nano, read.To)
			// As an HTTP service's: without them (an error, none published), it is priced at its count when discovered.
			if tasks, err := taskMinutes(ctx, cw, []string{r.ARN}, from, to); err == nil {
				if n, ok := tasks[r.ARN]; ok {
					if read.Tasks == nil {
						read.Tasks = map[string]float64{}
					}
					read.Tasks[r.ARN] = n
				}
			} else if ctx.Err() != nil {
				return RegionSignals{}, ctx.Err()
			}
		}
	}
	if read.From == "" {
		read.From, read.To = activityWindow(days, now)
	}
	if failed == "" && len(read.Unreadable) == 0 {
		if err := o.Cache.Put(path, read); err != nil {
			return RegionSignals{}, err
		}
	}
	done(discover.FromAWS, failed)
	return read, nil
}

// activityWindow is the activity's window: the days before now's 5-minute boundary, 10 minutes behind.
func activityWindow(days int, now time.Time) (from, to string) {
	period := activityPeriod.Milliseconds()
	toMs := (now.UnixMilli() - activityLag.Milliseconds()) / period * period
	return iso(time.UnixMilli(toMs - int64(days)*activityDay.Milliseconds())), iso(time.UnixMilli(toMs))
}

func signalsSpan(kept RegionSignals, days int) bool {
	from, err1 := time.Parse(time.RFC3339, kept.From)
	to, err2 := time.Parse(time.RFC3339, kept.To)
	return err1 == nil && err2 == nil && to.Sub(from) == time.Duration(days)*24*time.Hour
}

func signalsCoverAll(resources []discover.StandaloneResource, kept RegionSignals) bool {
	for _, r := range resources {
		_, points := kept.Points[r.ARN]
		_, unreadable := kept.Unreadable[r.ARN]
		if !points && !unreadable {
			return false
		}
	}
	return true
}
