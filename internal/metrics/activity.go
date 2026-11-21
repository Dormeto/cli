package metrics

// A standalone workload's activity over its last days (vision standalone workloads design §4, §6, §7): its idle
// signals' 5-minute points read from CloudWatch, as the router reads them (router lambda/src/lib/activity.mjs plan:
// the queries by backend kind, how series combine), and each signal's value a point.
//
// Ports manager packages/fleet/src/activity.ts (activityPlan, signalValues) and packages/discovery/src/activity-step.ts
// (activityPoints). The busy points the replay reads are internal/replay's (BusyMinutes).

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

const (
	activityPeriod  = 5 * time.Minute
	activityLag     = 10 * time.Minute // CloudWatch publishes a point minutes after its period (activity.mjs METRIC_LAG_MS)
	activityMaxDays = 14
	activityDay     = 24 * time.Hour
	resourceWord    = "${resource}"
)

// ActivityBackend is a workload's backend as the plan reads it: its kind and id; Companion a server's companion
// database.
type ActivityBackend struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Companion bool   `json:"companion,omitempty"`
}

// DimensionSet is a custom metric's dimensions, in the order they are given (a JSON object, its keys in order).
type DimensionSet []Dimension

// UnmarshalJSON reads an object, keeping its key order.
func (d *DimensionSet) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return fmt.Errorf("dimensions: an object is expected")
	}
	out := DimensionSet{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		var value string
		if err := dec.Decode(&value); err != nil {
			return err
		}
		out = append(out, Dimension{Name: key.(string), Value: value})
	}
	*d = out
	return nil
}

// MarshalJSON writes the object in order.
func (d DimensionSet) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, x := range d {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(x.Name)
		v, _ := json.Marshal(x.Value)
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// IdleMetric is a workload's custom idle metric: idle under Below. Its dimensions are one set (List false: the query
// metric) or a list of sets (List true: a query each, metric_<i>); a value's ${resource} is the workload's own id.
type IdleMetric struct {
	Namespace  string
	Name       string
	Dimensions []DimensionSet
	List       bool
	Statistic  string
	Below      float64
}

type idleMetricJSON struct {
	Namespace  string          `json:"namespace"`
	Name       string          `json:"name"`
	Dimensions json.RawMessage `json:"dimensions,omitempty"`
	Statistic  string          `json:"statistic"`
	Below      float64         `json:"below"`
}

// UnmarshalJSON reads the metric, its dimensions an object or a list of objects.
func (m *IdleMetric) UnmarshalJSON(b []byte) error {
	var raw idleMetricJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*m = IdleMetric{Namespace: raw.Namespace, Name: raw.Name, Statistic: raw.Statistic, Below: raw.Below}
	switch trimmed := bytes.TrimSpace(raw.Dimensions); {
	case len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")):
		m.Dimensions = []DimensionSet{{}}
	case trimmed[0] == '[':
		m.List = true
		return json.Unmarshal(trimmed, &m.Dimensions)
	default:
		var one DimensionSet
		if err := json.Unmarshal(trimmed, &one); err != nil {
			return err
		}
		m.Dimensions = []DimensionSet{one}
	}
	return nil
}

// MarshalJSON writes the metric as the API says it.
func (m IdleMetric) MarshalJSON() ([]byte, error) {
	var dims any = m.Dimensions
	if !m.List && len(m.Dimensions) == 1 {
		dims = m.Dimensions[0]
	}
	d, err := json.Marshal(dims)
	if err != nil {
		return nil, err
	}
	return json.Marshal(idleMetricJSON{m.Namespace, m.Name, d, m.Statistic, m.Below})
}

// ActivitySettings are a workload's resolved idle signals: a threshold, or nil for "off" (absent is off).
type ActivitySettings struct {
	IdleCPUPercent   *float64
	IdleNetworkBytes *float64
	IdleMetric       *IdleMetric
}

// UnmarshalJSON reads each signal's number or object, or "off".
func (s *ActivitySettings) UnmarshalJSON(b []byte) error {
	var raw struct {
		IdleCPUPercent   json.RawMessage `json:"idleCpuPercent"`
		IdleNetworkBytes json.RawMessage `json:"idleNetworkBytes"`
		IdleMetric       json.RawMessage `json:"idleMetric"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*s = ActivitySettings{}
	off := func(v json.RawMessage) bool {
		t := bytes.TrimSpace(v)
		return len(t) == 0 || bytes.Equal(t, []byte(`"off"`)) || bytes.Equal(t, []byte("null"))
	}
	if !off(raw.IdleCPUPercent) {
		if err := json.Unmarshal(raw.IdleCPUPercent, &s.IdleCPUPercent); err != nil {
			return fmt.Errorf("idleCpuPercent: %w", err)
		}
	}
	if !off(raw.IdleNetworkBytes) {
		if err := json.Unmarshal(raw.IdleNetworkBytes, &s.IdleNetworkBytes); err != nil {
			return fmt.Errorf("idleNetworkBytes: %w", err)
		}
	}
	if !off(raw.IdleMetric) {
		if err := json.Unmarshal(raw.IdleMetric, &s.IdleMetric); err != nil {
			return fmt.Errorf("idleMetric: %w", err)
		}
	}
	return nil
}

// ActivityQuery is one CloudWatch query of the plan.
type ActivityQuery struct {
	ID         string      `json:"id"`
	Namespace  string      `json:"namespace"`
	Name       string      `json:"name"`
	Dimensions []Dimension `json:"dimensions"`
	Stat       string      `json:"stat"`
}

// How a signal's queries make one value a point: each series the sum of its queries, scaled to the signal's unit; the
// largest series is the signal's value (the router's measured).
const (
	ScaleSame         = "same"
	ScaleEC2PerMinute = "ec2PerMinute" // an EC2 5-minute sum, a minute
	ScaleRDSPerMinute = "rdsPerMinute" // RDS bytes a second, a minute
)

// Series is a signal's series: the sum of its queries, scaled.
type Series struct {
	IDs   []string `json:"ids"`
	Scale string   `json:"scale"`
}

// PlannedSignal is a signal of the plan: cpu, network or metric, idle under Below (nil: off); one that does not apply
// says why.
type PlannedSignal struct {
	Signal  string   `json:"signal"`
	Below   *float64 `json:"below"`
	Applies bool     `json:"applies"`
	Reason  string   `json:"reason,omitempty"`
	Series  []Series `json:"series"`
}

// ActivityPlanOut is the plan: the signals and the queries they read.
type ActivityPlanOut struct {
	Signals []PlannedSignal `json:"signals"`
	Queries []ActivityQuery `json:"queries"`
}

func isDatabase(b ActivityBackend) bool { return b.Kind == "rds-instance" || b.Kind == "rds-cluster" }

func lastSegment(arn string) string { return arn[strings.LastIndex(arn, "/")+1:] }

func rdsDimension(b ActivityBackend) Dimension {
	if b.Kind == "rds-cluster" {
		return Dimension{Name: "DBClusterIdentifier", Value: b.ID}
	}
	return Dimension{Name: "DBInstanceIdentifier", Value: b.ID}
}

// ecsCluster is an ECS service's cluster from its ARN: service/<cluster>/<name> (service/<name> in the default cluster).
func ecsCluster(arn string) string {
	path := ""
	if _, after, ok := strings.Cut(arn, ":service/"); ok {
		path, _, _ = strings.Cut(after, ":service/")
	}
	if i := strings.Index(path, "/"); i >= 0 {
		return path[:i]
	}
	return "default"
}

func resourceIDOf(b ActivityBackend) string {
	if b.Kind == "ecs" {
		return lastSegment(b.ID)
	}
	return b.ID
}

func cpuOf(b ActivityBackend) (string, []Dimension, bool) {
	switch {
	case b.Kind == "instance":
		return "AWS/EC2", []Dimension{{Name: "InstanceId", Value: b.ID}}, true
	case b.Kind == "asg":
		return "AWS/EC2", []Dimension{{Name: "AutoScalingGroupName", Value: b.ID}}, true
	case b.Kind == "ecs":
		return "AWS/ECS", []Dimension{{Name: "ClusterName", Value: ecsCluster(b.ID)}, {Name: "ServiceName", Value: lastSegment(b.ID)}}, true
	case isDatabase(b):
		return "AWS/RDS", []Dimension{rdsDimension(b)}, true
	}
	return "", nil, false
}

type network struct {
	namespace  string
	names      [2]string
	stat       string
	dimensions []Dimension
	scale      string
}

func networkOf(b ActivityBackend) (network, bool) {
	switch {
	case b.Kind == "instance" || b.Kind == "asg":
		_, dims, _ := cpuOf(b)
		return network{"AWS/EC2", [2]string{"NetworkIn", "NetworkOut"}, "Sum", dims, ScaleEC2PerMinute}, true
	case isDatabase(b):
		return network{"AWS/RDS", [2]string{"NetworkReceiveThroughput", "NetworkTransmitThroughput"}, "Average", []Dimension{rdsDimension(b)}, ScaleRDSPerMinute}, true
	}
	return network{}, false
}

// ActivityPlan is the signals of a workload's resolved settings and their queries (the router's plan): CPU and network
// are its own backends' (its database's when its resource is the database), the metric's ${resource} its own id.
func ActivityPlan(backends []ActivityBackend, settings ActivitySettings) ActivityPlanOut {
	var own []ActivityBackend
	for _, b := range backends {
		if !isDatabase(b) {
			own = append(own, b)
		}
	}
	if len(own) == 0 {
		for _, b := range backends {
			if isDatabase(b) {
				own = append(own, b)
			}
		}
	}
	out := ActivityPlanOut{Signals: []PlannedSignal{}, Queries: []ActivityQuery{}}
	off := func(signal string, below *float64, reason string) PlannedSignal {
		return PlannedSignal{Signal: signal, Below: below, Reason: reason, Series: []Series{}}
	}

	switch cpu := settings.IdleCPUPercent; {
	case cpu == nil:
		out.Signals = append(out.Signals, off("cpu", nil, "off"))
	case len(own) == 0:
		out.Signals = append(out.Signals, off("cpu", cpu, "no backend to read"))
	default:
		s := PlannedSignal{Signal: "cpu", Below: cpu, Applies: true}
		for i, b := range own {
			namespace, dims, _ := cpuOf(b)
			id := fmt.Sprintf("cpu_%d", i)
			out.Queries = append(out.Queries, ActivityQuery{id, namespace, "CPUUtilization", dims, "Maximum"})
			s.Series = append(s.Series, Series{[]string{id}, ScaleSame})
		}
		out.Signals = append(out.Signals, s)
	}

	if threshold := settings.IdleNetworkBytes; threshold == nil {
		out.Signals = append(out.Signals, off("network", nil, "off"))
	} else {
		s := PlannedSignal{Signal: "network", Below: threshold, Applies: true}
		for i, b := range own {
			m, ok := networkOf(b)
			if !ok {
				continue
			}
			ids := make([]string, 2)
			for k, name := range m.names {
				ids[k] = fmt.Sprintf("net_%d_%s", i, map[int]string{0: "in", 1: "out"}[k])
				out.Queries = append(out.Queries, ActivityQuery{ids[k], m.namespace, name, m.dimensions, m.stat})
			}
			s.Series = append(s.Series, Series{ids, m.scale})
		}
		switch {
		case len(s.Series) > 0:
			out.Signals = append(out.Signals, s)
		case len(own) > 0:
			out.Signals = append(out.Signals, off("network", threshold, "an ECS service has no network metric without Container Insights"))
		default:
			out.Signals = append(out.Signals, off("network", threshold, "no backend to read"))
		}
	}

	if metric := settings.IdleMetric; metric == nil {
		out.Signals = append(out.Signals, off("metric", nil, "off"))
	} else {
		below := metric.Below
		resource, known := "", len(own) > 0
		if known {
			resource = resourceIDOf(own[0])
		}
		needs := slices.ContainsFunc(metric.Dimensions, func(d DimensionSet) bool {
			return slices.ContainsFunc(d, func(x Dimension) bool { return strings.Contains(x.Value, resourceWord) })
		})
		if !known && needs {
			out.Signals = append(out.Signals, off("metric", &below, "no backend to replace "+resourceWord))
		} else {
			s := PlannedSignal{Signal: "metric", Below: &below, Applies: true}
			for i, d := range metric.Dimensions {
				id := "metric"
				if metric.List {
					id = fmt.Sprintf("metric_%d", i)
				}
				dims := make([]Dimension, len(d))
				for k, x := range d {
					dims[k] = Dimension{Name: x.Name, Value: strings.ReplaceAll(x.Value, resourceWord, resource)}
				}
				out.Queries = append(out.Queries, ActivityQuery{id, metric.Namespace, metric.Name, dims, metric.Statistic})
				s.Series = append(s.Series, Series{[]string{id}, ScaleSame})
			}
			if s.Series == nil {
				s.Series = []Series{}
			}
			out.Signals = append(out.Signals, s)
		}
	}
	return out
}

// ActivityPoints are the points read: by query id, each point's minute from From and its value.
type ActivityPoints struct {
	From   string                  `json:"from"`
	To     string                  `json:"to"`
	Points map[string][][2]float64 `json:"points"`
}

// UnreadableError is a metric CloudWatch could not read: an error, never an idle day.
type UnreadableError struct{ why string }

func (e *UnreadableError) Error() string {
	return "CloudWatch could not read the metrics (" + e.why + ")"
}

// Unreadable tells whether err is a metric CloudWatch could not read.
func Unreadable(err error) bool {
	var u *UnreadableError
	return errors.As(err, &u)
}

// ReadActivity reads the queries' 5-minute points over the days before now: the window ends at now's 5-minute
// boundary 10 minutes behind and starts days before; every page read.
func ReadActivity(ctx context.Context, cw CloudWatchPort, queries []ActivityQuery, days int, now time.Time) (ActivityPoints, error) {
	if len(queries) == 0 || len(queries) > 30 || days < 1 || days > activityMaxDays {
		return ActivityPoints{}, errors.New("the activity of a workload needs its queries (1 to 30) and days (1 to 14)")
	}
	return readActivity(ctx, cw, queries, days, now)
}

// readActivity is ReadActivity without the API's limits (the estimate reads up to its --days, 15).
func readActivity(ctx context.Context, cw CloudWatchPort, queries []ActivityQuery, days int, now time.Time) (ActivityPoints, error) {
	periodMs := activityPeriod.Milliseconds()
	toMs := int64(math.Floor(float64(now.UnixMilli()-activityLag.Milliseconds())/float64(periodMs))) * periodMs
	fromMs := toMs - int64(days)*activityDay.Milliseconds()
	from, to := time.UnixMilli(fromMs), time.UnixMilli(toMs)
	points := map[string][][2]float64{}
	asked := make([]MetricDataQuery, len(queries))
	for i, q := range queries {
		points[q.ID] = [][2]float64{}
		asked[i] = MetricDataQuery{ID: q.ID, ReturnData: true, MetricStat: MetricStat{
			Metric: Metric{Namespace: q.Namespace, MetricName: q.Name, Dimensions: q.Dimensions}, Period: int(periodMs / 1000), Stat: q.Stat,
		}}
	}
	token := ""
	for {
		page, err := cw.GetMetricData(ctx, GetMetricDataInput{StartTime: from, EndTime: to, MetricDataQueries: asked, ScanBy: ScanByAscending, NextToken: token})
		if err != nil {
			return ActivityPoints{}, err
		}
		for _, r := range page.MetricDataResults {
			if why, bad := unreadableWhy(r); bad {
				return ActivityPoints{}, &UnreadableError{why}
			}
			list, ok := points[r.ID]
			if !ok {
				continue
			}
			for i, t := range r.Timestamps {
				if i >= len(r.Values) || math.IsNaN(r.Values[i]) || math.IsInf(r.Values[i], 0) {
					continue
				}
				minute := math.Floor(float64(t.UnixMilli()-fromMs)/60_000 + 0.5)
				if minute >= 0 && minute < float64(days*1440) {
					list = append(list, [2]float64{minute, r.Values[i]})
				}
			}
			points[r.ID] = list
		}
		if token = page.NextToken; token == "" {
			break
		}
	}
	for _, list := range points {
		slices.SortStableFunc(list, func(a, b [2]float64) int { return cmp.Compare(a[0], b[0]) })
	}
	return ActivityPoints{From: iso(from), To: iso(to), Points: points}, nil
}

// SignalValue is an applying signal's value at each 5-minute point from From (nil: missing).
type SignalValue struct {
	Signal string     `json:"signal"`
	Values []*float64 `json:"values"`
}

func scaled(scale string, sum float64) float64 {
	switch scale {
	case ScaleEC2PerMinute:
		return sum / periodMinutes
	case ScaleRDSPerMinute:
		return sum * 60
	}
	return sum
}

// SignalValues are each applying signal's value at each 5-minute point, from From to To: each series the sum of its
// queries, scaled; the largest series; nil when one of its queries has no point.
func SignalValues(signals []PlannedSignal, read ActivityPoints) []SignalValue {
	fromAt, err1 := time.Parse(time.RFC3339Nano, read.From)
	toAt, err2 := time.Parse(time.RFC3339Nano, read.To)
	count := 0
	if err1 == nil && err2 == nil {
		count = int(math.Floor(float64(toAt.Sub(fromAt).Milliseconds())/float64(activityPeriod.Milliseconds()) + 0.5))
	}
	byID := map[string]map[int]float64{}
	for id, list := range read.Points {
		m := make(map[int]float64, len(list))
		for _, p := range list {
			m[int(math.Floor(p[0]/periodMinutes+0.5))] = p[1]
		}
		byID[id] = m
	}
	out := []SignalValue{}
	for _, s := range signals {
		if !s.Applies {
			continue
		}
		values := make([]*float64, max(count, 0))
		for p := range values {
			var worst *float64
			missing := false
			for _, series := range s.Series {
				sum := 0.0
				for _, id := range series.IDs {
					v, ok := byID[id][p]
					if !ok {
						missing = true
						break
					}
					sum += v
				}
				if missing {
					break
				}
				v := scaled(series.Scale, sum)
				if worst == nil || v > *worst {
					worst = &v
				}
			}
			if !missing {
				values[p] = worst
			}
		}
		out = append(out, SignalValue{Signal: s.Signal, Values: values})
	}
	return out
}
