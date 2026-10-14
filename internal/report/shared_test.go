package report

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/estimate"
	"github.com/Dormeto/cli/internal/parity"
)

func sharedMarkdown(t *testing.T) {
	topic := parity.Load(t, "estimate")
	reproduced := 0
	for _, c := range topic.Cases {
		var out struct {
			Report   json.RawMessage `json:"report"`
			Markdown *string         `json:"markdown"`
		}
		if err := json.Unmarshal(c.Output, &out); err != nil {
			t.Fatal(err)
		}
		if out.Markdown == nil {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			var e estimate.Estimate
			if err := json.Unmarshal(out.Report, &e); err != nil {
				t.Fatal(err)
			}
			// The report in the case has no run time: the Markdown has "<generatedAt>" where it stands.
			e.GeneratedAt = "2026-10-12T08:00:00.000Z"
			got := strings.Replace(Markdown(e), "2026-10-12 08:00", "<generatedAt>", 1)
			// Go only (the user, 2026-10-14): the days of requests read among the options (--days).
			got = strings.Replace(got, "- Requests: the last 14 days (--days)\n", "", 1)
			// Go only (the user, 2026-10-15): an ECS service's tasks counted.
			got = strings.Replace(got, ", with the tasks counted (--ecs-uptime)", " (--ecs-uptime)", 1)
			if got != *out.Markdown {
				t.Errorf("Markdown differs from Node's:\n%s", firstDifference(got, *out.Markdown))
			}
		})
		reproduced++
	}
	if reproduced != 3 {
		t.Errorf("%d shared cases with a Markdown, want 3", reproduced)
	}
}

func firstDifference(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < max(len(g), len(w)); i++ {
		var a, b string
		if i < len(g) {
			a = g[i]
		}
		if i < len(w) {
			b = w[i]
		}
		if a != b {
			return fmt.Sprintf("line %d\n got  %s\n want %s", i+1, a, b)
		}
	}
	return "equal"
}
