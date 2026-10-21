package fleet

// The manager's manual exclusions (GET, POST and DELETE /iam/exclusions): the shapes the CLI reads, kept raw for
// --json, the rules of their targets, and the text `dormeto exclusions list` prints.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ExclusionTarget is what an exclusion leaves out of discovery: a target group, a load balancer or a whole account.
type ExclusionTarget struct {
	Kind    string `json:"kind"` // target-group, load-balancer or account
	ARN     string `json:"arn,omitempty"`
	Account string `json:"account"`
	Region  string `json:"region,omitempty"`
	Name    string `json:"name"`
}

// Text is the target as the manager words it: "target group api", "account sandbox (123456789012)".
func (t ExclusionTarget) Text() string {
	kind := strings.ReplaceAll(t.Kind, "-", " ")
	if t.Kind == "account" && t.Name != "" && t.Name != t.Account {
		return fmt.Sprintf("%s %s (%s)", kind, t.Name, t.Account)
	}
	return kind + " " + orStr(t.Name, t.Account)
}

func orStr(s, d string) string {
	if s != "" {
		return s
	}
	return d
}

// Exclusion is a row of GET /iam/exclusions: what the CLI reads of it. The row as the API sent it is kept, so --json
// prints every field.
type Exclusion struct {
	ID        string          `json:"id"`
	Target    ExclusionTarget `json:"target"`
	Reason    string          `json:"reason"`
	By        string          `json:"by"`
	At        string          `json:"at"`
	ExpiresAt string          `json:"expiresAt,omitempty"`
	LiftedAt  string          `json:"liftedAt,omitempty"`
	LapsedAt  string          `json:"lapsedAt,omitempty"`
	Paused    []struct {
		Router     string `json:"router"`
		WorkloadID string `json:"workloadId"`
		Name       string `json:"name,omitempty"`
	} `json:"paused,omitempty"`
	Expired bool `json:"expired"`
	Lifting bool `json:"lifting"`

	raw json.RawMessage
}

type exclusionFields Exclusion // without the methods below, so that decoding does not recurse

func (e *Exclusion) UnmarshalJSON(data []byte) error {
	var f exclusionFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*e = Exclusion(f)
	e.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON is the row as the API sent it, or the fields known when it was built here.
func (e Exclusion) MarshalJSON() ([]byte, error) {
	if e.raw != nil {
		return e.raw, nil
	}
	return json.Marshal(exclusionFields(e))
}

// State is "lifting" while a lift runs, "expired" once its date has passed, else "active".
func (e Exclusion) State() string {
	switch {
	case e.Lifting:
		return "lifting"
	case e.Expired:
		return "expired"
	}
	return "active"
}

// ListExclusions reads GET /exclusions.
func ListExclusions(ctx context.Context, c *Client) ([]Exclusion, error) {
	var out struct {
		Exclusions []Exclusion `json:"exclusions"`
	}
	err := c.Get(ctx, "/exclusions", nil, &out)
	return out.Exclusions, err
}

// ExclusionsText is the exclusions as a table: target, reason, who, until, the workloads it paused (by name) and its
// state; times in loc. "no exclusions" when there are none.
func ExclusionsText(es []Exclusion, loc *time.Location) string {
	if len(es) == 0 {
		return "no exclusions\n"
	}
	rows := [][]string{{"ID", "TARGET", "REASON", "BY", "UNTIL", "PAUSED", "STATE"}}
	for _, e := range es {
		var paused []string
		for _, p := range e.Paused {
			paused = append(paused, orStr(p.Name, p.WorkloadID))
		}
		rows = append(rows, []string{e.ID, e.Target.Text(), e.Reason, ShortARN(e.By), orStr(When(e.ExpiresAt, loc), none), orStr(strings.Join(paused, ", "), none), e.State()})
	}
	return Table(rows)
}

var (
	targetGroupARN  = regexp.MustCompile(`^arn:aws[a-z-]*:elasticloadbalancing:[a-z0-9-]+:\d{12}:targetgroup/[A-Za-z0-9-]{1,32}/[0-9a-f]{16}$`)
	loadBalancerARN = regexp.MustCompile(`^arn:aws[a-z-]*:elasticloadbalancing:[a-z0-9-]+:\d{12}:loadbalancer/app/[A-Za-z0-9-]{1,32}/[0-9a-f]{16}$`)
)

// IsTargetGroupARN and IsLoadBalancerARN tell whether s is the ARN of an application target group or load balancer, as
// the manager accepts one to exclude.
func IsTargetGroupARN(s string) bool  { return targetGroupARN.MatchString(s) }
func IsLoadBalancerARN(s string) bool { return loadBalancerARN.MatchString(s) }

// Exclude starts an exclusion: POST /exclusions with body ({target, reason, expiresAt?, workload?}), the operation that
// pauses its workloads.
func Exclude(ctx context.Context, c *Client, body map[string]any) (Operation, error) {
	var out struct {
		Operation Operation `json:"operation"`
	}
	err := c.Post(ctx, "/exclusions", body, &out)
	return out.Operation, err
}

// LiftExclusion lifts an exclusion: DELETE /exclusions/{id}, the operation that resumes its workloads and deletes it.
func LiftExclusion(ctx context.Context, c *Client, id string) (Operation, error) {
	var out struct {
		Operation Operation `json:"operation"`
	}
	err := c.Delete(ctx, "/exclusions/"+url.PathEscape(id), &out)
	return out.Operation, err
}
