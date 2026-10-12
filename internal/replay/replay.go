// The idle window replay: a workload's requests per minute replayed against idle windows with the router's rules.
// Ports manager packages/fleet/src/idle.ts (IDLE_WINDOWS, windowsFor, checkMinutesOf, replayIdle, recommendWindow).
package replay

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// IdleWindows are the nine windows tried, in minutes.
var IdleWindows = []int{5, 10, 15, 20, 30, 45, 60, 90, 120}

// RequestMinutes are the minutes with requests, as offsets from From with their counts.
type RequestMinutes struct {
	From    string   `json:"from"`
	To      string   `json:"to"`
	Minutes [][2]int `json:"minutes"`
}

// Period is a time span, both ends as ISO dates.
type Period struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ReplayedWindow is one window's replay.
type ReplayedWindow struct {
	Minutes int      `json:"minutes"`
	Asleep  []Period `json:"asleep"`
	Wakes   int      `json:"wakes"`
	WokeAt  []string `json:"wokeAt"`
}

// ReplayInput is replayIdle's argument.
type ReplayInput struct {
	Requests     RequestMinutes `json:"requests"`
	Windows      []int          `json:"windows"`
	CheckMinutes int            `json:"checkMinutes"`
	WakeSeconds  float64        `json:"wakeSeconds"`
	StartAsleep  bool           `json:"startAsleep"`
}

// WakeRow is a window with its wakes.
type WakeRow struct {
	Minutes int     `json:"minutes"`
	Wakes   float64 `json:"wakes"`
}

const (
	minMs = 60_000
	// The knee: the shortest window that already avoids this share of the wakes the longest one avoids.
	knee = 0.9
)

// Windows returns the windows tried: the nine, and the current one when it is not among them.
func Windows(current int) []int {
	out := append([]int{}, IdleWindows...)
	if !slices.Contains(out, current) {
		out = append(out, current)
	}
	slices.Sort(out)
	return out
}

var rate = regexp.MustCompile(`rate\((\d+) (minute|minutes|hour|hours)\)`)

// CheckMinutes returns a router's check period from its schedule: rate(n minutes) or rate(n hours), 5 minutes
// otherwise (nil schedule included).
func CheckMinutes(schedule *string) int {
	if schedule == nil {
		return 5
	}
	m := rate.FindStringSubmatch(*schedule)
	if m == nil {
		return 5
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 5
	}
	if strings.HasPrefix(m[2], "hour") {
		return n * 60
	}
	return n
}

// parseISO reads an ISO date as milliseconds since the epoch.
func parseISO(s string) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, fmt.Errorf("invalid date %q", s)
	}
	return t.UnixMilli(), nil
}

// isoOf formats milliseconds as Node's toISOString().
func isoOf(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// replayOne replays one window: naps at the first check a window after both the last request's minute and the wake;
// a request minute starting at or after that check comes after the nap (a check reads only the minutes that have
// ended) and wakes it; requests during a wake are activity. Times in milliseconds from the period's start.
func replayOne(starts []int, end float64, window int, checkMs, wakeMs float64, startAsleep bool) (asleep [][2]float64, wokeAt []float64) {
	windowMs := float64(window) * minMs
	awake := !startAsleep
	since, last, sleptAt := 0.0, 0.0, 0.0
	napAt := func() float64 { return math.Ceil(math.Max(since+windowMs, last+windowMs)/checkMs) * checkMs }
	for _, minute := range starts {
		t := float64(minute) * minMs
		if awake && t >= napAt() {
			sleptAt = napAt()
			awake = false
		}
		if !awake {
			// A nap and a wake at the same check leave no asleep time; the wake counts.
			if t > sleptAt {
				asleep = append(asleep, [2]float64{sleptAt, t})
			}
			wokeAt = append(wokeAt, t)
			awake = true
			since = t + wakeMs
			last = t + minMs
			continue
		}
		last = math.Max(last, t+minMs)
	}
	if awake && napAt() < end {
		sleptAt = napAt()
		awake = false
	}
	if !awake && end > sleptAt {
		asleep = append(asleep, [2]float64{sleptAt, end})
	}
	return asleep, wokeAt
}

// Replay replays each window over the requests.
func Replay(in ReplayInput) ([]ReplayedWindow, error) {
	start, err := parseISO(in.Requests.From)
	if err != nil {
		return nil, err
	}
	stop, err := parseISO(in.Requests.To)
	if err != nil {
		return nil, err
	}
	end := float64(stop - start)
	iso := func(ms float64) string { return isoOf(start + int64(ms)) }
	// Each minute once, in order: any request counts, whatever their number.
	seen := map[int]bool{}
	var starts []int
	for _, m := range in.Requests.Minutes {
		if m[1] > 0 && !seen[m[0]] {
			seen[m[0]] = true
			starts = append(starts, m[0])
		}
	}
	slices.Sort(starts)
	out := make([]ReplayedWindow, 0, len(in.Windows))
	for _, minutes := range in.Windows {
		asleep, wokeAt := replayOne(starts, end, minutes, float64(in.CheckMinutes)*minMs, in.WakeSeconds*1000, in.StartAsleep)
		w := ReplayedWindow{Minutes: minutes, Asleep: make([]Period, 0, len(asleep)), Wakes: len(wokeAt), WokeAt: make([]string, 0, len(wokeAt))}
		for _, a := range asleep {
			w.Asleep = append(w.Asleep, Period{iso(a[0]), iso(a[1])})
		}
		for _, at := range wokeAt {
			w.WokeAt = append(w.WokeAt, iso(at))
		}
		out = append(out, w)
	}
	return out, nil
}

// Recommend returns the shortest of the nine windows whose wakes are at most wakes(longest) + 10% of what the
// longest avoids against the shortest; the same wakes everywhere (no request, or no gap), the shortest, which saves
// the most at no cost.
func Recommend(rows []WakeRow) int {
	var candidates []WakeRow
	for _, m := range IdleWindows {
		if i := slices.IndexFunc(rows, func(r WakeRow) bool { return r.Minutes == m }); i >= 0 {
			candidates = append(candidates, rows[i])
		}
	}
	if len(candidates) == 0 {
		return IdleWindows[0]
	}
	shortest := candidates[0].Wakes
	longest := candidates[len(candidates)-1].Wakes
	limit := longest + (1-knee)*(shortest-longest)
	for _, r := range candidates {
		if r.Wakes <= limit+1e-9 {
			return r.Minutes
		}
	}
	return candidates[len(candidates)-1].Minutes
}
