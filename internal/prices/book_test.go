package prices

// Ports fleet/test/prices.test.ts.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/stopped"
)

// product is a product as the Price List API answers it (a JSON string), with one on-demand term.
func product(usd string, unit ...string) json.RawMessage {
	u := "Hrs"
	if len(unit) > 0 {
		u = unit[0]
	}
	s, _ := json.Marshal(`{"product":{"attributes":{}},"terms":{"OnDemand":{"X.JRTCKXETXF":{"priceDimensions":{"X.JRTCKXETXF.6YS6EN2CT7":{"unit":"` + u + `","pricePerUnit":{"USD":"` + usd + `"}}}}}}}`)
	return s
}

type spotCall struct {
	Region string
	In     SpotHistoryInput
}

type fixture struct {
	book     *Book
	asked    []map[string]string
	cache    map[string]*float64
	days     map[string]int
	spotAsk  []spotCall
	now      time.Time
	spotFunc func(region string, in SpotHistoryInput) []SpotPrice
	answers  func(f map[string]string) []json.RawMessage
	mu       sync.Mutex
}

type fakePricing struct{ f *fixture }

func (p fakePricing) GetProducts(_ context.Context, in GetProductsInput) (GetProductsOutput, error) {
	f := p.f
	filters := map[string]string{"ServiceCode": in.ServiceCode}
	for _, x := range in.Filters {
		k := x.Field
		if x.Type == "CONTAINS" {
			k = "~" + k
		}
		filters[k] = x.Value
	}
	f.asked = append(f.asked, filters)
	return GetProductsOutput{PriceList: f.answers(filters)}, nil
}

type fakeSpot struct{ f *fixture }

func (s fakeSpot) History(_ context.Context, region string, in SpotHistoryInput) (SpotHistoryOutput, error) {
	s.f.spotAsk = append(s.f.spotAsk, spotCall{region, in})
	return SpotHistoryOutput{SpotPriceHistory: s.f.spotFunc(region, in)}, nil
}

type fakeCache struct{ f *fixture }

func (c fakeCache) GetPrice(key string) (*Price, error) {
	v, ok := c.f.cache[key]
	if !ok {
		return nil, nil
	}
	return &Price{USD: v}, nil
}
func (c fakeCache) PutPrice(key string, usd *float64, days int) error {
	c.f.cache[key] = usd
	c.f.days[key] = days
	return nil
}

var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func setup(answers func(map[string]string) []json.RawMessage, spot func(string, SpotHistoryInput) []SpotPrice) *fixture {
	f := &fixture{cache: map[string]*float64{}, days: map[string]int{}, now: t0, answers: answers, spotFunc: spot}
	if spot == nil {
		f.spotFunc = func(string, SpotHistoryInput) []SpotPrice { return nil }
	}
	f.book = NewBook(fakePricing{f}, fakeSpot{f}, fakeCache{f}, func() time.Time { return f.now })
	return f
}

func none(map[string]string) []json.RawMessage { return nil }

func hourly(t *testing.T, b *Book, region string, item stopped.Item, d ...Discounts) *float64 {
	t.Helper()
	disc := Discounts{}
	if len(d) > 0 {
		disc = d[0]
	}
	v, err := b.Hourly(context.Background(), region, item, disc)
	if err != nil {
		t.Fatalf("Hourly(%+v): %v", item, err)
	}
	return v
}

func near(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 1e-6 {
		t.Errorf("got %v, want %v", deref(got), want)
	}
}

func isNil(t *testing.T, got *float64) {
	t.Helper()
	if got != nil {
		t.Errorf("got %v, want nil", *got)
	}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func ec2(typ string, count int, os string) stopped.Item {
	return stopped.Item{Kind: "ec2", Type: typ, Count: count, OS: os}
}

func TestPricesEC2ByTypeRegionAndOSTimesTheCountAsksOnceThenReadsTheCache(t *testing.T) {
	s := setup(func(f map[string]string) []json.RawMessage {
		if f["ServiceCode"] == "AmazonEC2" && f["instanceType"] == "t3.medium" && f["regionCode"] == "eu-west-1" && f["operatingSystem"] == "Linux" {
			return []json.RawMessage{product("0.0456000000")}
		}
		return nil
	}, nil)
	near(t, hourly(t, s.book, "eu-west-1", ec2("t3.medium", 2, "Linux")), 0.0912)
	near(t, hourly(t, s.book, "eu-west-1", ec2("t3.medium", 1, "Linux")), 0.0456)
	if len(s.asked) != 1 {
		t.Fatalf("asked %d times", len(s.asked))
	}
	for k, v := range map[string]string{"tenancy": "Shared", "preInstalledSw": "NA", "capacitystatus": "Used"} {
		if s.asked[0][k] != v {
			t.Errorf("filter %s = %q, want %q", k, s.asked[0][k], v)
		}
	}
}

func fargateAnswers(f map[string]string) []json.RawMessage {
	usd := map[string]string{
		"Fargate-vCPU-Hours:perCPU":     "0.0404800000",
		"Fargate-GB-Hours":              "0.0044450000",
		"Fargate-ARM-vCPU-Hours:perCPU": "0.0323800000",
		"Fargate-ARM-GB-Hours":          "0.0035600000",
	}[f["~usagetype"]]
	if f["ServiceCode"] == "AmazonECS" && usd != "" {
		return []json.RawMessage{product(usd, "hours")}
	}
	return nil
}

func TestPricesFargateByVCPUAndGBHoursX86OrARMTimesTheCount(t *testing.T) {
	s := setup(fargateAnswers, nil)
	near(t, hourly(t, s.book, "eu-west-1", stopped.Item{Kind: "fargate", VCPU: 0.5, MemoryGb: 1, Count: 2, Arch: "x86_64", OS: "Linux"}), 2*(0.5*0.04048+1*0.004445))
	near(t, hourly(t, s.book, "eu-west-1", stopped.Item{Kind: "fargate", VCPU: 1, MemoryGb: 2, Count: 1, Arch: "arm64", OS: "Linux"}), 0.03238+2*0.00356)
}

func TestPricesRDSByClassEngineAndDeploymentAuroraServerlessByACUMultiAZIsItsOwnPrice(t *testing.T) {
	s := setup(func(f map[string]string) []json.RawMessage {
		if f["ServiceCode"] != "AmazonRDS" {
			return nil
		}
		if f["~usagetype"] == "Aurora:ServerlessV2Usage" && f["databaseEngine"] == "Aurora MySQL" {
			return []json.RawMessage{product("0.1400000000", "ACU-Hr")}
		}
		if f["instanceType"] == "db.t4g.micro" && f["databaseEngine"] == "PostgreSQL" {
			if f["deploymentOption"] == "Multi-AZ" {
				return []json.RawMessage{product("0.0350000000")}
			}
			return []json.RawMessage{product("0.0170000000")}
		}
		return nil
	}, nil)
	near(t, hourly(t, s.book, "eu-west-1", stopped.Item{Kind: "rds", Class: "db.t4g.micro", Engine: "postgres"}), 0.017)
	near(t, hourly(t, s.book, "eu-west-1", stopped.Item{Kind: "rds", Class: "db.t4g.micro", Engine: "postgres", MultiAZ: true}), 0.035)
	near(t, hourly(t, s.book, "eu-west-1", stopped.Item{Kind: "aurora-serverless", Engine: "aurora-mysql", ACU: 0.5}), 0.07)
}

func TestCannotPriceAnotherOSOracleAnUnknownItemOrWhatTheAPIDoesNotReturnCachedAsSuch(t *testing.T) {
	s := setup(none, nil)
	isNil(t, hourly(t, s.book, "eu-west-1", ec2("m5.large", 1, "Red Hat Enterprise Linux")))
	isNil(t, hourly(t, s.book, "eu-west-1", stopped.Item{Kind: "rds", Class: "db.m5.large", Engine: "oracle-ee"}))
	isNil(t, hourly(t, s.book, "eu-west-1", stopped.Item{Kind: "unknown", Backend: "ecs:x", Reason: "denied"}))
	if len(s.asked) != 0 {
		t.Fatalf("asked %d times", len(s.asked))
	}
	isNil(t, hourly(t, s.book, "eu-west-1", ec2("x9.huge", 1, "Linux")))
	isNil(t, hourly(t, s.book, "eu-west-1", ec2("x9.huge", 1, "Linux")))
	if len(s.asked) != 1 {
		t.Fatalf("asked %d times", len(s.asked))
	}
}

type throttled struct{ calls int }

func (p *throttled) GetProducts(context.Context, GetProductsInput) (GetProductsOutput, error) {
	p.calls++
	return GetProductsOutput{}, errors.New("Rate exceeded")
}

func TestAThrottledAPIPricesNothingForThatItemWithoutCachingIt(t *testing.T) {
	s := setup(none, nil)
	p := &throttled{}
	b := NewBook(p, nil, fakeCache{s}, func() time.Time { return time.Unix(0, 0) })
	isNil(t, hourly(t, b, "eu-west-1", ec2("t3.medium", 1, "Linux")))
	if p.calls != 1 || len(s.cache) != 0 {
		t.Fatalf("calls = %d, cache = %v", p.calls, s.cache)
	}
}

func TestReadsAProductThatIsAnObjectNotAString(t *testing.T) {
	obj := json.RawMessage(`{"product":{"attributes":{}},"terms":{"OnDemand":{"X":{"priceDimensions":{"Y":{"unit":"Hrs","pricePerUnit":{"USD":"0.0046000000"}}}}}}}`)
	s := setup(func(f map[string]string) []json.RawMessage {
		if f["instanceType"] == "t4g.nano" {
			return []json.RawMessage{obj}
		}
		return nil
	}, nil)
	near(t, hourly(t, s.book, "eu-west-1", ec2("t4g.nano", 1, "Linux")), 0.0046)
}

func TestAsksForAWSRegionsOnlyAndRDSByItsUsageTypeSeveralProductsOfDifferentPricesAreNoPrice(t *testing.T) {
	s := setup(func(f map[string]string) []json.RawMessage {
		if f["ServiceCode"] == "AmazonRDS" {
			return []json.RawMessage{product("0.2900000000"), product("0.3770000000")}
		}
		return []json.RawMessage{product("0.0456000000"), product("0.0456000000")}
	}, nil)
	isNil(t, hourly(t, s.book, "eu-west-1", stopped.Item{Kind: "rds", Class: "db.r6g.large", Engine: "aurora-postgresql"}))
	near(t, hourly(t, s.book, "eu-west-1", ec2("t3.medium", 1, "Linux")), 0.0456)
	for _, a := range s.asked {
		if a["locationType"] != "AWS Region" {
			t.Errorf("locationType = %q in %v", a["locationType"], a)
		}
		if a["ServiceCode"] == "AmazonRDS" && a["~usagetype"] != "InstanceUsage:" {
			t.Errorf("usagetype = %q", a["~usagetype"])
		}
	}
	m := setup(func(map[string]string) []json.RawMessage { return []json.RawMessage{product("0.0350000000")} }, nil)
	hourly(t, m.book, "eu-west-1", stopped.Item{Kind: "rds", Class: "db.t4g.micro", Engine: "postgres", MultiAZ: true})
	if m.asked[0]["~usagetype"] != "Multi-AZUsage:" {
		t.Errorf("usagetype = %q", m.asked[0]["~usagetype"])
	}
}

type countingCache struct {
	reads *[]string
}

func (c countingCache) GetPrice(k string) (*Price, error) {
	*c.reads = append(*c.reads, k)
	return nil, nil
}
func (c countingCache) PutPrice(string, *float64, int) error { return nil }

type flaky struct {
	calls   int
	failing bool
}

func (p *flaky) GetProducts(context.Context, GetProductsInput) (GetProductsOutput, error) {
	p.calls++
	if p.failing {
		return GetProductsOutput{}, errors.New("Rate exceeded")
	}
	return GetProductsOutput{PriceList: []json.RawMessage{product("0.0456000000")}}, nil
}

func TestKeepsPricesInMemorySharesAPriceBeingAskedAndRemembersAFailureForMinutes(t *testing.T) {
	p := &flaky{failing: true}
	now := time.Unix(0, 0)
	var reads []string
	b := NewBook(p, nil, countingCache{&reads}, func() time.Time { return now })
	item := ec2("t3.medium", 1, "Linux")
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); hourly(t, b, "eu-west-1", item) }()
	}
	wg.Wait()
	if p.calls != 1 {
		t.Fatalf("calls = %d, want 1 (a price being asked is shared)", p.calls)
	}
	hourly(t, b, "eu-west-1", item)
	if p.calls != 1 {
		t.Fatalf("calls = %d, want 1 (a failure remembered)", p.calls)
	}
	p.failing = false
	now = now.Add(6 * time.Minute)
	near(t, hourly(t, b, "eu-west-1", item), 0.0456)
	before := len(reads)
	near(t, hourly(t, b, "eu-west-1", item), 0.0456)
	if len(reads) != before {
		t.Errorf("reads grew from %d to %d: want memory, not the table", before, len(reads))
	}
}

// R9.5b: Spot capacity at Spot prices, commitments as discounts.
var spotT = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func ts(d time.Duration) *time.Time { x := spotT.Add(d); return &x }

func TestPricesASpotInstanceAtTheLatestPriceOfEachZoneAveragedTimesTheCountCachedADay(t *testing.T) {
	s := setup(none, func(string, SpotHistoryInput) []SpotPrice {
		return []SpotPrice{
			{AvailabilityZone: "eu-west-1a", SpotPrice: "0.020000", Timestamp: ts(-time.Hour)},
			{AvailabilityZone: "eu-west-1a", SpotPrice: "0.030000", Timestamp: ts(-3 * time.Hour)},
			{AvailabilityZone: "eu-west-1b", SpotPrice: "0.018000", Timestamp: ts(-2 * time.Hour)},
		}
	})
	item := ec2("t3.medium", 2, "Linux")
	item.Spot = true
	near(t, hourly(t, s.book, "eu-west-1", item), 2*0.019)
	want := []spotCall{{"eu-west-1", SpotHistoryInput{InstanceTypes: []string{"t3.medium"}, ProductDescriptions: []string{"Linux/UNIX"}, StartTime: t0}}}
	if !reflect.DeepEqual(s.spotAsk, want) {
		t.Errorf("spot asked %+v, want %+v", s.spotAsk, want)
	}
	key := "eu-west-1#ec2-spot#t3.medium#Linux"
	near(t, s.cache[key], 0.019)
	if s.days[key] != 1 || len(s.asked) != 0 {
		t.Errorf("days = %d, asked = %v", s.days[key], s.asked)
	}
}

func TestAsksWindowsAsWindowsAnotherOSOrNoAnswerIsNoSpotPriceNoneCachedADay(t *testing.T) {
	s := setup(none, func(_ string, in SpotHistoryInput) []SpotPrice {
		if in.ProductDescriptions[0] == "Windows" {
			return []SpotPrice{{AvailabilityZone: "eu-west-1a", SpotPrice: "0.050000", Timestamp: ts(0)}}
		}
		return nil
	})
	spot := func(typ, os string) stopped.Item { i := ec2(typ, 1, os); i.Spot = true; return i }
	near(t, hourly(t, s.book, "eu-west-1", spot("m5.large", "Windows")), 0.05)
	isNil(t, hourly(t, s.book, "eu-west-1", spot("m5.large", "Red Hat Enterprise Linux")))
	isNil(t, hourly(t, s.book, "eu-west-1", spot("x9.huge", "Linux")))
	var got []string
	for _, a := range s.spotAsk {
		got = append(got, a.In.ProductDescriptions[0])
	}
	if !reflect.DeepEqual(got, []string{"Windows", "Linux/UNIX"}) {
		t.Errorf("descriptions = %v", got)
	}
	key := "eu-west-1#ec2-spot#x9.huge#Linux"
	if v, ok := s.cache[key]; !ok || v != nil || s.days[key] != 1 {
		t.Errorf("cache[%s] = %v (present %v), days %d", key, deref(v), ok, s.days[key])
	}
}

type optIn struct{ calls int }

func (p *optIn) History(context.Context, string, SpotHistoryInput) (SpotHistoryOutput, error) {
	p.calls++
	return SpotHistoryOutput{}, errors.New("You are not subscribed to this service")
}

func TestARegionTheAccountHasNotEnabledPricesNothingCachesNothingAskedAgainAfterFiveMinutes(t *testing.T) {
	s := setup(none, nil)
	p := &optIn{}
	b := NewBook(fakePricing{s}, p, fakeCache{s}, func() time.Time { return s.now })
	item := ec2("t3.medium", 1, "Linux")
	item.Spot = true
	isNil(t, hourly(t, b, "ap-east-2", item))
	isNil(t, hourly(t, b, "ap-east-2", item))
	if p.calls != 1 || len(s.cache) != 0 {
		t.Fatalf("calls = %d, cache = %v", p.calls, s.cache)
	}
	s.now = s.now.Add(301 * time.Second)
	hourly(t, b, "ap-east-2", item)
	if p.calls != 2 {
		t.Fatalf("calls = %d, want 2", p.calls)
	}
}

var fargate = stopped.Item{Kind: "fargate", VCPU: 0.5, MemoryGb: 1, Count: 2, Arch: "x86_64", OS: "Linux"}
var fargateUSD = 2 * (0.5*0.04048 + 1*0.004445)

func TestPricesFargateSpotAt30PercentOfTheOnDemandPrice(t *testing.T) {
	s := setup(fargateAnswers, nil)
	spot := fargate
	spot.Spot = true
	near(t, hourly(t, s.book, "eu-west-1", spot), 0.3*fargateUSD)
	near(t, hourly(t, s.book, "eu-west-1", fargate), fargateUSD)
}

func TestTakesTheComputeDiscountOffOnDemandEC2AndFargateTheDatabasesOneOffRDSAndAuroraNoneOffSpot(t *testing.T) {
	s := setup(func(f map[string]string) []json.RawMessage {
		switch f["ServiceCode"] {
		case "AmazonEC2":
			return []json.RawMessage{product("0.0456000000")}
		case "AmazonRDS":
			if f["~usagetype"] == "Aurora:ServerlessV2Usage" {
				return []json.RawMessage{product("0.1400000000")}
			}
			return []json.RawMessage{product("0.0170000000")}
		}
		return fargateAnswers(f)
	}, func(string, SpotHistoryInput) []SpotPrice {
		return []SpotPrice{{AvailabilityZone: "eu-west-1a", SpotPrice: "0.019000", Timestamp: ts(0)}}
	})
	d := Discounts{Compute: 20, Databases: 30}
	e := ec2("t3.medium", 2, "Linux")
	near(t, hourly(t, s.book, "eu-west-1", e, d), 0.8*0.0912)
	near(t, hourly(t, s.book, "eu-west-1", fargate, d), 0.8*fargateUSD)
	near(t, hourly(t, s.book, "eu-west-1", stopped.Item{Kind: "rds", Class: "db.t4g.micro", Engine: "postgres"}, d), 0.7*0.017)
	near(t, hourly(t, s.book, "eu-west-1", stopped.Item{Kind: "aurora-serverless", Engine: "aurora-mysql", ACU: 0.5}, d), 0.7*0.07)
	es := e
	es.Spot = true
	near(t, hourly(t, s.book, "eu-west-1", es, d), 2*0.019)
	fs := fargate
	fs.Spot = true
	near(t, hourly(t, s.book, "eu-west-1", fs, d), 0.3*fargateUSD)
	// The list price stays as it is, in the table and in memory: no discount taken twice.
	near(t, s.cache["eu-west-1#ec2#t3.medium#Linux"], 0.0456)
	near(t, hourly(t, s.book, "eu-west-1", e, d), 0.8*0.0912)
	near(t, hourly(t, s.book, "eu-west-1", e), 0.0912)
}

// Kubernetes workloads.
func pods() stopped.Item {
	n := "m5.large"
	return stopped.Item{Kind: "k8s-pods", Count: 2, CPU: 0.5, MemoryGb: 1, NodeType: &n, OS: "Linux", Autoscaled: true, Allocatable: &stopped.Allocatable{CPU: 1.93, MemoryGb: 6.886}}
}

func m5(f map[string]string) []json.RawMessage {
	if f["ServiceCode"] == "AmazonEC2" && f["instanceType"] == "m5.large" && f["operatingSystem"] == "Linux" {
		return []json.RawMessage{product("0.1070000000")}
	}
	return nil
}

func TestPricesPodsOnNodesAsTheirShareOfTheNode(t *testing.T) {
	s := setup(m5, func(string, SpotHistoryInput) []SpotPrice {
		return []SpotPrice{{AvailabilityZone: "eu-west-1a", SpotPrice: "0.040000", Timestamp: ts(0)}}
	})
	near(t, hourly(t, s.book, "eu-west-1", pods()), 0.107*(0.5/1.93)*2)
	p := pods()
	p.CPU, p.MemoryGb = 0.1, 4
	near(t, hourly(t, s.book, "eu-west-1", p), 0.107*(4/6.886)*2)
	// A pod asking more than its node's allocatable is at most the node.
	p = pods()
	p.CPU = 3
	near(t, hourly(t, s.book, "eu-west-1", p), 0.107*2)
	// Spot at the Spot price, never discounted; on-demand less the compute discount.
	p = pods()
	p.Spot = true
	near(t, hourly(t, s.book, "eu-west-1", p, Discounts{Compute: 20}), 0.04*(0.5/1.93)*2)
	near(t, hourly(t, s.book, "eu-west-1", pods(), Discounts{Compute: 20}), 0.8*0.107*(0.5/1.93)*2)
}

func TestPodsOnNodesNoAutoscalerRemovesSaveNothingWithoutTheNodesTypeOrAllocatableTheyAreNotPriced(t *testing.T) {
	s := setup(m5, nil)
	p := pods()
	p.Autoscaled = false
	if v := hourly(t, s.book, "eu-west-1", p); v == nil || *v != 0 {
		t.Errorf("not autoscaled = %v, want 0", deref(v))
	}
	p = pods()
	p.NodeType = nil
	isNil(t, hourly(t, s.book, "eu-west-1", p))
	p = pods()
	p.Allocatable = &stopped.Allocatable{}
	isNil(t, hourly(t, s.book, "eu-west-1", p))
	p = pods()
	x := "x9.huge"
	p.NodeType = &x
	isNil(t, hourly(t, s.book, "eu-west-1", p))
}

func TestAPriceCachedNullIsReadAsUnpricedNeverAsked(t *testing.T) {
	s := setup(none, nil)
	v := 0.0456
	s.cache["eu-west-1#ec2#t3.medium#Linux"] = &v
	s.cache["eu-west-1#ec2#m5.large#Linux"] = nil
	near(t, hourly(t, s.book, "eu-west-1", ec2("t3.medium", 1, "Linux")), 0.0456)
	isNil(t, hourly(t, s.book, "eu-west-1", ec2("m5.large", 1, "Linux")))
	if len(s.asked) != 0 {
		t.Errorf("asked %v", s.asked)
	}
}

func TestACacheFailureIsReturned(t *testing.T) {
	s := setup(none, nil)
	b := NewBook(fakePricing{s}, nil, brokenCache{}, func() time.Time { return t0 })
	if _, err := b.Hourly(context.Background(), "eu-west-1", ec2("t3.medium", 1, "Linux"), Discounts{}); err == nil || !strings.Contains(err.Error(), "disk") {
		t.Fatalf("err = %v", err)
	}
}

type brokenCache struct{}

func (brokenCache) GetPrice(string) (*Price, error)      { return nil, errors.New("disk full") }
func (brokenCache) PutPrice(string, *float64, int) error { return errors.New("disk full") }
