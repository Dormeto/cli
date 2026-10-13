package aws

// ECS: the clusters and services the scan reads (their target groups) and the
// services a nap stops (their tasks' size and capacity providers). Ports
// manager packages/discovery/src/aws.ts (scanPorts's ecs) and
// packages/estimate/src/bin.ts (portsOf's ecs).

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/stopped"
)

type ecsAPI interface {
	ListClusters(context.Context, *ecs.ListClustersInput, ...func(*ecs.Options)) (*ecs.ListClustersOutput, error)
	ListServices(context.Context, *ecs.ListServicesInput, ...func(*ecs.Options)) (*ecs.ListServicesOutput, error)
	DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	DescribeTaskDefinition(context.Context, *ecs.DescribeTaskDefinitionInput, ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
}

// scanECS is discover.ECSPort.
type scanECS struct{ client ecsAPI }

func (p scanECS) ListClusters(ctx context.Context, in discover.ListClustersInput) (discover.ListClustersOutput, error) {
	out, err := p.client.ListClusters(ctx, &ecs.ListClustersInput{NextToken: optional(in.NextToken)})
	if err != nil {
		return discover.ListClustersOutput{}, err
	}
	return discover.ListClustersOutput{ClusterArns: out.ClusterArns, NextToken: awssdk.ToString(out.NextToken)}, nil
}

func (p scanECS) ListServices(ctx context.Context, in discover.ListServicesInput) (discover.ListServicesOutput, error) {
	out, err := p.client.ListServices(ctx, &ecs.ListServicesInput{Cluster: optional(in.Cluster), NextToken: optional(in.NextToken)})
	if err != nil {
		return discover.ListServicesOutput{}, err
	}
	return discover.ListServicesOutput{ServiceArns: out.ServiceArns, NextToken: awssdk.ToString(out.NextToken)}, nil
}

func (p scanECS) DescribeServices(ctx context.Context, in discover.DescribeServicesInput) (discover.DescribeServicesOutput, error) {
	out, err := p.client.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: optional(in.Cluster), Services: in.Services})
	if err != nil {
		return discover.DescribeServicesOutput{}, err
	}
	var res discover.DescribeServicesOutput
	for _, s := range out.Services {
		service := discover.Service{ServiceArn: awssdk.ToString(s.ServiceArn)}
		for _, lb := range s.LoadBalancers {
			service.LoadBalancers = append(service.LoadBalancers, discover.ServiceLoadBalancer{TargetGroupArn: awssdk.ToString(lb.TargetGroupArn)})
		}
		res.Services = append(res.Services, service)
	}
	return res, nil
}

// stoppedECS is stopped.ECSPort.
type stoppedECS struct{ client ecsAPI }

func (p stoppedECS) DescribeServices(ctx context.Context, in stopped.DescribeServicesInput) (stopped.DescribeServicesOutput, error) {
	out, err := p.client.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: optional(in.Cluster), Services: in.Services})
	if err != nil {
		return stopped.DescribeServicesOutput{}, err
	}
	var res stopped.DescribeServicesOutput
	for _, s := range out.Services {
		service := stopped.Service{
			ServiceArn: awssdk.ToString(s.ServiceArn), ClusterArn: awssdk.ToString(s.ClusterArn),
			TaskDefinition: awssdk.ToString(s.TaskDefinition), LaunchType: string(s.LaunchType),
		}
		for _, c := range s.CapacityProviderStrategy {
			service.CapacityProviderStrategy = append(service.CapacityProviderStrategy, stopped.CapacityProviderStrategyItem{
				CapacityProvider: awssdk.ToString(c.CapacityProvider), Base: int(c.Base), Weight: int(c.Weight),
			})
		}
		res.Services = append(res.Services, service)
	}
	return res, nil
}

func (p stoppedECS) DescribeTaskDefinition(ctx context.Context, in stopped.DescribeTaskDefinitionInput) (stopped.DescribeTaskDefinitionOutput, error) {
	out, err := p.client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: optional(in.TaskDefinition)})
	if err != nil {
		return stopped.DescribeTaskDefinitionOutput{}, err
	}
	var res stopped.DescribeTaskDefinitionOutput
	if d := out.TaskDefinition; d != nil {
		res.TaskDefinition = &stopped.TaskDefinition{CPU: awssdk.ToString(d.Cpu), Memory: awssdk.ToString(d.Memory)}
		if rp := d.RuntimePlatform; rp != nil {
			res.TaskDefinition.RuntimePlatform = &stopped.RuntimePlatform{CPUArchitecture: string(rp.CpuArchitecture), OperatingSystemFamily: string(rp.OperatingSystemFamily)}
		}
	}
	return res, nil
}
