package replay

// Replays the shared `replay` cases (testdata/parity/replay.json).

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/parity"
	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/stopped"
)

func TestSharedCases(t *testing.T) {
	topic := parity.Load(t, "replay")
	if len(topic.Cases) == 0 {
		t.Fatal("no shared cases")
	}
	for _, c := range topic.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var in struct {
				Function string `json:"function"`
				// idleWindowAdvice
				Requests         RequestMinutes  `json:"requests"`
				Current          int             `json:"current"`
				CheckMinutes     int             `json:"checkMinutes"`
				WakeMs           *float64        `json:"wakeMs"`
				StartAsleep      bool            `json:"startAsleep"`
				ActualWakes      int             `json:"actualWakes"`
				ActualFrom       *string         `json:"actualFrom"`
				Stopped          []stopped.Item  `json:"stopped"`
				Baseline         prices.Baseline `json:"baseline"`
				WakerTargetGroup bool            `json:"wakerTargetGroup"`
				Estimated        bool            `json:"estimated"`
				Running          []Period        `json:"running"`
				Now              time.Time       `json:"now"`
				Prices           []struct {
					Item stopped.Item `json:"item"`
					USD  *float64     `json:"usd"`
				} `json:"prices"`
				// replayIdle
				Windows     []int   `json:"windows"`
				WakeSeconds float64 `json:"wakeSeconds"`
				// standaloneAdvice: busyMinutes' arguments, then idleWindowAdvice's with the windows tried and recommendable
				From          string                  `json:"from"`
				To            string                  `json:"to"`
				Signals       []metrics.PlannedSignal `json:"signals"`
				Values        []metrics.SignalValue   `json:"values"`
				Awake         []Period                `json:"awake"`
				Recommendable []int                   `json:"recommendable"`
				// recommendWindow
				Rows []WakeRow `json:"rows"`
				// checkMinutesOf
				Schedule *string `json:"schedule"`
			}
			if err := json.Unmarshal(c.Input, &in); err != nil {
				t.Fatal(err)
			}
			var got any
			var err error
			hourly := func(_ context.Context, item stopped.Item) (*float64, error) {
				want, _ := json.Marshal(item)
				for _, p := range in.Prices {
					if have, _ := json.Marshal(p.Item); string(want) == string(have) {
						return p.USD, nil
					}
				}
				t.Errorf("no price listed for %s", want)
				return nil, nil
			}
			switch in.Function {
			case "standaloneAdvice":
				// The product's windows (the export fails when they change).
				if !slices.Equal(in.Windows, StandaloneWindows) || !slices.Equal(in.Recommendable, StandaloneRecommendable) {
					t.Fatalf("windows %v and %v, Go's %v and %v", in.Windows, in.Recommendable, StandaloneWindows, StandaloneRecommendable)
				}
				requests := BusyMinutes(in.Signals, in.Values, in.From, in.To, in.Awake)
				got, err = Advice(Input{Requests: requests, Current: in.Current, CheckMinutes: in.CheckMinutes, WakeMs: in.WakeMs,
					StartAsleep: in.StartAsleep, ActualWakes: in.ActualWakes, ActualFrom: in.ActualFrom, Stopped: in.Stopped, Hourly: hourly,
					Baseline: in.Baseline, WakerTargetGroup: in.WakerTargetGroup, Estimated: in.Estimated, Now: in.Now, Running: in.Running,
					Windows: in.Windows, Recommendable: in.Recommendable})
			case "idleWindowAdvice":
				got, err = Advice(Input{Requests: in.Requests, Current: in.Current, CheckMinutes: in.CheckMinutes, WakeMs: in.WakeMs,
					StartAsleep: in.StartAsleep, ActualWakes: in.ActualWakes, ActualFrom: in.ActualFrom, Stopped: in.Stopped, Hourly: hourly,
					Baseline: in.Baseline, WakerTargetGroup: in.WakerTargetGroup, Estimated: in.Estimated, Now: in.Now, Running: in.Running})
			case "replayIdle":
				got, err = Replay(ReplayInput{Requests: in.Requests, Windows: in.Windows, CheckMinutes: in.CheckMinutes, WakeSeconds: in.WakeSeconds, StartAsleep: in.StartAsleep})
			case "recommendWindow":
				got = Recommend(in.Rows)
			case "windowsFor":
				got = Windows(in.Current)
			case "checkMinutesOf":
				got = CheckMinutes(in.Schedule)
			default:
				t.Fatalf("unknown function %q", in.Function)
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			parity.Equal(t, c.Output, b)
		})
	}
}

// Replays the shared `standalone-idle` cases of busyMinutes (testdata/parity/standalone-idle.json); the topic's other
// functions are internal/discover's and internal/metrics'.
func TestSharedCasesOfTheBusyPoints(t *testing.T) {
	ran := 0
	for _, c := range parity.Load(t, "standalone-idle").Cases {
		var in struct {
			Function string                  `json:"function"`
			Signals  []metrics.PlannedSignal `json:"signals"`
			Values   []metrics.SignalValue   `json:"values"`
			From     string                  `json:"from"`
			To       string                  `json:"to"`
			Awake    []Period                `json:"awake"`
		}
		if err := json.Unmarshal(c.Input, &in); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if in.Function != "busyMinutes" {
			continue
		}
		ran++
		t.Run(c.Name, func(t *testing.T) {
			b, err := json.Marshal(BusyMinutes(in.Signals, in.Values, in.From, in.To, in.Awake))
			if err != nil {
				t.Fatal(err)
			}
			parity.Equal(t, c.Output, b)
		})
	}
	if ran != 2 {
		t.Errorf("ran %d busyMinutes cases, the corpus has 2", ran)
	}
}
