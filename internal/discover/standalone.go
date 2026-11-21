package discover

// The standalone scan of one account and region (vision standalone workloads design §6): the EC2 instances, Auto
// Scaling groups, ECS services, RDS instances and clusters that no route uses, each a candidate of its own resource,
// eligible or ineligible with the router's reasons, or managed when the router tagged it as a standalone workload's
// resource. The texts and the workload id are the router's (router lambda/src/lib/onboarding.mjs resourceOf,
// workloadIdOf).
//
// Ports manager packages/discovery/src/standalone.ts (standaloneRules, workloadIdOf, refused) and the resource kinds'
// words of packages/fleet/src/idle-signals.ts (RESOURCE_KIND_TEXT).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/stopped"
)

// The kinds of a standalone workload's resource beyond the backend kinds (KindInstance, KindASG, KindECS).
const (
	KindRDSInstance = "rds-instance"
	KindRDSCluster  = "rds-cluster"
)

// KindStandalone is a standalone candidate's kind (an HTTP candidate has none).
const KindStandalone = "standalone"

// ResourceKindText is each resource kind as the router's texts say it.
var ResourceKindText = map[string]string{
	KindInstance: "instance", KindASG: "Auto Scaling group", KindECS: "ECS service",
	KindRDSInstance: "RDS instance", KindRDSCluster: "RDS cluster",
}

const (
	groupTag = "aws:autoscaling:groupName"
	again    = "apply the access module or the AWS CLI script of this release again"
)

// ecsTagsUnread is what the scan says when the services' tags are refused (scan.ts ECS_TAGS_UNREAD).
const ecsTagsUnread = "resource tags of ECS services could not be read: the discovery role may not describe them with their tags; they are listed without them"

// The tags EKS, its node groups, eksctl, Karpenter and the cluster autoscaler put on a cluster's nodes (the router's).
var eksNodeTag = regexp.MustCompile(`^(eks:cluster-name|eks:nodegroup-name|alpha\.eksctl\.io/cluster-name|karpenter\.sh/|kubernetes\.io/cluster/|k8s\.io/cluster-autoscaler/)`)

// refusedError is a call the discovery role may not make.
var refusedError = regexp.MustCompile(`(?i)AccessDenied|UnauthorizedOperation|not authorized`)

func refused(err error) bool {
	return refusedError.MatchString(awserr.Name(err) + " " + awserr.Message(err))
}

// WorkloadIDOf is the router's workload id of a resource: the first 16 hex characters of the SHA-256 of its ARN.
func WorkloadIDOf(arn string) string {
	sum := sha256.Sum256([]byte(arn))
	return hex.EncodeToString(sum[:])[:16]
}

// StandaloneRule is a standalone candidate: a resource no route uses (ID and ResourceArn its ARN), its backend the
// resource itself.
type StandaloneRule struct {
	Kind        string              `json:"kind"` // KindStandalone
	ID          string              `json:"id"`
	Account     string              `json:"account"`
	Region      string              `json:"region"`
	ResourceArn string              `json:"resourceArn"`
	Name        string              `json:"name"`
	Backend     Backend             `json:"backend"`
	Tags        map[string][]string `json:"tags"`
	Eligibility string              `json:"eligibility"`
	Reason      string              `json:"reason,omitempty"`
	WorkloadID  string              `json:"workloadId,omitempty"`
	IdleHours7d *int                `json:"idleHours7d"`
}

// StandaloneReport is what the standalone scan could not read: a sentence each for the run's problems, and the kinds
// whose stored candidates stay as they were (a kind without its port is unread without a problem).
type StandaloneReport struct {
	Problems []string `json:"problems"`
	Unread   []string `json:"unread"`
}

// StandaloneScan is the standalone part of a region's scan: its candidates (instances, then groups, services, RDS
// instances, RDS clusters, each in the order AWS listed them) and what it could not read.
type StandaloneScan struct {
	Rules  []StandaloneRule `json:"rules"`
	Report StandaloneReport `json:"report"`
}

// resource is a resource as read: what it is, its tags, and why the router would refuse it (in the router's order).
type resource struct {
	kind, id, arn, name string
	tags                map[string]string
	problems            []string
}

type outcome int

const (
	outcomeOut outcome = iota + 1
	outcomeListed
	outcomeManaged
)

func tagMap(list []Tag) map[string]string {
	m := make(map[string]string, len(list))
	for _, t := range list {
		m[t.Key] = t.Value
	}
	return m
}

func rdsTagMap(list []stopped.Tag) map[string]string {
	m := make(map[string]string, len(list))
	for _, t := range list {
		m[t.Key] = t.Value
	}
	return m
}

// groupNameOf is a target group's name from its ARN (…:targetgroup/<name>/<id>).
func groupNameOf(arn string) string {
	last := arn[strings.LastIndex(arn, ":")+1:]
	if parts := strings.Split(last, "/"); len(parts) > 1 {
		return parts[1]
	}
	return arn
}

func partitionOf(region string) string {
	switch {
	case strings.HasPrefix(region, "cn-"):
		return "aws-cn"
	case strings.HasPrefix(region, "us-gov-"):
		return "aws-us-gov"
	}
	return "aws"
}

func isNode(tags map[string]string) bool {
	for k := range tags {
		if eksNodeTag.MatchString(k) {
			return true
		}
	}
	return false
}

func feeds(label string, arns []string) []string {
	out := make([]string, 0, len(arns))
	for _, arn := range arns {
		out = append(out, fmt.Sprintf("%s feeds target group %s: it belongs to an HTTP workload (onboard its target group)", label, groupNameOf(arn)))
	}
	return out
}

// standaloneRead is what the HTTP scan read that the standalone one needs.
type standaloneRead struct {
	targetGroups []TargetGroup   // every target group of the region
	routed       map[string]bool // the target groups a rule forwards to: their backends are HTTP candidates'
	groups       []AutoScalingGroup
	services     []Service // described with their tags
}

// standaloneRules lists the region's standalone candidates. A list the discovery role may not read is said, and its
// kind left as it was; any other failure fails the region.
func standaloneRules(ctx context.Context, ports ScanPorts, where Where, read standaloneRead) ([]StandaloneRule, StandaloneReport, error) {
	report := StandaloneReport{Problems: []string{}, Unread: []string{}}
	byKind := map[string][]StandaloneRule{}
	listed := func(kind, what, action string, err error) error {
		if !refused(err) {
			return err
		}
		report.Problems = append(report.Problems, fmt.Sprintf("%s not listed: the discovery role may not call %s (%s)", what, action, again))
		report.Unread = append(report.Unread, kind)
		return nil
	}
	// Managed: the router's tag of the resource's own workload. Left out: tagged by another workload (its backend or its
	// companion), or leftOut (an HTTP candidate's backend, a Kubernetes node, a member of a group left out or managed).
	settle := func(r resource, leftOut bool) outcome {
		tagged, isTagged := r.tags[workloadTag]
		own := isTagged && tagged == WorkloadIDOf(r.arn)
		if !own && (leftOut || isTagged) {
			return outcomeOut
		}
		tags := make(map[string][]string, len(r.tags))
		for k, v := range r.tags {
			tags[k] = []string{v}
		}
		c := StandaloneRule{
			Kind: KindStandalone, ID: r.arn, Account: where.Account, Region: where.Region, ResourceArn: r.arn, Name: r.name,
			Backend: Backend{Kind: r.kind, ID: r.id, ARN: r.arn}, Tags: tags, Eligibility: Eligible,
		}
		switch {
		case own:
			c.Eligibility, c.WorkloadID = Managed, tagged
		case len(r.problems) > 0:
			c.Eligibility = Ineligible
		}
		if len(r.problems) > 0 {
			c.Reason = strings.Join(r.problems, "; ")
		}
		byKind[r.kind] = append(byKind[r.kind], c)
		if own {
			return outcomeManaged
		}
		return outcomeListed
	}
	routed := func(arns []string) bool {
		return slices.ContainsFunc(arns, func(a string) bool { return read.routed[a] })
	}

	// Groups before their instances: an instance of a group left out or managed is left out with it.
	groups := map[string]outcome{}
	for _, g := range read.groups {
		if g.AutoScalingGroupName == "" || g.AutoScalingGroupARN == "" {
			continue
		}
		label := ResourceKindText[KindASG] + " " + g.AutoScalingGroupName
		tags := tagMap(g.Tags)
		problems := feeds(label, g.TargetGroupARNs)
		if isNode(tags) {
			problems = append(problems, label+" holds EKS nodes: onboard the Kubernetes workloads that run on them")
		}
		groups[g.AutoScalingGroupName] = settle(resource{KindASG, g.AutoScalingGroupName, g.AutoScalingGroupARN, g.AutoScalingGroupName, tags, problems}, routed(g.TargetGroupARNs))
	}

	// The instances, and the instance target groups each one is registered in (the router reads every one, in their order).
	if ports.EC2 != nil {
		instances, err := pages(func(token string) ([]Instance, string, error) {
			out, err := ports.EC2.DescribeInstances(ctx, DescribeInstancesInput{NextToken: token})
			var found []Instance
			for _, r := range out.Reservations {
				found = append(found, r.Instances...)
			}
			return found, out.NextToken, err
		})
		if err != nil {
			if err := listed(KindInstance, "EC2 instances", "ec2:DescribeInstances", err); err != nil {
				return nil, report, err
			}
			instances = nil
		}
		targets := map[string][]string{}
		if len(instances) > 0 {
			for _, g := range read.targetGroups {
				if g.TargetType != "instance" || g.TargetGroupArn == "" {
					continue
				}
				health, err := ports.ELBv2.DescribeTargetHealth(ctx, DescribeTargetHealthInput{TargetGroupArn: g.TargetGroupArn})
				if err != nil {
					return nil, report, err
				}
				for _, d := range health.TargetHealthDescriptions {
					if d.Target.ID != "" {
						targets[d.Target.ID] = append(targets[d.Target.ID], g.TargetGroupArn)
					}
				}
			}
		}
		for _, i := range instances {
			if i.InstanceID == "" || (i.State != nil && (i.State.Name == "terminated" || i.State.Name == "shutting-down")) {
				continue
			}
			tags := tagMap(i.Tags)
			label := ResourceKindText[KindInstance] + " " + i.InstanceID
			group := tags[groupTag]
			tgs := targets[i.InstanceID]
			var problems []string
			if group != "" {
				problems = append(problems, fmt.Sprintf("%s is in Auto Scaling group %s: onboard the group (it would replace a stopped instance)", label, group))
			}
			if i.RootDeviceType == "instance-store" {
				problems = append(problems, label+" has an instance store root volume: it cannot be stopped")
			}
			if i.InstanceLifecycle == "spot" {
				problems = append(problems, label+" is a Spot instance: Dormeto stops On-Demand instances only")
			}
			if group == "" {
				problems = append(problems, feeds(label, tgs)...)
			}
			parent := outcome(0)
			if group != "" {
				parent = groups[group]
			}
			name := tags["Name"]
			if name == "" {
				name = i.InstanceID
			}
			arn := fmt.Sprintf("arn:%s:ec2:%s:%s:instance/%s", partitionOf(where.Region), where.Region, where.Account, i.InstanceID)
			settle(resource{KindInstance, i.InstanceID, arn, name, tags, problems}, isNode(tags) || routed(tgs) || parent == outcomeOut || parent == outcomeManaged)
		}
	} else {
		report.Unread = append(report.Unread, KindInstance)
	}

	for _, s := range read.services {
		if s.ServiceArn == "" || s.Status == "INACTIVE" {
			continue
		}
		id := s.ServiceArn
		if _, after, ok := strings.Cut(s.ServiceArn, ":service/"); ok {
			id, _, _ = strings.Cut(after, ":service/") // split(':service/')[1] stops at a second occurrence
		}
		label := ResourceKindText[KindECS] + " " + id
		var tgs []string
		for _, lb := range s.LoadBalancers {
			if lb.TargetGroupArn != "" {
				tgs = append(tgs, lb.TargetGroupArn)
			}
		}
		tags := make(map[string]string, len(s.Tags))
		for _, t := range s.Tags {
			tags[t.Key] = t.Value
		}
		problems := feeds(label, tgs)
		if s.SchedulingStrategy == "DAEMON" {
			problems = append(problems, label+" uses the daemon scheduling strategy: it has no desired count to set to 0")
		}
		settle(resource{KindECS, s.ServiceArn, s.ServiceArn, id[strings.LastIndex(id, "/")+1:], tags, problems}, routed(tgs))
	}

	// Clusters before their members: a member of a cluster left out or managed is left out with it.
	if ports.RDS != nil {
		databases, err := pages(func(marker string) ([]stopped.DBInstance, string, error) {
			out, err := ports.RDS.DescribeDBInstances(ctx, stopped.DescribeDBInstancesInput{Marker: marker})
			return out.DBInstances, out.Marker, err
		})
		if err != nil {
			if err := listed(KindRDSInstance, "RDS instances", "rds:DescribeDBInstances", err); err != nil {
				return nil, report, err
			}
			databases = nil
		}
		clusters, err := pages(func(marker string) ([]stopped.DBCluster, string, error) {
			out, err := ports.RDS.DescribeDBClusters(ctx, stopped.DescribeDBClustersInput{Marker: marker})
			return out.DBClusters, out.Marker, err
		})
		if err != nil {
			if err := listed(KindRDSCluster, "RDS clusters", "rds:DescribeDBClusters", err); err != nil {
				return nil, report, err
			}
			clusters = nil
		}
		clusterOf := map[string]outcome{}
		for _, c := range clusters {
			if c.DBClusterIdentifier == "" || c.DBClusterArn == "" {
				continue
			}
			var problems []string
			if c.EngineMode == "serverless" {
				problems = append(problems, ResourceKindText[KindRDSCluster]+" "+c.DBClusterIdentifier+" is Aurora Serverless v1: it cannot be stopped")
			}
			clusterOf[c.DBClusterIdentifier] = settle(resource{KindRDSCluster, c.DBClusterIdentifier, c.DBClusterArn, c.DBClusterIdentifier, rdsTagMap(c.TagList), problems}, false)
		}
		for _, d := range databases {
			if d.DBInstanceIdentifier == "" || d.DBInstanceArn == "" {
				continue
			}
			label := ResourceKindText[KindRDSInstance] + " " + d.DBInstanceIdentifier
			var problems []string
			parent := outcome(0)
			if d.DBClusterIdentifier != "" {
				problems = append(problems, fmt.Sprintf("%s is a member of Aurora cluster %s: onboard the cluster", label, d.DBClusterIdentifier))
				parent = clusterOf[d.DBClusterIdentifier]
			}
			if d.ReadReplicaSourceDBInstanceIdentifier != "" {
				problems = append(problems, label+" is a read replica: it cannot be stopped")
			}
			settle(resource{KindRDSInstance, d.DBInstanceIdentifier, d.DBInstanceArn, d.DBInstanceIdentifier, rdsTagMap(d.TagList), problems}, parent == outcomeOut || parent == outcomeManaged)
		}
	} else {
		report.Unread = append(report.Unread, KindRDSInstance, KindRDSCluster)
	}

	rules := []StandaloneRule{}
	for _, kind := range []string{KindInstance, KindASG, KindECS, KindRDSInstance, KindRDSCluster} {
		rules = append(rules, byKind[kind]...)
	}
	return rules, report, nil
}
