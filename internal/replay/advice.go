// The idle window advice. Ports manager packages/fleet/src/idle.ts (idleWindowAdvice, intersect).
package replay

import (
	"context"
	"time"

	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/stopped"
)

const (
	hourMs = 3_600_000
	dayMs  = 86_400_000
)

// Row is one window's figures.
type Row struct {
	Minutes           int      `json:"minutes"`
	AsleepHours       float64  `json:"asleepHours"`
	USDMonth          *float64 `json:"usdMonth"`
	WakesPerDay       float64  `json:"wakesPerDay"`
	WaitSecondsPerDay *float64 `json:"waitSecondsPerDay"`
}

// Actual compares the real wakes with the replay's.
type Actual struct {
	Wakes         int  `json:"wakes"`
	ReplayedWakes int  `json:"replayedWakes"`
	Noisy         bool `json:"noisy"`
}

// Result is the Node IdleWindowAdvice.
type Result struct {
	ComputedAt       string   `json:"computedAt"`
	From             string   `json:"from"`
	To               string   `json:"to"`
	Current          int      `json:"current"`
	Recommended      int      `json:"recommended"`
	CheckMinutes     int      `json:"checkMinutes"`
	WakeSeconds      *float64 `json:"wakeSeconds"`
	Priced           bool     `json:"priced"`
	Windows          []Row    `json:"windows"`
	Actual           Actual   `json:"actual"`
	WakerTargetGroup bool     `json:"wakerTargetGroup"`
}

// Input is idleWindowAdvice's argument. WakeMs, ActualFrom and Stopped are nil when Node's are null; Running nil is
// a backend running all the time. Ctx defaults to context.Background().
type Input struct {
	Ctx              context.Context
	Requests         RequestMinutes
	Current          int
	CheckMinutes     int
	WakeMs           *float64
	StartAsleep      bool
	ActualWakes      int
	ActualFrom       *string
	Stopped          []stopped.Item
	Hourly           func(context.Context, stopped.Item) (*float64, error)
	Baseline         prices.Baseline
	WakerTargetGroup bool
	Now              time.Time
	Running          []Period
}

// Advice is the advice of a workload.
func Advice(in Input) (Result, error) {
	ctx := in.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var wakeSeconds *float64
	wake := 0.0
	if in.WakeMs != nil {
		w := *in.WakeMs / 1000
		wakeSeconds, wake = &w, w
	}
	replayed, err := Replay(ReplayInput{Requests: in.Requests, Windows: Windows(in.Current), CheckMinutes: in.CheckMinutes, WakeSeconds: wake, StartAsleep: in.StartAsleep})
	if err != nil {
		return Result{}, err
	}
	fromMs, err := parseISO(in.Requests.From)
	if err != nil {
		return Result{}, err
	}
	toMs, err := parseISO(in.Requests.To)
	if err != nil {
		return Result{}, err
	}
	days := float64(toMs-fromMs) / dayMs
	// Priced only when every item of what the latest nap stopped has a price: never a partial dollar figure.
	priced := len(in.Stopped) > 0
	windows := make([]Row, 0, len(replayed))
	for _, r := range replayed {
		var hours float64
		for _, p := range r.Asleep {
			a, err := parseISO(p.From)
			if err != nil {
				return Result{}, err
			}
			b, err := parseISO(p.To)
			if err != nil {
				return Result{}, err
			}
			hours += float64(b-a) / hourMs
		}
		var usd *float64
		if priced {
			worth := r.Asleep
			if in.Running != nil {
				worth = intersect(r.Asleep, in.Running)
			}
			phases := make([]prices.Phase, len(worth))
			for i, p := range worth {
				phases[i] = prices.Phase{State: "ASLEEP", From: p.From, To: p.To, Stopped: in.Stopped}
			}
			savings, err := prices.SavingsOf(ctx, phases, in.Hourly, in.Baseline)
			if err != nil {
				return Result{}, err
			}
			if len(savings.Unpriced) > 0 {
				priced = false
			} else {
				u := savings.USD
				usd = &u
			}
		}
		row := Row{Minutes: r.Minutes, AsleepHours: hours / days, WakesPerDay: float64(r.Wakes) / days}
		if usd != nil {
			v := (*usd * 30) / days
			row.USDMonth = &v
		}
		if wakeSeconds != nil {
			v := (float64(r.Wakes) / days) * *wakeSeconds
			row.WaitSecondsPerDay = &v
		}
		windows = append(windows, row)
	}
	if !priced {
		for i := range windows {
			windows[i].USDMonth = nil
		}
	}
	var own ReplayedWindow
	wakeRows := make([]WakeRow, len(replayed))
	for i, r := range replayed {
		wakeRows[i] = WakeRow{r.Minutes, float64(r.Wakes)}
		if r.Minutes == in.Current {
			own = r
		}
	}
	replayedWakes := own.Wakes
	if in.ActualFrom != nil {
		replayedWakes = 0
		for _, w := range own.WokeAt {
			if w >= *in.ActualFrom {
				replayedWakes++
			}
		}
	}
	actual := float64(in.ActualWakes)
	return Result{
		ComputedAt: isoOf(in.Now.UnixMilli()), From: in.Requests.From, To: in.Requests.To, Current: in.Current,
		Recommended: Recommend(wakeRows), CheckMinutes: in.CheckMinutes, WakeSeconds: wakeSeconds, Priced: priced, Windows: windows,
		Actual: Actual{Wakes: in.ActualWakes, ReplayedWakes: replayedWakes,
			Noisy: in.ActualFrom != nil && float64(replayedWakes) > 1.5*actual && float64(replayedWakes) >= actual+3},
		WakerTargetGroup: in.WakerTargetGroup,
	}, nil
}

// intersect: two lists of periods, each sorted and disjoint: the time in both.
func intersect(a, b []Period) []Period {
	out := []Period{}
	for i, j := 0, 0; i < len(a) && j < len(b); {
		from := a[i].From
		if b[j].From > from {
			from = b[j].From
		}
		to := a[i].To
		if b[j].To < to {
			to = b[j].To
		}
		if from < to {
			out = append(out, Period{from, to})
		}
		if a[i].To < b[j].To {
			i++
		} else {
			j++
		}
	}
	return out
}
