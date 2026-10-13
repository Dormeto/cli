package cli

// The estimate command (spec §3, §8): its exit codes, a run writing both reports and printing the summary, a second
// run from the cache asking AWS nothing but who the profile is and its organization. Ports manager
// packages/estimate/test/cli.test.ts, one test per `it`.

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/estimate"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/options"
	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/stopped"
)

const (
	acctA = "111111111111"
	acctB = "222222222222"
	regR  = "eu-west-1"
	lbArn = "arn:aws:elasticloadbalancing:" + regR + ":" + acctA + ":loadbalancer/app/web/0123456789abcdef"
	tgArn = "arn:aws:elasticloadbalancing:" + regR + ":" + acctA + ":targetgroup/shop/fedcba9876543210"
)

var testNow = time.Date(2026, 10, 10, 12, 0, 30, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// rule is the one rule the fake region's scan finds (Node's RULE).
func rule(tags map[string][]string) discover.ScannedRule {
	return discover.ScannedRule{
		ID: tgArn, Account: acctA, Region: regR, LoadBalancer: "web", LoadBalancerArn: lbArn, Listener: "HTTPS:443", Priority: "10",
		Conditions: "host shop.example.com", Hosts: []string{"shop.example.com"}, TargetGroup: ptr("shop"), TargetGroupArn: ptr(tgArn),
		Backend: &discover.Backend{Kind: discover.KindInstance, ID: "i-1"}, Tags: tags, Rules: []discover.RuleRef{}, Eligibility: discover.Eligible,
		IdleHours7d: ptr(150), Requests7d: ptr(20),
	}
}

func member(id string) discover.Account {
	name := "prod"
	if id == acctA {
		name = "management"
	}
	return discover.Account{ID: id, Name: name, OUPath: []string{"r-root"}, Tags: map[string]string{}}
}

// fakeAccounts is a profile's accounts.Ports, each call replaceable.
type fakeAccounts struct {
	whoami       func(ctx context.Context) (string, error)
	organization func(ctx context.Context) (string, error)
	inventory    func(ctx context.Context) (discover.OrgInventory, error)
	assume       func(ctx context.Context, account, role string) (aws.Credentials, error)
}

func (f *fakeAccounts) Whoami(ctx context.Context) (string, error)       { return f.whoami(ctx) }
func (f *fakeAccounts) Organization(ctx context.Context) (string, error) { return f.organization(ctx) }
func (f *fakeAccounts) Inventory(ctx context.Context) (discover.OrgInventory, error) {
	return f.inventory(ctx)
}
func (f *fakeAccounts) Assume(ctx context.Context, account, role string) (aws.Credentials, error) {
	return f.assume(ctx, account, role)
}

// calls counts what the fake AWS was asked.
type calls struct {
	mu                               sync.Mutex
	scans, metrics, regions, assumed int
	discounts                        []prices.Discounts
}

func (c *calls) add(n *int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*n++
}

func (c *calls) counts() [4]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return [4]int{c.scans, c.metrics, c.regions, c.assumed}
}

type fakeOptions struct {
	whoami       func(ctx context.Context) (string, error)
	organization string
	tags         map[string][]string
}

// fakeEC2 answers instance i-1, a t3.medium on Linux, whatever is asked; no security group.
type fakeEC2 struct{}

func (fakeEC2) DescribeInstances(context.Context, stopped.DescribeInstancesInput) (stopped.DescribeInstancesOutput, error) {
	return stopped.DescribeInstancesOutput{Reservations: []stopped.Reservation{{Instances: []stopped.Instance{{InstanceID: "i-1", InstanceType: "t3.medium", PlatformDetails: "Linux/UNIX"}}}}}, nil
}
func (fakeEC2) DescribeSecurityGroups(context.Context, discover.DescribeSecurityGroupsInput) (discover.DescribeSecurityGroupsOutput, error) {
	return discover.DescribeSecurityGroupsOutput{}, nil
}

type fakeECS struct{}

func (fakeECS) DescribeServices(context.Context, stopped.DescribeServicesInput) (stopped.DescribeServicesOutput, error) {
	return stopped.DescribeServicesOutput{Services: []stopped.Service{}}, nil
}
func (fakeECS) DescribeTaskDefinition(context.Context, stopped.DescribeTaskDefinitionInput) (stopped.DescribeTaskDefinitionOutput, error) {
	return stopped.DescribeTaskDefinitionOutput{}, nil
}

type fakeASG struct{}

func (fakeASG) DescribeAutoScalingGroups(context.Context, discover.DescribeAutoScalingGroupsInput) (discover.DescribeAutoScalingGroupsOutput, error) {
	return discover.DescribeAutoScalingGroupsOutput{AutoScalingGroups: []discover.AutoScalingGroup{}}, nil
}

type fakeRules struct{}

func (fakeRules) DescribeRules(context.Context, discover.DescribeRulesInput) (discover.DescribeRulesOutput, error) {
	return discover.DescribeRulesOutput{Rules: []discover.Rule{}}, nil
}

// fakeCloudWatch answers every query with 14 datapoints of 2, one a day at 10:00 after the start.
type fakeCloudWatch struct{ calls *calls }

func (f fakeCloudWatch) GetMetricData(_ context.Context, in metrics.GetMetricDataInput) (metrics.GetMetricDataOutput, error) {
	f.calls.add(&f.calls.metrics)
	out := metrics.GetMetricDataOutput{}
	for _, q := range in.MetricDataQueries {
		r := metrics.MetricDataResult{ID: q.ID, StatusCode: "Complete"}
		for d := range 14 {
			r.Timestamps = append(r.Timestamps, in.StartTime.Add(time.Duration(600+d*1440)*time.Minute))
			r.Values = append(r.Values, 2)
		}
		out.MetricDataResults = append(out.MetricDataResults, r)
	}
	return out, nil
}

// fakeAWS is a profile's AWS: its account A (or whoami's), an organization of A and B when one is given, one region R
// whose scan finds the rule (with the tags given), an EC2 instance at $0.0456 an hour.
func fakeAWS(o fakeOptions) (*EstimateAWS, *calls) {
	c := &calls{}
	if o.whoami == nil {
		o.whoami = func(context.Context) (string, error) { return acctA, nil }
	}
	if o.tags == nil {
		o.tags = map[string][]string{}
	}
	accountsPorts := &fakeAccounts{
		whoami: o.whoami,
		organization: func(context.Context) (string, error) {
			if o.organization != "" {
				return o.organization, nil
			}
			return "", &smithy.GenericAPIError{Code: "AWSOrganizationsNotInUseException", Message: "Your account is not a member of an organization."}
		},
		inventory: func(context.Context) (discover.OrgInventory, error) {
			if o.organization == "" {
				return discover.OrgInventory{Accounts: []discover.Account{}, OUs: map[string]discover.OU{}}, nil
			}
			return discover.OrgInventory{Accounts: []discover.Account{member(acctA), member(acctB)}, OUs: map[string]discover.OU{}}, nil
		},
		assume: func(context.Context, string, string) (aws.Credentials, error) {
			c.add(&c.assumed)
			return aws.Credentials{AccessKeyID: "a", SecretAccessKey: "s", SessionToken: "t"}, nil
		},
	}
	e := &EstimateAWS{
		Accounts: accountsPorts,
		RegionsOf: func(context.Context, string, *aws.Credentials) ([]string, error) {
			c.add(&c.regions)
			return []string{regR}, nil
		},
		PortsOf: func(context.Context, string, string, *aws.Credentials) (discover.RegionPorts, error) {
			return discover.RegionPorts{
				Scan: func(context.Context) (discover.ScanResult, error) {
					c.add(&c.scans)
					return discover.ScanResult{Rules: []discover.ScannedRule{rule(o.tags)}}, nil
				},
				AutoScaling: fakeASG{}, ELBv2: fakeRules{}, EC2: fakeEC2{},
				Stopped: stopped.Ports{EC2: fakeEC2{}, ECS: fakeECS{}},
			}, nil
		},
		CloudWatchOf: func(context.Context, string, string, *aws.Credentials) (metrics.CloudWatchPort, error) {
			return fakeCloudWatch{c}, nil
		},
		Prices: func(prices.Cache) PriceBook {
			return PriceBook{Hourly: func(_ context.Context, _ string, item stopped.Item, d prices.Discounts) (*float64, error) {
				c.mu.Lock()
				c.discounts = append(c.discounts, d)
				c.mu.Unlock()
				if item.Kind == stopped.KindEC2 {
					return ptr(0.0456), nil
				}
				return nil, nil
			}}
		},
	}
	return e, c
}

// run is a command's dependencies and what it said.
type run struct {
	mu        sync.Mutex
	out, errs []string
	cacheRoot string
	deps      Deps
}

func newRun(t *testing.T, e *EstimateAWS) *run {
	r := &run{cacheRoot: t.TempDir()}
	r.deps = Deps{
		Now: func() time.Time { return testNow }, CacheRoot: r.cacheRoot,
		Log:   func(l string) { r.mu.Lock(); r.out = append(r.out, l); r.mu.Unlock() },
		Error: func(l string) { r.mu.Lock(); r.errs = append(r.errs, l); r.mu.Unlock() },
		AWS:   func(options.Options, string) EstimateAWS { return *e },
	}
	return r
}

func (r *run) logged() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.out)
}

func (r *run) errors() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.errs)
}

// written reads the JSON report written in dir.
func written(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "estimate-2026-10-10.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// at reads a path of a decoded JSON value: object keys and array indexes.
func at(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("%v: not an object at %q", path, k)
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				t.Fatalf("%v: no item %d", path, k)
			}
			v = a[k]
		}
	}
	return v
}

func TestABadOptionItsMessageAndTheUsageExitCode2(t *testing.T) {
	e, _ := fakeAWS(fakeOptions{})
	r := newRun(t, e)
	if code := Main([]string{"--profile", "p", "--refresh", "all"}, r.deps); code != 2 {
		t.Fatalf("exit code %d", code)
	}
	errs := r.errors()
	if len(errs) != 2 || errs[0] != "--refresh: discovery or metrics" || !strings.Contains(errs[1], "Usage: dormeto estimate --profile <profile>") {
		t.Fatalf("errors %q", errs)
	}
}

func TestCredentialsThatDoNotWorkAWSsMessageExitCode1(t *testing.T) {
	e, _ := fakeAWS(fakeOptions{whoami: func(context.Context) (string, error) {
		return "", &smithy.GenericAPIError{Code: "ExpiredTokenException", Message: "The security token included in the request is expired"}
	}})
	r := newRun(t, e)
	if code := Main([]string{"--profile", "p"}, r.deps); code != 1 {
		t.Fatalf("exit code %d", code)
	}
	if errs := r.errors(); !slices.Equal(errs, []string{"ExpiredTokenException: The security token included in the request is expired"}) {
		t.Fatalf("errors %q", errs)
	}
}

func TestWritesBothReportsAndPrintsTheSummaryTheDiscountsReachThePricesASecondRunAsksAWSOnlyWhoItIs(t *testing.T) {
	e, c := fakeAWS(fakeOptions{})
	r := newRun(t, e)
	out := t.TempDir()
	if code := Main([]string{"--profile", "p", "--discount-compute", "20", "--out", out}, r.deps); code != 0 {
		t.Fatalf("exit code %d: %q", code, r.errors())
	}
	entries, _ := os.ReadDir(out)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"estimate-2026-10-10.json", "estimate-2026-10-10.md"}) {
		t.Fatalf("written %q", names)
	}
	if n := at(t, written(t, out), "totals", "workloads"); n != 1.0 {
		t.Fatalf("totals.workloads %v", n)
	}
	md, _ := os.ReadFile(filepath.Join(out, "estimate-2026-10-10.md"))
	if !strings.Contains(string(md), "shop.example.com") {
		t.Fatalf("markdown without the workload:\n%s", md)
	}
	logged := strings.Join(r.logged(), "\n")
	if !strings.Contains(logged, "for 1 workload in 1 account") || !strings.Contains(logged, filepath.Join(out, "estimate-2026-10-10.md")) {
		t.Fatalf("log:\n%s", logged)
	}
	if len(c.discounts) == 0 || c.discounts[0] != (prices.Discounts{Compute: 20, Databases: 0}) {
		t.Fatalf("discounts %v", c.discounts)
	}
	// The cache is the account's (no organization).
	if entries, _ := os.ReadDir(r.cacheRoot); len(entries) != 1 || entries[0].Name() != acctA {
		t.Fatalf("cache %v", entries)
	}
	before := c.counts()
	if code := Main([]string{"--profile", "p", "--out", out}, r.deps); code != 0 {
		t.Fatalf("second run: exit code %d: %q", code, r.errors())
	}
	if after := c.counts(); after != before {
		t.Fatalf("scans, metrics, regions, assumed: %v, then %v", before, after)
	}
}

// statuses records a terminal's lines.
type statuses struct {
	mu            sync.Mutex
	updates, ends []string
}

func (s *statuses) Update(l string) { s.mu.Lock(); s.updates = append(s.updates, l); s.mu.Unlock() }
func (s *statuses) End(l string)    { s.mu.Lock(); s.ends = append(s.ends, l); s.mu.Unlock() }

func TestTheDiscoveryPhasesProgressALinePerAccountWithoutATerminalOneLineRewrittenInATerminalParallel(t *testing.T) {
	out := t.TempDir()
	e, _ := fakeAWS(fakeOptions{})
	d := newRun(t, e)
	if code := Main([]string{"--profile", "p", "--out", out}, d.deps); code != 0 {
		t.Fatalf("exit code %d: %q", code, d.errors())
	}
	if l := d.logged(); !slices.Contains(l, "  discovery "+acctA+" (1/1): 1 region, 1 workload") || !slices.Contains(l, "Phase 2: requests (5 regions at a time)") {
		t.Fatalf("log %q", l)
	}
	e2, _ := fakeAWS(fakeOptions{})
	term := newRun(t, e2)
	s := &statuses{}
	term.deps.Status = s
	if code := Main([]string{"--profile", "p", "--parallel", "2", "--uptime", "--refresh", "metrics", "--min-saving", "none", "--out", out}, term.deps); code != 0 {
		t.Fatalf("exit code %d: %q", code, term.errors())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.updates) == 0 {
		t.Fatal("no line rewritten")
	}
	// Phase 2 too (the user, 2026-10-11): the requests, then the uptime, each on its line.
	want := []string{
		"  Discovery [████████████████████] 100% · 1/1 account · 1 region · 1 workload · 0:00",
		"  Requests [████████████████████] 100% · 1/1 region · 1 workload · 0:00",
		"  Uptime [████████████████████] 100% · 1/1 region · 1 workload · 0:00",
	}
	if !slices.Equal(s.ends, want) {
		t.Fatalf("ends %q", s.ends)
	}
	perLine := regexp.MustCompile(`^ {2}(discovery|requests|uptime) `)
	for _, l := range term.logged() {
		if perLine.MatchString(l) {
			t.Fatalf("a line per account or region in a terminal: %q", l)
		}
	}
}

func TestRestrictedToAccountsOfTheOrganizationOnlyThoseAndHowManyOfItsAccounts(t *testing.T) {
	e, c := fakeAWS(fakeOptions{organization: "o-abc123"})
	r := newRun(t, e)
	out := t.TempDir()
	if code := Main([]string{"--profile", "p", "--accounts", acctA, "--out", out}, r.deps); code != 0 {
		t.Fatalf("exit code %d: %q", code, r.errors())
	}
	if l := r.logged(); l[0] != "Estimating the organization o-abc123 (1 of its 2 accounts, --accounts); cache "+filepath.Join(r.cacheRoot, "o-abc123") {
		t.Fatalf("first line %q", l[0])
	}
	// The other account is never reached.
	if n := c.counts(); n[3] != 0 || n[2] != 1 {
		t.Fatalf("assumed %d, regions %d", n[3], n[2])
	}
	j := written(t, out)
	if at(t, j, "accounts") != 1.0 || at(t, j, "accountsListed") != 2.0 || !slices.Equal(at(t, j, "options", "accounts").([]any), []any{acctA}) {
		t.Fatalf("accounts %v, listed %v, options.accounts %v", j["accounts"], j["accountsListed"], at(t, j, "options", "accounts"))
	}
}

func TestAccountsGivenThatCannotBeEstimatedWhyExitCode2(t *testing.T) {
	e, _ := fakeAWS(fakeOptions{organization: "o-abc123"})
	r := newRun(t, e)
	// A temporary --out: a run that went on would write nothing in the repository.
	if code := Main([]string{"--profile", "p", "--accounts", "999999999999", "--out", t.TempDir()}, r.deps); code != 2 {
		t.Fatalf("exit code %d", code)
	}
	if errs := r.errors(); !slices.Equal(errs, []string{"--accounts: 999999999999: not an active account of the organization o-abc123"}) {
		t.Fatalf("errors %q", errs)
	}
}

func TestSeveralProfilesSuchAsOneSSOProfilePerAccountEachAccountThroughItsOwnProfile(t *testing.T) {
	// Neither profile may list the organization's accounts: each reaches its own
	profileOf := func(self string) (*EstimateAWS, *calls) {
		e, c := fakeAWS(fakeOptions{whoami: func(context.Context) (string, error) { return self, nil }, organization: "o-abc123"})
		e.Accounts.(*fakeAccounts).inventory = func(context.Context) (discover.OrgInventory, error) {
			return discover.OrgInventory{}, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "You don't have permissions to access this resource."}
		}
		return e, c
	}
	qal, qalCalls := profileOf(acctA)
	prd, prdCalls := profileOf(acctB)
	r := newRun(t, qal)
	var asked []string
	r.deps.AWS = func(_ options.Options, profile string) EstimateAWS {
		asked = append(asked, profile)
		if profile == "qal" {
			return *qal
		}
		return *prd
	}
	out := t.TempDir()
	if code := Main([]string{"--profile", "qal,prd", "--accounts", acctA + "," + acctB, "--out", out}, r.deps); code != 0 {
		t.Fatalf("exit code %d: %q", code, r.errors())
	}
	if !slices.Equal(asked, []string{"qal", "prd"}) {
		t.Fatalf("asked %q", asked)
	}
	// Each account discovered and read with its own profile's credentials, nothing assumed
	q, p := qalCalls.counts(), prdCalls.counts()
	if q[0] != 1 || p[0] != 1 || q[1] != 1 || p[1] != 1 || q[3]+p[3] != 0 {
		t.Fatalf("qal %v, prd %v (scans, metrics, regions, assumed)", q, p)
	}
	j := written(t, out)
	if at(t, j, "organization") != "o-abc123" || at(t, j, "accounts") != 2.0 || len(at(t, j, "unreachable").([]any)) != 0 {
		t.Fatalf("organization %v, accounts %v, unreachable %v", j["organization"], j["accounts"], j["unreachable"])
	}
	if l := r.logged(); !strings.Contains(l[0], "the organization o-abc123 (2 of its 2 accounts, --accounts)") {
		t.Fatalf("first line %q", l[0])
	}
}

func TestWorkloadsExcludedByTheirTagsNotEstimatedTheirRequestsNeverReadListedInTheReport(t *testing.T) {
	e, c := fakeAWS(fakeOptions{tags: map[string][]string{"env": {"dev"}, "team": {"shop"}}})
	r := newRun(t, e)
	out := t.TempDir()
	if code := Main([]string{"--profile", "p", "--exclude-tags", "env=prod", "--exclude-tags", "env=dev|test,team=shop", "--out", out}, r.deps); code != 0 {
		t.Fatalf("exit code %d: %q", code, r.errors())
	}
	if n := c.counts(); n[1] != 0 {
		t.Fatalf("metrics %d", n[1])
	}
	j := written(t, out)
	if at(t, j, "totals", "workloads") != 0.0 {
		t.Fatalf("totals.workloads %v", at(t, j, "totals", "workloads"))
	}
	if excluded := at(t, j, "excluded").([]any); len(excluded) != 1 || at(t, excluded, 0, "workload", "targetGroup") != "shop" || at(t, excluded, 0, "criterion") != "env=dev|test, team=shop" {
		t.Fatalf("excluded %v", excluded)
	}
	if l := r.logged(); !slices.Contains(l, "Excluded by tags: 1 workload (see the report)") {
		t.Fatalf("log %q", l)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestEC2UptimeTheInstancesUptimeReadInPhase2AndKeptTheEstimateCountsOnlyTheHoursTheyRan(t *testing.T) {
	e, c := fakeAWS(fakeOptions{})
	r := newRun(t, e)
	out := t.TempDir()
	if code := Main([]string{"--profile", "p", "--ec2-uptime", "--min-saving", "none", "--out", out}, r.deps); code != 0 {
		t.Fatalf("exit code %d: %q", code, r.errors())
	}
	// The fake answers 14 datapoints: 14 periods of 5 minutes in 14 days.
	if h, _ := at(t, written(t, out), "workloads", 0, "uptime", "hoursPerDay").(float64); !near(h, 5.0/60) {
		t.Fatalf("hoursPerDay %v", h)
	}
	if l := r.logged(); !slices.Contains(l, "  uptime "+acctA+" "+regR+": 1 workload") {
		t.Fatalf("log %q", l)
	}
	if n := c.counts(); n[1] != 2 {
		t.Fatalf("metrics %d", n[1])
	}
	// A second run: from the cache.
	if code := Main([]string{"--profile", "p", "--ec2-uptime", "--min-saving", "none", "--out", out}, r.deps); code != 0 {
		t.Fatalf("second run: exit code %d: %q", code, r.errors())
	}
	if n := c.counts(); n[1] != 2 {
		t.Fatalf("metrics %d after the second run", n[1])
	}
	if l := r.logged(); !slices.Contains(l, "  uptime "+acctA+" "+regR+": 1 workload (cache)") {
		t.Fatalf("log %q", l)
	}
}

func TestUptimeEveryUptimeOptionTheInstancesMeasuredAsWithEC2Uptime(t *testing.T) {
	e, _ := fakeAWS(fakeOptions{})
	r := newRun(t, e)
	out := t.TempDir()
	if code := Main([]string{"--profile", "p", "--uptime", "--min-saving", "none", "--out", out}, r.deps); code != 0 {
		t.Fatalf("exit code %d: %q", code, r.errors())
	}
	j := written(t, out)
	if at(t, j, "options", "ec2Uptime") != true || at(t, j, "options", "ecsUptime") != true {
		t.Fatalf("options %v", j["options"])
	}
	if h, _ := at(t, j, "workloads", 0, "uptime", "hoursPerDay").(float64); !near(h, 5.0/60) {
		t.Fatalf("hoursPerDay %v", h)
	}
}

// Go only: Ctrl-C cancels the run's context. The run stops, says nothing, writes no report and caches nothing it
// was reading (a cancelled region is no result); exit code Interrupted.
func TestCtrlCDuringDiscoveryStopsTheRunSaysNothingWritesNoReportAndKeepsNoRegion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, c := fakeAWS(fakeOptions{})
	portsOf := e.PortsOf
	e.PortsOf = func(ctx context.Context, account, region string, creds *aws.Credentials) (discover.RegionPorts, error) {
		p, err := portsOf(ctx, account, region, creds)
		p.Scan = func(ctx context.Context) (discover.ScanResult, error) {
			cancel()
			<-ctx.Done()
			return discover.ScanResult{}, ctx.Err()
		}
		return p, err
	}
	r := newRun(t, e)
	r.deps.Context = ctx
	out := t.TempDir()
	if code := Main([]string{"--profile", "p", "--out", out}, r.deps); code != Interrupted {
		t.Fatalf("exit code %d", code)
	}
	if errs := r.errors(); len(errs) != 0 {
		t.Fatalf("errors %q", errs)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Fatalf("reports written: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(r.cacheRoot, acctA, "discovery")); !os.IsNotExist(err) {
		t.Fatalf("the region interrupted kept: %v", err)
	}
	if l := r.logged(); slices.Contains(l, "Phase 2: requests (5 regions at a time)") {
		t.Fatalf("went on: %q", l)
	}
	if n := c.counts(); n[1] != 0 {
		t.Fatalf("metrics read: %d", n[1])
	}
}

func TestCtrlCDuringTheRequestsStopsTheRunTheRegionsDiscoveredKeptTheRequestsNot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, _ := fakeAWS(fakeOptions{})
	e.CloudWatchOf = func(context.Context, string, string, *aws.Credentials) (metrics.CloudWatchPort, error) {
		return cancelling{cancel}, nil
	}
	r := newRun(t, e)
	r.deps.Context = ctx
	out := t.TempDir()
	if code := Main([]string{"--profile", "p", "--out", out}, r.deps); code != Interrupted {
		t.Fatalf("exit code %d", code)
	}
	if errs := r.errors(); len(errs) != 0 {
		t.Fatalf("errors %q", errs)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Fatalf("reports written: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(r.cacheRoot, acctA, "discovery", acctA+"-"+regR+".json")); err != nil {
		t.Fatalf("the region discovered not kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.cacheRoot, acctA, "requests")); !os.IsNotExist(err) {
		t.Fatalf("the requests interrupted kept: %v", err)
	}
}

// cancelling is a CloudWatch whose first call is interrupted by Ctrl-C.
type cancelling struct{ cancel context.CancelFunc }

func (c cancelling) GetMetricData(ctx context.Context, _ metrics.GetMetricDataInput) (metrics.GetMetricDataOutput, error) {
	c.cancel()
	<-ctx.Done()
	return metrics.GetMetricDataOutput{}, ctx.Err()
}

func TestHelpPrintsTheUsageExitCode0(t *testing.T) {
	e, _ := fakeAWS(fakeOptions{})
	r := newRun(t, e)
	if code := Main([]string{"--help"}, r.deps); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if l := r.logged(); !slices.Equal(l, []string{options.Usage}) || len(r.errors()) != 0 {
		t.Fatalf("log %q, errors %q", l, r.errors())
	}
}

func TestAReportThatCannotBeWrittenIsSaidExitCode1(t *testing.T) {
	e, _ := fakeAWS(fakeOptions{})
	r := newRun(t, e)
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// --out names a file: its reports cannot be written.
	if code := Main([]string{"--profile", "p", "--out", blocked}, r.deps); code != 1 {
		t.Fatalf("exit code %d", code)
	}
	if errs := r.errors(); len(errs) != 1 || !strings.HasPrefix(errs[0], "Error: ") || !strings.Contains(errs[0], "not a directory") {
		t.Fatalf("errors %q", errs)
	}
}

// Go only (Task 9's review): a member account's credentials are asked for each region scanned and each region whose
// requests are read (Node: credentials() once per region), so a role about to expire is assumed again in time; the
// profile's own account is read with the profile's (nil).
func TestAMemberAccountsCredentialsAreAskedForEachRegionReadSoItsRoleIsRenewedInTime(t *testing.T) {
	e, c := fakeAWS(fakeOptions{organization: "o-abc123"})
	// Each role session ends within the five minutes before its expiry: every ask assumes the role again.
	e.Accounts.(*fakeAccounts).assume = func(context.Context, string, string) (aws.Credentials, error) {
		c.add(&c.assumed)
		return aws.Credentials{AccessKeyID: "a", SecretAccessKey: "s", SessionToken: "t", CanExpire: true, Expires: testNow.Add(4 * time.Minute)}, nil
	}
	e.RegionsOf = func(context.Context, string, *aws.Credentials) ([]string, error) {
		return []string{"eu-west-1", "eu-west-3"}, nil
	}
	var mu sync.Mutex
	given := map[string][]bool{} // per account: credentials given to each port made
	record := func(account string, creds *aws.Credentials) {
		mu.Lock()
		defer mu.Unlock()
		given[account] = append(given[account], creds != nil && creds.AccessKeyID == "a")
	}
	portsOf, cloudwatchOf := e.PortsOf, e.CloudWatchOf
	e.PortsOf = func(ctx context.Context, account, region string, creds *aws.Credentials) (discover.RegionPorts, error) {
		record(account, creds)
		return portsOf(ctx, account, region, creds)
	}
	e.CloudWatchOf = func(ctx context.Context, account, region string, creds *aws.Credentials) (metrics.CloudWatchPort, error) {
		record(account, creds)
		return cloudwatchOf(ctx, account, region, creds)
	}
	r := newRun(t, e)
	if code := Main([]string{"--profile", "p", "--parallel", "1", "--out", t.TempDir()}, r.deps); code != 0 {
		t.Fatalf("exit code %d: %q", code, r.errors())
	}
	// B's regions listed, two scanned, two read: five asks, five roles assumed.
	if n := c.counts(); n[3] != 5 {
		t.Fatalf("roles assumed %d", n[3])
	}
	if !slices.Equal(given[acctA], []bool{false, false, false, false}) || !slices.Equal(given[acctB], []bool{true, true, true, true}) {
		t.Fatalf("credentials given: %v", given)
	}
}

// F5: a report no JSON can hold (report.JSON's error) is an error, exit code 1: nothing is written, neither report.
func TestAReportThatCannotBeEncodedIsSaidExitCode1NothingWritten(t *testing.T) {
	defer func(f func(estimate.Estimate, string) ([]byte, error)) { jsonReport = f }(jsonReport)
	jsonReport = func(estimate.Estimate, string) ([]byte, error) {
		return nil, &json.UnsupportedTypeError{Type: reflect.TypeFor[chan int]()}
	}
	e, _ := fakeAWS(fakeOptions{})
	r := newRun(t, e)
	out := t.TempDir()
	if code := Main([]string{"--profile", "p", "--out", out}, r.deps); code != 1 {
		t.Errorf("exit code %d", code)
	}
	if errs := r.errors(); !slices.Equal(errs, []string{"Error: json: unsupported type: chan int"}) {
		t.Errorf("errors %q", errs)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("written: %v", entries)
	}
}

// F6: a panic is said as any error, `Error: <its value>` on one line and exit code 1, never a goroutine dump: in the
// command's own goroutine and in a worker (a region's requests read by limit.Map).
func TestAPanicIsSaidAsAnErrorExitCode1(t *testing.T) {
	e, _ := fakeAWS(fakeOptions{whoami: func(context.Context) (string, error) {
		var accounts map[string]int
		accounts["111111111111"]++
		return acctA, nil
	}})
	r := newRun(t, e)
	if code := Main([]string{"--profile", "p", "--out", t.TempDir()}, r.deps); code != 1 {
		t.Errorf("exit code %d", code)
	}
	if errs := r.errors(); !slices.Equal(errs, []string{"Error: assignment to entry in nil map"}) {
		t.Errorf("errors %q", errs)
	}

	e, _ = fakeAWS(fakeOptions{})
	e.CloudWatchOf = func(context.Context, string, string, *aws.Credentials) (metrics.CloudWatchPort, error) {
		panic("boom")
	}
	r = newRun(t, e)
	out := t.TempDir()
	if code := Main([]string{"--profile", "p", "--out", out}, r.deps); code != 1 {
		t.Errorf("exit code %d", code)
	}
	if errs := r.errors(); !slices.Equal(errs, []string{"Error: boom"}) {
		t.Errorf("errors %q", errs)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("written: %v", entries)
	}
}
