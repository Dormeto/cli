package aws

import (
	"context"
	"reflect"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"

	"github.com/Dormeto/cli/internal/discover"
)

type fakeASG struct {
	asked []*autoscaling.DescribeAutoScalingGroupsInput
}

func (f *fakeASG) DescribeAutoScalingGroups(_ context.Context, in *autoscaling.DescribeAutoScalingGroupsInput, _ ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
	f.asked = append(f.asked, in)
	return &autoscaling.DescribeAutoScalingGroupsOutput{NextToken: awssdk.String("n"), AutoScalingGroups: []asgtypes.AutoScalingGroup{{
		AutoScalingGroupName: awssdk.String("web"), AutoScalingGroupARN: awssdk.String("arn:asg/web"), TargetGroupARNs: []string{"arn:tg/web/1"},
		Tags: []asgtypes.TagDescription{{Key: awssdk.String("eks:cluster-name"), Value: awssdk.String("demo")}},
	}}}, nil
}

func TestTheAutoScalingAdapterMapsTheGroups(t *testing.T) {
	f := &fakeASG{}
	out, err := autoScalingPort{f}.DescribeAutoScalingGroups(context.Background(), discover.DescribeAutoScalingGroupsInput{NextToken: "t"})
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "groups", out, `{"AutoScalingGroups":[{"AutoScalingGroupName":"web","AutoScalingGroupARN":"arn:asg/web","TargetGroupARNs":["arn:tg/web/1"],"Tags":[{"Key":"eks:cluster-name","Value":"demo"}]}],"NextToken":"n"}`)
	if !reflect.DeepEqual(f.asked, []*autoscaling.DescribeAutoScalingGroupsInput{{NextToken: awssdk.String("t")}}) {
		t.Errorf("asked %+v", f.asked[0])
	}
}
