package metrics

// The CloudWatch port: GetMetricData in the AWS API's own shapes (PascalCase
// JSON names, as the Node port's recorded entries). One call is one page; the
// callers follow NextToken. Ports manager packages/estimate/src/requests.ts
// (CloudWatchPort = discovery's ScanPorts['cloudwatch']).

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Dormeto/cli/internal/discover"
)

// The query shapes are the scan's (discovery reads the same GetMetricData).
type (
	Dimension       = discover.Dimension
	Metric          = discover.Metric
	MetricStat      = discover.MetricStat
	MetricDataQuery = discover.MetricDataQuery
)

// ScanByAscending asks the oldest values first (Node passes it on every call).
const ScanByAscending = "TimestampAscending"

// GetMetricDataInput is a GetMetricData call. Its times are written as Node's
// toISOString does (2026-10-12T08:00:00.000Z).
type GetMetricDataInput struct {
	StartTime         time.Time
	EndTime           time.Time
	MetricDataQueries []MetricDataQuery
	ScanBy            string
	NextToken         string
}

const isoMillis = "2006-01-02T15:04:05.000Z"

// MarshalJSON writes the input with its times as ISO strings.
func (in GetMetricDataInput) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		StartTime         string            `json:"StartTime"`
		EndTime           string            `json:"EndTime"`
		MetricDataQueries []MetricDataQuery `json:"MetricDataQueries"`
		ScanBy            string            `json:"ScanBy,omitempty"`
		NextToken         string            `json:"NextToken,omitempty"`
	}{in.StartTime.UTC().Format(isoMillis), in.EndTime.UTC().Format(isoMillis), in.MetricDataQueries, in.ScanBy, in.NextToken})
}

// Message is a message of a result (why CloudWatch could not read a metric).
type Message struct {
	Code  string `json:"Code,omitempty"`
	Value string `json:"Value,omitempty"`
}

// MetricDataResult is one query's values: Timestamps and Values side by side.
type MetricDataResult struct {
	ID         string      `json:"Id,omitempty"`
	Timestamps []time.Time `json:"Timestamps,omitempty"`
	Values     []float64   `json:"Values,omitempty"`
	StatusCode string      `json:"StatusCode,omitempty"`
	Messages   []Message   `json:"Messages,omitempty"`
}

// GetMetricDataOutput is a page of results; NextToken says there is another.
type GetMetricDataOutput struct {
	MetricDataResults []MetricDataResult `json:"MetricDataResults,omitempty"`
	NextToken         string             `json:"NextToken,omitempty"`
}

// CloudWatchPort is the metrics API.
type CloudWatchPort interface {
	GetMetricData(ctx context.Context, in GetMetricDataInput) (GetMetricDataOutput, error)
}

// CloudWatchOf gives an account's CloudWatch in a region (Node's cloudwatchOf).
type CloudWatchOf func(ctx context.Context, account, region string) (CloudWatchPort, error)
