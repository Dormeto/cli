package fleet

import (
	"reflect"
	"strings"
	"testing"
)

var fleetRows = []Router{
	{Name: "alpha", Account: "111", Region: "eu-west-1"},
	{Name: "beta", Account: "222", Region: "eu-west-1"},
	{Name: "gamma", Account: "111", Region: "us-east-1"},
	{Name: "alpha-eu", Region: "eu-west-3"}, // the manager's own account
	{Name: "delta", Account: "333", Region: "us-east-1"},
}

func names(rs []Router) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return out
}

func TestSelectOrsWithinAFilterAndAndsAcrossThem(t *testing.T) {
	got, err := Selection{Accounts: []string{"111", "222"}, RouterRegions: []string{"eu-west-1", "us-east-1"}}.Select(fleetRows, nil)
	if err != nil || !reflect.DeepEqual(names(got), []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("got %v, %v", names(got), err)
	}
	got, _ = Selection{Accounts: []string{"111"}, RouterRegions: []string{"us-east-1"}}.Select(fleetRows, nil)
	if !reflect.DeepEqual(names(got), []string{"gamma"}) {
		t.Errorf("accounts AND regions: %v", names(got))
	}
}

func TestSelectGlobOnTheName(t *testing.T) {
	got, err := Selection{NameGlob: "alpha*"}.Select(fleetRows, nil)
	if err != nil || !reflect.DeepEqual(names(got), []string{"alpha", "alpha-eu"}) {
		t.Fatalf("got %v, %v", names(got), err)
	}
	got, _ = Selection{NameGlob: "?eta"}.Select(fleetRows, nil)
	if !reflect.DeepEqual(names(got), []string{"beta"}) {
		t.Errorf("? glob: %v", names(got))
	}
}

func TestSelectByTheRoutersOfTaggedWorkloads(t *testing.T) {
	sel := Selection{Tags: map[string]string{"team": "payments"}}
	got, _ := sel.Select(fleetRows, map[string]bool{"beta": true, "delta": true})
	if !reflect.DeepEqual(names(got), []string{"beta", "delta"}) {
		t.Errorf("tagged: %v", names(got))
	}
	if got, _ = sel.Select(fleetRows, nil); len(got) != 0 {
		t.Errorf("no tagged workload selects %v", names(got))
	}
	// Without tags, the set is ignored.
	if got, _ = (Selection{}).Select(fleetRows, map[string]bool{"beta": true}); len(got) != 5 {
		t.Errorf("no tag filter: %v", names(got))
	}
}

func TestSelectNamesNarrowedByFiltersAndOnce(t *testing.T) {
	got, err := Selection{Names: []string{"gamma", "alpha", "gamma"}, NameGlob: "*a"}.Select(fleetRows, nil)
	if err != nil || !reflect.DeepEqual(names(got), []string{"alpha", "gamma"}) {
		t.Fatalf("got %v, %v", names(got), err)
	}
	got, _ = Selection{Names: []string{"alpha"}, NameGlob: "alpha*"}.Select(fleetRows, nil)
	if !reflect.DeepEqual(names(got), []string{"alpha"}) {
		t.Errorf("named and matched: %v", names(got))
	}
}

func TestSelectUnknownName(t *testing.T) {
	_, err := Selection{Names: []string{"alpha", "nope"}}.Select(fleetRows, nil)
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("err = %v", err)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		sel     Selection
		upgrade bool
		want    string // "" for valid
	}{
		{"list takes nothing", Selection{}, false, ""},
		{"upgrade needs something", Selection{}, true, "name routers, filter them or use --all"},
		{"upgrade by name", Selection{Names: []string{"a"}}, true, ""},
		{"upgrade by filter", Selection{Accounts: []string{"1"}}, true, ""},
		{"upgrade all", Selection{All: true}, true, ""},
		{"all with a name", Selection{All: true, Names: []string{"a"}}, true, "--all takes no router names nor filters"},
		{"all with a filter", Selection{All: true, NameGlob: "a*"}, true, "--all takes no router names nor filters"},
		{"all with a tag", Selection{All: true, Tags: map[string]string{"a": "b"}}, true, "--all takes no router names nor filters"},
		{"bad glob", Selection{NameGlob: "[a"}, false, `--name: bad pattern "[a"`},
	}
	for _, c := range cases {
		err := c.sel.Validate(c.upgrade)
		if (err == nil) != (c.want == "") || (err != nil && err.Error() != c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

func TestParseTags(t *testing.T) {
	got, err := ParseTags([]string{"team=payments", "expr=a=b", "empty="})
	if err != nil || !reflect.DeepEqual(got, map[string]string{"team": "payments", "expr": "a=b", "empty": ""}) {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"team", "=v", "a=b,c", " "} {
		if _, err := ParseTags([]string{bad}); err == nil || !strings.Contains(err.Error(), "--tag") || !strings.Contains(err.Error(), bad) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := ParseTags([]string{"a=1", "a=2"}); err == nil || !strings.Contains(err.Error(), `"a"`) {
		t.Errorf("twice: %v", err)
	}
}
