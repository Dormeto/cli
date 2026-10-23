package fleet

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRouterDetailsTextMinimalAnswer(t *testing.T) {
	var d RouterDetails
	in := `{"name":"r","region":"eu-west-1","router":{"version":"0.31.0"},"workloads":{"total":0,"byState":{},"paused":0},"health":{"status":"ok","problems":[],"checkSchedule":null,"lastScheduledCheckAt":null,"queues":{"check":null}},"pauses":[],"configuration":{"checkSchedule":{"value":null,"source":"terraform"},"metrics":{"value":false,"source":"terraform"}}}`
	if err := json.Unmarshal([]byte(in), &d); err != nil {
		t.Fatal(err)
	}
	got := d.Text(time.UTC, time.Now())
	for _, want := range []string{"version         0.31.0\n", "no scheduled run recorded", "check-failures —", "0, 0 paused", "idle check schedule      — (terraform)", "off (terraform)", "ip lists                 none", "no-wake rules            none"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q in\n%s", want, got)
		}
	}
	for _, not := range []string{"account", "problems", "pauses", "clusters"} {
		if strings.Contains(got, not+" ") && not != "pauses" {
			t.Errorf("empty %q is printed:\n%s", not, got)
		}
	}
	if out, _ := json.Marshal(d); string(out) != in {
		t.Errorf("raw answer not kept: %s", out)
	}
}

func TestConditionsText(t *testing.T) {
	got := conditionsText(json.RawMessage(`{"sourceIps":["1.2.3.4","5.6.7.8"],"pathPrefixes":["/a","/b"],"pathPattern":"^/x","userAgentPattern":"bot","headers":[{"name":"X-A","value":"1"},{"name":"X-B","pattern":"b+"}]}`))
	want := "source 1.2.3.4 | 5.6.7.8, path /a* | /b*, path ~ ^/x, user agent ~ bot, header X-A = 1, header X-B ~ b+"
	if got != want {
		t.Errorf("got %q", got)
	}
}

func TestRouterDetailsTextIPListDownloadInTheZone(t *testing.T) {
	var d RouterDetails
	in := `{"name":"r","router":{},"workloads":{},"health":{"status":"ok","ipLists":[{"name":"bots","entries":12,"fetchedAt":"2026-10-19T08:00:00.000Z"}]}}`
	if err := json.Unmarshal([]byte(in), &d); err != nil {
		t.Fatal(err)
	}
	if got := d.Text(time.FixedZone("CEST", 2*3600), time.Now()); !strings.Contains(got, "bots: 12 entries, downloaded 2026-10-19 10:00 CEST") {
		t.Errorf("got\n%s", got)
	}
}
