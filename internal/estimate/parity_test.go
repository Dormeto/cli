// Go only: the estimate of the shared `estimate` topic's first case (the world: every account and region, --uptime,
// --exclude-tags env=dev), made from what the Node estimate read in that case and kept in its cache
// (testdata/parity/cache: the discoveries, the requests, the uptime, the prices): the report, but its run time and
// Dormeto's cost by part (Go's, with Dormeto installed: the same total), is Node's, numbers to 1e-9. The end-to-end
// case (the command over the recorded answers) is the command's test.
package estimate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/accounts"
	"github.com/Dormeto/cli/internal/cache"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/options"
	"github.com/Dormeto/cli/internal/parity"
	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/stopped"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
)

var worldCache = filepath.Join("..", "..", "testdata", "parity", "cache", "o-a1b2c3d4e5")

// cached reads a cache file's value; false when there is none.
func cached(t *testing.T, path string, out any) bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(worldCache, path))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Value, out); err != nil {
		t.Fatal(err)
	}
	return true
}

// worldPorts: the organization of the world, its third account refusing the role.
type worldPorts struct{ refusal string }

func (worldPorts) Whoami(context.Context) (string, error)       { return "111111111111", nil }
func (worldPorts) Organization(context.Context) (string, error) { return "o-a1b2c3d4e5", nil }
func (worldPorts) Inventory(context.Context) (discover.OrgInventory, error) {
	return discover.OrgInventory{Accounts: []discover.Account{{ID: "111111111111", Name: "management"}, {ID: "222222222222", Name: "shop"}, {ID: "333333333333", Name: "legacy"}}}, nil
}
func (p worldPorts) Assume(_ context.Context, account, _ string) (aws.Credentials, error) {
	if account == "333333333333" {
		return aws.Credentials{}, &smithy.GenericAPIError{Code: "AccessDenied", Message: p.refusal}
	}
	return aws.Credentials{AccessKeyID: "AKIA", SecretAccessKey: "secret"}, nil
}

// noPrices: every price is in the cache; a call to AWS fails the test.
type noPrices struct{ t *testing.T }

func (p noPrices) GetProducts(_ context.Context, in prices.GetProductsInput) (prices.GetProductsOutput, error) {
	p.t.Errorf("a price not in the cache: %+v", in)
	return prices.GetProductsOutput{}, errors.New("not in the cache")
}

func (p noPrices) History(_ context.Context, region string, in prices.SpotHistoryInput) (prices.SpotHistoryOutput, error) {
	p.t.Errorf("a Spot price not in the cache: %s %+v", region, in)
	return prices.SpotHistoryOutput{}, errors.New("not in the cache")
}

func TestTheWorldsEstimateAsNodes(t *testing.T) {
	c := parity.Load(t, "estimate").Cases[0]
	var input struct {
		Argv []string `json:"argv"`
		Now  string   `json:"now"`
	}
	var output struct {
		Report map[string]json.RawMessage `json:"report"`
	}
	if err := json.Unmarshal(c.Input, &input); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(c.Output, &output); err != nil {
		t.Fatal(err)
	}
	o, err := options.Parse(input.Argv)
	if err != nil {
		t.Fatal(err)
	}
	now, err := parseISO(input.Now)
	if err != nil {
		t.Fatal(err)
	}
	// The accounts reached: the third refuses its role when its turn comes.
	var unreachable []accounts.Unreachable
	if err := json.Unmarshal(output.Report["unreachable"], &unreachable); err != nil || len(unreachable) != 1 {
		t.Fatalf("unreachable %s: %v", output.Report["unreachable"], err)
	}
	reached, err := accounts.Reach(context.Background(), worldPorts{refusal: unreachable[0].Reason}, accounts.Options{Role: o.Role, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reached.Accounts[2].Credentials(context.Background()); err == nil {
		t.Fatal("the third account's role assumed")
	}
	// Each reached account's regions, in order: discovered (cached), or failed as the report says (never cached).
	var failed []FailedRegion
	if err := json.Unmarshal(output.Report["failedRegions"], &failed); err != nil {
		t.Fatal(err)
	}
	var discovered []discover.RegionDiscovery
	var requests []metrics.RegionRequests
	var uptime []metrics.RegionUptime
	for _, a := range reached.Accounts {
		var regions []string
		if !cached(t, filepath.Join("regions", a.Account+".json"), &regions) {
			continue
		}
		for _, region := range regions {
			name := a.Account + "-" + region + ".json"
			var d discover.RegionDiscovery
			if !cached(t, filepath.Join("discovery", name), &d) {
				i := slices.IndexFunc(failed, func(f FailedRegion) bool { return f.Account == a.Account && f.Region == region })
				if i < 0 {
					t.Fatalf("%s: neither discovered nor failed", name)
				}
				d = discover.RegionDiscovery{Account: a.Account, Region: region, Workloads: []discover.Workload{}, Ineligible: []discover.IneligibleRule{}, Error: failed[i].Error}
			}
			discovered = append(discovered, d)
			var r metrics.RegionRequests
			if cached(t, filepath.Join("requests", name), &r) {
				requests = append(requests, r)
			}
			var u metrics.RegionUptime
			if cached(t, filepath.Join("uptime", name), &u) {
				uptime = append(uptime, u)
			}
		}
	}
	regions, excluded := ExcludeByTags(discovered, o.ExcludeTags)
	// The prices Node read, from its prices.json (copied: the price book writes there).
	raw, err := os.ReadFile(filepath.Join(worldCache, "prices.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prices.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	book := prices.NewBook(noPrices{t}, noPrices{t}, cache.New(dir, clock).Prices(), clock)
	e, err := All(context.Background(), Input{
		Reached: reached, Regions: regions, Requests: requests,
		Hourly: func(ctx context.Context, region string, item stopped.Item) (*float64, error) {
			return book.Hourly(ctx, region, item, o.Discounts)
		},
		Baseline: o.Baseline, Options: o, Now: now, Excluded: excluded, Uptime: uptime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.GeneratedAt != input.Now {
		t.Fatalf("generatedAt %s", e.GeneratedAt)
	}
	got, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(got, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "generatedAt") // the report's run times are removed
	// Go only (the user, 2026-10-13): Dormeto installed, its cost is the installation now (portal, the world's one
	// onboarded workload, estimated) and the recommendation's change: the same total as Node's.
	var node DormetoCost
	if err := json.Unmarshal(output.Report["dormeto"], &node); err != nil {
		t.Fatal(err)
	}
	portal := find(t, e.Workloads, "portal").Workload
	if !portal.Managed || node.Workloads.Count < 2 || node.Routers.Count < 1 {
		t.Fatalf("portal %+v, Node's cost %+v", portal, node)
	}
	parts(t, e, []Where{{portal.Account, portal.Region}}, 1)
	changed(t, e, Change{Onboard: node.Workloads.Count - 1, RoutersAdded: node.Routers.Count - 1,
		USDMonth: float64(node.Workloads.Count-1)*cost.Workload + float64(node.Routers.Count-1)*cost.Router})
	closeTo(t, "dormeto.usdMonth", e.Dormeto.USDMonth, node.USDMonth, 9)
	delete(fields, "dormeto")
	delete(output.Report, "dormeto")
	got, _ = json.Marshal(fields)
	want, _ := json.Marshal(output.Report)
	parity.Equal(t, want, got)
	// Node's key order (parity.Equal ignores it): the accounts' and the regions' as they came.
	if keys := e.ByAccount.Keys(); !slices.Equal(keys, []string{"111111111111", "222222222222"}) {
		t.Fatalf("byAccount %q", keys)
	}
	if keys := e.AccountNames.Keys(); !slices.Equal(keys, []string{"111111111111", "222222222222", "333333333333"}) {
		t.Fatalf("accountNames %q", keys)
	}
}
