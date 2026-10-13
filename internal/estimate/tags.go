// The workloads left out by their tags (vision docs/specs/2026-10-10-savings-estimate-design.md §3, --exclude-tags),
// after discovery and before the requests are read: any criterion excludes; in one, every key (and), each with one of
// its values (none: any value), matched as discovery's resourceTags against the tags of the workload's load balancer,
// target group or rule.
//
// Ports manager packages/estimate/src/tags.ts (TagCriterion, Excluded, describeTags, excludeByTags).
package estimate

import (
	"strings"

	"github.com/Dormeto/cli/internal/discover"
)

// TagCriterion is one --exclude-tags criterion: its keys in the order given (as Node's object keeps them), each with
// the values it accepts.
type TagCriterion = discover.TagWants

// Excluded is a workload left out by its tags, with the first criterion that matched, in the product's words.
type Excluded struct {
	Workload  discover.Workload `json:"workload"`
	Criterion string            `json:"criterion"`
}

// DescribeTags says a criterion as discovery describes its resourceTags, its keys in order: "env=dev|test, team=web,
// dormeto:ignore".
func DescribeTags(c TagCriterion) string {
	terms := make([]string, len(c))
	for i, t := range c {
		terms[i] = t.Key
		if len(t.Values) > 0 {
			terms[i] = t.Key + "=" + strings.Join(t.Values, "|")
		}
	}
	return strings.Join(terms, ", ")
}

// matching is the first criterion matching the tags.
func matching(criteria []TagCriterion, tags map[string][]string) (TagCriterion, bool) {
	for _, c := range criteria {
		if discover.ResourceTagsMatch(c.Map(), tags) {
			return c, true
		}
	}
	return nil, false
}

// ExcludeByTags keeps the workloads no criterion matches, in copies of the regions; the others are excluded, each with
// the first criterion that matched. No criterion: the regions as given.
func ExcludeByTags(regions []discover.RegionDiscovery, criteria []TagCriterion) ([]discover.RegionDiscovery, []Excluded) {
	excluded := []Excluded{}
	if len(criteria) == 0 {
		return regions, excluded
	}
	kept := make([]discover.RegionDiscovery, len(regions))
	for i, region := range regions {
		workloads := make([]discover.Workload, 0, len(region.Workloads))
		for _, w := range region.Workloads {
			if matched, ok := matching(criteria, w.Tags); ok {
				excluded = append(excluded, Excluded{Workload: w, Criterion: DescribeTags(matched)})
				continue
			}
			workloads = append(workloads, w)
		}
		region.Workloads = workloads
		kept[i] = region
	}
	return kept, excluded
}
