package discover

// The backend behind a target group: an Auto Scaling group, an ECS service, a
// Kubernetes owner or an instance, or why there is none.
//
// Ports manager packages/discovery/src/scan.ts (nodeClusterOf, backendOwners,
// backendOf).

import (
	"context"
	"fmt"
	"strings"

	"github.com/Dormeto/cli/internal/kubernetes"
)

// nodeClusterOf is the EKS cluster a node (instance or Auto Scaling group)
// belongs to, from the tags EKS, node groups and Karpenter put; empty when none.
func nodeClusterOf(tags []Tag) string {
	for _, t := range tags {
		switch t.Key {
		case "eks:cluster-name", "eks:eks-cluster-name", "aws:eks:cluster-name":
			if t.Value != "" {
				return t.Value
			}
		}
		if name, ok := strings.CutPrefix(t.Key, "kubernetes.io/cluster/"); ok {
			return name
		}
	}
	return ""
}

type asgOwner struct{ name, arn, cluster string }

// owners: target group → Auto Scaling group and ECS service ARN (the backends
// that register targets in it).
type owners struct {
	groups   map[string]asgOwner
	services map[string]string
}

func backendOwners(ctx context.Context, ports ScanPorts) (owners, error) {
	o := owners{groups: map[string]asgOwner{}, services: map[string]string{}}
	if ports.AutoScaling != nil {
		asgs, err := pages(func(token string) ([]AutoScalingGroup, string, error) {
			out, err := ports.AutoScaling.DescribeAutoScalingGroups(ctx, DescribeAutoScalingGroupsInput{NextToken: token})
			return out.AutoScalingGroups, out.NextToken, err
		})
		if err != nil {
			return o, err
		}
		for _, g := range asgs {
			cluster := nodeClusterOf(g.Tags)
			for _, arn := range g.TargetGroupARNs {
				o.groups[arn] = asgOwner{g.AutoScalingGroupName, g.AutoScalingGroupARN, cluster}
			}
		}
	}
	if ports.ECS != nil {
		clusters, err := pages(func(token string) ([]string, string, error) {
			out, err := ports.ECS.ListClusters(ctx, ListClustersInput{NextToken: token})
			return out.ClusterArns, out.NextToken, err
		})
		if err != nil {
			return o, err
		}
		for _, cluster := range clusters {
			arns, err := pages(func(token string) ([]string, string, error) {
				out, err := ports.ECS.ListServices(ctx, ListServicesInput{Cluster: cluster, NextToken: token})
				return out.ServiceArns, out.NextToken, err
			})
			if err != nil {
				return o, err
			}
			for _, batch := range chunks(arns, 10) {
				out, err := ports.ECS.DescribeServices(ctx, DescribeServicesInput{Cluster: cluster, Services: batch})
				if err != nil {
					return o, err
				}
				for _, s := range out.Services {
					for _, lb := range s.LoadBalancers {
						if lb.TargetGroupArn != "" && s.ServiceArn != "" {
							o.services[lb.TargetGroupArn] = s.ServiceArn
						}
					}
				}
			}
		}
	}
	return o, nil
}

// found is the answer of backendOf: the backend, or the reason there is none
// the scan can nap.
type found struct {
	backend *Backend
	reason  string
}

// part is the i-th ':' field of an ARN, as JS reads it ("undefined" when missing).
func part(arn string, i int) string {
	if fields := strings.Split(arn, ":"); i < len(fields) {
		return fields[i]
	}
	return "undefined"
}

func backendOf(ctx context.Context, ports ScanPorts, group TargetGroup, o owners, clusters []kubernetes.Cluster, tags map[string]string) (found, error) {
	arn := group.TargetGroupArn
	if asg, ok := o.groups[arn]; ok {
		if asg.cluster != "" {
			return found{reason: fmt.Sprintf("the Auto Scaling group %s holds nodes of EKS cluster %s: never napped (its pods are Kubernetes workloads)", asg.name, asg.cluster)}, nil
		}
		return found{backend: &Backend{Kind: KindASG, ID: asg.name, ARN: asg.arn}}, nil
	}
	if service, ok := o.services[arn]; ok {
		return found{backend: &Backend{Kind: KindECS, ID: service, ARN: service}}, nil
	}
	health, err := ports.ELBv2.DescribeTargetHealth(ctx, DescribeTargetHealthInput{TargetGroupArn: arn})
	if err != nil {
		return found{}, err
	}
	var ids []string
	for _, d := range health.TargetHealthDescriptions {
		if d.Target.ID != "" {
			ids = append(ids, d.Target.ID)
		}
	}
	if len(ids) == 0 {
		return found{reason: "no backend: no registered target, Auto Scaling group or ECS service"}, nil
	}
	// Pods (the controller's tags, else IP targets among a cluster's pods): their owner, or why it cannot be napped.
	if len(clusters) > 0 {
		if tags == nil {
			tags = map[string]string{}
		}
		res := kubernetes.Backend(ctx, clusters, kubernetes.Group{ARN: arn, Type: group.TargetType, VPCID: group.VpcId, Tags: tags, Targets: ids})
		if res.Backend != nil {
			return found{backend: &Backend{Kind: res.Backend.Kind, ID: res.Backend.ID, Kubernetes: &res.Backend.Kubernetes}}, nil
		}
		if res.Reason != "" {
			return found{reason: res.Reason}, nil
		}
	}
	if group.TargetType == "ip" {
		return found{reason: "IP targets without an ECS service"}, nil
	}
	// Instance targets in a region with clusters may be their nodes (a NodePort Service, a TargetGroupBinding in instance
	// mode): never stopped as instances. A role that may not describe them (before 0.10.0) lists no cluster either.
	if len(clusters) > 0 && ports.EC2 != nil {
		type node struct{ id, cluster string }
		var nodes []node
		for _, batch := range chunks(ids, 100) {
			out, err := ports.EC2.DescribeInstances(ctx, DescribeInstancesInput{InstanceIds: batch})
			if err != nil {
				out = DescribeInstancesOutput{} // as the Node scan: no reservations
			}
			for _, r := range out.Reservations {
				for _, i := range r.Instances {
					if cluster := nodeClusterOf(i.Tags); cluster != "" && i.InstanceID != "" {
						nodes = append(nodes, node{i.InstanceID, cluster})
					}
				}
			}
		}
		if len(nodes) > 0 {
			shown := make([]string, 0, 5)
			for _, n := range nodes[:min(5, len(nodes))] {
				shown = append(shown, n.id)
			}
			return found{reason: fmt.Sprintf("targets %s are nodes of EKS cluster %s (a NodePort Service or a TargetGroupBinding in instance mode): not traced to their pods yet, never napped as instances", strings.Join(shown, ", "), nodes[0].cluster)}, nil
		}
	}
	// The instance's ARN in the target group's partition, region and account.
	return found{backend: &Backend{Kind: KindInstance, ID: ids[0], ARN: fmt.Sprintf("arn:%s:ec2:%s:%s:instance/%s", part(arn, 1), part(arn, 3), part(arn, 4), ids[0])}}, nil
}
