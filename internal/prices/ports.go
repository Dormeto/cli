// Ports of the price book: the Price List API, EC2's Spot price history and
// the price cache. Ports fleet/src/prices.ts (PricingPort, SpotPort,
// PriceCache) and api/src/spot.ts (the Spot port's shape). Adapters over the
// AWS SDK live elsewhere; this package imports none.
package prices

import (
	"context"
	"encoding/json"
	"time"
)

// Filter is a Price List filter.
type Filter struct {
	Type  string `json:"Type"`
	Field string `json:"Field"`
	Value string `json:"Value"`
}

// GetProductsInput is the Price List API's GetProducts input.
type GetProductsInput struct {
	ServiceCode string   `json:"ServiceCode"`
	Filters     []Filter `json:"Filters"`
	MaxResults  int      `json:"MaxResults,omitempty"`
}

// GetProductsOutput is its answer: each product as a JSON string (or an object).
type GetProductsOutput struct {
	PriceList []json.RawMessage `json:"PriceList,omitempty"`
}

// PricingPort is the Price List API (GetProducts).
type PricingPort interface {
	GetProducts(ctx context.Context, in GetProductsInput) (GetProductsOutput, error)
}

// SpotHistoryInput is DescribeSpotPriceHistory's input as the price book asks.
type SpotHistoryInput struct {
	InstanceTypes       []string  `json:"InstanceTypes"`
	ProductDescriptions []string  `json:"ProductDescriptions"`
	StartTime           time.Time `json:"StartTime"`
}

// MarshalJSON writes StartTime as Node's toISOString does
// (2026-10-12T08:00:00.000Z), the form recorded inputs are compared in.
func (in SpotHistoryInput) MarshalJSON() ([]byte, error) {
	type plain struct {
		InstanceTypes       []string `json:"InstanceTypes"`
		ProductDescriptions []string `json:"ProductDescriptions"`
		StartTime           string   `json:"StartTime"`
	}
	return json.Marshal(plain{in.InstanceTypes, in.ProductDescriptions, in.StartTime.UTC().Format("2006-01-02T15:04:05.000Z")})
}

// SpotPrice is one entry of the Spot price history.
type SpotPrice struct {
	AvailabilityZone string     `json:"AvailabilityZone,omitempty"`
	SpotPrice        string     `json:"SpotPrice,omitempty"`
	Timestamp        *time.Time `json:"Timestamp,omitempty"`
}

// SpotHistoryOutput is DescribeSpotPriceHistory's answer.
type SpotHistoryOutput struct {
	SpotPriceHistory []SpotPrice `json:"SpotPriceHistory,omitempty"`
}

// SpotPort is EC2's Spot price history in a region (from the manager's account).
type SpotPort interface {
	History(ctx context.Context, region string, in SpotHistoryInput) (SpotHistoryOutput, error)
}

// Price is a cached price: USD nil when the API has none (a null).
type Price struct {
	USD *float64 `json:"usd"`
}

// Cache keeps a price per key, nil USD when the API has none, for days.
// GetPrice returns nil for a key not cached (or expired).
type Cache interface {
	GetPrice(key string) (*Price, error)
	PutPrice(key string, usd *float64, days int) error
}
