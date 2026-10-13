package aws

// The Auto Scaling groups the scan reads (discover.AutoScalingPort). Ports
// manager packages/discovery/src/aws.ts scanPorts's autoscaling.

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"

	"github.com/Dormeto/cli/internal/discover"
)

type autoScalingAPI interface {
	DescribeAutoScalingGroups(context.Context, *autoscaling.DescribeAutoScalingGroupsInput, ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error)
}

type autoScalingPort struct{ client autoScalingAPI }

func (p autoScalingPort) DescribeAutoScalingGroups(ctx context.Context, in discover.DescribeAutoScalingGroupsInput) (discover.DescribeAutoScalingGroupsOutput, error) {
	out, err := p.client.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{NextToken: optional(in.NextToken)})
	if err != nil {
		return discover.DescribeAutoScalingGroupsOutput{}, err
	}
	res := discover.DescribeAutoScalingGroupsOutput{NextToken: awssdk.ToString(out.NextToken)}
	for _, g := range out.AutoScalingGroups {
		group := discover.AutoScalingGroup{
			AutoScalingGroupName: awssdk.ToString(g.AutoScalingGroupName), AutoScalingGroupARN: awssdk.ToString(g.AutoScalingGroupARN), TargetGroupARNs: g.TargetGroupARNs,
		}
		for _, t := range g.Tags {
			group.Tags = append(group.Tags, discover.Tag{Key: awssdk.ToString(t.Key), Value: awssdk.ToString(t.Value)})
		}
		res.AutoScalingGroups = append(res.AutoScalingGroups, group)
	}
	return res, nil
}
