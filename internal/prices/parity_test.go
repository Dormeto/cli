package prices

// Replays the shared `prices` cases (testdata/parity/prices.json).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/parity"
	"github.com/Dormeto/cli/internal/stopped"
)

type recordedPricing struct{ a *parity.Answers }

func (r recordedPricing) GetProducts(_ context.Context, in GetProductsInput) (GetProductsOutput, error) {
	var out GetProductsOutput
	err := r.a.Call("pricing", "getProducts", in, &out)
	return out, err
}

type recordedSpot struct{ a *parity.Answers }

func (r recordedSpot) History(_ context.Context, region string, in SpotHistoryInput) (SpotHistoryOutput, error) {
	var out SpotHistoryOutput
	err := r.a.Call("spot", "history", map[string]any{"region": region, "input": in}, &out)
	return out, err
}

type putCall struct {
	USD  *float64 `json:"usd"`
	Days int      `json:"days"`
}

// caseCache holds input.cache and records every price put.
type caseCache struct {
	prices map[string]*float64
	puts   map[string]putCall
}

func (c *caseCache) GetPrice(key string) (*Price, error) {
	v, ok := c.prices[key]
	if !ok {
		return nil, nil
	}
	return &Price{USD: v}, nil
}

func (c *caseCache) PutPrice(key string, usd *float64, days int) error {
	c.prices[key] = usd
	c.puts[key] = putCall{usd, days}
	return nil
}

type lookup struct {
	Region    string       `json:"region"`
	Item      stopped.Item `json:"item"`
	Discounts Discounts    `json:"discounts"`
}

type pricedItem struct {
	Item stopped.Item `json:"item"`
	USD  *float64     `json:"usd"`
}

func TestSharedCases(t *testing.T) {
	topic := parity.Load(t, "prices")
	if len(topic.Cases) == 0 {
		t.Fatal("no shared cases")
	}
	for _, c := range topic.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var in struct {
				Function string              `json:"function"`
				Now      time.Time           `json:"now"`
				Cache    map[string]*float64 `json:"cache"`
				Lookups  []lookup            `json:"lookups"`
				Answers  json.RawMessage     `json:"answers"`
				From, To string
				Baseline Baseline     `json:"baseline"`
				Phases   []Phase      `json:"phases"`
				Prices   []pricedItem `json:"prices"`
			}
			if err := json.Unmarshal(c.Input, &in); err != nil {
				t.Fatal(err)
			}
			var got any
			switch in.Function {
			case "hourly":
				answers := parity.Recorded(t, in.Answers)
				cache := &caseCache{prices: in.Cache, puts: map[string]putCall{}}
				if cache.prices == nil {
					cache.prices = map[string]*float64{}
				}
				book := NewBook(recordedPricing{answers}, recordedSpot{answers}, cache, func() time.Time { return in.Now })
				prices := []*float64{}
				for _, l := range in.Lookups {
					usd, err := book.Hourly(context.Background(), l.Region, l.Item, l.Discounts)
					if err != nil {
						t.Fatalf("%+v: %v", l, err)
					}
					prices = append(prices, usd)
				}
				got = map[string]any{"prices": prices, "cache": cache.puts}
			case "baselineHours":
				got = BaselineHours(in.From, in.To, in.Baseline)
			case "savingsOf":
				hourly := func(_ context.Context, item stopped.Item) (*float64, error) {
					want, _ := json.Marshal(item)
					for _, p := range in.Prices {
						have, _ := json.Marshal(p.Item)
						if string(want) == string(have) {
							return p.USD, nil
						}
					}
					t.Errorf("no price listed for %s", want)
					return nil, nil
				}
				s, err := SavingsOf(context.Background(), in.Phases, hourly, in.Baseline)
				if err != nil {
					t.Fatal(err)
				}
				got = s
			case "DORMETO_COST":
				got = DormetoCost
			default:
				t.Fatalf("unknown function %q", in.Function)
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			parity.Equal(t, c.Output, b)
		})
	}
	if !strings.HasPrefix(topic.Manager, "0.") {
		t.Errorf("manager = %q", topic.Manager)
	}
}
