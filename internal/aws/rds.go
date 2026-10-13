package aws

// RDS: the instances and Aurora clusters a nap stops (stopped.RDSPort), and the
// region's databases discovery reads, paged (discover.DatabasesPort). Ports
// manager packages/estimate/src/bin.ts (portsOf's rds) and
// packages/estimate/src/discover.ts (databasesOf's calls).

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/stopped"
)

type rdsAPI interface {
	DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
	DescribeDBClusters(context.Context, *rds.DescribeDBClustersInput, ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error)
}

type rdsPort struct{ client rdsAPI }

func (p rdsPort) DescribeDBInstances(ctx context.Context, in stopped.DescribeDBInstancesInput) (stopped.DescribeDBInstancesOutput, error) {
	ask := &rds.DescribeDBInstancesInput{DBInstanceIdentifier: optional(in.DBInstanceIdentifier)}
	for _, f := range in.Filters {
		ask.Filters = append(ask.Filters, rdstypes.Filter{Name: awssdk.String(f.Name), Values: f.Values})
	}
	out, err := p.client.DescribeDBInstances(ctx, ask)
	if err != nil {
		return stopped.DescribeDBInstancesOutput{}, err
	}
	var res stopped.DescribeDBInstancesOutput
	for _, d := range out.DBInstances {
		res.DBInstances = append(res.DBInstances, stopped.DBInstance{
			DBInstanceIdentifier: awssdk.ToString(d.DBInstanceIdentifier), DBInstanceClass: awssdk.ToString(d.DBInstanceClass),
			Engine: awssdk.ToString(d.Engine), MultiAZ: awssdk.ToBool(d.MultiAZ), DBClusterIdentifier: awssdk.ToString(d.DBClusterIdentifier),
		})
	}
	return res, nil
}

func (p rdsPort) DescribeDBClusters(ctx context.Context, in stopped.DescribeDBClustersInput) (stopped.DescribeDBClustersOutput, error) {
	out, err := p.client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: optional(in.DBClusterIdentifier)})
	if err != nil {
		return stopped.DescribeDBClustersOutput{}, err
	}
	var res stopped.DescribeDBClustersOutput
	for _, c := range out.DBClusters {
		cluster := stopped.DBCluster{DBClusterIdentifier: awssdk.ToString(c.DBClusterIdentifier), Engine: awssdk.ToString(c.Engine)}
		if s := c.ServerlessV2ScalingConfiguration; s != nil {
			cluster.ServerlessV2ScalingConfiguration = &stopped.ServerlessV2Scaling{MinCapacity: s.MinCapacity, MaxCapacity: s.MaxCapacity}
		}
		res.DBClusters = append(res.DBClusters, cluster)
	}
	return res, nil
}

// databasesPort is discover.DatabasesPort: DescribeDBInstances and
// DescribeDBClusters a page at a time (the marker "" asks the first page).
type databasesPort struct{ client rdsAPI }

func groupsOf(in []rdstypes.VpcSecurityGroupMembership) []discover.VpcSecurityGroup {
	var out []discover.VpcSecurityGroup
	for _, g := range in {
		out = append(out, discover.VpcSecurityGroup{VpcSecurityGroupId: awssdk.ToString(g.VpcSecurityGroupId), Status: awssdk.ToString(g.Status)})
	}
	return out
}

func tagList(in []rdstypes.Tag) []discover.Tag {
	var out []discover.Tag
	for _, t := range in {
		out = append(out, discover.Tag{Key: awssdk.ToString(t.Key), Value: awssdk.ToString(t.Value)})
	}
	return out
}

func (p databasesPort) DescribeDBInstances(ctx context.Context, marker string) ([]discover.DBInstance, string, error) {
	out, err := p.client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{Marker: optional(marker)})
	if err != nil {
		return nil, "", err
	}
	var res []discover.DBInstance
	for _, d := range out.DBInstances {
		res = append(res, discover.DBInstance{
			DBInstanceIdentifier: awssdk.ToString(d.DBInstanceIdentifier), DBInstanceStatus: awssdk.ToString(d.DBInstanceStatus), Engine: awssdk.ToString(d.Engine),
			DBInstanceClass: awssdk.ToString(d.DBInstanceClass), MultiAZ: awssdk.ToBool(d.MultiAZ), DBClusterIdentifier: awssdk.ToString(d.DBClusterIdentifier),
			ReadReplicaSourceDBInstanceIdentifier: awssdk.ToString(d.ReadReplicaSourceDBInstanceIdentifier),
			ReadReplicaDBInstanceIdentifiers:      d.ReadReplicaDBInstanceIdentifiers, VpcSecurityGroups: groupsOf(d.VpcSecurityGroups), TagList: tagList(d.TagList),
		})
	}
	return res, awssdk.ToString(out.Marker), nil
}

func (p databasesPort) DescribeDBClusters(ctx context.Context, marker string) ([]discover.DBCluster, string, error) {
	out, err := p.client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{Marker: optional(marker)})
	if err != nil {
		return nil, "", err
	}
	var res []discover.DBCluster
	for _, c := range out.DBClusters {
		res = append(res, discover.DBCluster{
			DBClusterIdentifier: awssdk.ToString(c.DBClusterIdentifier), Status: awssdk.ToString(c.Status), Engine: awssdk.ToString(c.Engine),
			EngineMode: awssdk.ToString(c.EngineMode), VpcSecurityGroups: groupsOf(c.VpcSecurityGroups), TagList: tagList(c.TagList),
		})
	}
	return res, awssdk.ToString(out.Marker), nil
}
