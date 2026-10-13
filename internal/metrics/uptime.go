package metrics

// The measured uptime of EC2 instances without an Auto Scaling group
// (--ec2-uptime), of ECS services (--ecs-uptime; --uptime both) and of
// companion databases: when each ran over its region's requests period, from
// its CPUUtilization. EC2 publishes an instance's every 5 minutes while it runs
// (detailed monitoring adds up to the same 5 minutes), ECS a service's every
// minute while it has a task, RDS a database's every minute while it is
// available, and nothing while stopped or at 0 tasks. Twenty per GetMetricData
// call (4,032 values each over 14 days, under the 100,800 a call returns),
// every page. A metric CloudWatch could not read is unreadable, never a stop.
// What Dormeto manages is not measured: its uptime is already its naps. Kept
// per account and region the same UTC day.
//
// Ports manager packages/estimate/src/uptime.ts.

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/cache"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/limit"
)

const (
	periodMinutes  = 5
	uptimePerCall  = 20
	rdsInstancePre = "rds-instance:"
	rdsClusterPre  = "rds-cluster:"
)

// RegionUptime is a region's measured uptime: by instance id, service ARN or
// database key, when it ran ([start, end) in minutes from From, sorted and
// disjoint), or why it could not be read. JSON names as Node's.
type RegionUptime struct {
	Account    string              `json:"account"`
	Region     string              `json:"region"`
	From       string              `json:"from"`
	To         string              `json:"to"`
	Running    map[string][][2]int `json:"running"`
	Unreadable map[string]string   `json:"unreadable"`
}

// UptimeKinds says what each option measures: EC2 instances without an Auto
// Scaling group (EC2), ECS services (ECS), never what Dormeto manages; and the
// companion databases a nap could stop (RDS).
type UptimeKinds struct{ EC2, ECS, RDS bool }

// DefaultKinds is what Node measures when no kinds are given: the instances.
var DefaultKinds = UptimeKinds{EC2: true}

// Measured says whether the option measures the workload's uptime.
func Measured(w discover.Workload, kinds UptimeKinds) bool {
	return !w.Managed && ((kinds.EC2 && w.Backend.Kind == discover.KindInstance) || (kinds.ECS && w.Backend.Kind == discover.KindECS))
}

// DatabaseKey is a companion database's key in the uptime: its kind and identifier.
func DatabaseKey(d discover.Database) string { return d.Kind + ":" + d.ID }

// metricOf is the metric of an instance by its id; of an ECS service
// (arn:aws:ecs:...:service/<cluster>/<service>) by its cluster and name; of a
// companion database (rds-instance:<id>, rds-cluster:<id>) by its identifier.
func metricOf(id string) Metric {
	switch {
	case strings.HasPrefix(id, rdsInstancePre):
		return Metric{Namespace: "AWS/RDS", MetricName: "CPUUtilization", Dimensions: []Dimension{{Name: "DBInstanceIdentifier", Value: strings.TrimPrefix(id, rdsInstancePre)}}}
	case strings.HasPrefix(id, rdsClusterPre):
		return Metric{Namespace: "AWS/RDS", MetricName: "CPUUtilization", Dimensions: []Dimension{{Name: "DBClusterIdentifier", Value: strings.TrimPrefix(id, rdsClusterPre)}}}
	}
	if parts := strings.Split(id, ":"); strings.HasPrefix(id, "arn:") && len(parts) > 2 && parts[2] == "ecs" {
		return Metric{Namespace: "AWS/ECS", MetricName: "CPUUtilization", Dimensions: []Dimension{
			{Name: "ClusterName", Value: discover.ClusterOf(id)},
			{Name: "ServiceName", Value: id[strings.LastIndex(id, "/")+1:]},
		}}
	}
	return Metric{Namespace: "AWS/EC2", MetricName: "CPUUtilization", Dimensions: []Dimension{{Name: "InstanceId", Value: id}}}
}

// UptimeRead is what Uptime reads of a list of ids.
type UptimeRead struct {
	Running    map[string][][2]int
	Unreadable map[string]string
}

// Uptime reads the running periods of the ids over [from, to] (ISO strings,
// the region's requests period): each datapoint is running its 5 minutes,
// consecutive ones joined. An error is a call that failed, whole.
func Uptime(ctx context.Context, cw CloudWatchPort, ids []string, from, to string) (UptimeRead, error) {
	startT, err := time.Parse(time.RFC3339Nano, from)
	if err != nil {
		return UptimeRead{}, fmt.Errorf("uptime period from %q: %w", from, err)
	}
	endT, err := time.Parse(time.RFC3339Nano, to)
	if err != nil {
		return UptimeRead{}, fmt.Errorf("uptime period to %q: %w", to, err)
	}
	end := roundMinutes(endT.Sub(startT))
	minutes := make(map[string][]int, len(ids))
	for _, id := range ids {
		minutes[id] = []int{}
	}
	unreadable := map[string]string{}
	for first := 0; first < len(ids); first += uptimePerCall {
		batch := ids[first:min(first+uptimePerCall, len(ids))]
		queries := make([]MetricDataQuery, len(batch))
		for k, id := range batch {
			queries[k] = MetricDataQuery{ID: "u" + strconv.Itoa(k), ReturnData: true, MetricStat: MetricStat{Metric: metricOf(id), Period: periodMinutes * 60, Stat: "SampleCount"}}
		}
		err := pages(ctx, cw, startT, endT, queries, func(result MetricDataResult) {
			k, ok := queryIndex(result.ID, len(batch))
			if !ok {
				return
			}
			id := batch[k]
			if why, bad := unreadableWhy(result); bad {
				unreadable[id] = "CloudWatch could not read the uptime (" + why + ")"
				return
			}
			for j, timestamp := range result.Timestamps {
				if j < len(result.Values) && result.Values[j] > 0 {
					minutes[id] = append(minutes[id], roundMinutes(timestamp.Sub(startT)))
				}
			}
		})
		if err != nil {
			return UptimeRead{}, err
		}
	}
	running := map[string][][2]int{}
	for id, starts := range minutes {
		if _, bad := unreadable[id]; bad {
			continue
		}
		slices.Sort(starts)
		periods := [][2]int{}
		for _, m := range slices.Compact(starts) {
			a, b := max(0, m), min(end, m+periodMinutes)
			if a >= b {
				continue
			}
			if n := len(periods); n > 0 && periods[n-1][1] >= a {
				periods[n-1][1] = max(periods[n-1][1], b)
			} else {
				periods = append(periods, [2]int{a, b})
			}
		}
		running[id] = periods
	}
	return UptimeRead{Running: running, Unreadable: unreadable}, nil
}

// UptimeOptions are the inputs of UptimeAll.
type UptimeOptions struct {
	Regions []discover.RegionDiscovery
	// Requests are the regions' requests: each region is measured over its period.
	Requests     []RegionRequests
	CloudWatchOf CloudWatchOf
	Cache        *cache.Cache
	// Refresh is "discovery" or "metrics" (read again) or "".
	Refresh  string
	Now      func() time.Time
	Progress func(discover.ProgressEvent)
	// Kinds is what is measured; nil: DefaultKinds (Node's default argument).
	Kinds *UptimeKinds
	// Concurrency is the regions read at once (--parallel; 5 when < 1).
	Concurrency int
	// OnPlan says how many regions are measured; OnStart when each starts.
	OnPlan  func(count int)
	OnStart func(account, region string)
}

type uptimeJob struct {
	region discover.RegionDiscovery
	ids    []string
	period RegionRequests
}

// UptimeAll measures the regions with something to measure, over their
// requests period, --parallel regions at a time, in the regions' order. A
// region it cannot read at all is a RegionUptime whose ids are unreadable with
// AWS's reason; the error is the context's.
func UptimeAll(ctx context.Context, o UptimeOptions) ([]RegionUptime, error) {
	kinds := DefaultKinds
	if o.Kinds != nil {
		kinds = *o.Kinds
	}
	jobs := []uptimeJob{}
	for _, region := range o.Regions {
		var ids []string
		add := func(id string) {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		for _, w := range region.Workloads {
			if Measured(w, kinds) {
				add(w.Backend.ID)
			}
		}
		if kinds.RDS {
			for _, d := range region.Databases {
				if d.NotStoppable == "" {
					add(DatabaseKey(d))
				}
			}
		}
		i := slices.IndexFunc(o.Requests, func(r RegionRequests) bool { return r.Account == region.Account && r.Region == region.Region })
		if len(ids) > 0 && i >= 0 {
			jobs = append(jobs, uptimeJob{region, ids, o.Requests[i]})
		}
	}
	if o.OnPlan != nil {
		o.OnPlan(len(jobs))
	}
	say := &events{progress: o.Progress, start: o.OnStart}
	return limit.Map(ctx, jobs, concurrencyOf(o.Concurrency), func(ctx context.Context, j uptimeJob) (RegionUptime, error) {
		say.Start(j.region.Account, j.region.Region)
		return regionUptime(ctx, j, o, say)
	})
}

// regionUptime is one region's uptime: kept, read, or unreadable with AWS's reason.
func regionUptime(ctx context.Context, j uptimeJob, o UptimeOptions, say *events) (RegionUptime, error) {
	account, region := j.region.Account, j.region.Region
	path := fmt.Sprintf("uptime/%s-%s.json", account, region)
	if o.Refresh == "" {
		var kept RegionUptime
		ok, err := o.Cache.Get(path, cache.FreshRequests(o.Now()), &kept)
		if err != nil {
			return RegionUptime{}, err
		}
		// Kept while it measures the same period and every instance.
		if ok && kept.From == j.period.From && keptAll(j.ids, kept) {
			say.Progress(discover.ProgressEvent{Phase: PhaseUptime, Account: account, Region: region, From: discover.FromCache, Workloads: len(j.ids)})
			return kept, nil
		}
	}
	read, err := func() (RegionUptime, error) {
		cw, err := o.CloudWatchOf(ctx, account, region)
		if err != nil {
			return RegionUptime{}, err
		}
		r, err := Uptime(ctx, cw, j.ids, j.period.From, j.period.To)
		if err != nil {
			return RegionUptime{}, err
		}
		read := RegionUptime{Account: account, Region: region, From: j.period.From, To: j.period.To, Running: r.Running, Unreadable: r.Unreadable}
		return read, o.Cache.Put(path, read)
	}()
	if err != nil {
		if ctx.Err() != nil {
			return RegionUptime{}, ctx.Err()
		}
		// Not read at all (a denial, throttling): its instances and services
		// unreadable with AWS's reason, nothing kept, the run goes on.
		why := awserr.Said(err)
		read = RegionUptime{Account: account, Region: region, From: j.period.From, To: j.period.To, Running: map[string][][2]int{}, Unreadable: map[string]string{}}
		for _, id := range j.ids {
			read.Unreadable[id] = why
		}
		say.Progress(discover.ProgressEvent{Phase: PhaseUptime, Account: account, Region: region, From: discover.FromAWS, Workloads: len(j.ids), Error: why})
		return read, nil
	}
	say.Progress(discover.ProgressEvent{Phase: PhaseUptime, Account: account, Region: region, From: discover.FromAWS, Workloads: len(j.ids)})
	return read, nil
}

func keptAll(ids []string, kept RegionUptime) bool {
	for _, id := range ids {
		_, running := kept.Running[id]
		_, unreadable := kept.Unreadable[id]
		if !running && !unreadable {
			return false
		}
	}
	return true
}
