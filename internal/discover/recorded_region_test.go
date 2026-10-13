package discover

// A region's discovery driven by AWS-shaped recorded answers: the entries of
// testdata/parity/estimate.json (the Node estimate's world), which the
// end-to-end case replays. The workloads and databases must be those the Node
// estimate wrote in testdata/parity/cache.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Dormeto/cli/internal/parity"
	"github.com/Dormeto/cli/internal/stopped"
)

// recordedRegion answers the region's AWS calls from recorded answers; every
// recorded input is {"account", "region", "input": <the call's input>}.
type recordedRegion struct {
	t               *testing.T
	a               *parity.Answers
	account, region string
}

func (r recordedRegion) call(port, method string, in, out any) error {
	r.t.Helper()
	return parity.AWSError(r.a.Call(port, method, map[string]any{"account": r.account, "region": r.region, "input": in}, out))
}

func (r recordedRegion) DescribeAutoScalingGroups(_ context.Context, in DescribeAutoScalingGroupsInput) (out DescribeAutoScalingGroupsOutput, err error) {
	return out, r.call("autoscaling", "describeAutoScalingGroups", in, &out)
}
func (r recordedRegion) DescribeRules(_ context.Context, in DescribeRulesInput) (out DescribeRulesOutput, err error) {
	return out, r.call("elbv2", "describeRules", in, &out)
}
func (r recordedRegion) DescribeSecurityGroups(_ context.Context, in DescribeSecurityGroupsInput) (out DescribeSecurityGroupsOutput, err error) {
	return out, r.call("ec2", "describeSecurityGroups", in, &out)
}
func (r recordedRegion) DescribeInstances(_ context.Context, in stopped.DescribeInstancesInput) (out stopped.DescribeInstancesOutput, err error) {
	return out, r.call("ec2", "describeInstances", in, &out)
}
func (r recordedRegion) DescribeServices(_ context.Context, in stopped.DescribeServicesInput) (out stopped.DescribeServicesOutput, err error) {
	return out, r.call("ecs", "describeServices", in, &out)
}
func (r recordedRegion) DescribeTaskDefinition(_ context.Context, in stopped.DescribeTaskDefinitionInput) (out stopped.DescribeTaskDefinitionOutput, err error) {
	return out, r.call("ecs", "describeTaskDefinition", in, &out)
}
func (r recordedRegion) DescribeDBInstances(_ context.Context, in stopped.DescribeDBInstancesInput) (out stopped.DescribeDBInstancesOutput, err error) {
	return out, r.call("rds", "describeDBInstances", in, &out)
}
func (r recordedRegion) DescribeDBClusters(_ context.Context, in stopped.DescribeDBClustersInput) (out stopped.DescribeDBClustersOutput, err error) {
	return out, r.call("rds", "describeDBClusters", in, &out)
}

func TestARegionDrivenByTheEstimateCasesRecordedAnswersGivesTheWorkloadsAndDatabasesNodeWrote(t *testing.T) {
	var in struct {
		Answers json.RawMessage `json:"answers"`
	}
	c := parity.Load(t, "estimate").Cases[0]
	if err := json.Unmarshal(c.Input, &in); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "parity", "cache", "o-a1b2c3d4e5", "discovery", acctA+"-"+regR+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Value RegionDiscovery `json:"value"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	// The scan's rules for the workloads Node wrote (the Kubernetes ones need the clusters' recorded answers).
	var rules []ScannedRule
	var want []Workload
	for _, w := range env.Value.Workloads {
		if w.Backend.Kind == KindK8s {
			continue
		}
		want = append(want, w)
		r := ScannedRule{
			ID: w.TargetGroupArn, Account: w.Account, Region: w.Region, LoadBalancer: w.LoadBalancer, LoadBalancerArn: w.LoadBalancerArn,
			Hosts: w.Hosts, TargetGroup: sp(w.TargetGroup), TargetGroupArn: sp(w.TargetGroupArn), Backend: &Backend{Kind: w.Backend.Kind, ID: w.Backend.ID},
			Tags: w.Tags, Eligibility: Eligible,
		}
		if w.Managed {
			r.Eligibility = Managed
			r.Rules = []RuleRef{{ID: "arn:aws:elasticloadbalancing:eu-west-1:111111111111:listener-rule/app/web/0123456789abcdef/443bb443bb443bb4/000000000000000e", Managed: true}}
		}
		rules = append(rules, r)
	}
	rec := recordedRegion{t, parity.Recorded(t, in.Answers), acctA, regR}
	ports := RegionPorts{
		Scan:        func(context.Context) (ScanResult, error) { return ScanResult{Rules: rules}, nil },
		AutoScaling: rec, ELBv2: rec, EC2: rec,
		Stopped: stopped.Ports{EC2: rec, ECS: rec, RDS: rec},
	}
	got, err := Region(context.Background(), ports, Where{acctA, regR})
	if err != nil {
		t.Fatal(err)
	}
	same(t, "workloads", got.Workloads, asJSON(t, want))
	same(t, "databases", got.Databases, asJSON(t, env.Value.Databases))
	if len(got.Databases) == 0 || got.DatabasesError != "" {
		t.Errorf("databases %d, error %q", len(got.Databases), got.DatabasesError)
	}
}

// A done context (Ctrl-C) while the databases are read: the region is not a
// result, and nothing is cached for it.
type cancellingRDS struct{ cancel func() }

func (c cancellingRDS) DescribeDBInstances(ctx context.Context, _ stopped.DescribeDBInstancesInput) (stopped.DescribeDBInstancesOutput, error) {
	c.cancel()
	return stopped.DescribeDBInstancesOutput{}, ctx.Err()
}
func (c cancellingRDS) DescribeDBClusters(context.Context, stopped.DescribeDBClustersInput) (stopped.DescribeDBClustersOutput, error) {
	return stopped.DescribeDBClustersOutput{}, nil
}

func TestADoneContextIsTheRegionsErrorNotADatabasesErrorAndNothingIsCached(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := databasesWorld(cancellingRDS{cancel})
	if _, err := Region(ctx, f.ports(), Where{acctA, regR}); err != context.Canceled {
		t.Fatalf("err %v", err)
	}
	// Through All: the context's error, no file, and the next run scans again.
	w := newRun(t, func(w *run) { w.regions = []string{regR} })
	ctx, cancel = context.WithCancel(context.Background())
	o := w.options(runOver{})
	o.PortsOf = func(context.Context, string, string, *aws.Credentials) (RegionPorts, error) {
		return databasesWorld(cancellingRDS{cancel}).ports(), nil
	}
	if _, err := All(ctx, o); err != context.Canceled {
		t.Fatalf("All: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "discovery", acctA+"-"+regR+".json")); err == nil {
		t.Error("a cancelled region was cached")
	}
	w.run()
	if want := []string{acctA + "/" + regR}; len(w.scanned()) != 1 || w.scanned()[0] != want[0] {
		t.Errorf("scans %v", w.scanned())
	}
}
