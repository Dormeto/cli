package aws

// The ports of a region's discovery beyond the scan's (discover.RegionPorts):
// instances with their state and security groups, security groups and who they
// let in, an ECS service's desired count and security groups, an Auto Scaling
// group's desired capacity and instances, listener rules by ARN. Ports manager
// packages/estimate/src/bin.ts (portsOf: autoscaling, elbv2, ec2, ecs).

import (
	"context"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"github.com/Dormeto/cli/internal/discover"
)

// Discover is the ports of the region's discovery: the scan (ScanRegion with
// the region's ports, at now()), what each workload's backend is now, the
// security groups and the databases, and what a nap stops. Make one Region per
// account and region (its EKS clusters are kept 15 minutes).
func (r *Region) Discover(where discover.Where, now func() time.Time) discover.RegionPorts {
	return discover.RegionPorts{
		Scan: func(ctx context.Context) (discover.ScanResult, error) {
			return discover.ScanRegion(ctx, r.Scan(), where, now())
		},
		AutoScaling: regionAutoScaling{r.autoscaling.client},
		ELBv2:       ruleDescriber{r.elbv2.client},
		EC2:         regionEC2{r.ec2},
		ECS:         regionECS{r.ecs},
		RDS:         databasesPort{r.rds.client},
		Stopped:     r.Stopped(),
	}
}

// regionAutoScaling is discover.RegionAutoScaling.
type regionAutoScaling struct{ client autoScalingAPI }

func (p regionAutoScaling) Group(ctx context.Context, name string) (*discover.Group, error) {
	out, err := p.client.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{AutoScalingGroupNames: []string{name}})
	if err != nil {
		return nil, err
	}
	if len(out.AutoScalingGroups) == 0 {
		return nil, nil
	}
	g := out.AutoScalingGroups[0]
	group := &discover.Group{Desired: int(awssdk.ToInt32(g.DesiredCapacity))}
	for _, i := range g.Instances {
		group.Instances = append(group.Instances, awssdk.ToString(i.InstanceId))
	}
	return group, nil
}

// ruleDescriber is discover.RuleDescriber: rules by ARN, with the region's ELB
// client (adaptive retries).
type ruleDescriber struct{ client elbv2API }

func (p ruleDescriber) DescribeRules(ctx context.Context, ruleArns []string) ([]discover.Rule, error) {
	out, err := p.client.DescribeRules(ctx, &elb.DescribeRulesInput{RuleArns: ruleArns})
	if err != nil {
		return nil, err
	}
	var rules []discover.Rule
	for _, r := range out.Rules {
		rule := discover.Rule{RuleArn: awssdk.ToString(r.RuleArn), Priority: awssdk.ToString(r.Priority), IsDefault: awssdk.ToBool(r.IsDefault)}
		for _, c := range r.Conditions {
			rule.Conditions = append(rule.Conditions, conditionOf(c))
		}
		for _, a := range r.Actions {
			rule.Actions = append(rule.Actions, actionOf(a))
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// regionEC2API are the EC2 calls of regionEC2.
type regionEC2API interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
}

// regionEC2 is discover.RegionEC2.
type regionEC2 struct{ client regionEC2API }

func (p regionEC2) Instances(ctx context.Context, ids []string) ([]discover.InstanceInfo, error) {
	out, err := p.client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: ids})
	if err != nil {
		return nil, err
	}
	var res []discover.InstanceInfo
	for _, r := range out.Reservations {
		for _, i := range r.Instances {
			info := discover.InstanceInfo{ID: awssdk.ToString(i.InstanceId)}
			if i.State != nil {
				info.State = string(i.State.Name)
			}
			for _, g := range i.SecurityGroups {
				info.SecurityGroups = append(info.SecurityGroups, awssdk.ToString(g.GroupId))
			}
			res = append(res, info)
		}
	}
	return res, nil
}

func (p regionEC2) SecurityGroups(ctx context.Context, ids []string) ([]discover.SecurityGroup, error) {
	out, err := p.client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: ids})
	if err != nil {
		return nil, err
	}
	var res []discover.SecurityGroup
	for _, sg := range out.SecurityGroups {
		group := discover.SecurityGroup{ID: awssdk.ToString(sg.GroupId)}
		for _, perm := range sg.IpPermissions {
			for _, pair := range perm.UserIdGroupPairs {
				group.AllowedFrom = append(group.AllowedFrom, awssdk.ToString(pair.GroupId))
			}
		}
		res = append(res, group)
	}
	return res, nil
}

// regionECS is discover.RegionECS.
type regionECS struct{ client ecsAPI }

func (p regionECS) Service(ctx context.Context, cluster, service string) (*discover.ServiceInfo, error) {
	out, err := p.client.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: optional(cluster), Services: []string{service}})
	if err != nil {
		return nil, err
	}
	if len(out.Services) == 0 {
		return nil, nil
	}
	s := out.Services[0]
	info := &discover.ServiceInfo{DesiredCount: int(s.DesiredCount)}
	if n := s.NetworkConfiguration; n != nil && n.AwsvpcConfiguration != nil {
		info.SecurityGroups = n.AwsvpcConfiguration.SecurityGroups
	}
	return info, nil
}
