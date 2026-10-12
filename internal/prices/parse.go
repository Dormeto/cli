// The Price List answer's parsing. Ports onDemand of fleet/src/prices.ts.
package prices

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ordered reads a JSON object's values in the order it was written (Node's
// Object.values order for these non-numeric keys).
type ordered[T any] []T

func (o *ordered[T]) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil { // null
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("prices: expected an object")
	}
	for dec.More() {
		if _, err := dec.Token(); err != nil { // the key
			return err
		}
		var v T
		if err := dec.Decode(&v); err != nil {
			return err
		}
		*o = append(*o, v)
	}
	return nil
}

type listProduct struct {
	Terms *struct {
		OnDemand ordered[struct {
			PriceDimensions ordered[struct {
				PricePerUnit struct {
					USD json.RawMessage `json:"USD"`
				} `json:"pricePerUnit"`
			}] `json:"priceDimensions"`
		}] `json:"OnDemand"`
	} `json:"terms"`
}

// number is JavaScript's Number(x) of a JSON value: NaN when it is not a number.
func number(raw json.RawMessage) float64 {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return math.NaN()
	}
	var s string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &s) != nil {
			return math.NaN()
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return 0
		}
	} else {
		s = string(raw)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

// onDemand is the on-demand hourly price in USD of the products answered: each
// product is a JSON string (or an object). Several products of different prices
// are no price (an ambiguous answer is not guessed): nil. A product that is not
// JSON is an error, as JSON.parse throws.
func onDemand(list []json.RawMessage) (*float64, error) {
	var prices []float64
	for _, raw := range list {
		raw = bytes.TrimSpace(raw)
		if len(raw) > 0 && raw[0] == '"' {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, err
			}
			raw = json.RawMessage(s)
		}
		var p listProduct
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.Terms == nil {
			continue
		}
		found := false
		var usd float64
	offers:
		for _, offer := range p.Terms.OnDemand {
			for _, d := range offer.PriceDimensions {
				n := number(d.PricePerUnit.USD)
				if !math.IsNaN(n) && !math.IsInf(n, 0) && n > 0 {
					usd, found = n, true
					break offers
				}
			}
		}
		if found && !contains(prices, usd) {
			prices = append(prices, usd)
		}
	}
	if len(prices) != 1 {
		return nil, nil
	}
	return &prices[0], nil
}

func contains(list []float64, v float64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
