package discover

// A region with one application load balancer and the router discovery cases
// (manager packages/discovery/test/scan-fakes.ts).

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

const (
	pfx  = "arn:aws:elasticloadbalancing:eu-west-1:111122223333"
	lbA  = pfx + ":loadbalancer/app/web/abc"
	l443 = pfx + ":listener/app/web/abc/l443"
	l80  = pfx + ":listener/app/web/abc/l80"

	batchASGARN = "arn:aws:autoscaling:eu-west-1:111122223333:autoScalingGroup:6d4f2c1e-0000-4000-8000-000000000001:autoScalingGroupName/batch-asg"
)

func tgArn(name string) string { return pfx + ":targetgroup/" + name + "/1" }

// tgs in the order of the Node fake's TGS object.
var tgNames = []struct{ key, name, typ string }{
	{"shop", "shop", "instance"}, {"api", "api", "ip"}, {"batch", "batch", "instance"}, {"fn", "fn", "lambda"},
	{"shared", "shared", "instance"}, {"orphan", "orphan", "ip"}, {"empty", "empty", "instance"},
	{"demo", "demo-app", "instance"}, {"waker", "dmt-dev-demo", "lambda"}, {"green", "green", "instance"}, {"blue", "blue", "instance"},
}

func tgKey(key string) string {
	for _, g := range tgNames {
		if g.key == key {
			return tgArn(g.name)
		}
	}
	panic("no target group " + key)
}

type fwd struct {
	key    string
	weight int
}

func fwdTo(groups ...fwd) []Action {
	var tg []ForwardTarget
	for _, g := range groups {
		w := g.weight
		tg = append(tg, ForwardTarget{TargetGroupArn: tgKey(g.key), Weight: &w})
	}
	return []Action{{Type: "forward", Order: 1, ForwardConfig: &ForwardConfig{TargetGroups: tg}}}
}

// conds reads conditions from JSON, as the API gives them.
func conds(s string) []Condition {
	var c []Condition
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		panic(err)
	}
	return c
}

func host(h ...string) []Condition {
	b, _ := json.Marshal(h)
	return conds(`[{"Field":"host-header","HostHeaderConfig":{"Values":` + string(b) + `}}]`)
}

func newRules() map[string][]Rule {
	return map[string][]Rule{
		l80: {{RuleArn: "r80", Priority: "default", IsDefault: true, Conditions: []Condition{}, Actions: []Action{{Type: "redirect", Order: 1}}}},
		l443: {
			{RuleArn: "r-demo", Priority: "10", Conditions: host("dev.example.com"), Actions: fwdTo(fwd{"demo", 1}, fwd{"waker", 0})},
			{RuleArn: "r-admin", Priority: "5", Conditions: host("dev.example.com"), Actions: fwdTo(fwd{"waker", 1})},
			{RuleArn: "r-shop", Priority: "20", Conditions: host("shop.example.com", "www.shop.example.com"), Actions: []Action{{Type: "forward", Order: 1, TargetGroupArn: tgKey("shop")}}},
			{RuleArn: "r-api", Priority: "30", Conditions: conds(`[{"Field":"path-pattern","PathPatternConfig":{"Values":["/api/*"]}}]`), Actions: fwdTo(fwd{"api", 1})},
			{RuleArn: "r-batch", Priority: "40", Conditions: conds(`[{"Field":"host-header","Values":["batch.example.com"]}]`), Actions: fwdTo(fwd{"batch", 1})},
			{RuleArn: "r-canary", Priority: "50", Conditions: host("canary.example.com"), Actions: fwdTo(fwd{"shop", 0}, fwd{"green", 1}, fwd{"blue", 1})},
			{RuleArn: "r-fn", Priority: "60", Conditions: host("fn.example.com"), Actions: fwdTo(fwd{"fn", 1})},
			{RuleArn: "r-shared-1", Priority: "70", Conditions: host("a.example.com"), Actions: fwdTo(fwd{"shared", 1})},
			{RuleArn: "r-shared-2", Priority: "71", Conditions: host("b.example.com"), Actions: fwdTo(fwd{"shared", 1})},
			{RuleArn: "r-orphan", Priority: "80", Conditions: host("orphan.example.com"), Actions: fwdTo(fwd{"orphan", 1})},
			{RuleArn: "r-empty", Priority: "90", Conditions: host("empty.example.com"), Actions: fwdTo(fwd{"empty", 1})},
			{RuleArn: "r-fixed", Priority: "95", Conditions: host("down.example.com"), Actions: []Action{{Type: "fixed-response", Order: 1}}},
			{RuleArn: "r-default", Priority: "default", IsDefault: true, Conditions: []Condition{}, Actions: []Action{{Type: "fixed-response", Order: 1}}},
		},
	}
}

var worldTags = map[string]map[string]string{
	"r-demo":      {"dormeto:workload": "demo"},
	"r-admin":     {"dormeto:workload": "demo", "dormeto:role": "admin"},
	lbA:           {"env": "dev", "team": "web"},
	tgArn("shop"): {"team": "shop"},
	"r-shop":      {"env": "preview"},
}

var worldTargets = map[string][]string{
	"shop": {"i-shop"}, "api": {"10.0.1.5"}, "batch": {}, "shared": {"i-s"}, "orphan": {"10.0.9.9"}, "empty": {}, "demo": {"i-demo"}, "green": {"i-g"}, "blue": {"i-b"},
}

// world is the fake ports and what they were asked.
type world struct {
	t             *testing.T
	rules         map[string][]Rule
	hourly        map[string][]float64
	tagBatches    []int
	metricQueries int
	ports         ScanPorts
}

func newWorld(t *testing.T, hourly map[string][]float64) *world {
	w := &world{t: t, rules: newRules(), hourly: hourly}
	w.ports = ScanPorts{ELBv2: elbWorld{w}, AutoScaling: asgWorld{}, ECS: ecsWorld{}, CloudWatch: cwWorld{w}}
	return w
}

// pageOf pages by five, with a marker that is the next index.
func pageOf[T any](items []T, marker string) ([]T, string) {
	start, _ := strconv.Atoi(marker)
	start = min(start, len(items))
	end := min(start+5, len(items))
	next := ""
	if start+5 < len(items) {
		next = strconv.Itoa(start + 5)
	}
	return items[start:end], next
}

type elbWorld struct{ w *world }

func (e elbWorld) DescribeLoadBalancers(_ context.Context, in DescribeLoadBalancersInput) (DescribeLoadBalancersOutput, error) {
	items, next := pageOf([]LoadBalancer{
		{LoadBalancerArn: lbA, LoadBalancerName: "web", Type: "application"},
		{LoadBalancerArn: pfx + ":loadbalancer/net/nlb/x", LoadBalancerName: "nlb", Type: "network"},
	}, in.Marker)
	return DescribeLoadBalancersOutput{LoadBalancers: items, NextMarker: next}, nil
}

func (e elbWorld) DescribeListeners(_ context.Context, in DescribeListenersInput) (DescribeListenersOutput, error) {
	if in.LoadBalancerArn != lbA {
		return DescribeListenersOutput{}, nil
	}
	return DescribeListenersOutput{Listeners: []Listener{{ListenerArn: l80, Port: 80, Protocol: "HTTP"}, {ListenerArn: l443, Port: 443, Protocol: "HTTPS"}}}, nil
}

func (e elbWorld) DescribeRules(_ context.Context, in DescribeRulesInput) (DescribeRulesOutput, error) {
	items, next := pageOf(e.w.rules[in.ListenerArn], in.Marker)
	return DescribeRulesOutput{Rules: items, NextMarker: next}, nil
}

func (e elbWorld) DescribeTags(_ context.Context, in DescribeTagsInput) (DescribeTagsOutput, error) {
	if len(in.ResourceArns) > 20 {
		e.w.t.Errorf("describeTags asked %d resources", len(in.ResourceArns))
	}
	e.w.tagBatches = append(e.w.tagBatches, len(in.ResourceArns))
	var out DescribeTagsOutput
	for _, a := range in.ResourceArns {
		d := TagDescription{ResourceArn: a}
		for k, v := range worldTags[a] {
			d.Tags = append(d.Tags, Tag{Key: k, Value: v})
		}
		out.TagDescriptions = append(out.TagDescriptions, d)
	}
	return out, nil
}

func (e elbWorld) DescribeTargetGroups(_ context.Context, in DescribeTargetGroupsInput) (DescribeTargetGroupsOutput, error) {
	var all []TargetGroup
	for _, g := range tgNames {
		all = append(all, TargetGroup{TargetGroupArn: tgArn(g.name), TargetGroupName: g.name, TargetType: g.typ})
	}
	items, next := pageOf(all, in.Marker)
	return DescribeTargetGroupsOutput{TargetGroups: items, NextMarker: next}, nil
}

func (e elbWorld) DescribeTargetHealth(_ context.Context, in DescribeTargetHealthInput) (DescribeTargetHealthOutput, error) {
	var out DescribeTargetHealthOutput
	for _, g := range tgNames {
		if tgArn(g.name) == in.TargetGroupArn {
			for _, id := range worldTargets[g.key] {
				out.TargetHealthDescriptions = append(out.TargetHealthDescriptions, TargetHealthDescription{Target: TargetDescription{ID: id}})
			}
		}
	}
	return out, nil
}

type asgWorld struct{}

func (asgWorld) DescribeAutoScalingGroups(context.Context, DescribeAutoScalingGroupsInput) (DescribeAutoScalingGroupsOutput, error) {
	return DescribeAutoScalingGroupsOutput{AutoScalingGroups: []AutoScalingGroup{{AutoScalingGroupName: "batch-asg", AutoScalingGroupARN: batchASGARN, TargetGroupARNs: []string{tgKey("batch")}}}}, nil
}

type ecsWorld struct{}

const mainCluster = "arn:aws:ecs:eu-west-1:111122223333:cluster/main"
const apiService = "arn:aws:ecs:eu-west-1:111122223333:service/main/api"

func (ecsWorld) ListClusters(context.Context, ListClustersInput) (ListClustersOutput, error) {
	return ListClustersOutput{ClusterArns: []string{mainCluster}}, nil
}

func (ecsWorld) ListServices(context.Context, ListServicesInput) (ListServicesOutput, error) {
	return ListServicesOutput{ServiceArns: []string{apiService}}, nil
}

func (ecsWorld) DescribeServices(_ context.Context, in DescribeServicesInput) (DescribeServicesOutput, error) {
	var out DescribeServicesOutput
	for _, s := range in.Services {
		out.Services = append(out.Services, Service{ServiceArn: s, LoadBalancers: []ServiceLoadBalancer{{TargetGroupArn: tgKey("api")}}})
	}
	return out, nil
}

type cwWorld struct{ w *world }

func (c cwWorld) GetMetricData(_ context.Context, in GetMetricDataInput) (GetMetricDataOutput, error) {
	c.w.metricQueries += len(in.MetricDataQueries)
	if h := in.EndTime.Sub(in.StartTime).Hours(); h != 168 {
		c.w.t.Errorf("the window is %v hours, want 168", h)
	}
	var out GetMetricDataOutput
	for _, q := range in.MetricDataQueries {
		var dim string
		for _, d := range q.MetricStat.Metric.Dimensions {
			if d.Name == "TargetGroup" {
				dim = d.Value
			}
		}
		out.MetricDataResults = append(out.MetricDataResults, MetricDataResult{ID: q.ID, Values: c.w.hourly[dim]})
	}
	return out, nil
}

// byKey: target group candidates by target group name, rule candidates by rule ARN.
func byKey(rules []ScannedRule) map[string]ScannedRule {
	out := map[string]ScannedRule{}
	for _, c := range rules {
		key := c.ID
		if strings.Contains(c.ID, ":targetgroup/") && c.TargetGroup != nil {
			key = *c.TargetGroup
		}
		out[key] = c
	}
	return out
}

func describe(v any) string { return fmt.Sprintf("%+v", v) }
