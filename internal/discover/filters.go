package discover

// Perimeter evaluation (spec section 4): criteria, global excludes first, then
// filters (one including filter is enough, a filter's excludes only apply to
// that filter), and the account-region pairs worth scanning.
//
// Ports manager packages/discovery/src/filters.ts.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// globMatches: a glob with * wildcards, case-insensitive (host and load
// balancer names are).
func globMatches(pattern, value string) bool {
	parts := strings.Split(pattern, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re, err := regexp.Compile("(?i)^" + strings.Join(parts, ".*") + "$")
	return err == nil && re.MatchString(value)
}

// GlobMatches reports whether value matches a pattern with * wildcards,
// ignoring case.
func GlobMatches(pattern, value string) bool { return globMatches(pattern, value) }

// tagsMatch: every wanted key on the account, with one of the values (none: any).
func tagsMatch(wanted TagWants, tags map[string]string) bool {
	for _, w := range wanted {
		v, ok := tags[w.Key]
		if !ok || (len(w.Values) > 0 && !slices.Contains(w.Values, v)) {
			return false
		}
	}
	return true
}

// ResourceTagsMatch: each wanted key on the load balancer, the target group or
// the rule, with one of the values (none: any value).
func ResourceTagsMatch(wanted, tags map[string][]string) bool {
	for key, values := range wanted {
		have, ok := tags[key]
		if !ok {
			return false
		}
		if len(values) == 0 {
			continue
		}
		if !slices.ContainsFunc(have, func(v string) bool { return slices.Contains(values, v) }) {
			return false
		}
	}
	return true
}

// matchesAccount looks at the account dimensions only: account IDs, OUs
// (recursive, the root matches every account) and account tags.
func matchesAccount(c Criterion, a Account) bool {
	if c.AccountIDs != nil && !slices.Contains(c.AccountIDs, a.ID) {
		return false
	}
	if c.OUIDs != nil && !slices.ContainsFunc(c.OUIDs, func(ou string) bool { return slices.Contains(a.OUPath, ou) }) {
		return false
	}
	if c.AccountTags != nil && !tagsMatch(c.AccountTags, a.Tags) {
		return false
	}
	return true
}

func matchesRegion(c Criterion, region string) bool {
	return c.Regions == nil || slices.Contains(c.Regions, region)
}

// Matches reports whether the criterion matches a scanned rule of an account:
// every dimension it sets must match.
func Matches(c Criterion, a Account, r ScannedRule, _ OrgInventory) bool {
	if !matchesAccount(c, a) || !matchesRegion(c, r.Region) {
		return false
	}
	if c.LoadBalancers != nil && !slices.ContainsFunc(c.LoadBalancers, func(p string) bool {
		if strings.HasPrefix(p, "arn:") {
			return p == r.LoadBalancerArn
		}
		return globMatches(p, r.LoadBalancer)
	}) {
		return false
	}
	if c.Hosts != nil && !slices.ContainsFunc(c.Hosts, func(p string) bool {
		return slices.ContainsFunc(r.Hosts, func(h string) bool { return globMatches(p, h) })
	}) {
		return false
	}
	if c.ResourceTags != nil && !ResourceTagsMatch(c.ResourceTags.Map(), r.Tags) {
		return false
	}
	if c.BackendKinds != nil && !(r.Backend != nil && slices.Contains(c.BackendKinds, r.Backend.Kind)) {
		return false
	}
	if c.TargetGroups != nil && !slices.ContainsFunc(c.TargetGroups, func(p string) bool {
		if strings.HasPrefix(p, "arn:") {
			return r.TargetGroupArn != nil && p == *r.TargetGroupArn
		}
		return r.TargetGroup != nil && globMatches(p, *r.TargetGroup)
	}) {
		return false
	}
	return true
}

func plural(one, many string, values []string) string {
	word := one
	if len(values) > 1 {
		word = many
	}
	return word + " " + strings.Join(values, ", ")
}

// targetGroupName is the name in a target group ARN
// (arn:...:targetgroup/<name>/<id>), or the pattern itself.
func targetGroupName(p string) string {
	if !strings.HasPrefix(p, "arn:") {
		return p
	}
	parts := strings.Split(p, ":")
	if name := strings.Split(parts[len(parts)-1], "/"); len(name) > 1 {
		return name[1]
	}
	return p
}

func tagText(tags TagWants) string {
	parts := make([]string, len(tags))
	for i, t := range tags {
		parts[i] = t.Key
		if len(t.Values) > 0 {
			parts[i] += "=" + strings.Join(t.Values, "|")
		}
	}
	return strings.Join(parts, ", ")
}

// DescribeCriterion says what the criterion sets ("account 111111111111,
// region eu-west-1"), for reasons and lint messages.
func DescribeCriterion(c Criterion) string {
	var parts []string
	if c.AccountIDs != nil {
		parts = append(parts, plural("account", "accounts", c.AccountIDs))
	}
	if c.OUIDs != nil {
		parts = append(parts, plural("OU", "OUs", c.OUIDs))
	}
	if c.AccountTags != nil {
		parts = append(parts, "account tags "+tagText(c.AccountTags))
	}
	if c.Regions != nil {
		parts = append(parts, plural("region", "regions", c.Regions))
	}
	if c.LoadBalancers != nil {
		parts = append(parts, plural("load balancer", "load balancers", c.LoadBalancers))
	}
	if c.Hosts != nil {
		parts = append(parts, plural("host", "hosts", c.Hosts))
	}
	if c.ResourceTags != nil {
		parts = append(parts, "tags "+tagText(c.ResourceTags))
	}
	if c.BackendKinds != nil {
		parts = append(parts, plural("backend", "backends", c.BackendKinds))
	}
	if c.TargetGroups != nil {
		names := make([]string, len(c.TargetGroups))
		for i, p := range c.TargetGroups {
			names[i] = targetGroupName(p)
		}
		parts = append(parts, plural("target group", "target groups", names))
	}
	if len(parts) == 0 {
		return "everything"
	}
	return strings.Join(parts, ", ")
}

func globalReason(index int, c Criterion) string {
	if c.Label != "" {
		return "excluded: " + c.Label
	}
	note := ""
	if c.Note != "" {
		note = ` "` + c.Note + `"`
	}
	return fmt.Sprintf("excluded: global exclude %d%s (%s)", index+1, note, DescribeCriterion(c))
}

// Decide says whether a rule is in the perimeter: global excludes win, then
// any filter including it without excluding it.
func Decide(cfg Config, a Account, r ScannedRule, inv OrgInventory) Decision {
	if i := slices.IndexFunc(cfg.GlobalExcludes, func(c Criterion) bool { return Matches(c, a, r, inv) }); i >= 0 {
		return Decision{"excluded", globalReason(i, cfg.GlobalExcludes[i])}
	}
	excludedBy := ""
	for _, f := range cfg.Filters {
		include := slices.IndexFunc(f.Include, func(c Criterion) bool { return Matches(c, a, r, inv) })
		if include < 0 {
			continue
		}
		exclude := slices.IndexFunc(f.Exclude, func(c Criterion) bool { return Matches(c, a, r, inv) })
		if exclude < 0 {
			return Decision{"in", fmt.Sprintf(`in scope: filter "%s", include %d`, f.Name, include+1)}
		}
		if excludedBy == "" {
			excludedBy = fmt.Sprintf(`excluded: filter "%s", exclude %d`, f.Name, exclude+1)
		}
	}
	if excludedBy != "" {
		return Decision{"excluded", excludedBy}
	}
	return Decision{"out", "out of scope: no filter includes it"}
}

// setsResource: whether the criterion sets a resource dimension.
func setsResource(c Criterion) bool {
	return c.LoadBalancers != nil || c.Hosts != nil || c.ResourceTags != nil || c.BackendKinds != nil || c.TargetGroups != nil
}

// A global exclude that sets only account dimensions removes the whole account
// before any call.
func excludesAccount(c Criterion) bool { return c.Regions == nil && !setsResource(c) }

// A global exclude without resource dimension removes the account-region pair
// before any call.
func excludesPair(c Criterion) bool { return !setsResource(c) }

func includes(cfg Config) []Criterion {
	var out []Criterion
	for _, f := range cfg.Filters {
		out = append(out, f.Include...)
	}
	return out
}

// globalAccountExclude is the index of the global exclude that removes the
// whole account before any call, or -1.
func globalAccountExclude(cfg Config, a Account) int {
	return slices.IndexFunc(cfg.GlobalExcludes, func(c Criterion) bool { return excludesAccount(c) && matchesAccount(c, a) })
}

// globalPairExclude is the index of the global exclude that removes the
// account-region pair before any call, or -1.
func globalPairExclude(cfg Config, a Account, region string) int {
	return slices.IndexFunc(cfg.GlobalExcludes, func(c Criterion) bool {
		return excludesPair(c) && matchesAccount(c, a) && matchesRegion(c, region)
	})
}

// ScanAccounts says, for each account of the inventory, whether it is scanned.
func ScanAccounts(cfg Config, inv OrgInventory) []AccountPlan {
	plans := make([]AccountPlan, 0, len(inv.Accounts))
	for _, a := range inv.Accounts {
		plans = append(plans, accountPlan(cfg, a))
	}
	return plans
}

func accountPlan(cfg Config, a Account) AccountPlan {
	if g := globalAccountExclude(cfg, a); g >= 0 {
		return AccountPlan{a.ID, false, globalReason(g, cfg.GlobalExcludes[g])}
	}
	fi := slices.IndexFunc(cfg.Filters, func(f Filter) bool {
		return slices.ContainsFunc(f.Include, func(c Criterion) bool { return matchesAccount(c, a) })
	})
	if fi < 0 {
		return AccountPlan{a.ID, false, "no filter includes this account"}
	}
	// Regions the includes can reach in this account; when they are known and
	// all globally excluded, no pair is left and the account's role is not even
	// assumed.
	var matching []Criterion
	for _, c := range includes(cfg) {
		if matchesAccount(c, a) {
			matching = append(matching, c)
		}
	}
	var reachable []string
	known := true
	if slices.Contains(cfg.Regions, "*") {
		for _, c := range matching {
			if c.Regions == nil {
				known = false
				break
			}
			for _, r := range c.Regions {
				if !slices.Contains(reachable, r) {
					reachable = append(reachable, r)
				}
			}
		}
	} else {
		for _, r := range cfg.Regions {
			if slices.ContainsFunc(matching, func(c Criterion) bool { return matchesRegion(c, r) }) {
				reachable = append(reachable, r)
			}
		}
	}
	if known && !slices.ContainsFunc(reachable, func(r string) bool { return globalPairExclude(cfg, a, r) < 0 }) {
		return AccountPlan{a.ID, false, "no region of the perimeter left to scan"}
	}
	return AccountPlan{a.ID, true, fmt.Sprintf(`included by filter "%s"`, cfg.Filters[fi].Name)}
}

// TargetPairs says, for each account and region, whether it is scanned;
// enabled gives each account's enabled regions, for the regions ["*"].
func TargetPairs(cfg Config, inv OrgInventory, enabled map[string][]string) []PairPlan {
	accounts := map[string]Account{}
	for _, a := range inv.Accounts {
		accounts[a.ID] = a
	}
	all := slices.Contains(cfg.Regions, "*")
	pairs := []PairPlan{}
	for _, plan := range ScanAccounts(cfg, inv) {
		if !plan.Scan {
			regions := cfg.Regions
			if all {
				regions = []string{"*"}
			}
			for _, r := range regions {
				pairs = append(pairs, PairPlan{plan.Account, r, false, plan.Reason})
			}
			continue
		}
		a := accounts[plan.Account]
		regions := cfg.Regions
		if all {
			regions = enabled[plan.Account]
		}
		for _, r := range regions {
			if g := globalPairExclude(cfg, a, r); g >= 0 {
				pairs = append(pairs, PairPlan{plan.Account, r, false, globalReason(g, cfg.GlobalExcludes[g])})
				continue
			}
			included := slices.ContainsFunc(includes(cfg), func(c Criterion) bool { return matchesAccount(c, a) && matchesRegion(c, r) })
			reason := "no filter includes this region"
			if included {
				reason = plan.Reason
			}
			pairs = append(pairs, PairPlan{plan.Account, r, included, reason})
		}
	}
	return pairs
}
