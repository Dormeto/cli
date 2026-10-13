package kubernetes_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Dormeto/cli/internal/kubernetes"
	"github.com/Dormeto/cli/internal/kubernetes/kubernetestest"
	"github.com/Dormeto/cli/internal/parity"
)

// The shared cases of discovery's own Kubernetes functions (scan topic):
// clustersOf and kubernetesBackend over the recorded answers.

func TestSharedCasesClustersOfAndKubernetesBackend(t *testing.T) {
	ran := 0
	for _, c := range parity.Load(t, "scan").Cases {
		var in struct {
			Function string          `json:"function"`
			Reader   string          `json:"reader"`
			Groups   []group         `json:"groups"`
			Answers  json.RawMessage `json:"answers"`
		}
		if err := json.Unmarshal(c.Input, &in); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if in.Function != "clustersOf" && in.Function != "kubernetesBackend" {
			continue
		}
		ran++
		t.Run(c.Name, func(t *testing.T) {
			ports := kubernetestest.RecordedPortsJSON(t, in.Answers)
			ports.Reader = in.Reader
			ctx := context.Background()
			clusters, err := kubernetes.ClustersOf(ctx, ports)
			if err != nil {
				t.Fatal(err)
			}
			var got any = clusters
			if in.Function == "kubernetesBackend" {
				results := make([]kubernetes.Result, len(in.Groups))
				for i, g := range in.Groups {
					results[i] = kubernetes.Backend(ctx, clusters, kubernetes.Group(g))
				}
				got = results
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			parity.Equal(t, c.Output, b)
		})
	}
	if ran != 9 {
		t.Fatalf("ran %d cases, the corpus has 9 (1 clustersOf, 8 kubernetesBackend)", ran)
	}
}

// group is a shared case's target group.
type group kubernetes.Group

func (g *group) UnmarshalJSON(b []byte) error {
	return json.Unmarshal(b, (*kubernetes.Group)(g))
}
