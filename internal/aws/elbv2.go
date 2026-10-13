package aws

// The load balancers' calls of the scan (discover.ELBv2Port). Ports manager
// packages/discovery/src/aws.ts scanPorts's elbv2.

import (
	"bytes"
	"context"
	"encoding/json"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/Dormeto/cli/internal/discover"
)

// elbv2API is the part of the ELB client the scan calls.
type elbv2API interface {
	DescribeLoadBalancers(context.Context, *elb.DescribeLoadBalancersInput, ...func(*elb.Options)) (*elb.DescribeLoadBalancersOutput, error)
	DescribeListeners(context.Context, *elb.DescribeListenersInput, ...func(*elb.Options)) (*elb.DescribeListenersOutput, error)
	DescribeRules(context.Context, *elb.DescribeRulesInput, ...func(*elb.Options)) (*elb.DescribeRulesOutput, error)
	DescribeTags(context.Context, *elb.DescribeTagsInput, ...func(*elb.Options)) (*elb.DescribeTagsOutput, error)
	DescribeTargetGroups(context.Context, *elb.DescribeTargetGroupsInput, ...func(*elb.Options)) (*elb.DescribeTargetGroupsOutput, error)
	DescribeTargetHealth(context.Context, *elb.DescribeTargetHealthInput, ...func(*elb.Options)) (*elb.DescribeTargetHealthOutput, error)
}

type elbv2Port struct{ client elbv2API }

// optional is a string the API takes only when it is set (a first page's
// marker or token).
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (p elbv2Port) DescribeLoadBalancers(ctx context.Context, in discover.DescribeLoadBalancersInput) (discover.DescribeLoadBalancersOutput, error) {
	out, err := p.client.DescribeLoadBalancers(ctx, &elb.DescribeLoadBalancersInput{Marker: optional(in.Marker)})
	if err != nil {
		return discover.DescribeLoadBalancersOutput{}, err
	}
	res := discover.DescribeLoadBalancersOutput{NextMarker: awssdk.ToString(out.NextMarker)}
	for _, lb := range out.LoadBalancers {
		res.LoadBalancers = append(res.LoadBalancers, discover.LoadBalancer{
			LoadBalancerArn: awssdk.ToString(lb.LoadBalancerArn), LoadBalancerName: awssdk.ToString(lb.LoadBalancerName), Type: string(lb.Type),
		})
	}
	return res, nil
}

func (p elbv2Port) DescribeListeners(ctx context.Context, in discover.DescribeListenersInput) (discover.DescribeListenersOutput, error) {
	out, err := p.client.DescribeListeners(ctx, &elb.DescribeListenersInput{LoadBalancerArn: optional(in.LoadBalancerArn), Marker: optional(in.Marker)})
	if err != nil {
		return discover.DescribeListenersOutput{}, err
	}
	res := discover.DescribeListenersOutput{NextMarker: awssdk.ToString(out.NextMarker)}
	for _, l := range out.Listeners {
		res.Listeners = append(res.Listeners, discover.Listener{
			ListenerArn: awssdk.ToString(l.ListenerArn), Port: int(awssdk.ToInt32(l.Port)), Protocol: string(l.Protocol),
		})
	}
	return res, nil
}

func (p elbv2Port) DescribeRules(ctx context.Context, in discover.DescribeRulesInput) (discover.DescribeRulesOutput, error) {
	out, err := p.client.DescribeRules(ctx, &elb.DescribeRulesInput{ListenerArn: optional(in.ListenerArn), Marker: optional(in.Marker)})
	if err != nil {
		return discover.DescribeRulesOutput{}, err
	}
	res := discover.DescribeRulesOutput{NextMarker: awssdk.ToString(out.NextMarker)}
	for _, r := range out.Rules {
		rule := discover.Rule{RuleArn: awssdk.ToString(r.RuleArn), Priority: awssdk.ToString(r.Priority), IsDefault: awssdk.ToBool(r.IsDefault)}
		for _, c := range r.Conditions {
			rule.Conditions = append(rule.Conditions, conditionOf(c))
		}
		for _, a := range r.Actions {
			rule.Actions = append(rule.Actions, actionOf(a))
		}
		res.Rules = append(res.Rules, rule)
	}
	return res, nil
}

func actionOf(a elbtypes.Action) discover.Action {
	action := discover.Action{Type: string(a.Type), Order: int(awssdk.ToInt32(a.Order)), TargetGroupArn: awssdk.ToString(a.TargetGroupArn)}
	if f := a.ForwardConfig; f != nil {
		action.ForwardConfig = &discover.ForwardConfig{}
		if f.TargetGroups != nil {
			action.ForwardConfig.TargetGroups = make([]discover.ForwardTarget, 0, len(f.TargetGroups))
		}
		for _, g := range f.TargetGroups {
			target := discover.ForwardTarget{TargetGroupArn: awssdk.ToString(g.TargetGroupArn)}
			if g.Weight != nil {
				w := int(*g.Weight)
				target.Weight = &w
			}
			action.ForwardConfig.TargetGroups = append(action.ForwardConfig.TargetGroups, target)
		}
	}
	return action
}

// conditionOf is a rule's condition: the fields the scan reads, and Raw, the
// whole condition's AWS JSON, as Node's JSON.stringify of the SDK's object
// gives it (its keys in the API's order, a list the API gave empty kept
// empty): discovery compares two rules' conditions by it (scan.ts
// conditionsKey), so two rules differing by a header, a query string, a method
// or a source address differ.
func conditionOf(c elbtypes.RuleCondition) discover.Condition {
	d := discover.Condition{Field: awssdk.ToString(c.Field), Values: c.Values, Raw: rawCondition(c)}
	if c.HostHeaderConfig != nil {
		d.HostHeaderConfig = &discover.ValuesConfig{Values: c.HostHeaderConfig.Values}
	}
	if c.PathPatternConfig != nil {
		d.PathPatternConfig = &discover.ValuesConfig{Values: c.PathPatternConfig.Values}
	}
	return d
}

// The condition's AWS JSON: the members of the API's model, in its order.
type (
	rawValues struct {
		Values      []string `json:"Values,omitzero"`
		RegexValues []string `json:"RegexValues,omitzero"`
	}
	rawHeader struct {
		HttpHeaderName *string  `json:"HttpHeaderName,omitempty"`
		Values         []string `json:"Values,omitzero"`
		RegexValues    []string `json:"RegexValues,omitzero"`
	}
	rawPair struct {
		Key   *string `json:"Key,omitempty"`
		Value *string `json:"Value,omitempty"`
	}
	rawQuery struct {
		Values []rawPair `json:"Values,omitzero"`
	}
	rawMethod struct {
		Values []string `json:"Values,omitzero"`
	}
	rawSourceIP struct {
		Values        []string `json:"Values,omitzero"`
		IpAddressType string   `json:"IpAddressType,omitempty"`
	}
	rawRuleCondition struct {
		Field                   *string      `json:"Field,omitempty"`
		Values                  []string     `json:"Values,omitzero"`
		HostHeaderConfig        *rawValues   `json:"HostHeaderConfig,omitempty"`
		PathPatternConfig       *rawValues   `json:"PathPatternConfig,omitempty"`
		HttpHeaderConfig        *rawHeader   `json:"HttpHeaderConfig,omitempty"`
		QueryStringConfig       *rawQuery    `json:"QueryStringConfig,omitempty"`
		HttpRequestMethodConfig *rawMethod   `json:"HttpRequestMethodConfig,omitempty"`
		SourceIpConfig          *rawSourceIP `json:"SourceIpConfig,omitempty"`
		RegexValues             []string     `json:"RegexValues,omitzero"`
	}
)

func rawCondition(c elbtypes.RuleCondition) string {
	r := rawRuleCondition{Field: c.Field, Values: c.Values, RegexValues: c.RegexValues}
	if h := c.HostHeaderConfig; h != nil {
		r.HostHeaderConfig = &rawValues{Values: h.Values, RegexValues: h.RegexValues}
	}
	if p := c.PathPatternConfig; p != nil {
		r.PathPatternConfig = &rawValues{Values: p.Values, RegexValues: p.RegexValues}
	}
	if h := c.HttpHeaderConfig; h != nil {
		r.HttpHeaderConfig = &rawHeader{HttpHeaderName: h.HttpHeaderName, Values: h.Values, RegexValues: h.RegexValues}
	}
	if q := c.QueryStringConfig; q != nil {
		r.QueryStringConfig = &rawQuery{}
		if q.Values != nil {
			r.QueryStringConfig.Values = make([]rawPair, 0, len(q.Values))
		}
		for _, v := range q.Values {
			r.QueryStringConfig.Values = append(r.QueryStringConfig.Values, rawPair{Key: v.Key, Value: v.Value})
		}
	}
	if m := c.HttpRequestMethodConfig; m != nil {
		r.HttpRequestMethodConfig = &rawMethod{Values: m.Values}
	}
	if s := c.SourceIpConfig; s != nil {
		r.SourceIpConfig = &rawSourceIP{Values: s.Values, IpAddressType: string(s.IpAddressType)}
	}
	// As JSON.stringify writes it: <, > and & as they are.
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return ""
	}
	return string(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
}

func (p elbv2Port) DescribeTags(ctx context.Context, in discover.DescribeTagsInput) (discover.DescribeTagsOutput, error) {
	out, err := p.client.DescribeTags(ctx, &elb.DescribeTagsInput{ResourceArns: in.ResourceArns})
	if err != nil {
		return discover.DescribeTagsOutput{}, err
	}
	var res discover.DescribeTagsOutput
	for _, d := range out.TagDescriptions {
		desc := discover.TagDescription{ResourceArn: awssdk.ToString(d.ResourceArn)}
		for _, t := range d.Tags {
			desc.Tags = append(desc.Tags, discover.Tag{Key: awssdk.ToString(t.Key), Value: awssdk.ToString(t.Value)})
		}
		res.TagDescriptions = append(res.TagDescriptions, desc)
	}
	return res, nil
}

func (p elbv2Port) DescribeTargetGroups(ctx context.Context, in discover.DescribeTargetGroupsInput) (discover.DescribeTargetGroupsOutput, error) {
	ask := &elb.DescribeTargetGroupsInput{Marker: optional(in.Marker)}
	if len(in.TargetGroupArns) > 0 {
		ask.TargetGroupArns = in.TargetGroupArns
	}
	out, err := p.client.DescribeTargetGroups(ctx, ask)
	if err != nil {
		return discover.DescribeTargetGroupsOutput{}, err
	}
	res := discover.DescribeTargetGroupsOutput{NextMarker: awssdk.ToString(out.NextMarker)}
	for _, g := range out.TargetGroups {
		res.TargetGroups = append(res.TargetGroups, discover.TargetGroup{
			TargetGroupArn: awssdk.ToString(g.TargetGroupArn), TargetGroupName: awssdk.ToString(g.TargetGroupName), TargetType: string(g.TargetType),
			LoadBalancerArns: g.LoadBalancerArns, VpcId: awssdk.ToString(g.VpcId),
		})
	}
	return res, nil
}

func (p elbv2Port) DescribeTargetHealth(ctx context.Context, in discover.DescribeTargetHealthInput) (discover.DescribeTargetHealthOutput, error) {
	out, err := p.client.DescribeTargetHealth(ctx, &elb.DescribeTargetHealthInput{TargetGroupArn: optional(in.TargetGroupArn)})
	if err != nil {
		return discover.DescribeTargetHealthOutput{}, err
	}
	var res discover.DescribeTargetHealthOutput
	for _, d := range out.TargetHealthDescriptions {
		var target discover.TargetDescription
		if d.Target != nil {
			target.ID = awssdk.ToString(d.Target.Id)
		}
		res.TargetHealthDescriptions = append(res.TargetHealthDescriptions, discover.TargetHealthDescription{Target: target})
	}
	return res, nil
}
