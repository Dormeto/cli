package aws

import (
	"context"
	"reflect"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/metrics"
)

type fakeCloudWatch struct {
	asked []*cloudwatch.GetMetricDataInput
}

func (f *fakeCloudWatch) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	f.asked = append(f.asked, in)
	return &cloudwatch.GetMetricDataOutput{NextToken: awssdk.String("n"), MetricDataResults: []cwtypes.MetricDataResult{{Id: awssdk.String("q0"), Values: []float64{0, 12}}}}, nil
}

func TestTheCloudWatchAdapterMapsTheQueriesAndTheirValues(t *testing.T) {
	f := &fakeCloudWatch{}
	from := time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC)
	to := from.Add(7 * 24 * time.Hour)
	query := discover.MetricDataQuery{ID: "q0", ReturnData: true, MetricStat: discover.MetricStat{
		Metric: discover.Metric{Namespace: "AWS/ApplicationELB", MetricName: "RequestCount", Dimensions: []discover.Dimension{{Name: "TargetGroup", Value: "targetgroup/web/1"}}},
		Period: 3600, Stat: "Sum",
	}}
	out, err := cloudWatchPort{f}.GetMetricData(context.Background(), discover.GetMetricDataInput{StartTime: from, EndTime: to, MetricDataQueries: []discover.MetricDataQuery{query}, NextToken: "t"})
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "results", out, `{"MetricDataResults":[{"Id":"q0","Values":[0,12]}],"NextToken":"n"}`)
	want := &cloudwatch.GetMetricDataInput{StartTime: &from, EndTime: &to, NextToken: awssdk.String("t"), MetricDataQueries: []cwtypes.MetricDataQuery{{
		Id: awssdk.String("q0"), ReturnData: awssdk.Bool(true), MetricStat: &cwtypes.MetricStat{
			Metric: &cwtypes.Metric{Namespace: awssdk.String("AWS/ApplicationELB"), MetricName: awssdk.String("RequestCount"), Dimensions: []cwtypes.Dimension{{Name: awssdk.String("TargetGroup"), Value: awssdk.String("targetgroup/web/1")}}},
			Period: awssdk.Int32(3600), Stat: awssdk.String("Sum"),
		},
	}}}
	if !reflect.DeepEqual(f.asked, []*cloudwatch.GetMetricDataInput{want}) {
		t.Errorf("asked %+v", f.asked[0])
	}
}

type metricsCloudWatch struct {
	asked []*cloudwatch.GetMetricDataInput
}

func (f *metricsCloudWatch) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	f.asked = append(f.asked, in)
	at := time.Date(2026, 10, 5, 11, 5, 0, 0, time.UTC)
	return &cloudwatch.GetMetricDataOutput{NextToken: awssdk.String("n"), MetricDataResults: []cwtypes.MetricDataResult{
		{Id: awssdk.String("m0"), StatusCode: cwtypes.StatusCodePartialData, Timestamps: []time.Time{at}, Values: []float64{3}},
		{Id: awssdk.String("m1"), StatusCode: cwtypes.StatusCodeForbidden, Messages: []cwtypes.MessageData{{Code: awssdk.String("Forbidden"), Value: awssdk.String("denied")}}},
	}}, nil
}

// The estimate's port is the same adapter in the API's own shapes: the scan
// order, the status and messages, the timestamps.
func TestTheCloudWatchAdapterGivesTheEstimateTheStatusMessagesAndTimestampsAndAsksTheOldestFirst(t *testing.T) {
	f := &metricsCloudWatch{}
	from := time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC)
	to := from.Add(14 * 24 * time.Hour)
	query := metrics.MetricDataQuery{ID: "m0", ReturnData: true, MetricStat: metrics.MetricStat{
		Metric: metrics.Metric{Namespace: "AWS/EC2", MetricName: "CPUUtilization", Dimensions: []metrics.Dimension{{Name: "InstanceId", Value: "i-1"}}},
		Period: 300, Stat: "SampleCount",
	}}
	out, err := metricsPort{cloudWatchPort{f}}.GetMetricData(context.Background(), metrics.GetMetricDataInput{StartTime: from, EndTime: to, MetricDataQueries: []metrics.MetricDataQuery{query}, ScanBy: metrics.ScanByAscending})
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "results", out, `{"MetricDataResults":[{"Id":"m0","Timestamps":["2026-10-05T11:05:00Z"],"Values":[3],"StatusCode":"PartialData"},{"Id":"m1","StatusCode":"Forbidden","Messages":[{"Code":"Forbidden","Value":"denied"}]}],"NextToken":"n"}`)
	if len(f.asked) != 1 || f.asked[0].ScanBy != cwtypes.ScanByTimestampAscending || f.asked[0].NextToken != nil || len(f.asked[0].MetricDataQueries) != 1 {
		t.Errorf("asked %+v", f.asked)
	}
}
