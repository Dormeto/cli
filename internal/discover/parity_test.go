package discover

// Replays the shared `scan` cases (testdata/parity/scan.json) of the filters
// and the organization inventory.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Dormeto/cli/internal/parity"
)

type recordedOrg struct{ a *parity.Answers }

func (r recordedOrg) ListRoots(_ context.Context, in ListRootsInput) (ListRootsOutput, error) {
	var out ListRootsOutput
	err := r.a.Call("organizations", "listRoots", in, &out)
	return out, err
}

func (r recordedOrg) ListOrganizationalUnitsForParent(_ context.Context, in ListOrganizationalUnitsForParentInput) (ListOrganizationalUnitsForParentOutput, error) {
	var out ListOrganizationalUnitsForParentOutput
	err := r.a.Call("organizations", "listOrganizationalUnitsForParent", in, &out)
	return out, err
}

func (r recordedOrg) ListAccountsForParent(_ context.Context, in ListAccountsForParentInput) (ListAccountsForParentOutput, error) {
	var out ListAccountsForParentOutput
	err := r.a.Call("organizations", "listAccountsForParent", in, &out)
	return out, err
}

func (r recordedOrg) ListTagsForResource(_ context.Context, in ListTagsForResourceInput) (ListTagsForResourceOutput, error) {
	var out ListTagsForResourceOutput
	err := r.a.Call("organizations", "listTagsForResource", in, &out)
	return out, err
}

func TestSharedCasesOfTheFiltersAndTheInventory(t *testing.T) {
	ran := map[string]int{}
	for _, c := range parity.Load(t, "scan").Cases {
		var in struct {
			Function       string              `json:"function"`
			Criterion      Criterion           `json:"criterion"`
			Config         Config              `json:"config"`
			Account        Account             `json:"account"`
			Rule           ScannedRule         `json:"rule"`
			Inventory      OrgInventory        `json:"inventory"`
			EnabledRegions map[string][]string `json:"enabledRegions"`
			Wanted         map[string][]string `json:"wanted"`
			Tags           json.RawMessage     `json:"tags"`
			Answers        json.RawMessage     `json:"answers"`
		}
		if err := json.Unmarshal(c.Input, &in); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		var got any
		switch in.Function {
		case "matches":
			got = Matches(in.Criterion, in.Account, in.Rule, in.Inventory)
		case "decide":
			got = Decide(in.Config, in.Account, in.Rule, in.Inventory)
		case "scanAccounts":
			got = ScanAccounts(in.Config, in.Inventory)
		case "targetPairs":
			got = TargetPairs(in.Config, in.Inventory, in.EnabledRegions)
		case "resourceTagsMatch":
			var tags map[string][]string
			if err := json.Unmarshal(in.Tags, &tags); err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
			got = ResourceTagsMatch(in.Wanted, tags)
		case "loadInventory":
			var withTags bool
			var raw struct {
				Tags bool `json:"tags"`
			}
			if err := json.Unmarshal(c.Input, &raw); err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
			withTags = raw.Tags
			inv, err := LoadInventory(context.Background(), recordedOrg{parity.Recorded(t, in.Answers)}, withTags)
			if err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
			got = inv
		default:
			continue
		}
		ran[in.Function]++
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		t.Run(c.Name, func(t *testing.T) { parity.Equal(t, c.Output, b) })
	}
	want := map[string]int{"matches": 33, "decide": 6, "scanAccounts": 12, "targetPairs": 12, "resourceTagsMatch": 3, "loadInventory": 2}
	for fn, n := range want {
		if ran[fn] != n {
			t.Errorf("%s: ran %d shared cases, the corpus has %d", fn, ran[fn], n)
		}
	}
	if len(ran) != len(want) {
		t.Errorf("ran %v", ran)
	}
}
