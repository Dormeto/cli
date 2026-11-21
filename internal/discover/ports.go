package discover

// The ports discovery's scan and the organization inventory read through: the
// AWS APIs' own shapes (the Node ports' JSON; PascalCase for ELBv2, Auto
// Scaling, EC2, CloudWatch and Organizations, camelCase for ECS), and the
// Kubernetes ports of internal/kubernetes. A nil port is a call the scan does
// not make.
//
// Ports manager packages/discovery/src/scan.ts (ScanPorts) and org.ts
// (OrganizationsPort).

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Dormeto/cli/internal/kubernetes"
	"github.com/Dormeto/cli/internal/stopped"
)

// Tag is an AWS tag.
type Tag struct {
	Key   string `json:"Key,omitempty"`
	Value string `json:"Value,omitempty"`
}

// LoadBalancer is the part of an ELBv2 load balancer the scan reads.
type LoadBalancer struct {
	LoadBalancerArn  string `json:"LoadBalancerArn,omitempty"`
	LoadBalancerName string `json:"LoadBalancerName,omitempty"`
	Type             string `json:"Type,omitempty"`
}

// Listener is an ELBv2 listener.
type Listener struct {
	ListenerArn string `json:"ListenerArn,omitempty"`
	Port        int    `json:"Port,omitempty"`
	Protocol    string `json:"Protocol,omitempty"`
}

// Condition is a rule's condition. Raw keeps its JSON, as Node's
// JSON.stringify gives it for a field the scan does not know.
type Condition struct {
	Field             string
	Values            []string
	HostHeaderConfig  *ValuesConfig
	PathPatternConfig *ValuesConfig
	Raw               string
}

// ValuesConfig holds the values of a host or path condition.
type ValuesConfig struct {
	Values []string `json:"Values,omitempty"`
}

// UnmarshalJSON reads a condition and keeps its JSON.
func (c *Condition) UnmarshalJSON(b []byte) error {
	type plain struct {
		Field             string        `json:"Field"`
		Values            []string      `json:"Values"`
		HostHeaderConfig  *ValuesConfig `json:"HostHeaderConfig"`
		PathPatternConfig *ValuesConfig `json:"PathPatternConfig"`
	}
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = Condition{Field: p.Field, Values: p.Values, HostHeaderConfig: p.HostHeaderConfig, PathPatternConfig: p.PathPatternConfig, Raw: string(b)}
	return nil
}

// MarshalJSON writes the condition as it was read.
func (c Condition) MarshalJSON() ([]byte, error) {
	if c.Raw != "" {
		return []byte(c.Raw), nil
	}
	type plain struct {
		Field             string        `json:"Field,omitempty"`
		Values            []string      `json:"Values,omitempty"`
		HostHeaderConfig  *ValuesConfig `json:"HostHeaderConfig,omitempty"`
		PathPatternConfig *ValuesConfig `json:"PathPatternConfig,omitempty"`
	}
	return json.Marshal(plain{c.Field, c.Values, c.HostHeaderConfig, c.PathPatternConfig})
}

// ForwardTarget is a target group of a forward action, with its weight (absent: 1).
type ForwardTarget struct {
	TargetGroupArn string `json:"TargetGroupArn,omitempty"`
	Weight         *int   `json:"Weight,omitempty"`
}

// ForwardConfig is a forward action's target groups (nil: the action's own).
type ForwardConfig struct {
	TargetGroups []ForwardTarget `json:"TargetGroups,omitempty"`
}

// Action is a rule's action.
type Action struct {
	Type           string         `json:"Type,omitempty"`
	Order          int            `json:"Order,omitempty"`
	TargetGroupArn string         `json:"TargetGroupArn,omitempty"`
	ForwardConfig  *ForwardConfig `json:"ForwardConfig,omitempty"`
}

// Rule is an ELBv2 listener rule.
type Rule struct {
	RuleArn    string      `json:"RuleArn,omitempty"`
	Priority   string      `json:"Priority,omitempty"`
	IsDefault  bool        `json:"IsDefault,omitempty"`
	Conditions []Condition `json:"Conditions,omitempty"`
	Actions    []Action    `json:"Actions,omitempty"`
}

// TargetGroup is an ELBv2 target group.
type TargetGroup struct {
	TargetGroupArn   string   `json:"TargetGroupArn,omitempty"`
	TargetGroupName  string   `json:"TargetGroupName,omitempty"`
	TargetType       string   `json:"TargetType,omitempty"`
	LoadBalancerArns []string `json:"LoadBalancerArns,omitempty"`
	VpcId            string   `json:"VpcId,omitempty"`
}

// DescribeLoadBalancersInput is a page of load balancers.
type DescribeLoadBalancersInput struct {
	Marker string `json:"Marker,omitempty"`
}

// DescribeLoadBalancersOutput is its answer.
type DescribeLoadBalancersOutput struct {
	LoadBalancers []LoadBalancer `json:"LoadBalancers,omitempty"`
	NextMarker    string         `json:"NextMarker,omitempty"`
}

// DescribeListenersInput is a page of a load balancer's listeners.
type DescribeListenersInput struct {
	LoadBalancerArn string `json:"LoadBalancerArn"`
	Marker          string `json:"Marker,omitempty"`
}

// DescribeListenersOutput is its answer.
type DescribeListenersOutput struct {
	Listeners  []Listener `json:"Listeners,omitempty"`
	NextMarker string     `json:"NextMarker,omitempty"`
}

// DescribeRulesInput is a page of a listener's rules.
type DescribeRulesInput struct {
	ListenerArn string   `json:"ListenerArn,omitempty"`
	Marker      string   `json:"Marker,omitempty"`
	RuleArns    []string `json:"RuleArns,omitempty"` // rules by ARN (a managed workload's waker)
}

// DescribeRulesOutput is its answer.
type DescribeRulesOutput struct {
	Rules      []Rule `json:"Rules,omitempty"`
	NextMarker string `json:"NextMarker,omitempty"`
}

// DescribeTagsInput asks the tags of up to 20 resources.
type DescribeTagsInput struct {
	ResourceArns []string `json:"ResourceArns"`
}

// TagDescription is the tags of a resource.
type TagDescription struct {
	ResourceArn string `json:"ResourceArn,omitempty"`
	Tags        []Tag  `json:"Tags,omitempty"`
}

// DescribeTagsOutput is its answer.
type DescribeTagsOutput struct {
	TagDescriptions []TagDescription `json:"TagDescriptions,omitempty"`
}

// DescribeTargetGroupsInput is a page of target groups.
type DescribeTargetGroupsInput struct {
	Marker          string   `json:"Marker,omitempty"`
	TargetGroupArns []string `json:"TargetGroupArns,omitempty"`
}

// DescribeTargetGroupsOutput is its answer.
type DescribeTargetGroupsOutput struct {
	TargetGroups []TargetGroup `json:"TargetGroups,omitempty"`
	NextMarker   string        `json:"NextMarker,omitempty"`
}

// DescribeTargetHealthInput asks a target group's targets.
type DescribeTargetHealthInput struct {
	TargetGroupArn string `json:"TargetGroupArn"`
}

// TargetDescription is a registered target.
type TargetDescription struct {
	ID string `json:"Id,omitempty"`
}

// TargetHealthDescription is a target and its health.
type TargetHealthDescription struct {
	Target TargetDescription `json:"Target"`
}

// DescribeTargetHealthOutput is its answer.
type DescribeTargetHealthOutput struct {
	TargetHealthDescriptions []TargetHealthDescription `json:"TargetHealthDescriptions,omitempty"`
}

// ELBv2Port is the load balancers API.
type ELBv2Port interface {
	DescribeLoadBalancers(ctx context.Context, in DescribeLoadBalancersInput) (DescribeLoadBalancersOutput, error)
	DescribeListeners(ctx context.Context, in DescribeListenersInput) (DescribeListenersOutput, error)
	DescribeRules(ctx context.Context, in DescribeRulesInput) (DescribeRulesOutput, error)
	DescribeTags(ctx context.Context, in DescribeTagsInput) (DescribeTagsOutput, error)
	DescribeTargetGroups(ctx context.Context, in DescribeTargetGroupsInput) (DescribeTargetGroupsOutput, error)
	DescribeTargetHealth(ctx context.Context, in DescribeTargetHealthInput) (DescribeTargetHealthOutput, error)
}

// DescribeAutoScalingGroupsInput is a page of Auto Scaling groups.
type DescribeAutoScalingGroupsInput struct {
	NextToken             string   `json:"NextToken,omitempty"`
	AutoScalingGroupNames []string `json:"AutoScalingGroupNames,omitempty"`
}

// ASGInstance is an instance of an Auto Scaling group.
type ASGInstance struct {
	InstanceId string `json:"InstanceId,omitempty"`
}

// AutoScalingGroup is the part of a group the scan reads.
type AutoScalingGroup struct {
	AutoScalingGroupName string        `json:"AutoScalingGroupName,omitempty"`
	AutoScalingGroupARN  string        `json:"AutoScalingGroupARN,omitempty"`
	TargetGroupARNs      []string      `json:"TargetGroupARNs,omitempty"`
	Tags                 []Tag         `json:"Tags,omitempty"`
	DesiredCapacity      int           `json:"DesiredCapacity,omitempty"`
	Instances            []ASGInstance `json:"Instances,omitempty"`
}

// DescribeAutoScalingGroupsOutput is its answer.
type DescribeAutoScalingGroupsOutput struct {
	AutoScalingGroups []AutoScalingGroup `json:"AutoScalingGroups,omitempty"`
	NextToken         string             `json:"NextToken,omitempty"`
}

// AutoScalingPort is the Auto Scaling API.
type AutoScalingPort interface {
	DescribeAutoScalingGroups(ctx context.Context, in DescribeAutoScalingGroupsInput) (DescribeAutoScalingGroupsOutput, error)
}

// DescribeInstancesInput asks instances by id, or, without ids, a page of every instance of the region (the
// standalone scan).
type DescribeInstancesInput struct {
	InstanceIds []string `json:"InstanceIds,omitempty"`
	NextToken   string   `json:"NextToken,omitempty"`
}

// InstanceState is an instance's state.
type InstanceState struct {
	Name string `json:"Name,omitempty"`
}

// Instance is an instance with its tags, and what the standalone scan reads of it.
type Instance struct {
	InstanceID        string         `json:"InstanceId,omitempty"`
	State             *InstanceState `json:"State,omitempty"`
	RootDeviceType    string         `json:"RootDeviceType,omitempty"`
	InstanceLifecycle string         `json:"InstanceLifecycle,omitempty"`
	Tags              []Tag          `json:"Tags,omitempty"`
}

// Reservation groups instances.
type Reservation struct {
	Instances []Instance `json:"Instances,omitempty"`
}

// DescribeInstancesOutput is its answer.
type DescribeInstancesOutput struct {
	Reservations []Reservation `json:"Reservations,omitempty"`
	NextToken    string        `json:"NextToken,omitempty"`
}

// EC2Port is read where the region has EKS clusters (their nodes), and by the standalone scan (every instance).
type EC2Port interface {
	DescribeInstances(ctx context.Context, in DescribeInstancesInput) (DescribeInstancesOutput, error)
}

// DescribeSecurityGroupsInput asks security groups by id.
type DescribeSecurityGroupsInput struct {
	GroupIds []string `json:"GroupIds"`
}

// UserIdGroupPair is a security group a rule names.
type UserIdGroupPair struct {
	GroupId string `json:"GroupId,omitempty"`
}

// IpPermission is an ingress rule.
type IpPermission struct {
	UserIdGroupPairs []UserIdGroupPair `json:"UserIdGroupPairs,omitempty"`
}

// SecurityGroup is a security group and its ingress rules.
type SecurityGroup struct {
	GroupId       string         `json:"GroupId,omitempty"`
	IpPermissions []IpPermission `json:"IpPermissions,omitempty"`
}

// DescribeSecurityGroupsOutput is its answer.
type DescribeSecurityGroupsOutput struct {
	SecurityGroups []SecurityGroup `json:"SecurityGroups,omitempty"`
}

// RegionEC2Port is what a region's discovery reads of EC2: instances (with
// their state and security groups) and security groups.
type RegionEC2Port interface {
	stopped.EC2Port
	DescribeSecurityGroups(ctx context.Context, in DescribeSecurityGroupsInput) (DescribeSecurityGroupsOutput, error)
}

// RulesPort is the ELBv2 call discovery makes beyond the scan: rules by ARN.
type RulesPort interface {
	DescribeRules(ctx context.Context, in DescribeRulesInput) (DescribeRulesOutput, error)
}

// RegionPorts are what one region's discovery reads (Node's RegionPorts, which
// extends StoppedPorts): the scan, the Auto Scaling groups, a rule by ARN, EC2
// (instances, security groups) and what a nap stops (ECS services, RDS
// instances and clusters, the clusters' clients). Stopped.RDS nil: the
// databases are not read. Stopped.EC2 and EC2 are the same client.
type RegionPorts struct {
	// Scan is discovery's scan of the region (ScanRegion with the region's
	// ports); its clusters the profile could not read are listed by Region.
	Scan        func(ctx context.Context) (ScanResult, error)
	AutoScaling AutoScalingPort
	ELBv2       RulesPort
	EC2         RegionEC2Port
	Stopped     stopped.Ports
}

// ListClustersInput is a page of ECS clusters.
type ListClustersInput struct {
	NextToken string `json:"nextToken,omitempty"`
}

// ListClustersOutput is its answer.
type ListClustersOutput struct {
	ClusterArns []string `json:"clusterArns,omitempty"`
	NextToken   string   `json:"nextToken,omitempty"`
}

// ListServicesInput is a page of a cluster's services.
type ListServicesInput struct {
	Cluster   string `json:"cluster"`
	NextToken string `json:"nextToken,omitempty"`
}

// ListServicesOutput is its answer.
type ListServicesOutput struct {
	ServiceArns []string `json:"serviceArns,omitempty"`
	NextToken   string   `json:"nextToken,omitempty"`
}

// DescribeServicesInput asks up to 10 services of a cluster; Include ["TAGS"] asks their tags too (the standalone
// scan).
type DescribeServicesInput struct {
	Cluster  string   `json:"cluster"`
	Services []string `json:"services"`
	Include  []string `json:"include,omitempty"`
}

// ServiceLoadBalancer is a service's load balancer attachment.
type ServiceLoadBalancer struct {
	TargetGroupArn string `json:"targetGroupArn,omitempty"`
}

// ServiceTag is an ECS tag (camelCase, as ECS answers).
type ServiceTag struct {
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
}

// Service is the part of an ECS service the scan reads.
type Service struct {
	ServiceArn         string                `json:"serviceArn,omitempty"`
	Status             string                `json:"status,omitempty"`
	SchedulingStrategy string                `json:"schedulingStrategy,omitempty"`
	LoadBalancers      []ServiceLoadBalancer `json:"loadBalancers,omitempty"`
	Tags               []ServiceTag          `json:"tags,omitempty"`
}

// DescribeServicesOutput is its answer.
type DescribeServicesOutput struct {
	Services []Service `json:"services,omitempty"`
}

// ECSPort is the ECS API.
type ECSPort interface {
	ListClusters(ctx context.Context, in ListClustersInput) (ListClustersOutput, error)
	ListServices(ctx context.Context, in ListServicesInput) (ListServicesOutput, error)
	DescribeServices(ctx context.Context, in DescribeServicesInput) (DescribeServicesOutput, error)
}

// Dimension is a CloudWatch metric dimension.
type Dimension struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

// Metric names a CloudWatch metric.
type Metric struct {
	Namespace  string      `json:"Namespace"`
	MetricName string      `json:"MetricName"`
	Dimensions []Dimension `json:"Dimensions"`
}

// MetricStat is a metric with its period and statistic.
type MetricStat struct {
	Metric Metric `json:"Metric"`
	Period int    `json:"Period"`
	Stat   string `json:"Stat"`
}

// MetricDataQuery is one query of GetMetricData.
type MetricDataQuery struct {
	ID         string     `json:"Id"`
	ReturnData bool       `json:"ReturnData"`
	MetricStat MetricStat `json:"MetricStat"`
}

// GetMetricDataInput is a GetMetricData call. Its times are written as Node's
// toISOString does (2026-10-12T08:00:00.000Z). ScanBy "TimestampAscending":
// the oldest values first (the standalone idle share's).
type GetMetricDataInput struct {
	StartTime         time.Time
	EndTime           time.Time
	MetricDataQueries []MetricDataQuery
	ScanBy            string
	NextToken         string
}

const isoMillis = "2006-01-02T15:04:05.000Z"

// MarshalJSON writes the input with its times as ISO strings.
func (in GetMetricDataInput) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		StartTime         string            `json:"StartTime"`
		EndTime           string            `json:"EndTime"`
		MetricDataQueries []MetricDataQuery `json:"MetricDataQueries"`
		ScanBy            string            `json:"ScanBy,omitempty"`
		NextToken         string            `json:"NextToken,omitempty"`
	}{in.StartTime.UTC().Format(isoMillis), in.EndTime.UTC().Format(isoMillis), in.MetricDataQueries, in.ScanBy, in.NextToken})
}

// MetricMessage is a message of a result (why CloudWatch could not read a metric).
type MetricMessage struct {
	Code  string `json:"Code,omitempty"`
	Value string `json:"Value,omitempty"`
}

// MetricDataResult is the values of one query, with their timestamps side by
// side, and its status (a result CloudWatch could not read has no values).
type MetricDataResult struct {
	ID         string          `json:"Id,omitempty"`
	Timestamps []time.Time     `json:"Timestamps,omitempty"`
	Values     []float64       `json:"Values,omitempty"`
	StatusCode string          `json:"StatusCode,omitempty"`
	Messages   []MetricMessage `json:"Messages,omitempty"`
}

// GetMetricDataOutput is its answer.
type GetMetricDataOutput struct {
	MetricDataResults []MetricDataResult `json:"MetricDataResults,omitempty"`
	NextToken         string             `json:"NextToken,omitempty"`
}

// CloudWatchPort is the metrics API.
type CloudWatchPort interface {
	GetMetricData(ctx context.Context, in GetMetricDataInput) (GetMetricDataOutput, error)
}

// ScanPorts are the read-only calls of a scan. ELBv2 is required; a port left
// nil is one the scan does not read (no Auto Scaling groups, ECS services,
// idle hours, nodes or clusters; for the standalone scan, no instances or
// databases). Kubernetes is the region's EKS clusters and their API. RDS is
// read by the standalone scan only.
type ScanPorts struct {
	ELBv2       ELBv2Port
	AutoScaling AutoScalingPort
	EC2         EC2Port
	ECS         ECSPort
	CloudWatch  CloudWatchPort
	RDS         stopped.RDSPort
	Kubernetes  *kubernetes.Ports
}

// ListRootsInput is a page of the organization's roots.
type ListRootsInput struct {
	NextToken string `json:"NextToken,omitempty"`
}

// Root is an organization root.
type Root struct {
	ID   string `json:"Id,omitempty"`
	Name string `json:"Name,omitempty"`
}

// ListRootsOutput is its answer.
type ListRootsOutput struct {
	Roots     []Root `json:"Roots,omitempty"`
	NextToken string `json:"NextToken,omitempty"`
}

// ListOrganizationalUnitsForParentInput is a page of a parent's OUs.
type ListOrganizationalUnitsForParentInput struct {
	ParentID  string `json:"ParentId"`
	NextToken string `json:"NextToken,omitempty"`
}

// OrganizationalUnit is an OU.
type OrganizationalUnit struct {
	ID   string `json:"Id,omitempty"`
	Name string `json:"Name,omitempty"`
}

// ListOrganizationalUnitsForParentOutput is its answer.
type ListOrganizationalUnitsForParentOutput struct {
	OrganizationalUnits []OrganizationalUnit `json:"OrganizationalUnits,omitempty"`
	NextToken           string               `json:"NextToken,omitempty"`
}

// ListAccountsForParentInput is a page of a parent's accounts.
type ListAccountsForParentInput struct {
	ParentID  string `json:"ParentId"`
	NextToken string `json:"NextToken,omitempty"`
}

// OrgAccount is an account of the organization; State replaces the deprecated Status.
type OrgAccount struct {
	ID     string `json:"Id,omitempty"`
	Name   string `json:"Name,omitempty"`
	State  string `json:"State,omitempty"`
	Status string `json:"Status,omitempty"`
}

// ListAccountsForParentOutput is its answer.
type ListAccountsForParentOutput struct {
	Accounts  []OrgAccount `json:"Accounts,omitempty"`
	NextToken string       `json:"NextToken,omitempty"`
}

// ListTagsForResourceInput is a page of an account's tags.
type ListTagsForResourceInput struct {
	ResourceID string `json:"ResourceId"`
	NextToken  string `json:"NextToken,omitempty"`
}

// ListTagsForResourceOutput is its answer.
type ListTagsForResourceOutput struct {
	Tags      []Tag  `json:"Tags,omitempty"`
	NextToken string `json:"NextToken,omitempty"`
}

// OrganizationsPort is the read-only Organizations delegation.
type OrganizationsPort interface {
	ListRoots(ctx context.Context, in ListRootsInput) (ListRootsOutput, error)
	ListOrganizationalUnitsForParent(ctx context.Context, in ListOrganizationalUnitsForParentInput) (ListOrganizationalUnitsForParentOutput, error)
	ListAccountsForParent(ctx context.Context, in ListAccountsForParentInput) (ListAccountsForParentOutput, error)
	ListTagsForResource(ctx context.Context, in ListTagsForResourceInput) (ListTagsForResourceOutput, error)
}
