package aws

// RDS: the instances and Aurora clusters a nap stops and the region's databases
// discovery reads (the standalone scan's too), paged by Marker (stopped.RDSPort). Ports
// manager packages/estimate/src/bin.ts (portsOf's rds) and
// packages/estimate/src/discover.ts (databasesOf's calls).

import (
	"context"
	"sync"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/Dormeto/cli/internal/stopped"
)

type rdsAPI interface {
	DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
	DescribeDBClusters(context.Context, *rds.DescribeDBClustersInput, ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error)
}

type rdsPort struct{ client rdsAPI }

func (p rdsPort) DescribeDBInstances(ctx context.Context, in stopped.DescribeDBInstancesInput) (stopped.DescribeDBInstancesOutput, error) {
	ask := &rds.DescribeDBInstancesInput{DBInstanceIdentifier: optional(in.DBInstanceIdentifier), Marker: optional(in.Marker)}
	for _, f := range in.Filters {
		ask.Filters = append(ask.Filters, rdstypes.Filter{Name: awssdk.String(f.Name), Values: f.Values})
	}
	out, err := p.client.DescribeDBInstances(ctx, ask)
	if err != nil {
		return stopped.DescribeDBInstancesOutput{}, err
	}
	res := stopped.DescribeDBInstancesOutput{Marker: awssdk.ToString(out.Marker)}
	for _, d := range out.DBInstances {
		res.DBInstances = append(res.DBInstances, stopped.DBInstance{
			DBInstanceIdentifier: awssdk.ToString(d.DBInstanceIdentifier), DBInstanceArn: awssdk.ToString(d.DBInstanceArn), DBInstanceClass: awssdk.ToString(d.DBInstanceClass),
			Engine: awssdk.ToString(d.Engine), MultiAZ: awssdk.ToBool(d.MultiAZ), DBClusterIdentifier: awssdk.ToString(d.DBClusterIdentifier),
			DBInstanceStatus: awssdk.ToString(d.DBInstanceStatus), ReadReplicaSourceDBInstanceIdentifier: awssdk.ToString(d.ReadReplicaSourceDBInstanceIdentifier),
			ReadReplicaDBInstanceIdentifiers: d.ReadReplicaDBInstanceIdentifiers, VpcSecurityGroups: groupsOf(d.VpcSecurityGroups), TagList: tagList(d.TagList),
		})
	}
	return res, nil
}

func (p rdsPort) DescribeDBClusters(ctx context.Context, in stopped.DescribeDBClustersInput) (stopped.DescribeDBClustersOutput, error) {
	out, err := p.client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: optional(in.DBClusterIdentifier), Marker: optional(in.Marker)})
	if err != nil {
		return stopped.DescribeDBClustersOutput{}, err
	}
	res := stopped.DescribeDBClustersOutput{Marker: awssdk.ToString(out.Marker)}
	for _, c := range out.DBClusters {
		cluster := stopped.DBCluster{
			DBClusterIdentifier: awssdk.ToString(c.DBClusterIdentifier), DBClusterArn: awssdk.ToString(c.DBClusterArn), Engine: awssdk.ToString(c.Engine), Status: awssdk.ToString(c.Status),
			EngineMode: awssdk.ToString(c.EngineMode), VpcSecurityGroups: groupsOf(c.VpcSecurityGroups), TagList: tagList(c.TagList),
		}
		if s := c.ServerlessV2ScalingConfiguration; s != nil {
			cluster.ServerlessV2ScalingConfiguration = &stopped.ServerlessV2Scaling{MinCapacity: s.MinCapacity, MaxCapacity: s.MaxCapacity}
		}
		res.DBClusters = append(res.DBClusters, cluster)
	}
	return res, nil
}

func groupsOf(in []rdstypes.VpcSecurityGroupMembership) []stopped.VpcSecurityGroup {
	var out []stopped.VpcSecurityGroup
	for _, g := range in {
		out = append(out, stopped.VpcSecurityGroup{VpcSecurityGroupId: awssdk.ToString(g.VpcSecurityGroupId), Status: awssdk.ToString(g.Status)})
	}
	return out
}

func tagList(in []rdstypes.Tag) []stopped.Tag {
	var out []stopped.Tag
	for _, t := range in {
		out = append(out, stopped.Tag{Key: awssdk.ToString(t.Key), Value: awssdk.ToString(t.Value)})
	}
	return out
}

// listedRDS is a region's RDS whose unfiltered listings (the pages of every database, by their marker) are asked once:
// the standalone scan and the companions list the same databases in one region's discovery. A describe by identifier or
// by filter is asked each time.
type listedRDS struct {
	port      rdsPort
	mu        sync.Mutex
	instances map[string]stopped.DescribeDBInstancesOutput
	clusters  map[string]stopped.DescribeDBClustersOutput
}

func newListedRDS(port rdsPort) *listedRDS {
	return &listedRDS{port: port, instances: map[string]stopped.DescribeDBInstancesOutput{}, clusters: map[string]stopped.DescribeDBClustersOutput{}}
}

func (l *listedRDS) DescribeDBInstances(ctx context.Context, in stopped.DescribeDBInstancesInput) (stopped.DescribeDBInstancesOutput, error) {
	if in.DBInstanceIdentifier != "" || len(in.Filters) > 0 {
		return l.port.DescribeDBInstances(ctx, in)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if out, ok := l.instances[in.Marker]; ok {
		return out, nil
	}
	out, err := l.port.DescribeDBInstances(ctx, in)
	if err == nil {
		l.instances[in.Marker] = out
	}
	return out, err
}

func (l *listedRDS) DescribeDBClusters(ctx context.Context, in stopped.DescribeDBClustersInput) (stopped.DescribeDBClustersOutput, error) {
	if in.DBClusterIdentifier != "" {
		return l.port.DescribeDBClusters(ctx, in)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if out, ok := l.clusters[in.Marker]; ok {
		return out, nil
	}
	out, err := l.port.DescribeDBClusters(ctx, in)
	if err == nil {
		l.clusters[in.Marker] = out
	}
	return out, err
}
