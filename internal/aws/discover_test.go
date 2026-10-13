package aws

// The adapters of a region's discovery (discover.RegionPorts): the databases
// (RDS DescribeDBInstances and DescribeDBClusters, paged), instances with their
// state and security groups, security groups' ingress, an ECS service's desired
// count and security groups, an Auto Scaling group, rules by ARN; and that a
// throttled ELB call is retried by the adapter's retryer.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/Dormeto/cli/internal/discover"
)

type fakeDatabases struct{ asked []any }

func (f *fakeDatabases) DescribeDBInstances(_ context.Context, in *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	f.asked = append(f.asked, in)
	if in.Marker == nil {
		return &rds.DescribeDBInstancesOutput{Marker: awssdk.String("m2"), DBInstances: []rdstypes.DBInstance{{
			DBInstanceIdentifier: awssdk.String("shop-db"), DBInstanceStatus: awssdk.String("available"), Engine: awssdk.String("mysql"),
			DBInstanceClass: awssdk.String("db.t3.medium"), MultiAZ: awssdk.Bool(true), DBClusterIdentifier: nil,
			ReadReplicaSourceDBInstanceIdentifier: awssdk.String("src"), ReadReplicaDBInstanceIdentifiers: []string{"r1"},
			VpcSecurityGroups: []rdstypes.VpcSecurityGroupMembership{{VpcSecurityGroupId: awssdk.String("sg-db"), Status: awssdk.String("active")}},
			TagList:           []rdstypes.Tag{{Key: awssdk.String("env"), Value: awssdk.String("dev")}},
		}}}, nil
	}
	return &rds.DescribeDBInstancesOutput{DBInstances: []rdstypes.DBInstance{{DBInstanceIdentifier: awssdk.String("aur-1"), DBClusterIdentifier: awssdk.String("aurora")}}}, nil
}

func (f *fakeDatabases) DescribeDBClusters(_ context.Context, in *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	f.asked = append(f.asked, in)
	return &rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{{
		DBClusterIdentifier: awssdk.String("aurora"), Status: awssdk.String("available"), Engine: awssdk.String("aurora-postgresql"), EngineMode: awssdk.String("provisioned"),
		VpcSecurityGroups: []rdstypes.VpcSecurityGroupMembership{{VpcSecurityGroupId: awssdk.String("sg-aur"), Status: awssdk.String("inactive")}},
		TagList:           []rdstypes.Tag{{Key: awssdk.String("team"), Value: awssdk.String("shop")}},
	}}}, nil
}

func TestTheDatabasesAdapterPagesByMarkerAndMapsTheFieldsDiscoveryReads(t *testing.T) {
	f := &fakeDatabases{}
	p := databasesPort{f}
	ctx := context.Background()
	first, next, err := p.DescribeDBInstances(ctx, "")
	if err != nil || next != "m2" {
		t.Fatalf("%v %q", err, next)
	}
	second, next, _ := p.DescribeDBInstances(ctx, "m2")
	clusters, next2, _ := p.DescribeDBClusters(ctx, "")
	if next != "" || next2 != "" {
		t.Errorf("next markers %q %q", next, next2)
	}
	eqJSON(t, "first", first, `[{"DBInstanceIdentifier":"shop-db","DBInstanceStatus":"available","Engine":"mysql","DBInstanceClass":"db.t3.medium","MultiAZ":true,"DBClusterIdentifier":"","ReadReplicaSourceDBInstanceIdentifier":"src","ReadReplicaDBInstanceIdentifiers":["r1"],"VpcSecurityGroups":[{"VpcSecurityGroupId":"sg-db","Status":"active"}],"TagList":[{"Key":"env","Value":"dev"}]}]`)
	if second[0].DBClusterIdentifier != "aurora" {
		t.Errorf("second %+v", second)
	}
	eqJSON(t, "clusters", clusters, `[{"DBClusterIdentifier":"aurora","Status":"available","Engine":"aurora-postgresql","EngineMode":"provisioned","VpcSecurityGroups":[{"VpcSecurityGroupId":"sg-aur","Status":"inactive"}],"TagList":[{"Key":"team","Value":"shop"}]}]`)
	want := []any{&rds.DescribeDBInstancesInput{}, &rds.DescribeDBInstancesInput{Marker: awssdk.String("m2")}, &rds.DescribeDBClustersInput{}}
	if !reflect.DeepEqual(f.asked, want) {
		t.Errorf("asked %+v", f.asked)
	}
}

type fakeRegionEC2 struct{ asked []any }

func (f *fakeRegionEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.asked = append(f.asked, in)
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{
		{Instances: []ec2types.Instance{{InstanceId: awssdk.String("i-1"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped}, SecurityGroups: []ec2types.GroupIdentifier{{GroupId: awssdk.String("sg-a")}, {GroupId: awssdk.String("sg-b")}}}}},
		{Instances: []ec2types.Instance{{InstanceId: awssdk.String("i-2")}}},
	}}, nil
}

func (f *fakeRegionEC2) DescribeSecurityGroups(_ context.Context, in *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	f.asked = append(f.asked, in)
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{{GroupId: awssdk.String("sg-db"), IpPermissions: []ec2types.IpPermission{
		{UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: awssdk.String("sg-web")}, {GroupId: awssdk.String("sg-app")}}},
		{IpRanges: []ec2types.IpRange{{CidrIp: awssdk.String("10.0.0.0/16")}}},
	}}}}, nil
}

func TestTheRegionEC2AdapterGivesInstancesTheirStateAndGroupsAndSecurityGroupsWhoTheyLetIn(t *testing.T) {
	f := &fakeRegionEC2{}
	p := regionEC2{f}
	ctx := context.Background()
	instances, err := p.Instances(ctx, []string{"i-1", "i-2"})
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "instances", instances, `[{"ID":"i-1","State":"stopped","SecurityGroups":["sg-a","sg-b"]},{"ID":"i-2","State":"","SecurityGroups":null}]`)
	groups, err := p.SecurityGroups(ctx, []string{"sg-db"})
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "groups", groups, `[{"ID":"sg-db","AllowedFrom":["sg-web","sg-app"]}]`)
	want := []any{&ec2.DescribeInstancesInput{InstanceIds: []string{"i-1", "i-2"}}, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{"sg-db"}}}
	if !reflect.DeepEqual(f.asked, want) {
		t.Errorf("asked %+v", f.asked)
	}
}

type fakeRegionECS struct {
	ecsAPI
	asked []*ecs.DescribeServicesInput
	out   []ecstypes.Service
}

func (f *fakeRegionECS) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	f.asked = append(f.asked, in)
	return &ecs.DescribeServicesOutput{Services: f.out}, nil
}

func TestTheRegionECSAdapterGivesAServicesDesiredCountAndAwsvpcSecurityGroups(t *testing.T) {
	f := &fakeRegionECS{out: []ecstypes.Service{{DesiredCount: 3, NetworkConfiguration: &ecstypes.NetworkConfiguration{AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{SecurityGroups: []string{"sg-api"}}}}}}
	p := regionECS{f}
	service, err := p.Service(context.Background(), "prod", "arn:svc")
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "service", service, `{"DesiredCount":3,"SecurityGroups":["sg-api"]}`)
	if !reflect.DeepEqual(f.asked, []*ecs.DescribeServicesInput{{Cluster: awssdk.String("prod"), Services: []string{"arn:svc"}}}) {
		t.Errorf("asked %+v", f.asked)
	}
	// No such service (no network configuration, or none at all): nil, and 0.
	f.out = nil
	if none, err := p.Service(context.Background(), "prod", "arn:svc"); err != nil || none != nil {
		t.Errorf("%v %v", none, err)
	}
	f.out = []ecstypes.Service{{}}
	if bare, _ := p.Service(context.Background(), "prod", "arn:svc"); bare == nil || bare.DesiredCount != 0 || len(bare.SecurityGroups) != 0 {
		t.Errorf("%+v", bare)
	}
}

type fakeGroup struct {
	asked []*autoscaling.DescribeAutoScalingGroupsInput
}

func (f *fakeGroup) DescribeAutoScalingGroups(_ context.Context, in *autoscaling.DescribeAutoScalingGroupsInput, _ ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
	f.asked = append(f.asked, in)
	if len(in.AutoScalingGroupNames) == 0 || in.AutoScalingGroupNames[0] != "web-asg" {
		return &autoscaling.DescribeAutoScalingGroupsOutput{}, nil
	}
	return &autoscaling.DescribeAutoScalingGroupsOutput{AutoScalingGroups: []asgtypes.AutoScalingGroup{{DesiredCapacity: awssdk.Int32(2), Instances: []asgtypes.Instance{{InstanceId: awssdk.String("i-1")}, {InstanceId: awssdk.String("i-2")}}}}}, nil
}

func TestTheRegionAutoScalingAdapterGivesAGroupsDesiredCapacityAndInstances(t *testing.T) {
	f := &fakeGroup{}
	p := regionAutoScaling{f}
	group, err := p.Group(context.Background(), "web-asg")
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "group", group, `{"Desired":2,"Instances":["i-1","i-2"]}`)
	if none, _ := p.Group(context.Background(), "gone"); none != nil {
		t.Errorf("%+v", none)
	}
	if !reflect.DeepEqual(f.asked[0], &autoscaling.DescribeAutoScalingGroupsInput{AutoScalingGroupNames: []string{"web-asg"}}) {
		t.Errorf("asked %+v", f.asked[0])
	}
}

const throttled = `<ErrorResponse xmlns="http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/"><Error><Type>Sender</Type><Code>Throttling</Code><Message>Rate exceeded</Message></Error><RequestId>r1</RequestId></ErrorResponse>`

// Review focus 4: a throttled region is retried, not failed: the ELB client's retryer (adaptive) retries a Throttling
// answer, through the port discovery's region uses.
func TestAThrottledELBCallIsRetriedByTheAdaptersRetryer(t *testing.T) {
	var calls atomic.Int64
	var form atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form.Store(r.PostForm.Get("RuleArns.member.1"))
		w.Header().Set("Content-Type", "text/xml")
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(throttled))
			return
		}
		_, _ = w.Write([]byte(rulesAnswer))
	}))
	defer srv.Close()
	cfg := awssdk.Config{Region: "eu-west-3", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: awssdk.String(srv.URL)}
	ports := NewRegion(cfg, "eu-west-1", "").Discover(discover.Where{Account: "111111111111", Region: "eu-west-1"}, time.Now)
	started := time.Now()
	rules, err := ports.ELBv2.DescribeRules(context.Background(), []string{"arn:rule/1"})
	if err != nil {
		t.Fatalf("the throttled call was not retried: %v", err)
	}
	if calls.Load() != 2 || len(rules) != 4 || form.Load() != "arn:rule/1" {
		t.Errorf("%d calls, %d rules, asked %v", calls.Load(), len(rules), form.Load())
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("took %s", elapsed)
	}
}

func TestTheRegionDiscoveryPortsAreTheRegionsClients(t *testing.T) {
	cfg := awssdk.Config{Region: "eu-west-3", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", "")}
	ports := NewRegion(cfg, "eu-west-1", "").Discover(discover.Where{Account: "111111111111", Region: "eu-west-1"}, time.Now)
	if ports.Scan == nil || ports.AutoScaling == nil || ports.ELBv2 == nil || ports.EC2 == nil || ports.ECS == nil || ports.RDS == nil ||
		ports.Stopped.EC2 == nil || ports.Stopped.ECS == nil || ports.Stopped.RDS == nil || ports.Stopped.KubernetesOf == nil {
		t.Errorf("%+v", ports)
	}
}
