// Package gcponboard is the logic of the CLI's Google Cloud onboarding.
package gcponboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/gcp"
)

// Condition is an IAM condition; two conditions are equal when their titles
// and expressions are.
type Condition struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Expression  string `json:"expression"`
	Location    string `json:"location,omitempty"`

	extra map[string]json.RawMessage
}

type plainCondition Condition

func (c *Condition) UnmarshalJSON(b []byte) error {
	var v plainCondition
	extra, err := unmarshalExtra(b, &v, "title", "description", "expression", "location")
	if err != nil {
		return err
	}
	*c = Condition(v)
	c.extra = extra
	return nil
}

func (c Condition) MarshalJSON() ([]byte, error) { return marshalExtra(plainCondition(c), c.extra) }

// unmarshalExtra decodes b into v and returns the fields of b other than known.
func unmarshalExtra(b []byte, v any, known ...string) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(b, v); err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	for _, k := range known {
		delete(all, k)
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

// marshalExtra encodes v and adds the kept fields.
func marshalExtra(v any, extra map[string]json.RawMessage) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return b, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	for k, x := range extra {
		all[k] = x
	}
	return json.Marshal(all)
}

// Binding is a role given to members, under an optional condition.
type Binding struct {
	Role      string     `json:"role"`
	Members   []string   `json:"members"`
	Condition *Condition `json:"condition,omitempty"`

	extra map[string]json.RawMessage
}

type plainBinding Binding

func (b *Binding) UnmarshalJSON(data []byte) error {
	var v plainBinding
	extra, err := unmarshalExtra(data, &v, "role", "members", "condition")
	if err != nil {
		return err
	}
	*b = Binding(v)
	b.extra = extra
	return nil
}

func (b Binding) MarshalJSON() ([]byte, error) { return marshalExtra(plainBinding(b), b.extra) }

// Policy is an IAM policy. Fields the CLI does not know are kept as they were
// read, so that writing the policy back never drops them.
type Policy struct {
	Version  int       `json:"version,omitempty"`
	Etag     string    `json:"etag,omitempty"`
	Bindings []Binding `json:"bindings,omitempty"`

	extra map[string]json.RawMessage
}

type plainPolicy Policy

// UnmarshalJSON keeps the fields other than version, etag and bindings.
func (p *Policy) UnmarshalJSON(b []byte) error {
	var v plainPolicy
	extra, err := unmarshalExtra(b, &v, "version", "etag", "bindings")
	if err != nil {
		return err
	}
	*p = Policy(v)
	p.extra = extra
	return nil
}

// MarshalJSON writes the known fields and the kept ones.
func (p Policy) MarshalJSON() ([]byte, error) { return marshalExtra(plainPolicy(p), p.extra) }

// Grant is one member of one role, under an optional condition.
type Grant struct {
	Role      string     `json:"role"`
	Member    string     `json:"member"`
	Condition *Condition `json:"condition,omitempty"`
}

func sameCondition(a, b *Condition) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Title == b.Title && a.Expression == b.Expression
}

// serviceAccountEmail is the email of a serviceAccount: or deleted:serviceAccount: member.
func serviceAccountEmail(member string) (string, bool) {
	m := strings.TrimPrefix(member, "deleted:")
	if !strings.HasPrefix(m, "serviceAccount:") {
		return "", false
	}
	m = strings.TrimPrefix(m, "serviceAccount:")
	if i := strings.IndexByte(m, '?'); i >= 0 {
		m = m[:i]
	}
	return m, true
}

func sameMember(binding, want string) bool {
	if binding == want {
		return true
	}
	if !strings.HasPrefix(want, "serviceAccount:") || !strings.HasPrefix(binding, "deleted:") {
		return false
	}
	be, ok := serviceAccountEmail(binding)
	we, _ := serviceAccountEmail(want)
	return ok && be == we
}

// Add gives each grant's member its role under its condition, joining the
// binding of the same role and condition (or creating one). It never removes
// anything and returns the grants that were not there yet.
func Add(p *Policy, grants ...Grant) []Grant {
	var added []Grant
next:
	for _, g := range grants {
		for i := range p.Bindings {
			b := &p.Bindings[i]
			if b.Role != g.Role || !sameCondition(b.Condition, g.Condition) {
				continue
			}
			for _, m := range b.Members {
				if m == g.Member {
					continue next
				}
			}
			b.Members = append(b.Members, g.Member)
			added = append(added, g)
			continue next
		}
		p.Bindings = append(p.Bindings, Binding{Role: g.Role, Members: []string{g.Member}, Condition: g.Condition})
		added = append(added, g)
	}
	return added
}

// Remove takes each grant's member out of the binding of its role and
// condition, and only that member; a binding left empty is dropped. A deleted
// service account's member (deleted:serviceAccount:<email>?uid=<n>) is matched
// by its email. It returns the grants removed, with the member as it was in the
// policy.
func Remove(p *Policy, grants ...Grant) []Grant {
	var removed []Grant
	for _, g := range grants {
		out := make([]Binding, 0, len(p.Bindings))
		for _, b := range p.Bindings {
			if b.Role == g.Role && sameCondition(b.Condition, g.Condition) {
				kept := make([]string, 0, len(b.Members))
				for _, m := range b.Members {
					if sameMember(m, g.Member) {
						removed = append(removed, Grant{Role: g.Role, Member: m, Condition: g.Condition})
					} else {
						kept = append(kept, m)
					}
				}
				if len(kept) != len(b.Members) {
					if len(kept) == 0 {
						continue
					}
					b.Members = kept
				}
			}
			out = append(out, b)
		}
		p.Bindings = out
	}
	return removed
}

func grantsOf(p *Policy) []Grant {
	if p == nil {
		return nil
	}
	var out []Grant
	for _, b := range p.Bindings {
		for _, m := range b.Members {
			out = append(out, Grant{Role: b.Role, Member: m, Condition: b.Condition})
		}
	}
	return out
}

func grantKey(g Grant) string {
	c := ""
	if g.Condition != nil {
		c = g.Condition.Title + "\x00" + g.Condition.Expression
	}
	return g.Role + "\x00" + g.Member + "\x00" + c
}

func sortGrants(gs []Grant) {
	sort.SliceStable(gs, func(i, j int) bool { return grantKey(gs[i]) < grantKey(gs[j]) })
}

// Diff lists the grants in after and not in before, and those in before and
// not in after, each sorted by role, member and condition. Either policy may be nil.
func Diff(before, after *Policy) (added, removed []Grant) {
	was := map[string]bool{}
	for _, g := range grantsOf(before) {
		was[grantKey(g)] = true
	}
	is := map[string]bool{}
	for _, g := range grantsOf(after) {
		is[grantKey(g)] = true
		if !was[grantKey(g)] {
			added = append(added, g)
		}
	}
	for _, g := range grantsOf(before) {
		if !is[grantKey(g)] {
			removed = append(removed, g)
		}
	}
	sortGrants(added)
	sortGrants(removed)
	return added, removed
}

// ResourcePolicy is where the IAM policy of one resource is read and written.
// GetByQuery reads with a GET whose URL carries the requested version (Cloud
// Run v2); otherwise Get is a POST with the version in the body.
type ResourcePolicy struct {
	Get, Set   string
	GetByQuery bool
}

func newResourcePolicy(base string, byQuery bool) ResourcePolicy {
	rp := ResourcePolicy{Get: base + ":getIamPolicy", Set: base + ":setIamPolicy", GetByQuery: byQuery}
	if byQuery {
		rp.Get += "?options.requestedPolicyVersion=3"
	}
	return rp
}

// ProjectPolicy is the policy of a project.
func ProjectPolicy(project string) ResourcePolicy {
	return newResourcePolicy("https://cloudresourcemanager.googleapis.com/v3/projects/"+url.PathEscape(project), false)
}

// ServiceAccountPolicy is the policy of a service account.
func ServiceAccountPolicy(project, email string) ResourcePolicy {
	return newResourcePolicy("https://iam.googleapis.com/v1/projects/"+url.PathEscape(project)+"/serviceAccounts/"+url.PathEscape(email), false)
}

// QueuePolicy is the policy of a Cloud Tasks queue.
func QueuePolicy(project, region, queue string) ResourcePolicy {
	return newResourcePolicy("https://cloudtasks.googleapis.com/v2/projects/"+url.PathEscape(project)+"/locations/"+url.PathEscape(region)+"/queues/"+url.PathEscape(queue), false)
}

// RunPolicy is the policy of a Cloud Run service.
func RunPolicy(project, region, service string) ResourcePolicy {
	return newResourcePolicy("https://run.googleapis.com/v2/projects/"+url.PathEscape(project)+"/locations/"+url.PathEscape(region)+"/services/"+url.PathEscape(service), true)
}

// Result is a policy as read and as written by Update.
type Result struct{ Before, After *Policy }

const maxAttempts = 5

// pause waits before attempt n+1 (n counts from 1); tests replace it.
var pause = func(ctx context.Context, n int) error {
	t := time.NewTimer(time.Duration(n) * 200 * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func isAborted(err error) bool {
	var e *gcp.Error
	return gcp.IsConflict(err) || (errors.As(err, &e) && e.Status == "ABORTED")
}

func read(ctx context.Context, c *gcp.Client, rp ResourcePolicy) (*Policy, error) {
	var p Policy
	var err error
	if rp.GetByQuery {
		err = c.Do(ctx, http.MethodGet, rp.Get, nil, &p)
	} else {
		in := map[string]any{"options": map[string]int{"requestedPolicyVersion": 3}}
		err = c.Do(ctx, http.MethodPost, rp.Get, in, &p)
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func hasCondition(p *Policy) bool {
	for _, b := range p.Bindings {
		if b.Condition != nil {
			return true
		}
	}
	return false
}

// Update reads the policy, applies mutate to a copy and writes it back with
// the etag read; nothing is written when mutate changed no grant. A write
// refused for the etag (409 or ABORTED) is retried, up to 5 attempts in all,
// each with a fresh read. Version 3 is written whenever a binding has a
// condition, else the version read is kept.
func Update(ctx context.Context, c *gcp.Client, rp ResourcePolicy, mutate func(*Policy) error) (Result, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if err := pause(ctx, attempt-1); err != nil {
				return Result{}, err
			}
		}
		before, err := read(ctx, c, rp)
		if err != nil {
			return Result{}, err
		}
		next := *before
		next.Bindings = make([]Binding, len(before.Bindings))
		for i, b := range before.Bindings {
			b.Members = append([]string(nil), b.Members...)
			next.Bindings[i] = b
		}
		if err := mutate(&next); err != nil {
			return Result{}, err
		}
		if a, r := Diff(before, &next); len(a)+len(r) == 0 {
			return Result{Before: before, After: before}, nil
		}
		if hasCondition(&next) && next.Version < 3 {
			next.Version = 3
		}
		var after Policy
		err = c.Do(ctx, http.MethodPost, rp.Set, map[string]any{"policy": next}, &after)
		if err == nil {
			return Result{Before: before, After: &after}, nil
		}
		if !isAborted(err) {
			return Result{}, err
		}
		lastErr = err
	}
	return Result{}, fmt.Errorf("IAM policy changed %d times while it was updated: %w", maxAttempts, lastErr)
}
