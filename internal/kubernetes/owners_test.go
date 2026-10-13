package kubernetes

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// fakeAPI is a cluster's API held in memory: objects by collection path.
type fakeAPI struct {
	objects map[string][]json.RawMessage
	err     map[string]error // path -> error
	calls   []string
	queries []map[string]string
}

func (f *fakeAPI) Get(_ context.Context, path string) (json.RawMessage, error) {
	f.calls = append(f.calls, "GET "+path)
	if err := f.err[path]; err != nil {
		return nil, err
	}
	at := strings.LastIndex(path, "/")
	for _, o := range f.objects[path[:at]] {
		var m struct{ Metadata struct{ Name string } }
		_ = json.Unmarshal(o, &m)
		if m.Metadata.Name == path[at+1:] {
			return o, nil
		}
	}
	return nil, &Error{Status: 404, Reason: "NotFound", Message: path + " not found"}
}

func (f *fakeAPI) List(_ context.Context, path string, query map[string]string) ([]json.RawMessage, error) {
	f.calls = append(f.calls, "LIST "+path)
	f.queries = append(f.queries, query)
	if err := f.err[path]; err != nil {
		return nil, err
	}
	items, ok := f.objects[path]
	if !ok {
		return nil, &Error{Status: 404, Reason: "NotFound", Message: path + " not found"}
	}
	return items, nil
}

func obj(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

type m = map[string]any

func owned(name string, ref m, extra m) json.RawMessage {
	meta := m{"name": name, "namespace": "demo"}
	if ref != nil {
		meta["ownerReferences"] = []m{ref}
	}
	for k, v := range extra {
		meta[k] = v
	}
	return obj(m{"metadata": meta, "status": m{"phase": "Running"}})
}

func podOf(name, kind, owner string, phase string) json.RawMessage {
	return obj(m{
		"metadata": m{"name": name, "namespace": "demo", "ownerReferences": []m{{"kind": kind, "name": owner, "controller": true}}},
		"status":   m{"phase": phase, "podIP": "10.0.0.1"},
	})
}

func TestLabelSelectorWritesAnOwnersSelectorAsTheAPITakesIt(t *testing.T) {
	var s Selector
	if err := json.Unmarshal([]byte(`{"matchLabels": {"tier": "front", "app": "web"}}`), &s); err != nil {
		t.Fatal(err)
	}
	got, err := LabelSelector(s)
	if err != nil || got != "tier=front,app=web" {
		t.Fatalf("matchLabels keep the API's order: %q %v", got, err)
	}
	full := Selector{
		MatchLabels: Labels{{"app", "web"}},
		MatchExpressions: []Expression{
			{Key: "env", Operator: "In", Values: []string{"a", "b"}}, {Key: "canary", Operator: "NotIn", Values: []string{"true"}},
			{Key: "ready", Operator: "Exists"}, {Key: "legacy", Operator: "DoesNotExist"},
		},
	}
	got, err = LabelSelector(full)
	if err != nil || got != "app=web,env in (a,b),canary notin (true),ready,!legacy" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := LabelSelector(Selector{}); err != nil || got != "" {
		t.Fatalf("no selector: %q %v", got, err)
	}
	if _, err := LabelSelector(Selector{MatchExpressions: []Expression{{Key: "k", Operator: "Gt"}}}); err == nil {
		t.Fatal("an unknown operator is an error")
	}
}

func TestOwnerPathByKind(t *testing.T) {
	if got := OwnerPath(Ref{"demo", Owner{"Deployment", "web"}}); got != "/apis/apps/v1/namespaces/demo/deployments/web" {
		t.Error(got)
	}
	if got := OwnerPath(Ref{"demo", Owner{"StatefulSet", "cache"}}); got != "/apis/apps/v1/namespaces/demo/statefulsets/cache" {
		t.Error(got)
	}
}

func names(pods []Pod) []string {
	var out []string
	for _, p := range pods {
		out = append(out, p.Metadata.Name)
	}
	sort.Strings(out)
	return out
}

func web() *fakeAPI {
	return &fakeAPI{objects: map[string][]json.RawMessage{
		"/apis/apps/v1/namespaces/demo/deployments": {obj(m{"kind": "Deployment", "metadata": m{"name": "web", "namespace": "demo"}, "spec": m{"replicas": 2, "selector": m{"matchLabels": m{"app": "web"}}}})},
		"/apis/apps/v1/namespaces/demo/replicasets": {
			owned("web-5d9c", m{"kind": "Deployment", "name": "web", "controller": true}, nil),
			owned("web-old", m{"kind": "Deployment", "name": "web", "controller": true}, nil),
			owned("canary-1", m{"kind": "Deployment", "name": "web-canary", "controller": true}, nil),
		},
		"/api/v1/namespaces/demo/pods": {
			podOf("web-5d9c-a", "ReplicaSet", "web-5d9c", "Running"),
			podOf("web-5d9c-b", "ReplicaSet", "web-5d9c", "Pending"),
			podOf("web-old-c", "ReplicaSet", "web-old", "Succeeded"),
			podOf("web-old-d", "ReplicaSet", "web-old", "Failed"),
			podOf("canary-1-x", "ReplicaSet", "canary-1", "Running"),
			obj(m{"metadata": m{"name": "lonely", "namespace": "demo"}, "status": m{"phase": "Running"}}),
		},
	}}
}

func TestOwnerPodsOfADeploymentGoThroughItsReplicaSetsWithoutFinishedOnesOrAnotherOwnersPods(t *testing.T) {
	api := web()
	pods, err := OwnerPods(context.Background(), api, Ref{"demo", Owner{"Deployment", "web"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(pods); !reflect.DeepEqual(got, []string{"web-5d9c-a", "web-5d9c-b"}) {
		t.Fatalf("pods %v (a canary selecting the same labels is foreign, finished pods are gone for good)", got)
	}
	for i, q := range api.queries {
		if !reflect.DeepEqual(q, map[string]string{"labelSelector": "app=web"}) {
			t.Errorf("list %d query %v", i, q)
		}
	}
}

func TestOwnerPodsOfAStatefulSetAreThoseItControls(t *testing.T) {
	api := &fakeAPI{objects: map[string][]json.RawMessage{
		"/apis/apps/v1/namespaces/demo/statefulsets": {obj(m{"metadata": m{"name": "cache", "namespace": "demo"}, "spec": m{"replicas": 1, "selector": m{"matchLabels": m{"app": "cache"}}}})},
		"/api/v1/namespaces/demo/pods": {
			podOf("cache-0", "StatefulSet", "cache", "Running"),
			podOf("other-0", "StatefulSet", "other", "Running"),
		},
	}}
	pods, err := OwnerPods(context.Background(), api, Ref{"demo", Owner{"StatefulSet", "cache"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(pods); !reflect.DeepEqual(got, []string{"cache-0"}) {
		t.Fatalf("pods %v", got)
	}
	for _, c := range api.calls {
		if strings.Contains(c, "replicasets") {
			t.Errorf("a StatefulSet's pods are not read through ReplicaSets: %s", c)
		}
	}
}

func TestOwnerPodsOfAnOwnerThatDoesNotExistAreNone(t *testing.T) {
	api := web()
	pods, err := OwnerPods(context.Background(), api, Ref{"demo", Owner{"Deployment", "gone"}})
	if err != nil || len(pods) != 0 {
		t.Fatalf("%v %v", pods, err)
	}
	if w, err := OwnerOf(context.Background(), api, Ref{"demo", Owner{"Deployment", "gone"}}); err != nil || w != nil {
		t.Fatalf("OwnerOf: %v %v", w, err)
	}
	// GetWorkload keeps the API's 404, as k8sItems's get does.
	if _, err := GetWorkload(context.Background(), api, Ref{"demo", Owner{"Deployment", "gone"}}); err == nil || err.(*Error).Status != 404 {
		t.Fatalf("GetWorkload: %v", err)
	}
}

func TestAnErrorOtherThan404FailsTheCaller(t *testing.T) {
	api := web()
	refused := &Error{Status: 403, Reason: "Forbidden", Message: "forbidden"}
	api.err = map[string]error{"/apis/apps/v1/namespaces/demo/deployments/web": refused}
	_, err := OwnerPods(context.Background(), api, Ref{"demo", Owner{"Deployment", "web"}})
	if err != refused {
		t.Fatalf("%v", err)
	}
	api = web()
	api.err = map[string]error{"/api/v1/namespaces/demo/pods": refused}
	if _, err := OwnerPods(context.Background(), api, Ref{"demo", Owner{"Deployment", "web"}}); err != refused {
		t.Fatalf("%v", err)
	}
}
