package aws

import (
	"context"
	"reflect"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/stopped"
)

type fakeEC2 struct{ asked []any }

func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.asked = append(f.asked, in)
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{
		InstanceId: awssdk.String("i-1"), InstanceType: ec2types.InstanceTypeT3Medium, PlatformDetails: awssdk.String("Linux/UNIX"),
		InstanceLifecycle: ec2types.InstanceLifecycleTypeSpot, Tags: []ec2types.Tag{{Key: awssdk.String("eks:cluster-name"), Value: awssdk.String("demo")}},
	}}}}}, nil
}

func (f *fakeEC2) DescribeRegions(_ context.Context, in *ec2.DescribeRegionsInput, _ ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error) {
	f.asked = append(f.asked, in)
	return &ec2.DescribeRegionsOutput{Regions: []ec2types.Region{{RegionName: awssdk.String("eu-west-1")}, {}, {RegionName: awssdk.String("eu-west-3")}}}, nil
}

func TestTheEC2AdaptersGiveTheScansAndTheStoppedItemsShapes(t *testing.T) {
	f := &fakeEC2{}
	ctx := context.Background()
	scan, err := scanEC2{f}.DescribeInstances(ctx, discover.DescribeInstancesInput{InstanceIds: []string{"i-1"}})
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "scan", scan, `{"Reservations":[{"Instances":[{"InstanceId":"i-1","Tags":[{"Key":"eks:cluster-name","Value":"demo"}]}]}]}`)
	items, err := stoppedEC2{f}.DescribeInstances(ctx, stopped.DescribeInstancesInput{InstanceIds: []string{"i-1"}})
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "stopped", items, `{"Reservations":[{"Instances":[{"InstanceId":"i-1","InstanceType":"t3.medium","PlatformDetails":"Linux/UNIX","InstanceLifecycle":"spot"}]}]}`)
	want := &ec2.DescribeInstancesInput{InstanceIds: []string{"i-1"}}
	if !reflect.DeepEqual(f.asked, []any{want, want}) {
		t.Errorf("asked %+v", f.asked)
	}
}

func TestTheEnabledRegionsAreThoseEC2Names(t *testing.T) {
	f := &fakeEC2{}
	regions, err := enabledRegions(context.Background(), f)
	if err != nil || !reflect.DeepEqual(regions, []string{"eu-west-1", "eu-west-3"}) {
		t.Errorf("%v %v", regions, err)
	}
	if !reflect.DeepEqual(f.asked, []any{&ec2.DescribeRegionsInput{}}) {
		t.Errorf("asked %+v", f.asked)
	}
}
