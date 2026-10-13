// Package kubernetestest holds the fakes of the Kubernetes ports for tests: the
// recorded answers of the shared cases, as the ports of internal/kubernetes.
package kubernetestest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Dormeto/cli/internal/kubernetes"
	"github.com/Dormeto/cli/internal/parity"
)

// Option adjusts how recorded calls are looked up.
type Option func(*recorded)

// WithInput maps a call's recorded input before it is looked up: in the
// estimate topic every input is {"account", "region", "input": <the shape>}.
func WithInput(wrap func(input any) any) Option { return func(r *recorded) { r.wrap = wrap } }

type recorded struct {
	t       testing.TB
	answers *parity.Answers
	wrap    func(any) any
}

// RecordedPorts answers the Kubernetes calls of a case from its recorded
// answers: eks.listClusters, eks.describeCluster, kubernetes.of, kubernetes.get
// and kubernetes.list. A recorded Kubernetes error arrives as a
// *kubernetes.Error (status, reason, message); a call with no recorded answer
// fails with the parity error. Reader is left empty.
func RecordedPorts(t testing.TB, answers *parity.Answers, opts ...Option) kubernetes.Ports {
	r := &recorded{t: t, answers: answers, wrap: func(in any) any { return in }}
	for _, o := range opts {
		o(r)
	}
	return kubernetes.Ports{EKS: eks{r}, Of: r.of}
}

// RecordedPortsJSON is RecordedPorts over a case's input.answers.
func RecordedPortsJSON(t testing.TB, answers json.RawMessage, opts ...Option) kubernetes.Ports {
	t.Helper()
	return RecordedPorts(t, parity.Recorded(t, answers), opts...)
}

// Client is the recorded client of a cluster.
func Client(t testing.TB, answers *parity.Answers, cluster string, opts ...Option) kubernetes.Client {
	r := &recorded{t: t, answers: answers, wrap: func(in any) any { return in }}
	for _, o := range opts {
		o(r)
	}
	return client{r, cluster}
}

// failure maps a recorded Kubernetes error to the *kubernetes.Error the client
// returns; any other error is returned as it is.
func failure(err error) error {
	var rec *parity.RecordedError
	if !errors.As(err, &rec) {
		return err
	}
	if rec.Name != "KubernetesError" && rec.Status == 0 {
		return err
	}
	reason := rec.Reason
	if reason == "" {
		reason = rec.Code
	}
	return &kubernetes.Error{Status: rec.Status, Reason: reason, Message: rec.Message}
}

func (r *recorded) call(port, method string, input, out any) error {
	return failure(r.answers.Call(port, method, r.wrap(input), out))
}

func (r *recorded) of(_ context.Context, cluster string) (kubernetes.Client, error) {
	var ignored json.RawMessage
	if err := r.call("kubernetes", "of", map[string]any{"cluster": cluster}, &ignored); err != nil {
		return nil, err
	}
	return client{r, cluster}, nil
}

type client struct {
	r       *recorded
	cluster string
}

func (c client) Get(_ context.Context, path string) (json.RawMessage, error) {
	var out json.RawMessage
	if err := c.r.call("kubernetes", "get", map[string]any{"cluster": c.cluster, "path": path}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c client) List(_ context.Context, path string, query map[string]string) ([]json.RawMessage, error) {
	in := map[string]any{"cluster": c.cluster, "path": path}
	if len(query) > 0 {
		in["query"] = query
	}
	var out []json.RawMessage
	if err := c.r.call("kubernetes", "list", in, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type eks struct{ r *recorded }

func (e eks) ListClusters(_ context.Context, in kubernetes.ListClustersInput) (kubernetes.ListClustersOutput, error) {
	var out kubernetes.ListClustersOutput
	err := e.r.call("eks", "listClusters", in, &out)
	return out, err
}

func (e eks) DescribeCluster(_ context.Context, in kubernetes.DescribeClusterInput) (kubernetes.DescribeClusterOutput, error) {
	var out kubernetes.DescribeClusterOutput
	err := e.r.call("eks", "describeCluster", in, &out)
	return out, err
}
