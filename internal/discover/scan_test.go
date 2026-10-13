package discover

// The cases of manager packages/discovery/test/scan.test.ts.

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/kubernetes"
)

var (
	scanNow   = time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	scanWhere = Where{Account: "111122223333", Region: "eu-west-1"}
)

func scanWorld(t *testing.T, w *world) map[string]ScannedRule {
	t.Helper()
	res, err := ScanRegion(context.Background(), w.ports, scanWhere, scanNow)
	if err != nil {
		t.Fatal(err)
	}
	return byKey(res.Rules)
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %s\nwant %s", describe(got), describe(want))
	}
}

func TestScanRegionGivesOneCandidatePerApplicationTargetGroupAndOnePerRuleForwardingToNone(t *testing.T) {
	found := scanWorld(t, newWorld(t, nil))
	keys := []string{}
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	eq(t, keys, []string{"api", "batch", "demo-app", "empty", "orphan", "r-canary", "r-default", "r-fixed", "r-fn", "r80", "shared", "shop"})
	eq(t, []string{found["demo-app"].Eligibility, found["demo-app"].WorkloadID}, []string{"managed", "demo"})
	// Each backend with its ARN (the dashboard shows it).
	eq(t, found["shop"].Eligibility, "eligible")
	eq(t, found["shop"].Backend, &Backend{Kind: "instance", ID: "i-shop", ARN: "arn:aws:ec2:eu-west-1:111122223333:instance/i-shop"})
	eq(t, found["api"].Backend, &Backend{Kind: "ecs", ID: apiService, ARN: apiService})
	eq(t, found["batch"].Backend, &Backend{Kind: "asg", ID: "batch-asg", ARN: batchASGARN})
	shared := []string{}
	for _, r := range found["shared"].Rules {
		shared = append(shared, r.ID)
	}
	eq(t, []any{found["shared"].Eligibility, shared}, []any{"eligible", []string{"r-shared-1", "r-shared-2"}})
	reasons := map[string]string{}
	for k, c := range found {
		if c.Eligibility == "ineligible" {
			reasons[k] = c.Reason
		}
	}
	eq(t, reasons, map[string]string{
		"r80":       "redirect action",
		"r-canary":  "weighted forward to several target groups",
		"r-fn":      "Lambda target group",
		"orphan":    "IP targets without an ECS service",
		"empty":     "no backend: no registered target, Auto Scaling group or ECS service",
		"r-fixed":   "fixed response",
		"r-default": "fixed response",
	})
	if _, ok := found["r-admin"]; ok {
		t.Error("an admin rule is a candidate")
	}
}

func TestScanRegionStampsTheAccountAndRegionAndDescribesListenerConditionsAndTargetGroup(t *testing.T) {
	found := scanWorld(t, newWorld(t, nil))
	shop := found["shop"]
	eq(t, []string{shop.ID, shop.Account, shop.Region}, []string{tgKey("shop"), "111122223333", "eu-west-1"})
	eq(t, []string{shop.LoadBalancer, shop.Listener, shop.Priority, shop.Conditions, *shop.TargetGroup},
		[]string{"web", "HTTPS:443", "20", "host shop.example.com | www.shop.example.com", "shop"})
	eq(t, found["shared"].Conditions, "host a.example.com (+1 rules)")
	eq(t, found["api"].Conditions, "path /api/*")
	eq(t, found["r-default"].Conditions, "default")
}

func TestScanRegionExtractsTheHostNamesInBothConditionFormats(t *testing.T) {
	found := scanWorld(t, newWorld(t, nil))
	eq(t, found["shop"].Hosts, []string{"shop.example.com", "www.shop.example.com"})
	eq(t, found["batch"].Hosts, []string{"batch.example.com"})
	eq(t, found["shared"].Hosts, []string{"a.example.com", "b.example.com"})
	eq(t, found["api"].Hosts, []string{})
}

func TestScanRegionKeepsEveryValueATagHasOnTheLoadBalancerTheTargetGroupOrTheRules(t *testing.T) {
	w := newWorld(t, nil)
	found := scanWorld(t, w)
	eq(t, found["shop"].Tags, map[string][]string{"env": {"dev", "preview"}, "team": {"web", "shop"}})
	eq(t, found["api"].Tags, map[string][]string{"env": {"dev"}, "team": {"web"}})
	if len(w.tagBatches) < 2 {
		t.Errorf("tags read in %v batches: the fixture needs more than one", w.tagBatches)
	}
	for _, n := range w.tagBatches {
		if n > 20 {
			t.Errorf("a batch of %d", n)
		}
	}
}

func TestScanRegionCountsTheIdleHoursOfTheLast7DaysForEligibleAndManagedGroupsOnly(t *testing.T) {
	w := newWorld(t, map[string][]float64{"targetgroup/shop/1": {5, 0, 3}, "targetgroup/demo-app/1": {}})
	found := scanWorld(t, w)
	eq(t, []int{*found["shop"].IdleHours7d, *found["shop"].Requests7d}, []int{166, 8})
	eq(t, []int{*found["demo-app"].IdleHours7d, *found["demo-app"].Requests7d}, []int{168, 0})
	if found["r-fn"].IdleHours7d != nil {
		t.Error("an ineligible rule has idle hours")
	}
	eq(t, w.metricQueries, 5)
}

func TestScanRegionGivesAManagedGroupTheBackendOfItsApplicationTargetGroup(t *testing.T) {
	found := scanWorld(t, newWorld(t, nil))
	eq(t, found["demo-app"].Backend, &Backend{Kind: "instance", ID: "i-demo", ARN: "arn:aws:ec2:eu-west-1:111122223333:instance/i-demo"})
	eq(t, found["demo-app"].Reason, "")
}

func TestScanRegionReportsAManagedRuleThatNoLongerForwardsToItsApplication(t *testing.T) {
	w := newWorld(t, nil)
	w.rules[l443][0].Actions = []Action{{Type: "redirect", Order: 1}}
	found := scanWorld(t, w)
	r := found["r-demo"]
	eq(t, []string{r.Eligibility, r.WorkloadID, r.Reason}, []string{"managed", "demo", "redirect action"})
}

func TestScanRegionReportsTheApplicationTargetGroupOfASleepingManagedWorkload(t *testing.T) {
	w := newWorld(t, nil)
	w.rules[l443][0].Actions = fwdTo(fwd{"demo", 0}, fwd{"waker", 1})
	found := scanWorld(t, w)
	eq(t, []string{*found["demo-app"].TargetGroup, found["demo-app"].Eligibility}, []string{"demo-app", "managed"})
}

func TestScanRegionPagesOfLoadBalancersListenersRulesAndTargetGroupsAreFollowed(t *testing.T) {
	// The fake pages by five: the 13 rules of the HTTPS listener and 11 target groups take several pages.
	found := scanWorld(t, newWorld(t, nil))
	if _, ok := found["r-default"]; !ok {
		t.Error("the last rule of the last page is missing")
	}
	if _, ok := found["demo-app"]; !ok {
		t.Error("a target group of the third page is missing")
	}
}

func TestScanRegionReturnsAPortsError(t *testing.T) {
	w := newWorld(t, nil)
	w.ports.ELBv2 = refusing{elbWorld{w}}
	_, err := ScanRegion(context.Background(), w.ports, scanWhere, scanNow)
	if err == nil || err.Error() != "denied" {
		t.Errorf("got %v", err)
	}
}

type refusing struct{ ELBv2Port }

func (refusing) DescribeLoadBalancers(context.Context, DescribeLoadBalancersInput) (DescribeLoadBalancersOutput, error) {
	return DescribeLoadBalancersOutput{}, deniedError{}
}

type deniedError struct{}

func (deniedError) Error() string { return "denied" }

// emptyELB is a region without load balancers.
type emptyELB struct{ ELBv2Port }

func (emptyELB) DescribeLoadBalancers(context.Context, DescribeLoadBalancersInput) (DescribeLoadBalancersOutput, error) {
	return DescribeLoadBalancersOutput{}, nil
}

func (emptyELB) DescribeTargetGroups(context.Context, DescribeTargetGroupsInput) (DescribeTargetGroupsOutput, error) {
	return DescribeTargetGroupsOutput{}, nil
}

type failingEKS struct{ err error }

func (f failingEKS) ListClusters(context.Context, kubernetes.ListClustersInput) (kubernetes.ListClustersOutput, error) {
	return kubernetes.ListClustersOutput{}, f.err
}

func (f failingEKS) DescribeCluster(context.Context, kubernetes.DescribeClusterInput) (kubernetes.DescribeClusterOutput, error) {
	return kubernetes.DescribeClusterOutput{}, f.err
}

// sdkError is a failed call as the AWS SDK returns it.
func sdkError(code, message string) error {
	return &smithy.OperationError{ServiceID: "EKS", OperationName: "ListClusters", Err: &smithy.GenericAPIError{Code: code, Message: message}}
}

func TestScanRegionWithoutEKSRightsByTheAWSErrorsCodeScansTheRegionAsBefore(t *testing.T) {
	k := &kubernetes.Ports{EKS: failingEKS{sdkError("AccessDeniedException", "User: arn:aws:sts::111122223333:assumed-role/x/y may not list clusters")}}
	res, err := ScanRegion(context.Background(), ScanPorts{ELBv2: emptyELB{}, Kubernetes: k}, scanWhere, scanNow)
	if err != nil || len(res.Clusters) != 0 {
		t.Fatalf("%v %+v", err, res)
	}
	k.EKS = failingEKS{sdkError("ThrottlingException", "Rate exceeded")}
	_, err = ScanRegion(context.Background(), ScanPorts{ELBv2: emptyELB{}, Kubernetes: k}, scanWhere, scanNow)
	if awserr.Said(err) != "ThrottlingException: Rate exceeded" {
		t.Fatalf("another EKS failure fails the scan: %v", err)
	}
}
