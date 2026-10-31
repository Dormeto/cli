package kubernetes

// Ports the read parts of router lambda/src/lib/kubernetes.mjs: labelSelector,
// livePods, ownerOf and ownerPods (what k8sItems needs to find an owner's pods).
// The scale, sleep and wake writes are not ported: the estimate never calls them.
// ServiceOwner is Google Cloud's: a standalone NEG names a Service, and its onboarding reads the owner behind it.

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

// podOwner is the owner found of some pods.
type podOwner struct{ kind, name, namespace string }

// ownersOfPods finds the one owner of pods, a Deployment through its ReplicaSet, or a StatefulSet, or says why there is none
// that can be napped. behind names what the pods are behind, in that reason.
func ownersOfPods(pods []Pod, behind string, replicaSets func(namespace string) ([]Workload, error)) ([]podOwner, string, error) {
	var owners []podOwner
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
			sets, err := replicaSets(namespace)
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
			owners[at] = podOwner{owner.Kind, owner.Name, namespace}
		} else {
			seen[key] = len(owners)
			owners = append(owners, podOwner{owner.Kind, owner.Name, namespace})
		}
	}
	if len(owners) > 1 {
		var texts []string
		for _, o := range owners {
			texts = append(texts, o.kind+" "+o.name)
		}
		return nil, "pods of several owners behind " + behind + ": " + strings.Join(texts, ", "), nil
	}
	return owners, "", nil
}

// ServiceOwner traces a Service to the one Deployment or StatefulSet behind it: its selector, the pods it selects, then
// their owner. reason says why it cannot be told, with no owner; err is a refused or failed call.
func ServiceOwner(ctx context.Context, k Client, namespace, service string) (owner *Owner, reason string, err error) {
	var s struct {
		Spec struct {
			Selector map[string]string `json:"selector"`
		} `json:"spec"`
	}
	raw, err := k.Get(ctx, fmt.Sprintf("/api/v1/namespaces/%s/services/%s", namespace, service))
	switch {
	case statusOf(err) == 404:
		return nil, fmt.Sprintf("Service %s is not in namespace %s", service, namespace), nil
	case err != nil:
		return nil, "", err
	}
	if err := decode(raw, &s); err != nil {
		return nil, "", err
	}
	if len(s.Spec.Selector) == 0 {
		return nil, fmt.Sprintf("Service %s has no selector: its pods cannot be told", service), nil
	}
	parts := make([]string, 0, len(s.Spec.Selector))
	for key, value := range s.Spec.Selector {
		parts = append(parts, key+"="+value)
	}
	sort.Strings(parts)
	all, err := listAs[Pod](ctx, k, fmt.Sprintf("/api/v1/namespaces/%s/pods", namespace), map[string]string{"labelSelector": strings.Join(parts, ",")})
	if err != nil {
		return nil, "", err
	}
	var pods []Pod
	for _, p := range all {
		if !finished(p) {
			pods = append(pods, p)
		}
	}
	if len(pods) == 0 {
		return nil, fmt.Sprintf("no pod behind Service %s: its owner cannot be told (scale it up, then onboard again)", service), nil
	}
	var sets []Workload
	listed := false
	owners, reason, err := ownersOfPods(pods, "Service "+service, func(namespace string) ([]Workload, error) {
		if !listed {
			var err error
			if sets, err = listAs[Workload](ctx, k, fmt.Sprintf("/apis/apps/v1/namespaces/%s/replicasets", namespace), nil); err != nil {
				return nil, err
			}
			listed = true
		}
		return sets, nil
	})
	if err != nil || reason != "" {
		return nil, reason, err
	}
	return &Owner{owners[0].kind, owners[0].name}, "", nil
}
