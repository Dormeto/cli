package replay

// A standalone resource's activity replayed (vision standalone workloads design §7: a napped resource is started
// again when its history next shows activity): each 5-minute point busy, as its five minutes, and the windows tried
// for a standalone workload.
//
// Ports manager packages/fleet/src/activity.ts (STANDALONE_WINDOWS, STANDALONE_RECOMMENDABLE, busyMinutes).

import (
	"math"

	"github.com/Dormeto/cli/internal/metrics"
)

// StandaloneWindows are the windows tried for a standalone workload: 5 minutes to 24 hours (its idle window's range).
var StandaloneWindows = []int{5, 10, 15, 20, 30, 45, 60, 90, 120, 180, 240, 360, 480, 720, 1440}

// StandaloneRecommendable are the windows the knee is chosen from, 5 minutes to the built-in 2 hours: each longer
// window only avoids the stops of longer quiet times (nights, weekends), so the knee alone would choose a day, awake
// through every night; they are shown to compare.
var StandaloneRecommendable = []int{5, 10, 15, 20, 30, 45, 60, 90, 120}

const (
	pointMs      = 5 * minMs
	pointMinutes = 5
)

// BusyMinutes are the busy points from from to to, as their five minutes each (the advisor's requests): a point is
// busy while the resource was awake (fully inside one of the awake periods) and a signal that applies is at or over
// its threshold or has no value; while it was asleep (stopped), a point says nothing. awake: every time but its naps.
func BusyMinutes(signals []metrics.PlannedSignal, values []metrics.SignalValue, from, to string, awake []Period) RequestMinutes {
	out := RequestMinutes{From: from, To: to, Minutes: [][2]int{}}
	fromMs, err1 := parseISO(from)
	toMs, err2 := parseISO(to)
	if err1 != nil || err2 != nil {
		return out
	}
	count := int(math.Floor(float64(toMs-fromMs)/pointMs + 0.5))
	type period struct{ a, b int64 }
	var periods []period
	for _, p := range awake {
		a, err1 := parseISO(p.From)
		b, err2 := parseISO(p.To)
		if err1 == nil && err2 == nil {
			periods = append(periods, period{a, b})
		}
	}
	wasAwake := func(t int64) bool {
		for _, p := range periods {
			if t >= p.a && t+pointMs <= p.b {
				return true
			}
		}
		return false
	}
	below := map[string]*float64{}
	for _, s := range signals {
		if s.Applies {
			below[s.Signal] = s.Below
		}
	}
	for p := 0; p < count; p++ {
		if !wasAwake(fromMs + int64(p)*pointMs) {
			continue
		}
		busy := false
		for _, s := range values {
			if p >= len(s.Values) {
				continue // as Node's undefined: neither null nor at its threshold
			}
			if s.Values[p] == nil {
				busy = true
			} else if b := below[s.Signal]; b != nil && *s.Values[p] >= *b {
				busy = true
			}
		}
		if busy {
			for m := 0; m < pointMinutes; m++ {
				out.Minutes = append(out.Minutes, [2]int{p*pointMinutes + m, 1})
			}
		}
	}
	return out
}
