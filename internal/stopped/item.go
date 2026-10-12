// Package stopped describes what a nap of each backend stops.
//
// Item ports the StoppedItem union of manager packages/fleet/src/prices.ts.
package stopped

import "encoding/json"

// Kinds of Item.
const (
	KindEC2              = "ec2"
	KindFargate          = "fargate"
	KindRDS              = "rds"
	KindAuroraServerless = "aurora-serverless"
	KindK8sPods          = "k8s-pods"
	KindUnknown          = "unknown"
)

// Allocatable is a node's allocatable CPU and memory (k8s-pods).
type Allocatable struct {
	CPU      float64 `json:"cpu"`
	MemoryGb float64 `json:"memoryGb"`
}

// Item is one thing a nap stops: the StoppedItem union as one struct. Kind
// says which fields it carries; it marshals to the JSON of Node's item of that
// kind (fields the kind does not carry are absent, those it carries always
// present, a zero count included).
//
//	ec2:               type, count, os, spot?
//	fargate:           vcpu, memoryGb, count, arch, os, spot?
//	rds:               class, engine, multiAz
//	aurora-serverless: engine, acu
//	k8s-pods:          count, cpu, memoryGb, nodeType (or null), os, spot?, autoscaled, allocatable
//	unknown:           backend, reason
type Item struct {
	Kind        string       `json:"kind"`
	Type        string       `json:"type,omitempty"`
	Count       int          `json:"count,omitempty"`
	OS          string       `json:"os,omitempty"`
	Spot        bool         `json:"spot,omitempty"`
	VCPU        float64      `json:"vcpu,omitempty"`
	MemoryGb    float64      `json:"memoryGb,omitempty"`
	Arch        string       `json:"arch,omitempty"`
	Class       string       `json:"class,omitempty"`
	Engine      string       `json:"engine,omitempty"`
	MultiAZ     bool         `json:"multiAz,omitempty"`
	ACU         float64      `json:"acu,omitempty"`
	CPU         float64      `json:"cpu,omitempty"`
	NodeType    *string      `json:"nodeType,omitempty"`
	Autoscaled  bool         `json:"autoscaled,omitempty"`
	Allocatable *Allocatable `json:"allocatable,omitempty"`
	Backend     string       `json:"backend,omitempty"`
	Reason      string       `json:"reason,omitempty"`
}

// MarshalJSON writes the fields of the item's kind, in Node's order.
func (i Item) MarshalJSON() ([]byte, error) {
	type pair struct {
		k string
		v any
	}
	var fields []pair
	add := func(k string, v any) { fields = append(fields, pair{k, v}) }
	add("kind", i.Kind)
	switch i.Kind {
	case KindEC2:
		add("type", i.Type)
		add("count", i.Count)
		add("os", i.OS)
		if i.Spot {
			add("spot", true)
		}
	case KindFargate:
		add("vcpu", i.VCPU)
		add("memoryGb", i.MemoryGb)
		add("count", i.Count)
		add("arch", i.Arch)
		add("os", i.OS)
		if i.Spot {
			add("spot", true)
		}
	case KindRDS:
		add("class", i.Class)
		add("engine", i.Engine)
		add("multiAz", i.MultiAZ)
	case KindAuroraServerless:
		add("engine", i.Engine)
		add("acu", i.ACU)
	case KindK8sPods:
		add("count", i.Count)
		add("cpu", i.CPU)
		add("memoryGb", i.MemoryGb)
		add("nodeType", i.NodeType)
		add("os", i.OS)
		if i.Spot {
			add("spot", true)
		}
		add("autoscaled", i.Autoscaled)
		alloc := i.Allocatable
		if alloc == nil {
			alloc = &Allocatable{}
		}
		add("allocatable", alloc)
	case KindUnknown:
		add("backend", i.Backend)
		add("reason", i.Reason)
	}
	out := []byte{'{'}
	for n, f := range fields {
		if n > 0 {
			out = append(out, ',')
		}
		k, _ := json.Marshal(f.k)
		v, err := json.Marshal(f.v)
		if err != nil {
			return nil, err
		}
		out = append(out, k...)
		out = append(out, ':')
		out = append(out, v...)
	}
	return append(out, '}'), nil
}
