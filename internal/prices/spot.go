// The Spot price of a type. Ports spotAverage of fleet/src/prices.ts (the
// Spot port's adapter, api/src/spot.ts createSpotPort, is built over the AWS
// SDK elsewhere).
package prices

import (
	"math"
	"strconv"
	"strings"
)

// spotAverage is the latest price of each zone, averaged (zone names differ
// between accounts); none is nil.
func spotAverage(history []SpotPrice) *float64 {
	type zone struct {
		usd float64
		at  int64
	}
	latest := map[string]zone{}
	var order []string
	for _, h := range history {
		usd, err := strconv.ParseFloat(strings.TrimSpace(h.SpotPrice), 64)
		var at int64
		if h.Timestamp != nil {
			at = h.Timestamp.UnixMilli()
		}
		if h.AvailabilityZone == "" || err != nil || math.IsInf(usd, 0) || math.IsNaN(usd) || usd <= 0 {
			continue
		}
		known, ok := latest[h.AvailabilityZone]
		if !ok {
			order = append(order, h.AvailabilityZone)
		}
		if !ok || at > known.at {
			latest[h.AvailabilityZone] = zone{usd, at}
		}
	}
	if len(order) == 0 {
		return nil
	}
	sum := 0.0
	for _, name := range order {
		sum += latest[name].usd
	}
	avg := sum / float64(len(order))
	return &avg
}
