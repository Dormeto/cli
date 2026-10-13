package aws

// RDS: the instances and Aurora clusters a nap stops (stopped.RDSPort). Ports
// manager packages/estimate/src/bin.ts (portsOf's rds).

import (
	"context"

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
