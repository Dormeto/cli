package fleet

import (
	"encoding/json"
	"fmt"
	"regexp"
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

func TestRouterDetailsTextSaysHowEachClusterIsReached(t *testing.T) {
	var d RouterDetails
	in := `{"name":"r","router":{},"workloads":{},"health":{"status":"ok"},"clusters":[
	  {"name":"a","reachable":true,"allowed":true,"missing":[],"via":"direct"},
	  {"name":"b","reachable":true,"allowed":true,"missing":[],"via":"proxy","proxy":{"name":"r-kube-b","version":"0.33.0"}},
	  {"name":"c","reachable":false,"allowed":false,"missing":[],"reason":"no route","via":"proxy","proxy":{"name":"r-kube-c","version":null}},
	  {"name":"d","reachable":true,"allowed":true,"missing":[]},
	  {"name":"e","reachable":true,"allowed":true,"missing":[],"via":"direct","checkedAt":"2026-10-19T08:00:00.000Z"}]}`
	if err := json.Unmarshal([]byte(in), &d); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(clustersText(d.Clusters, time.UTC), "\n")
	for i, want := range []string{
		"a: reachable, every right, directly",
		"b: reachable, every right, through its proxy r-kube-b (0.33.0)",
		"c: unreachable: no route, through its proxy r-kube-c",
		"d: reachable, every right", // a router before 0.33.0 says nothing
		"e: reachable, every right, directly, checked " + When("2026-10-19T08:00:00.000Z", time.UTC),
	} {
		if i >= len(lines) || lines[i] != want {
			t.Errorf("line %d: want %q in\n%s", i, want, strings.Join(lines, "\n"))
		}
	}
}

// gcpDetails is a Google Cloud router's answer as its controller's router action gives it (the router's
// gcp/src/controller-core.mjs, routerInfo): no Lambda, queue or concurrency figure, the metrics null, and the manager's
// level pushed to the router (url null), managerSettings being %s.
const gcpDetails = `{"name":"dmtr-dormeto-dev-europe-west1","cloud":"gcp","account":"dormeto-dev","region":"europe-west1","source":"cli",
 "router":{"name":"dmtr-europe-west1","version":"0.40.0","build":null},
 "settings":{"hibernate":null,"joinMinutes":null,"by":null,"at":null},
 "managerSettings":%s,
 "configuration":{"checkSchedule":{"value":"*/5 * * * *","source":"cli"},"ipLists":{"value":[],"source":"cli"},"noWakeRules":{"value":[],"source":"cli"},"metrics":{"value":null,"source":"cli"}},
 "pauses":[],"workloads":{"total":1,"byState":{"ASLEEP":1},"paused":0,"lastActivityAt":null},
 "health":{"status":"ok","problems":[],"checkSchedule":"*/5 * * * *","lastScheduledCheckAt":null,"lambda":{},"queues":{},"ipLists":[],"concurrency":null,"drift24h":0}}`

// hasRow tells whether text holds the labelled row label, value, whatever its alignment.
func hasRow(text, label, value string) bool {
	return regexp.MustCompile(`(?m)^ *` + regexp.QuoteMeta(label) + ` +` + regexp.QuoteMeta(value) + `$`).MatchString(text)
}

func gcpDetailsText(t *testing.T, managerSettings string) string {
	t.Helper()
	var d RouterDetails
	if err := json.Unmarshal([]byte(fmt.Sprintf(gcpDetails, managerSettings)), &d); err != nil {
		t.Fatal(err)
	}
	return d.Text(time.UTC, time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
}

// A Google Cloud router's organization level is pushed by the manager, not fetched, so its url is always null: what it
// holds is the manager's level. None pushed (or one the router can't read), the router takes the built-in value.
func TestRouterDetailsTextOfAGoogleCloudRouterReadsTheOrganizationLevelPushedToIt(t *testing.T) {
	for name, c := range map[string]struct{ m, hibernate, join string }{
		"pushed": {`{"url":null,"hibernate":false,"joinMinutes":45,"readAt":"2026-11-09T11:58:00.000Z","error":null}`,
			"inherit (manager: off, read 2m ago)", "inherit (manager: up to 45 min apart, read 2m ago)"},
		"none pushed": {`{"url":null,"hibernate":null,"joinMinutes":null,"readAt":null,"error":null}`,
			"inherit (built-in default: on)", "inherit (built-in default: up to 15 min apart)"},
		"unreadable": {`{"url":null,"hibernate":null,"joinMinutes":null,"readAt":"2026-11-09T11:58:00.000Z","error":"settings.json is invalid: x"}`,
			"inherit (built-in default: on; manager: settings.json is invalid: x)", "inherit (built-in default: up to 15 min apart)"},
	} {
		got := gcpDetailsText(t, c.m)
		for _, want := range [][2]string{{"hibernate", c.hibernate}, {"join windows", c.join}} {
			if !hasRow(got, want[0], want[1]) {
				t.Errorf("%s: lacks %s %q in\n%s", name, want[0], want[1], got)
			}
		}
	}
}

// The rows of AWS's figures a Google Cloud router has none of are left out, as the Lambda and concurrency ones are.
func TestRouterDetailsTextOfAGoogleCloudRouterLeavesOutTheAWSRows(t *testing.T) {
	got := gcpDetailsText(t, `{"url":null,"hibernate":null,"joinMinutes":null,"readAt":null,"error":null}`)
	for _, not := range []string{"failure queues", "wake statistics metrics", "lambda", "concurrency"} {
		if strings.Contains(got, not) {
			t.Errorf("prints %q:\n%s", not, got)
		}
	}
	for _, want := range [][2]string{{"cloud", "gcp"}, {"project", "dormeto-dev"}, {"idle check schedule", "*/5 * * * * (cli)"}, {"ip lists", "none (cli)"}} {
		if !hasRow(got, want[0], want[1]) {
			t.Errorf("lacks %s %q in\n%s", want[0], want[1], got)
		}
	}
}
