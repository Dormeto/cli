package cli

// The estimate with its standalone resources (vision standalone workloads design §7, --standalone on by default): the
// standalone scan's resources described, their signals read, priced, shown in both reports; their own cache files, read
// again only when asked, never read by a run with --standalone off.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/stopped"
)

// standaloneScan is the fake region's standalone scan in an account: the instance i-9 (build-agent) and the database
// reports, eligible; a Spot instance, ineligible; the RDS clusters refused.
func standaloneScan(account string) *discover.StandaloneScan {
	arn := func(service, resource string) string {
		return "arn:aws:" + service + ":" + regR + ":" + account + ":" + resource
	}
	candidate := func(kind, id, arn, name string) discover.StandaloneRule {
		return discover.StandaloneRule{Kind: discover.KindStandalone, ID: arn, Account: account, Region: regR, ResourceArn: arn, Name: name,
			Backend: discover.Backend{Kind: kind, ID: id, ARN: arn}, Tags: map[string][]string{}, Eligibility: discover.Eligible}
	}
	spot := candidate(discover.KindInstance, "i-spot", arn("ec2", "instance/i-spot"), "i-spot")
	spot.Eligibility, spot.Reason = discover.Ineligible, "instance i-spot is a Spot instance: Dormeto stops On-Demand instances only"
	return &discover.StandaloneScan{
		Rules: []discover.StandaloneRule{candidate(discover.KindInstance, "i-9", arn("ec2", "instance/i-9"), "build-agent"), spot,
			candidate(discover.KindRDSInstance, "reports", arn("rds", "db:reports"), "reports")},
		Report: discover.StandaloneReport{Problems: []string{"RDS clusters not listed: the discovery role may not call rds:DescribeDBClusters (apply the access module or the AWS CLI script of this release again)"},
			Unread: []string{discover.KindRDSCluster}},
	}
}

// fakeRDS answers the database reports, a db.t3.medium MySQL.
type fakeRDS struct{}

func (fakeRDS) DescribeDBInstances(context.Context, stopped.DescribeDBInstancesInput) (stopped.DescribeDBInstancesOutput, error) {
	return stopped.DescribeDBInstancesOutput{DBInstances: []stopped.DBInstance{{DBInstanceIdentifier: "reports", DBInstanceClass: "db.t3.medium", Engine: "mysql", DBInstanceStatus: "available"}}}, nil
}
func (fakeRDS) DescribeDBClusters(context.Context, stopped.DescribeDBClustersInput) (stopped.DescribeDBClustersOutput, error) {
	return stopped.DescribeDBClustersOutput{}, nil
}

// signalsCloudWatch answers the standalone signals (5-minute queries): every point of the period, 90 an hour a day
// from 09:00 UTC, else 0; any other query as fakeCloudWatch. It counts the standalone calls.
type signalsCloudWatch struct {
	fakeCloudWatch
	mu    *sync.Mutex
	asked *int
}

func (f signalsCloudWatch) GetMetricData(ctx context.Context, in metrics.GetMetricDataInput) (metrics.GetMetricDataOutput, error) {
	if len(in.MetricDataQueries) == 0 || in.MetricDataQueries[0].MetricStat.Period != 300 {
		return f.fakeCloudWatch.GetMetricData(ctx, in)
	}
	f.mu.Lock()
	*f.asked++
	f.mu.Unlock()
	out := metrics.GetMetricDataOutput{}
	for _, q := range in.MetricDataQueries {
		r := metrics.MetricDataResult{ID: q.ID, StatusCode: "Complete"}
		for t := in.StartTime; t.Before(in.EndTime); t = t.Add(5 * time.Minute) {
			v := 0.0
			if t.UTC().Hour() == 9 {
				v = 90
			}
			r.Timestamps, r.Values = append(r.Timestamps, t), append(r.Values, v)
		}
		out.MetricDataResults = append(out.MetricDataResults, r)
	}
	return out, nil
}

// standaloneAWS is fakeAWS with the standalone scan, the database, its price and the signals; scans counts the
// standalone scans.
func standaloneAWS() (*EstimateAWS, *calls, *int, *int) { return standaloneAWSOf(fakeOptions{}) }

// standaloneAWSOf is standaloneAWS with fakeAWS's options: its standalone scan in the account the scan is asked for.
func standaloneAWSOf(o fakeOptions) (*EstimateAWS, *calls, *int, *int) {
	e, c := fakeAWS(o)
	var mu sync.Mutex
	scans, signals := new(int), new(int)
	ports := e.PortsOf
	e.PortsOf = func(ctx context.Context, account, region string, creds *aws.Credentials) (discover.RegionPorts, error) {
		p, err := ports(ctx, account, region, creds)
		p.Standalone = func(ctx context.Context) (discover.ScanResult, error) {
			mu.Lock()
			*scans++
			mu.Unlock()
			res, err := p.Scan(ctx)
			res.Standalone = standaloneScan(account)
			return res, err
		}
		p.Stopped.RDS = fakeRDS{}
		return p, err
	}
	e.CloudWatchOf = func(context.Context, string, string, *aws.Credentials) (metrics.CloudWatchPort, error) {
		return signalsCloudWatch{fakeCloudWatch{c}, &mu, signals}, nil
	}
	book := e.Prices
	e.Prices = func(pc prices.Cache) PriceBook {
		b := book(pc)
		hourly := b.Hourly
		b.Hourly = func(ctx context.Context, region string, item stopped.Item, d prices.Discounts) (*float64, error) {
			if item.Kind == stopped.KindRDS {
				return ptr(0.068), nil
			}
			return hourly(ctx, region, item, d)
		}
		return b
	}
	return e, c, scans, signals
}

func TestTheStandaloneResourcesAreEstimatedAndShownInBothReports(t *testing.T) {
	e, _, _, _ := standaloneAWS()
	r := newRun(t, e)
	out := t.TempDir()
	if code := Main([]string{"--profile", "p", "--out", out}, r.deps); code != 0 {
		t.Fatalf("exit code %d: %q", code, r.errors())
	}
	report := written(t, out)
	part := at(t, report, "standalone").(map[string]any)
	// The JSON's keys: the window, the totals for HTTP, standalone and both, the assumption, the resources, those left out
	// and what the scan could not read.
	var keys []string
	for k := range part {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"assumption", "leftOut", "resources", "totals", "unread", "window"}) {
		t.Fatalf("keys %q", keys)
	}
	if at(t, report, "options", "standalone", "window") != 120.0 || at(t, part, "assumption") != estimate.StandaloneAssumption {
		t.Fatalf("options %v", at(t, report, "options"))
	}
	if at(t, part, "totals", "standalone", "count") != 2.0 || at(t, part, "totals", "http", "count") != 1.0 || at(t, part, "totals", "both", "count") != 3.0 ||
		at(t, report, "totals", "standalone") != 2.0 {
		t.Fatalf("totals %v", at(t, part, "totals"))
	}
	if at(t, part, "resources", 0, "resource", "kind") != "instance" || at(t, part, "resources", 1, "resource", "kind") != "rds-instance" {
		t.Fatalf("resources %v", at(t, part, "resources"))
	}
	if at(t, part, "leftOut", 0, "reason") != "instance i-spot is a Spot instance: Dormeto stops On-Demand instances only" {
		t.Fatalf("left out %v", at(t, part, "leftOut"))
	}
	if !strings.HasPrefix(at(t, part, "unread", 0, "reason").(string), "RDS clusters not listed: the discovery role may not call rds:DescribeDBClusters") {
		t.Fatalf("unread %v", at(t, part, "unread"))
	}
	b, _ := os.ReadFile(filepath.Join(out, "estimate-2026-10-10.md"))
	md := string(b)
	for _, want := range []string{
		"| Account | Name | Workloads | ASG | EC2 | ECS | EKS | RDS | Standalone | Excluded |",
		"### HTTP and standalone",
		"| HTTP | 1 |", "| Standalone | 2 |", "| **Both** | **3** |",
		estimate.StandaloneAssumption,
		"| Kind | Account | Region | Load balancer | Hosts | Backend |", "| http | " + acctA + " |",
		"## Standalone resources", "| standalone | " + acctA + " | " + regR + " | instance build-agent (i-9) | 100% |",
		"| standalone | " + acctA + " | " + regR + " | RDS instance reports | 100% |",
		"| " + acctA + " | " + regR + " | instance i-spot | instance i-spot is a Spot instance: Dormeto stops On-Demand instances only |",
		"- " + acctA + " " + regR + ": RDS clusters not listed: the discovery role may not call rds:DescribeDBClusters",
		"- Standalone resources: estimated (--standalone on), at a 2-hour idle window (--standalone-window), idle by their default signals",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("the Markdown without %q:\n%s", want, md)
		}
	}
	// The assumption once.
	if n := strings.Count(md, estimate.StandaloneAssumption); n != 1 {
		t.Errorf("the assumption %d times", n)
	}
	logged := strings.Join(r.logged(), "\n")
	for _, want := range []string{"for 1 workload and 2 standalone resources in 1 account", "HTTP and standalone:", estimate.StandaloneAssumption, "Standalone resources that would save the most:"} {
		if !strings.Contains(logged, want) {
			t.Errorf("the summary without %q:\n%s", want, logged)
		}
	}
}

// The standalone part's cache files are their own: a second run reads them, --refresh discovery scans again,
// --refresh metrics reads the signals again, and --standalone off reads none of them (nor asks AWS anything of them).
func TestTheStandaloneResourcesAreKeptApartAndReadOnlyWhenAsked(t *testing.T) {
	e, c, scans, signals := standaloneAWS()
	r := newRun(t, e)
	out := t.TempDir()
	runs := func(args ...string) {
		t.Helper()
		if code := Main(append([]string{"--profile", "p", "--out", out}, args...), r.deps); code != 0 {
			t.Fatalf("%q: exit code %d: %q", args, code, r.errors())
		}
	}
	runs()
	if *scans != 1 || *signals != 2 {
		t.Fatalf("standalone scans %d, signal reads %d", *scans, *signals)
	}
	dir := filepath.Join(r.cacheRoot, acctA)
	for _, f := range []string{"standalone/" + acctA + "-" + regR + ".json", "signals/" + acctA + "-" + regR + ".json", "discovery/" + acctA + "-" + regR + ".json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not kept: %v", f, err)
		}
	}
	before := c.counts()
	runs()
	if *scans != 1 || *signals != 2 || c.counts() != before {
		t.Fatalf("from the cache: standalone scans %d, signal reads %d, %v then %v", *scans, *signals, before, c.counts())
	}
	runs("--refresh", "metrics")
	if *scans != 1 || *signals != 4 {
		t.Fatalf("--refresh metrics: standalone scans %d, signal reads %d", *scans, *signals)
	}
	runs("--refresh", "discovery")
	if *scans != 2 || *signals != 6 {
		t.Fatalf("--refresh discovery: standalone scans %d, signal reads %d", *scans, *signals)
	}
	// Off: the standalone files kept are not read (removed, nothing notices), nothing of them is asked.
	if err := os.RemoveAll(filepath.Join(dir, "standalone")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "signals")); err != nil {
		t.Fatal(err)
	}
	runs("--standalone", "off")
	if *scans != 2 || *signals != 6 {
		t.Fatalf("off: standalone scans %d, signal reads %d", *scans, *signals)
	}
	var report map[string]json.RawMessage
	b, _ := os.ReadFile(filepath.Join(out, "estimate-2026-10-10.json"))
	if err := json.Unmarshal(b, &report); err != nil {
		t.Fatal(err)
	}
	if _, ok := report["standalone"]; ok {
		t.Fatal("off: a standalone part")
	}
}

// A standalone scan that fails, but for a refusal, leaves the region's HTTP workloads estimated: its error is the
// region's standalone problem.
func TestAFailedStandaloneScanLeavesTheRegionsHTTPWorkloadsEstimated(t *testing.T) {
	e, _, _, _ := standaloneAWS()
	failed := 0
	ports := e.PortsOf
	e.PortsOf = func(ctx context.Context, account, region string, creds *aws.Credentials) (discover.RegionPorts, error) {
		p, err := ports(ctx, account, region, creds)
		p.Standalone = func(context.Context) (discover.ScanResult, error) {
			failed++
			return discover.ScanResult{}, &smithy.GenericAPIError{Code: "TargetGroupNotFound", Message: "One or more target groups not found"}
		}
		return p, err
	}
	r := newRun(t, e)
	out := t.TempDir()
	if code := Main([]string{"--profile", "p", "--out", out}, r.deps); code != 0 {
		t.Fatalf("exit code %d: %q", code, r.errors())
	}
	report := written(t, out)
	if at(t, report, "totals", "workloads") != 1.0 || len(at(t, report, "failedRegions").([]any)) != 0 {
		t.Fatalf("totals %v, failed %v", at(t, report, "totals"), at(t, report, "failedRegions"))
	}
	if got := at(t, report, "standalone", "unread", 0, "reason"); got != "standalone resources not read in "+acctA+" "+regR+": TargetGroupNotFound: One or more target groups not found" {
		t.Fatalf("unread %v", got)
	}
	// Not kept: the next run scans the region again, as a region that failed.
	if code := Main([]string{"--profile", "p", "--out", out}, r.deps); code != 0 || failed != 2 {
		t.Fatalf("second run: exit code %d, standalone scans %d", code, failed)
	}
}
