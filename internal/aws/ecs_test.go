package aws

import (
	"context"
	"reflect"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/stopped"
)

type fakeECS struct{ asked []any }

func (f *fakeECS) ListClusters(_ context.Context, in *ecs.ListClustersInput, _ ...func(*ecs.Options)) (*ecs.ListClustersOutput, error) {
	f.asked = append(f.asked, in)
	return &ecs.ListClustersOutput{ClusterArns: []string{"arn:cluster/app"}, NextToken: awssdk.String("n")}, nil
}

func (f *fakeECS) ListServices(_ context.Context, in *ecs.ListServicesInput, _ ...func(*ecs.Options)) (*ecs.ListServicesOutput, error) {
	f.asked = append(f.asked, in)
	return &ecs.ListServicesOutput{ServiceArns: []string{"arn:service/app/web"}}, nil
}

func (f *fakeECS) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	f.asked = append(f.asked, in)
	return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
		ServiceArn: awssdk.String("arn:service/app/web"), ClusterArn: awssdk.String("arn:cluster/app"), TaskDefinition: awssdk.String("arn:task/web:3"),
		LaunchType: ecstypes.LaunchTypeFargate, LoadBalancers: []ecstypes.LoadBalancer{{TargetGroupArn: awssdk.String("arn:tg/web/1")}},
		CapacityProviderStrategy: []ecstypes.CapacityProviderStrategyItem{{CapacityProvider: awssdk.String("FARGATE_SPOT"), Base: 1, Weight: 3}},
	}}}, nil
}

func (f *fakeECS) DescribeTaskDefinition(_ context.Context, in *ecs.DescribeTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	f.asked = append(f.asked, in)
	return &ecs.DescribeTaskDefinitionOutput{TaskDefinition: &ecstypes.TaskDefinition{
		Cpu: awssdk.String("512"), Memory: awssdk.String("1024"),
		RuntimePlatform: &ecstypes.RuntimePlatform{CpuArchitecture: ecstypes.CPUArchitectureArm64, OperatingSystemFamily: ecstypes.OSFamilyLinux},
	}}, nil
}

func TestTheECSAdaptersGiveTheScansAndTheStoppedItemsShapes(t *testing.T) {
	f := &fakeECS{}
	ctx := context.Background()
	s := scanECS{f}
	clusters, _ := s.ListClusters(ctx, discover.ListClustersInput{NextToken: "t"})
	services, _ := s.ListServices(ctx, discover.ListServicesInput{Cluster: "arn:cluster/app"})
	described, _ := s.DescribeServices(ctx, discover.DescribeServicesInput{Cluster: "arn:cluster/app", Services: []string{"arn:service/app/web"}})
	eqJSON(t, "clusters", clusters, `{"clusterArns":["arn:cluster/app"],"nextToken":"n"}`)
	eqJSON(t, "services", services, `{"serviceArns":["arn:service/app/web"]}`)
	eqJSON(t, "scan's services", described, `{"services":[{"serviceArn":"arn:service/app/web","loadBalancers":[{"targetGroupArn":"arn:tg/web/1"}]}]}`)
	st := stoppedECS{f}
	stoppedServices, _ := st.DescribeServices(ctx, stopped.DescribeServicesInput{Cluster: "arn:cluster/app", Services: []string{"arn:service/app/web"}})
	task, _ := st.DescribeTaskDefinition(ctx, stopped.DescribeTaskDefinitionInput{TaskDefinition: "arn:task/web:3"})
	eqJSON(t, "stopped's services", stoppedServices, `{"services":[{"serviceArn":"arn:service/app/web","clusterArn":"arn:cluster/app","taskDefinition":"arn:task/web:3","launchType":"FARGATE","capacityProviderStrategy":[{"capacityProvider":"FARGATE_SPOT","base":1,"weight":3}]}]}`)
	eqJSON(t, "task definition", task, `{"taskDefinition":{"cpu":"512","memory":"1024","runtimePlatform":{"cpuArchitecture":"ARM64","operatingSystemFamily":"LINUX"}}}`)
	services2 := &ecs.DescribeServicesInput{Cluster: awssdk.String("arn:cluster/app"), Services: []string{"arn:service/app/web"}}
	want := []any{
		&ecs.ListClustersInput{NextToken: awssdk.String("t")},
		&ecs.ListServicesInput{Cluster: awssdk.String("arn:cluster/app")},
		services2, services2,
		&ecs.DescribeTaskDefinitionInput{TaskDefinition: awssdk.String("arn:task/web:3")},
	}
	if !reflect.DeepEqual(f.asked, want) {
		t.Errorf("asked %+v", f.asked)
	}
}
