package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/smithy-go"
)

// Ports manager packages/discovery/test/kubernetes.test.ts (the "Kubernetes
// discovery" cases; the scanRegion ones belong to the scan).

func pod(name, ip string, owner m, spec m) json.RawMessage {
	meta := m{"name": name, "namespace": "demo", "labels": m{"app": strings.Split(name, "-")[0]}}
	if owner != nil {
		meta["ownerReferences"] = []m{{"kind": owner["kind"], "name": owner["name"], "controller": true}}
	}
	sp := m{"nodeName": "ip-10-0-1-1.eu-west-1.compute.internal", "schedulerName": "default-scheduler"}
	for k, v := range spec {
		sp[k] = v
	}
	return obj(m{"metadata": meta, "spec": sp, "status": m{"phase": "Running", "podIP": ip}})
}

func rs(name, deployment string) json.RawMessage {
	return owned(name, m{"kind": "Deployment", "name": deployment, "controller": true}, nil)
}

func ownerObj(kind, name string, meta m) json.RawMessage {
	md := m{"name": name, "namespace": "demo"}
	for k, v := range meta {
		md[k] = v
	}
	return obj(m{"kind": kind, "metadata": md, "spec": m{"replicas": 2, "selector": m{"matchLabels": m{"app": name}}}})
}

func ingress(name, service string, annotations m) json.RawMessage {
	rule := func(host, svc string) m {
		return m{"host": host, "http": m{"paths": []m{{"path": "/", "pathType": "Prefix", "backend": m{"service": m{"name": svc, "port": m{"number": 80}}}}}}}
	}
	return obj(m{"metadata": m{"name": name, "namespace": "demo", "annotations": annotations},
		"spec": m{"rules": []m{rule("docs.example.com", "docs"), rule(service+".example.com", service)}}})
}

func service(name string, selector m) json.RawMessage {
	return obj(m{"metadata": m{"name": name, "namespace": "demo"}, "spec": m{"selector": selector, "ports": []m{{"port": 80}}}})
}

// demo is the demo cluster: web (a Deployment behind a grouped Ingress, an HPA,
// Argo CD), cache (a StatefulSet behind a lone Ingress, on Fargate), and pods
// Dormeto must not take for workloads.
func demo(karpenter bool, extra map[string][]json.RawMessage) *fakeAPI {
	objects := map[string][]json.RawMessage{
		"/api/v1/pods": {
			pod("web-5d9c-a1", "10.0.1.1", m{"kind": "ReplicaSet", "name": "web-5d9c"}, nil), pod("web-5d9c-a2", "10.0.1.2", m{"kind": "ReplicaSet", "name": "web-5d9c"}, nil),
			pod("cache-0", "10.0.2.1", m{"kind": "StatefulSet", "name": "cache"}, m{"schedulerName": "fargate-scheduler", "nodeName": "fargate-ip-10-0-2-1.eu-west-1.compute.internal"}),
			pod("agent-x1", "10.0.3.1", m{"kind": "DaemonSet", "name": "agent"}, nil), pod("migrate-q", "10.0.3.2", m{"kind": "Job", "name": "migrate"}, nil), pod("lonely", "10.0.3.3", nil, nil),
			pod("api-1a-b", "10.0.4.1", m{"kind": "ReplicaSet", "name": "api-1a"}, nil), pod("api2-2b-c", "10.0.4.2", m{"kind": "ReplicaSet", "name": "api2-2b"}, nil),
		},
		"/apis/apps/v1/namespaces/demo/replicasets": {rs("web-5d9c", "web"), rs("api-1a", "api"), rs("api2-2b", "api2")},
		"/apis/apps/v1/namespaces/demo/deployments": {
			ownerObj("Deployment", "web", m{"labels": m{"app.kubernetes.io/instance": "web"}, "annotations": m{"argocd.argoproj.io/tracking-id": "web:apps/Deployment:demo/web"}}),
			ownerObj("Deployment", "api", nil), ownerObj("Deployment", "api2", nil),
		},
		"/apis/apps/v1/namespaces/demo/statefulsets": {ownerObj("StatefulSet", "cache", m{"labels": m{"kustomize.toolkit.fluxcd.io/name": "apps"}})},
		"/api/v1/namespaces/demo/services":           {service("web", m{"app": "web"}), service("cache", m{"app": "cache"})},
		"/apis/networking.k8s.io/v1/namespaces/demo/ingresses": {
			ingress("web", "web", m{"alb.ingress.kubernetes.io/group.name": "demo-group", "alb.ingress.kubernetes.io/group.order": "10"}),
			ingress("cache", "cache", nil),
		},
		"/apis/autoscaling/v2/namespaces/demo/horizontalpodautoscalers": {obj(m{"metadata": m{"name": "web"}, "spec": m{"scaleTargetRef": m{"kind": "Deployment", "name": "web"}}})},
		"/apis/keda.sh/v1alpha1/namespaces/demo/scaledobjects":          {},
	}
	if karpenter {
		objects["/apis/karpenter.sh"] = []json.RawMessage{obj(m{"metadata": m{"name": "v1"}})}
	}
	for k, v := range extra {
		objects[k] = v
	}
	return &fakeAPI{objects: objects}
}

func eksCluster(vpc m) *EKSCluster {
	c := &EKSCluster{}
	b := obj(m{"name": "c", "endpoint": "https://c.example", "version": "1.36", "certificateAuthority": m{"data": ""}, "resourcesVpcConfig": vpc, "accessConfig": m{"authenticationMode": "API"}})
	if err := json.Unmarshal(b, c); err != nil {
		panic(err)
	}
	return c
}

var publicVPC = m{"endpointPublicAccess": true, "publicAccessCidrs": []string{"0.0.0.0/0"}, "vpcId": "vpc-1"}

type region struct {
	names    []string
	clusters map[string]*EKSCluster
	clients  map[string]Client
}

func (r region) ListClusters(context.Context, ListClustersInput) (ListClustersOutput, error) {
	return ListClustersOutput{Clusters: r.names}, nil
}

func (r region) DescribeCluster(_ context.Context, in DescribeClusterInput) (DescribeClusterOutput, error) {
	return DescribeClusterOutput{Cluster: r.clusters[in.Name]}, nil
}

func (r region) ports() Ports {
	return Ports{EKS: r, Account: "111122223333", Region: "eu-west-1", Of: func(_ context.Context, name string) (Client, error) { return r.clients[name], nil }}
}

func one(client Client) region {
	return region{names: []string{"eks-demo"}, clusters: map[string]*EKSCluster{"eks-demo": eksCluster(publicVPC)}, clients: map[string]Client{"eks-demo": client}}
}

const arn = "arn:aws:elasticloadbalancing:eu-west-1:111122223333:targetgroup"

func group(name, typ string, targets []string, tags map[string]string) Group {
	return Group{ARN: arn + "/" + name + "/1", Type: typ, VPCID: "vpc-1", Tags: tags, Targets: targets}
}

func controllerTags(resource string) map[string]string {
	return map[string]string{"elbv2.k8s.aws/cluster": "eks-demo", "ingress.k8s.aws/stack": "demo-group", "ingress.k8s.aws/resource": resource}
}

func mustClusters(t *testing.T, r region) []Cluster {
	t.Helper()
	c, err := ClustersOf(context.Background(), r.ports())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	privateReason = "the cluster's endpoint is private: Dormeto reaches it through a proxy function in its VPC, set up with the router of 111122223333 eu-west-1: install it"
	limitedReason = "the cluster's public endpoint is limited to some addresses and its private access is off: turn on private access, and Dormeto reaches it from inside its VPC"
)

func TestPrivateReasonWithoutAnAccountOrRegionNamesItsAccountAndRegion(t *testing.T) {
	r := region{
		names:    []string{"private"},
		clusters: map[string]*EKSCluster{"private": eksCluster(m{"endpointPublicAccess": false, "endpointPrivateAccess": true, "vpcId": "vpc-1"})},
		clients:  map[string]Client{"private": &fakeAPI{}},
	}
	for _, where := range [][2]string{{"", "eu-west-1"}, {"111122223333", ""}, {"", ""}} {
		p := r.ports()
		p.Account, p.Region = where[0], where[1]
		got, err := ClustersOf(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Replace(privateReason, "111122223333 eu-west-1", "its account and region", 1)
		if got[0].Reason != want {
			t.Errorf("%q: got %q", where, got[0].Reason)
		}
	}
}

func TestReadsTheClustersOfTheRegionThoseItCannotReachWithTheReason(t *testing.T) {
	denied := &Error{Status: 401, Reason: "Unauthorized", Message: "Unauthorized"}
	r := region{
		names: []string{"eks-demo", "private", "limited-on", "office", "denied"},
		clusters: map[string]*EKSCluster{
			"eks-demo":   eksCluster(publicVPC),
			"private":    eksCluster(m{"endpointPublicAccess": false, "endpointPrivateAccess": true, "vpcId": "vpc-1"}),
			"limited-on": eksCluster(m{"endpointPublicAccess": true, "endpointPrivateAccess": true, "publicAccessCidrs": []string{"198.51.100.0/24"}, "vpcId": "vpc-1"}),
			"office":     eksCluster(m{"endpointPublicAccess": true, "publicAccessCidrs": []string{"198.51.100.0/24"}, "vpcId": "vpc-1"}),
			"denied":     eksCluster(publicVPC),
		},
		clients: map[string]Client{"eks-demo": demo(true, nil), "private": &fakeAPI{}, "limited-on": &fakeAPI{}, "office": &fakeAPI{}, "denied": &fakeAPI{err: map[string]error{"/api/v1/pods": denied}}},
	}
	got := mustClusters(t, r)
	var summary []string
	for _, c := range got {
		summary = append(summary, fmt.Sprintf("%s|%s", c.Name, c.Reason))
	}
	want := []string{
		"eks-demo|", "private|" + privateReason, "limited-on|" + privateReason, "office|" + limitedReason,
		"denied|the cluster does not let DmtDiscoveryRole read it (an access entry with AmazonEKSViewPolicy): Unauthorized",
	}
	if strings.Join(summary, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(summary, "\n"))
	}
	if got[0].VPCID != "vpc-1" || got[0].Autoscaler != "karpenter" || len(got[0].Pods) != 8 {
		t.Errorf("eks-demo: %+v", got[0])
	}
	// Who reads is named in the reason of a refused cluster.
	p := r.ports()
	p.Reader = "the profile"
	again, err := ClustersOf(context.Background(), p)
	if err != nil || !strings.Contains(again[4].Reason, "does not let the profile read it") {
		t.Fatalf("%v %q", err, again[4].Reason)
	}
}

func TestAClusterThatFailsForAnotherReasonCannotBeRead(t *testing.T) {
	r := one(&fakeAPI{err: map[string]error{"/api/v1/pods": errors.New("boom")}})
	got := mustClusters(t, r)
	if got[0].Reason != "the cluster cannot be read: boom" {
		t.Fatalf("%q", got[0].Reason)
	}
	// Of failing is the cluster's failure too.
	p := one(demo(false, nil)).ports()
	p.Of = func(context.Context, string) (Client, error) { return nil, &Error{Status: 403, Message: "no token"} }
	c, err := ClustersOf(context.Background(), p)
	if err != nil || !strings.HasPrefix(c[0].Reason, "the cluster does not let DmtDiscoveryRole read it") {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestAnAWSErrorReadingAClusterIsSaidByItsMessageNotTheSDKsText(t *testing.T) {
	p := one(demo(false, nil)).ports()
	p.Of = func(context.Context, string) (Client, error) {
		return nil, &smithy.OperationError{ServiceID: "EKS", OperationName: "DescribeCluster", Err: &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "No cluster found for name: eks-demo."}}
	}
	c, err := ClustersOf(context.Background(), p)
	if err != nil || c[0].Reason != "the cluster cannot be read: No cluster found for name: eks-demo." {
		t.Fatalf("%v %q", err, c[0].Reason)
	}
}

func TestEKSFailuresFailClustersOf(t *testing.T) {
	boom := errors.New("not authorized to perform: eks:ListClusters")
	_, err := ClustersOf(context.Background(), Ports{EKS: failingEKS{boom}})
	if !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
}

type failingEKS struct{ err error }

func (f failingEKS) ListClusters(context.Context, ListClustersInput) (ListClustersOutput, error) {
	return ListClustersOutput{}, f.err
}
func (f failingEKS) DescribeCluster(context.Context, DescribeClusterInput) (DescribeClusterOutput, error) {
	return DescribeClusterOutput{}, f.err
}

func TestAClusterListFollowsThePagesOfEKS(t *testing.T) {
	eks := &pagedEKS{}
	c, err := ClustersOf(context.Background(), Ports{EKS: eks, Of: func(context.Context, string) (Client, error) { return demo(false, nil), nil }})
	if err != nil || len(c) != 2 || c[0].Name != "a" || c[1].Name != "b" {
		t.Fatalf("%v %+v", err, c)
	}
}

type pagedEKS struct{}

func (pagedEKS) ListClusters(_ context.Context, in ListClustersInput) (ListClustersOutput, error) {
	if in.NextToken == "p2" {
		return ListClustersOutput{Clusters: []string{"b"}}, nil
	}
	return ListClustersOutput{Clusters: []string{"a"}, NextToken: "p2"}, nil
}
func (pagedEKS) DescribeCluster(_ context.Context, in DescribeClusterInput) (DescribeClusterOutput, error) {
	return DescribeClusterOutput{Cluster: eksCluster(publicVPC)}, nil
}

func TestATargetGroupOfAnIngressInAGroupItsDeploymentTheSwitchThroughDormetosIngressHPAArgoCDTheAutoscaler(t *testing.T) {
	clusters := mustClusters(t, one(demo(true, nil)))
	got := Backend(context.Background(), clusters, group("k8s-demo-web", "ip", []string{"10.0.1.1", "10.0.1.2"}, controllerTags("demo/web-web:80")))
	want := `{"kind":"k8s","id":"eks-demo/demo/Deployment/web","kubernetes":{"cluster":"eks-demo","namespace":"demo","owner":{"kind":"Deployment","name":"web"},"switch":"ingress","ingress":{"name":"web","ruleIndex":1,"groupOrder":9,"service":{"name":"web","port":{"number":80}}},"gitops":"argocd","hpa":true,"keda":false,"capacity":"nodes","autoscaler":"karpenter"}}`
	if asJSON(t, got) != want {
		t.Fatalf("got  %s\nwant %s", asJSON(t, got), want)
	}
}

func TestATargetGroupOfALoneIngressItsStatefulSetOnFargateTheSwitchByPatchingItFlux(t *testing.T) {
	clusters := mustClusters(t, one(demo(false, nil)))
	got := Backend(context.Background(), clusters, group("k8s-demo-cache", "instance", []string{"i-0abc"}, controllerTags("demo/cache-cache:80")))
	if got.Backend == nil || got.Backend.ID != "eks-demo/demo/StatefulSet/cache" {
		t.Fatalf("%+v", got)
	}
	want := `{"cluster":"eks-demo","namespace":"demo","owner":{"kind":"StatefulSet","name":"cache"},"switch":"patched-ingress","ingress":{"name":"cache","ruleIndex":1,"service":{"name":"cache","port":{"number":80}}},"gitops":"flux","hpa":false,"keda":false,"capacity":"fargate","autoscaler":null}`
	if asJSON(t, got.Backend.Kubernetes) != want {
		t.Fatalf("got  %s\nwant %s", asJSON(t, got.Backend.Kubernetes), want)
	}
}

func TestKEDANoneInAClusterWithoutItsAPIUnknownWhenTheClusterRefusesToShowItsScaledObjects(t *testing.T) {
	without := demo(true, nil)
	delete(without.objects, "/apis/keda.sh/v1alpha1/namespaces/demo/scaledobjects")
	g := group("k8s-demo-web", "ip", []string{"10.0.1.1"}, controllerTags("demo/web-web:80"))
	found := Backend(context.Background(), mustClusters(t, one(without)), g)
	if found.Backend == nil || found.Backend.Kubernetes.Keda == nil || *found.Backend.Kubernetes.Keda {
		t.Fatalf("a cluster without KEDA's API: no ScaledObject (false): %+v", found)
	}
	refusing := demo(true, nil)
	refusing.err = map[string]error{"/apis/keda.sh/v1alpha1/namespaces/demo/scaledobjects": &Error{Status: 403, Reason: "Forbidden", Message: "forbidden"}}
	unknown := Backend(context.Background(), mustClusters(t, one(refusing)), g)
	if unknown.Backend == nil || unknown.Backend.Kubernetes.Keda != nil {
		t.Fatalf("a cluster refusing them: unknown (null): %+v", unknown)
	}
	if !strings.Contains(asJSON(t, unknown.Backend.Kubernetes), `"keda":null`) {
		t.Error(asJSON(t, unknown.Backend.Kubernetes))
	}
}

func TestATargetGroupNoIngressClaimsItsIPTargetsPodsOfTheClusterATargetGroupBindingSwitchedByItsRules(t *testing.T) {
	clusters := mustClusters(t, one(demo(true, nil)))
	ctx := context.Background()
	found := Backend(ctx, clusters, group("bound", "ip", []string{"10.0.1.1"}, nil))
	if found.Backend == nil || found.Backend.Kubernetes.Switch != "rule" || found.Backend.Kubernetes.Ingress != nil {
		t.Fatalf("%+v", found)
	}
	// IP targets that are no pod of any cluster: not Kubernetes.
	for _, g := range []Group{group("other", "ip", []string{"10.9.9.9"}, nil), group("vm", "instance", []string{"i-0aaa"}, nil)} {
		if got := Backend(ctx, clusters, g); got.Backend != nil || got.Reason != "" || asJSON(t, got) != "null" {
			t.Errorf("%s: %+v (%s)", g.ARN, got, asJSON(t, got))
		}
	}
}

func TestRefusesWhatCannotBeNappedAsOneKubernetesOwnerWithEachReason(t *testing.T) {
	keda := map[string][]json.RawMessage{"/apis/keda.sh/v1alpha1/namespaces/demo/scaledobjects": {
		obj(m{"metadata": m{"name": "web"}, "spec": m{"scaleTargetRef": m{"name": "web"}, "minReplicaCount": 0}}),
	}}
	clusters := mustClusters(t, one(demo(true, keda)))
	reason := func(targets []string, tags map[string]string) string {
		return Backend(context.Background(), clusters, group("x", "ip", targets, tags)).Reason
	}
	for _, c := range []struct {
		targets []string
		tags    map[string]string
		want    string
	}{
		{[]string{"10.0.3.1"}, nil, "pods owned by DaemonSet agent: Dormeto naps Deployments and StatefulSets"},
		{[]string{"10.0.3.2"}, nil, "pods owned by Job migrate: Dormeto naps Deployments and StatefulSets"},
		{[]string{"10.0.3.3"}, nil, "pods without an owner (lonely)"},
		{[]string{"10.0.4.1", "10.0.4.2"}, nil, "pods of several owners behind the target group: Deployment api, Deployment api2"},
		{[]string{"10.0.1.1"}, controllerTags("demo/web-web:80"), "a KEDA ScaledObject already scales Deployment demo/web to zero (minReplicaCount 0): it naps by itself"},
	} {
		if got := reason(c.targets, c.tags); got != c.want {
			t.Errorf("%v: %q, want %q", c.targets, got, c.want)
		}
	}
}

func TestATargetGroupOfAClusterDormetoCannotReachOrReadIsIneligibleWithTheClustersReason(t *testing.T) {
	r := one(demo(true, nil))
	r.clusters["eks-demo"] = eksCluster(m{"endpointPublicAccess": false, "endpointPrivateAccess": true, "vpcId": "vpc-1"})
	got := Backend(context.Background(), mustClusters(t, r), group("k8s-demo-web", "ip", []string{"10.0.1.1"}, controllerTags("demo/web-web:80")))
	if got.Reason != "cluster eks-demo: "+privateReason {
		t.Fatalf("%q", got.Reason)
	}
	// A tagged cluster the region does not have.
	missing := Backend(context.Background(), mustClusters(t, r), group("g", "ip", nil, map[string]string{"elbv2.k8s.aws/cluster": "gone"}))
	if missing.Reason != "cluster gone not found in the region" {
		t.Fatalf("%q", missing.Reason)
	}
}

func TestPodsGoneAWorkloadAsleepItsOwnerCannotBeTold(t *testing.T) {
	clusters := mustClusters(t, one(demo(true, map[string][]json.RawMessage{"/api/v1/pods": {}})))
	got := Backend(context.Background(), clusters, group("k8s-demo-web", "ip", nil, controllerTags("demo/web-web:80")))
	if got.Reason != "no pod behind the target group: its owner cannot be told (wake it, then discover again)" {
		t.Fatalf("%q", got.Reason)
	}
}

func TestAnErrorReadingTheOwnersIsThatClustersReason(t *testing.T) {
	api := demo(true, nil)
	api.err = map[string]error{"/apis/apps/v1/namespaces/demo/replicasets": errors.New("timeout")}
	clusters := mustClusters(t, one(api))
	got := Backend(context.Background(), clusters, group("k8s-demo-web", "ip", []string{"10.0.1.1"}, nil))
	if got.Reason != "cluster eks-demo: timeout" {
		t.Fatalf("%q", got.Reason)
	}
}

func TestACollectionIsReadOncePerScan(t *testing.T) {
	api := demo(true, nil)
	clusters := mustClusters(t, one(api))
	g := group("k8s-demo-web", "ip", []string{"10.0.1.1", "10.0.1.2"}, controllerTags("demo/web-web:80"))
	Backend(context.Background(), clusters, g)
	Backend(context.Background(), clusters, g)
	n := 0
	for _, c := range api.calls {
		if c == "LIST /apis/networking.k8s.io/v1/namespaces/demo/ingresses" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("ingresses listed %d times", n)
	}
}
