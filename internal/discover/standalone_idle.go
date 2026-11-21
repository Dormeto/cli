package discover

// The idle share of standalone candidates over the last 7 days (vision standalone workloads design §4, §6): the
// default signals of each one's own resource, read from CloudWatch in the router's 5-minute points. An hour is idle
// when every signal is under its threshold at each of its 12 points; a missing point is not idle (the router's rule). A
// candidate with no point at all in the week (a resource stopped all week, a service at 0) has no share: no data,
// neither idle nor busy. A read that fails gives no share: its candidates are said, never idle.
//
// Ports manager packages/discovery/src/standalone-idle.ts (defaultSignals, standaloneIdle); the signals are the
// router's (lambda/src/lib/activity.mjs, its built-ins settings-levels.mjs builtInSettingsOf).

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/awserr"
)

const (
	standaloneHours = 168
	signalPeriod    = 5 * time.Minute // activity.mjs PERIOD_MS
	pointsAnHour    = int(time.Hour / signalPeriod)
	// CloudWatch publishes a point minutes after its period (activity.mjs METRIC_LAG_MS): the week ends at the last
	// hour 10 minutes behind, so that its last hour is complete.
	metricLag = 10 * time.Minute
	// GetMetricData's most queries a call.
	metricBatch = 500
	// The built-ins of a standalone workload with no companion (settings-levels.mjs STANDALONE_BUILT_IN; a workload
	// whose resource is a database: its CPU off, its connections under 1).
	builtInCPUPercent  = 5
	builtInConnections = 1
)

// SignalMetric is the CloudWatch metric of a signal.
type SignalMetric struct {
	Namespace  string      `json:"namespace"`
	Name       string      `json:"name"`
	Dimensions []Dimension `json:"dimensions"`
	Stat       string      `json:"stat"`
}

// DefaultSignal is a built-in idle signal of a standalone resource: its metric is idle under Below.
type DefaultSignal struct {
	Signal string       `json:"signal"` // cpu or metric
	Below  float64      `json:"below"`
	Metric SignalMetric `json:"metric"`
}

// DefaultSignals are a candidate's default signals, by its own resource's kind (a candidate lists no companion yet):
// CPU under 5 % for an instance, a group and an ECS service; a database's connections under 1.
func DefaultSignals(backend Backend) []DefaultSignal {
	cpu := func(namespace string, dimensions ...Dimension) []DefaultSignal {
		return []DefaultSignal{{Signal: "cpu", Below: builtInCPUPercent, Metric: SignalMetric{namespace, "CPUUtilization", dimensions, "Maximum"}}}
	}
	switch backend.Kind {
	case KindInstance:
		return cpu("AWS/EC2", Dimension{"InstanceId", backend.ID})
	case KindASG:
		return cpu("AWS/EC2", Dimension{"AutoScalingGroupName", backend.ID})
	case KindECS:
		// service/<cluster>/<name>, or service/<name> in the default cluster (an ARN of the old format).
		path := backend.ID
		if _, after, ok := strings.Cut(backend.ARN, ":service/"); ok {
			path, _, _ = strings.Cut(after, ":service/")
		}
		cluster, name := "default", path
		if strings.Contains(path, "/") {
			cluster, name = path[:strings.Index(path, "/")], path[strings.LastIndex(path, "/")+1:]
		}
		return cpu("AWS/ECS", Dimension{"ClusterName", cluster}, Dimension{"ServiceName", name})
	}
	name := "DBInstanceIdentifier"
	if backend.Kind == KindRDSCluster {
		name = "DBClusterIdentifier"
	}
	return []DefaultSignal{{Signal: "metric", Below: builtInConnections, Metric: SignalMetric{"AWS/RDS", "DatabaseConnections", []Dimension{{name, backend.ID}}, "Maximum"}}}
}

// FailedRead is a batch of candidates CloudWatch failed for, with the error as one line ("<name>: <message>").
type FailedRead struct {
	IDs   []string `json:"ids"`
	Error string   `json:"error"`
}

// IdleShares are the candidates read, by id, with their idle hours of the week (nil: no point at all), and the
// batches CloudWatch failed for.
type IdleShares struct {
	Shares map[string]*int `json:"shares"`
	Failed []FailedRead    `json:"failed"`
}

// unreadableMetrics is a result CloudWatch could not read: a failed read, never a week without a point.
func unreadableMetrics(r MetricDataResult) error {
	if r.StatusCode == "" || r.StatusCode == "Complete" || r.StatusCode == "PartialData" {
		return nil
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
	text := r.StatusCode
	if len(why) > 0 {
		text += ": " + strings.Join(why, "; ")
	}
	return errors.New("CloudWatch could not read the metrics (" + text + ")")
}

// StandaloneIdle reads the idle share of the candidates over the week before now: one GetMetricData call (and its
// pages) a batch of at most 500 queries, a candidate's signals in one batch.
func StandaloneIdle(ctx context.Context, cw CloudWatchPort, candidates []StandaloneRule, now time.Time) IdleShares {
	nowMs := now.UnixMilli()
	hourMs := time.Hour.Milliseconds()
	periodMs := signalPeriod.Milliseconds()
	end := int64(math.Floor(float64(nowMs-metricLag.Milliseconds())/float64(hourMs))) * hourMs
	start := end - standaloneHours*hourMs
	out := IdleShares{Shares: map[string]*int{}, Failed: []FailedRead{}}
	// A candidate's signals stay in one batch: its share needs all of them.
	var batches [][]StandaloneRule
	size := metricBatch
	for _, c := range candidates {
		n := len(DefaultSignals(c.Backend))
		if size+n > metricBatch {
			batches = append(batches, nil)
			size = 0
		}
		batches[len(batches)-1] = append(batches[len(batches)-1], c)
		size += n
	}
	type query struct {
		id string
		s  DefaultSignal
	}
	for _, batch := range batches {
		var queries []query
		var asked []MetricDataQuery
		for _, c := range batch {
			for _, s := range DefaultSignals(c.Backend) {
				asked = append(asked, MetricDataQuery{
					ID: "s" + strconv.Itoa(len(queries)), ReturnData: true,
					MetricStat: MetricStat{Metric: Metric{Namespace: s.Metric.Namespace, MetricName: s.Metric.Name, Dimensions: s.Metric.Dimensions}, Period: int(periodMs / 1000), Stat: s.Metric.Stat},
				})
				queries = append(queries, query{c.ID, s})
			}
		}
		points := map[string]map[int64]float64{}
		err := func() error {
			token := ""
			for {
				page, err := cw.GetMetricData(ctx, GetMetricDataInput{StartTime: time.UnixMilli(start), EndTime: time.UnixMilli(end), ScanBy: "TimestampAscending", NextToken: token, MetricDataQueries: asked})
				if err != nil {
					return err
				}
				// A query's points may come over several pages.
				for _, r := range page.MetricDataResults {
					if err := unreadableMetrics(r); err != nil {
						return err
					}
					series := points[r.ID]
					if series == nil {
						series = map[int64]float64{}
					}
					for k, t := range r.Timestamps {
						if k < len(r.Values) && !math.IsNaN(r.Values[k]) && !math.IsInf(r.Values[k], 0) {
							series[t.UnixMilli()] = r.Values[k]
						}
					}
					points[r.ID] = series
				}
				if token = page.NextToken; token == "" {
					return nil
				}
			}
		}()
		if err != nil {
			ids := make([]string, len(batch))
			for i, c := range batch {
				ids[i] = c.ID
			}
			out.Failed = append(out.Failed, FailedRead{IDs: ids, Error: awserr.Said(err)})
			continue
		}
		type signal struct {
			s      DefaultSignal
			series map[int64]float64
		}
		var order []string
		byCandidate := map[string][]signal{}
		for i, q := range queries {
			if _, ok := byCandidate[q.id]; !ok {
				order = append(order, q.id)
			}
			byCandidate[q.id] = append(byCandidate[q.id], signal{q.s, points["s"+strconv.Itoa(i)]})
		}
		for _, id := range order {
			signals := byCandidate[id]
			empty := true
			for _, s := range signals {
				if len(s.series) > 0 {
					empty = false
				}
			}
			if empty {
				out.Shares[id] = nil
				continue
			}
			idle := 0
			for h := int64(0); h < standaloneHours; h++ {
				hour := start + h*hourMs
				under := true
				for _, s := range signals {
					for p := int64(0); p < int64(pointsAnHour) && under; p++ {
						v, ok := s.series[hour+p*periodMs]
						under = ok && v < s.s.Below
					}
				}
				if under {
					idle++
				}
			}
			out.Shares[id] = &idle
		}
	}
	return out
}
