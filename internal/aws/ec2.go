package aws

// EC2: the instances the scan reads (an EKS node's cluster) and those a nap
// stops (their type, OS and lifecycle), and the regions an account has
// enabled. Ports manager packages/discovery/src/aws.ts (scanPorts's ec2,
// enabledRegionsOf) and packages/estimate/src/bin.ts (portsOf's ec2, regionsOf).

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/stopped"
)

type ec2API interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeRegions(context.Context, *ec2.DescribeRegionsInput, ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error)
}

// scanEC2 is discover.EC2Port: the instances with their tags.
type scanEC2 struct{ client ec2API }

func (p scanEC2) DescribeInstances(ctx context.Context, in discover.DescribeInstancesInput) (discover.DescribeInstancesOutput, error) {
	out, err := p.client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: in.InstanceIds})
	if err != nil {
		return discover.DescribeInstancesOutput{}, err
	}
	var res discover.DescribeInstancesOutput
	for _, r := range out.Reservations {
		var reservation discover.Reservation
		for _, i := range r.Instances {
			instance := discover.Instance{InstanceID: awssdk.ToString(i.InstanceId)}
			for _, t := range i.Tags {
				instance.Tags = append(instance.Tags, discover.Tag{Key: awssdk.ToString(t.Key), Value: awssdk.ToString(t.Value)})
			}
			reservation.Instances = append(reservation.Instances, instance)
		}
		res.Reservations = append(res.Reservations, reservation)
	}
	return res, nil
}

// stoppedEC2 is stopped.EC2Port: the instances' type, OS and lifecycle.
type stoppedEC2 struct{ client ec2API }

func (p stoppedEC2) DescribeInstances(ctx context.Context, in stopped.DescribeInstancesInput) (stopped.DescribeInstancesOutput, error) {
	out, err := p.client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: in.InstanceIds})
	if err != nil {
		return stopped.DescribeInstancesOutput{}, err
	}
	var res stopped.DescribeInstancesOutput
	for _, r := range out.Reservations {
		var reservation stopped.Reservation
		for _, i := range r.Instances {
			reservation.Instances = append(reservation.Instances, stopped.Instance{
				InstanceID: awssdk.ToString(i.InstanceId), InstanceType: string(i.InstanceType),
				PlatformDetails: awssdk.ToString(i.PlatformDetails), InstanceLifecycle: string(i.InstanceLifecycle),
			})
		}
		res.Reservations = append(res.Reservations, reservation)
	}
	return res, nil
}

// EnabledRegions are the regions an account has enabled (opt-in regions only
// once opted in), asked in us-east-1 with the account's configuration (Of).
func EnabledRegions(ctx context.Context, cfg awssdk.Config) ([]string, error) {
	return enabledRegions(ctx, ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.Region = global }))
}

func enabledRegions(ctx context.Context, client ec2API) ([]string, error) {
	out, err := client.DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
	if err != nil {
		return nil, err
	}
	regions := []string{}
	for _, r := range out.Regions {
		if name := awssdk.ToString(r.RegionName); name != "" {
			regions = append(regions, name)
		}
	}
	return regions, nil
}
