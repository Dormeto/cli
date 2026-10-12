// A period's estimated savings: each ASLEEP phase's hours within the baseline
// times the hourly list prices of what its nap stopped. What cannot be priced
// is listed with its hours, never counted as nothing saved. Ports
// fleet/src/savings.ts.
package prices

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Dormeto/cli/internal/stopped"
)

// Cost is Dormeto's estimated cost a month by part.
type Cost struct {
	Manager  float64 `json:"manager"`
	Router   float64 `json:"router"`
	Workload float64 `json:"workload"`
}

// DormetoCost is Dormeto's estimated cost a month (vision docs/architecture/costs.md): the manager, a router per account
// and region with workloads, each workload; the savings job and the estimate before install net it off.
var DormetoCost = Cost{Manager: 5, Router: 0.26, Workload: 0.13}

// Baseline is when the workload would have run anyway: always, or weekdays 08:00 to 20:00 in a time zone.
type Baseline struct {
	Kind     string `json:"kind"` // "always" or "business-hours"
	TimeZone string `json:"timeZone,omitempty"`
}

// Phase is a period of one state of a workload's history (fleet/src/history.ts), with what its nap stopped.
// Stopped nil is a phase not described; an empty one stops nothing.
type Phase struct {
	State   string         `json:"state"`
	From    string         `json:"from"`
	To      string         `json:"to"`
	Stopped []stopped.Item `json:"stopped,omitempty"`
}

// PricedItem is an item with its hourly price and the hours it was stopped.
type PricedItem struct {
	Item   stopped.Item `json:"item"`
	Hourly float64      `json:"hourly"`
	Hours  float64      `json:"hours"`
	USD    float64      `json:"usd"`
}

// Unpriced is what could not be priced, with its hours.
type Unpriced struct {
	Reason string        `json:"reason"`
	Item   *stopped.Item `json:"item,omitempty"`
	Hours  float64       `json:"hours"`
}

// Savings is a period's estimated savings.
type Savings struct {
	USD      float64      `json:"usd"`
	Hours    float64      `json:"hours"` // asleep hours within the baseline
	Items    []PricedItem `json:"items"`
	Unpriced []Unpriced   `json:"unpriced"`
}

const (
	hourMs        = 3_600_000
	businessStart = 8
	businessEnd   = 20
)

var zones sync.Map // IANA name -> *time.Location

func location(name string) (*time.Location, bool) {
	if l, ok := zones.Load(name); ok {
		return l.(*time.Location), true
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return nil, false
	}
	zones.Store(name, l)
	return l, true
}

// offset is the offset of a time zone at an instant (ms to add to UTC).
func offset(t int64, loc *time.Location) int64 {
	_, secs := time.UnixMilli(t).In(loc).Zone()
	return int64(secs) * 1000
}

// instant is the instant of a local wall time in a time zone.
func instant(y int, m time.Month, d, h int, loc *time.Location) int64 {
	wall := time.Date(y, m, d, h, 0, 0, 0, time.UTC).UnixMilli()
	first := wall - offset(wall, loc)
	return wall - offset(first, loc)
}

func parseMs(s string) (int64, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

// BaselineHours is the hours of [from, to] the baseline would have run. A
// period that is not valid is 0 hours; a time zone that is not an IANA name is
// an error (Node's Intl throws a RangeError), where the period is not empty.
func BaselineHours(from, to string, b Baseline) (float64, error) {
	a, okA := parseMs(from)
	e, okB := parseMs(to)
	if !okA || !okB || !(e > a) {
		return 0, nil
	}
	if b.Kind == "always" {
		return float64(e-a) / hourMs, nil
	}
	loc, ok := location(b.TimeZone)
	if !ok {
		return 0, fmt.Errorf("prices: unknown time zone %q", b.TimeZone)
	}
	var total int64
	y, m, d := time.UnixMilli(a).In(loc).Date()
	// Each local day from the period's first, its business window clipped to the period.
	for i := 0; ; i++ {
		day := time.Date(y, m, d+i, 0, 0, 0, 0, time.UTC)
		dy, dm, dd := day.Date()
		start := instant(dy, dm, dd, businessStart, loc)
		if start >= e {
			break
		}
		if wd := day.Weekday(); wd == time.Sunday || wd == time.Saturday {
			continue
		}
		end := instant(dy, dm, dd, businessEnd, loc)
		total += max(0, min(end, e)-max(start, a))
	}
	return float64(total) / hourMs, nil
}

// SavingsOf prices each ASLEEP phase's hours within the baseline by what it
// stopped, through hourly (nil: not priced).
func SavingsOf(ctx context.Context, phases []Phase, hourly func(context.Context, stopped.Item) (*float64, error), baseline Baseline) (Savings, error) {
	items := map[string]int{}
	unpriced := map[string]int{}
	out := Savings{Items: []PricedItem{}, Unpriced: []Unpriced{}}
	for _, phase := range phases {
		if phase.State != "ASLEEP" {
			continue
		}
		h, err := BaselineHours(phase.From, phase.To, baseline)
		if err != nil {
			return Savings{}, err
		}
		if h == 0 {
			continue
		}
		out.Hours += h
		if phase.Stopped == nil {
			i, ok := unpriced["none"]
			if !ok {
				i = len(out.Unpriced)
				unpriced["none"] = i
				out.Unpriced = append(out.Unpriced, Unpriced{Reason: "not described (a router before 0.27.0)"})
			}
			out.Unpriced[i].Hours += h
			continue
		}
		for _, item := range phase.Stopped {
			kb, err := json.Marshal(item)
			if err != nil {
				return Savings{}, err
			}
			key := string(kb)
			price, err := hourly(ctx, item)
			if err != nil {
				return Savings{}, err
			}
			if price == nil {
				i, ok := unpriced[key]
				if !ok {
					i = len(out.Unpriced)
					unpriced[key] = i
					it := item
					out.Unpriced = append(out.Unpriced, Unpriced{Reason: "not priced", Item: &it})
				}
				out.Unpriced[i].Hours += h
				continue
			}
			i, ok := items[key]
			if !ok {
				i = len(out.Items)
				items[key] = i
				out.Items = append(out.Items, PricedItem{Item: item, Hourly: *price})
			}
			out.Items[i].Hours += h
			out.Items[i].USD += float64(*price * h)
		}
	}
	for _, i := range out.Items {
		out.USD += i.USD
	}
	return out, nil
}
