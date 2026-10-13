package aws

// CloudWatch: the target groups' hourly requests the scan reads for their idle
// hours (discover.CloudWatchPort). Ports manager
// packages/discovery/src/aws.ts scanPorts's cloudwatch.

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/Dormeto/cli/internal/discover"
)

type cloudWatchAPI interface {
	GetMetricData(context.Context, *cloudwatch.GetMetricDataInput, ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

type cloudWatchPort struct{ client cloudWatchAPI }

func (p cloudWatchPort) GetMetricData(ctx context.Context, in discover.GetMetricDataInput) (discover.GetMetricDataOutput, error) {
	start, end := in.StartTime, in.EndTime
	ask := &cloudwatch.GetMetricDataInput{StartTime: &start, EndTime: &end, NextToken: optional(in.NextToken)}
	for _, q := range in.MetricDataQueries {
		metric := &cwtypes.Metric{Namespace: awssdk.String(q.MetricStat.Metric.Namespace), MetricName: awssdk.String(q.MetricStat.Metric.MetricName)}
		for _, d := range q.MetricStat.Metric.Dimensions {
			metric.Dimensions = append(metric.Dimensions, cwtypes.Dimension{Name: awssdk.String(d.Name), Value: awssdk.String(d.Value)})
		}
		ask.MetricDataQueries = append(ask.MetricDataQueries, cwtypes.MetricDataQuery{
			Id: awssdk.String(q.ID), ReturnData: awssdk.Bool(q.ReturnData),
			MetricStat: &cwtypes.MetricStat{Metric: metric, Period: awssdk.Int32(int32(q.MetricStat.Period)), Stat: awssdk.String(q.MetricStat.Stat)},
		})
	}
	out, err := p.client.GetMetricData(ctx, ask)
	if err != nil {
		return discover.GetMetricDataOutput{}, err
	}
	res := discover.GetMetricDataOutput{NextToken: awssdk.ToString(out.NextToken)}
	for _, r := range out.MetricDataResults {
		res.MetricDataResults = append(res.MetricDataResults, discover.MetricDataResult{ID: awssdk.ToString(r.Id), Values: r.Values})
	}
	return res, nil
}
