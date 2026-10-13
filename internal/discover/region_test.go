package discover

// Ports manager packages/estimate/test/discover.test.ts (a region's discovery,
// its companion databases, its Kubernetes workloads).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/Dormeto/cli/internal/kubernetes"
	"github.com/Dormeto/cli/internal/stopped"
)

const (
	acctA = "111111111111"
	acctB = "222222222222"
	regR  = "eu-west-1"
)

var lbArn = fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:loadbalancer/app/web/0123456789abcdef", regR, acctA)

func tgOf(name string) string {
	return fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:targetgroup/%s/fedcba9876543210", regR, acctA, name)
}

func sp(s string) *string { return &s }

// scanned is Node's test rule(name, over).
func wl(name string, over ...func(*ScannedRule)) ScannedRule {
	r := ScannedRule{
		ID: tgOf(name), Account: acctA, Region: regR, LoadBalancer: "web", LoadBalancerArn: lbArn, Listener: "HTTPS:443", Priority: "10",
		Conditions: "host " + name + ".example.com", Hosts: []string{name + ".example.com"}, TargetGroup: sp(name), TargetGroupArn: sp(tgOf(name)),
		Backend: &Backend{Kind: "asg", ID: name + "-asg"}, Tags: map[string][]string{}, Rules: []RuleRef{}, Eligibility: "eligible",
	}
	for _, o := range over {
		o(&r)
	}
	return r
}

// fake is the ports of a region at once, as Node's regionPorts: each method has a
// default answer a test overrides with its field.
type fake struct {
	mu         sync.Mutex
	rules      []ScannedRule
	clusters   []kubernetes.Cluster
	desired    int // the Auto Scaling group's desired capacity
	ecsDesired int
	// instance: type, state and security groups of an instance.
	instance  func(id string) (typ, state string, groups []string)
	askedSvc  []string // cluster/service of every Service call
	askedRule [][]string
	askedSG   [][]string
	rulesOf   func(arns []string) ([]Rule, error)
	service   func(cluster, service string) (*ServiceInfo, error)
	ingress   map[string][]string
	rds       DatabasesPort
	k8s       func(ctx context.Context, cluster string) (kubernetes.Client, error)
	delay     func() // called inside every ECS call
}

func newFake(rules ...ScannedRule) *fake {
	f := &fake{rules: rules, desired: 2, ecsDesired: 3}
	f.instance = func(id string) (string, string, []string) {
		if id == "i-9" {
			return "m5.large", "", nil
		}
		return "t3.medium", "", nil
	}
	return f
}

func (f *fake) ports() RegionPorts {
	return RegionPorts{
		Scan: func(context.Context) (ScanResult, error) {
			return ScanResult{Rules: f.rules, Clusters: f.clusters}, nil
		},
		AutoScaling: f, ELBv2: f, EC2: f, ECS: f, RDS: f.rds,
		Stopped: stopped.Ports{EC2: f, ECS: f, KubernetesOf: f.k8s},
	}
}

func (f *fake) Group(_ context.Context, name string) (*Group, error) {
	return &Group{Desired: f.desired, Instances: []string{"i-1", "i-2"}}, nil
}

func (f *fake) DescribeRules(_ context.Context, arns []string) ([]Rule, error) {
	f.mu.Lock()
	f.askedRule = append(f.askedRule, arns)
	f.mu.Unlock()
	if f.rulesOf != nil {
		return f.rulesOf(arns)
	}
	return nil, nil
}

func (f *fake) Instances(_ context.Context, ids []string) ([]InstanceInfo, error) {
	var out []InstanceInfo
	for _, id := range ids {
		_, state, groups := f.instance(id)
		out = append(out, InstanceInfo{ID: id, State: state, SecurityGroups: groups})
	}
	return out, nil
}

func (f *fake) SecurityGroups(_ context.Context, ids []string) ([]SecurityGroup, error) {
	f.mu.Lock()
	f.askedSG = append(f.askedSG, ids)
	f.mu.Unlock()
	var out []SecurityGroup
	for _, id := range ids {
		out = append(out, SecurityGroup{ID: id, AllowedFrom: f.ingress[id]})
	}
	return out, nil
}

func (f *fake) Service(_ context.Context, cluster, service string) (*ServiceInfo, error) {
	f.mu.Lock()
	f.askedSvc = append(f.askedSvc, cluster+"|"+service)
	f.mu.Unlock()
	if f.delay != nil {
		f.delay()
	}
	if f.service != nil {
		return f.service(cluster, service)
	}
	return &ServiceInfo{DesiredCount: f.ecsDesired}, nil
}

// The ports of what a nap stops.
func (f *fake) DescribeInstances(_ context.Context, in stopped.DescribeInstancesInput) (stopped.DescribeInstancesOutput, error) {
	var res stopped.Reservation
	for _, id := range in.InstanceIds {
		typ, _, _ := f.instance(id)
		res.Instances = append(res.Instances, stopped.Instance{InstanceID: id, InstanceType: typ, PlatformDetails: "Linux/UNIX"})
	}
	return stopped.DescribeInstancesOutput{Reservations: []stopped.Reservation{res}}, nil
}

func (f *fake) DescribeServices(_ context.Context, in stopped.DescribeServicesInput) (stopped.DescribeServicesOutput, error) {
	if f.delay != nil {
		f.delay()
	}
	return stopped.DescribeServicesOutput{Services: []stopped.Service{{ServiceArn: in.Services[0], LaunchType: "FARGATE", TaskDefinition: "api:7"}}}, nil
}

func (f *fake) DescribeTaskDefinition(context.Context, stopped.DescribeTaskDefinitionInput) (stopped.DescribeTaskDefinitionOutput, error) {
	return stopped.DescribeTaskDefinitionOutput{TaskDefinition: &stopped.TaskDefinition{CPU: "256", Memory: "512", RuntimePlatform: &stopped.RuntimePlatform{CPUArchitecture: "ARM64", OperatingSystemFamily: "LINUX"}}}, nil
}

func discoverNow(t *testing.T, f *fake) RegionDiscovery {
	t.Helper()
	found, err := Region(context.Background(), f.ports(), Where{acctA, regR})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func same(t *testing.T, what string, got any, want string) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: bad want: %v", what, err)
	}
	if err := json.Unmarshal([]byte(asJSON(t, got)), &g); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(w, g) {
		t.Errorf("%s:\n got  %s\n want %s", what, asJSON(t, got), want)
	}
}

func ecsRule(name, service string, over ...func(*ScannedRule)) ScannedRule {
	return wl(name, append([]func(*ScannedRule){func(r *ScannedRule) {
		r.Backend = &Backend{Kind: "ecs", ID: service, ARN: service}
	}}, over...)...)
}

func serviceArn(name string) string {
	return fmt.Sprintf("arn:aws:ecs:%s:%s:service/prod/%s", regR, acctA, name)
}

func TestItDescribesWhatEachEligibleWorkloadWouldStopAnAutoScalingGroupAnECSServiceAnInstance(t *testing.T) {
	service := serviceArn("api")
	f := newFake(wl("web"), ecsRule("api", service), wl("admin", func(r *ScannedRule) { r.Backend = &Backend{Kind: "instance", ID: "i-9"} }))
	found := discoverNow(t, f)
	var rows [][]any
	for _, w := range found.Workloads {
		rows = append(rows, []any{w.TargetGroup, w.Stopped})
	}
	same(t, "stopped", rows, `[
	  ["web", [{"kind":"ec2","type":"t3.medium","count":2,"os":"Linux"}]],
	  ["api", [{"kind":"fargate","vcpu":0.25,"memoryGb":0.5,"arch":"arm64","os":"Linux","count":3}]],
	  ["admin", [{"kind":"ec2","type":"m5.large","count":1,"os":"Linux"}]]]`)
	// The service's cluster from its ARN.
	if f.askedSvc[0] != "prod|"+service {
		t.Errorf("asked %v", f.askedSvc)
	}
	same(t, "first", found.Workloads[0], fmt.Sprintf(`{"account":%q,"region":%q,"loadBalancer":"web","loadBalancerArn":%q,"targetGroup":"web","targetGroupArn":%q,"hosts":["web.example.com"],"backend":{"kind":"asg","id":"web-asg"},
	  "stopped":[{"kind":"ec2","type":"t3.medium","count":2,"os":"Linux"}],"tags":{},"securityGroups":[]}`, acctA, regR, lbArn, tgOf("web")))
}

func TestAnECSServiceKeepsItsDesiredCountOneAtZeroTasksIsDescribedAsOneTaskUnlessDormetoManagesIt(t *testing.T) {
	service := serviceArn("api")
	at := func(desired int, over ...func(*ScannedRule)) Workload {
		f := newFake(ecsRule("api", service, over...))
		f.ecsDesired = desired
		return discoverNow(t, f).Workloads[0]
	}
	task := func(count int) string {
		return fmt.Sprintf(`[{"kind":"fargate","vcpu":0.25,"memoryGb":0.5,"arch":"arm64","os":"Linux","count":%d}]`, count)
	}
	w := at(3)
	if w.EcsDesired == nil || *w.EcsDesired != 3 {
		t.Errorf("ecsDesired %v", w.EcsDesired)
	}
	same(t, "3", w.Stopped, task(3))
	w = at(0)
	if w.EcsDesired == nil || *w.EcsDesired != 0 {
		t.Errorf("ecsDesired at 0: %v", w.EcsDesired)
	}
	same(t, "0", w.Stopped, task(1))
	ruleArn := fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:listener-rule/app/web/0123456789abcdef/aaaa/dddd", regR, acctA)
	napped := at(0, func(r *ScannedRule) {
		r.Eligibility, r.WorkloadID = "managed", "abc"
		r.Rules = []RuleRef{{ID: ruleArn, Listener: "HTTPS:443", Priority: "10", Hosts: []string{}, Managed: true}}
	})
	if napped.EcsDesired != nil {
		t.Errorf("a managed service keeps no ecsDesired: %v", *napped.EcsDesired)
	}
	same(t, "managed", napped.Stopped, `[]`)
}

// A region of hundreds of workloads described one after the other kept the last accounts long after the others.
func TestItDescribesARegionsWorkloadsEightAtATimeInTheirOrder(t *testing.T) {
	var rules []ScannedRule
	var names []string
	for i := range 20 {
		rules = append(rules, ecsRule(fmt.Sprintf("svc%d", i), serviceArn(fmt.Sprintf("svc%d", i))))
		names = append(names, fmt.Sprintf("svc%d", i))
	}
	f := newFake(rules...)
	var mu sync.Mutex
	active, most := 0, 0
	f.delay = func() {
		mu.Lock()
		active++
		most = max(most, active)
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
	}
	found := discoverNow(t, f)
	var got []string
	for _, w := range found.Workloads {
		got = append(got, w.TargetGroup)
	}
	if !reflect.DeepEqual(got, names) {
		t.Errorf("order %v", got)
	}
	if most != 8 {
		t.Errorf("most at once: %d", most)
	}
}

func TestItKeepsEachWorkloadsTags(t *testing.T) {
	f := newFake(wl("web", func(r *ScannedRule) { r.Tags = map[string][]string{"env": {"dev"}, "team": {"web", "shop"}} }))
	same(t, "tags", discoverNow(t, f).Workloads[0].Tags, `{"env":["dev"],"team":["web","shop"]}`)
}

func inState(t *testing.T, state string) Workload {
	f := newFake(wl("admin", func(r *ScannedRule) { r.Backend = &Backend{Kind: "instance", ID: "i-9"} }))
	f.instance = func(string) (string, string, []string) { return "m5.large", state, nil }
	return discoverNow(t, f).Workloads[0]
}

func TestAnInstanceKeepsItsStateOneAlreadyStoppedIsDescribedAllTheSame(t *testing.T) {
	item := `[{"kind":"ec2","type":"m5.large","count":1,"os":"Linux"}]`
	for _, state := range []string{"running", "stopped"} {
		w := inState(t, state)
		if w.InstanceState != state {
			t.Errorf("%s: state %q", state, w.InstanceState)
		}
		same(t, state, w.Stopped, item)
	}
	// No state said: running, as before.
	if w := inState(t, ""); w.InstanceState != "running" {
		t.Errorf("no state: %q", w.InstanceState)
	}
}

func TestATerminatedInstanceStopsNothing(t *testing.T) {
	w := inState(t, "terminated")
	if w.InstanceState != "terminated" {
		t.Errorf("state %q", w.InstanceState)
	}
	same(t, "stopped", w.Stopped, `[]`)
}

func TestAnInstanceDormetoManagesThatIsStoppedIsNappingStillEstimated(t *testing.T) {
	ruleArn := fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:listener-rule/app/web/0123456789abcdef/aaaa/cccc", regR, acctA)
	f := newFake(wl("napping", func(r *ScannedRule) {
		r.Eligibility, r.WorkloadID = "managed", "abc"
		r.Backend = &Backend{Kind: "instance", ID: "i-9"}
		r.Rules = []RuleRef{{ID: ruleArn, Listener: "HTTPS:443", Priority: "10", Hosts: []string{}, Managed: true}}
	}))
	f.instance = func(string) (string, string, []string) { return "m5.large", "stopped", nil }
	f.rulesOf = func([]string) ([]Rule, error) { return nil, nil }
	same(t, "stopped", discoverNow(t, f).Workloads[0].Stopped, `[{"kind":"ec2","type":"m5.large","count":1,"os":"Linux"}]`)
}

func TestAnAutoScalingGroupAtADesiredCapacityOfZeroStopsNothing(t *testing.T) {
	f := newFake(wl("web"))
	f.desired = 0
	same(t, "stopped", discoverNow(t, f).Workloads[0].Stopped, `[]`)
}

func TestItKeepsTheIneligibleRulesWithDiscoverysReason(t *testing.T) {
	f := newFake(wl("shared", func(r *ScannedRule) {
		r.Eligibility, r.Reason = "ineligible", "its target group is shared by another rule"
	}))
	found := discoverNow(t, f)
	if len(found.Workloads) != 0 {
		t.Errorf("workloads %v", found.Workloads)
	}
	same(t, "ineligible", found.Ineligible, `[{"loadBalancer":"web","targetGroup":"shared","hosts":["shared.example.com"],"reason":"its target group is shared by another rule"}]`)
}

func TestItEstimatesAWorkloadDormetoAlreadyManagesTooWithItsWakerTargetGroupFromItsRulesForward(t *testing.T) {
	ruleArn := fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:listener-rule/app/web/0123456789abcdef/aaaa/bbbb", regR, acctA)
	waker := fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:targetgroup/dmtr-dev-napping/0000000000000000", regR, acctA)
	f := newFake(wl("napping", func(r *ScannedRule) {
		r.Eligibility, r.WorkloadID = "managed", "abc"
		r.Rules = []RuleRef{{ID: ruleArn, Listener: "HTTPS:443", Priority: "10", Hosts: []string{}, Managed: true}}
	}))
	zero, one := 0, 1
	f.rulesOf = func([]string) ([]Rule, error) {
		return []Rule{{RuleArn: ruleArn, Actions: []Action{{Type: "forward", ForwardConfig: &ForwardConfig{TargetGroups: []ForwardTarget{{TargetGroupArn: tgOf("napping"), Weight: &zero}, {TargetGroupArn: waker, Weight: &one}}}}}}}, nil
	}
	found := discoverNow(t, f)
	if !reflect.DeepEqual(f.askedRule, [][]string{{ruleArn}}) {
		t.Errorf("asked %v", f.askedRule)
	}
	if len(found.Ineligible) != 0 {
		t.Errorf("ineligible %v", found.Ineligible)
	}
	w := found.Workloads[0]
	if w.TargetGroup != "napping" || !w.Managed || w.WakerTargetGroupArn != waker {
		t.Errorf("workload %+v", w)
	}
	same(t, "stopped", w.Stopped, `[{"kind":"ec2","type":"t3.medium","count":2,"os":"Linux"}]`)
}

// A managed workload with no forward of two target groups has no waker (its requests while asleep are not counted).
func TestAManagedWorkloadWithoutAWakerForwardIsManagedAllTheSame(t *testing.T) {
	f := newFake(wl("napping", func(r *ScannedRule) {
		r.Eligibility = "managed"
		r.Rules = []RuleRef{{ID: "arn:rule", Managed: false}, {ID: "arn:rule2", Managed: true}}
	}))
	f.rulesOf = func([]string) ([]Rule, error) { return nil, nil }
	w := discoverNow(t, f).Workloads[0]
	if !w.Managed || w.WakerTargetGroupArn != "" {
		t.Errorf("%+v", w)
	}
	// The rule tagged by Dormeto is the one read.
	if !reflect.DeepEqual(f.askedRule, [][]string{{"arn:rule2"}}) {
		t.Errorf("asked %v", f.askedRule)
	}
}

// Companion databases: an RDS instance or an Aurora provisioned cluster whose security group lets a workload's in.
type fakeRDS struct {
	instances []DBInstance
	clusters  []DBCluster
	failing   error
}

func (r fakeRDS) DescribeDBInstances(_ context.Context, marker string) ([]DBInstance, string, error) {
	if r.failing != nil {
		return nil, "", r.failing
	}
	if marker != "" {
		return r.instances[6:], "", nil
	}
	return r.instances[:6], "next", nil
}

func (r fakeRDS) DescribeDBClusters(context.Context, string) ([]DBCluster, string, error) {
	return r.clusters, "", nil
}

func db(id string, over ...func(*DBInstance)) DBInstance {
	d := DBInstance{DBInstanceIdentifier: id, DBInstanceStatus: "available", Engine: "mysql", DBInstanceClass: "db.t3.medium", VpcSecurityGroups: []VpcSecurityGroup{{"sg-db", "active"}}}
	for _, o := range over {
		o(&d)
	}
	return d
}

func databasesWorld(rds DatabasesPort) *fake {
	service := serviceArn("api")
	f := newFake(wl("web"), ecsRule("api", service), wl("admin", func(r *ScannedRule) { r.Backend = &Backend{Kind: "instance", ID: "i-9"} }))
	sgOf := map[string]string{"i-9": "sg-app", "i-1": "sg-web", "i-2": "sg-web"}
	f.instance = func(id string) (string, string, []string) {
		typ := "t3.medium"
		if id == "i-9" {
			typ = "m5.large"
		}
		return typ, "", []string{sgOf[id]}
	}
	f.ingress = map[string][]string{"sg-db": {"sg-web"}, "sg-db2": {"sg-api"}, "sg-aur": {"sg-app"}, "sg-other": {"sg-elsewhere"}}
	f.service = func(cluster, service string) (*ServiceInfo, error) {
		return &ServiceInfo{DesiredCount: 3, SecurityGroups: []string{"sg-api"}}, nil
	}
	f.rds = rds
	return f
}

func TestEachWorkloadsSecurityGroupsAndTheDatabasesWhoseSecurityGroupLetsOneIn(t *testing.T) {
	aurora := func(d *DBInstance) {
		d.DBClusterIdentifier, d.Engine, d.DBInstanceClass = "aurora", "aurora-postgresql", "db.r6g.large"
	}
	instances := []DBInstance{
		db("shop-db", func(d *DBInstance) { d.TagList = []Tag{{"env", "dev"}} }),
		db("replica", func(d *DBInstance) { d.ReadReplicaSourceDBInstanceIdentifier = "shop-db" }),
		db("with-replicas", func(d *DBInstance) {
			d.ReadReplicaDBInstanceIdentifiers = []string{"far"}
			d.VpcSecurityGroups = []VpcSecurityGroup{{"sg-db2", "active"}}
		}),
		db("other-db", func(d *DBInstance) { d.VpcSecurityGroups = []VpcSecurityGroup{{"sg-other", "active"}} }),
		db("night-db", func(d *DBInstance) { d.DBInstanceStatus = "stopped" }),
		db("new-db", func(d *DBInstance) { d.DBInstanceStatus = "creating" }),
		db("aur-1", aurora), db("aur-2", aurora),
		db("sls-1", func(d *DBInstance) {
			d.DBClusterIdentifier, d.Engine, d.DBInstanceClass = "aurora-sls", "aurora-postgresql", "db.serverless"
		}),
	}
	clusters := []DBCluster{
		{DBClusterIdentifier: "aurora", Status: "available", Engine: "aurora-postgresql", EngineMode: "provisioned", VpcSecurityGroups: []VpcSecurityGroup{{"sg-aur", "active"}}, TagList: []Tag{{"team", "shop"}}},
		{DBClusterIdentifier: "aurora-sls", Status: "available", Engine: "aurora-postgresql", EngineMode: "provisioned", VpcSecurityGroups: []VpcSecurityGroup{{"sg-aur", "active"}}},
	}
	f := databasesWorld(fakeRDS{instances: instances, clusters: clusters})
	found := discoverNow(t, f)
	var groups [][]any
	for _, w := range found.Workloads {
		groups = append(groups, []any{w.TargetGroup, w.SecurityGroups})
	}
	same(t, "groups", groups, `[["web",["sg-web"]],["api",["sg-api"]],["admin",["sg-app"]]]`)
	item := func(cls, engine string) string {
		return fmt.Sprintf(`{"kind":"rds","class":%q,"engine":%q,"multiAz":false}`, cls, engine)
	}
	// A database stopped when discovered (a schedule may start it) is a companion all the same: its uptime is measured.
	same(t, "databases", found.Databases, fmt.Sprintf(`[
	  {"kind":"rds-instance","id":"shop-db","status":"available","allowedFrom":["sg-web"],"tags":{"env":["dev"]},"stopped":[%[1]s]},
	  {"kind":"rds-instance","id":"replica","status":"available","allowedFrom":["sg-web"],"tags":{},"stopped":[],"notStoppable":"a read replica: cannot be stopped"},
	  {"kind":"rds-instance","id":"with-replicas","status":"available","allowedFrom":["sg-api"],"tags":{},"stopped":[],"notStoppable":"it has read replicas: cannot be stopped"},
	  {"kind":"rds-instance","id":"night-db","status":"stopped","allowedFrom":["sg-web"],"tags":{},"stopped":[%[1]s]},
	  {"kind":"rds-instance","id":"new-db","status":"creating","allowedFrom":["sg-web"],"tags":{},"stopped":[],"notStoppable":"creating when discovered"},
	  {"kind":"rds-cluster","id":"aurora","status":"available","allowedFrom":["sg-app"],"tags":{"team":["shop"]},"stopped":[%[2]s,%[2]s]},
	  {"kind":"rds-cluster","id":"aurora-sls","status":"available","allowedFrom":["sg-app"],"tags":{},"stopped":[],"notStoppable":"Aurora Serverless: not yet"}]`,
		item("db.t3.medium", "mysql"), item("db.r6g.large", "aurora-postgresql")))
	if found.DatabasesError != "" {
		t.Errorf("databasesError %q", found.DatabasesError)
	}
}

func TestWithoutRDSRightsTheDatabasesAreNotReadSaidWithoutAnRDSPortNone(t *testing.T) {
	denied := fakeRDS{failing: &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized to perform: rds:DescribeDBInstances"}}
	found := discoverNow(t, databasesWorld(denied))
	if len(found.Databases) != 0 || found.Databases == nil || found.DatabasesError != "AccessDenied: not authorized to perform: rds:DescribeDBInstances" {
		t.Errorf("%#v %q", found.Databases, found.DatabasesError)
	}
	none := discoverNow(t, databasesWorld(nil))
	if none.Databases == nil || len(none.Databases) != 0 || none.DatabasesError != "" {
		t.Errorf("without a port: %#v %q", none.Databases, none.DatabasesError)
	}
}

// The other rules of the databases the Node tests leave to the shared cases.
func TestTheRestOfTheDatabasesRules(t *testing.T) {
	instances := []DBInstance{
		db("sqlserver-az", func(d *DBInstance) { d.Engine, d.MultiAZ = "sqlserver-se", true }),
		db("inactive", func(d *DBInstance) { d.VpcSecurityGroups = []VpcSecurityGroup{{"sg-db", "inactive"}} }),
		db("member", func(d *DBInstance) { d.DBClusterIdentifier, d.Engine = "multi", "postgres" }),
		db("lonely-member", func(d *DBInstance) { d.DBClusterIdentifier = "other" }),
		db("a"), db("b"),
	}
	clusters := []DBCluster{
		{DBClusterIdentifier: "multi", Status: "available", Engine: "postgres", VpcSecurityGroups: []VpcSecurityGroup{{"sg-db", "active"}}},
		{DBClusterIdentifier: "empty", Status: "available", Engine: "aurora-mysql", VpcSecurityGroups: []VpcSecurityGroup{{"sg-db", "active"}}},
		{DBClusterIdentifier: "creating", Status: "creating", Engine: "aurora-mysql", VpcSecurityGroups: []VpcSecurityGroup{{"sg-db", "active"}}},
		{DBClusterIdentifier: "classic", Status: "available", Engine: "aurora-mysql", EngineMode: "serverless", VpcSecurityGroups: []VpcSecurityGroup{{"sg-db", "active"}}},
	}
	found := discoverNow(t, databasesWorld(fakeRDS{instances: instances, clusters: clusters}))
	got := map[string]string{}
	for _, d := range found.Databases {
		got[d.ID] = d.NotStoppable
	}
	want := map[string]string{
		"sqlserver-az": "SQL Server Multi-AZ: cannot be stopped",
		"a":            "", "b": "",
		"multi": "a Multi-AZ DB cluster: not yet", "empty": "no instance", "creating": "creating when discovered", "classic": "Aurora Serverless: not yet",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	// A security group Status inactive is not followed: the instance has no group letting anything in.
	if _, ok := got["inactive"]; ok {
		t.Error("an inactive group was followed")
	}
	// The groups asked of EC2 are the databases' own, once each.
}

func TestTheDatabasesSecurityGroupsAreAskedOfEC2BatchesOfOneHundred(t *testing.T) {
	var instances []DBInstance
	for i := range 250 {
		id := fmt.Sprintf("sg-%03d", i)
		instances = append(instances, db(fmt.Sprintf("db-%d", i), func(d *DBInstance) { d.VpcSecurityGroups = []VpcSecurityGroup{{id, "active"}} }))
	}
	f := databasesWorld(pagedRDS{instances})
	discoverNow(t, f)
	var sizes []int
	for _, a := range f.askedSG {
		sizes = append(sizes, len(a))
	}
	if !reflect.DeepEqual(sizes, []int{100, 100, 50}) {
		t.Errorf("batches %v", sizes)
	}
}

type pagedRDS struct{ instances []DBInstance }

func (p pagedRDS) DescribeDBInstances(context.Context, string) ([]DBInstance, string, error) {
	return p.instances, "", nil
}
func (p pagedRDS) DescribeDBClusters(context.Context, string) ([]DBCluster, string, error) {
	return nil, "", nil
}

func TestAFailureOfAWorkloadsDescriptionIsAnUnknownItemNeverGuessed(t *testing.T) {
	f := newFake(ecsRule("api", serviceArn("api")))
	f.service = func(string, string) (*ServiceInfo, error) {
		return nil, &smithy.GenericAPIError{Code: "ThrottlingException", Message: strings.Repeat("x", 300)}
	}
	w := discoverNow(t, f).Workloads[0]
	if len(w.Stopped) != 1 || w.Stopped[0].Kind != "unknown" || w.Stopped[0].Backend != "ecs:"+serviceArn("api") || len(w.Stopped[0].Reason) != 200 {
		t.Errorf("%+v", w.Stopped)
	}
	if len(w.SecurityGroups) != 0 || w.SecurityGroups == nil {
		t.Errorf("security groups %#v", w.SecurityGroups)
	}
}

func TestADescribeRulesFailureOfAManagedWorkloadFailsTheRegion(t *testing.T) {
	f := newFake(wl("napping", func(r *ScannedRule) {
		r.Eligibility = "managed"
		r.Rules = []RuleRef{{ID: "arn:rule", Managed: true}}
	}))
	f.rulesOf = func([]string) ([]Rule, error) { return nil, errors.New("rules refused") }
	if _, err := Region(context.Background(), f.ports(), Where{acctA, regR}); err == nil || err.Error() != "rules refused" {
		t.Errorf("err %v", err)
	}
}

// Kubernetes workloads: read with the profile's own access.
func k8sRule(name, namespace string) ScannedRule {
	capacity := "nodes"
	if namespace == "fargate" {
		capacity = "fargate"
	}
	return wl(name, func(r *ScannedRule) {
		r.Backend = &Backend{Kind: "k8s", ID: "eks-demo/" + namespace + "/Deployment/" + name, Kubernetes: &kubernetes.Candidate{
			Cluster: "eks-demo", Namespace: namespace, Owner: kubernetes.Owner{Kind: "Deployment", Name: name}, Switch: "ingress", Capacity: capacity, Autoscaler: "karpenter",
		}}
	})
}

// demoCluster is the cluster as the router's client reads it: the owners, their ReplicaSets and pods (on Fargate or a
// Karpenter node), the node.
type demoCluster struct{}

func (demoCluster) Get(_ context.Context, path string) (json.RawMessage, error) {
	switch {
	case strings.HasSuffix(path, "/deployments/web") || strings.HasSuffix(path, "/deployments/docs"):
		app := path[strings.LastIndex(path, "/")+1:]
		return json.RawMessage(fmt.Sprintf(`{"spec":{"replicas":1,"selector":{"matchLabels":{"app":%q}}}}`, app)), nil
	case strings.HasPrefix(path, "/api/v1/nodes/"):
		return json.RawMessage(`{"metadata":{"labels":{"node.kubernetes.io/instance-type":"m5.large","kubernetes.io/os":"linux","karpenter.sh/nodepool":"demo"}},"status":{"allocatable":{"cpu":"1930m","memory":"7220184Ki"}}}`), nil
	}
	return nil, &kubernetes.Error{Status: 404, Message: path + " not found"}
}

func (demoCluster) List(_ context.Context, path string, query map[string]string) ([]json.RawMessage, error) {
	selector := query["labelSelector"]
	owned := func(app string) string {
		return fmt.Sprintf(`"ownerReferences":[{"kind":"ReplicaSet","name":"%s-5d4f8","controller":true}]`, app)
	}
	if strings.HasPrefix(path, "/apis/apps/v1/namespaces/") && strings.HasSuffix(path, "/replicasets") && strings.HasPrefix(selector, "app=") {
		app := selector[4:]
		return []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"metadata":{"name":"%s-5d4f8","ownerReferences":[{"kind":"Deployment","name":%q,"controller":true}]}}`, app, app))}, nil
	}
	if path == "/api/v1/namespaces/demo/pods" && selector == "app=web" {
		return []json.RawMessage{json.RawMessage(`{"metadata":{"name":"web-1","labels":{"app":"web"},` + owned("web") + `},"spec":{"nodeName":"ip-10-0-1-1","containers":[{"resources":{"requests":{"cpu":"250m","memory":"512Mi"}}}]},"status":{"phase":"Running"}}`)}, nil
	}
	if path == "/api/v1/namespaces/fargate/pods" && selector == "app=docs" {
		return []json.RawMessage{json.RawMessage(`{"metadata":{"name":"docs-1","labels":{"app":"docs"},"annotations":{"CapacityProvisioned":"0.25vCPU 0.5GB"},` + owned("docs") + `},"spec":{"schedulerName":"fargate-scheduler","nodeName":"fargate-ip-1","containers":[{"resources":{"requests":{"cpu":"250m","memory":"512Mi"}}}]},"status":{"phase":"Running"}}`)}, nil
	}
	return nil, nil
}

func TestItDescribesThePodsANapWouldStopOnANodeItsShareOnFargateItsSize(t *testing.T) {
	f := newFake(k8sRule("web", "demo"), k8sRule("docs", "fargate"))
	f.k8s = func(context.Context, string) (kubernetes.Client, error) { return demoCluster{}, nil }
	found := discoverNow(t, f)
	var rows [][]any
	for _, w := range found.Workloads {
		rows = append(rows, []any{w.TargetGroup, w.Backend, w.Stopped})
	}
	same(t, "k8s", rows, `[
	  ["web", {"kind":"k8s","id":"eks-demo/demo/Deployment/web"}, [{"kind":"k8s-pods","count":1,"cpu":0.25,"memoryGb":0.5,"nodeType":"m5.large","os":"Linux","autoscaled":true,"allocatable":{"cpu":1.93,"memoryGb":6.886}}]],
	  ["docs", {"kind":"k8s","id":"eks-demo/fargate/Deployment/docs"}, [{"kind":"fargate","vcpu":0.25,"memoryGb":0.5,"count":1,"arch":"x86_64","os":"Linux"}]]]`)
}

func TestItListsTheClustersTheProfileCannotReadWithTheReason(t *testing.T) {
	reason := "the cluster does not let the profile read it (an access entry with AmazonEKSViewPolicy): Unauthorized"
	f := newFake()
	f.clusters = []kubernetes.Cluster{{Name: "eks-ok"}, {Name: "eks-prod", Reason: reason}}
	found := discoverNow(t, f)
	same(t, "clusters", found.Clusters, fmt.Sprintf(`[{"name":"eks-prod","reason":%q}]`, reason))
}
