package aws

import (
	"context"
	"reflect"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/Dormeto/cli/internal/stopped"
)

type fakeRDS struct{ asked []any }

func (f *fakeRDS) DescribeDBInstances(_ context.Context, in *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	f.asked = append(f.asked, in)
	return &rds.DescribeDBInstancesOutput{DBInstances: []rdstypes.DBInstance{{
		DBInstanceIdentifier: awssdk.String("db-1"), DBInstanceClass: awssdk.String("db.t4g.medium"), Engine: awssdk.String("aurora-postgresql"),
		MultiAZ: awssdk.Bool(false), DBClusterIdentifier: awssdk.String("cluster-1"),
	}}}, nil
}

func (f *fakeRDS) DescribeDBClusters(_ context.Context, in *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	f.asked = append(f.asked, in)
	return &rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{{
		DBClusterIdentifier: awssdk.String("cluster-1"), Engine: awssdk.String("aurora-postgresql"),
		ServerlessV2ScalingConfiguration: &rdstypes.ServerlessV2ScalingConfigurationInfo{MinCapacity: awssdk.Float64(0.5), MaxCapacity: awssdk.Float64(4)},
	}}}, nil
}

func TestTheRDSAdapterMapsTheInstancesAndClusters(t *testing.T) {
	f := &fakeRDS{}
	p := rdsPort{f}
	ctx := context.Background()
	byFilter, _ := p.DescribeDBInstances(ctx, stopped.DescribeDBInstancesInput{Filters: []stopped.Filter{{Name: "db-cluster-id", Values: []string{"cluster-1"}}}})
	clusters, _ := p.DescribeDBClusters(ctx, stopped.DescribeDBClustersInput{DBClusterIdentifier: "cluster-1"})
	_, _ = p.DescribeDBInstances(ctx, stopped.DescribeDBInstancesInput{DBInstanceIdentifier: "db-1"})
	eqJSON(t, "instances", byFilter, `{"DBInstances":[{"DBInstanceIdentifier":"db-1","DBInstanceClass":"db.t4g.medium","Engine":"aurora-postgresql","DBClusterIdentifier":"cluster-1"}]}`)
	eqJSON(t, "clusters", clusters, `{"DBClusters":[{"DBClusterIdentifier":"cluster-1","Engine":"aurora-postgresql","ServerlessV2ScalingConfiguration":{"MinCapacity":0.5,"MaxCapacity":4}}]}`)
	want := []any{
		&rds.DescribeDBInstancesInput{Filters: []rdstypes.Filter{{Name: awssdk.String("db-cluster-id"), Values: []string{"cluster-1"}}}},
		&rds.DescribeDBClustersInput{DBClusterIdentifier: awssdk.String("cluster-1")},
		&rds.DescribeDBInstancesInput{DBInstanceIdentifier: awssdk.String("db-1")},
	}
	if !reflect.DeepEqual(f.asked, want) {
		t.Errorf("asked %+v", f.asked)
	}
}
