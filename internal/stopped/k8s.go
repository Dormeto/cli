package stopped

// What a nap of a Kubernetes backend stops (R9): its running pods, Fargate pods
// by size, the others by node; alike pods counted. Ports k8sItems and its
// helpers of router lambda/src/lib/kubernetes.mjs (quantity, requestsOf,
// fargateSize, nodeItem).

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"

	"github.com/Dormeto/cli/internal/kubernetes"
)

// Fargate adds 256 MB to each pod for the Kubernetes components, before
// rounding up to one of its sizes.
const (
	fargateExtraGb = 0.25
	gib            = float64(1 << 30)
	unpriced       = "pods without CPU and memory requests: their share of the node is unknown"
)

var (
	binary   = map[string]float64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "Pi": 1 << 50, "Ei": 1 << 60}
	decimal  = map[string]float64{"k": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "P": 1e15, "E": 1e18}
	fraction = map[string]float64{"n": 1e9, "u": 1e6, "m": 1e3}
	quantRe  = regexp.MustCompile(`^([0-9.]+(?:[eE][+-]?\d+)?)([a-zA-Z]*)$`)
	sizeRe   = regexp.MustCompile(`^([\d.]+)vCPU ([\d.]+)GB$`)
)

// quantity reads a Kubernetes quantity ('250m', '512Mi', '1e9', '2') as a number.
func quantity(text string) float64 {
	m := quantRe.FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	if f, ok := fraction[m[2]]; ok {
		return n / f
	}
	if f, ok := binary[m[2]]; ok {
		return n * f
	}
	if f, ok := decimal[m[2]]; ok {
		return n * f
	}
	return n
}

// requestsOf is a pod's effective request: the larger of its containers' sum
// and its largest init container, plus its overhead.
func requestsOf(pod kubernetes.Pod) (cpu, memoryGb float64) {
	of := func(key string) float64 {
		sum := 0.0
		for _, c := range pod.Spec.Containers {
			sum += quantity(c.Resources.Requests[key])
		}
		largest := sum
		for _, c := range pod.Spec.InitContainers {
			largest = math.Max(largest, quantity(c.Resources.Requests[key]))
		}
		return largest + quantity(pod.Spec.Overhead[key])
	}
	return of("cpu"), of("memory") / gib
}

func steps(from, to, by int) []float64 {
	var out []float64
	for v := from; v <= to; v += by {
		out = append(out, float64(v))
	}
	return out
}

var fargateSizes = []struct {
	vcpu  float64
	sizes []float64
}{
	{0.25, []float64{0.5, 1, 2}}, {0.5, []float64{1, 2, 3, 4}}, {1, steps(2, 8, 1)}, {2, steps(4, 16, 1)},
	{4, steps(8, 30, 1)}, {8, steps(16, 60, 4)}, {16, steps(32, 120, 8)},
}

// fargateSize is the size Fargate gave the pod (its CapacityProvisioned
// annotation), else its requests plus 256 MB rounded up to a Fargate size;
// false above the largest.
func fargateSize(pod kubernetes.Pod) (vcpu, memoryGb float64, ok bool) {
	if m := sizeRe.FindStringSubmatch(pod.Metadata.Annotations["CapacityProvisioned"]); m != nil {
		v, errV := strconv.ParseFloat(m[1], 64)
		g, errG := strconv.ParseFloat(m[2], 64)
		if errV == nil && errG == nil {
			return v, g, true
		}
	}
	cpu, mem := requestsOf(pod)
	for _, row := range fargateSizes {
		for _, gb := range row.sizes {
			if gb >= mem+fargateExtraGb {
				if row.vcpu >= cpu {
					return row.vcpu, gb, true
				}
				break
			}
		}
	}
	return 0, 0, false
}

func isFargate(pod kubernetes.Pod) bool {
	_, annotated := pod.Metadata.Annotations["CapacityProvisioned"]
	return pod.Spec.SchedulerName == "fargate-scheduler" || annotated
}

// round is Math.round(n * 1000) / 1000.
func round(n float64) float64 { return math.Floor(n*1000+0.5) / 1000 }

// node is the part of a Node the items read.
type node struct {
	Metadata kubernetes.Metadata `json:"metadata"`
	Status   struct {
		Allocatable map[string]string `json:"allocatable"`
	} `json:"status"`
}

// nodeItem is a pod on an EC2 node: its requests, the node's instance type, OS,
// allocatable and capacity type, and whether a node autoscaler removes the node
// it frees (Karpenter's nodes; a node group when discovery found the Cluster
// Autoscaler).
func nodeItem(pod kubernetes.Pod, n node, b Backend) Item {
	labels := n.Metadata.Labels
	cpu, memoryGb := requestsOf(pod)
	item := Item{Kind: KindK8sPods, CPU: round(cpu), MemoryGb: round(memoryGb), OS: "Linux"}
	if t, ok := labels["node.kubernetes.io/instance-type"]; ok {
		item.NodeType = &t
	} else if t, ok := labels["beta.kubernetes.io/instance-type"]; ok {
		item.NodeType = &t
	}
	if labels["kubernetes.io/os"] == "windows" {
		item.OS = "Windows"
	}
	item.Spot = labels["karpenter.sh/capacity-type"] == "spot" || labels["eks.amazonaws.com/capacityType"] == "SPOT"
	pool, ok := labels["karpenter.sh/nodepool"]
	if !ok {
		pool = labels["karpenter.sh/provisioner-name"]
	}
	item.Autoscaled = pool != "" || b.Autoscaler == kubernetes.Autoscaler("cluster-autoscaler")
	item.Allocatable = &Allocatable{CPU: round(quantity(n.Status.Allocatable["cpu"])), MemoryGb: round(quantity(n.Status.Allocatable["memory"]) / gib)}
	return item
}

// k8sItems is what a nap of the backend stops.
func k8sItems(ctx context.Context, ports Ports, b Backend) ([]Item, error) {
	if ports.KubernetesOf == nil {
		return nil, errors.New("ports.kubernetesOf is not a function")
	}
	k8s, err := ports.KubernetesOf(ctx, b.Cluster)
	if err != nil {
		return nil, err
	}
	ref := kubernetes.Ref{Namespace: b.Namespace, Owner: b.Owner}
	owner, err := kubernetes.GetWorkload(ctx, k8s, ref)
	if err != nil {
		return nil, err
	}
	all, err := kubernetes.LivePods(ctx, k8s, ref, owner)
	if err != nil {
		return nil, err
	}
	nodes := map[string]node{}
	var counted []Item
	at := map[string]int{}
	var reasons []string
	said := map[string]bool{}
	unknownReason := func(r string) {
		if !said[r] {
			said[r] = true
			reasons = append(reasons, r)
		}
	}
	for _, pod := range all {
		if pod.Metadata.DeletionTimestamp != "" || pod.Spec.NodeName == "" {
			continue
		}
		var item Item
		if isFargate(pod) {
			vcpu, memoryGb, ok := fargateSize(pod)
			if !ok {
				unknownReason("pods larger than any Fargate size")
				continue
			}
			item = Item{Kind: KindFargate, VCPU: vcpu, MemoryGb: memoryGb, Arch: "x86_64", OS: "Linux"}
		} else {
			cpu, memoryGb := requestsOf(pod)
			if cpu == 0 && memoryGb == 0 {
				unknownReason(unpriced)
				continue
			}
			name := pod.Spec.NodeName
			n, ok := nodes[name]
			if !ok {
				raw, err := k8s.Get(ctx, "/api/v1/nodes/"+name)
				if err != nil {
					return nil, err
				}
				if err := json.Unmarshal(raw, &n); err != nil {
					return nil, err
				}
				nodes[name] = n
			}
			item = nodeItem(pod, n, b)
		}
		key, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		if i, ok := at[string(key)]; ok {
			counted[i].Count++
			continue
		}
		item.Count = 1
		at[string(key)] = len(counted)
		counted = append(counted, item)
	}
	for _, reason := range reasons {
		counted = append(counted, Item{Kind: KindUnknown, Backend: "k8s:" + b.ID, Reason: reason})
	}
	return counted, nil
}
