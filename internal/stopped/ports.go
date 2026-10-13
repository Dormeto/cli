package stopped

// The ports Describe reads through: the AWS APIs' own shapes (the Node ports'
// JSON, PascalCase for EC2 and RDS, camelCase for ECS), and the client of a
// cluster by name. Ports router lambda/src/lib/stopped.mjs's ports (ec2, ecs,
// rds, kubernetesOf) and manager packages/estimate/src/stopped.ts (StoppedPorts).

import (
	"context"

	"github.com/Dormeto/cli/internal/kubernetes"
)

// DescribeInstancesInput is EC2 DescribeInstances' input (the ids asked).
type DescribeInstancesInput struct {
	InstanceIds []string `json:"InstanceIds"`
}

// DescribeInstancesOutput is its answer.
type DescribeInstancesOutput struct {
	Reservations []Reservation `json:"Reservations,omitempty"`
}

// Reservation groups instances.
type Reservation struct {
	Instances []Instance `json:"Instances,omitempty"`
}

// Instance is the part of an EC2 instance Describe reads.
type Instance struct {
	InstanceID        string `json:"InstanceId,omitempty"`
	InstanceType      string `json:"InstanceType,omitempty"`
	PlatformDetails   string `json:"PlatformDetails,omitempty"`
	InstanceLifecycle string `json:"InstanceLifecycle,omitempty"`
	// State and SecurityGroups: what discovery reads of an instance.
	State          *InstanceState    `json:"State,omitempty"`
	SecurityGroups []GroupIdentifier `json:"SecurityGroups,omitempty"`
}

// InstanceState is an instance's state.
type InstanceState struct {
	Name string `json:"Name,omitempty"`
}

// GroupIdentifier is a security group an instance is in.
type GroupIdentifier struct {
	GroupId string `json:"GroupId,omitempty"`
}

// EC2Port is EC2 (DescribeInstances).
type EC2Port interface {
	DescribeInstances(ctx context.Context, in DescribeInstancesInput) (DescribeInstancesOutput, error)
}

// DescribeServicesInput is ECS DescribeServices' input.
type DescribeServicesInput struct {
	Cluster  string   `json:"cluster"`
	Services []string `json:"services"`
}

// DescribeServicesOutput is its answer.
type DescribeServicesOutput struct {
	Services []Service `json:"services,omitempty"`
}

// CapacityProviderStrategyItem is one entry of a service's strategy.
type CapacityProviderStrategyItem struct {
	CapacityProvider string `json:"capacityProvider"`
	Base             int    `json:"base,omitempty"`
	Weight           int    `json:"weight,omitempty"`
}

// Service is the part of an ECS service Describe reads.
type Service struct {
	ServiceArn               string                         `json:"serviceArn,omitempty"`
	ClusterArn               string                         `json:"clusterArn,omitempty"`
	TaskDefinition           string                         `json:"taskDefinition,omitempty"`
	LaunchType               string                         `json:"launchType,omitempty"`
	DesiredCount             int                            `json:"desiredCount,omitempty"`
	NetworkConfiguration     *NetworkConfiguration          `json:"networkConfiguration,omitempty"`
	CapacityProviderStrategy []CapacityProviderStrategyItem `json:"capacityProviderStrategy,omitempty"`
}

// NetworkConfiguration is a service's network (awsvpc).
type NetworkConfiguration struct {
	AwsvpcConfiguration *AwsvpcConfiguration `json:"awsvpcConfiguration,omitempty"`
}

// AwsvpcConfiguration holds the security groups of an awsvpc service.
type AwsvpcConfiguration struct {
	SecurityGroups []string `json:"securityGroups,omitempty"`
}

// DescribeTaskDefinitionInput is ECS DescribeTaskDefinition's input.
type DescribeTaskDefinitionInput struct {
	TaskDefinition string `json:"taskDefinition"`
}

// DescribeTaskDefinitionOutput is its answer.
type DescribeTaskDefinitionOutput struct {
	TaskDefinition *TaskDefinition `json:"taskDefinition,omitempty"`
}

// RuntimePlatform is a task definition's platform.
type RuntimePlatform struct {
	CPUArchitecture       string `json:"cpuArchitecture,omitempty"`
	OperatingSystemFamily string `json:"operatingSystemFamily,omitempty"`
}

// TaskDefinition is the part of a task definition Describe reads (the task's
// size, as strings: "512", "1024").
type TaskDefinition struct {
	CPU             string           `json:"cpu,omitempty"`
	Memory          string           `json:"memory,omitempty"`
	RuntimePlatform *RuntimePlatform `json:"runtimePlatform,omitempty"`
}

// ECSPort is ECS (DescribeServices, DescribeTaskDefinition).
type ECSPort interface {
	DescribeServices(ctx context.Context, in DescribeServicesInput) (DescribeServicesOutput, error)
	DescribeTaskDefinition(ctx context.Context, in DescribeTaskDefinitionInput) (DescribeTaskDefinitionOutput, error)
}

// Filter is an RDS filter.
type Filter struct {
	Name   string   `json:"Name"`
	Values []string `json:"Values"`
}

// DescribeDBInstancesInput is RDS DescribeDBInstances' input: one instance by
// identifier, or the instances of a cluster by filter.
type DescribeDBInstancesInput struct {
	DBInstanceIdentifier string   `json:"DBInstanceIdentifier,omitempty"`
	Filters              []Filter `json:"Filters,omitempty"`
	Marker               string   `json:"Marker,omitempty"` // the page after it (discovery's databases)
}

// VpcSecurityGroup is a database's security group and its status.
type VpcSecurityGroup struct {
	VpcSecurityGroupId string `json:"VpcSecurityGroupId,omitempty"`
	Status             string `json:"Status,omitempty"`
}

// Tag is a resource tag.
type Tag struct {
	Key   string `json:"Key,omitempty"`
	Value string `json:"Value,omitempty"`
}

// DBInstance is the part of an RDS instance Describe reads.
type DBInstance struct {
	DBInstanceIdentifier string `json:"DBInstanceIdentifier,omitempty"`
	DBInstanceClass      string `json:"DBInstanceClass,omitempty"`
	Engine               string `json:"Engine,omitempty"`
	MultiAZ              bool   `json:"MultiAZ,omitempty"`
	DBClusterIdentifier  string `json:"DBClusterIdentifier,omitempty"`
	// What discovery's databases read.
	DBInstanceStatus                      string             `json:"DBInstanceStatus,omitempty"`
	ReadReplicaSourceDBInstanceIdentifier string             `json:"ReadReplicaSourceDBInstanceIdentifier,omitempty"`
	ReadReplicaDBInstanceIdentifiers      []string           `json:"ReadReplicaDBInstanceIdentifiers,omitempty"`
	VpcSecurityGroups                     []VpcSecurityGroup `json:"VpcSecurityGroups,omitempty"`
	TagList                               []Tag              `json:"TagList,omitempty"`
}

// DescribeDBInstancesOutput is its answer.
type DescribeDBInstancesOutput struct {
	DBInstances []DBInstance `json:"DBInstances,omitempty"`
	Marker      string       `json:"Marker,omitempty"`
}

// DescribeDBClustersInput is RDS DescribeDBClusters' input.
type DescribeDBClustersInput struct {
	DBClusterIdentifier string `json:"DBClusterIdentifier,omitempty"`
	Marker              string `json:"Marker,omitempty"`
}

// ServerlessV2Scaling is an Aurora Serverless v2 cluster's scaling range.
type ServerlessV2Scaling struct {
	MinCapacity *float64 `json:"MinCapacity,omitempty"`
	MaxCapacity *float64 `json:"MaxCapacity,omitempty"`
}

// DBCluster is the part of an Aurora cluster Describe reads.
type DBCluster struct {
	DBClusterIdentifier              string               `json:"DBClusterIdentifier,omitempty"`
	Engine                           string               `json:"Engine,omitempty"`
	ServerlessV2ScalingConfiguration *ServerlessV2Scaling `json:"ServerlessV2ScalingConfiguration,omitempty"`
	// What discovery's databases read.
	Status            string             `json:"Status,omitempty"`
	EngineMode        string             `json:"EngineMode,omitempty"`
	VpcSecurityGroups []VpcSecurityGroup `json:"VpcSecurityGroups,omitempty"`
	TagList           []Tag              `json:"TagList,omitempty"`
}

// DescribeDBClustersOutput is its answer.
type DescribeDBClustersOutput struct {
	DBClusters []DBCluster `json:"DBClusters,omitempty"`
	Marker     string      `json:"Marker,omitempty"`
}

// RDSPort is RDS (DescribeDBInstances, DescribeDBClusters).
type RDSPort interface {
	DescribeDBInstances(ctx context.Context, in DescribeDBInstancesInput) (DescribeDBInstancesOutput, error)
	DescribeDBClusters(ctx context.Context, in DescribeDBClustersInput) (DescribeDBClustersOutput, error)
}

// Ports are what Describe reads through. A port a backend kind does not need
// may be nil. KubernetesOf gives the client of a cluster by name.
type Ports struct {
	EC2          EC2Port
	ECS          ECSPort
	RDS          RDSPort
	KubernetesOf func(ctx context.Context, cluster string) (kubernetes.Client, error)
}
