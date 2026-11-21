package metrics

// Replays the shared `standalone-idle` cases (testdata/parity/standalone-idle.json) of the activity's functions: the
// router's plan of a workload's idle signals, their 5-minute points read from CloudWatch, and each signal's values. The
// topic's other functions are internal/discover's and internal/replay's.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/parity"
)

// scanCW answers GetMetricData from a case's recorded entries (no account or region: the standalone-idle topic's).
type scanCW struct{ a *parity.Answers }

func (r scanCW) GetMetricData(_ context.Context, in GetMetricDataInput) (out GetMetricDataOutput, err error) {
	return out, parity.AWSError(r.a.Call("cloudwatch", "getMetricData", in, &out))
}

func TestSharedCasesOfTheActivitysPlanPointsAndValues(t *testing.T) {
	ran := map[string]int{}
	for _, c := range parity.Load(t, "standalone-idle").Cases {
		var in struct {
			Function string            `json:"function"`
			Backends []ActivityBackend `json:"backends"`
			Settings ActivitySettings  `json:"settings"`
			Queries  []ActivityQuery   `json:"queries"`
			Days     int               `json:"days"`
			Now      time.Time         `json:"now"`
			Signals  []PlannedSignal   `json:"signals"`
			Read     ActivityPoints    `json:"read"`
			Answers  json.RawMessage   `json:"answers"`
		}
		if err := json.Unmarshal(c.Input, &in); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		switch in.Function {
		case "activityPlan", "activityPoints", "signalValues":
		case "defaultSignals", "standaloneIdle", "busyMinutes": // internal/discover's, internal/replay's
			continue
		default:
			t.Errorf("%s: unknown function %q", c.Name, in.Function)
			continue
		}
		ran[in.Function]++
		t.Run(c.Name, func(t *testing.T) {
			var got any
			switch in.Function {
			case "activityPlan":
				got = ActivityPlan(in.Backends, in.Settings)
			case "activityPoints":
				points, err := ReadActivity(context.Background(), scanCW{parity.Recorded(t, in.Answers)}, in.Queries, in.Days, in.Now)
				got = points
				if err != nil {
					if !Unreadable(err) {
						t.Fatalf("an error that is not CloudWatch's unreadable metrics: %v", err)
					}
					got = map[string]any{"error": map[string]string{"message": err.Error()}}
				}
			case "signalValues":
				got = SignalValues(in.Signals, in.Read)
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			parity.Equal(t, c.Output, b)
		})
	}
	if ran["activityPlan"] != 8 || ran["activityPoints"] != 2 || ran["signalValues"] != 1 {
		t.Errorf("ran %v, the corpus has 8 activityPlan, 2 activityPoints and 1 signalValues", ran)
	}
}
