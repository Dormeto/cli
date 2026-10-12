package replay

// Replays the shared `replay` cases (testdata/parity/replay.json).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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
				Running          []Period        `json:"running"`
				Now              time.Time       `json:"now"`
				Prices           []struct {
					Item stopped.Item `json:"item"`
					USD  *float64     `json:"usd"`
				} `json:"prices"`
				// replayIdle
				Windows     []int   `json:"windows"`
				WakeSeconds float64 `json:"wakeSeconds"`
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
			switch in.Function {
			case "idleWindowAdvice":
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
				got, err = Advice(Input{Requests: in.Requests, Current: in.Current, CheckMinutes: in.CheckMinutes, WakeMs: in.WakeMs,
					StartAsleep: in.StartAsleep, ActualWakes: in.ActualWakes, ActualFrom: in.ActualFrom, Stopped: in.Stopped, Hourly: hourly,
					Baseline: in.Baseline, WakerTargetGroup: in.WakerTargetGroup, Now: in.Now, Running: in.Running})
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
