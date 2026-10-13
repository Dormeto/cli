package discover

// The types discovery shares across its files: criteria, the organization's
// accounts, the decisions and plans of the perimeter, and what a scan reports.
//
// Ports manager packages/discovery/src/types.ts (Criterion, DiscoveryConfig,
// Account, OrgInventory, RuleRef, ScannedRule, Decision, AccountPlan,
// PairPlan).

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Dormeto/cli/internal/kubernetes"
)

// Backend kinds of a candidate.
const (
	KindInstance = "instance"
	KindASG      = "asg"
	KindECS      = "ecs"
	KindK8s      = "k8s"
)

// TagWant is a tag a criterion asks for, with the values it accepts (none: any value).
type TagWant struct {
	Key    string
	Values []string
}

// TagWants are the tags of a criterion, in the order the configuration lists
// them (the order of their description). It reads and writes a JSON object
// {"key": ["value", ...]}. A nil TagWants is a dimension the criterion does not
// set; an empty one is set, and asks for nothing.
type TagWants []TagWant

// UnmarshalJSON reads an object, keeping its key order.
func (w *TagWants) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*w = nil
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return fmt.Errorf("tags: an object is expected")
	}
	out := TagWants{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		var values []string
		if err := dec.Decode(&values); err != nil {
			return err
		}
		out = append(out, TagWant{Key: key.(string), Values: values})
	}
	*w = out
	return nil
}

// MarshalJSON writes the object in order.
func (w TagWants) MarshalJSON() ([]byte, error) {
	if w == nil {
		return []byte("null"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, t := range w {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(t.Key)
		v, _ := json.Marshal(append([]string{}, t.Values...))
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Map is the wanted tags by key.
func (w TagWants) Map() map[string][]string {
	m := make(map[string][]string, len(w))
	for _, t := range w {
		m[t.Key] = t.Values
	}
	return m
}

// Criterion selects accounts, regions and resources; every dimension it sets
// must match. A nil slice or TagWants is a dimension it does not set.
type Criterion struct {
	Note          string   `json:"note,omitempty"`
	AccountIDs    []string `json:"accountIds,omitempty"`
	OUIDs         []string `json:"ouIds,omitempty"`
	AccountTags   TagWants `json:"accountTags,omitempty"`
	Regions       []string `json:"regions,omitempty"`
	LoadBalancers []string `json:"loadBalancers,omitempty"`
	Hosts         []string `json:"hosts,omitempty"`
	ResourceTags  TagWants `json:"resourceTags,omitempty"`
	BackendKinds  []string `json:"backendKinds,omitempty"`
	TargetGroups  []string `json:"targetGroups,omitempty"`
	Label         string   `json:"label,omitempty"`
}

// Filter includes what any of its includes match, minus its own excludes.
type Filter struct {
	Name    string      `json:"name"`
	Include []Criterion `json:"include"`
	Exclude []Criterion `json:"exclude"`
}

// Config is the discovery configuration the perimeter reads.
type Config struct {
	Version        int         `json:"version"`
	Mode           string      `json:"mode"`
	Regions        []string    `json:"regions"`
	RefreshHours   int         `json:"refreshHours"`
	GlobalExcludes []Criterion `json:"globalExcludes"`
	Filters        []Filter    `json:"filters"`
}

// Account is an active account of the organization.
type Account struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	OUPath []string          `json:"ouPath"` // root first, parent last
	Tags   map[string]string `json:"tags"`
}

// OU is an organizational unit (the root included, with a nil parent).
type OU struct {
	Name   string  `json:"name"`
	Parent *string `json:"parent"`
}

// OrgInventory is the organization's accounts and OU tree.
type OrgInventory struct {
	Accounts []Account     `json:"accounts"`
	OUs      map[string]OU `json:"ous"`
}

// RuleRef is a listener rule of a candidate.
type RuleRef struct {
	ID         string   `json:"id"` // rule ARN
	Listener   string   `json:"listener"`
	Priority   string   `json:"priority"`
	Conditions string   `json:"conditions"`
	Hosts      []string `json:"hosts"`
	Managed    bool     `json:"managed"` // tagged dormeto:workload
}

// Eligibility of a candidate.
const (
	Managed    = "managed"
	Eligible   = "eligible"
	Ineligible = "ineligible"
)

// ScannedRule is a candidate: an application target group with every rule
// forwarding to it (ID the target group ARN), or a rule that forwards to no
// single application target group (ID the rule ARN).
type ScannedRule struct {
	ID              string              `json:"id"`
	Account         string              `json:"account"`
	Region          string              `json:"region"`
	LoadBalancer    string              `json:"loadBalancer"`
	LoadBalancerArn string              `json:"loadBalancerArn"`
	Listener        string              `json:"listener"`
	Priority        string              `json:"priority"`
	Conditions      string              `json:"conditions"`
	Hosts           []string            `json:"hosts"`
	TargetGroup     *string             `json:"targetGroup"`
	TargetGroupArn  *string             `json:"targetGroupArn"`
	Backend         *Backend            `json:"backend"`
	Tags            map[string][]string `json:"tags"` // every value of each tag on the load balancer, the target group or the rule
	Rules           []RuleRef           `json:"rules"`
	Eligibility     string              `json:"eligibility"`
	Reason          string              `json:"reason,omitempty"`
	WorkloadID      string              `json:"workloadId,omitempty"`
	IdleHours7d     *int                `json:"idleHours7d"`
	Requests7d      *int                `json:"requests7d"`
}

// Decision is whether a rule is in the perimeter, and why.
type Decision struct {
	Scope  string `json:"scope"` // in, out or excluded
	Reason string `json:"reason"`
}

// AccountPlan says whether an account is scanned.
type AccountPlan struct {
	Account string `json:"account"`
	Scan    bool   `json:"scan"`
	Reason  string `json:"reason"`
}

// PairPlan says whether an account and region are scanned.
type PairPlan struct {
	Account string `json:"account"`
	Region  string `json:"region"`
	Scan    bool   `json:"scan"`
	Reason  string `json:"reason"`
}

// Backend is the backend behind a target group: an instance, an Auto Scaling
// group, an ECS service or a Kubernetes owner (Kubernetes then says how).
type Backend struct {
	Kind       string                `json:"kind"`
	ID         string                `json:"id"`
	ARN        string                `json:"arn,omitempty"` // from 2026-10-05 scans
	Kubernetes *kubernetes.Candidate `json:"kubernetes,omitempty"`
}
