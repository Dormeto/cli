package kubernetes

// The Kubernetes objects the estimate reads, with the fields it uses.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// OwnerReference is a metadata.ownerReferences entry.
type OwnerReference struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Controller bool   `json:"controller,omitempty"`
}

// Metadata is an object's metadata.
type Metadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	OwnerReferences   []OwnerReference  `json:"ownerReferences,omitempty"`
	DeletionTimestamp string            `json:"deletionTimestamp,omitempty"`
}

// Controller is the reference that controls the object, if any.
func (m Metadata) Controller() (OwnerReference, bool) {
	for _, r := range m.OwnerReferences {
		if r.Controller {
			return r, true
		}
	}
	return OwnerReference{}, false
}

// Label is one matchLabels entry.
type Label struct{ Key, Value string }

// Labels are a selector's matchLabels, in the order the API wrote them (the
// label selector keeps it).
type Labels []Label

// UnmarshalJSON reads an object keeping its keys' order.
func (l *Labels) UnmarshalJSON(b []byte) error {
	*l = nil
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if _, err := dec.Token(); err != nil {
		return err
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		var value string
		if err := dec.Decode(&value); err != nil {
			return err
		}
		*l = append(*l, Label{key.(string), value})
	}
	_, err := dec.Token()
	return err
}

// MarshalJSON writes the object back.
func (l Labels) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range l {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(e.Key)
		v, _ := json.Marshal(e.Value)
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Expression is a selector's matchExpressions entry.
type Expression struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}

// Selector is a label selector (matchLabels and matchExpressions).
type Selector struct {
	MatchLabels      Labels       `json:"matchLabels,omitempty"`
	MatchExpressions []Expression `json:"matchExpressions,omitempty"`
}

// Workload is a Deployment or a StatefulSet (or a ReplicaSet: its metadata).
type Workload struct {
	Kind     string   `json:"kind,omitempty"`
	Metadata Metadata `json:"metadata"`
	Spec     struct {
		Replicas *int      `json:"replicas,omitempty"`
		Selector *Selector `json:"selector,omitempty"`
	} `json:"spec"`
}

// Container is a pod's container (its requests only).
type Container struct {
	Resources struct {
		Requests map[string]string `json:"requests,omitempty"`
	} `json:"resources"`
}

// Condition is a pod's status condition.
type Condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

// Pod is a pod with the fields the estimate reads.
type Pod struct {
	Metadata Metadata `json:"metadata"`
	Spec     struct {
		NodeName       string            `json:"nodeName,omitempty"`
		SchedulerName  string            `json:"schedulerName,omitempty"`
		Containers     []Container       `json:"containers,omitempty"`
		InitContainers []Container       `json:"initContainers,omitempty"`
		Overhead       map[string]string `json:"overhead,omitempty"`
	} `json:"spec"`
	Status struct {
		Phase      string      `json:"phase,omitempty"`
		PodIP      string      `json:"podIP,omitempty"`
		Conditions []Condition `json:"conditions,omitempty"`
	} `json:"status"`
}

// decode reads a JSON answer into v (nothing for an empty answer).
func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// listAs lists the items at path as T.
func listAs[T any](ctx context.Context, k Client, path string, query map[string]string) ([]T, error) {
	raws, err := k.List(ctx, path, query)
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
