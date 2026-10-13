package aws

// CloudWatch: the one adapter over the SDK's GetMetricData, seen through the
// two ports that read it: the scan's hourly requests per target group for their
// idle hours (discover.CloudWatchPort) and the estimate's requests per minute
// and uptime (metrics.CloudWatchPort). Both ports are the API's own shapes, one
// page a call (the callers follow NextToken); only the Go types differ, so each
// view converts between its types and the SDK's and shares the call. Ports
// manager packages/discovery/src/aws.ts scanPorts's cloudwatch.

import (
	"context"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/metrics"
)

type cloudWatchAPI interface {
	GetMetricData(context.Context, *cloudwatch.GetMetricDataInput, ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

// cloudWatchPort is the scan's view of the adapter (discover.CloudWatchPort).
type cloudWatchPort struct{ client cloudWatchAPI }

// metricsPort is the estimate's view of the same adapter (metrics.CloudWatchPort).
type metricsPort struct{ cloudWatchPort }

// sdkQueries are the port's queries as the SDK's.
func sdkQueries(queries []discover.MetricDataQuery) []cwtypes.MetricDataQuery {
	var out []cwtypes.MetricDataQuery
	for _, q := range queries {
		metric := &cwtypes.Metric{Namespace: awssdk.String(q.MetricStat.Metric.Namespace), MetricName: awssdk.String(q.MetricStat.Metric.MetricName)}
		for _, d := range q.MetricStat.Metric.Dimensions {
			metric.Dimensions = append(metric.Dimensions, cwtypes.Dimension{Name: awssdk.String(d.Name), Value: awssdk.String(d.Value)})
		}
		out = append(out, cwtypes.MetricDataQuery{
			Id: awssdk.String(q.ID), ReturnData: awssdk.Bool(q.ReturnData),
			MetricStat: &cwtypes.MetricStat{Metric: metric, Period: awssdk.Int32(int32(q.MetricStat.Period)), Stat: awssdk.String(q.MetricStat.Stat)},
		})
	}
	return out
}

// get is the one GetMetricData call, one page.
func (p cloudWatchPort) get(ctx context.Context, start, end time.Time, queries []discover.MetricDataQuery, scanBy, token string) (*cloudwatch.GetMetricDataOutput, error) {
	ask := &cloudwatch.GetMetricDataInput{StartTime: &start, EndTime: &end, NextToken: optional(token), MetricDataQueries: sdkQueries(queries)}
	if scanBy != "" {
		ask.ScanBy = cwtypes.ScanBy(scanBy)
	}
	return p.client.GetMetricData(ctx, ask)
}

func (p cloudWatchPort) GetMetricData(ctx context.Context, in discover.GetMetricDataInput) (discover.GetMetricDataOutput, error) {
	out, err := p.get(ctx, in.StartTime, in.EndTime, in.MetricDataQueries, "", in.NextToken)
	if err != nil {
		return discover.GetMetricDataOutput{}, err
	}
	res := discover.GetMetricDataOutput{NextToken: awssdk.ToString(out.NextToken)}
	for _, r := range out.MetricDataResults {
		res.MetricDataResults = append(res.MetricDataResults, discover.MetricDataResult{ID: awssdk.ToString(r.Id), Values: r.Values})
	}
	return res, nil
}

func (p metricsPort) GetMetricData(ctx context.Context, in metrics.GetMetricDataInput) (metrics.GetMetricDataOutput, error) {
	out, err := p.get(ctx, in.StartTime, in.EndTime, in.MetricDataQueries, in.ScanBy, in.NextToken)
	if err != nil {
		return metrics.GetMetricDataOutput{}, err
	}
	res := metrics.GetMetricDataOutput{NextToken: awssdk.ToString(out.NextToken)}
	for _, r := range out.MetricDataResults {
		result := metrics.MetricDataResult{ID: awssdk.ToString(r.Id), Timestamps: r.Timestamps, Values: r.Values, StatusCode: string(r.StatusCode)}
		for _, m := range r.Messages {
			result.Messages = append(result.Messages, metrics.Message{Code: awssdk.ToString(m.Code), Value: awssdk.ToString(m.Value)})
		}
		res.MetricDataResults = append(res.MetricDataResults, result)
	}
	return res, nil
}
