package discover

// The ports a region's discovery reads through, beyond the scan's: what a
// workload's backend is now (an Auto Scaling group's desired capacity and
// instances, an ECS service's desired count, an instance's state), the
// security groups that say which databases are companions, and the region's RDS
// instances and clusters. Domain-level methods over the AWS APIs' answers; the
// adapters are in internal/aws.
//
// Ports manager packages/estimate/src/discover.ts (RegionPorts).

import (
	"context"

	"github.com/Dormeto/cli/internal/stopped"
)

// Group is an Auto Scaling group as a nap would stop it.
type Group struct {
	Desired   int
	Instances []string
}

// RegionAutoScaling reads an Auto Scaling group (DescribeAutoScalingGroups).
type RegionAutoScaling interface {
	// Group is the named group, nil when AWS knows none.
	Group(ctx context.Context, name string) (*Group, error)
}

// RuleDescriber reads listener rules by ARN (ELBv2 DescribeRules).
type RuleDescriber interface {
	DescribeRules(ctx context.Context, ruleArns []string) ([]Rule, error)
}

// InstanceInfo is an EC2 instance's state ("" when AWS said none) and security groups.
type InstanceInfo struct {
	ID             string
	State          string
	SecurityGroups []string
}

// SecurityGroup is a security group and the groups its rules let in (rules
// naming a security group; address ranges are not followed).
type SecurityGroup struct {
	ID          string
	AllowedFrom []string
}

// RegionEC2 reads instances and security groups (DescribeInstances,
// DescribeSecurityGroups). Instances answers the instances of all reservations
// in order.
type RegionEC2 interface {
	Instances(ctx context.Context, ids []string) ([]InstanceInfo, error)
	SecurityGroups(ctx context.Context, ids []string) ([]SecurityGroup, error)
}

// ServiceInfo is an ECS service's desired count and its awsvpc security groups.
type ServiceInfo struct {
	DesiredCount   int
	SecurityGroups []string
}

// RegionECS reads an ECS service (DescribeServices).
type RegionECS interface {
	// Service is the service of the cluster, nil when AWS knows none.
	Service(ctx context.Context, cluster, service string) (*ServiceInfo, error)
}

// VpcSecurityGroup is a security group of a database and its status.
type VpcSecurityGroup struct {
	VpcSecurityGroupId string `json:"VpcSecurityGroupId,omitempty"`
	Status             string `json:"Status,omitempty"`
}

// DBInstance is the part of an RDS instance the databases are read from.
type DBInstance struct {
	DBInstanceIdentifier                  string
	DBInstanceStatus                      string
	Engine                                string
	DBInstanceClass                       string
	MultiAZ                               bool
	DBClusterIdentifier                   string
	ReadReplicaSourceDBInstanceIdentifier string
	ReadReplicaDBInstanceIdentifiers      []string
	VpcSecurityGroups                     []VpcSecurityGroup
	TagList                               []Tag
}

// DBCluster is the part of an RDS cluster the databases are read from.
type DBCluster struct {
	DBClusterIdentifier string
	Status              string
	Engine              string
	EngineMode          string
	VpcSecurityGroups   []VpcSecurityGroup
	TagList             []Tag
}

// DatabasesPort pages through the region's RDS instances and clusters
// (DescribeDBInstances, DescribeDBClusters; the marker "" asks the first page,
// the marker returned "" ends the pages).
type DatabasesPort interface {
	DescribeDBInstances(ctx context.Context, marker string) (instances []DBInstance, next string, err error)
	DescribeDBClusters(ctx context.Context, marker string) (clusters []DBCluster, next string, err error)
}

// RegionPorts are what one region's discovery reads. RDS nil: the databases
// are not read.
type RegionPorts struct {
	// Scan is discovery's scan of the region (ScanRegion with the region's
	// ports); its clusters the profile could not read are listed by Region.
	Scan        func(ctx context.Context) (ScanResult, error)
	AutoScaling RegionAutoScaling
	ELBv2       RuleDescriber
	EC2         RegionEC2
	ECS         RegionECS
	RDS         DatabasesPort
	// Stopped is what describes the backends (the router's description).
	Stopped stopped.Ports
}
