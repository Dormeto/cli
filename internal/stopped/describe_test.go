package stopped

// Ports the cases of router lambda/test/stopped.test.mjs and the k8sItems and
// describeStopped cases of lambda/test/kubernetes.test.mjs.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/kubernetes"
)

type fakeEC2 map[string]Instance

func (f fakeEC2) DescribeInstances(_ context.Context, in DescribeInstancesInput) (DescribeInstancesOutput, error) {
	var out Reservation
	for _, id := range in.InstanceIds {
		i := f[id]
		i.InstanceID = id
		out.Instances = append(out.Instances, i)
	}
	return DescribeInstancesOutput{Reservations: []Reservation{out}}, nil
}

const (
	service = "arn:aws:ecs:eu-west-1:111122223333:service/app/web"
	cluster = "arn:aws:ecs:eu-west-1:111122223333:cluster/app"
)

type fakeECS struct {
	service Service
	task    TaskDefinition
	refuse  bool
}

func (f fakeECS) DescribeServices(context.Context, DescribeServicesInput) (DescribeServicesOutput, error) {
	s := f.service
	s.ServiceArn, s.ClusterArn, s.TaskDefinition = service, cluster, "arn:td/web:3"
	return DescribeServicesOutput{Services: []Service{s}}, nil
}

func (f fakeECS) DescribeTaskDefinition(context.Context, DescribeTaskDefinitionInput) (DescribeTaskDefinitionOutput, error) {
	if f.refuse {
		return DescribeTaskDefinitionOutput{}, errors.New("not authorized to perform: ecs:DescribeTaskDefinition")
	}
	return DescribeTaskDefinitionOutput{TaskDefinition: &f.task}, nil
}

type fakeRDS struct {
	instances map[string]DBInstance
	order     []string
	clusters  map[string]DBCluster
}

func (f fakeRDS) DescribeDBInstances(_ context.Context, in DescribeDBInstancesInput) (DescribeDBInstancesOutput, error) {
	var out DescribeDBInstancesOutput
	if len(in.Filters) > 0 {
		for _, id := range f.order {
			if i := f.instances[id]; i.DBClusterIdentifier == in.Filters[0].Values[0] {
				i.DBInstanceIdentifier = id
				out.DBInstances = append(out.DBInstances, i)
			}
		}
		return out, nil
	}
	i := f.instances[in.DBInstanceIdentifier]
	i.DBInstanceIdentifier = in.DBInstanceIdentifier
	out.DBInstances = []DBInstance{i}
	return out, nil
}

func (f fakeRDS) DescribeDBClusters(_ context.Context, in DescribeDBClustersInput) (DescribeDBClustersOutput, error) {
	c := f.clusters[in.DBClusterIdentifier]
	c.DBClusterIdentifier = in.DBClusterIdentifier
	return DescribeDBClustersOutput{DBClusters: []DBCluster{c}}, nil
}

func describe(t *testing.T, ports Ports, backends ...Backend) []Item {
	t.Helper()
	items, err := Describe(context.Background(), ports, backends)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// want compares items as JSON (Node's deepEqual on the same objects).
func want(t *testing.T, got []Item, expected ...string) {
	t.Helper()
	if g, w := asJSON(t, got), "["+strings.Join(expected, ",")+"]"; g != w {
		t.Errorf("items\n got:  %s\n want: %s", g, w)
	}
}

func TestEC2InstancesByTypeAndOSAndAnAutoScalingGroupByTheTypesOfItsInstances(t *testing.T) {
	ec2 := fakeEC2{
		"i-1": {InstanceType: "t3.medium", PlatformDetails: "Linux/UNIX"},
		"i-2": {InstanceType: "m5.large", PlatformDetails: "Windows"},
		"i-3": {InstanceType: "t3.small", PlatformDetails: "Linux/UNIX"},
		"i-4": {InstanceType: "t3a.small", PlatformDetails: "Linux/UNIX"},
		"i-5": {InstanceType: "t3.small", PlatformDetails: "Linux/UNIX"},
	}
	items := describe(t, Ports{EC2: ec2},
		Backend{Kind: "instance", ID: "i-1"}, Backend{Kind: "instance", ID: "i-2"},
		Backend{Kind: "asg", Name: "web", Desired: 3, Instances: []string{"i-3", "i-4", "i-5"}})
	want(t, items,
		`{"kind":"ec2","type":"t3.medium","count":1,"os":"Linux"}`,
		`{"kind":"ec2","type":"m5.large","count":1,"os":"Windows"}`,
		`{"kind":"ec2","type":"t3.small","count":2,"os":"Linux"}`,
		`{"kind":"ec2","type":"t3a.small","count":1,"os":"Linux"}`)
}

func TestAnOSThatIsNeitherLinuxNorWindowsIsSaidAsItIs(t *testing.T) {
	items := describe(t, Ports{EC2: fakeEC2{"i-1": {InstanceType: "m5.large", PlatformDetails: "Red Hat Enterprise Linux"}}}, Backend{Kind: "instance", ID: "i-1"})
	want(t, items, `{"kind":"ec2","type":"m5.large","count":1,"os":"Red Hat Enterprise Linux"}`)
}

func TestWindowsWithItsOwnLicenseOrWithSQLServerIsSaidAsItIs(t *testing.T) {
	items := describe(t, Ports{EC2: fakeEC2{
		"i-1": {InstanceType: "m5.large", PlatformDetails: "Windows BYOL"},
		"i-2": {InstanceType: "m5.large", PlatformDetails: "Windows with SQL Server Standard"},
	}}, Backend{Kind: "instance", ID: "i-1"}, Backend{Kind: "instance", ID: "i-2"})
	var oses []string
	for _, i := range items {
		oses = append(oses, i.OS)
	}
	if !reflect.DeepEqual(oses, []string{"Windows BYOL", "Windows with SQL Server Standard"}) {
		t.Errorf("oses: %v", oses)
	}
}

func ecsBackend(desired int) Backend {
	return Backend{Kind: "ecs", ID: service, Cluster: cluster, Desired: desired}
}

func TestAFargateServiceItsTaskSizeTimesItsDesiredCountARMOnACapacityProviderOneOnEC2CapacityNotCounted(t *testing.T) {
	task := TaskDefinition{CPU: "512", Memory: "1024", RuntimePlatform: &RuntimePlatform{CPUArchitecture: "ARM64", OperatingSystemFamily: "LINUX"}}
	want(t, describe(t, Ports{ECS: fakeECS{service: Service{LaunchType: "FARGATE"}, task: task}}, ecsBackend(2)),
		`{"kind":"fargate","vcpu":0.5,"memoryGb":1,"count":2,"arch":"arm64","os":"Linux"}`)
	provider := fakeECS{service: Service{CapacityProviderStrategy: []CapacityProviderStrategyItem{{CapacityProvider: "FARGATE", Weight: 1}}}, task: TaskDefinition{CPU: "256", Memory: "512"}}
	want(t, describe(t, Ports{ECS: provider}, ecsBackend(1)),
		`{"kind":"fargate","vcpu":0.25,"memoryGb":0.5,"count":1,"arch":"x86_64","os":"Linux"}`)
	want(t, describe(t, Ports{ECS: fakeECS{service: Service{LaunchType: "EC2"}, task: task}}, ecsBackend(2)))
}

func TestATaskSizeTheBoundaryRefusesIsAnUnknownItemAndTheNapGoesOn(t *testing.T) {
	items := describe(t, Ports{ECS: fakeECS{service: Service{LaunchType: "FARGATE"}, refuse: true}}, ecsBackend(1))
	if len(items) != 1 || items[0].Kind != "unknown" || items[0].Backend != "ecs:"+service || !strings.Contains(items[0].Reason, "DescribeTaskDefinition") {
		t.Errorf("items: %s", asJSON(t, items))
	}
}

func TestRDSInstancesMultiAZSaidTheProvisionedInstancesOfAnAuroraClusterAuroraServerlessV2AtItsMinimum(t *testing.T) {
	min, max := 0.5, 4.0
	rds := fakeRDS{
		order: []string{"shop", "aur-1", "srv-1"},
		instances: map[string]DBInstance{
			"shop":  {DBInstanceClass: "db.t4g.micro", Engine: "postgres", MultiAZ: true},
			"aur-1": {DBInstanceClass: "db.r6g.large", Engine: "aurora-postgresql", DBClusterIdentifier: "aur"},
			"srv-1": {DBInstanceClass: "db.serverless", Engine: "aurora-mysql", DBClusterIdentifier: "srv"},
		},
		clusters: map[string]DBCluster{
			"aur": {Engine: "aurora-postgresql"},
			"srv": {Engine: "aurora-mysql", ServerlessV2ScalingConfiguration: &ServerlessV2Scaling{MinCapacity: &min, MaxCapacity: &max}},
		},
	}
	items := describe(t, Ports{RDS: rds}, Backend{Kind: "rds-instance", ID: "shop"}, Backend{Kind: "rds-cluster", ID: "aur"}, Backend{Kind: "rds-cluster", ID: "srv"})
	want(t, items,
		`{"kind":"rds","class":"db.t4g.micro","engine":"postgres","multiAz":true}`,
		`{"kind":"rds","class":"db.r6g.large","engine":"aurora-postgresql","multiAz":false}`,
		`{"kind":"aurora-serverless","engine":"aurora-mysql","acu":0.5}`)
}

func TestAnInstanceOnSpotIsSaidSoAGroupsOnDemandAndSpotInstancesOfOneTypeAreTwoItems(t *testing.T) {
	ec2 := fakeEC2{
		"i-1": {InstanceType: "t3.medium", PlatformDetails: "Linux/UNIX", InstanceLifecycle: "spot"},
		"i-2": {InstanceType: "t3.medium", PlatformDetails: "Linux/UNIX"},
		"i-3": {InstanceType: "t3.medium", PlatformDetails: "Linux/UNIX", InstanceLifecycle: "spot"},
		"i-4": {InstanceType: "t3.medium", PlatformDetails: "Linux/UNIX"},
	}
	items := describe(t, Ports{EC2: ec2}, Backend{Kind: "instance", ID: "i-1"}, Backend{Kind: "asg", Name: "web", Desired: 3, Instances: []string{"i-2", "i-3", "i-4"}})
	want(t, items,
		`{"kind":"ec2","type":"t3.medium","count":1,"os":"Linux","spot":true}`,
		`{"kind":"ec2","type":"t3.medium","count":2,"os":"Linux"}`,
		`{"kind":"ec2","type":"t3.medium","count":1,"os":"Linux","spot":true}`)
}

func TestAGroupOfSpotInstancesWhoseDesiredCapacityWasNotAllDescribedTheRestAddedToItsFirstItem(t *testing.T) {
	ec2 := fakeEC2{"i-1": {InstanceType: "t3.small", PlatformDetails: "Linux/UNIX", InstanceLifecycle: "spot"}}
	items := describe(t, Ports{EC2: ec2}, Backend{Kind: "asg", Name: "web", Desired: 3, Instances: []string{"i-1"}})
	want(t, items, `{"kind":"ec2","type":"t3.small","count":3,"os":"Linux","spot":true}`)
}

func TestACapacityProviderStrategySplitsTheDesiredCountAsECSPlacesIt(t *testing.T) {
	counts := func(strategy []CapacityProviderStrategyItem, desired int) map[string]int {
		return StrategyCounts(strategy, desired)
	}
	s := func(p string, base, weight int) CapacityProviderStrategyItem {
		return CapacityProviderStrategyItem{CapacityProvider: p, Base: base, Weight: weight}
	}
	eq := func(got, want map[string]int) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	}
	eq(counts([]CapacityProviderStrategyItem{s("FARGATE", 1, 1), s("FARGATE_SPOT", 0, 3)}, 5), map[string]int{"FARGATE": 2, "FARGATE_SPOT": 3})
	// Rounded with the largest remainder: always the desired count.
	even := counts([]CapacityProviderStrategyItem{s("FARGATE", 0, 1), s("FARGATE_SPOT", 0, 1)}, 3)
	if even["FARGATE"]+even["FARGATE_SPOT"] != 3 || (even["FARGATE"] != 1 && even["FARGATE"] != 2) {
		t.Errorf("even: %v", even)
	}
	eq(counts([]CapacityProviderStrategyItem{s("FARGATE", 5, 1), s("FARGATE_SPOT", 0, 1)}, 2), map[string]int{"FARGATE": 2, "FARGATE_SPOT": 0})
	// No weight: the base's provider takes the rest.
	eq(counts([]CapacityProviderStrategyItem{s("FARGATE", 1, 0), s("FARGATE_SPOT", 0, 0)}, 3), map[string]int{"FARGATE": 3, "FARGATE_SPOT": 0})
	eq(counts([]CapacityProviderStrategyItem{s("app-asg", 0, 1), s("FARGATE_SPOT", 0, 1)}, 4), map[string]int{"app-asg": 2, "FARGATE_SPOT": 2})
}

func TestAFargateServiceOnFargateAndFargateSpotAnItemEachOnSpotOnlyOneSpotItemAShareOnEC2CapacityNotCounted(t *testing.T) {
	of := func(strategy []CapacityProviderStrategyItem, desired int) []Item {
		return describe(t, Ports{ECS: fakeECS{service: Service{CapacityProviderStrategy: strategy}, task: TaskDefinition{CPU: "256", Memory: "512"}}}, ecsBackend(desired))
	}
	const size = `"vcpu":0.25,"memoryGb":0.5,"count":%d,"arch":"x86_64","os":"Linux"%s`
	item := func(count int, spot string) string {
		return `{"kind":"fargate",` + fmt.Sprintf(size, count, spot) + `}`
	}
	want(t, of([]CapacityProviderStrategyItem{{CapacityProvider: "FARGATE", Base: 1, Weight: 1}, {CapacityProvider: "FARGATE_SPOT", Weight: 3}}, 5), item(2, ""), item(3, `,"spot":true`))
	want(t, of([]CapacityProviderStrategyItem{{CapacityProvider: "FARGATE_SPOT", Weight: 1}}, 2), item(2, `,"spot":true`))
	want(t, of([]CapacityProviderStrategyItem{{CapacityProvider: "app-asg", Weight: 1}, {CapacityProvider: "FARGATE_SPOT", Weight: 1}}, 4), item(2, `,"spot":true`))
}

// Kubernetes (router kubernetes.test.mjs): the cluster as a fake of the API.

const k8sID = "eks-demo/demo/Deployment/web"

type fakeAPI struct {
	owner string
	pods  []string
	nodes map[string]string
}

func (f fakeAPI) Get(_ context.Context, path string) (json.RawMessage, error) {
	if path == "/apis/apps/v1/namespaces/demo/deployments/web" {
		return json.RawMessage(f.owner), nil
	}
	if name, ok := strings.CutPrefix(path, "/api/v1/nodes/"); ok {
		if n, ok := f.nodes[name]; ok {
			return json.RawMessage(n), nil
		}
	}
	return nil, &kubernetes.Error{Status: 404, Reason: "NotFound", Message: path + " not found"}
}

func (f fakeAPI) List(_ context.Context, path string, _ map[string]string) ([]json.RawMessage, error) {
	switch path {
	case "/api/v1/namespaces/demo/pods":
		var out []json.RawMessage
		for _, p := range f.pods {
			out = append(out, json.RawMessage(p))
		}
		return out, nil
	case "/apis/apps/v1/namespaces/demo/replicasets":
		return []json.RawMessage{json.RawMessage(`{"metadata":{"name":"web-5d4f8","namespace":"demo","ownerReferences":[{"kind":"Deployment","name":"web","controller":true}]}}`)}, nil
	}
	return nil, &kubernetes.Error{Status: 404, Message: path + " not found"}
}

const deployment = `{"kind":"Deployment","metadata":{"name":"web","namespace":"demo"},"spec":{"replicas":2,"selector":{"matchLabels":{"app":"web"}}}}`

type podOptions struct {
	node        string
	fargate     bool
	annotations string
	requests    string // the requests object, "" for none
	init        string
}

func pod(name string, o podOptions) string {
	if o.node == "" {
		o.node = "ip-10-0-1-1.eu-west-1.compute.internal"
	}
	scheduler := "default-scheduler"
	if o.fargate {
		scheduler = "fargate-scheduler"
	}
	if o.annotations == "" {
		o.annotations = "{}"
	}
	resources := "{}"
	if o.requests != "" {
		resources = `{"requests":` + o.requests + `}`
	}
	init := ""
	if o.init != "" {
		init = `,"initContainers":[{"name":"init","resources":{"requests":` + o.init + `}}]`
	}
	return fmt.Sprintf(`{"metadata":{"name":%q,"namespace":"demo","labels":{"app":"web"},"annotations":%s,"ownerReferences":[{"kind":"ReplicaSet","name":"web-5d4f8","controller":true}]},`+
		`"spec":{"nodeName":%q,"schedulerName":%q,"containers":[{"name":"app","resources":%s}]%s},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}`,
		name, o.annotations, o.node, scheduler, resources, init)
}

func nodeJSON(name, typ string, labels string, allocatable string) string {
	if typ == "" {
		typ = "m5.large"
	}
	if allocatable == "" {
		allocatable = `{"cpu":"1930m","memory":"7220184Ki"}`
	}
	extra := ""
	if labels != "" {
		extra = "," + labels
	}
	return fmt.Sprintf(`{"metadata":{"name":%q,"labels":{"node.kubernetes.io/instance-type":%q,"kubernetes.io/os":"linux"%s}},"status":{"allocatable":%s}}`, name, typ, extra, allocatable)
}

func k8sPorts(api fakeAPI) Ports {
	return Ports{KubernetesOf: func(context.Context, string) (kubernetes.Client, error) { return api, nil }}
}

func k8sBackend() Backend {
	return Backend{Kind: "k8s", ID: k8sID, Cluster: "eks-demo", Namespace: "demo", Owner: kubernetes.Owner{Kind: "Deployment", Name: "web"}}
}

func TestAFargatePodIsTheFargateSizeItWasGivenOrItsRequestsPlus256MBRoundedUp(t *testing.T) {
	const fargateNode = "fargate-ip-10-0-3-1.eu-west-1.compute.internal"
	provisioned := `{"CapacityProvisioned":"0.25vCPU 0.5GB"}`
	api := fakeAPI{owner: deployment, pods: []string{
		pod("web-1", podOptions{node: fargateNode, fargate: true, annotations: provisioned, requests: `{"cpu":"100m","memory":"128Mi"}`}),
		pod("web-2", podOptions{node: fargateNode, fargate: true, annotations: provisioned, requests: `{"cpu":"100m","memory":"128Mi"}`}),
		pod("web-3", podOptions{node: fargateNode, fargate: true, requests: `{"cpu":"300m","memory":"700Mi"}`}),
		pod("web-4", podOptions{node: fargateNode, fargate: true, requests: `{"cpu":"250m","memory":"256Mi"}`, init: `{"cpu":"1","memory":"1Gi"}`}),
	}}
	want(t, describe(t, k8sPorts(api), k8sBackend()),
		`{"kind":"fargate","vcpu":0.25,"memoryGb":0.5,"count":2,"arch":"x86_64","os":"Linux"}`,
		`{"kind":"fargate","vcpu":0.5,"memoryGb":1,"count":1,"arch":"x86_64","os":"Linux"}`,
		// The larger of the containers' sum and the largest init container (1 vCPU, 1 GiB + 256 MB gives 2 GB).
		`{"kind":"fargate","vcpu":1,"memoryGb":2,"count":1,"arch":"x86_64","os":"Linux"}`)
}

func TestPodsOnEC2NodesTheirRequestsTheNodesTypeAllocatableAndCapacityTypeAndWhetherAnAutoscalerFreesTheNode(t *testing.T) {
	spot := "ip-10-0-1-1.eu-west-1.compute.internal"
	group := "ip-10-0-1-2.eu-west-1.compute.internal"
	api := fakeAPI{
		owner: deployment,
		pods: []string{
			pod("web-1", podOptions{node: spot, requests: `{"cpu":"250m","memory":"512Mi"}`}),
			pod("web-2", podOptions{node: spot, requests: `{"cpu":"250m","memory":"512Mi"}`}),
			pod("web-3", podOptions{node: group, requests: `{"cpu":"500m","memory":"1Gi"}`}),
		},
		nodes: map[string]string{
			spot:  nodeJSON(spot, "", `"karpenter.sh/nodepool":"default","karpenter.sh/capacity-type":"spot"`, ""),
			group: nodeJSON(group, "t4g.medium", `"eks.amazonaws.com/nodegroup":"system","eks.amazonaws.com/capacityType":"ON_DEMAND"`, `{"cpu":"1930m","memory":"3366880Ki"}`),
		},
	}
	items := describe(t, k8sPorts(api), k8sBackend())
	want(t, items,
		`{"kind":"k8s-pods","count":2,"cpu":0.25,"memoryGb":0.5,"nodeType":"m5.large","os":"Linux","spot":true,"autoscaled":true,"allocatable":{"cpu":1.93,"memoryGb":6.886}}`,
		`{"kind":"k8s-pods","count":1,"cpu":0.5,"memoryGb":1,"nodeType":"t4g.medium","os":"Linux","autoscaled":false,"allocatable":{"cpu":1.93,"memoryGb":3.211}}`)
	// A managed node group under the Cluster Autoscaler (found by discovery) frees its nodes too.
	b := k8sBackend()
	b.Autoscaler = kubernetes.Autoscaler("cluster-autoscaler")
	if autoscaled := describe(t, k8sPorts(api), b); !autoscaled[1].Autoscaled {
		t.Errorf("a node group under the Cluster Autoscaler is autoscaled: %s", asJSON(t, autoscaled))
	}
}

func TestPodsWithoutCPUAndMemoryRequestsAreSaidNotPriced(t *testing.T) {
	api := fakeAPI{owner: deployment, pods: []string{pod("web-1", podOptions{})}}
	want(t, describe(t, k8sPorts(api), k8sBackend()),
		`{"kind":"unknown","backend":"k8s:`+k8sID+`","reason":"pods without CPU and memory requests: their share of the node is unknown"}`)
}

func TestDescribeDescribesAK8sBackendThroughTheClusterAClusterErrorIsAnUnknownItemNeverAFailedNap(t *testing.T) {
	name := "ip-10-0-1-1.eu-west-1.compute.internal"
	api := fakeAPI{
		owner: deployment,
		pods: []string{
			pod("web-1", podOptions{requests: `{"cpu":"250m","memory":"512Mi"}`}),
			pod("web-2", podOptions{requests: `{"cpu":"250m","memory":"512Mi"}`}),
		},
		nodes: map[string]string{name: nodeJSON(name, "", "", "")},
	}
	want(t, describe(t, k8sPorts(api), k8sBackend()),
		`{"kind":"k8s-pods","count":2,"cpu":0.25,"memoryGb":0.5,"nodeType":"m5.large","os":"Linux","autoscaled":false,"allocatable":{"cpu":1.93,"memoryGb":6.886}}`)
	broken := Ports{KubernetesOf: func(context.Context, string) (kubernetes.Client, error) {
		return nil, errors.New("cluster unreachable")
	}}
	want(t, describe(t, broken, k8sBackend()), `{"kind":"unknown","backend":"k8s:`+k8sID+`","reason":"cluster unreachable"}`)
}

func TestAnUnknownItemsReasonIsAtMost200Characters(t *testing.T) {
	broken := Ports{KubernetesOf: func(context.Context, string) (kubernetes.Client, error) {
		return nil, errors.New(strings.Repeat("x", 300))
	}}
	items := describe(t, broken, k8sBackend())
	if len(items) != 1 || len(items[0].Reason) != 200 {
		t.Errorf("reason: %d", len(items[0].Reason))
	}
}

func TestAtMost50ItemsAreKept(t *testing.T) {
	ec2 := fakeEC2{"i-1": {InstanceType: "t3.small"}}
	var backends []Backend
	for range 60 {
		backends = append(backends, Backend{Kind: "instance", ID: "i-1"})
	}
	if items := describe(t, Ports{EC2: ec2}, backends...); len(items) != 50 {
		t.Errorf("items: %d", len(items))
	}
}
