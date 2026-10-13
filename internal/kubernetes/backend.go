// Kubernetes workloads in discovery: the EKS clusters of an account and region,
// and a target group traced to the one Deployment or StatefulSet behind it.
//
// Ports manager packages/discovery/src/kubernetes.ts (clustersOf,
// kubernetesBackend).
package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/Dormeto/cli/internal/awserr"
)

// Autoscaler is the node autoscaler of a cluster: karpenter, cluster-autoscaler
// or none (empty, null in JSON).
type Autoscaler string

// MarshalJSON writes none as null.
func (a Autoscaler) MarshalJSON() ([]byte, error) {
	if a == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(a))
}

// UnmarshalJSON reads null as none.
func (a *Autoscaler) UnmarshalJSON(b []byte) error {
	var s *string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*a = ""
	if s != nil {
		*a = Autoscaler(*s)
	}
	return nil
}

// Cluster is a cluster of the region with what discovery can read of it: its
// pods (by IP) and whether a node autoscaler runs. A cluster it cannot reach or
// read keeps the Reason (its target groups are ineligible with it, the account
// goes on).
type Cluster struct {
	Name       string
	VPCID      string
	Reason     string // empty when read
	Client     Client
	Pods       []Pod
	Autoscaler Autoscaler
	lists      *lists
}

// lists keeps the collections read once per scan.
type lists struct {
	mu      sync.Mutex
	entries map[string]*listing
}

type listing struct {
	once  sync.Once
	items []json.RawMessage
	err   error
}

// MarshalJSON writes the cluster as the scan reports it: its name and its
// reason (null when read).
func (c Cluster) MarshalJSON() ([]byte, error) {
	var reason *string
	if c.Reason != "" {
		reason = &c.Reason
	}
	return json.Marshal(struct {
		Name   string  `json:"name"`
		Reason *string `json:"reason"`
	}{c.Name, reason})
}

// Group is a target group to trace.
type Group struct {
	ARN     string            `json:"arn"`
	Type    string            `json:"type,omitempty"`
	VPCID   string            `json:"vpcId,omitempty"`
	Tags    map[string]string `json:"tags"`
	Targets []string          `json:"targets"`
}

// Port is an Ingress backend's service port.
type Port struct {
	Number int    `json:"number,omitempty"`
	Name   string `json:"name,omitempty"`
}

// Service is an Ingress backend's service.
type Service struct {
	Name string `json:"name"`
	Port Port   `json:"port"`
}

// Ingress is the Ingress rule the target group was made for.
type Ingress struct {
	Name       string  `json:"name"`
	RuleIndex  int     `json:"ruleIndex"`
	GroupOrder *int    `json:"groupOrder,omitempty"`
	Service    Service `json:"service"`
}

// Candidate is the Kubernetes workload behind a target group, with how the
// waker is switched in. Switch is rule, ingress or patched-ingress; GitOps
// argocd or flux (empty: none); Keda nil when the cluster refuses to show its
// ScaledObjects; Capacity fargate or nodes.
type Candidate struct {
	Cluster    string     `json:"cluster"`
	Namespace  string     `json:"namespace"`
	Owner      Owner      `json:"owner"`
	Switch     string     `json:"switch"`
	Ingress    *Ingress   `json:"ingress,omitempty"`
	GitOps     string     `json:"gitops,omitempty"`
	HPA        bool       `json:"hpa"`
	Keda       *bool      `json:"keda"`
	Capacity   string     `json:"capacity"`
	Autoscaler Autoscaler `json:"autoscaler"`
}

// K8sBackend is the backend discovery reports for a Kubernetes workload.
type K8sBackend struct {
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	Kubernetes Candidate `json:"kubernetes"`
}

// Result is the answer of Backend: the owner (Backend), why the target group
// cannot be napped (Reason), or neither when it is not a Kubernetes target
// group. It marshals to the Node value: the backend, the reason or null.
type Result struct {
	Backend *K8sBackend
	Reason  string
}

// MarshalJSON writes the backend, the reason, or null.
func (r Result) MarshalJSON() ([]byte, error) {
	switch {
	case r.Backend != nil:
		return json.Marshal(r.Backend)
	case r.Reason != "":
		return json.Marshal(r.Reason)
	}
	return []byte("null"), nil
}

const (
	private     = "the cluster's endpoint is private (or limited to some addresses): Dormeto reaches it with the in-cluster agent (R10.6)"
	clusterTag  = "elbv2.k8s.aws/cluster"
	resourceTag = "ingress.k8s.aws/resource"
	groupName   = "alb.ingress.kubernetes.io/group.name"
	groupOrder  = "alb.ingress.kubernetes.io/group.order"
)

func statusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// ClustersOf reads every cluster of the region.
func ClustersOf(ctx context.Context, ports Ports) ([]Cluster, error) {
	var names []string
	next := ""
	for {
		page, err := ports.EKS.ListClusters(ctx, ListClustersInput{NextToken: next})
		if err != nil {
			return nil, err
		}
		names = append(names, page.Clusters...)
		if next = page.NextToken; next == "" {
			break
		}
	}
	reader := ports.Reader
	if reader == "" {
		reader = "DmtDiscoveryRole"
	}
	clusters := make([]Cluster, 0, len(names))
	for _, name := range names {
		out, err := ports.EKS.DescribeCluster(ctx, DescribeClusterInput{Name: name})
		if err != nil {
			return nil, err
		}
		c := Cluster{Name: name}
		var vpc *VPCConfig
		if out.Cluster != nil {
			vpc = out.Cluster.ResourcesVpcConfig
		}
		reachable := false
		if vpc != nil {
			c.VPCID = vpc.VPCID
			for _, cidr := range vpc.PublicAccessCidrs {
				reachable = reachable || cidr == "0.0.0.0/0"
			}
			reachable = reachable && vpc.EndpointPublicAccess
		}
		if !reachable {
			c.Reason = private
			clusters = append(clusters, c)
			continue
		}
		if err := read(ctx, ports, &c); err != nil {
			if s := statusOf(err); s == 401 || s == 403 {
				c.Reason = fmt.Sprintf("the cluster does not let %s read it (an access entry with AmazonEKSViewPolicy): %s", reader, awserr.Message(err))
			} else {
				c.Reason = "the cluster cannot be read: " + awserr.Message(err)
			}
			c.Client, c.Pods, c.Autoscaler, c.lists = nil, nil, "", nil
		}
		clusters = append(clusters, c)
	}
	return clusters, nil
}

// read reaches a cluster: its client, its pods and its node autoscaler.
func read(ctx context.Context, ports Ports, c *Cluster) error {
	client, err := ports.Of(ctx, c.Name)
	if err != nil {
		return err
	}
	c.Client = client
	if c.Pods, err = listAs[Pod](ctx, client, "/api/v1/pods", nil); err != nil {
		return err
	}
	if c.Autoscaler, err = autoscalerOf(ctx, client); err != nil {
		return err
	}
	c.lists = &lists{entries: map[string]*listing{}}
	return nil
}

// autoscalerOf finds Karpenter (its API is there) or the Cluster Autoscaler
// (its Deployment in kube-system): a node autoscaler removes the nodes a nap
// frees, so the pods' share of their nodes is saved.
func autoscalerOf(ctx context.Context, client Client) (Autoscaler, error) {
	if _, err := client.Get(ctx, "/apis/karpenter.sh/v1"); err == nil {
		return "karpenter", nil
	} else if statusOf(err) != 404 {
		return "", err
	}
	deployments, err := listAs[Workload](ctx, client, "/apis/apps/v1/namespaces/kube-system/deployments", nil)
	if err != nil {
		return "", nil // the list is optional
	}
	for _, d := range deployments {
		if strings.Contains(d.Metadata.Name, "cluster-autoscaler") {
			return "cluster-autoscaler", nil
		}
	}
	return "", nil
}

// listOf reads a collection once per scan (a failure is kept too).
func (c *Cluster) listOf(ctx context.Context, path string) ([]json.RawMessage, error) {
	c.lists.mu.Lock()
	l := c.lists.entries[path]
	if l == nil {
		l = &listing{}
		c.lists.entries[path] = l
	}
	c.lists.mu.Unlock()
	l.once.Do(func() { l.items, l.err = c.Client.List(ctx, path, nil) })
	return l.items, l.err
}

func listFrom[T any](ctx context.Context, c *Cluster, path string) ([]T, error) {
	raws, err := c.listOf(ctx, path)
	if err != nil {
		return nil, err
	}
	out := make([]T, len(raws))
	for i, raw := range raws {
		if err := json.Unmarshal(raw, &out[i]); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return out, nil
}

// optionalFrom is for the objects of an optional API (HPA, KEDA): none when it
// is missing (404), known false when the cluster refuses to show them.
func optionalFrom[T any](ctx context.Context, c *Cluster, path string) (items []T, known bool) {
	items, err := listFrom[T](ctx, c, path)
	if err != nil {
		return nil, statusOf(err) == 404
	}
	return items, true
}

func selects(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	return true
}

type ingressObject struct {
	Metadata Metadata `json:"metadata"`
	Spec     struct {
		Rules []struct {
			HTTP struct {
				Paths []struct {
					Backend struct {
						Service *Service `json:"service"`
					} `json:"backend"`
				} `json:"paths"`
			} `json:"http"`
		} `json:"rules"`
	} `json:"spec"`
}

type claim struct {
	namespace string
	ingress   ingressObject
	ruleIndex int
	service   Service
}

func portText(p Port) string {
	switch {
	case p.Number != 0:
		return strconv.Itoa(p.Number)
	case p.Name != "":
		return p.Name
	}
	return "undefined"
}

// ingressOf finds the Ingress rule the controller made the target group for:
// its tag ingress.k8s.aws/resource is <namespace>/<ingress>-<service>:<port>.
func ingressOf(ctx context.Context, c *Cluster, resource string) (*claim, error) {
	namespace := strings.SplitN(resource, "/", 2)[0]
	ingresses, err := listFrom[ingressObject](ctx, c, fmt.Sprintf("/apis/networking.k8s.io/v1/namespaces/%s/ingresses", namespace))
	if err != nil {
		return nil, err
	}
	for _, ing := range ingresses {
		for i, rule := range ing.Spec.Rules {
			for _, path := range rule.HTTP.Paths {
				if s := path.Backend.Service; s != nil && fmt.Sprintf("%s/%s-%s:%s", namespace, ing.Metadata.Name, s.Name, portText(s.Port)) == resource {
					return &claim{namespace, ing, i, *s}, nil
				}
			}
		}
	}
	return nil, nil
}

// Backend finds the target group's Kubernetes owner, why it cannot be napped,
// or neither when it is not a Kubernetes target group (no cluster's tag, and no
// target among a reachable cluster's pods).
func Backend(ctx context.Context, clusters []Cluster, g Group) Result {
	tagged := g.Tags[clusterTag]
	var cluster *Cluster
	for i := range clusters {
		c := &clusters[i]
		if tagged != "" {
			if c.Name == tagged {
				cluster = c
				break
			}
			continue
		}
		if g.Type != "ip" {
			break
		}
		if hasTarget(c, g.Targets) {
			cluster = c
			break
		}
	}
	if cluster == nil {
		if tagged != "" {
			return Result{Reason: fmt.Sprintf("cluster %s not found in the region", tagged)}
		}
		return Result{}
	}
	if cluster.Reason != "" {
		return Result{Reason: fmt.Sprintf("cluster %s: %s", cluster.Name, cluster.Reason)}
	}
	b, reason, err := ownerBackend(ctx, cluster, g)
	if err != nil {
		return Result{Reason: fmt.Sprintf("cluster %s: %s", cluster.Name, awserr.Message(err))}
	}
	return Result{Backend: b, Reason: reason}
}

func hasTarget(c *Cluster, targets []string) bool { return len(podsOfTargets(c, targets)) > 0 }

// podsOfTargets are the cluster's pods whose IP is one of the targets.
func podsOfTargets(c *Cluster, targets []string) []Pod {
	var out []Pod
	for _, p := range c.Pods {
		for _, t := range targets {
			if p.Status.PodIP != "" && p.Status.PodIP == t {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

type scaleTarget struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type scaledObject struct {
	Spec struct {
		ScaleTargetRef  scaleTarget `json:"scaleTargetRef"`
		MinReplicaCount *int        `json:"minReplicaCount"`
	} `json:"spec"`
}

type hpa struct {
	Spec struct {
		ScaleTargetRef scaleTarget `json:"scaleTargetRef"`
	} `json:"spec"`
}

// ownerBackend returns the owner, or the reason it cannot be napped, or an error.
func ownerBackend(ctx context.Context, c *Cluster, g Group) (*K8sBackend, string, error) {
	// Its pods: those of the Ingress backend's Service, else its IP targets.
	var cl *claim
	if resource := g.Tags[resourceTag]; resource != "" {
		var err error
		if cl, err = ingressOf(ctx, c, resource); err != nil {
			return nil, "", err
		}
	}
	var candidates []Pod
	if cl != nil {
		services, err := listFrom[struct {
			Metadata Metadata `json:"metadata"`
			Spec     struct {
				Selector map[string]string `json:"selector"`
			} `json:"spec"`
		}](ctx, c, fmt.Sprintf("/api/v1/namespaces/%s/services", cl.namespace))
		if err != nil {
			return nil, "", err
		}
		var selector map[string]string
		for _, s := range services {
			if s.Metadata.Name == cl.service.Name {
				selector = s.Spec.Selector
				break
			}
		}
		for _, p := range c.Pods {
			if p.Metadata.Namespace == cl.namespace && selects(selector, p.Metadata.Labels) {
				candidates = append(candidates, p)
			}
		}
	} else {
		candidates = podsOfTargets(c, g.Targets)
	}
	var pods []Pod
	for _, p := range candidates {
		if !finished(p) {
			pods = append(pods, p)
		}
	}
	if len(pods) == 0 {
		return nil, "no pod behind the target group: its owner cannot be told (wake it, then discover again)", nil
	}

	// Their owners: a Deployment through its ReplicaSet, or a StatefulSet.
	type found struct{ kind, name, namespace string }
	var owners []found
	seen := map[string]int{}
	for _, p := range pods {
		namespace := p.Metadata.Namespace
		ref, ok := p.Metadata.Controller()
		if !ok {
			var names []string
			for _, x := range pods {
				if _, ok := x.Metadata.Controller(); !ok {
					names = append(names, x.Metadata.Name)
				}
			}
			return nil, fmt.Sprintf("pods without an owner (%s)", strings.Join(names, ", ")), nil
		}
		owner := ref
		if ref.Kind == "ReplicaSet" {
			sets, err := listFrom[Workload](ctx, c, fmt.Sprintf("/apis/apps/v1/namespaces/%s/replicasets", namespace))
			if err != nil {
				return nil, "", err
			}
			for _, set := range sets {
				if set.Metadata.Name == ref.Name {
					if up, ok := set.Metadata.Controller(); ok {
						owner = up
					}
					break
				}
			}
		}
		if resources[owner.Kind] == "" {
			return nil, fmt.Sprintf("pods owned by %s %s: Dormeto naps Deployments and StatefulSets", owner.Kind, owner.Name), nil
		}
		key := namespace + "/" + owner.Kind + "/" + owner.Name
		if at, ok := seen[key]; ok {
			owners[at] = found{owner.Kind, owner.Name, namespace}
		} else {
			seen[key] = len(owners)
			owners = append(owners, found{owner.Kind, owner.Name, namespace})
		}
	}
	if len(owners) > 1 {
		var texts []string
		for _, o := range owners {
			texts = append(texts, o.kind+" "+o.name)
		}
		return nil, "pods of several owners behind the target group: " + strings.Join(texts, ", "), nil
	}
	kind, name, namespace := owners[0].kind, owners[0].name, owners[0].namespace
	objects, err := listFrom[Workload](ctx, c, fmt.Sprintf("/apis/apps/v1/namespaces/%s/%s", namespace, resources[kind]))
	if err != nil {
		return nil, "", err
	}
	var object Workload
	for _, o := range objects {
		if o.Metadata.Name == name {
			object = o
			break
		}
	}

	// What scales it besides Dormeto.
	hpas, _ := optionalFrom[hpa](ctx, c, fmt.Sprintf("/apis/autoscaling/v2/namespaces/%s/horizontalpodautoscalers", namespace))
	scaledObjects, kedaKnown := optionalFrom[scaledObject](ctx, c, fmt.Sprintf("/apis/keda.sh/v1alpha1/namespaces/%s/scaledobjects", namespace))
	var scaled *scaledObject
	for i, s := range scaledObjects {
		target := s.Spec.ScaleTargetRef
		if target.Name == name && (target.Kind == "" && kind == "Deployment" || target.Kind == kind) {
			scaled = &scaledObjects[i]
			break
		}
	}
	if scaled != nil && scaled.Spec.MinReplicaCount != nil && *scaled.Spec.MinReplicaCount == 0 {
		return nil, fmt.Sprintf("a KEDA ScaledObject already scales %s %s/%s to zero (minReplicaCount 0): it naps by itself", kind, namespace, name), nil
	}

	// A GitOps owner resets replicas while asleep: Argo CD by its tracking annotation, Flux by its labels.
	labels, annotations := object.Metadata.Labels, object.Metadata.Annotations
	gitops := ""
	switch {
	case annotations["argocd.argoproj.io/tracking-id"] != "" || labels["argocd.argoproj.io/instance"] != "":
		gitops = "argocd"
	case labels["kustomize.toolkit.fluxcd.io/name"] != "" || labels["helm.toolkit.fluxcd.io/name"] != "":
		gitops = "flux"
	}

	// The switch: Dormeto's Ingress in the customer's group (before it), the lone Ingress patched, or the rules.
	switching := "rule"
	var ingress *Ingress
	if cl != nil {
		ingress = &Ingress{Name: cl.ingress.Metadata.Name, RuleIndex: cl.ruleIndex, Service: cl.service}
		if cl.ingress.Metadata.Annotations[groupName] != "" {
			switching = "ingress"
			order, parsed := 0.0, error(nil)
			if text := strings.TrimSpace(cl.ingress.Metadata.Annotations[groupOrder]); text != "" {
				order, parsed = strconv.ParseFloat(text, 64)
			}
			if parsed == nil {
				v := int(math.Max(-1000, order-1))
				ingress.GroupOrder = &v
			}
		} else {
			switching = "patched-ingress"
		}
	}
	capacity := "fargate"
	for _, p := range pods {
		if p.Spec.SchedulerName != "fargate-scheduler" && !strings.HasPrefix(p.Spec.NodeName, "fargate-") {
			capacity = "nodes"
		}
	}
	hasHPA := false
	for _, h := range hpas {
		hasHPA = hasHPA || h.Spec.ScaleTargetRef.Kind == kind && h.Spec.ScaleTargetRef.Name == name
	}
	var keda *bool
	if kedaKnown {
		v := scaled != nil
		keda = &v
	}
	return &K8sBackend{
		Kind: "k8s", ID: fmt.Sprintf("%s/%s/%s/%s", c.Name, namespace, kind, name),
		Kubernetes: Candidate{
			Cluster: c.Name, Namespace: namespace, Owner: Owner{kind, name}, Switch: switching, Ingress: ingress, GitOps: gitops,
			HPA: hasHPA, Keda: keda, Capacity: capacity, Autoscaler: c.Autoscaler,
		},
	}, "", nil
}
