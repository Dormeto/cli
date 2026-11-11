package main

// The writing commands of `dormeto gcp` take AWS's --yes and --dry-run, and the flags AWS's twins have: pause's --for and
// --until HH:MM, schedule set --file -, wake --wait, list's filters; --ip-list's refresh_hours (the AWS and Google Cloud
// review's §4.2).

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/gcponboard"
)

// at sets the clock of the gcp commands for the test.
func at(t *testing.T, now time.Time) {
	t.Helper()
	before := gcpNow
	gcpNow = func() time.Time { return now }
	t.Cleanup(func() { gcpNow = before })
}

func TestGcpWritesTakeYesAndDryRun(t *testing.T) {
	where := []string{"--project", "p", "--region", "europe-west1"}
	page := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(page, []byte("<h1>Back soon</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	schedule := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(schedule, []byte(`{"timeZone":"UTC","windows":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	access := []string{"--gke-cluster", "europe-west1-b/c", "--namespace", "shop", "--workload", "shop"}
	for _, c := range []struct {
		run  string
		args []string
	}{
		{"workloads nap", []string{"workloads", "nap", "shop"}},
		{"workloads wake", []string{"workloads", "wake", "shop"}},
		{"workloads pause", []string{"workloads", "pause", "shop", "--reason", "freeze"}},
		{"workloads resume", []string{"workloads", "resume", "shop"}},
		{"workloads offboard", []string{"workloads", "offboard", "shop"}},
		{"workloads schedule set", []string{"workloads", "schedule", "set", "shop", "--file", schedule}},
		{"workloads schedule clear", []string{"workloads", "schedule", "clear", "shop"}},
		{"workloads settings", []string{"workloads", "settings", "shop", "--idle-minutes", "30"}},
		{"workloads waiting-page set", []string{"workloads", "waiting-page", "set", "shop", "--title", "Back soon"}},
		{"workloads waiting-page clear", []string{"workloads", "waiting-page", "clear", "shop"}},
		{"workloads access grant", append([]string{"workloads", "access", "grant"}, access...)},
		{"workloads access revoke", append([]string{"workloads", "access", "revoke"}, access...)},
		{"routers waiting-page set", []string{"routers", "waiting-page", "set", "--html", page}},
		{"routers waiting-page clear", []string{"routers", "waiting-page", "clear"}},
		{"routers settings", []string{"routers", "settings", "--idle-minutes", "30"}},
		{"routers pause", []string{"routers", "pause", "--reason", "freeze"}},
		{"routers resume", []string{"routers", "resume"}},
	} {
		args := slices.Concat([]string{"gcp"}, c.args, where)
		calls, errs, exit := gcpRan(t, 0, append(args, "--yes", "--dry-run")...)
		if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != c.run || !calls[0].r.Yes || !calls[0].r.DryRun {
			t.Errorf("%s: exit %d, stderr %q, calls %+v", c.run, exit, errs, calls)
		}
		calls, _, exit = gcpRan(t, 0, args...)
		if exit != 0 || len(calls) != 1 || calls[0].r.Yes || calls[0].r.DryRun {
			t.Errorf("%s without them: exit %d, calls %+v", c.run, exit, calls)
		}
	}
}

func TestGcpWorkloadsPauseReadsForAndUntilAsRoutersPause(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	at(t, now)
	pause := []string{"gcp", "workloads", "pause", "shop", "--project", "p", "--region", "europe-west1", "--reason", " freeze "}
	for _, c := range []struct {
		args  []string
		until time.Time
	}{
		{[]string{"--for", "2h"}, now.Add(2 * time.Hour)},
		{[]string{"--for", "3d"}, now.Add(72 * time.Hour)},
		{[]string{"--until", "18:30"}, time.Date(2026, 11, 10, 18, 30, 0, 0, time.Local)},
		{[]string{"--until", "2026-12-01T08:00:00+01:00"}, time.Date(2026, 12, 1, 7, 0, 0, 0, time.UTC)},
	} {
		calls, errs, exit := gcpRan(t, 0, append(slices.Clone(pause), c.args...)...)
		if exit != 0 || errs != "" || len(calls) != 1 {
			t.Fatalf("%v: exit %d, stderr %q, calls %+v", c.args, exit, errs, calls)
		}
		// The controller takes RFC 3339, in UTC.
		if r := calls[0].r; r.Until != c.until.UTC().Format(time.RFC3339) || r.Reason != "freeze" {
			t.Errorf("%v: until %q, reason %q", c.args, r.Until, r.Reason)
		}
	}
	calls, _, _ := gcpRan(t, 0, pause...)
	if len(calls) != 1 || calls[0].r.Until != "" {
		t.Fatalf("neither: %+v", calls)
	}
}

func TestGcpWorkloadsPauseRefusesWhatRoutersPauseRefusesBeforeAnyCall(t *testing.T) {
	at(t, time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local))
	base := []string{"gcp", "workloads", "pause", "shop", "--project", "p", "--region", "europe-west1"}
	for _, extra := range [][]string{
		{"--reason", "  "},
		{"--reason", strings.Repeat("x", 257)},
		{"--reason", "x", "--for", "1h", "--until", "18:00"},
		{"--reason", "x", "--for", "400d"},
		{"--reason", "x", "--until", "tomorrow"},
		{"--reason", "x", "--until", "08:00"},
		{"--reason", "x", "--until", "2001-01-01T00:00:00Z"},
	} {
		calls, errs, exit := gcpRan(t, 0, append(slices.Clone(base), extra...)...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: exit %d %q calls %v", extra, exit, errs, calls)
		}
	}
	if _, errs, _ := gcpRan(t, 0, append(slices.Clone(base), "--reason", strings.Repeat("x", 257))...); !strings.Contains(errs, "--reason: give 1 to 256 characters") {
		t.Errorf("stderr %q", errs)
	}
}

func TestGcpWorkloadsPauseHelpSaysForUntilAndTheReasonsRange(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "workloads", "pause", "--help")
	for _, want := range []string{"--for", "90m, 2h, 3d", "HH:MM today", "1 to 256 characters"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("%d lacks %q:\n%s", code, want, out)
		}
	}
}

// The schedule is read as AWS's schedule set reads it: a file or - for the standard input, a schedule or what
// `schedule show --json` prints.
func TestGcpWorkloadsScheduleSetReadsAFileOrTheStandardInputAsAws(t *testing.T) {
	const schedule = `{"timeZone":"Europe/Paris","windows":[{"kind":"nap","days":["mon"],"start":"22:00","end":"06:00"}]}`
	set := []string{"gcp", "workloads", "schedule", "set", "shop", "--project", "p", "--region", "europe-west1"}
	file := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(file, []byte(schedule), 0o600); err != nil {
		t.Fatal(err)
	}
	shown := `{"schedule":` + schedule + `,"nextEvents":[{"at":"2026-11-16T21:00:00.000Z","kind":"nap-start","window":0}]}`
	for name, c := range map[string]struct {
		stdin string
		args  []string
	}{
		"a file":                 {"", []string{"--file", file}},
		"standard input":         {schedule, []string{"--file", "-"}},
		"schedule show --json's": {shown, []string{"--file", "-"}},
	} {
		calls, errs, exit := gcpRanWith(t, c.stdin, 0, append(slices.Clone(set), c.args...)...)
		if exit != 0 || errs != "" || len(calls) != 1 {
			t.Fatalf("%s: exit %d, stderr %q, calls %+v", name, exit, errs, calls)
		}
		if !sameJSON(json.RawMessage(calls[0].r.Schedule), json.RawMessage(schedule)) {
			t.Errorf("%s: sent %s", name, calls[0].r.Schedule)
		}
	}
	for name, c := range map[string]struct {
		stdin string
		args  []string
	}{
		"no --file":        {"", nil},
		"absent":           {"", []string{"--file", filepath.Join(t.TempDir(), "none.json")}},
		"not JSON":         {"windows: []", []string{"--file", "-"}},
		"not an object":    {"[1]", []string{"--file", "-"}},
		"show of none":     {`{"schedule":null,"nextEvents":[]}`, []string{"--file", "-"}},
		"show of no field": {`{"nextEvents":[]}`, []string{"--file", "-"}},
	} {
		calls, errs, exit := gcpRanWith(t, c.stdin, 0, append(slices.Clone(set), c.args...)...)
		if exit != 2 || len(calls) != 0 || !strings.Contains(errs, "--file") {
			t.Errorf("%s: exit %d, stderr %q, calls %v", name, exit, errs, calls)
		}
	}
}

func TestGcpWorkloadsWakeReadsWaitAndTimeout(t *testing.T) {
	wake := []string{"gcp", "workloads", "wake", "shop", "--project", "p", "--region", "europe-west1"}
	calls, errs, exit := gcpRan(t, 0, append(slices.Clone(wake), "--wait")...)
	if exit != 0 || errs != "" || len(calls) != 1 || !calls[0].r.Wait || calls[0].r.Timeout != 15*time.Minute {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
	calls, _, _ = gcpRan(t, 0, append(slices.Clone(wake), "--wait", "--timeout", "2m")...)
	if len(calls) != 1 || calls[0].r.Timeout != 2*time.Minute {
		t.Fatalf("%+v", calls)
	}
	calls, _, _ = gcpRan(t, 0, wake...)
	if len(calls) != 1 || calls[0].r.Wait {
		t.Fatalf("%+v", calls)
	}
	if calls, errs, exit := gcpRan(t, 0, append(slices.Clone(wake), "--wait", "--timeout", "0s")...); exit != 2 || len(calls) != 0 || !strings.Contains(errs, "--timeout") {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
}

func TestGcpWorkloadsListReadsAwsFilters(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "workloads", "list", "--project", "p", "--region", "europe-west1",
		"--state", "asleep,Awake", "--state", "failed", "--search", "shop", "--tag", "team=web", "--tag", "env=prod")
	if exit != 0 || errs != "" || len(calls) != 1 {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
	r := calls[0].r
	if !slices.Equal(r.States, []string{"ASLEEP", "AWAKE", "FAILED"}) || r.Search != "shop" || !reflect.DeepEqual(r.HasTags, map[string]string{"team": "web", "env": "prod"}) {
		t.Fatalf("%+v", r)
	}
	if calls, _, _ := gcpRan(t, 0, "gcp", "workloads", "list", "--project", "p", "--region", "europe-west1"); len(calls) != 1 || calls[0].r.States != nil || calls[0].r.HasTags != nil {
		t.Fatalf("no filter: %+v", calls)
	}
	for _, bad := range [][]string{{"--tag", "team"}, {"--tag", "a=b,c"}, {"--tag", "team=web", "--tag", "team=api"}} {
		calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "workloads", "list", "--project", "p", "--region", "europe-west1"}, bad...)...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: %d %q %v", bad, exit, errs, calls)
		}
	}
}

// refresh_hours is AWS's name of the period (the router module's); refresh, the name of 0.11.0, is no key.
func TestGcpIPListTakesRefreshHoursAndNotRefresh(t *testing.T) {
	install := []string{"gcp", "routers", "install", "--project", "p", "--region", "europe-west1"}
	want := []gcponboard.IPList{{Name: "x", URL: "https://example.com/x.txt", RefreshHours: 6}}
	calls, errs, exit := gcpRan(t, 0, append(slices.Clone(install), "--ip-list", "name=x,url=https://example.com/x.txt,refresh_hours=6")...)
	if exit != 0 || errs != "" || len(calls) != 1 || !reflect.DeepEqual(calls[0].r.IPLists, want) {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
	calls, errs, exit = gcpRan(t, 0, append(slices.Clone(install), "--ip-list", "name=x,url=https://example.com/x.txt,refresh=6")...)
	if exit != 2 || len(calls) != 0 || !strings.Contains(errs, "refresh is not a key: name, url, refresh_hours") {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
}

// One --yes help text, AWS's (the AWS and Google Cloud review's §4.4): a command that asks says "do not ask for confirmation
// (needed without a terminal)", one that asks for several targets only AWS's words for that, one that never asks AWS's no-op.
func TestGcpYesHelpIsAWSs(t *testing.T) {
	asks := "do not ask for confirmation (needed without a terminal)"
	several := "do not ask for confirmation of several targets (needed without a terminal)"
	never := "accepted and ignored: this command asks no confirmation"
	want := map[string]string{
		"dormeto gcp routers install": asks, "dormeto gcp routers uninstall": asks, "dormeto gcp connect": asks, "dormeto gcp disconnect": asks,
		"dormeto gcp workloads offboard": asks, "dormeto gcp workloads onboard": asks, "dormeto gcp workloads access grant": asks,
		"dormeto gcp workloads access revoke": asks, "dormeto gcp workloads settings": several,
	}
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		for _, sub := range c.Commands() {
			walk(sub, path+" "+sub.Name())
		}
		f := c.Flags().Lookup("yes")
		if f == nil || c.Hidden {
			return
		}
		expected, ok := want[path]
		if !ok {
			expected = never
		}
		if f.Usage != expected {
			t.Errorf("%s --yes: %q, want %q", path, f.Usage, expected)
		}
	}
	root := newRoot(build{})
	for _, c := range root.Commands() {
		if c.Name() == "gcp" {
			walk(c, "dormeto gcp")
		}
	}
}

// Under --json a question goes to stderr, as AWS's: stdout holds the JSON alone.
func TestGcpQuestionsGoToStderrUnderJSON(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		c := &cobra.Command{}
		c.Flags().Bool("json", asJSON, "")
		var o, e strings.Builder
		c.SetOut(&o)
		c.SetErr(&e)
		want := io.Writer(&o)
		if asJSON {
			want = &e
		}
		if got := gcpPromptOut(c); got != want {
			t.Errorf("--json %v: the questions go to %v", asJSON, got)
		}
	}
}

// routers install and uninstall take --json (their dry run and their question print the released shape with the plan).
func TestGcpRoutersInstallAndUninstallTakeJSON(t *testing.T) {
	for _, verb := range []string{"install", "uninstall"} {
		calls, errs, exit := gcpRan(t, 0, "gcp", "routers", verb, "--project", "p", "--region", "europe-west1", "--dry-run", "--json")
		if exit != 0 || errs != "" || len(calls) != 1 || !calls[0].r.JSON || !calls[0].r.DryRun {
			t.Errorf("%s: exit %d, stderr %q, calls %+v", verb, exit, errs, calls)
		}
	}
}
