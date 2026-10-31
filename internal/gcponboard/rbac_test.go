package gcponboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Dormeto/cli/internal/gcp/gcptest"
	"github.com/Dormeto/cli/internal/install"
)

const (
	kubeHost      = "gke-0123456789abcdef-1034397096264.europe-west1-b.gke.goog"
	clusterPath   = "/v1/projects/dormeto-dev/locations/europe-west1-b/clusters/shop-cluster"
	rolesPath     = "/apis/rbac.authorization.k8s.io/v1/namespaces/shop/roles"
	bindingsPath  = "/apis/rbac.authorization.k8s.io/v1/namespaces/shop/rolebindings"
	shopRole      = rolesPath + "/dormeto-shop"
	shopBinding   = bindingsPath + "/dormeto-shop"
	createRole    = "POST " + rolesPath
	createBinding = "POST " + bindingsPath
)

var (
	shopAccess = Request{Project: "dormeto-dev", Region: "europe-west1", GKECluster: "europe-west1-b/shop-cluster", Namespace: "shop", ID: "shop", Yes: true}
	shopEntry  = RecordRbac{Cluster: "europe-west1-b/shop-cluster", Namespace: "shop", Workload: "shop"}
)

// kubeStatus is a Kubernetes API's failure.
type kubeStatus struct {
	code           int
	reason, status string
}

// fakeKube is a Kubernetes API that knows the Roles and RoleBindings of its namespaces, as JSON objects by path.
type fakeKube struct {
	*httptest.Server
	mu      sync.Mutex
	objects map[string]map[string]any
	calls   []string
	seen    []*http.Request // the requests as the production code wrote them
	version int
	// deny answers a call with a failure, before anything else.
	deny func(call string) *kubeStatus
	// before runs before a write is done.
	before func(call string)
	// raw answers every call, in place of the API: the DNS endpoint's own refusal.
	raw http.HandlerFunc
}

func newFakeKube(t *testing.T) *fakeKube {
	t.Helper()
	k := &fakeKube{objects: map[string]map[string]any{}}
	k.Server = httptest.NewServer(http.HandlerFunc(k.serve))
	t.Cleanup(k.Close)
	return k
}

func (k *fakeKube) failure(w http.ResponseWriter, code int, reason, message string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "apiVersion": "v1", "metadata": map[string]any{}, "status": "Failure",
		"message": message, "reason": reason, "code": code})
}

func (k *fakeKube) serve(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	call := r.Method + " " + r.URL.Path
	k.calls = append(k.calls, call)
	if k.raw != nil {
		k.raw(w, r)
		return
	}
	if k.deny != nil {
		if s := k.deny(call); s != nil {
			k.failure(w, s.code, s.reason, s.status)
			return
		}
	}
	if !strings.HasPrefix(r.URL.Path, "/apis/rbac.authorization.k8s.io/v1/namespaces/") {
		k.failure(w, 404, "NotFound", "the fake knows namespaced RBAC only: "+call)
		return
	}
	if r.Method != http.MethodGet && k.before != nil {
		k.mu.Unlock()
		k.before(call)
		k.mu.Lock()
	}
	switch r.Method {
	case http.MethodGet:
		o, ok := k.objects[r.URL.Path]
		if !ok {
			k.failure(w, 404, "NotFound", r.URL.Path+" not found")
			return
		}
		_ = json.NewEncoder(w).Encode(o)
	case http.MethodPost:
		var o map[string]any
		_ = json.NewDecoder(r.Body).Decode(&o)
		meta, _ := o["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		path := r.URL.Path + "/" + name
		if _, there := k.objects[path]; there {
			k.failure(w, 409, "AlreadyExists", path+" already exists")
			return
		}
		k.store(path, o)
		_ = json.NewEncoder(w).Encode(o)
	case http.MethodPut:
		var o map[string]any
		_ = json.NewDecoder(r.Body).Decode(&o)
		old, there := k.objects[r.URL.Path]
		if !there {
			k.failure(w, 404, "NotFound", r.URL.Path+" not found")
			return
		}
		if o["metadata"].(map[string]any)["resourceVersion"] != old["metadata"].(map[string]any)["resourceVersion"] {
			k.failure(w, 409, "Conflict", "the object has been modified")
			return
		}
		k.store(r.URL.Path, o)
		_ = json.NewEncoder(w).Encode(o)
	case http.MethodDelete:
		if _, there := k.objects[r.URL.Path]; !there {
			k.failure(w, 404, "NotFound", r.URL.Path+" not found")
			return
		}
		delete(k.objects, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Success"})
	default:
		k.failure(w, 405, "MethodNotAllowed", call)
	}
}

// store keeps an object as the API server does, with a new resourceVersion.
func (k *fakeKube) store(path string, o map[string]any) {
	k.version++
	o["metadata"].(map[string]any)["resourceVersion"] = strconv.Itoa(k.version)
	k.objects[path] = o
}

// put places an object as if someone had made it.
func (k *fakeKube) put(t *testing.T, path, kind string, labels map[string]string, extra map[string]any) {
	t.Helper()
	name := path[strings.LastIndex(path, "/")+1:]
	o := map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": kind, "metadata": map[string]any{"name": name, "namespace": "shop", "labels": labels}}
	for key, v := range extra {
		o[key] = v
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	b, _ := json.Marshal(o)
	var stored map[string]any
	if err := json.Unmarshal(b, &stored); err != nil {
		t.Fatal(err)
	}
	k.store(path, stored)
}

func (k *fakeKube) object(path string) map[string]any {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.objects[path]
}

func (k *fakeKube) has(path string) bool { return k.object(path) != nil }

func (k *fakeKube) callList() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.calls)
}

// writes are the calls that change something, since mark.
func (k *fakeKube) writes(mark int) []string {
	var out []string
	for _, c := range k.callList()[mark:] {
		if !strings.HasPrefix(c, "GET ") {
			out = append(out, c)
		}
	}
	return out
}

// kubeTransport sends a cluster's DNS endpoint to the fake Kubernetes server, the rest to Google's fake.
type kubeTransport struct {
	next   http.RoundTripper
	target *url.URL
	k      *fakeKube
}

func (t kubeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(req.URL.Hostname(), ".gke.goog") {
		return t.next.RoundTrip(req)
	}
	t.k.mu.Lock()
	t.k.seen = append(t.k.seen, req.Clone(req.Context()))
	t.k.mu.Unlock()
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host = t.target.Scheme, t.target.Host
	out.Host = req.URL.Host
	return http.DefaultTransport.RoundTrip(out)
}

// rbacRig is an installed router, a cluster with a DNS endpoint open to Google Cloud, and its fake Kubernetes API.
type rbacRig struct {
	*workloadsRig
	k *fakeKube
	// cluster is clusters.get's answer; denied, its 403 instead; gone, its 404.
	cluster      map[string]any
	denied, gone bool
}

func newRbacRig(t *testing.T) *rbacRig {
	t.Helper()
	g := &rbacRig{workloadsRig: newWorkloadsRig(t), k: newFakeKube(t),
		cluster: dnsEndpoint(map[string]any{"endpoint": kubeHost, "allowExternalTraffic": true})}
	target, _ := url.Parse(g.k.URL)
	http2 := *g.d.Google.HTTP
	http2.Transport = kubeTransport{next: http2.Transport, target: target, k: g.k}
	g.d.Google.HTTP = &http2
	g.s.Route("container.googleapis.com", "/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path != clusterPath, g.gone:
			gcptest.Fail(w, 404, "NOT_FOUND", "cluster not found")
		case g.denied:
			gcptest.Fail(w, 403, "PERMISSION_DENIED", `Required "container.clusters.get" permission(s)`)
		default:
			gcptest.Reply(w, g.cluster)
		}
	})
	return g
}

func dnsEndpoint(config map[string]any) map[string]any {
	return map[string]any{"name": "shop-cluster", "controlPlaneEndpointsConfig": map[string]any{"dnsEndpointConfig": config}}
}

func (g *rbacRig) rbacOf(t *testing.T) []RecordRbac { t.Helper(); return g.record(t).Rbac }

var ourLabels = map[string]string{"app.kubernetes.io/managed-by": "dormeto"}

// jsonOf is v as JSON values, to compare with what the server holds.
func jsonOf(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The rules are pinned here, in this one form: spec §8's.
func TestRbacGrantWritesExactlyTheRoleAndTheRoleBinding(t *testing.T) {
	g := newRbacRig(t)
	code, out := g.call(t, WorkloadsAccessGrant, shopAccess)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	wantRules := []any{
		map[string]any{"apiGroups": []any{"apps"}, "resources": []any{"deployments", "statefulsets", "replicasets"}, "verbs": []any{"get", "list", "watch"}},
		map[string]any{"apiGroups": []any{"apps"}, "resources": []any{"deployments/scale", "statefulsets/scale"}, "verbs": []any{"get", "patch"}},
		map[string]any{"apiGroups": []any{""}, "resources": []any{"pods", "services", "events"}, "verbs": []any{"get", "list", "watch"}},
		map[string]any{"apiGroups": []any{"keda.sh"}, "resources": []any{"scaledobjects"}, "verbs": []any{"get", "list", "watch", "patch"}},
	}
	wantLabels := map[string]any{"app.kubernetes.io/managed-by": "dormeto"}
	role := g.k.object(shopRole)
	if role == nil {
		t.Fatalf("no Role:\n%s\n%v", out, g.k.callList())
	}
	if !reflect.DeepEqual(role["rules"], wantRules) {
		t.Errorf("rules: %#v", role["rules"])
	}
	if role["kind"] != "Role" || role["apiVersion"] != "rbac.authorization.k8s.io/v1" {
		t.Errorf("role: %#v", role)
	}
	meta := role["metadata"].(map[string]any)
	if meta["name"] != "dormeto-shop" || meta["namespace"] != "shop" || !reflect.DeepEqual(meta["labels"], wantLabels) {
		t.Errorf("role's metadata: %#v", meta)
	}
	binding := g.k.object(shopBinding)
	if binding == nil {
		t.Fatalf("no RoleBinding:\n%s", out)
	}
	if binding["kind"] != "RoleBinding" || binding["apiVersion"] != "rbac.authorization.k8s.io/v1" {
		t.Errorf("binding: %#v", binding)
	}
	meta = binding["metadata"].(map[string]any)
	if meta["name"] != "dormeto-shop" || meta["namespace"] != "shop" || !reflect.DeepEqual(meta["labels"], wantLabels) {
		t.Errorf("binding's metadata: %#v", meta)
	}
	wantSubjects := []any{map[string]any{"kind": "User", "name": routerSA, "apiGroup": "rbac.authorization.k8s.io"}}
	if !reflect.DeepEqual(binding["subjects"], wantSubjects) {
		t.Errorf("subjects: %#v", binding["subjects"])
	}
	wantRef := map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "dormeto-shop"}
	if !reflect.DeepEqual(binding["roleRef"], wantRef) {
		t.Errorf("roleRef: %#v", binding["roleRef"])
	}
}

func TestRbacTouchesNothingClusterScoped(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessShow, shopAccess); code != 0 {
		t.Fatalf("show: exit %d:\n%s", code, out)
	}
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("grant: exit %d:\n%s", code, out)
	}
	if code, out := g.call(t, WorkloadsAccessRevoke, shopAccess); code != 0 {
		t.Fatalf("revoke: exit %d:\n%s", code, out)
	}
	calls := g.k.callList()
	if len(calls) == 0 {
		t.Fatal("no call")
	}
	for _, c := range calls {
		_, path, _ := strings.Cut(c, " ")
		if !strings.HasPrefix(path, "/apis/rbac.authorization.k8s.io/v1/namespaces/shop/") || strings.Contains(strings.ToLower(path), "cluster") {
			t.Errorf("%s is not in the namespace", c)
		}
	}
}

func TestRbacCallsTheDNSEndpointByHTTPSWithTheOperatorsAccessToken(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.k.mu.Lock()
	defer g.k.mu.Unlock()
	if len(g.k.seen) == 0 {
		t.Fatal("no call")
	}
	for _, r := range g.k.seen {
		if r.URL.Scheme != "https" || r.URL.Host != kubeHost || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("%s %s with %q", r.Method, r.URL, r.Header.Get("Authorization"))
		}
	}
}

func TestRbacGrantPrintsTheObjectsBeforeAsking(t *testing.T) {
	g := newRbacRig(t)
	g.d.Terminal = true
	g.d.Prompter = install.NewPrompter(strings.NewReader("n\n"), g.out, true)
	r := shopAccess
	r.Yes = false
	before := len(g.s.Mutations())
	code, out := g.call(t, WorkloadsAccessGrant, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	asked := strings.Index(out, "[y/N]")
	if asked < 0 {
		t.Fatalf("not asked:\n%s", out)
	}
	for _, want := range []string{"+ Role dormeto-shop", "+ RoleBinding dormeto-shop", "apps: deployments, statefulsets, replicasets: get, list, watch",
		"apps: deployments/scale, statefulsets/scale: get, patch", `"": pods, services, events: get, list, watch`,
		"keda.sh: scaledobjects: get, list, watch, patch", "User " + routerSA, "app.kubernetes.io/managed-by: dormeto"} {
		if i := strings.Index(out, want); i < 0 || i > asked {
			t.Errorf("%q not printed before asking:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "Nothing was changed.") {
		t.Errorf("declined:\n%s", out)
	}
	if w := g.k.writes(0); len(w) != 0 {
		t.Errorf("declined, written: %v", w)
	}
	if m := g.s.Mutations(); len(m) != before {
		t.Errorf("declined, Google changed: %v", m[before:])
	}
}

func TestRbacGrantWithoutATerminalNeedsYes(t *testing.T) {
	g := newRbacRig(t)
	r := shopAccess
	r.Yes = false
	code, out := g.call(t, WorkloadsAccessGrant, r)
	if code != 2 || !strings.Contains(out, "--yes") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if w := g.k.writes(0); len(w) != 0 {
		t.Errorf("written: %v", w)
	}
	if !strings.Contains(out, "+ Role dormeto-shop") {
		t.Errorf("the objects are not printed:\n%s", out)
	}
}

func TestRbacGrantWithJSONNeedsYesBeforeAnyCall(t *testing.T) {
	g := newRbacRig(t)
	r := shopAccess
	r.Yes, r.JSON = false, true
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsAccessGrant, r)
	if code != 2 || !strings.Contains(out, "--yes") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if c := g.s.Calls()[mark:]; len(c) != 0 || len(g.k.callList()) != 0 {
		t.Errorf("calls: %v %v", c, g.k.callList())
	}
}

func TestRbacGrantAppliedTwiceChangesNothing(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.k.writes(0); !slices.Equal(got, []string{createRole, createBinding}) {
		t.Fatalf("writes: %v", got)
	}
	role, binding := g.k.object(shopRole), g.k.object(shopBinding)
	mark, google := len(g.k.callList()), len(g.s.Mutations())
	code, out := g.call(t, WorkloadsAccessGrant, shopAccess)
	if code != 0 || !strings.Contains(out, "up to date") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if w := g.k.writes(mark); len(w) != 0 {
		t.Errorf("written again: %v", w)
	}
	if m := g.s.Mutations(); len(m) != google {
		t.Errorf("the record is written again: %v", m[google:])
	}
	if !reflect.DeepEqual(g.k.object(shopRole), role) || !reflect.DeepEqual(g.k.object(shopBinding), binding) {
		t.Error("the objects changed")
	}
}

func TestRbacGrantWritesOnlyWhatDiffersAndKeepsTheObjectsMetadata(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.k.put(t, shopRole, "Role", map[string]string{"app.kubernetes.io/managed-by": "dormeto", "team": "web"}, map[string]any{
		"rules": []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"pods"}, "verbs": []any{"get"}}}})
	version := g.k.object(shopRole)["metadata"].(map[string]any)["resourceVersion"]
	mark := len(g.k.callList())
	code, out := g.call(t, WorkloadsAccessGrant, shopAccess)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "~ Role dormeto-shop") || !strings.Contains(out, "= RoleBinding dormeto-shop") {
		t.Errorf("plan:\n%s", out)
	}
	if got := g.k.writes(mark); !slices.Equal(got, []string{"PUT " + shopRole}) {
		t.Fatalf("writes: %v", got)
	}
	role := g.k.object(shopRole)
	if rules := role["rules"].([]any); len(rules) != 4 {
		t.Errorf("rules: %v", rules)
	}
	meta := role["metadata"].(map[string]any)
	if meta["labels"].(map[string]any)["team"] != "web" || meta["resourceVersion"] == version {
		t.Errorf("metadata: %v", meta)
	}
}

func TestRbacAnObjectOfThatNameNotOursIsRefusedAndNothingIsChanged(t *testing.T) {
	cases := map[string]func(k *fakeKube, t *testing.T){
		"a Role without the label": func(k *fakeKube, t *testing.T) { k.put(t, shopRole, "Role", nil, nil) },
		"a Role of someone else's": func(k *fakeKube, t *testing.T) {
			k.put(t, shopRole, "Role", map[string]string{"app.kubernetes.io/managed-by": "helm"}, nil)
		},
		"a RoleBinding without the label": func(k *fakeKube, t *testing.T) {
			k.put(t, shopBinding, "RoleBinding", map[string]string{"team": "web"}, nil)
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			g := newRbacRig(t)
			plant(g.k, t)
			google := len(g.s.Mutations())
			code, out := g.call(t, WorkloadsAccessGrant, shopAccess)
			if code != 1 || !strings.Contains(out, "dormeto-shop") || !strings.Contains(out, "app.kubernetes.io/managed-by: dormeto") || !strings.Contains(out, "Nothing was changed.") {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if w := g.k.writes(0); len(w) != 0 {
				t.Errorf("written: %v", w)
			}
			if m := g.s.Mutations(); len(m) != google {
				t.Errorf("the record is written: %v", m[google:])
			}
		})
	}
}

func TestRbacGrantRecordsBeforeTheApply(t *testing.T) {
	g := newRbacRig(t)
	var seen []RecordRbac
	first := true
	g.k.before = func(string) {
		if first {
			first = false
			seen = g.rbacOf(t)
		}
	}
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !slices.Equal(seen, []RecordRbac{shopEntry}) {
		t.Errorf("at the first write the record lists %v", seen)
	}
	if got := g.rbacOf(t); !slices.Equal(got, []RecordRbac{shopEntry}) {
		t.Errorf("record: %v", got)
	}
	raw := g.s.Field(recordDB, "install/record", "data")
	if !strings.Contains(raw, `"rbac":[{"cluster":"europe-west1-b/shop-cluster","namespace":"shop","workload":"shop"}]`) {
		t.Errorf("record: %s", raw)
	}
}

func TestRbacGrantRecordsWhatItFindsOursWithoutWritingInTheCluster(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	rec := g.record(t)
	rec.Rbac = nil
	b, _ := json.Marshal(rec)
	g.s.Lock()
	g.s.Documents[recordDB+"/install/record"]["data"] = map[string]any{"stringValue": string(b)}
	g.s.Unlock()
	mark := len(g.k.callList())
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if w := g.k.writes(mark); len(w) != 0 {
		t.Errorf("written: %v", w)
	}
	if got := g.rbacOf(t); !slices.Equal(got, []RecordRbac{shopEntry}) {
		t.Errorf("record: %v", got)
	}
}

func TestRbacGrantWithoutARouterIsRefused(t *testing.T) {
	g := newRbacRig(t)
	r := shopAccess
	r.Region = "europe-north1"
	code, out := g.call(t, WorkloadsAccessGrant, r)
	if code != 1 || !strings.Contains(out, "dmtr-europe-north1") || !strings.Contains(out, "dormeto gcp install") || !strings.Contains(out, "Nothing was changed.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if c := g.k.callList(); len(c) != 0 {
		t.Errorf("calls: %v", c)
	}
}

func TestRbacInstallKeepsTheRecordedAccess(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if code, out := g.run(t, withImage); code != 0 {
		t.Fatalf("install: exit %d:\n%s", code, out)
	}
	if got := g.rbacOf(t); !slices.Equal(got, []RecordRbac{shopEntry}) {
		t.Errorf("record: %v", got)
	}
}

func TestRbacRevokeRemovesOursThenTheRecordsEntry(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.k.put(t, rolesPath+"/other", "Role", nil, nil)
	g.k.put(t, rolesPath+"/dormeto-web", "Role", ourLabels, nil)
	var seen []RecordRbac
	first := true
	g.k.before = func(string) {
		if first {
			first = false
			seen = g.rbacOf(t)
		}
	}
	mark := len(g.k.callList())
	code, out := g.call(t, WorkloadsAccessRevoke, shopAccess)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// The binding first: the router loses its rights before the Role goes.
	if got := g.k.writes(mark); !slices.Equal(got, []string{"DELETE " + shopBinding, "DELETE " + shopRole}) {
		t.Fatalf("writes: %v", got)
	}
	if !slices.Equal(seen, []RecordRbac{shopEntry}) {
		t.Errorf("at the removal the record lists %v", seen)
	}
	if got := g.rbacOf(t); len(got) != 0 {
		t.Errorf("record: %v", got)
	}
	if !g.k.has(rolesPath+"/other") || !g.k.has(rolesPath+"/dormeto-web") {
		t.Error("another object is gone")
	}
	for _, want := range []string{"RoleBinding dormeto-shop", "Role dormeto-shop"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q not said:\n%s", want, out)
		}
	}
}

func TestRbacRevokeRemovesOnlyOurs(t *testing.T) {
	g := newRbacRig(t)
	g.k.put(t, shopRole, "Role", nil, nil)
	g.k.put(t, shopBinding, "RoleBinding", ourLabels, nil)
	code, out := g.call(t, WorkloadsAccessRevoke, shopAccess)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.k.writes(0); !slices.Equal(got, []string{"DELETE " + shopBinding}) {
		t.Fatalf("writes: %v", got)
	}
	if !g.k.has(shopRole) {
		t.Error("someone else's Role is gone")
	}
	if !strings.Contains(out, "not Dormeto's") {
		t.Errorf("not said:\n%s", out)
	}
}

func TestRbacRevokeOfNothingIsNoChangeAndClearsTheRecord(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.k.mu.Lock()
	delete(g.k.objects, shopRole)
	delete(g.k.objects, shopBinding)
	g.k.mu.Unlock()
	mark := len(g.k.callList())
	r := shopAccess
	r.Yes = false // nothing to remove: nothing to ask
	code, out := g.call(t, WorkloadsAccessRevoke, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if w := g.k.writes(mark); len(w) != 0 {
		t.Errorf("written: %v", w)
	}
	if got := g.rbacOf(t); len(got) != 0 {
		t.Errorf("record: %v", got)
	}
}

func TestRbacRevokeWithoutTerminalNeedsYes(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	mark := len(g.k.callList())
	r := shopAccess
	r.Yes = false
	code, out := g.call(t, WorkloadsAccessRevoke, r)
	if code != 2 || !strings.Contains(out, "- RoleBinding dormeto-shop") || !strings.Contains(out, "- Role dormeto-shop") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if w := g.k.writes(mark); len(w) != 0 {
		t.Errorf("written: %v", w)
	}
	if got := g.rbacOf(t); len(got) != 1 {
		t.Errorf("record: %v", got)
	}
}

// What uninstall leaves in a cluster is removed afterwards: the record is gone by then.
func TestRbacRevokeWorksAfterTheUninstall(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if code, out := g.uninstall(t, uninstallEurope); code != 0 {
		t.Fatalf("uninstall: exit %d:\n%s", code, out)
	}
	code, out := g.call(t, WorkloadsAccessRevoke, shopAccess)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if g.k.has(shopRole) || g.k.has(shopBinding) {
		t.Errorf("left:\n%s", out)
	}
}

func TestRbacRevokeOfAClusterThatIsGoneClearsTheRecord(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.gone = true
	code, out := g.call(t, WorkloadsAccessRevoke, shopAccess)
	if code != 0 || !strings.Contains(out, "europe-west1-b/shop-cluster") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.rbacOf(t); len(got) != 0 {
		t.Errorf("record: %v", got)
	}
}

func TestRbacWithoutAClusterFound(t *testing.T) {
	for name, f := range map[string]func(context.Context, Deps, Request) int{"show": WorkloadsAccessShow, "grant": WorkloadsAccessGrant} {
		t.Run(name, func(t *testing.T) {
			g := newRbacRig(t)
			g.gone = true
			code, out := g.call(t, f, shopAccess)
			if code != 1 || !strings.Contains(out, "europe-west1-b/shop-cluster") || !strings.Contains(out, "dormeto-dev") {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if got := g.rbacOf(t); len(got) != 0 {
				t.Errorf("record: %v", got)
			}
		})
	}
}

func TestRbacUninstallListsWhatItLeavesInTheClusterWithTheCommand(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	mark := len(g.k.callList())
	r := uninstallEurope
	r.Configuration = "dormeto-dev"
	code, out := g.uninstall(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if c := g.k.callList()[mark:]; len(c) != 0 {
		t.Errorf("the uninstall reached the cluster: %v", c)
	}
	command := "dormeto gcp workloads access revoke --project dormeto-dev --region europe-west1 --gke-cluster europe-west1-b/shop-cluster --namespace shop --workload shop --gcloud-configuration dormeto-dev"
	// In the plan, and said at the end, once the record that lists it is gone.
	if strings.Count(out, "left in the cluster") < 2 || strings.Count(out, command) < 2 {
		t.Errorf("not listed twice:\n%s", out)
	}
	if i := strings.LastIndex(out, "left in the cluster"); i < strings.LastIndex(out, "removed database") {
		t.Errorf("not said at the end:\n%s", out)
	}
	if !g.k.has(shopRole) || !g.k.has(shopBinding) {
		t.Error("removed from the cluster")
	}
}

func TestRbacUninstallWithoutAccessSaysNothingOfAClusterWhenThereIsNone(t *testing.T) {
	g := newRbacRig(t)
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 || strings.Contains(out, "left in the cluster") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestRbacAClusterWithoutItsDNSEndpointSaysWhatToTurnOn(t *testing.T) {
	cases := map[string]map[string]any{
		"no configuration":  {"name": "shop-cluster"},
		"no endpoint":       dnsEndpoint(map[string]any{"allowExternalTraffic": true}),
		"external traffic":  dnsEndpoint(map[string]any{"endpoint": kubeHost}),
		"traffic said no":   dnsEndpoint(map[string]any{"endpoint": kubeHost, "allowExternalTraffic": false}),
		"no config at all":  {"controlPlaneEndpointsConfig": map[string]any{}},
		"no traffic config": {"controlPlaneEndpointsConfig": map[string]any{"dnsEndpointConfig": map[string]any{}}},
	}
	for name, cluster := range cases {
		for verb, f := range map[string]func(context.Context, Deps, Request) int{"show": WorkloadsAccessShow, "grant": WorkloadsAccessGrant, "revoke": WorkloadsAccessRevoke} {
			t.Run(name+"/"+verb, func(t *testing.T) {
				g := newRbacRig(t)
				g.cluster = cluster
				r := shopAccess
				r.Configuration = "dormeto-dev"
				code, out := g.call(t, f, r)
				if code != 1 {
					t.Fatalf("exit %d:\n%s", code, out)
				}
				for _, want := range []string{"DNS endpoint", "europe-west1-b/shop-cluster",
					"gcloud container clusters update shop-cluster --location europe-west1-b --project dormeto-dev --enable-dns-access --configuration dormeto-dev", "Nothing was changed."} {
					if !strings.Contains(out, want) {
						t.Errorf("%q not said:\n%s", want, out)
					}
				}
				if c := g.k.callList(); len(c) != 0 {
					t.Errorf("calls: %v", c)
				}
				if got := g.rbacOf(t); len(got) != 0 {
					t.Errorf("record: %v", got)
				}
			})
		}
	}
}

func TestRbacARefusalNamesWhatIsMissing(t *testing.T) {
	forbidden := func(what string) string {
		return `roles.rbac.authorization.k8s.io "dormeto-shop" is forbidden: User "me@example.com" cannot ` + what + ` in the namespace "shop"`
	}
	cases := map[string]struct {
		call, message string
		want          []string
	}{
		"reading the Role":        {"GET " + shopRole, forbidden(`get resource "roles"`), []string{"container.roles.get"}},
		"reading the RoleBinding": {"GET " + shopBinding, forbidden(`get resource "rolebindings"`), []string{"container.roleBindings.get"}},
		"creating the Role":       {createRole, forbidden(`create resource "roles"`), []string{"container.roles.create"}},
		"creating the binding":    {createBinding, forbidden(`create resource "rolebindings"`), []string{"container.roleBindings.create"}},
		"the escalation rule": {createRole, `roles.rbac.authorization.k8s.io "dormeto-shop" is forbidden: user "me@example.com" (groups=["system:authenticated"]) is attempting to grant RBAC permissions not currently held:` + "\n{APIGroups:[\"apps\"], Resources:[\"deployments\"], Verbs:[\"get\"]}",
			[]string{"container.roles.escalate", "container.roles.bind", "not currently held"}},
		"the binding's escalation": {createBinding, `rolebindings.rbac.authorization.k8s.io "dormeto-shop" is forbidden: user "me@example.com" (groups=["system:authenticated"]) is attempting to grant RBAC permissions not currently held:`,
			[]string{"container.roles.escalate", "container.roles.bind"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			g := newRbacRig(t)
			g.k.deny = func(call string) *kubeStatus {
				if call == c.call {
					return &kubeStatus{403, "Forbidden", c.message}
				}
				return nil
			}
			code, out := g.call(t, WorkloadsAccessGrant, shopAccess)
			if code != 1 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			for _, want := range c.want {
				if !strings.Contains(out, want) {
					t.Errorf("%q not said:\n%s", want, out)
				}
			}
		})
	}
}

func TestRbacARefusedRemovalNamesWhatIsMissingAndKeepsTheRecord(t *testing.T) {
	cases := map[string]struct{ call, want string }{
		"the binding": {"DELETE " + shopBinding, "container.roleBindings.delete"},
		"the Role":    {"DELETE " + shopRole, "container.roles.delete"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			g := newRbacRig(t)
			if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			g.k.deny = func(call string) *kubeStatus {
				if call == c.call {
					return &kubeStatus{403, "Forbidden", "forbidden"}
				}
				return nil
			}
			code, out := g.call(t, WorkloadsAccessRevoke, shopAccess)
			if code != 1 || !strings.Contains(out, c.want) {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if got := g.rbacOf(t); !slices.Equal(got, []RecordRbac{shopEntry}) {
				t.Errorf("record: %v", got)
			}
		})
	}
}

func TestRbacAWriteRefusedFirstLeavesNoRecordEntry(t *testing.T) {
	g := newRbacRig(t)
	g.k.deny = func(call string) *kubeStatus {
		if call == createRole {
			return &kubeStatus{403, "Forbidden", "forbidden"}
		}
		return nil
	}
	code, out := g.call(t, WorkloadsAccessGrant, shopAccess)
	if code != 1 || !strings.Contains(out, "Nothing was changed.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.rbacOf(t); len(got) != 0 {
		t.Errorf("record: %v", got)
	}
}

func TestRbacAWriteRefusedAfterTheFirstSaysWhatIsDoneAndKeepsTheRecord(t *testing.T) {
	g := newRbacRig(t)
	g.k.deny = func(call string) *kubeStatus {
		if call == createBinding {
			return &kubeStatus{403, "Forbidden", "forbidden"}
		}
		return nil
	}
	code, out := g.call(t, WorkloadsAccessGrant, shopAccess)
	if code != 1 || strings.Contains(out, "Nothing was changed.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"created Role dormeto-shop", "container.roleBindings.create", "access revoke"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q not said:\n%s", want, out)
		}
	}
	if got := g.rbacOf(t); !slices.Equal(got, []RecordRbac{shopEntry}) {
		t.Errorf("record: %v", got)
	}
	// Run again, it finishes.
	g.k.deny = nil
	mark := len(g.k.callList())
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.k.writes(mark); !slices.Equal(got, []string{createBinding}) {
		t.Errorf("writes: %v", got)
	}
}

func TestRbacANamespaceThatDoesNotExistIsSaidInKubernetesWords(t *testing.T) {
	g := newRbacRig(t)
	g.k.deny = func(call string) *kubeStatus {
		if call == createRole {
			return &kubeStatus{404, "NotFound", `namespaces "shop" not found`}
		}
		return nil
	}
	code, out := g.call(t, WorkloadsAccessGrant, shopAccess)
	if code != 1 || !strings.Contains(out, `namespaces "shop" not found`) || !strings.Contains(out, "Nothing was changed.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.rbacOf(t); len(got) != 0 {
		t.Errorf("record: %v", got)
	}
}

// The DNS endpoint itself refuses a caller without container.clusters.connect, in Google's words and not as a Kubernetes Status.
func TestRbacTheEndpointsOwnRefusalNamesTheConnectPermission(t *testing.T) {
	g := newRbacRig(t)
	g.k.raw = func(w http.ResponseWriter, _ *http.Request) {
		gcptest.Fail(w, 403, "PERMISSION_DENIED", "Permission denied")
	}
	code, out := g.call(t, WorkloadsAccessShow, shopAccess)
	if code != 1 || !strings.Contains(out, "container.clusters.connect") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestRbacClustersGetRefusedNamesThePermission(t *testing.T) {
	g := newRbacRig(t)
	g.denied = true
	code, out := g.call(t, WorkloadsAccessShow, shopAccess)
	if code != 1 || !strings.Contains(out, "container.clusters.get") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestRbacShowChangesNothingAndPrintsBothSides(t *testing.T) {
	g := newRbacRig(t)
	g.k.put(t, shopRole, "Role", ourLabels, map[string]any{
		"rules": []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"secrets"}, "verbs": []any{"get"}}}})
	mark := len(g.s.Mutations())
	code, out := g.call(t, WorkloadsAccessShow, shopAccess)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{`"": secrets: get`, "RoleBinding dormeto-shop: none", "~ Role dormeto-shop", "+ RoleBinding dormeto-shop",
		"keda.sh: scaledobjects: get, list, watch, patch", "User " + routerSA} {
		if !strings.Contains(out, want) {
			t.Errorf("%q not said:\n%s", want, out)
		}
	}
	if w := g.k.writes(0); len(w) != 0 {
		t.Errorf("written: %v", w)
	}
	if m := g.s.Mutations(); len(m) != mark {
		t.Errorf("changed: %v", m[mark:])
	}
}

func TestRbacShowSaysAnObjectNotOursWouldBeRefused(t *testing.T) {
	g := newRbacRig(t)
	g.k.put(t, shopRole, "Role", nil, nil)
	code, out := g.call(t, WorkloadsAccessShow, shopAccess)
	if code != 0 || !strings.Contains(out, "not Dormeto's") || !strings.Contains(out, "grant refuses") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestRbacShowWithJSON(t *testing.T) {
	g := newRbacRig(t)
	g.k.put(t, shopRole, "Role", nil, nil)
	r := shopAccess
	r.JSON, r.Yes = true, false
	code, out := g.call(t, WorkloadsAccessShow, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var got struct {
		Cluster        string `json:"cluster"`
		Namespace      string `json:"namespace"`
		Workload       string `json:"workload"`
		ServiceAccount string `json:"serviceAccount"`
		Role           struct {
			Action  string `json:"action"`
			Current *struct {
				Kind string `json:"kind"`
			} `json:"current"`
			Wanted struct {
				Rules []any `json:"rules"`
			} `json:"wanted"`
		} `json:"role"`
		RoleBinding struct {
			Action  string `json:"action"`
			Current any    `json:"current"`
		} `json:"roleBinding"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if got.Cluster != shopEntry.Cluster || got.Namespace != "shop" || got.Workload != "shop" || got.ServiceAccount != routerSA {
		t.Errorf("%+v", got)
	}
	if got.Role.Action != "refused" || got.Role.Current == nil || got.Role.Current.Kind != "Role" || len(got.Role.Wanted.Rules) != 4 {
		t.Errorf("role: %+v", got.Role)
	}
	if got.RoleBinding.Action != "create" || got.RoleBinding.Current != nil {
		t.Errorf("binding: %+v", got.RoleBinding)
	}
}

func TestRbacGrantAndRevokeWithJSON(t *testing.T) {
	g := newRbacRig(t)
	r := shopAccess
	r.JSON = true
	code, out := g.call(t, WorkloadsAccessGrant, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var granted struct {
		Changed []string `json:"changed"`
	}
	if err := json.Unmarshal([]byte(out), &granted); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if !slices.Equal(granted.Changed, []string{"created Role dormeto-shop", "created RoleBinding dormeto-shop"}) {
		t.Errorf("%+v", granted)
	}
	g.k.put(t, shopRole, "Role", nil, nil)
	code, out = g.call(t, WorkloadsAccessRevoke, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var revoked struct {
		Removed []string `json:"removed"`
		Left    []string `json:"left"`
	}
	if err := json.Unmarshal([]byte(out), &revoked); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if !slices.Equal(revoked.Removed, []string{"RoleBinding dormeto-shop"}) || !slices.Equal(revoked.Left, []string{"Role dormeto-shop"}) {
		t.Errorf("%+v", revoked)
	}
}

func TestRbacBadFlagsExit2BeforeAnyCall(t *testing.T) {
	cases := map[string]func(r *Request){
		"no cluster":       func(r *Request) { r.GKECluster = "" },
		"no location":      func(r *Request) { r.GKECluster = "shop-cluster" },
		"empty location":   func(r *Request) { r.GKECluster = "/shop-cluster" },
		"empty name":       func(r *Request) { r.GKECluster = "europe-west1-b/" },
		"too many parts":   func(r *Request) { r.GKECluster = "europe-west1-b/a/b" },
		"a bad location":   func(r *Request) { r.GKECluster = "Europe/a" },
		"a bad name":       func(r *Request) { r.GKECluster = "europe-west1-b/A_b" },
		"no namespace":     func(r *Request) { r.Namespace = "" },
		"a bad namespace":  func(r *Request) { r.Namespace = "Shop/x" },
		"no workload":      func(r *Request) { r.ID = "" },
		"a bad workload":   func(r *Request) { r.ID = "Shop" },
		"a bad region":     func(r *Request) { r.Region = "europe" },
		"no project":       func(r *Request) { r.Project = "" },
		"a long namespace": func(r *Request) { r.Namespace = strings.Repeat("a", 64) },
	}
	for name, change := range cases {
		for verb, f := range map[string]func(context.Context, Deps, Request) int{"show": WorkloadsAccessShow, "grant": WorkloadsAccessGrant, "revoke": WorkloadsAccessRevoke} {
			t.Run(fmt.Sprintf("%s/%s", name, verb), func(t *testing.T) {
				g := newRbacRig(t)
				r := shopAccess
				change(&r)
				mark := len(g.s.Calls())
				code, out := g.call(t, f, r)
				if code != 2 || out == "" {
					t.Fatalf("exit %d:\n%s", code, out)
				}
				if c := g.s.Calls()[mark:]; len(c) != 0 || len(g.k.callList()) != 0 {
					t.Errorf("calls: %v %v", c, g.k.callList())
				}
			})
		}
	}
}

func TestRbacPlanRefusedSaysSoAndApplyWritesNothing(t *testing.T) {
	g := newRbacRig(t)
	g.k.put(t, shopRole, "Role", nil, nil)
	x := newRun(context.Background(), g.d, shopAccess)
	rb := Rbac{Cluster: shopEntry.Cluster, Namespace: "shop", Workload: "shop", ServiceAccount: routerSA}
	plan, err := x.PlanRbac(rb)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Refused() == nil {
		t.Fatal("not refused")
	}
	if _, err := x.ApplyRbac(plan); err == nil {
		t.Fatal("applied")
	}
	if w := g.k.writes(0); len(w) != 0 {
		t.Errorf("written: %v", w)
	}
}

func TestRbacALostAnswerKeepsTheRecordsEntryAndSaysHowToGoOn(t *testing.T) {
	g := newRbacRig(t)
	g.k.deny = func(call string) *kubeStatus {
		if call == createRole {
			return &kubeStatus{500, "InternalError", "etcd is away"}
		}
		return nil
	}
	code, out := g.call(t, WorkloadsAccessGrant, shopAccess)
	if code != 1 || strings.Contains(out, "Nothing was changed.") || !strings.Contains(out, "etcd is away") || !strings.Contains(out, "access revoke") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.rbacOf(t); !slices.Equal(got, []RecordRbac{shopEntry}) {
		t.Errorf("record: %v", got)
	}
	if got := g.k.writes(0); !slices.Equal(got, []string{createRole}) {
		t.Errorf("a write is sent again: %v", got)
	}
}

func TestRbacInterruptedAWriteMayHaveBeenMade(t *testing.T) {
	g := newRbacRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.k.before = func(string) { cancel() }
	code := WorkloadsAccessGrant(ctx, g.d, shopAccess)
	if code != 130 || !strings.Contains(g.out.String(), "Interrupted.") {
		t.Fatalf("exit %d:\n%s", code, g.out)
	}
	if got := g.rbacOf(t); !slices.Equal(got, []RecordRbac{shopEntry}) {
		t.Errorf("record: %v", got)
	}
}

func TestRbacInterruptedARemovalKeepsTheRecordsEntry(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.k.before = func(string) { cancel() }
	if code := WorkloadsAccessRevoke(ctx, g.d, shopAccess); code != 130 {
		t.Fatalf("exit %d:\n%s", code, g.out)
	}
	if got := g.rbacOf(t); !slices.Equal(got, []RecordRbac{shopEntry}) {
		t.Errorf("record: %v", got)
	}
}

// A rule someone narrowed by hand differs, whatever else it says.
func TestRbacARuleNarrowedByHandIsWrittenBack(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	role := g.k.object(shopRole)
	rules := slices.Clone(role["rules"].([]any))
	first := map[string]any{"resourceNames": []any{"only-this"}}
	for key, v := range rules[0].(map[string]any) {
		first[key] = v
	}
	rules[0] = first
	g.k.put(t, shopRole, "Role", ourLabels, map[string]any{"rules": rules})
	mark := len(g.k.callList())
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.k.writes(mark); !slices.Equal(got, []string{"PUT " + shopRole}) {
		t.Errorf("writes: %v", got)
	}
}

func TestRbacARecordThatCannotBeReadChangesNothing(t *testing.T) {
	g := newRbacRig(t)
	g.s.Route("firestore.googleapis.com", "/v1/projects/dormeto-dev/databases/dmtr-europe-west1", func(w http.ResponseWriter, _ *http.Request) {
		gcptest.Fail(w, 403, "PERMISSION_DENIED", "datastore.databases.get denied")
	})
	for name, f := range map[string]func(context.Context, Deps, Request) int{"grant": WorkloadsAccessGrant, "revoke": WorkloadsAccessRevoke} {
		code, out := g.call(t, f, shopAccess)
		if code != 1 || !strings.Contains(out, "The install record is not read") || !strings.Contains(out, "datastore.databases.get denied") || !strings.Contains(out, "Nothing was changed.") {
			t.Errorf("%s: exit %d:\n%s", name, code, out)
		}
	}
	if c := g.k.callList(); len(c) != 0 {
		t.Errorf("calls: %v", c)
	}
}

// I1: what Google names is checked before the operator's token goes there.
func TestRbacAnEndpointThatIsNotUnderGkeGoogGetsNothing(t *testing.T) {
	for _, endpoint := range []string{"evil.example/#", "user@evil.example", "evil.example", "gke.goog", "evil.example/.gke.goog", "evil.example#.gke.goog",
		"evil.example?.gke.goog", "x.gke.goog:8443", "x.gke.goog.evil.example", "X.GKE.GOOG", "a..gke.goog", "a b.gke.goog"} {
		for verb, f := range map[string]func(context.Context, Deps, Request) int{"show": WorkloadsAccessShow, "grant": WorkloadsAccessGrant, "revoke": WorkloadsAccessRevoke} {
			t.Run(endpoint+"/"+verb, func(t *testing.T) {
				g := newRbacRig(t)
				g.cluster = dnsEndpoint(map[string]any{"endpoint": endpoint, "allowExternalTraffic": true})
				var sent []string
				transport := g.d.Google.HTTP.Transport
				g.d.Google.HTTP.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
					if strings.HasSuffix(r.URL.Hostname(), ".googleapis.com") || strings.HasSuffix(r.URL.Hostname(), ".run.app") {
						return transport.RoundTrip(r)
					}
					sent = append(sent, r.URL.String())
					return nil, http.ErrUseLastResponse
				})
				code, out := g.call(t, f, shopAccess)
				if code != 1 || !strings.Contains(out, endpoint) || !strings.Contains(out, "gke.goog") || !strings.Contains(out, "Nothing was changed.") {
					t.Fatalf("exit %d:\n%s", code, out)
				}
				if len(sent) != 0 || len(g.k.callList()) != 0 {
					t.Errorf("sent: %v %v", sent, g.k.callList())
				}
				if got := g.rbacOf(t); len(got) != 0 {
					t.Errorf("record: %v", got)
				}
			})
		}
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// I2: a redirect is not followed, for a read or for a write: the token would go with it.
func TestRbacACallIsNotRedirected(t *testing.T) {
	cases := map[string]struct {
		calls  string
		status int
	}{
		"a read":  {"GET " + shopRole, http.StatusFound},
		"a write": {createRole, http.StatusTemporaryRedirect},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			g := newRbacRig(t)
			var seen []string
			other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
			}))
			defer other.Close()
			target, _ := url.Parse(other.URL)
			transport := g.d.Google.HTTP.Transport
			g.d.Google.HTTP.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Hostname() != "other.gke.goog" {
					return transport.RoundTrip(r)
				}
				out := r.Clone(r.Context())
				out.URL.Scheme, out.URL.Host = target.Scheme, target.Host
				return http.DefaultTransport.RoundTrip(out)
			})
			g.k.raw = func(w http.ResponseWriter, r *http.Request) {
				if r.Method+" "+r.URL.Path != c.calls {
					if r.Method == http.MethodGet {
						w.WriteHeader(404)
						_, _ = w.Write([]byte(`{"kind":"Status","message":"not found","reason":"NotFound","code":404}`))
					}
					return
				}
				w.Header().Set("Location", "http://other.gke.goog"+r.URL.Path)
				w.WriteHeader(c.status)
			}
			code, out := g.call(t, WorkloadsAccessGrant, shopAccess)
			if code != 1 || !strings.Contains(out, "redirect") {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if len(seen) != 0 {
				t.Errorf("the second server saw: %v", seen)
			}
		})
	}
}

func TestRbacGooglesOwnCallsKeepTheirRedirects(t *testing.T) {
	g := newRbacRig(t)
	if g.d.Google.HTTP.CheckRedirect != nil {
		t.Fatal("the rig's client has a CheckRedirect")
	}
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if g.d.Google.HTTP.CheckRedirect != nil {
		t.Error("the shared client was changed")
	}
}

// M1: an object of ours was there before: the entry stays when the first write is refused.
func TestRbacAFirstWriteRefusedKeepsTheEntryWhenOursWasThere(t *testing.T) {
	cases := map[string]struct {
		plant func(k *fakeKube, t *testing.T)
		call  string
	}{
		"the Role": {func(k *fakeKube, t *testing.T) {
			k.put(t, shopRole, "Role", ourLabels, map[string]any{"rules": []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"pods"}, "verbs": []any{"get"}}}})
		}, "PUT " + shopRole},
		"the RoleBinding": {func(k *fakeKube, t *testing.T) { k.put(t, shopBinding, "RoleBinding", ourLabels, nil) }, createRole},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			g := newRbacRig(t)
			c.plant(g.k, t)
			g.k.deny = func(call string) *kubeStatus {
				if call == c.call {
					return &kubeStatus{403, "Forbidden", "forbidden"}
				}
				return nil
			}
			code, out := g.call(t, WorkloadsAccessGrant, shopAccess)
			if code != 1 || strings.Contains(out, "Nothing was changed.") || !strings.Contains(out, "access revoke") {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if got := g.rbacOf(t); !slices.Equal(got, []RecordRbac{shopEntry}) {
				t.Errorf("record: %v", got)
			}
		})
	}
}

// A first write refused on an entry listed earlier changes nothing, the record included.
func TestRbacAFirstWriteRefusedKeepsAnEntryListedEarlier(t *testing.T) {
	g := newRbacRig(t)
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.k.mu.Lock()
	delete(g.k.objects, shopRole)
	delete(g.k.objects, shopBinding)
	g.k.mu.Unlock()
	g.k.deny = func(call string) *kubeStatus {
		if call == createRole {
			return &kubeStatus{403, "Forbidden", "forbidden"}
		}
		return nil
	}
	code, out := g.call(t, WorkloadsAccessGrant, shopAccess)
	if code != 1 || !strings.Contains(out, "Nothing was changed.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.rbacOf(t); !slices.Equal(got, []RecordRbac{shopEntry}) {
		t.Errorf("record: %v", got)
	}
}

// M2: with --json the ✓ lines are not printed: the message says what was done.
func TestRbacAPartialFailureUnderJSONSaysWhatWasDone(t *testing.T) {
	refuse := func(g *rbacRig, call string, status *kubeStatus) {
		g.k.deny = func(c string) *kubeStatus {
			if c == call {
				return status
			}
			return nil
		}
	}
	r := shopAccess
	r.JSON = true
	for name, status := range map[string]*kubeStatus{"a refusal": {403, "Forbidden", "forbidden"}, "a 500": {500, "InternalError", "etcd is away"}} {
		t.Run(name+" of a grant", func(t *testing.T) {
			g := newRbacRig(t)
			refuse(g, createBinding, status)
			code, out := g.call(t, WorkloadsAccessGrant, r)
			if code != 1 || !strings.Contains(out, "created Role dormeto-shop") || !strings.Contains(out, "access revoke") {
				t.Fatalf("exit %d:\n%s", code, out)
			}
		})
		t.Run(name+" of a revoke", func(t *testing.T) {
			g := newRbacRig(t)
			if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			refuse(g, "DELETE "+shopRole, status)
			code, out := g.call(t, WorkloadsAccessRevoke, r)
			if code != 1 || !strings.Contains(out, "removed RoleBinding dormeto-shop") || !strings.Contains(out, "Run the same command again") {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if got := g.rbacOf(t); !slices.Equal(got, []RecordRbac{shopEntry}) {
				t.Errorf("record: %v", got)
			}
		})
	}
}

// M3: an update keeps what it does not own.
func TestRbacAnUpdateKeepsOwnerReferencesAndFinalizers(t *testing.T) {
	g := newRbacRig(t)
	g.k.put(t, shopRole, "Role", ourLabels, map[string]any{"rules": []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"pods"}, "verbs": []any{"get"}}}})
	owners := []any{map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "name": "owner", "uid": "u-1", "controller": true}}
	g.k.mu.Lock()
	meta := g.k.objects[shopRole]["metadata"].(map[string]any)
	meta["ownerReferences"], meta["finalizers"], meta["annotations"] = owners, []any{"example.com/hold"}, map[string]any{"note": "kept"}
	g.k.mu.Unlock()
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	got := g.k.object(shopRole)["metadata"].(map[string]any)
	if !reflect.DeepEqual(got["ownerReferences"], owners) || !reflect.DeepEqual(got["finalizers"], []any{"example.com/hold"}) || !reflect.DeepEqual(got["annotations"], map[string]any{"note": "kept"}) {
		t.Errorf("metadata: %v", got)
	}
}

// M4: what an update replaces is printed before asking.
func TestRbacGrantPrintsWhatAnUpdateReplacesBeforeAsking(t *testing.T) {
	g := newRbacRig(t)
	g.k.put(t, shopRole, "Role", ourLabels, map[string]any{"rules": []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"secrets"}, "resourceNames": []any{"token"}, "verbs": []any{"get"}}}})
	g.k.put(t, shopBinding, "RoleBinding", ourLabels, map[string]any{"subjects": []any{map[string]any{"kind": "User", "name": "someone@example.com", "apiGroup": "rbac.authorization.k8s.io"}},
		"roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "dormeto-shop"}})
	g.d.Terminal = true
	g.d.Prompter = install.NewPrompter(strings.NewReader("n\n"), g.out, true)
	r := shopAccess
	r.Yes = false
	code, out := g.call(t, WorkloadsAccessGrant, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	asked := strings.Index(out, "[y/N]")
	for _, want := range []string{`"": secrets (named token): get`, "subject: User someone@example.com", "keda.sh: scaledobjects: get, list, watch, patch", "subject: User " + routerSA} {
		if i := strings.Index(out, want); i < 0 || i > asked {
			t.Errorf("%q not printed before asking:\n%s", want, out)
		}
	}
}

// M4: a binding also held by someone else is named before it goes.
func TestRbacRevokeNamesAnotherSubjectBeforeAsking(t *testing.T) {
	g := newRbacRig(t)
	g.k.put(t, shopBinding, "RoleBinding", ourLabels, map[string]any{"subjects": []any{
		map[string]any{"kind": "User", "name": routerSA, "apiGroup": "rbac.authorization.k8s.io"},
		map[string]any{"kind": "Group", "name": "team@example.com", "apiGroup": "rbac.authorization.k8s.io"}},
		"roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "dormeto-shop"}})
	g.d.Terminal = true
	g.d.Prompter = install.NewPrompter(strings.NewReader("n\n"), g.out, true)
	r := shopAccess
	r.Yes = false
	code, out := g.call(t, WorkloadsAccessRevoke, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if i := strings.Index(out, "Group team@example.com"); i < 0 || i > strings.Index(out, "[y/N]") {
		t.Errorf("not named before asking:\n%s", out)
	}
	if strings.Contains(out, "User "+routerSA) {
		t.Errorf("the router's own is named:\n%s", out)
	}
	if !g.k.has(shopBinding) {
		t.Error("declined, removed")
	}
}

// M6: a 5xx says which call.
func TestRbacA5xxSaysWhichCall(t *testing.T) {
	g := newRbacRig(t)
	g.k.deny = func(call string) *kubeStatus {
		if call == "GET "+shopRole {
			return &kubeStatus{500, "InternalError", "etcd is away"}
		}
		return nil
	}
	code, out := g.call(t, WorkloadsAccessShow, shopAccess)
	if code != 1 || !strings.Contains(out, "get Role dormeto-shop in namespace shop: etcd is away") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
