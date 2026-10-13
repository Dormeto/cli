package kubernetestest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/kubernetes"
)

const answers = `{
  "kubernetes.of": [
    {"input": {"cluster": "a"}, "output": {}},
    {"input": {"cluster": "denied"}, "error": {"name": "KubernetesError", "message": "Unauthorized", "status": 401, "reason": "Unauthorized"}}
  ],
  "kubernetes.get": [
    {"input": {"cluster": "a", "path": "/x"}, "output": {"kind": "X"}},
    {"input": {"cluster": "a", "path": "/gone"}, "error": {"name": "KubernetesError", "message": "/gone not found", "status": 404, "reason": "NotFound"}}
  ],
  "kubernetes.list": [
    {"input": {"cluster": "a", "path": "/pods"}, "output": [{"n": 1}]},
    {"input": {"cluster": "a", "path": "/pods", "query": {"labelSelector": "app=web"}}, "output": [{"n": 2}, {"n": 3}]}
  ],
  "eks.listClusters": [
    {"input": {}, "output": {"clusters": ["a"]}},
    {"input": {"nextToken": "t"}, "error": {"name": "AccessDeniedException", "message": "not authorized"}}
  ]
}`

func TestRecordedPortsAnswerFromTheRecordedAnswers(t *testing.T) {
	ctx := context.Background()
	p := RecordedPortsJSON(t, json.RawMessage(answers))
	c, err := p.Of(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.Get(ctx, "/x"); err != nil || string(got) != `{"kind": "X"}` && string(got) != `{"kind":"X"}` {
		t.Fatalf("%s %v", got, err)
	}
	if items, err := c.List(ctx, "/pods", nil); err != nil || len(items) != 1 {
		t.Fatalf("%v %v", items, err)
	}
	if items, err := c.List(ctx, "/pods", map[string]string{"labelSelector": "app=web"}); err != nil || len(items) != 2 {
		t.Fatalf("the query is part of the input: %v %v", items, err)
	}
	out, err := p.EKS.ListClusters(ctx, kubernetes.ListClustersInput{})
	if err != nil || len(out.Clusters) != 1 || out.Clusters[0] != "a" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestARecordedKubernetesErrorArrivesAsAKubernetesError(t *testing.T) {
	ctx := context.Background()
	p := RecordedPortsJSON(t, json.RawMessage(answers))
	_, err := p.Of(ctx, "denied")
	var e *kubernetes.Error
	if !errors.As(err, &e) || *e != (kubernetes.Error{Status: 401, Reason: "Unauthorized", Message: "Unauthorized"}) {
		t.Fatalf("%#v", err)
	}
	c, _ := p.Of(ctx, "a")
	_, err = c.Get(ctx, "/gone")
	if !errors.As(err, &e) || *e != (kubernetes.Error{Status: 404, Reason: "NotFound", Message: "/gone not found"}) {
		t.Fatalf("%#v", err)
	}
}

func TestAnotherRecordedErrorStaysItselfAndACallWithoutAnAnswerFails(t *testing.T) {
	ctx := context.Background()
	p := RecordedPortsJSON(t, json.RawMessage(answers))
	_, err := p.EKS.ListClusters(ctx, kubernetes.ListClustersInput{NextToken: "t"})
	var e *kubernetes.Error
	if err == nil || errors.As(err, &e) || err.Error() != "not authorized" {
		t.Fatalf("an AWS error is not a Kubernetes one: %#v", err)
	}
	if _, err := p.Of(ctx, "unknown"); err == nil || !strings.Contains(err.Error(), "no recorded answer") {
		t.Fatalf("%v", err)
	}
}

func TestWithInputWrapsEveryCallsInput(t *testing.T) {
	wrapped := `{"kubernetes.of": [{"input": {"account": "1", "region": "r", "input": {"cluster": "a"}}, "output": {}}]}`
	p := RecordedPortsJSON(t, json.RawMessage(wrapped), WithInput(func(in any) any {
		return map[string]any{"account": "1", "region": "r", "input": in}
	}))
	if _, err := p.Of(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
}
