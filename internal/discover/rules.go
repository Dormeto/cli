package discover

// What the scan reads of a listener rule: where it forwards, its conditions
// and host names, and why a rule is no candidate on its own.
//
// Ports manager packages/discovery/src/scan.ts (forwardOf, hostsOf,
// conditionsKey, conditionsOf, REASONS).

import (
	"encoding/json"
	"sort"
	"strings"
)

// forward is where a rule sends traffic: the type of its final action, the
// target groups that receive traffic (weight > 0) and all of them.
type forward struct {
	typ    string // empty: no action
	groups []string
	all    []string
}

func forwardOf(rule Rule) forward {
	actions := append([]Action(nil), rule.Actions...)
	sort.SliceStable(actions, func(i, j int) bool { return actions[i].Order < actions[j].Order })
	var action Action
	if len(actions) > 0 {
		action = actions[len(actions)-1]
	}
	if action.Type != "forward" {
		return forward{typ: action.Type}
	}
	var targets []ForwardTarget
	if action.ForwardConfig != nil && action.ForwardConfig.TargetGroups != nil {
		targets = action.ForwardConfig.TargetGroups
	} else {
		one := 1
		targets = []ForwardTarget{{TargetGroupArn: action.TargetGroupArn, Weight: &one}}
	}
	f := forward{typ: "forward", groups: []string{}, all: []string{}}
	for _, g := range targets {
		f.all = append(f.all, g.TargetGroupArn)
		if g.Weight == nil || *g.Weight > 0 {
			f.groups = append(f.groups, g.TargetGroupArn)
		}
	}
	return f
}

// valuesOf is the values of a condition in either format (the first one set,
// an empty list included), or nil.
func valuesOf(cfg *ValuesConfig, values []string) []string {
	if cfg != nil && cfg.Values != nil {
		return cfg.Values
	}
	return values
}

func hostsOf(rule Rule) []string {
	hosts := []string{}
	for _, c := range rule.Conditions {
		if c.Field == "host-header" {
			hosts = append(hosts, valuesOf(c.HostHeaderConfig, c.Values)...)
		}
	}
	return hosts
}

// conditionsKey: a rule's conditions, whatever their order and their values'
// order: equal for two rules matching the same requests.
func conditionsKey(rule Rule) string {
	keys := make([]string, 0, len(rule.Conditions))
	for _, c := range rule.Conditions {
		field := c.Field
		if field == "" {
			field = "undefined"
		}
		values := valuesOf(c.HostHeaderConfig, nil)
		if values == nil {
			values = valuesOf(c.PathPatternConfig, c.Values)
		}
		if values != nil {
			sorted := append([]string{}, values...)
			sort.Strings(sorted)
			keys = append(keys, field+"="+strings.Join(sorted, "|"))
			continue
		}
		raw, _ := json.Marshal(c)
		keys = append(keys, field+"="+string(raw))
	}
	sort.Strings(keys)
	return strings.Join(keys, "&")
}

func conditionsOf(rule Rule) string {
	if rule.IsDefault {
		return "default"
	}
	parts := make([]string, 0, len(rule.Conditions))
	for _, c := range rule.Conditions {
		switch c.Field {
		case "host-header":
			parts = append(parts, "host "+strings.Join(valuesOf(c.HostHeaderConfig, c.Values), " | "))
		case "path-pattern":
			parts = append(parts, "path "+strings.Join(valuesOf(c.PathPatternConfig, c.Values), " | "))
		default:
			parts = append(parts, c.Field)
		}
	}
	if s := strings.Join(parts, ", "); s != "" {
		return s
	}
	return "any"
}

var reasons = map[string]string{
	"redirect":             "redirect action",
	"fixed-response":       "fixed response",
	"authenticate-oidc":    "authentication only",
	"authenticate-cognito": "authentication only",
}

// reasonOf says why an action of this type is no application: a JS template
// of an absent type reads "undefined action".
func reasonOf(typ string) string {
	if r, ok := reasons[typ]; ok {
		return r
	}
	if typ == "" {
		typ = "undefined"
	}
	return typ + " action"
}
