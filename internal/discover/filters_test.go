package discover

// The cases of manager packages/discovery/test/filters.test.ts.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

const (
	root = "r-1u0b"
	ou1  = "ou-1u0b-aaaaaaa1"
	ou2  = "ou-1u0b-aaaaaaa2"
	ou3  = "ou-1u0b-aaaaaaa3"
	ou4  = "ou-1u0b-aaaaaaa4"
	ou5  = "ou-1u0b-aaaaaaa5"
	prod = "ou-1u0b-pppppppp"
)

var (
	accA = Account{ID: "111111111111", Name: "tools", OUPath: []string{root}, Tags: map[string]string{}}
	accB = Account{ID: "222222222222", Name: "deep", OUPath: []string{root, ou1, ou2, ou3, ou4, ou5}, Tags: map[string]string{"env": "dev"}}
	accC = Account{ID: "333333333333", Name: "shop-prod", OUPath: []string{root, prod}, Tags: map[string]string{"env": "prod", "team": "shop"}}
)

func parent(s string) *string { return &s }

var inventory = OrgInventory{
	Accounts: []Account{accA, accB, accC},
	OUs: map[string]OU{
		root: {Name: "Root"}, ou1: {Name: "one", Parent: parent(root)}, ou2: {Name: "two", Parent: parent(ou1)},
		ou3: {Name: "three", Parent: parent(ou2)}, ou4: {Name: "four", Parent: parent(ou3)}, ou5: {Name: "five", Parent: parent(ou4)},
		prod: {Name: "prod", Parent: parent(root)},
	},
}

func str(s string) *string { return &s }

func rule(mod ...func(*ScannedRule)) ScannedRule {
	r := ScannedRule{
		ID: "arn:aws:elasticloadbalancing:eu-west-1:111111111111:listener-rule/app/web-shop/1/2/3", Account: accA.ID, Region: "eu-west-1",
		LoadBalancer: "web-shop", LoadBalancerArn: "arn:aws:elasticloadbalancing:eu-west-1:111111111111:loadbalancer/app/web-shop/1",
		Listener: "HTTPS:443", Priority: "10", Conditions: "host shop.example.com", Hosts: []string{"shop.example.com"},
		TargetGroup: str("shop"), TargetGroupArn: str("arn:aws:elasticloadbalancing:eu-west-1:111111111111:targetgroup/shop/1"),
		Backend: &Backend{Kind: "instance", ID: "i-1"}, Tags: map[string][]string{"env": {"preview"}}, Rules: []RuleRef{},
		Eligibility: Eligible,
	}
	for _, m := range mod {
		m(&r)
	}
	return r
}

// crit reads a criterion from JSON, as the configuration gives it.
func crit(t *testing.T, s string) Criterion {
	t.Helper()
	var c Criterion
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func config(over ...func(*Config)) Config {
	c := Config{Version: 1, Mode: "organization", Regions: []string{"eu-west-1", "eu-west-3"}, RefreshHours: 24}
	for _, o := range over {
		o(&c)
	}
	return c
}

func filter(t *testing.T, name string, include []string, exclude ...string) Filter {
	f := Filter{Name: name, Exclude: []Criterion{}}
	for _, s := range include {
		f.Include = append(f.Include, crit(t, s))
	}
	for _, s := range exclude {
		f.Exclude = append(f.Exclude, crit(t, s))
	}
	return f
}

func TestMatches(t *testing.T) {
	m := func(t *testing.T, c string, opts ...any) bool {
		acc, r := accA, rule()
		for _, o := range opts {
			switch v := o.(type) {
			case Account:
				acc = v
				r.Account = v.ID
			case ScannedRule:
				r = v
			}
		}
		return Matches(crit(t, c), acc, r, inventory)
	}
	is := func(t *testing.T, want bool, got bool) {
		t.Helper()
		if want != got {
			t.Errorf("got %v, want %v", got, want)
		}
	}
	t.Run("an empty criterion matches everything", func(t *testing.T) { is(t, true, m(t, `{}`)) })
	t.Run("account IDs, values being alternatives", func(t *testing.T) {
		is(t, true, m(t, fmt.Sprintf(`{"accountIds":["%s","%s"]}`, accA.ID, accC.ID)))
		is(t, false, m(t, fmt.Sprintf(`{"accountIds":["%s"]}`, accC.ID)))
	})
	t.Run("an OU recursively, five levels deep, and the root for every account", func(t *testing.T) {
		is(t, true, m(t, `{"ouIds":["`+ou1+`"]}`, accB))
		is(t, true, m(t, `{"ouIds":["`+ou5+`"]}`, accB))
		is(t, false, m(t, `{"ouIds":["`+ou1+`"]}`, accC))
		is(t, true, m(t, `{"ouIds":["`+root+`"]}`, accC))
	})
	t.Run("account tags with given values, or any value with an empty list", func(t *testing.T) {
		is(t, true, m(t, `{"accountTags":{"env":["prod","staging"]}}`, accC))
		is(t, false, m(t, `{"accountTags":{"env":["dev"]}}`, accC))
		is(t, true, m(t, `{"accountTags":{"team":[]}}`, accC))
		is(t, false, m(t, `{"accountTags":{"team":[]}}`, accB))
	})
	t.Run("every dimension it sets", func(t *testing.T) {
		is(t, true, m(t, `{"accountIds":["`+accA.ID+`"],"regions":["eu-west-1"]}`))
		is(t, false, m(t, `{"accountIds":["`+accA.ID+`"],"regions":["eu-west-3"]}`))
	})
	t.Run("load balancers by name pattern, exact name or full ARN", func(t *testing.T) {
		is(t, true, m(t, `{"loadBalancers":["web-*"]}`))
		is(t, true, m(t, `{"loadBalancers":["web-shop"]}`))
		is(t, false, m(t, `{"loadBalancers":["web"]}`))
		is(t, true, m(t, `{"loadBalancers":["`+rule().LoadBalancerArn+`"]}`))
		is(t, false, m(t, `{"loadBalancers":["arn:aws:elasticloadbalancing:eu-west-1:111111111111:loadbalancer/app/other/1"]}`))
	})
	t.Run("host patterns against the rule host names; a rule without host condition never matches", func(t *testing.T) {
		is(t, true, m(t, `{"hosts":["*.example.com"]}`))
		is(t, true, m(t, `{"hosts":["SHOP.example.com"]}`))
		is(t, false, m(t, `{"hosts":["*.example.org"]}`))
		is(t, false, m(t, `{"hosts":["*"]}`, rule(func(r *ScannedRule) { r.Hosts = []string{} })))
	})
	t.Run("resource tags of the load balancer, rule or target group (merged by the scan)", func(t *testing.T) {
		is(t, true, m(t, `{"resourceTags":{"env":["preview"]}}`))
		is(t, true, m(t, `{"resourceTags":{"env":[]}}`))
		is(t, false, m(t, `{"resourceTags":{"env":["prod"]}}`))
	})
	t.Run("a tag value of any of the three resources, so a load balancer tag cannot be overridden by the rule", func(t *testing.T) {
		prodLB := rule(func(r *ScannedRule) { r.Tags = map[string][]string{"env": {"prod", "preview"}} })
		is(t, true, m(t, `{"resourceTags":{"env":["prod"]}}`, prodLB))
		is(t, false, m(t, `{"resourceTags":{"constructor":[]}}`))
	})
	t.Run("backend kinds, never for an ineligible rule", func(t *testing.T) {
		is(t, true, m(t, `{"backendKinds":["instance","asg"]}`))
		is(t, false, m(t, `{"backendKinds":["ecs"]}`))
		is(t, false, m(t, `{"backendKinds":["instance"]}`, rule(func(r *ScannedRule) { r.Eligibility, r.Backend, r.Reason = Ineligible, nil, "fixed response" })))
	})
}

func TestDecide(t *testing.T) {
	decide := func(cfg Config) Decision { return Decide(cfg, accA, rule(), inventory) }
	t.Run("a global exclude wins over any filter, with its note in the reason", func(t *testing.T) {
		cfg := config(func(c *Config) {
			c.GlobalExcludes = []Criterion{crit(t, `{"note":"tools account","accountIds":["`+accA.ID+`"]}`)}
			c.Filters = []Filter{filter(t, "all", []string{`{}`})}
		})
		want := Decision{"excluded", `excluded: global exclude 1 "tools account" (account ` + accA.ID + `)`}
		if got := decide(cfg); got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
	t.Run("a filter exclude applies to that filter only", func(t *testing.T) {
		cfg := config(func(c *Config) {
			c.Filters = []Filter{
				filter(t, "dev", []string{`{"accountIds":["` + accA.ID + `"]}`}, `{"backendKinds":["instance"]}`),
				filter(t, "preview", []string{`{"resourceTags":{"env":["preview"]}}`}),
			}
		})
		if got, want := decide(cfg), (Decision{"in", `in scope: filter "preview", include 1`}); got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
	t.Run("the filter and exclude number when every including filter excludes the rule", func(t *testing.T) {
		cfg := config(func(c *Config) {
			c.Filters = []Filter{filter(t, "dev", []string{`{"ouIds":["` + ou1 + `"]}`, `{"accountIds":["` + accA.ID + `"]}`}, `{"regions":["eu-west-3"]}`, `{"backendKinds":["instance"]}`)}
		})
		if got, want := decide(cfg), (Decision{"excluded", `excluded: filter "dev", exclude 2`}); got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
	t.Run("out of scope when no filter includes the rule", func(t *testing.T) {
		cfg := config(func(c *Config) { c.Filters = []Filter{filter(t, "prod", []string{`{"ouIds":["` + prod + `"]}`})} })
		if got, want := decide(cfg), (Decision{"out", "out of scope: no filter includes it"}); got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
	t.Run("the include that brought the rule in scope", func(t *testing.T) {
		cfg := config(func(c *Config) {
			c.Filters = []Filter{filter(t, "dev", []string{`{"ouIds":["` + prod + `"]}`, `{"hosts":["*.example.com"]}`})}
		})
		if got, want := decide(cfg), (Decision{"in", `in scope: filter "dev", include 2`}); got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

func scanned[P any](plans []P, id func(P) (string, bool)) []string {
	out := []string{}
	for _, p := range plans {
		if s, ok := id(p); ok {
			out = append(out, s)
		}
	}
	return out
}

func accountsScanned(plans []AccountPlan) []string {
	return scanned(plans, func(p AccountPlan) (string, bool) { return p.Account, p.Scan })
}

func pairsScanned(plans []PairPlan) []string {
	return scanned(plans, func(p PairPlan) (string, bool) { return p.Account + "/" + p.Region, p.Scan })
}

func planOf(plans []AccountPlan, id string) AccountPlan {
	for _, p := range plans {
		if p.Account == id {
			return p
		}
	}
	return AccountPlan{}
}

func TestScanAccountsAndTargetPairs(t *testing.T) {
	regions := map[string][]string{accA.ID: {"eu-west-1", "us-east-1"}, accB.ID: {"eu-west-1"}, accC.ID: {"eu-west-1"}}
	eq := func(t *testing.T, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	}
	t.Run("nothing scanned without filters", func(t *testing.T) {
		eq(t, accountsScanned(ScanAccounts(config(), inventory)), []string{})
		eq(t, pairsScanned(TargetPairs(config(), inventory, regions)), []string{})
	})
	t.Run("an account no filter includes, with the reason", func(t *testing.T) {
		plans := ScanAccounts(config(func(c *Config) { c.Filters = []Filter{filter(t, "a", []string{`{"accountIds":["` + accA.ID + `"]}`})} }), inventory)
		eq(t, accountsScanned(plans), []string{accA.ID})
		eq(t, planOf(plans, accB.ID), AccountPlan{accB.ID, false, "no filter includes this account"})
	})
	t.Run("a globally excluded account, by ID or through its OU, even when a filter names it", func(t *testing.T) {
		byID := config(func(c *Config) {
			c.GlobalExcludes = []Criterion{crit(t, `{"accountIds":["`+accA.ID+`"]}`)}
			c.Filters = []Filter{filter(t, "a", []string{`{"accountIds":["` + accA.ID + `","` + accB.ID + `"]}`})}
		})
		eq(t, accountsScanned(ScanAccounts(byID, inventory)), []string{accB.ID})
		byOU := config(func(c *Config) {
			c.GlobalExcludes = []Criterion{crit(t, `{"note":"deep OUs","ouIds":["`+ou1+`"]}`)}
			c.Filters = []Filter{filter(t, "b", []string{`{"accountIds":["` + accB.ID + `"]}`})}
		})
		plans := ScanAccounts(byOU, inventory)
		eq(t, accountsScanned(plans), []string{})
		eq(t, planOf(plans, accB.ID).Reason, `excluded: global exclude 1 "deep OUs" (OU `+ou1+`)`)
	})
	t.Run("an account whose includable regions are all globally excluded, explicit regions or [*]", func(t *testing.T) {
		explicit := config(func(c *Config) {
			c.GlobalExcludes = []Criterion{crit(t, `{"regions":["eu-west-3"]}`)}
			c.Filters = []Filter{filter(t, "b", []string{`{"accountIds":["` + accB.ID + `"],"regions":["eu-west-3"]}`})}
		})
		eq(t, planOf(ScanAccounts(explicit, inventory), accB.ID), AccountPlan{accB.ID, false, "no region of the perimeter left to scan"})
		star := explicit
		star.Regions = []string{"*"}
		eq(t, planOf(ScanAccounts(star, inventory), accB.ID).Scan, false)
		allRegions := config(func(c *Config) {
			c.GlobalExcludes = []Criterion{crit(t, `{"accountIds":["`+accA.ID+`"],"regions":["eu-west-1","eu-west-3"]}`)}
			c.Filters = []Filter{filter(t, "a", []string{`{"accountIds":["` + accA.ID + `"]}`})}
		})
		eq(t, planOf(ScanAccounts(allRegions, inventory), accA.ID).Scan, false)
		open := config(func(c *Config) {
			c.Regions = []string{"*"}
			c.GlobalExcludes = []Criterion{crit(t, `{"regions":["eu-west-3"]}`)}
			c.Filters = []Filter{filter(t, "b", []string{`{"accountIds":["` + accB.ID + `"]}`})}
		})
		eq(t, planOf(ScanAccounts(open, inventory), accB.ID).Scan, true)
	})
	t.Run("a globally excluded region in every account", func(t *testing.T) {
		cfg := config(func(c *Config) {
			c.GlobalExcludes = []Criterion{crit(t, `{"regions":["eu-west-3"]}`)}
			c.Filters = []Filter{filter(t, "all", []string{`{"ouIds":["` + root + `"]}`})}
		})
		eq(t, pairsScanned(TargetPairs(cfg, inventory, regions)), []string{accA.ID + "/eu-west-1", accB.ID + "/eu-west-1", accC.ID + "/eu-west-1"})
	})
	t.Run("the pair kept when a global exclude sets a resource dimension", func(t *testing.T) {
		cfg := config(func(c *Config) {
			c.GlobalExcludes = []Criterion{crit(t, `{"accountIds":["`+accA.ID+`"],"backendKinds":["ecs"]}`)}
			c.Filters = []Filter{filter(t, "a", []string{`{"accountIds":["` + accA.ID + `"]}`})}
		})
		eq(t, pairsScanned(TargetPairs(cfg, inventory, regions)), []string{accA.ID + "/eu-west-1", accA.ID + "/eu-west-3"})
	})
	t.Run("regions no include allows", func(t *testing.T) {
		cfg := config(func(c *Config) {
			c.Filters = []Filter{filter(t, "a", []string{`{"accountIds":["` + accA.ID + `"],"regions":["eu-west-3"]}`})}
		})
		plans := TargetPairs(cfg, inventory, regions)
		eq(t, pairsScanned(plans), []string{accA.ID + "/eu-west-3"})
		for _, p := range plans {
			if p.Account == accA.ID && p.Region == "eu-west-1" {
				eq(t, p.Reason, "no filter includes this region")
			}
		}
	})
	t.Run("the enabled regions of each account with regions [*]", func(t *testing.T) {
		cfg := config(func(c *Config) {
			c.Regions = []string{"*"}
			c.Filters = []Filter{filter(t, "a", []string{`{"accountIds":["` + accA.ID + `"]}`})}
		})
		eq(t, pairsScanned(TargetPairs(cfg, inventory, regions)), []string{accA.ID + "/eu-west-1", accA.ID + "/us-east-1"})
	})
}

func TestDescribeCriterion(t *testing.T) {
	for in, want := range map[string]string{
		`{}`:                   "everything",
		`{"accountIds":["1"]}`: "account 1",
		`{"accountIds":["1","2"],"regions":["eu-west-1"]}`:                                         "accounts 1, 2, region eu-west-1",
		`{"accountTags":{"env":["a","b"],"team":[]},"resourceTags":{"k":["v"]}}`:                   "account tags env=a|b, team, tags k=v",
		`{"targetGroups":["arn:aws:elasticloadbalancing:eu-west-1:1:targetgroup/shop/1","api-*"]}`: "target groups shop, api-*",
		`{"loadBalancers":["a"],"hosts":["h","i"],"backendKinds":["ecs"],"ouIds":["ou-1"]}`:        "OU ou-1, load balancer a, hosts h, i, backend ecs",
	} {
		if got := DescribeCriterion(crit(t, in)); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

func TestResourceTagsMatchAndGlob(t *testing.T) {
	if !ResourceTagsMatch(map[string][]string{"env": {"dev", "test"}, "team": {}}, map[string][]string{"env": {"test"}, "team": {"web"}}) {
		t.Error("every key, one of its values")
	}
	if ResourceTagsMatch(map[string][]string{"env": {}}, map[string][]string{}) {
		t.Error("a key missing")
	}
	if !ResourceTagsMatch(map[string][]string{}, map[string][]string{"a": {"b"}}) {
		t.Error("no criterion")
	}
	for _, c := range []struct {
		p, v string
		want bool
	}{{"web-*", "WEB-shop", true}, {"a.b", "axb", false}, {"*", "", true}, {"a*c*", "abcd", true}, {"a(b)", "a(b)", true}, {"x", "xx", false}} {
		if GlobMatches(c.p, c.v) != c.want {
			t.Errorf("GlobMatches(%q, %q) != %v", c.p, c.v, c.want)
		}
	}
}
