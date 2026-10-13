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
	"github.com/Dormeto/cli/internal/stopped"
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

func TestTheRDSAdapterPagesByMarkerAndMapsTheFieldsDiscoveryReads(t *testing.T) {
	f := &fakeDatabases{}
	p := rdsPort{f}
	ctx := context.Background()
	first, err := p.DescribeDBInstances(ctx, stopped.DescribeDBInstancesInput{})
	if err != nil || first.Marker != "m2" {
		t.Fatalf("%v %q", err, first.Marker)
	}
	second, _ := p.DescribeDBInstances(ctx, stopped.DescribeDBInstancesInput{Marker: "m2"})
	clusters, _ := p.DescribeDBClusters(ctx, stopped.DescribeDBClustersInput{})
	eqJSON(t, "first", first, `{"DBInstances":[{"DBInstanceIdentifier":"shop-db","DBInstanceClass":"db.t3.medium","Engine":"mysql","MultiAZ":true,"DBInstanceStatus":"available","ReadReplicaSourceDBInstanceIdentifier":"src","ReadReplicaDBInstanceIdentifiers":["r1"],"VpcSecurityGroups":[{"VpcSecurityGroupId":"sg-db","Status":"active"}],"TagList":[{"Key":"env","Value":"dev"}]}],"Marker":"m2"}`)
	if second.DBInstances[0].DBClusterIdentifier != "aurora" || second.Marker != "" {
		t.Errorf("second %+v", second)
	}
	eqJSON(t, "clusters", clusters, `{"DBClusters":[{"DBClusterIdentifier":"aurora","Engine":"aurora-postgresql","Status":"available","EngineMode":"provisioned","VpcSecurityGroups":[{"VpcSecurityGroupId":"sg-aur","Status":"inactive"}],"TagList":[{"Key":"team","Value":"shop"}]}]}`)
	want := []any{&rds.DescribeDBInstancesInput{}, &rds.DescribeDBInstancesInput{Marker: awssdk.String("m2")}, &rds.DescribeDBClustersInput{}}
	if !reflect.DeepEqual(f.asked, want) {
		t.Errorf("asked %+v", f.asked)
	}
}

type fakeRegionEC2 struct {
	*fakeEC2
	out []ec2types.Reservation
}

func (f *fakeRegionEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.asked = append(f.asked, in)
	return &ec2.DescribeInstancesOutput{Reservations: f.out}, nil
}

func TestTheEC2AdapterGivesInstancesTheirStateAndGroupsAndSecurityGroupsTheirIngress(t *testing.T) {
	f := &fakeRegionEC2{fakeEC2: &fakeEC2{}, out: []ec2types.Reservation{
		{Instances: []ec2types.Instance{{InstanceId: awssdk.String("i-1"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped}, SecurityGroups: []ec2types.GroupIdentifier{{GroupId: awssdk.String("sg-a")}, {GroupId: awssdk.String("sg-b")}}}}},
		{Instances: []ec2types.Instance{{InstanceId: awssdk.String("i-2")}}},
	}}
	p := stoppedEC2{f}
	ctx := context.Background()
	instances, err := p.DescribeInstances(ctx, stopped.DescribeInstancesInput{InstanceIds: []string{"i-1", "i-2"}})
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "instances", instances, `{"Reservations":[{"Instances":[{"InstanceId":"i-1","State":{"Name":"stopped"},"SecurityGroups":[{"GroupId":"sg-a"},{"GroupId":"sg-b"}]}]},{"Instances":[{"InstanceId":"i-2"}]}]}`)
	groups, err := p.DescribeSecurityGroups(ctx, discover.DescribeSecurityGroupsInput{GroupIds: []string{"sg-db"}})
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "groups", groups, `{"SecurityGroups":[{"GroupId":"sg-db","IpPermissions":[{"UserIdGroupPairs":[{"GroupId":"sg-web"},{"GroupId":"sg-app"}]},{}]}]}`)
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

func TestTheECSAdapterGivesAServicesDesiredCountAndAwsvpcSecurityGroups(t *testing.T) {
	f := &fakeRegionECS{out: []ecstypes.Service{{ServiceArn: awssdk.String("arn:svc"), DesiredCount: 3, NetworkConfiguration: &ecstypes.NetworkConfiguration{AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{SecurityGroups: []string{"sg-api"}}}}, {ServiceArn: awssdk.String("arn:bare")}}}
	out, err := stoppedECS{f}.DescribeServices(context.Background(), stopped.DescribeServicesInput{Cluster: "prod", Services: []string{"arn:svc"}})
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "services", out, `{"services":[{"serviceArn":"arn:svc","desiredCount":3,"networkConfiguration":{"awsvpcConfiguration":{"securityGroups":["sg-api"]}}},{"serviceArn":"arn:bare"}]}`)
	if !reflect.DeepEqual(f.asked, []*ecs.DescribeServicesInput{{Cluster: awssdk.String("prod"), Services: []string{"arn:svc"}}}) {
		t.Errorf("asked %+v", f.asked)
	}
}

type fakeGroup struct {
	asked []*autoscaling.DescribeAutoScalingGroupsInput
}

func (f *fakeGroup) DescribeAutoScalingGroups(_ context.Context, in *autoscaling.DescribeAutoScalingGroupsInput, _ ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
	f.asked = append(f.asked, in)
	return &autoscaling.DescribeAutoScalingGroupsOutput{AutoScalingGroups: []asgtypes.AutoScalingGroup{{AutoScalingGroupName: awssdk.String("web-asg"), DesiredCapacity: awssdk.Int32(2), Instances: []asgtypes.Instance{{InstanceId: awssdk.String("i-1")}, {InstanceId: awssdk.String("i-2")}}}}}, nil
}

func TestTheAutoScalingAdapterAsksGroupsByNameAndGivesDesiredCapacityAndInstances(t *testing.T) {
	f := &fakeGroup{}
	out, err := autoScalingPort{f}.DescribeAutoScalingGroups(context.Background(), discover.DescribeAutoScalingGroupsInput{AutoScalingGroupNames: []string{"web-asg"}})
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "groups", out, `{"AutoScalingGroups":[{"AutoScalingGroupName":"web-asg","DesiredCapacity":2,"Instances":[{"InstanceId":"i-1"},{"InstanceId":"i-2"}]}]}`)
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
	out, err := ports.ELBv2.DescribeRules(context.Background(), discover.DescribeRulesInput{RuleArns: []string{"arn:rule/1"}})
	rules := out.Rules
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
	if ports.Scan == nil || ports.AutoScaling == nil || ports.ELBv2 == nil || ports.EC2 == nil ||
		ports.Stopped.EC2 == nil || ports.Stopped.ECS == nil || ports.Stopped.RDS == nil || ports.Stopped.KubernetesOf == nil {
		t.Errorf("%+v", ports)
	}
}
