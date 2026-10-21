package fleet

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A CLI install as GET /update answers it: a plan request waiting for its build, and the plan of an earlier request
// ready to apply.
const cliUpdateState = `{"installedWithCli":true,"version":"0.17.1","env":"prod","region":"eu-west-1",
 "applied":{"version":"0.17.1","build":"dmt-prod-install:aaaa","at":"2026-10-18T10:05:00Z"},"cli":"0.5.0",
 "versions":[{"version":"0.19.0-rc.1","router":null,"minCli":null,"published":null,"staged":true},
  {"version":"0.18.1","router":"0.30.0","minCli":null,"published":"2026-10-19"},
  {"version":"0.18.0","router":null,"minCli":"0.6.0","published":"2026-10-10","disabled":"run ` + "`dormeto install`" + ` with the CLI 0.6.0 or later"}],
 "build":{"id":"dmt-prod-install:cccc","by":"dashboard","action":"plan","version":"0.18.0","status":"SUCCEEDED","phase":"COMPLETED",
  "startedAt":"2026-10-19T11:00:00Z","endedAt":"2026-10-19T11:04:00Z","steps":["plan"],"tail":[],"consoleUrl":"https://eu-west-1.console.aws.amazon.com/b"},
 "pending":{"key":"prod/requests/r.json","id":"20261019T115800Z-0123abcd","etag":"e","action":"plan","version":"0.18.1","at":"2026-10-19T11:58:00Z",
  "who":"arn:aws:sts::1:assumed-role/Ci/run-42","operation":"op-1"},
 "plan":{"build":"dmt-prod-install:cccc","version":"0.18.0","summary":{"add":2,"change":1,"destroy":1,
  "replaced":["module.m.aws_lambda_function.api"],"destroyed":["module.m.aws_sqs_queue.old"]}},
 "extra":"kept"}`

func TestUpdateStateIsDecodedAndKeptAsSent(t *testing.T) {
	s := parse[UpdateState](t, cliUpdateState)
	if !s.InstalledWithCLI || s.Version != "0.17.1" || s.Pending == nil || s.Pending.Operation != "op-1" || s.Plan == nil ||
		s.Plan.Summary.Add != 2 || s.Plan.Build != "dmt-prod-install:cccc" || len(s.Versions) != 3 || s.Versions[0].Router != "" ||
		!s.Versions[0].Staged || s.Versions[2].Disabled == "" || s.Applied == nil || s.Applied.At != "2026-10-18T10:05:00Z" {
		t.Fatalf("decoded %+v", s)
	}
	out, err := json.Marshal(s)
	if err != nil || !strings.Contains(string(out), `"extra":"kept"`) {
		t.Errorf("%v: %s", err, out)
	}
	built, _ := json.Marshal(UpdateState{Version: "1.0.0"})
	if !strings.Contains(string(built), `"version":"1.0.0"`) {
		t.Errorf("built here: %s", built)
	}
}

func TestNewestIsTheHighestVersionAboveTheRunningOne(t *testing.T) {
	s := parse[UpdateState](t, `{"installedWithCli":true,"version":"0.17.1","versions":[{"version":"0.18.0"},{"version":"0.18.1-rc.1"},{"version":"0.18.1"},{"version":"0.17.0"},{"version":"bad"}]}`)
	if got := s.Newest(); got != "0.18.1" {
		t.Errorf("Newest() = %q", got)
	}
	for _, answer := range []string{
		`{"installedWithCli":true,"version":"0.17.1","versions":[{"version":"0.17.1"},{"version":"0.17.0"}]}`,
		`{"installedWithCli":true,"version":"0.17.1","versions":[]}`,
		`{"installedWithCli":false,"version":"0.17.1","env":"prod","region":"eu-west-1"}`,
	} {
		if got := parse[UpdateState](t, answer).Newest(); got != "" {
			t.Errorf("Newest() of %s = %q", answer, got)
		}
	}
}

func TestUpdatePlanText(t *testing.T) {
	s := parse[UpdateState](t, cliUpdateState)
	want := "plan       0.18.0, build dmt-prod-install:cccc\n" +
		"changes    2 to add, 1 to change, 1 to destroy\n" +
		"replaced   module.m.aws_lambda_function.api\n" +
		"destroyed  module.m.aws_sqs_queue.old\n"
	if got := s.Plan.Text(); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	nothing := UpdatePlan{Build: "b", Version: "1.0.0"}
	if got := nothing.Text(); got != "plan     1.0.0, build b\nchanges  none: the plan changes nothing\n" {
		t.Errorf("got %q", got)
	}
}

func TestManagerStatusTextOfACLIInstall(t *testing.T) {
	m := ManagerStatus{
		Version: "0.17.1",
		Update:  parse[UpdateState](t, cliUpdateState),
		Check:   ptr(parse[UpdateCheck](t, `{"enabled":true,"alert":false,"newer":true,"result":{"checkedAt":"2026-10-19T06:00:00Z","running":"0.17.1","latest":"0.18.1"}}`)),
	}
	want := "version      0.17.1\n" +
		"installed    with the CLI 0.5.0\n" +
		"env          prod\n" +
		"region       eu-west-1\n" +
		"applied      0.17.1 (build dmt-prod-install:aaaa, 2026-10-18 10:05 UTC)\n" +
		"newer        0.19.0-rc.1  —           staged\n" +
		"             0.18.1       2026-10-19  router 0.30.0\n" +
		"             0.18.0       2026-10-10  run `dormeto install` with the CLI 0.6.0 or later\n" +
		"last build   plan 0.18.0: SUCCEEDED, 2026-10-19 11:04 UTC\n" +
		"             https://eu-west-1.console.aws.amazon.com/b\n" +
		"pending      plan 0.18.1, requested by Ci/run-42 2026-10-19 11:58 UTC (operation op-1)\n" +
		"plan         0.18.0, build dmt-prod-install:cccc\n" +
		"changes      2 to add, 1 to change, 1 to destroy\n" +
		"replaced     module.m.aws_lambda_function.api\n" +
		"destroyed    module.m.aws_sqs_queue.old\n" +
		"daily check  on, alert off\n" +
		"last check   2026-10-19 06:00 UTC: 0.18.1 available\n"
	if got := m.Text(now.Location()); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	local := m.Text(time.FixedZone("CEST", 2*60*60))
	for _, want := range []string{"(build dmt-prod-install:aaaa, 2026-10-18 12:05 CEST)", "requested by Ci/run-42 2026-10-19 13:58 CEST", "last check   2026-10-19 08:00 CEST:"} {
		if !strings.Contains(local, want) {
			t.Errorf("local times: lacks %q:\n%s", want, local)
		}
	}
}

func TestManagerStatusTextOfATerraformInstall(t *testing.T) {
	m := ManagerStatus{Version: "0.17.1", Update: parse[UpdateState](t, `{"installedWithCli":false,"version":"0.17.1","env":"prod","region":"eu-west-1"}`)}
	want := "version      0.17.1\n" +
		"installed    with Terraform: change its module's source to update it\n" +
		"env          prod\n" +
		"region       eu-west-1\n" +
		"daily check  not available\n"
	if got := m.Text(now.Location()); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestManagerStatusTextOfARunningBuildAndARequestThatDidNotStart(t *testing.T) {
	m := ManagerStatus{Version: "0.17.1", Update: parse[UpdateState](t, `{"installedWithCli":true,"version":"0.17.1","env":"prod","region":"eu-west-1","versions":[],
	 "build":{"id":"b","by":"cli","action":"apply","version":"0.17.1","status":"IN_PROGRESS","phase":"BUILD","startedAt":"2026-10-19T11:58:00Z","endedAt":null,"steps":[],"tail":[],"consoleUrl":null},
	 "pending":{"id":"r","action":"apply","version":"0.18.1","at":"2026-10-19T11:00:00Z","who":"ana@x.io","operation":"op-2","notStarted":true}}`)}
	text := m.Text(now.Location())
	for _, want := range []string{
		"newer        none: up to date\n",
		"last build   started from the CLI: IN_PROGRESS (BUILD), started 2026-10-19 11:58 UTC\n",
		"pending      apply 0.18.1, requested by ana@x.io 2026-10-19 11:00 UTC (operation op-2)\n             it did not start: run it again\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	unreachable := ManagerStatus{Version: "0.17.1", Update: parse[UpdateState](t, `{"installedWithCli":true,"version":"0.17.1","versions":[],"releasesUnreachable":true}`)}
	if text := unreachable.Text(now.Location()); !strings.Contains(text, "newer        the releases could not be read: try again later\n") {
		t.Errorf("unreachable:\n%s", text)
	}
}

func TestUpdateCheckText(t *testing.T) {
	for answer, want := range map[string]string{
		`{"enabled":false,"alert":false,"newer":false,"result":null}`:                                                                                                 "daily check  off\n",
		`{"enabled":true,"alert":true,"newer":false,"result":null}`:                                                                                                   "daily check  on, alert on\nlast check   not checked yet\n",
		`{"enabled":true,"alert":true,"newer":false,"result":{"checkedAt":"2026-10-19T06:00:00Z","running":"0.17.1","latest":null}}`:                                  "daily check  on, alert on\nlast check   2026-10-19 06:00 UTC: up to date\n",
		`{"enabled":true,"alert":false,"result":{"checkedAt":"2026-10-19T06:00:00Z","running":"0.17.1","latest":null,"error":"the release index could not be read"}}`: "daily check  on, alert off\nlast check   2026-10-19 06:00 UTC: the release index could not be read\n",
		`{"enabled":false,"alert":false,"result":{"checkedAt":"2026-10-19T06:00:00Z","running":"0.17.1","latest":"0.18.1"}}`:                                          "daily check  off\nlast check   2026-10-19 06:00 UTC: 0.18.1 available\n",
	} {
		if got := parse[UpdateCheck](t, answer).Text(now.Location()); got != want {
			t.Errorf("%s: got %q, want %q", answer, got, want)
		}
	}
}

func TestUpdateCheckIsKeptAsSent(t *testing.T) {
	c := parse[UpdateCheck](t, `{"enabled":true,"alert":true,"newer":false,"updatedBy":"arn","result":null,"extra":1}`)
	out, _ := json.Marshal(c)
	if !strings.Contains(string(out), `"extra":1`) || !c.Alert {
		t.Errorf("%+v: %s", c, out)
	}
}

func TestChangesText(t *testing.T) {
	c := parse[Changes](t, `[{"version":"0.18.1","notes":"## 0.18.1 — 2026-10-19\n\nRouter 0.30.0. Access: apply the delegation policy again.\n\n### Fixed\n- a fix","access":"Access: apply the delegation policy again.","accessNeeded":true},
	 {"version":"0.18.0","notes":null,"access":null,"accessNeeded":false}]`)
	want := "## 0.18.1 — 2026-10-19\n\nRouter 0.30.0. Access: apply the delegation policy again.\n\n### Fixed\n- a fix\n\n" +
		"note: Access: apply the delegation policy again.\n" +
		"      this update does not apply it: https://dormeto.com/organization-access.html\n\n" +
		"## 0.18.0\nrelease notes unavailable\n"
	if got := c.Text(); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if got := parse[Changes](t, `[]`).Text(); got != "release notes unavailable\n" {
		t.Errorf("none: %q", got)
	}
	out, _ := json.Marshal(c)
	if !strings.Contains(string(out), `"notes":null`) {
		t.Errorf("not kept: %s", out)
	}
}

func TestBuildFailureText(t *testing.T) {
	b := parse[UpdateBuild](t, `{"id":"b","by":"dashboard","action":"plan","version":"0.18.1","status":"FAILED","steps":["init","plan"],"tail":["Error: no","exit 1"],"consoleUrl":"https://eu-west-1.console.aws.amazon.com/b"}`)
	want := "the step that failed: plan\n  Error: no\n  exit 1\nthe build: https://eu-west-1.console.aws.amazon.com/b\n"
	if got := b.FailureText(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	for _, status := range []string{"SUCCEEDED", "IN_PROGRESS"} {
		if got := (UpdateBuild{Status: status, Steps: []string{"x"}}).FailureText(); got != "" {
			t.Errorf("%s: %q", status, got)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestInstallBuildIsTheBuildTheRecordNames(t *testing.T) {
	o := parse[Operation](t, `{"id":"op-1","type":"update-manager","status":"succeeded","context":{"build":"dmt-prod-install:b","consoleUrl":null}}`)
	if got := o.InstallBuild(); got != "dmt-prod-install:b" {
		t.Errorf("InstallBuild() = %q", got)
	}
	for _, record := range []string{`{"id":"op-1","context":{}}`, `{"id":"op-1"}`, `{"id":"op-1","context":{"build":7}}`} {
		if got := parse[Operation](t, record).InstallBuild(); got != "" {
			t.Errorf("%s: %q", record, got)
		}
	}
}
