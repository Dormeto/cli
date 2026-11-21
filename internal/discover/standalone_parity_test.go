package discover

// Replays the shared `standalone-idle` cases (testdata/parity/standalone-idle.json) of discovery's functions: a
// standalone candidate's default signals and its idle share. The topic's other functions are internal/metrics' and
// internal/replay's.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/parity"
)

// standaloneIdleFunctions are the topic's functions by the package that replays them.
var standaloneIdleFunctions = map[string]string{
	"defaultSignals": "discover", "standaloneIdle": "discover",
	"activityPlan": "metrics", "activityPoints": "metrics", "signalValues": "metrics",
	"busyMinutes": "replay",
}

func TestSharedCasesOfTheStandaloneSignalsAndIdleShare(t *testing.T) {
	ran := map[string]int{}
	for _, c := range parity.Load(t, "standalone-idle").Cases {
		var in struct {
			Function   string           `json:"function"`
			Backend    Backend          `json:"backend"`
			Candidates []StandaloneRule `json:"candidates"`
			Now        time.Time        `json:"now"`
			Answers    json.RawMessage  `json:"answers"`
		}
		if err := json.Unmarshal(c.Input, &in); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		owner, ok := standaloneIdleFunctions[in.Function]
		if !ok {
			t.Errorf("%s: unknown function %q", c.Name, in.Function)
			continue
		}
		if owner != "discover" {
			continue
		}
		ran[in.Function]++
		t.Run(c.Name, func(t *testing.T) {
			var got any
			switch in.Function {
			case "defaultSignals":
				got = DefaultSignals(in.Backend)
			case "standaloneIdle":
				idle := StandaloneIdle(context.Background(), recordedCW{parity.Recorded(t, in.Answers)}, in.Candidates, in.Now)
				got = idle
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			parity.Equal(t, c.Output, b)
		})
	}
	if ran["defaultSignals"] != 6 || ran["standaloneIdle"] != 4 {
		t.Errorf("ran %v, the corpus has 6 defaultSignals and 4 standaloneIdle", ran)
	}
}
