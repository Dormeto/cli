package discover

// One account and region: every rule of the application load balancers, with
// what Dormeto could do with it (managed, eligible, ineligible and why), its
// host names and tags, and how idle its target group was over the last 7 days.
//
// Ports manager packages/discovery/src/scan.ts (scanRegion, groupCandidate,
// ruleCandidate, tagValues, tagsOf, all), itself ported from the router
// discovery (router lambda/src/lib/candidates.mjs). The backends behind the
// target groups are in backend.go, the rules' reading in rules.go, the idle
// hours in idle.go.

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/kubernetes"
)

const (
	workloadTag = "dormeto:workload"
	roleTag     = "dormeto:role"
	shadowsTag  = "dormeto:shadows" // a Dormeto rule before a customer rule (R4c)
	maxRules    = 10
	tagBatch    = 20
)

// Where is an account and region.
type Where struct{ Account, Region string }

// ScanResult is a region's scan: its candidates in the scan's order and the
// region's clusters as read (the estimate lists those it cannot read).
type ScanResult struct {
	Rules    []ScannedRule        `json:"rules"`
	Clusters []kubernetes.Cluster `json:"clusters"`
}

// row is a rule of a listener of an application load balancer.
type row struct {
	lb         LoadBalancer
	listener   Listener
	rule       Rule
	forward    forward
	managed    string       // workload of a rule tagged dormeto:workload
	shadowedBy string       // workload whose shadow rule (dormeto:shadows) covers this customer rule
	app        *TargetGroup // the one application target group it forwards to
}

func ruleRef(r *row) RuleRef {
	return RuleRef{
		ID:         r.rule.RuleArn,
		Listener:   fmt.Sprintf("%s:%d", r.listener.Protocol, r.listener.Port),
		Priority:   r.rule.Priority,
		Conditions: conditionsOf(r.rule),
		Hosts:      hostsOf(r.rule),
		Managed:    r.managed != "",
	}
}

// rank orders rules: the default rule comes last.
func rank(priority string) float64 {
	if priority == "" || priority == "default" {
		return math.Inf(1)
	}
	if n, err := strconv.ParseFloat(strings.TrimSpace(priority), 64); err == nil {
		return n
	}
	return math.NaN()
}

// tagsOf reads the tags of the resources, 20 ARNs a call.
func tagsOf(ctx context.Context, elbv2 ELBv2Port, arns []string) (map[string]map[string]string, error) {
	tags := map[string]map[string]string{}
	var unique []string
	for _, a := range arns {
		if !slices.Contains(unique, a) {
			unique = append(unique, a)
		}
	}
	for _, batch := range chunks(unique, tagBatch) {
		out, err := elbv2.DescribeTags(ctx, DescribeTagsInput{ResourceArns: batch})
		if err != nil {
			return nil, err
		}
		for _, d := range out.TagDescriptions {
			if d.ResourceArn == "" {
				continue
			}
			m := make(map[string]string, len(d.Tags))
			for _, t := range d.Tags {
				m[t.Key] = t.Value
			}
			tags[d.ResourceArn] = m
		}
	}
	return tags, nil
}

var accessDenied = regexp.MustCompile(`(?i)AccessDenied|not authorized`)

// ScanRegion scans an account and region. now gives the end of the idle window
// (the hour before it). A call that fails fails the scan, except the clusters'
// listing when the role lacks EKS rights (the region is scanned as before).
func ScanRegion(ctx context.Context, ports ScanPorts, where Where, now time.Time) (ScanResult, error) {
	elbv2 := ports.ELBv2
	all, err := pages(func(m string) ([]LoadBalancer, string, error) {
		out, err := elbv2.DescribeLoadBalancers(ctx, DescribeLoadBalancersInput{Marker: m})
		return out.LoadBalancers, out.NextMarker, err
	})
	if err != nil {
		return ScanResult{}, err
	}
	var lbs []LoadBalancer
	for _, lb := range all {
		if lb.Type == "application" {
			lbs = append(lbs, lb)
		}
	}
	var rows []*row
	for _, lb := range lbs {
		listeners, err := pages(func(m string) ([]Listener, string, error) {
			out, err := elbv2.DescribeListeners(ctx, DescribeListenersInput{LoadBalancerArn: lb.LoadBalancerArn, Marker: m})
			return out.Listeners, out.NextMarker, err
		})
		if err != nil {
			return ScanResult{}, err
		}
		sort.SliceStable(listeners, func(i, j int) bool { return listeners[i].Port < listeners[j].Port })
		for _, listener := range listeners {
			rules, err := pages(func(m string) ([]Rule, string, error) {
				out, err := elbv2.DescribeRules(ctx, DescribeRulesInput{ListenerArn: listener.ListenerArn, Marker: m})
				return out.Rules, out.NextMarker, err
			})
			if err != nil {
				return ScanResult{}, err
			}
			for _, rule := range rules {
				rows = append(rows, &row{lb: lb, listener: listener, rule: rule, forward: forwardOf(rule)})
			}
		}
	}
	tgs, err := pages(func(m string) ([]TargetGroup, string, error) {
		out, err := elbv2.DescribeTargetGroups(ctx, DescribeTargetGroupsInput{Marker: m})
		return out.TargetGroups, out.NextMarker, err
	})
	if err != nil {
		return ScanResult{}, err
	}
	groups := map[string]*TargetGroup{}
	for i := range tgs {
		groups[tgs[i].TargetGroupArn] = &tgs[i]
	}
	var arns []string
	for _, lb := range lbs {
		arns = append(arns, lb.LoadBalancerArn)
	}
	for _, r := range rows {
		arns = append(arns, r.rule.RuleArn)
	}
	for _, r := range rows {
		arns = append(arns, r.forward.all...)
	}
	arns = slices.DeleteFunc(arns, func(a string) bool { return a == "" })
	tags, err := tagsOf(ctx, elbv2, arns)
	if err != nil {
		return ScanResult{}, err
	}
	var visible []*row
	for _, r := range rows {
		if tags[r.rule.RuleArn][roleTag] != "admin" {
			visible = append(visible, r)
		}
	}
	own, err := backendOwners(ctx, ports)
	if err != nil {
		return ScanResult{}, err
	}
	// A discovery role without EKS rights (accounts set up before 0.10.0) scans the region as before.
	clusters := []kubernetes.Cluster{}
	if ports.Kubernetes != nil {
		k := *ports.Kubernetes
		k.Account, k.Region = where.Account, where.Region
		got, err := kubernetes.ClustersOf(ctx, k)
		if err != nil {
			if !accessDenied.MatchString(awserr.Name(err) + " " + awserr.Message(err)) {
				return ScanResult{}, err
			}
			got = nil
		}
		if got != nil {
			clusters = got
		}
	}

	// The application target group of each rule: the one it forwards to, or, managed, its target group that is not the
	// waker. Managed: a rule tagged dormeto:workload, or one forwarding to a waker target group (tagged so by onboarding):
	// the controller makes the rule of Dormeto's Ingress, or patches a lone Ingress's, and never tags them (spec section 6).
	wakerOf := func(r *row) string {
		for _, arn := range r.forward.all {
			if g := groups[arn]; g != nil && g.TargetType == "lambda" {
				if w := tags[g.TargetGroupArn][workloadTag]; w != "" {
					return w
				}
			}
		}
		return ""
	}
	for _, r := range visible {
		if w, ok := tags[r.rule.RuleArn][workloadTag]; ok {
			r.managed = w
		} else {
			r.managed = wakerOf(r)
		}
		if r.forward.typ != "forward" {
			continue
		}
		if r.managed != "" {
			for _, arn := range r.forward.all {
				if g := groups[arn]; g != nil && g.TargetType != "lambda" {
					r.app = g
					break
				}
			}
		} else if len(r.forward.groups) == 1 {
			if g := groups[r.forward.groups[0]]; g != nil && g.TargetType != "lambda" && g.TargetType != "alb" {
				r.app = g
			}
		}
	}
	// Customer rules covered by a workload's shadow (R4c): left untouched, they are part of the workload.
	for _, r := range visible {
		if r.managed == "" {
			continue
		}
		covered := tags[r.rule.RuleArn][shadowsTag]
		if covered == "" {
			continue
		}
		if i := slices.IndexFunc(visible, func(c *row) bool { return c.rule.RuleArn == covered }); i >= 0 {
			visible[i].shadowedBy = r.managed
		}
	}
	// Dormeto's Ingress in a group forwards to its own target group, before the customer rule whose conditions it copies:
	// that rule is the workload's, and its target group (the one onboarded) the workload's application target group.
	for _, r := range visible {
		if r.managed == "" || r.app == nil || tags[r.rule.RuleArn][workloadTag] != "" {
			continue
		}
		i := slices.IndexFunc(visible, func(c *row) bool {
			return c.managed == "" && c.app != nil && c.listener.ListenerArn == r.listener.ListenerArn &&
				rank(c.rule.Priority) > rank(r.rule.Priority) && conditionsKey(c.rule) == conditionsKey(r.rule)
		})
		if i < 0 {
			continue
		}
		visible[i].shadowedBy = r.managed
		r.app = visible[i].app
	}
	// Target groups receiving traffic from a weighted forward of several target groups (canary, blue-green).
	weighted := map[string]bool{}
	for _, r := range visible {
		if r.managed == "" && r.forward.typ == "forward" && len(r.forward.groups) > 1 {
			for _, g := range r.forward.groups {
				weighted[g] = true
			}
		}
	}
	var order []string
	byGroup := map[string][]*row{}
	for _, r := range visible {
		if r.app != nil && r.app.TargetGroupArn != "" {
			arn := r.app.TargetGroupArn
			if _, ok := byGroup[arn]; !ok {
				order = append(order, arn)
			}
			byGroup[arn] = append(byGroup[arn], r)
		}
	}

	candidates := []ScannedRule{}
	for _, arn := range order {
		members := byGroup[arn]
		sort.SliceStable(members, func(i, j int) bool { return rank(members[i].rule.Priority) < rank(members[j].rule.Priority) })
		c, err := groupCandidate(ctx, ports, arn, members, candidateContext{where, tags, weighted, own, clusters})
		if err != nil {
			return ScanResult{}, err
		}
		candidates = append(candidates, c)
	}
	for _, r := range visible {
		if r.app == nil {
			candidates = append(candidates, ruleCandidate(r, where, tags, groups))
		}
	}

	var pairs []pair
	index := map[string]int{}
	for _, c := range candidates {
		if c.Eligibility == Ineligible || c.TargetGroupArn == nil || *c.TargetGroupArn == "" {
			continue
		}
		p := pair{c.LoadBalancerArn, *c.TargetGroupArn}
		if i, ok := index[p.tg]; ok {
			pairs[i] = p // a later pair replaces the value, the first keeps the place
			continue
		}
		index[p.tg] = len(pairs)
		pairs = append(pairs, p)
	}
	if len(pairs) > 0 && ports.CloudWatch != nil {
		idles, err := idleHours(ctx, ports.CloudWatch, pairs, now)
		if err != nil {
			return ScanResult{}, err
		}
		for i := range candidates {
			c := &candidates[i]
			if c.Eligibility == Ineligible || c.TargetGroupArn == nil || *c.TargetGroupArn == "" {
				continue
			}
			if v, ok := idles[*c.TargetGroupArn]; ok {
				h, r := v.hours, v.requests
				c.IdleHours7d, c.Requests7d = &h, &r
			}
		}
	}
	return ScanResult{Rules: candidates, Clusters: clusters}, nil
}

type candidateContext struct {
	where    Where
	tags     map[string]map[string]string
	weighted map[string]bool
	owners   owners
	clusters []kubernetes.Cluster
}

func ptr(s string) *string { return &s }

// groupCandidate: one candidate per application target group, with every rule forwarding to it.
func groupCandidate(ctx context.Context, ports ScanPorts, arn string, members []*row, c candidateContext) (ScannedRule, error) {
	first := members[0]
	app := first.app
	var managedRules []*row
	for _, m := range members {
		if m.managed != "" {
			managedRules = append(managedRules, m)
		}
	}
	conditions := conditionsOf(first.rule)
	if len(members) > 1 {
		conditions += fmt.Sprintf(" (+%d rules)", len(members)-1)
	}
	hosts := []string{}
	refs := make([]RuleRef, len(members))
	var lbTags []map[string]string
	var lbSeen []string
	for i, m := range members {
		for _, h := range hostsOf(m.rule) {
			if !slices.Contains(hosts, h) {
				hosts = append(hosts, h)
			}
		}
		refs[i] = ruleRef(m)
		if !slices.Contains(lbSeen, m.lb.LoadBalancerArn) {
			lbSeen = append(lbSeen, m.lb.LoadBalancerArn)
			lbTags = append(lbTags, c.tags[m.lb.LoadBalancerArn])
		}
	}
	sets := append(lbTags, c.tags[arn])
	for _, m := range members {
		sets = append(sets, c.tags[m.rule.RuleArn])
	}
	base := ScannedRule{
		ID:              arn,
		Account:         c.where.Account,
		Region:          c.where.Region,
		LoadBalancer:    first.lb.LoadBalancerName,
		LoadBalancerArn: first.lb.LoadBalancerArn,
		Listener:        fmt.Sprintf("%s:%d", first.listener.Protocol, first.listener.Port),
		Priority:        first.rule.Priority,
		Conditions:      conditions,
		Hosts:           hosts,
		TargetGroup:     ptr(app.TargetGroupName),
		TargetGroupArn:  ptr(arn),
		Tags:            tagValues(sets),
		Rules:           refs,
		Eligibility:     Eligible,
	}
	// What keeps the group from being napped as one workload, in order.
	var workloads []string
	for _, m := range managedRules {
		if !slices.Contains(workloads, m.managed) {
			workloads = append(workloads, m.managed)
		}
	}
	outside := 0
	for _, m := range members {
		if m.managed == "" && !(m.shadowedBy != "" && slices.Contains(workloads, m.shadowedBy)) {
			outside++
		}
	}
	lbSet := map[string]bool{}
	for _, m := range members {
		lbSet[m.lb.LoadBalancerArn] = true
	}
	issue := ""
	switch {
	case slices.ContainsFunc(members, func(m *row) bool { return m.rule.IsDefault }):
		issue = "listener default action (not supported yet)"
	// Napping one workload would leave the other's rules in front of a stopped backend.
	case len(workloads) > 1:
		issue = fmt.Sprintf("rules of %d workloads share the target group (%s)", len(workloads), strings.Join(workloads, ", "))
	case len(managedRules) > 0 && outside > 0:
		issue = fmt.Sprintf("%d of %d rules sharing the target group are not part of the workload", outside, len(members))
	case c.weighted[arn]:
		issue = "target group also in a weighted forward"
	case len(lbSet) > 1:
		issue = "target group used by several load balancers"
	case len(members) > maxRules:
		issue = fmt.Sprintf("more than %d rules", maxRules)
	}
	b, err := backendOf(ctx, ports, *app, c.owners, c.clusters, c.tags[arn])
	if err != nil {
		return ScannedRule{}, err
	}
	if len(managedRules) > 0 {
		base.Eligibility, base.WorkloadID, base.Backend, base.Reason = Managed, managedRules[0].managed, b.backend, issue
		return base, nil
	}
	if issue != "" {
		base.Eligibility, base.Reason = Ineligible, issue
		return base, nil
	}
	if b.backend == nil {
		base.Eligibility, base.Reason = Ineligible, b.reason
		return base, nil
	}
	base.Backend = b.backend
	return base, nil
}

// ruleCandidate: a rule that forwards to no single application target group, a
// candidate of its own, never eligible.
func ruleCandidate(r *row, where Where, tags map[string]map[string]string, groups map[string]*TargetGroup) ScannedRule {
	var group *TargetGroup
	if len(r.forward.groups) == 1 {
		group = groups[r.forward.groups[0]]
	}
	var reason string
	switch {
	case r.forward.typ != "forward":
		reason = reasonOf(r.forward.typ)
	case len(r.forward.groups) != 1:
		reason = "weighted forward to several target groups"
	case group == nil:
		reason = "target group not found"
	case group.TargetType == "lambda":
		reason = "Lambda target group"
	case group.TargetType == "alb":
		reason = "load balancer target group"
	default:
		reason = "application target group not found"
	}
	sets := []map[string]string{tags[r.lb.LoadBalancerArn]}
	c := ScannedRule{
		ID:              r.rule.RuleArn,
		Account:         where.Account,
		Region:          where.Region,
		LoadBalancer:    r.lb.LoadBalancerName,
		LoadBalancerArn: r.lb.LoadBalancerArn,
		Listener:        fmt.Sprintf("%s:%d", r.listener.Protocol, r.listener.Port),
		Priority:        r.rule.Priority,
		Conditions:      conditionsOf(r.rule),
		Hosts:           hostsOf(r.rule),
		Rules:           []RuleRef{ruleRef(r)},
		Eligibility:     Ineligible,
		Reason:          reason,
	}
	if group != nil {
		c.TargetGroup, c.TargetGroupArn = ptr(group.TargetGroupName), ptr(group.TargetGroupArn)
		sets = append(sets, tags[group.TargetGroupArn])
	}
	c.Tags = tagValues(append(sets, tags[r.rule.RuleArn]))
	// A managed rule that no longer forwards to its application waits for offboarding.
	if r.managed != "" {
		c.Eligibility, c.WorkloadID = Managed, r.managed
	}
	return c
}

// tagValues: every value of each tag over the load balancer, target group and
// rule: a tag criterion matches any of the three.
func tagValues(sets []map[string]string) map[string][]string {
	out := map[string][]string{}
	for _, set := range sets {
		keys := make([]string, 0, len(set))
		for k := range set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !slices.Contains(out[k], set[k]) {
				out[k] = append(out[k], set[k])
			}
		}
	}
	return out
}
