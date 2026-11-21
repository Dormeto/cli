package discover

// Replays the shared `scanRegion` cases (testdata/parity/scan.json) over the
// recorded answers.

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/kubernetes/kubernetestest"
	"github.com/Dormeto/cli/internal/parity"
	"github.com/Dormeto/cli/internal/stopped"
)

type recordedELB struct{ a *parity.Answers }

func (r recordedELB) DescribeLoadBalancers(_ context.Context, in DescribeLoadBalancersInput) (DescribeLoadBalancersOutput, error) {
	var out DescribeLoadBalancersOutput
	err := r.a.Call("elbv2", "describeLoadBalancers", in, &out)
	return out, parity.AWSError(err)
}

func (r recordedELB) DescribeListeners(_ context.Context, in DescribeListenersInput) (DescribeListenersOutput, error) {
	var out DescribeListenersOutput
	err := r.a.Call("elbv2", "describeListeners", in, &out)
	return out, parity.AWSError(err)
}

func (r recordedELB) DescribeRules(_ context.Context, in DescribeRulesInput) (DescribeRulesOutput, error) {
	var out DescribeRulesOutput
	err := r.a.Call("elbv2", "describeRules", in, &out)
	return out, parity.AWSError(err)
}

func (r recordedELB) DescribeTags(_ context.Context, in DescribeTagsInput) (DescribeTagsOutput, error) {
	var out DescribeTagsOutput
	err := r.a.Call("elbv2", "describeTags", in, &out)
	return out, parity.AWSError(err)
}

func (r recordedELB) DescribeTargetGroups(_ context.Context, in DescribeTargetGroupsInput) (DescribeTargetGroupsOutput, error) {
	var out DescribeTargetGroupsOutput
	err := r.a.Call("elbv2", "describeTargetGroups", in, &out)
	return out, parity.AWSError(err)
}

func (r recordedELB) DescribeTargetHealth(_ context.Context, in DescribeTargetHealthInput) (DescribeTargetHealthOutput, error) {
	var out DescribeTargetHealthOutput
	err := r.a.Call("elbv2", "describeTargetHealth", in, &out)
	return out, parity.AWSError(err)
}

type recordedASG struct{ a *parity.Answers }

func (r recordedASG) DescribeAutoScalingGroups(_ context.Context, in DescribeAutoScalingGroupsInput) (DescribeAutoScalingGroupsOutput, error) {
	var out DescribeAutoScalingGroupsOutput
	err := r.a.Call("autoscaling", "describeAutoScalingGroups", in, &out)
	return out, parity.AWSError(err)
}

type recordedEC2 struct{ a *parity.Answers }

func (r recordedEC2) DescribeInstances(_ context.Context, in DescribeInstancesInput) (DescribeInstancesOutput, error) {
	var out DescribeInstancesOutput
	err := r.a.Call("ec2", "describeInstances", in, &out)
	return out, parity.AWSError(err)
}

type recordedECS struct{ a *parity.Answers }

func (r recordedECS) ListClusters(_ context.Context, in ListClustersInput) (ListClustersOutput, error) {
	var out ListClustersOutput
	err := r.a.Call("ecs", "listClusters", in, &out)
	return out, parity.AWSError(err)
}

func (r recordedECS) ListServices(_ context.Context, in ListServicesInput) (ListServicesOutput, error) {
	var out ListServicesOutput
	err := r.a.Call("ecs", "listServices", in, &out)
	return out, parity.AWSError(err)
}

func (r recordedECS) DescribeServices(_ context.Context, in DescribeServicesInput) (DescribeServicesOutput, error) {
	var out DescribeServicesOutput
	err := r.a.Call("ecs", "describeServices", in, &out)
	return out, parity.AWSError(err)
}

type recordedCW struct{ a *parity.Answers }

func (r recordedCW) GetMetricData(_ context.Context, in GetMetricDataInput) (GetMetricDataOutput, error) {
	var out GetMetricDataOutput
	err := r.a.Call("cloudwatch", "getMetricData", in, &out)
	return out, parity.AWSError(err)
}

type recordedRDS struct{ a *parity.Answers }

func (r recordedRDS) DescribeDBInstances(_ context.Context, in stopped.DescribeDBInstancesInput) (stopped.DescribeDBInstancesOutput, error) {
	var out stopped.DescribeDBInstancesOutput
	err := r.a.Call("rds", "describeDBInstances", in, &out)
	return out, parity.AWSError(err)
}

func (r recordedRDS) DescribeDBClusters(_ context.Context, in stopped.DescribeDBClustersInput) (stopped.DescribeDBClustersOutput, error) {
	var out stopped.DescribeDBClustersOutput
	err := r.a.Call("rds", "describeDBClusters", in, &out)
	return out, parity.AWSError(err)
}

func recordedScanPorts(t *testing.T, names []string, answers json.RawMessage, reader string) ScanPorts {
	a := parity.Recorded(t, answers)
	ports := ScanPorts{ELBv2: recordedELB{a}}
	for _, n := range names {
		switch n {
		case "autoscaling":
			ports.AutoScaling = recordedASG{a}
		case "ecs":
			ports.ECS = recordedECS{a}
		case "cloudwatch":
			ports.CloudWatch = recordedCW{a}
		case "ec2":
			ports.EC2 = recordedEC2{a}
		case "rds":
			ports.RDS = recordedRDS{a}
		case "kubernetes":
			k := kubernetestest.RecordedPorts(t, a)
			k.Reader = reader
			ports.Kubernetes = &k
		}
	}
	return ports
}

func TestSharedCasesOfScanRegion(t *testing.T) {
	ran := 0
	for _, c := range parity.Load(t, "scan").Cases {
		var in struct {
			Function   string          `json:"function"`
			Where      Where           `json:"where"`
			Now        string          `json:"now"`
			Ports      []string        `json:"ports"`
			Reader     string          `json:"reader"`
			Standalone bool            `json:"standalone"`
			Answers    json.RawMessage `json:"answers"`
		}
		if err := json.Unmarshal(c.Input, &in); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if in.Function != "scanRegion" {
			continue
		}
		ran++
		t.Run(c.Name, func(t *testing.T) {
			if !slices.Contains(in.Ports, "elbv2") {
				t.Fatalf("ports %v: elbv2 is always given", in.Ports)
			}
			now, err := time.Parse(time.RFC3339Nano, in.Now)
			if err != nil {
				t.Fatal(err)
			}
			res, err := ScanRegionWith(context.Background(), recordedScanPorts(t, in.Ports, in.Answers, in.Reader), in.Where, now, ScanOptions{Standalone: in.Standalone})
			var got any = res
			if in.Standalone && err == nil {
				// Node's output: the standalone candidates after the HTTP ones, and what the scan could not read.
				if res.Standalone == nil {
					t.Fatal("a standalone scan without its standalone part")
				}
				rules := []any{}
				for _, r := range res.Rules {
					rules = append(rules, r)
				}
				for _, r := range res.Standalone.Rules {
					rules = append(rules, r)
				}
				got = map[string]any{"rules": rules, "clusters": res.Clusters, "standalone": res.Standalone.Report}
			} else if res.Standalone != nil {
				t.Fatal("a standalone part without the standalone scan")
			}
			if err != nil {
				var api smithy.APIError
				if !errors.As(err, &api) {
					t.Fatalf("an error that is not a recorded one: %v", err)
				}
				got = map[string]any{"error": map[string]string{"name": awserr.Name(err), "message": awserr.Message(err)}}
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			parity.Equal(t, c.Output, b)
		})
	}
	if ran != 20 {
		t.Fatalf("ran %d scanRegion cases, the corpus has 20", ran)
	}
}

// noStandaloneIdle fails the test on the standalone idle share's read (5-minute points, oldest first).
type noStandaloneIdle struct {
	t *testing.T
	CloudWatchPort
}

func (c noStandaloneIdle) GetMetricData(ctx context.Context, in GetMetricDataInput) (GetMetricDataOutput, error) {
	if in.ScanBy != "" {
		c.t.Errorf("the standalone idle share read: %+v", in.MetricDataQueries)
	}
	return c.CloudWatchPort.GetMetricData(ctx, in)
}

// Without the standalone idle share (the estimate's scan, which reads the signals over its own days): the shared case's
// candidates all the same, their idleHours7d null, and no read of it.
func TestTheStandaloneScanWithoutItsIdleShareReadsNoneAndGivesTheSameCandidates(t *testing.T) {
	c := parity.Load(t, "scan").Cases[95]
	var in struct {
		Where   Where           `json:"where"`
		Now     time.Time       `json:"now"`
		Ports   []string        `json:"ports"`
		Answers json.RawMessage `json:"answers"`
	}
	if err := json.Unmarshal(c.Input, &in); err != nil || !strings.HasPrefix(c.Name, "scanRegion: standalone: each kind") {
		t.Fatalf("%s: %v", c.Name, err)
	}
	ports := recordedScanPorts(t, in.Ports, in.Answers, "")
	ports.CloudWatch = noStandaloneIdle{t, ports.CloudWatch}
	res, err := ScanRegionWith(context.Background(), ports, in.Where, in.Now, ScanOptions{Standalone: true, WithoutStandaloneIdle: true})
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Rules []json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(c.Output, &want); err != nil {
		t.Fatal(err)
	}
	standalone := want.Rules[len(res.Rules):]
	if len(standalone) != len(res.Standalone.Rules) {
		t.Fatalf("%d standalone candidates, the case's %d", len(res.Standalone.Rules), len(standalone))
	}
	for i, raw := range standalone {
		var rule map[string]any
		_ = json.Unmarshal(raw, &rule)
		rule["idleHours7d"] = nil
		w, _ := json.Marshal(rule)
		g, _ := json.Marshal(res.Standalone.Rules[i])
		parity.Equal(t, w, g)
	}
}
