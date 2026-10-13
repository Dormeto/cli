package kubernetes

// Ports the read parts of router lambda/src/lib/kubernetes.mjs: labelSelector,
// livePods, ownerOf and ownerPods (what k8sItems needs to find an owner's pods).
// The scale, sleep and wake writes are not ported: the estimate never calls them.

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Owner is a Deployment or a StatefulSet.
type Owner struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Ref is where an owner lives in its cluster.
type Ref struct {
	Namespace string
	Owner     Owner
}

var resources = map[string]string{"Deployment": "deployments", "StatefulSet": "statefulsets"}

// OwnerPath is the owner's path in the API.
func OwnerPath(ref Ref) string {
	return fmt.Sprintf("/apis/apps/v1/namespaces/%s/%s/%s", ref.Namespace, resources[ref.Owner.Kind], ref.Owner.Name)
}

// LabelSelector writes an owner's selector as the API's labelSelector
// parameter takes it.
func LabelSelector(s Selector) (string, error) {
	var parts []string
	for _, l := range s.MatchLabels {
		parts = append(parts, l.Key+"="+l.Value)
	}
	for _, e := range s.MatchExpressions {
		switch e.Operator {
		case "In":
			parts = append(parts, fmt.Sprintf("%s in (%s)", e.Key, strings.Join(e.Values, ",")))
		case "NotIn":
			parts = append(parts, fmt.Sprintf("%s notin (%s)", e.Key, strings.Join(e.Values, ",")))
		case "Exists":
			parts = append(parts, e.Key)
		case "DoesNotExist":
			parts = append(parts, "!"+e.Key)
		default:
			return "", fmt.Errorf("unknown selector operator %q", e.Operator)
		}
	}
	return strings.Join(parts, ","), nil
}

func finished(p Pod) bool { return p.Status.Phase == "Succeeded" || p.Status.Phase == "Failed" }

// GetWorkload reads the owner; a missing one is the API's 404.
func GetWorkload(ctx context.Context, k Client, ref Ref) (Workload, error) {
	var w Workload
	raw, err := k.Get(ctx, OwnerPath(ref))
	if err != nil {
		return w, err
	}
	if err := decode(raw, &w); err != nil {
		return w, err
	}
	return w, nil
}

// OwnerOf reads the owner, nil when it does not exist.
func OwnerOf(ctx context.Context, k Client, ref Ref) (*Workload, error) {
	w, err := GetWorkload(ctx, k, ref)
	var e *Error
	if errors.As(err, &e) && e.Status == 404 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// LivePods are the owner's pods still there (finished ones are gone for good):
// those it controls, a Deployment's through its ReplicaSets. The selector may
// match another owner's pods (a canary selecting the same labels): never the
// owner's, foreign behind the target group.
func LivePods(ctx context.Context, k Client, ref Ref, w Workload) ([]Pod, error) {
	var sel Selector
	if w.Spec.Selector != nil {
		sel = *w.Spec.Selector
	}
	selector, err := LabelSelector(sel)
	if err != nil {
		return nil, err
	}
	query := map[string]string{"labelSelector": selector}
	pods, err := listAs[Pod](ctx, k, fmt.Sprintf("/api/v1/namespaces/%s/pods", ref.Namespace), query)
	if err != nil {
		return nil, err
	}
	parents := map[string]bool{}
	if ref.Owner.Kind == "Deployment" {
		sets, err := listAs[Workload](ctx, k, fmt.Sprintf("/apis/apps/v1/namespaces/%s/replicasets", ref.Namespace), query)
		if err != nil {
			return nil, err
		}
		for _, rs := range sets {
			if c, ok := rs.Metadata.Controller(); ok && c.Kind == "Deployment" && c.Name == ref.Owner.Name {
				parents["ReplicaSet/"+rs.Metadata.Name] = true
			}
		}
	} else {
		parents[ref.Owner.Kind+"/"+ref.Owner.Name] = true
	}
	var live []Pod
	for _, p := range pods {
		c, ok := p.Metadata.Controller()
		if !finished(p) && ok && parents[c.Kind+"/"+c.Name] {
			live = append(live, p)
		}
	}
	return live, nil
}

// OwnerPods are the owner's pods still there; none when the owner is gone.
func OwnerPods(ctx context.Context, k Client, ref Ref) ([]Pod, error) {
	w, err := OwnerOf(ctx, k, ref)
	if err != nil || w == nil {
		return nil, err
	}
	return LivePods(ctx, k, ref, *w)
}
