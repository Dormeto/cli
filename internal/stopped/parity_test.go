package stopped

// Replays the shared `stopped` cases (testdata/parity/stopped.json).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Dormeto/cli/internal/kubernetes/kubernetestest"
	"github.com/Dormeto/cli/internal/parity"
)

type recordedEC2 struct{ a *parity.Answers }

func (r recordedEC2) DescribeInstances(_ context.Context, in DescribeInstancesInput) (DescribeInstancesOutput, error) {
	var out DescribeInstancesOutput
	err := r.a.Call("ec2", "describeInstances", in, &out)
	return out, parity.AWSError(err)
}

type recordedECS struct{ a *parity.Answers }

func (r recordedECS) DescribeServices(_ context.Context, in DescribeServicesInput) (DescribeServicesOutput, error) {
	var out DescribeServicesOutput
	err := r.a.Call("ecs", "describeServices", in, &out)
	return out, parity.AWSError(err)
}

func (r recordedECS) DescribeTaskDefinition(_ context.Context, in DescribeTaskDefinitionInput) (DescribeTaskDefinitionOutput, error) {
	var out DescribeTaskDefinitionOutput
	err := r.a.Call("ecs", "describeTaskDefinition", in, &out)
	return out, parity.AWSError(err)
}

type recordedRDS struct{ a *parity.Answers }

func (r recordedRDS) DescribeDBInstances(_ context.Context, in DescribeDBInstancesInput) (DescribeDBInstancesOutput, error) {
	var out DescribeDBInstancesOutput
	err := r.a.Call("rds", "describeDBInstances", in, &out)
	return out, parity.AWSError(err)
}

func (r recordedRDS) DescribeDBClusters(_ context.Context, in DescribeDBClustersInput) (DescribeDBClustersOutput, error) {
	var out DescribeDBClustersOutput
	err := r.a.Call("rds", "describeDBClusters", in, &out)
	return out, parity.AWSError(err)
}

func recordedPorts(t *testing.T, answers json.RawMessage) Ports {
	a := parity.Recorded(t, answers)
	return Ports{
		EC2:          recordedEC2{a},
		ECS:          recordedECS{a},
		RDS:          recordedRDS{a},
		KubernetesOf: kubernetestest.RecordedPorts(t, a).Of,
	}
}

func TestSharedCases(t *testing.T) {
	topic := parity.Load(t, "stopped")
	if len(topic.Cases) != 29 {
		t.Fatalf("shared cases: %d, want 29", len(topic.Cases))
	}
	described, counted := 0, 0
	for _, c := range topic.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var in struct {
				Function string                         `json:"function"`
				Backends []Backend                      `json:"backends"`
				Answers  json.RawMessage                `json:"answers"`
				Strategy []CapacityProviderStrategyItem `json:"strategy"`
				Desired  int                            `json:"desired"`
			}
			if err := json.Unmarshal(c.Input, &in); err != nil {
				t.Fatal(err)
			}
			var got any
			switch in.Function {
			case "describeStopped":
				described++
				items, err := Describe(context.Background(), recordedPorts(t, in.Answers), in.Backends)
				if err != nil {
					t.Fatal(err)
				}
				got = items
			case "strategyCounts":
				counted++
				got = StrategyCounts(in.Strategy, in.Desired)
			default:
				t.Fatalf("unknown function %q", in.Function)
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			parity.Equal(t, c.Output, b)
		})
	}
	if described != 22 || counted != 7 {
		t.Errorf("described %d, counted %d", described, counted)
	}
}
