package gcponboard

// Google Cloud's standalone workloads in the gcp subtree (vision standalone workloads design §2, §5, §8): the kind in the
// list and its filter, show as dormeto aws workloads show shows the same workload.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/fleet"
)

const agentPath = "projects/dormeto-dev/zones/europe-west1-b/instances/build-agent"

// standaloneStatus is the router's status of the standalone workload build-agent, as AWS's status names its keys (the
// router's POST /v1/actions status), with resource as the cloud's id: path on Google Cloud, resourceArn on AWS.
func standaloneStatus(resourceKey, resource string) map[string]any {
	var status map[string]any
	_ = json.Unmarshal([]byte(`{"workloadId":"build-agent","name":"build-agent","kind":"standalone","state":"FAILED","stateAt":"2026-11-14T08:40:00.000Z",
"updatedAt":"2026-11-14T08:40:00.000Z","failedAt":"2026-11-14T08:40:00.000Z","failure":{"kind":"permission","step":"wake","message":"VM build-agent: Google refused the change"},
"consecutiveFailures":2,"nextAttemptAt":"2026-11-14T10:10:00.000Z","pauses":[],"tags":{"team":"ci"},
"idle":[{"signal":"cpu","below":5,"value":0.8,"underSince":"2026-11-14T06:35:00.000Z","applies":true,"from":"default"},
 {"signal":"network","below":null,"value":null,"underSince":null,"applies":false,"reason":"off","from":"default"},
 {"signal":"metric","below":1,"value":3,"underSince":null,"applies":true,"from":"type"}],
"nap":{"eligibleAt":"2026-11-14T12:20:00.000Z","latestAt":"2026-11-14T12:35:00.000Z","inSeconds":4800},
"router":{"version":"0.42.0","build":"unknown"},"backends":[{"kind":"instance","id":"build-agent","status":null,"location":"europe-west1-b"}],"resourceMissing":true,
"settings":{"idleMinutes":120,"wakeTimeoutSeconds":600,"idleCpuPercent":5,"idleNetworkBytes":"off","idleMetric":"off"},
"settingsFrom":{"idleMinutes":"default","wakeTimeoutSeconds":"default","idleCpuPercent":"default","idleNetworkBytes":"default","idleMetric":"type"},
"type":{"name":"ci","from":"tags","missing":""},"schedule":null}`), &status)
	status[resourceKey] = resource
	return status
}

func (g *workloadsRig) standaloneAgent() {
	g.s.Lock()
	defer g.s.Unlock()
	g.s.Workloads["shop"], g.s.Workloads["build-agent"] = "AWAKE", "FAILED"
	g.s.WorkloadDocs["build-agent"] = map[string]any{"kind": "standalone", "resource": agentPath, "name": "build-agent", "tags": map[string]any{"team": "ci"},
		"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "build-agent", "id": "333"}}, "resourceMissing": true,
		"settings": map[string]any{"idleMinutes": 120, "wakeTimeoutSeconds": 600, "idleCpuPercent": 5, "idleNetworkBytes": "off", "idleMetric": "off"}}
	g.s.Statuses = map[string]map[string]any{"build-agent": standaloneStatus("resource", agentPath)}
}

// The list says each workload's kind (http for a row without one), --kind keeps one, and --json passes the router's keys.
func TestWorkloadsListSaysTheKindAndKeepsOne(t *testing.T) {
	g := newWorkloadsRig(t)
	g.standaloneAgent()
	code, out := g.call(t, WorkloadsList, workloadsEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	lines := strings.Split(out, "\n")
	if f := strings.Fields(lines[0]); len(f) < 4 || f[3] != "KIND" {
		t.Fatalf("header %q", lines[0])
	}
	if f := strings.Fields(lines[1]); f[1] != "build-agent" || f[3] != "standalone" {
		t.Fatalf("row %q", lines[1])
	}
	if f := strings.Fields(lines[2]); f[1] != "shop" || f[3] != "http" {
		t.Fatalf("row %q", lines[2])
	}
	r := workloadsEurope
	r.Kind = "standalone"
	if code, out = g.call(t, WorkloadsList, r); code != 0 || strings.Contains(out, "shop") || !strings.Contains(out, "1 workload") {
		t.Fatalf("--kind standalone: exit %d:\n%s", code, out)
	}
	r.Kind = "http"
	if code, out = g.call(t, WorkloadsList, r); code != 0 || strings.Contains(out, "build-agent") {
		t.Fatalf("--kind http: exit %d:\n%s", code, out)
	}
	r.Kind = "vm"
	if code, out = g.call(t, WorkloadsList, r); code != 2 || !strings.Contains(out, "--kind: give http or standalone") {
		t.Fatalf("--kind vm: exit %d:\n%s", code, out)
	}
	r = workloadsEurope
	r.JSON = true
	_, out = g.call(t, WorkloadsList, r)
	var list struct{ Workloads []map[string]any }
	if err := json.Unmarshal([]byte(out), &list); err != nil || list.Workloads[0]["kind"] != "standalone" || list.Workloads[0]["resource"] != agentPath {
		t.Fatalf("%v:\n%s", err, out)
	}
	if _, has := list.Workloads[1]["kind"]; has {
		t.Fatalf("an HTTP row with a kind:\n%s", out)
	}
}

// show of a standalone workload reads the router's status and prints it as dormeto aws workloads show prints the same
// workload, the cloud's own words aside: its kind, its resource (missing), its idle signals with their levels and last
// values, its nap, its kept failure and when it is retried; no URL.
func TestWorkloadsShowOfAStandaloneWorkloadIsAWSs(t *testing.T) {
	g := newWorkloadsRig(t)
	g.standaloneAgent()
	g.d.Now = func() time.Time { return time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC) }
	mark := len(g.s.Calls())
	r := workloadsEurope
	r.ID = "build-agent"
	code, out := g.call(t, WorkloadsShow, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.controllerCalls(mark); len(got) != 2 || got[1] != "POST /v1/actions" {
		t.Fatalf("calls %v", got)
	}
	// AWS's text of the same workload: its status as the manager passes it, with its ARN, account and cloud.
	aws := standaloneStatus("resourceArn", "arn:aws:ec2:eu-west-1:111122223333:instance/i-0b7c")
	aws["cloud"], aws["account"], aws["region"] = "aws", "111122223333", "eu-west-1"
	b, _ := json.Marshal(aws)
	var d fleet.WorkloadDetails
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	want := d.Text("prod-eu", time.UTC)
	// The cloud's own words: its router's name, cloud, account or project, region and resource.
	for _, w := range [][2]string{{"prod-eu/", "dmtr-dormeto-dev-europe-west1/"}, {"cloud           aws", "cloud           gcp"},
		{"account         111122223333", "project         dormeto-dev"}, {"region          eu-west-1", "region          europe-west1"},
		{"arn:aws:ec2:eu-west-1:111122223333:instance/i-0b7c", agentPath}} {
		want = strings.Replace(want, w[0], w[1], 1)
	}
	if out != want {
		t.Fatalf("gcp:\n%s\naws, the cloud's words replaced:\n%s", out, want)
	}
	for _, w := range []string{"kind            standalone", agentPath + " (resource missing)", "CPU      0.8, idle under 5", "nap      from",
		"failure         wake failed (permission): VM build-agent: Google refused the change",
		"retry           next attempt 2026-11-14 10:10 UTC, until it succeeds or a person naps it"} {
		contains(t, out, w)
	}
	if strings.Contains(out, "url") {
		t.Fatalf("a URL:\n%s", out)
	}
}
