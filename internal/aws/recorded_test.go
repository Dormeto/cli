package aws

// Ruling P20: every AWS answer the shared cases recorded (testdata/parity estimate.json, scan.json, stopped.json and
// prices.json) through its adapter, so the organization path (Organizations, AssumeRole, several accounts, Auto Scaling,
// Fargate capacity providers, RDS clusters, EKS) runs through internal/aws as the live command runs it, where the
// end-to-end case bypasses the adapters by design.
//
// The recorded output, decoded into the SDK's own output type (encoding/json maps the API's PascalCase names, and ECS's
// and EKS's camelCase ones, to the SDK's fields; enums are strings, timestamps ISO strings; a recorded field the SDK type
// has no field for fails), is what a fake SDK client answers the adapter. The adapter's port output must equal the
// recorded output decoded into the port's own type, as the parity fakes hand it to the code under test: a field an
// adapter drops or mangles fails. The SDK input the adapter builds from the recorded port input must be the recorded
// input (the call Node made): a field it drops fails. A recorded error is returned as it is.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/kubernetes"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/parity"
	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/stopped"
)

// replay is a fake SDK client's one call: the output it answers (a pointer to the SDK's output type) or the error, and
// the input the adapter gave it.
type replay struct {
	out any
	err error
	in  any
}

func answer[O any](r *replay, in any) (*O, error) {
	r.in = in
	if r.err != nil {
		return nil, r.err
	}
	out, ok := r.out.(*O)
	if !ok {
		return nil, fmt.Errorf("replay: asked for %T, holding %T", out, r.out)
	}
	return out, nil
}

// The fake SDK clients, one per service (their methods share names).
type (
	replayELB     struct{ *replay }
	replayASG     struct{ *replay }
	replayEC2     struct{ *replay }
	replayECS     struct{ *replay }
	replayCW      struct{ *replay }
	replayRDS     struct{ *replay }
	replayEKS     struct{ *replay }
	replaySTS     struct{ *replay }
	replayOrg     struct{ *replay }
	replayPricing struct{ *replay }
)

func (f replayELB) DescribeLoadBalancers(_ context.Context, in *elb.DescribeLoadBalancersInput, _ ...func(*elb.Options)) (*elb.DescribeLoadBalancersOutput, error) {
	return answer[elb.DescribeLoadBalancersOutput](f.replay, in)
}
func (f replayELB) DescribeListeners(_ context.Context, in *elb.DescribeListenersInput, _ ...func(*elb.Options)) (*elb.DescribeListenersOutput, error) {
	return answer[elb.DescribeListenersOutput](f.replay, in)
}
func (f replayELB) DescribeRules(_ context.Context, in *elb.DescribeRulesInput, _ ...func(*elb.Options)) (*elb.DescribeRulesOutput, error) {
	return answer[elb.DescribeRulesOutput](f.replay, in)
}
func (f replayELB) DescribeTags(_ context.Context, in *elb.DescribeTagsInput, _ ...func(*elb.Options)) (*elb.DescribeTagsOutput, error) {
	return answer[elb.DescribeTagsOutput](f.replay, in)
}
func (f replayELB) DescribeTargetGroups(_ context.Context, in *elb.DescribeTargetGroupsInput, _ ...func(*elb.Options)) (*elb.DescribeTargetGroupsOutput, error) {
	return answer[elb.DescribeTargetGroupsOutput](f.replay, in)
}
func (f replayELB) DescribeTargetHealth(_ context.Context, in *elb.DescribeTargetHealthInput, _ ...func(*elb.Options)) (*elb.DescribeTargetHealthOutput, error) {
	return answer[elb.DescribeTargetHealthOutput](f.replay, in)
}
func (f replayASG) DescribeAutoScalingGroups(_ context.Context, in *autoscaling.DescribeAutoScalingGroupsInput, _ ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
	return answer[autoscaling.DescribeAutoScalingGroupsOutput](f.replay, in)
}
func (f replayEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return answer[ec2.DescribeInstancesOutput](f.replay, in)
}
func (f replayEC2) DescribeRegions(_ context.Context, in *ec2.DescribeRegionsInput, _ ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error) {
	return answer[ec2.DescribeRegionsOutput](f.replay, in)
}
func (f replayEC2) DescribeSecurityGroups(_ context.Context, in *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return answer[ec2.DescribeSecurityGroupsOutput](f.replay, in)
}
func (f replayEC2) DescribeSpotPriceHistory(_ context.Context, in *ec2.DescribeSpotPriceHistoryInput, _ ...func(*ec2.Options)) (*ec2.DescribeSpotPriceHistoryOutput, error) {
	return answer[ec2.DescribeSpotPriceHistoryOutput](f.replay, in)
}
func (f replayECS) ListClusters(_ context.Context, in *ecs.ListClustersInput, _ ...func(*ecs.Options)) (*ecs.ListClustersOutput, error) {
	return answer[ecs.ListClustersOutput](f.replay, in)
}
func (f replayECS) ListServices(_ context.Context, in *ecs.ListServicesInput, _ ...func(*ecs.Options)) (*ecs.ListServicesOutput, error) {
	return answer[ecs.ListServicesOutput](f.replay, in)
}
func (f replayECS) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	return answer[ecs.DescribeServicesOutput](f.replay, in)
}
func (f replayECS) DescribeTaskDefinition(_ context.Context, in *ecs.DescribeTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	return answer[ecs.DescribeTaskDefinitionOutput](f.replay, in)
}
func (f replayCW) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	return answer[cloudwatch.GetMetricDataOutput](f.replay, in)
}
func (f replayRDS) DescribeDBInstances(_ context.Context, in *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	return answer[rds.DescribeDBInstancesOutput](f.replay, in)
}
func (f replayRDS) DescribeDBClusters(_ context.Context, in *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	return answer[rds.DescribeDBClustersOutput](f.replay, in)
}
func (f replayEKS) ListClusters(_ context.Context, in *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	return answer[eks.ListClustersOutput](f.replay, in)
}
func (f replayEKS) DescribeCluster(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	return answer[eks.DescribeClusterOutput](f.replay, in)
}
func (f replaySTS) GetCallerIdentity(_ context.Context, in *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return answer[sts.GetCallerIdentityOutput](f.replay, in)
}
func (f replaySTS) AssumeRole(_ context.Context, in *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	return answer[sts.AssumeRoleOutput](f.replay, in)
}
func (f replayOrg) DescribeOrganization(_ context.Context, in *organizations.DescribeOrganizationInput, _ ...func(*organizations.Options)) (*organizations.DescribeOrganizationOutput, error) {
	return answer[organizations.DescribeOrganizationOutput](f.replay, in)
}
func (f replayOrg) ListRoots(_ context.Context, in *organizations.ListRootsInput, _ ...func(*organizations.Options)) (*organizations.ListRootsOutput, error) {
	return answer[organizations.ListRootsOutput](f.replay, in)
}
func (f replayOrg) ListOrganizationalUnitsForParent(_ context.Context, in *organizations.ListOrganizationalUnitsForParentInput, _ ...func(*organizations.Options)) (*organizations.ListOrganizationalUnitsForParentOutput, error) {
	return answer[organizations.ListOrganizationalUnitsForParentOutput](f.replay, in)
}
func (f replayOrg) ListAccountsForParent(_ context.Context, in *organizations.ListAccountsForParentInput, _ ...func(*organizations.Options)) (*organizations.ListAccountsForParentOutput, error) {
	return answer[organizations.ListAccountsForParentOutput](f.replay, in)
}
func (f replayOrg) ListTagsForResource(_ context.Context, in *organizations.ListTagsForResourceInput, _ ...func(*organizations.Options)) (*organizations.ListTagsForResourceOutput, error) {
	return answer[organizations.ListTagsForResourceOutput](f.replay, in)
}
func (f replayPricing) GetProducts(_ context.Context, in *pricing.GetProductsInput, _ ...func(*pricing.Options)) (*pricing.GetProductsOutput, error) {
	return answer[pricing.GetProductsOutput](f.replay, in)
}

// recordedCall is one recorded answer: where it comes from, the port input as the code passed it (the estimate's scope
// removed: the client's region kept apart), and the output or the error.
type recordedCall struct {
	where, key    string
	region        string
	input, output json.RawMessage
	err           *parity.RecordedError
}

// recordedCalls are every recorded AWS answer of the shared cases, the clusters' API left out (internal/kubernetes).
func recordedCalls(t *testing.T) []recordedCall {
	t.Helper()
	var calls []recordedCall
	for _, topic := range []string{"estimate", "scan", "stopped", "prices"} {
		for n, c := range parity.Load(t, topic).Cases {
			var in struct {
				Answers map[string][]struct {
					Input  json.RawMessage       `json:"input"`
					Output json.RawMessage       `json:"output"`
					Error  *parity.RecordedError `json:"error"`
				} `json:"answers"`
			}
			if err := json.Unmarshal(c.Input, &in); err != nil {
				t.Fatalf("%s %q: %v", topic, c.Name, err)
			}
			for _, key := range slices.Sorted(maps.Keys(in.Answers)) {
				if strings.HasPrefix(key, "kubernetes.") {
					continue
				}
				for i, e := range in.Answers[key] {
					call := recordedCall{where: fmt.Sprintf("%s/%d/%s/%d", topic, n, key, i), key: key, input: e.Input, output: e.Output, err: e.Error}
					if topic == "estimate" {
						// {"account", "region", "input"}: the account whose credentials make the call, the client's region.
						var scoped struct {
							Region string          `json:"region"`
							Input  json.RawMessage `json:"input"`
						}
						decodeJSON(t, call.input, &scoped)
						call.region, call.input = scoped.Region, scoped.Input
					} else if key == "spot.history" {
						// {"region", "input"}: the price book's history(region, input).
						var asked struct {
							Region string          `json:"region"`
							Input  json.RawMessage `json:"input"`
						}
						decodeJSON(t, call.input, &asked)
						call.region, call.input = asked.Region, asked.Input
					}
					calls = append(calls, call)
				}
			}
		}
	}
	return calls
}

func decodeJSON(t *testing.T, b json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %T: %v: %s", v, err, b)
	}
}

func encodeJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode %T: %v", v, err)
	}
	return b
}

// sdkOutput is the recorded output decoded into the SDK's output type: a recorded field the type has no field for
// fails (it would be lost before the adapter sees it).
func sdkOutput[O any](t *testing.T, output json.RawMessage) *O {
	t.Helper()
	out := new(O)
	dec := json.NewDecoder(bytes.NewReader(output))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		t.Fatalf("the recorded output does not decode into %T: %v\n%s", out, err, output)
	}
	return out
}

// normalized is a call's input as JSON compares the SDK's with Node's: keys in lower case (the SDK's Go fields against
// ECS's and EKS's camelCase), what is unset (null, "") absent, times as instants.
func normalized(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, value := range x {
			if value == nil || value == "" {
				continue
			}
			out[strings.ToLower(k)] = normalized(value)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalized(x[i])
		}
		return out
	case string:
		if at, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return at.UTC().Format(time.RFC3339Nano)
		}
	}
	return v
}

// sameInput fails unless the SDK input the adapter built is the recorded input.
func sameInput(t *testing.T, recorded json.RawMessage, sdk any) {
	t.Helper()
	var want, got any
	decodeJSON(t, recorded, &want)
	decodeJSON(t, encodeJSON(t, sdk), &got)
	parity.Equal(t, encodeJSON(t, normalized(want)), encodeJSON(t, normalized(got)))
}

// adapterView is an adapter seen through one port: it calls the adapter with the recorded call over a fake SDK
// client, and checks what the port gives.
type adapterView struct {
	name string
	run  func(t *testing.T, c recordedCall)
}

// through is a view whose port speaks the API's JSON: O the SDK's output, PI and PO the port's input and output. input
// says which calls' SDK input is compared (nil: every call's): not those the port cannot make (another view's).
func through[O, PI, PO any](name string, input func(c recordedCall) bool, call func(ctx context.Context, r *replay, c recordedCall, in PI) (PO, error)) adapterView {
	return adapterView{name: name, run: func(t *testing.T, c recordedCall) {
		r := &replay{}
		if c.err != nil {
			r.err = parity.AWSError(c.err)
		} else {
			r.out = sdkOutput[O](t, c.output)
		}
		var in PI
		decodeJSON(t, c.input, &in)
		got, err := call(context.Background(), r, c, in)
		if c.err != nil {
			if !errors.Is(err, r.err) {
				t.Errorf("error %v, not the SDK's %v as it is", err, r.err)
			}
		} else if err != nil {
			t.Fatal(err)
		} else {
			var want PO
			decodeJSON(t, c.output, &want)
			parity.Equal(t, encodeJSON(t, want), encodeJSON(t, got))
		}
		if input == nil || input(c) {
			sameInput(t, c.input, r.in)
		}
	}}
}

// scanBy says a GetMetricData call is the estimate's (metrics.CloudWatchPort): it reads the oldest values first.
func scanBy(c recordedCall) bool {
	var in struct{ ScanBy string }
	_ = json.Unmarshal(c.input, &in)
	return in.ScanBy != ""
}

func notScanBy(c recordedCall) bool { return !scanBy(c) }

// adapterViews are the adapters by recorded port method, each port that reads the method a view.
var adapterViews = map[string][]adapterView{
	"elbv2.describeLoadBalancers": {through[elb.DescribeLoadBalancersOutput]("elbv2Port.DescribeLoadBalancers", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.DescribeLoadBalancersInput) (discover.DescribeLoadBalancersOutput, error) {
			return elbv2Port{replayELB{r}}.DescribeLoadBalancers(ctx, in)
		})},
	"elbv2.describeListeners": {through[elb.DescribeListenersOutput]("elbv2Port.DescribeListeners", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.DescribeListenersInput) (discover.DescribeListenersOutput, error) {
			return elbv2Port{replayELB{r}}.DescribeListeners(ctx, in)
		})},
	"elbv2.describeRules": {through[elb.DescribeRulesOutput]("elbv2Port.DescribeRules", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.DescribeRulesInput) (rulesRead, error) {
			out, err := elbv2Port{replayELB{r}}.DescribeRules(ctx, in)
			return rulesRead{out}, err
		})},
	"elbv2.describeTags": {through[elb.DescribeTagsOutput]("elbv2Port.DescribeTags", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.DescribeTagsInput) (discover.DescribeTagsOutput, error) {
			return elbv2Port{replayELB{r}}.DescribeTags(ctx, in)
		})},
	"elbv2.describeTargetGroups": {through[elb.DescribeTargetGroupsOutput]("elbv2Port.DescribeTargetGroups", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.DescribeTargetGroupsInput) (discover.DescribeTargetGroupsOutput, error) {
			return elbv2Port{replayELB{r}}.DescribeTargetGroups(ctx, in)
		})},
	"elbv2.describeTargetHealth": {through[elb.DescribeTargetHealthOutput]("elbv2Port.DescribeTargetHealth", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.DescribeTargetHealthInput) (discover.DescribeTargetHealthOutput, error) {
			return elbv2Port{replayELB{r}}.DescribeTargetHealth(ctx, in)
		})},
	"autoscaling.describeAutoScalingGroups": {through[autoscaling.DescribeAutoScalingGroupsOutput]("autoScalingPort.DescribeAutoScalingGroups", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.DescribeAutoScalingGroupsInput) (discover.DescribeAutoScalingGroupsOutput, error) {
			return autoScalingPort{replayASG{r}}.DescribeAutoScalingGroups(ctx, in)
		})},
	"ec2.describeInstances": {
		through[ec2.DescribeInstancesOutput]("scanEC2.DescribeInstances", nil,
			func(ctx context.Context, r *replay, _ recordedCall, in discover.DescribeInstancesInput) (discover.DescribeInstancesOutput, error) {
				return scanEC2{replayEC2{r}}.DescribeInstances(ctx, in)
			}),
		through[ec2.DescribeInstancesOutput]("stoppedEC2.DescribeInstances", nil,
			func(ctx context.Context, r *replay, _ recordedCall, in stopped.DescribeInstancesInput) (stopped.DescribeInstancesOutput, error) {
				return stoppedEC2{replayEC2{r}}.DescribeInstances(ctx, in)
			}),
	},
	"ec2.describeSecurityGroups": {through[ec2.DescribeSecurityGroupsOutput]("stoppedEC2.DescribeSecurityGroups", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.DescribeSecurityGroupsInput) (discover.DescribeSecurityGroupsOutput, error) {
			return stoppedEC2{replayEC2{r}}.DescribeSecurityGroups(ctx, in)
		})},
	// The enabled regions: their names (Node's regionsOf).
	"ec2.describeRegions": {through[ec2.DescribeRegionsOutput]("enabledRegions", nil,
		func(ctx context.Context, r *replay, c recordedCall, _ struct{}) (regionNames, error) {
			names, err := enabledRegions(ctx, replayEC2{r})
			return regionNames{names}, err
		})},
	"spot.history": {through[ec2.DescribeSpotPriceHistoryOutput]("spotPort.History", nil,
		func(ctx context.Context, r *replay, c recordedCall, in prices.SpotHistoryInput) (prices.SpotHistoryOutput, error) {
			var asked string
			out, err := newSpot(func(region string) spotAPI { asked = region; return replayEC2{r} }).History(ctx, c.region, in)
			if asked != c.region {
				return out, fmt.Errorf("a client made in %q, asked in %q", asked, c.region)
			}
			return out, err
		})},
	"ecs.listClusters": {through[ecs.ListClustersOutput]("scanECS.ListClusters", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.ListClustersInput) (discover.ListClustersOutput, error) {
			return scanECS{replayECS{r}}.ListClusters(ctx, in)
		})},
	"ecs.listServices": {through[ecs.ListServicesOutput]("scanECS.ListServices", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.ListServicesInput) (discover.ListServicesOutput, error) {
			return scanECS{replayECS{r}}.ListServices(ctx, in)
		})},
	"ecs.describeServices": {
		through[ecs.DescribeServicesOutput]("scanECS.DescribeServices", nil,
			func(ctx context.Context, r *replay, _ recordedCall, in discover.DescribeServicesInput) (discover.DescribeServicesOutput, error) {
				return scanECS{replayECS{r}}.DescribeServices(ctx, in)
			}),
		through[ecs.DescribeServicesOutput]("stoppedECS.DescribeServices", nil,
			func(ctx context.Context, r *replay, _ recordedCall, in stopped.DescribeServicesInput) (stopped.DescribeServicesOutput, error) {
				return stoppedECS{replayECS{r}}.DescribeServices(ctx, in)
			}),
	},
	"ecs.describeTaskDefinition": {through[ecs.DescribeTaskDefinitionOutput]("stoppedECS.DescribeTaskDefinition", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in stopped.DescribeTaskDefinitionInput) (stopped.DescribeTaskDefinitionOutput, error) {
			return stoppedECS{replayECS{r}}.DescribeTaskDefinition(ctx, in)
		})},
	// The scan's hourly requests (discover.CloudWatchPort) and the estimate's (metrics.CloudWatchPort): each output
	// through both views, each call's input through the view that makes it.
	"cloudwatch.getMetricData": {
		through[cloudwatch.GetMetricDataOutput]("cloudWatchPort.GetMetricData", notScanBy,
			func(ctx context.Context, r *replay, _ recordedCall, in discover.GetMetricDataInput) (discover.GetMetricDataOutput, error) {
				return cloudWatchPort{replayCW{r}}.GetMetricData(ctx, in)
			}),
		through[cloudwatch.GetMetricDataOutput]("metricsPort.GetMetricData", scanBy,
			func(ctx context.Context, r *replay, _ recordedCall, in metrics.GetMetricDataInput) (metrics.GetMetricDataOutput, error) {
				return metricsPort{cloudWatchPort{replayCW{r}}}.GetMetricData(ctx, in)
			}),
	},
	"rds.describeDBInstances": {through[rds.DescribeDBInstancesOutput]("rdsPort.DescribeDBInstances", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in stopped.DescribeDBInstancesInput) (stopped.DescribeDBInstancesOutput, error) {
			return rdsPort{replayRDS{r}}.DescribeDBInstances(ctx, in)
		})},
	"rds.describeDBClusters": {through[rds.DescribeDBClustersOutput]("rdsPort.DescribeDBClusters", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in stopped.DescribeDBClustersInput) (stopped.DescribeDBClustersOutput, error) {
			return rdsPort{replayRDS{r}}.DescribeDBClusters(ctx, in)
		})},
	"eks.listClusters": {through[eks.ListClustersOutput]("eksPort.ListClusters", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in kubernetes.ListClustersInput) (kubernetes.ListClustersOutput, error) {
			return eksPort{replayEKS{r}}.ListClusters(ctx, in)
		})},
	"eks.describeCluster": {through[eks.DescribeClusterOutput]("eksPort.DescribeCluster", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in kubernetes.DescribeClusterInput) (kubernetes.DescribeClusterOutput, error) {
			return eksPort{replayEKS{r}}.DescribeCluster(ctx, in)
		})},
	"organizations.describeOrganization": {through[organizations.DescribeOrganizationOutput]("accountsPort.Organization", nil,
		func(ctx context.Context, r *replay, _ recordedCall, _ struct{}) (organizationID, error) {
			id, err := (&accountsPort{organizations: organizationsPort{replayOrg{r}}}).Organization(ctx)
			return organizationID{Organization: struct{ Id string }{id}}, err
		})},
	"organizations.listRoots": {through[organizations.ListRootsOutput]("organizationsPort.ListRoots", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.ListRootsInput) (discover.ListRootsOutput, error) {
			return organizationsPort{replayOrg{r}}.ListRoots(ctx, in)
		})},
	"organizations.listOrganizationalUnitsForParent": {through[organizations.ListOrganizationalUnitsForParentOutput]("organizationsPort.ListOrganizationalUnitsForParent", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.ListOrganizationalUnitsForParentInput) (discover.ListOrganizationalUnitsForParentOutput, error) {
			return organizationsPort{replayOrg{r}}.ListOrganizationalUnitsForParent(ctx, in)
		})},
	"organizations.listAccountsForParent": {through[organizations.ListAccountsForParentOutput]("organizationsPort.ListAccountsForParent", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.ListAccountsForParentInput) (discover.ListAccountsForParentOutput, error) {
			return organizationsPort{replayOrg{r}}.ListAccountsForParent(ctx, in)
		})},
	"organizations.listTagsForResource": {through[organizations.ListTagsForResourceOutput]("organizationsPort.ListTagsForResource", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in discover.ListTagsForResourceInput) (discover.ListTagsForResourceOutput, error) {
			return organizationsPort{replayOrg{r}}.ListTagsForResource(ctx, in)
		})},
	"sts.getCallerIdentity": {through[sts.GetCallerIdentityOutput]("accountsPort.Whoami", nil,
		func(ctx context.Context, r *replay, _ recordedCall, _ struct{}) (callerAccount, error) {
			account, err := (&accountsPort{sts: replaySTS{r}}).Whoami(ctx)
			return callerAccount{account}, err
		})},
	// A member account's role: the account and role named by the recorded RoleArn, assumed as Node asked.
	"sts.assumeRole": {through[sts.AssumeRoleOutput]("accountsPort.Assume", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in struct{ RoleArn string }) (roleCredentials, error) {
			account, role, ok := strings.Cut(strings.TrimPrefix(in.RoleArn, "arn:aws:iam::"), ":role/")
			if !ok {
				return roleCredentials{}, fmt.Errorf("a role ARN %q", in.RoleArn)
			}
			creds, err := (&accountsPort{sts: replaySTS{r}}).Assume(ctx, account, role)
			if err == nil && !creds.CanExpire {
				err = errors.New("credentials that never expire")
			}
			return roleCredentialsOf(creds), err
		})},
	"pricing.getProducts": {through[pricing.GetProductsOutput]("pricingPort.GetProducts", nil,
		func(ctx context.Context, r *replay, _ recordedCall, in prices.GetProductsInput) (prices.GetProductsOutput, error) {
			return (&pricingPort{replayPricing{r}}).GetProducts(ctx, in)
		})},
}

// The ports that do not speak the API's JSON, in the recorded output's shape: what each reads of it.
type (
	// regionNames are the enabled regions' names, as DescribeRegions lists them.
	regionNames struct{ names []string }
	// organizationID is the organization's id, as DescribeOrganization gives it.
	organizationID struct{ Organization struct{ Id string } }
	// callerAccount is the profile's account, as GetCallerIdentity gives it.
	callerAccount struct{ Account string }
	// roleCredentials are a role's credentials, as AssumeRole gives them.
	roleCredentials struct {
		Credentials struct {
			AccessKeyId, SecretAccessKey, SessionToken string
			Expiration                                 time.Time
		}
	}
)

// rulesRead is DescribeRules's output with, beside it, each condition's fields as the scan reads them: a condition
// marshals as its raw JSON alone (discover.Condition), which would hide a field the adapter dropped from the others.
type rulesRead struct{ discover.DescribeRulesOutput }

func (r rulesRead) MarshalJSON() ([]byte, error) {
	read := make([][]discover.Condition, len(r.Rules))
	for i, rule := range r.Rules {
		read[i] = append([]discover.Condition{}, rule.Conditions...) // none and an empty list alike, as the port's JSON
		for j := range read[i] {
			read[i][j].Raw = ""
		}
	}
	return json.Marshal(map[string]any{"output": r.DescribeRulesOutput, "conditions": read})
}

func (r *rulesRead) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &r.DescribeRulesOutput) }

func (r regionNames) MarshalJSON() ([]byte, error) {
	regions := []map[string]string{}
	for _, name := range r.names {
		regions = append(regions, map[string]string{"RegionName": name})
	}
	return json.Marshal(map[string]any{"Regions": regions})
}

func (r *regionNames) UnmarshalJSON(b []byte) error {
	var out struct{ Regions []struct{ RegionName string } }
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	for _, region := range out.Regions {
		r.names = append(r.names, region.RegionName)
	}
	return nil
}

func roleCredentialsOf(c awssdk.Credentials) roleCredentials {
	var r roleCredentials
	r.Credentials.AccessKeyId, r.Credentials.SecretAccessKey, r.Credentials.SessionToken, r.Credentials.Expiration = c.AccessKeyID, c.SecretAccessKey, c.SessionToken, c.Expires
	return r
}

func TestEveryRecordedAWSAnswerThroughItsAdapterGivesThePortsOutput(t *testing.T) {
	ran := map[string]int{}
	for _, c := range recordedCalls(t) {
		views, ok := adapterViews[c.key]
		if !ok {
			t.Errorf("%s: no adapter for %s", c.where, c.key)
			continue
		}
		for _, v := range views {
			ran[v.name]++
			t.Run(c.where+"/"+v.name, func(t *testing.T) { v.run(t, c) })
		}
	}
	// Every adapter's view met recorded answers.
	for _, key := range slices.Sorted(maps.Keys(adapterViews)) {
		for _, v := range adapterViews[key] {
			if ran[v.name] == 0 {
				t.Errorf("%s: no recorded answer", v.name)
			}
		}
	}
	t.Logf("%d adapter views over the recorded answers: %v", len(ran), ran)
}
