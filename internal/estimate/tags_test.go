// The workloads left out by their tags (spec §3, --exclude-tags): any criterion excludes; in one, every key (and), each
// with one of its values (none: any value), as discovery's resourceTags match the tags of a load balancer, target group
// or rule. Ported from manager packages/estimate/test/tags.test.ts.
package estimate

import (
	"reflect"
	"testing"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/stopped"
)

func tagged(name string, tags map[string][]string) discover.Workload {
	return discover.Workload{
		Account: "111111111111", Region: "eu-west-1", LoadBalancer: "web", LoadBalancerArn: "arn:lb", TargetGroup: name, TargetGroupArn: "arn:tg/" + name,
		Hosts: []string{name + ".example.com"}, Backend: discover.WorkloadBackend{Kind: "asg", ID: name}, Stopped: []stopped.Item{}, Tags: tags,
	}
}

func taggedRegion(workloads []discover.Workload) discover.RegionDiscovery {
	return discover.RegionDiscovery{Account: "111111111111", Region: "eu-west-1", Workloads: workloads, Ineligible: []discover.IneligibleRule{}}
}

func taggedWorkloads() []discover.Workload {
	return []discover.Workload{
		tagged("shop", map[string][]string{"env": {"prod"}, "team": {"web"}}), tagged("docs", map[string][]string{"env": {"dev"}, "team": {"web"}}),
		tagged("crm", map[string][]string{"env": {"test"}, "team": {"sales"}}), tagged("legacy", map[string][]string{"dormeto:ignore": {""}}),
		tagged("blog", map[string][]string{}),
	}
}

// tag is a criterion's key with the values it accepts (none: any value).
func tag(key string, values ...string) discover.TagWant {
	if values == nil {
		values = []string{}
	}
	return discover.TagWant{Key: key, Values: values}
}

func kept(criteria ...TagCriterion) []string {
	regions, _ := ExcludeByTags([]discover.RegionDiscovery{taggedRegion(taggedWorkloads())}, criteria)
	return targetGroups(regions[0].Workloads)
}

func targetGroups(workloads []discover.Workload) []string {
	out := []string{}
	for _, w := range workloads {
		out = append(out, w.TargetGroup)
	}
	return out
}

func TestNoneGivenEveryWorkloadKept(t *testing.T) {
	regions, excluded := ExcludeByTags([]discover.RegionDiscovery{taggedRegion(taggedWorkloads())}, []TagCriterion{})
	if !reflect.DeepEqual(regions, []discover.RegionDiscovery{taggedRegion(taggedWorkloads())}) || !reflect.DeepEqual(excluded, []Excluded{}) {
		t.Fatalf("regions %+v, excluded %#v", regions, excluded)
	}
}

func TestAKeyWithOneOfItsValues(t *testing.T) {
	if got, want := kept(TagCriterion{tag("env", "dev", "test")}), []string{"shop", "legacy", "blog"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kept %q, want %q", got, want)
	}
}

func TestEveryKeyOfACriterion(t *testing.T) {
	if got, want := kept(TagCriterion{tag("env", "dev", "prod"), tag("team", "web")}), []string{"crm", "legacy", "blog"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kept %q, want %q", got, want)
	}
	if got, want := kept(TagCriterion{tag("env", "test"), tag("team", "web")}), targetGroups(taggedWorkloads()); !reflect.DeepEqual(got, want) {
		t.Fatalf("kept %q, want %q", got, want)
	}
}

func TestAKeyAloneWhateverItsValue(t *testing.T) {
	if got, want := kept(TagCriterion{tag("dormeto:ignore")}), []string{"shop", "docs", "crm", "blog"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kept %q, want %q", got, want)
	}
}

func TestAnyOfSeveralCriteriaEachWithTheFirstThatMatched(t *testing.T) {
	regions, excluded := ExcludeByTags([]discover.RegionDiscovery{taggedRegion(taggedWorkloads())},
		[]TagCriterion{{tag("env", "dev")}, {tag("team", "sales")}, {tag("dormeto:ignore")}, {tag("team", "web")}})
	if got := targetGroups(regions[0].Workloads); !reflect.DeepEqual(got, []string{"blog"}) {
		t.Fatalf("kept %q", got)
	}
	got := [][2]string{}
	for _, x := range excluded {
		got = append(got, [2]string{x.Workload.TargetGroup, x.Criterion})
	}
	want := [][2]string{{"shop", "team=web"}, {"docs", "env=dev"}, {"crm", "team=sales"}, {"legacy", "dormeto:ignore"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("excluded %q, want %q", got, want)
	}
}

func TestACriterionInTheProductsWords(t *testing.T) {
	if got, want := DescribeTags(TagCriterion{tag("env", "dev", "test"), tag("team", "web"), tag("dormeto:ignore")}), "env=dev|test, team=web, dormeto:ignore"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
