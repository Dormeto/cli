package cli

// The end-to-end case: each case of the shared `estimate` topic (testdata/parity/estimate.json) run through Main and
// every package under it, AWS answered from the case's recorded answers as the manager's export wires the Node
// estimate (scripts/parity/cases/estimate.mjs, as bin.ts): the exit code, the reports written (the JSON without its
// run time and tool, the Markdown without its time line), the lines logged and the errors are Node's. Once with an
// empty cache, once with the cache the Node estimate wrote for the first case (testdata/parity/cache): the same exit
// code, reports and errors (the log then says what came from the cache).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/kubernetes"
	"github.com/Dormeto/cli/internal/kubernetes/kubernetestest"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/options"
	"github.com/Dormeto/cli/internal/parity"
	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/stopped"
)

// global is the region of STS, Organizations, the Price List and the enabled regions.
const global = "us-east-1"

// world is the recorded AWS of a case: a call is answered by the first entry of equal input, its input scoped as
// {"account", "region", "input"} (the account whose credentials make the call, the client's region); a call without
// an answer fails the test, even where the code would go on without it.
type world struct {
	t        *testing.T
	answers  *parity.Answers
	now      time.Time
	profiles map[string]string // a profile's own account
}

type scope struct {
	w               *world
	account, region string
}

// missing fails the test on a call without a recorded answer (a recorded failure is the call's error).
func (w *world) missing(err error) {
	if err != nil && strings.HasPrefix(err.Error(), "parity: ") {
		w.t.Errorf("%v", err)
	}
}

func call[O any](s scope, port, method string, in any) (O, error) {
	var out O
	err := s.w.answers.Call(port, method, map[string]any{"account": s.account, "region": s.region, "input": in}, &out)
	s.w.missing(err)
	return out, parity.AWSError(err)
}

// The ports of an account's region, one type per Go port (their methods share names).
type (
	elb        scope
	asg        scope
	scanEC2    scope
	scanECS    scope
	scanCW     scope
	regionEC2  scope
	stoppedECS scope
	rds        scope
	metricsCW  scope
	pricing    scope
	org        scope
	sts        scope
)

func (s elb) DescribeLoadBalancers(_ context.Context, in discover.DescribeLoadBalancersInput) (discover.DescribeLoadBalancersOutput, error) {
	return call[discover.DescribeLoadBalancersOutput](scope(s), "elbv2", "describeLoadBalancers", in)
}
func (s elb) DescribeListeners(_ context.Context, in discover.DescribeListenersInput) (discover.DescribeListenersOutput, error) {
	return call[discover.DescribeListenersOutput](scope(s), "elbv2", "describeListeners", in)
}
func (s elb) DescribeRules(_ context.Context, in discover.DescribeRulesInput) (discover.DescribeRulesOutput, error) {
	return call[discover.DescribeRulesOutput](scope(s), "elbv2", "describeRules", in)
}
func (s elb) DescribeTags(_ context.Context, in discover.DescribeTagsInput) (discover.DescribeTagsOutput, error) {
	return call[discover.DescribeTagsOutput](scope(s), "elbv2", "describeTags", in)
}
func (s elb) DescribeTargetGroups(_ context.Context, in discover.DescribeTargetGroupsInput) (discover.DescribeTargetGroupsOutput, error) {
	return call[discover.DescribeTargetGroupsOutput](scope(s), "elbv2", "describeTargetGroups", in)
}
func (s elb) DescribeTargetHealth(_ context.Context, in discover.DescribeTargetHealthInput) (discover.DescribeTargetHealthOutput, error) {
	return call[discover.DescribeTargetHealthOutput](scope(s), "elbv2", "describeTargetHealth", in)
}
func (s asg) DescribeAutoScalingGroups(_ context.Context, in discover.DescribeAutoScalingGroupsInput) (discover.DescribeAutoScalingGroupsOutput, error) {
	return call[discover.DescribeAutoScalingGroupsOutput](scope(s), "autoscaling", "describeAutoScalingGroups", in)
}
func (s scanEC2) DescribeInstances(_ context.Context, in discover.DescribeInstancesInput) (discover.DescribeInstancesOutput, error) {
	return call[discover.DescribeInstancesOutput](scope(s), "ec2", "describeInstances", in)
}
func (s scanECS) ListClusters(_ context.Context, in discover.ListClustersInput) (discover.ListClustersOutput, error) {
	return call[discover.ListClustersOutput](scope(s), "ecs", "listClusters", in)
}
func (s scanECS) ListServices(_ context.Context, in discover.ListServicesInput) (discover.ListServicesOutput, error) {
	return call[discover.ListServicesOutput](scope(s), "ecs", "listServices", in)
}
func (s scanECS) DescribeServices(_ context.Context, in discover.DescribeServicesInput) (discover.DescribeServicesOutput, error) {
	return call[discover.DescribeServicesOutput](scope(s), "ecs", "describeServices", in)
}
func (s scanCW) GetMetricData(_ context.Context, in discover.GetMetricDataInput) (discover.GetMetricDataOutput, error) {
	return call[discover.GetMetricDataOutput](scope(s), "cloudwatch", "getMetricData", in)
}
func (s regionEC2) DescribeInstances(_ context.Context, in stopped.DescribeInstancesInput) (stopped.DescribeInstancesOutput, error) {
	return call[stopped.DescribeInstancesOutput](scope(s), "ec2", "describeInstances", in)
}
func (s regionEC2) DescribeSecurityGroups(_ context.Context, in discover.DescribeSecurityGroupsInput) (discover.DescribeSecurityGroupsOutput, error) {
	return call[discover.DescribeSecurityGroupsOutput](scope(s), "ec2", "describeSecurityGroups", in)
}
func (s stoppedECS) DescribeServices(_ context.Context, in stopped.DescribeServicesInput) (stopped.DescribeServicesOutput, error) {
	return call[stopped.DescribeServicesOutput](scope(s), "ecs", "describeServices", in)
}
func (s stoppedECS) DescribeTaskDefinition(_ context.Context, in stopped.DescribeTaskDefinitionInput) (stopped.DescribeTaskDefinitionOutput, error) {
	return call[stopped.DescribeTaskDefinitionOutput](scope(s), "ecs", "describeTaskDefinition", in)
}
func (s rds) DescribeDBInstances(_ context.Context, in stopped.DescribeDBInstancesInput) (stopped.DescribeDBInstancesOutput, error) {
	return call[stopped.DescribeDBInstancesOutput](scope(s), "rds", "describeDBInstances", in)
}
func (s rds) DescribeDBClusters(_ context.Context, in stopped.DescribeDBClustersInput) (stopped.DescribeDBClustersOutput, error) {
	return call[stopped.DescribeDBClustersOutput](scope(s), "rds", "describeDBClusters", in)
}
func (s metricsCW) GetMetricData(_ context.Context, in metrics.GetMetricDataInput) (metrics.GetMetricDataOutput, error) {
	return call[metrics.GetMetricDataOutput](scope(s), "cloudwatch", "getMetricData", in)
}
func (s pricing) GetProducts(_ context.Context, in prices.GetProductsInput) (prices.GetProductsOutput, error) {
	return call[prices.GetProductsOutput](scope(s), "pricing", "getProducts", in)
}
func (s org) ListRoots(_ context.Context, in discover.ListRootsInput) (discover.ListRootsOutput, error) {
	return call[discover.ListRootsOutput](scope(s), "organizations", "listRoots", in)
}
func (s org) ListOrganizationalUnitsForParent(_ context.Context, in discover.ListOrganizationalUnitsForParentInput) (discover.ListOrganizationalUnitsForParentOutput, error) {
	return call[discover.ListOrganizationalUnitsForParentOutput](scope(s), "organizations", "listOrganizationalUnitsForParent", in)
}
func (s org) ListAccountsForParent(_ context.Context, in discover.ListAccountsForParentInput) (discover.ListAccountsForParentOutput, error) {
	return call[discover.ListAccountsForParentOutput](scope(s), "organizations", "listAccountsForParent", in)
}
func (s org) ListTagsForResource(_ context.Context, in discover.ListTagsForResourceInput) (discover.ListTagsForResourceOutput, error) {
	return call[discover.ListTagsForResourceOutput](scope(s), "organizations", "listTagsForResource", in)
}

// spot is Spot price history from the profile's account, in the region asked.
type spot struct {
	w       *world
	account string
}

func (s spot) History(_ context.Context, region string, in prices.SpotHistoryInput) (prices.SpotHistoryOutput, error) {
	return call[prices.SpotHistoryOutput](scope{s.w, s.account, region}, "spot", "history", in)
}

// The accounts a profile reaches: STS and Organizations from its account, in us-east-1.
func (s sts) Whoami(context.Context) (string, error) {
	out, err := call[struct{ Account string }](scope(s), "sts", "getCallerIdentity", map[string]any{})
	return out.Account, err
}
func (s sts) Organization(context.Context) (string, error) {
	out, err := call[struct{ Organization struct{ Id string } }](scope(s), "organizations", "describeOrganization", map[string]any{})
	return out.Organization.Id, err
}
func (s sts) Inventory(ctx context.Context) (discover.OrgInventory, error) {
	return discover.LoadInventory(ctx, org(s), false)
}
func (s sts) Assume(_ context.Context, account, role string) (aws.Credentials, error) {
	out, err := call[struct {
		Credentials struct {
			AccessKeyId, SecretAccessKey, SessionToken string
			Expiration                                 time.Time
		}
	}](scope(s), "sts", "assumeRole", map[string]any{"RoleArn": "arn:aws:iam::" + account + ":role/" + role, "RoleSessionName": "dormeto-estimate", "DurationSeconds": 3600})
	c := out.Credentials
	return aws.Credentials{AccessKeyID: c.AccessKeyId, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, CanExpire: true, Expires: c.Expiration}, err
}

// checkedEKS and checkedClient fail the test on a Kubernetes call without a recorded answer.
type checkedEKS struct {
	w *world
	kubernetes.EKSPort
}

func (c checkedEKS) ListClusters(ctx context.Context, in kubernetes.ListClustersInput) (kubernetes.ListClustersOutput, error) {
	out, err := c.EKSPort.ListClusters(ctx, in)
	c.w.missing(err)
	return out, err
}
func (c checkedEKS) DescribeCluster(ctx context.Context, in kubernetes.DescribeClusterInput) (kubernetes.DescribeClusterOutput, error) {
	out, err := c.EKSPort.DescribeCluster(ctx, in)
	c.w.missing(err)
	return out, err
}

type checkedClient struct {
	w *world
	kubernetes.Client
}

func (c checkedClient) Get(ctx context.Context, path string) (json.RawMessage, error) {
	out, err := c.Client.Get(ctx, path)
	c.w.missing(err)
	return out, err
}
func (c checkedClient) List(ctx context.Context, path string, query map[string]string) ([]json.RawMessage, error) {
	out, err := c.Client.List(ctx, path, query)
	c.w.missing(err)
	return out, err
}

// kubernetesOf is the region's EKS clusters and their API, read by "the profile".
func (s scope) kubernetesOf() *kubernetes.Ports {
	k := kubernetestest.RecordedPorts(s.w.t, s.w.answers, kubernetestest.WithInput(func(in any) any {
		return map[string]any{"account": s.account, "region": s.region, "input": in}
	}))
	of := k.Of
	return &kubernetes.Ports{EKS: checkedEKS{s.w, k.EKS}, Reader: "the profile", Of: func(ctx context.Context, cluster string) (kubernetes.Client, error) {
		c, err := of(ctx, cluster)
		s.w.missing(err)
		if err != nil {
			return nil, err
		}
		return checkedClient{s.w, c}, nil
	}}
}

// aws is a profile's AWS as bin.ts wires it, over the recorded answers.
func (w *world) aws(_ options.Options, profile string) EstimateAWS {
	self := w.profiles[profile]
	at := func(account, region string) scope { return scope{w, account, region} }
	return EstimateAWS{
		Accounts: sts(at(self, global)),
		RegionsOf: func(_ context.Context, account string, _ *aws.Credentials) ([]string, error) {
			out, err := call[struct{ Regions []struct{ RegionName string } }](at(account, global), "ec2", "describeRegions", map[string]any{})
			regions := []string{}
			for _, r := range out.Regions {
				if r.RegionName != "" {
					regions = append(regions, r.RegionName)
				}
			}
			return regions, err
		},
		PortsOf: func(_ context.Context, account, region string, _ *aws.Credentials) (discover.RegionPorts, error) {
			s := at(account, region)
			k := s.kubernetesOf()
			scan := discover.ScanPorts{ELBv2: elb(s), AutoScaling: asg(s), EC2: scanEC2(s), ECS: scanECS(s), CloudWatch: scanCW(s), Kubernetes: k}
			return discover.RegionPorts{
				Scan: func(ctx context.Context) (discover.ScanResult, error) {
					return discover.ScanRegion(ctx, scan, discover.Where{Account: account, Region: region}, w.now)
				},
				AutoScaling: asg(s), ELBv2: elb(s), EC2: regionEC2(s),
				Stopped: stopped.Ports{EC2: regionEC2(s), ECS: stoppedECS(s), RDS: rds(s), KubernetesOf: k.Of},
			}, nil
		},
		CloudWatchOf: func(_ context.Context, account, region string, _ *aws.Credentials) (metrics.CloudWatchPort, error) {
			return metricsCW(at(account, region)), nil
		},
		Prices: func(c prices.Cache) PriceBook {
			return priceBook(pricing(at(self, global)), spot{w, self}, c, func() time.Time { return w.now })
		},
	}
}

// estimateCase is a case of the estimate topic.
type estimateCase struct {
	Argv     []string          `json:"argv"`
	Now      time.Time         `json:"now"`
	Profiles map[string]string `json:"profiles"`
	Answers  json.RawMessage   `json:"answers"`
}

type estimateOutput struct {
	ExitCode int             `json:"exitCode"`
	Report   json.RawMessage `json:"report"`
	Markdown *string         `json:"markdown"`
	Written  []string        `json:"written"`
	Log      []string        `json:"log"`
	Errors   []string        `json:"errors"`
}

var timeLine = regexp.MustCompile(`estimated \d{4}-\d{2}-\d{2} \d{2}:\d{2} UTC`)

// runCase runs a case's argv through Main with cacheRoot as its cache and gives what Node's export records.
func runCase(t *testing.T, in estimateCase, cacheRoot string) estimateOutput {
	t.Helper()
	w := &world{t: t, answers: parity.Recorded(t, in.Answers), now: in.Now, profiles: in.Profiles}
	var mu sync.Mutex
	got := estimateOutput{Written: []string{}, Log: []string{}, Errors: []string{}}
	written := map[string]string{}
	local := func(l string) string { return strings.ReplaceAll(l, cacheRoot, "<cacheRoot>") }
	got.ExitCode = Main(in.Argv, Deps{
		Now: func() time.Time { return in.Now }, CacheRoot: cacheRoot, AWS: w.aws, Version: "0.0.0-test",
		Log:   func(l string) { mu.Lock(); got.Log = append(got.Log, local(l)); mu.Unlock() },
		Error: func(l string) { mu.Lock(); got.Errors = append(got.Errors, local(l)); mu.Unlock() },
		WriteFile: func(path, text string) error {
			mu.Lock()
			defer mu.Unlock()
			got.Written = append(got.Written, path)
			written[path] = text
			return nil
		},
	})
	got.Report = json.RawMessage("null")
	for _, path := range got.Written {
		text := written[path]
		switch filepath.Ext(path) {
		case ".json":
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(text), &fields); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			if !strings.HasPrefix(text, "{\n  \"tool\": {\n    \"name\": \"dormeto\",\n    \"version\": \"0.0.0-test\"\n  },\n  \"generatedAt\": \""+in.Now.Format("2006-01-02T15:04:05.000Z")+"\",") {
				t.Errorf("%s: not the tool, then the run's time:\n%.200s", path, text)
			}
			// The report's run time and the Go command's tool removed.
			delete(fields, "generatedAt")
			delete(fields, "tool")
			got.Report, _ = json.Marshal(fields)
		case ".md":
			md := timeLine.ReplaceAllString(text, "estimated <generatedAt> UTC")
			got.Markdown = &md
		}
	}
	return got
}

func estimateCases(t *testing.T) ([]parity.Case, []estimateCase, []estimateOutput) {
	topic := parity.Load(t, "estimate")
	if len(topic.Cases) != 5 {
		t.Fatalf("%d estimate cases", len(topic.Cases))
	}
	ins := make([]estimateCase, len(topic.Cases))
	outs := make([]estimateOutput, len(topic.Cases))
	for i, c := range topic.Cases {
		if err := json.Unmarshal(c.Input, &ins[i]); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if err := json.Unmarshal(c.Output, &outs[i]); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		// The Go command's usage names `dormeto estimate` (the plan's global constraints).
		for j, e := range outs[i].Errors {
			outs[i].Errors[j] = strings.Replace(e, "Usage: npm run estimate --", "Usage: dormeto estimate", 1)
		}
	}
	return topic.Cases, ins, outs
}

// sameReports compares the exit code, the reports written and the errors with Node's.
func sameReports(t *testing.T, want, got estimateOutput) {
	t.Helper()
	if got.ExitCode != want.ExitCode {
		t.Errorf("exit code %d, Node's %d (errors %q)", got.ExitCode, want.ExitCode, got.Errors)
	}
	if !slices.Equal(got.Written, want.Written) {
		t.Errorf("written %q, Node's %q", got.Written, want.Written)
	}
	parity.Equal(t, want.Report, got.Report)
	switch {
	case (want.Markdown == nil) != (got.Markdown == nil):
		t.Errorf("markdown %v, Node's %v", got.Markdown != nil, want.Markdown != nil)
	case want.Markdown != nil && *got.Markdown != *want.Markdown:
		n, lines := firstDifference(*want.Markdown, *got.Markdown)
		t.Errorf("markdown differs from line %d:\n%s", n, lines)
	}
	if !slices.Equal(got.Errors, want.Errors) {
		t.Errorf("errors %q, Node's %q", got.Errors, want.Errors)
	}
}

// firstDifference is the first line two texts differ at, and both lines.
func firstDifference(want, got string) (int, string) {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := range max(len(w), len(g)) {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return i + 1, "want: " + a + "\ngot:  " + b
		}
	}
	return 0, ""
}

func TestTheSharedEstimateCasesThroughTheCommandWithAnEmptyCacheGiveNodesReportsLogAndErrors(t *testing.T) {
	cases, ins, outs := estimateCases(t)
	for i, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got := runCase(t, ins[i], t.TempDir())
			sameReports(t, outs[i], got)
			if !slices.Equal(got.Log, outs[i].Log) {
				n, lines := firstDifference(strings.Join(outs[i].Log, "\n"), strings.Join(got.Log, "\n"))
				t.Errorf("log differs from line %d:\n%s", n, lines)
			}
		})
	}
}

// nodeCache is a copy of the cache directory the Node estimate wrote for the first case.
func nodeCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "..", "testdata", "parity", "cache"))); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestTheSharedEstimateCasesThroughTheCommandWithTheCacheNodeWroteGiveNodesReports(t *testing.T) {
	cases, ins, outs := estimateCases(t)
	for i, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got := runCase(t, ins[i], nodeCache(t))
			sameReports(t, outs[i], got)
		})
	}
}

// The first case, from the cache Node wrote: nothing is asked of AWS but who the profile is, its organization and
// accounts, and the role of the account refusing it (never cached); the region that failed is scanned again.
func TestTheFirstCaseFromTheCacheNodeWroteSaysWhatCameFromTheCache(t *testing.T) {
	_, ins, outs := estimateCases(t)
	got := runCase(t, ins[0], nodeCache(t))
	sameReports(t, outs[0], got)
	for _, want := range []string{"  requests 111111111111 eu-west-1: 13 workloads (cache)", "  uptime 222222222222 eu-west-1: 2 workloads (cache)"} {
		if !slices.Contains(got.Log, want) {
			t.Errorf("log without %q: %q", want, got.Log)
		}
	}
}
