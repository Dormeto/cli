package fleet

// The discovery candidates (GET /iam/discovery/candidates): the rows the CLI prints, every page of a listing, and the
// resolution of what people name (a candidate's id, or its target group's name) to candidates.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// candidatePage is the most candidates the manager gives in a page.
const candidatePage = 200

// Candidate is a row of GET /iam/discovery/candidates: what the CLI reads of it. The row as the API sent it is kept, so
// --json prints every field. Its id is the ARN of its target group (or of its rule), and only ever travels in a body.
type Candidate struct {
	ID             string   `json:"id"`
	Account        string   `json:"account"`
	AccountName    string   `json:"accountName,omitempty"`
	Region         string   `json:"region"`
	LoadBalancer   string   `json:"loadBalancer,omitempty"`
	TargetGroup    string   `json:"targetGroup,omitempty"`
	TargetGroupARN string   `json:"targetGroupArn,omitempty"`
	Hosts          []string `json:"hosts,omitempty"`
	Status         string   `json:"status"`
	StatusReason   string   `json:"statusReason,omitempty"`
	WorkloadID     string   `json:"workloadId,omitempty"`
	IdleHours7d    *float64 `json:"idleHours7d,omitempty"`
	Requests7d     *float64 `json:"requests7d,omitempty"`
	Workload       *struct {
		Router     string `json:"router"`
		WorkloadID string `json:"workloadId"`
		Name       string `json:"name"`
	} `json:"workload,omitempty"`
	Operation *OpRef `json:"operation,omitempty"`
	Backend   *struct {
		Kind string `json:"kind"`
	} `json:"backend,omitempty"`
	Dismissed *struct {
		By   string `json:"by"`
		At   string `json:"at"`
		Note string `json:"note"`
	} `json:"dismissed,omitempty"`

	raw json.RawMessage
}

type candidateFields Candidate // without the methods below, so that decoding does not recurse

func (c *Candidate) UnmarshalJSON(data []byte) error {
	var f candidateFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*c = Candidate(f)
	c.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON is the row as the API sent it, or the fields known when it was built here.
func (c Candidate) MarshalJSON() ([]byte, error) {
	if c.raw != nil {
		return c.raw, nil
	}
	return json.Marshal(candidateFields(c))
}

// Name is the candidate's target group name, or its id when it has no target group.
func (c Candidate) Name() string {
	switch {
	case c.TargetGroupARN != "":
		return TargetGroupName(c.TargetGroupARN)
	case c.TargetGroup != "":
		return c.TargetGroup
	}
	return TargetGroupName(c.ID)
}

// TargetGroupName is the name in a target group's ARN (…:targetgroup/<name>/<id>), the ARN itself when it has none.
func TargetGroupName(arn string) string {
	_, rest, ok := strings.Cut(arn, ":targetgroup/")
	if name, _, _ := strings.Cut(rest, "/"); ok && name != "" {
		return name
	}
	return arn
}

// ListCandidates reads the candidates of a status (and of an account, and a region of it, when given) page by page,
// 200 at a time, and stops at limit when it is above zero.
func ListCandidates(ctx context.Context, c *Client, status, account, region string, limit int) ([]Candidate, error) {
	var out []Candidate
	cursor := ""
	for {
		size := candidatePage
		if limit > 0 {
			size = min(size, limit-len(out))
		}
		q := url.Values{"status": {status}, "limit": {strconv.Itoa(size)}}
		if account != "" {
			q.Set("account", account)
		}
		if region != "" {
			q.Set("region", region)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Candidates []Candidate `json:"candidates"`
			Cursor     *string     `json:"cursor"`
		}
		if err := c.Get(ctx, "/discovery/candidates", q, &page); err != nil {
			return out, err
		}
		out = append(out, page.Candidates...)
		if limit > 0 && len(out) >= limit {
			return out[:limit], nil
		}
		if page.Cursor == nil || *page.Cursor == "" || *page.Cursor == cursor {
			return out, nil
		}
		cursor = *page.Cursor
	}
}

// ResolveCandidates finds what each ref names among cs: the candidate with that id, else the one whose target group has
// that name. A ref that names none, or the target groups of several accounts or regions, is an error (a usage error to
// the commands) and nothing is resolved; each candidate is given once.
func ResolveCandidates(cs []Candidate, refs []string) ([]Candidate, error) {
	var out []Candidate
	seen := map[string]bool{}
	for _, ref := range refs {
		var found []Candidate
		for _, c := range cs {
			if c.ID == ref {
				found = []Candidate{c}
				break
			}
			if c.Name() == ref {
				found = append(found, c)
			}
		}
		switch len(found) {
		case 0:
			status := ""
			if len(cs) > 0 {
				status = " in " + cs[0].Status
			}
			return nil, fmt.Errorf("no candidate %s%s", ref, status)
		case 1:
		default:
			lines := []string{ref + " names several target groups:"}
			for _, c := range found {
				lines = append(lines, "  "+c.ID)
			}
			return nil, fmt.Errorf("%s", strings.Join(lines, "\n"))
		}
		if !seen[found[0].ID] {
			seen[found[0].ID] = true
			out = append(out, found[0])
		}
	}
	return out, nil
}
