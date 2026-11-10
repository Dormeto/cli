package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/gcponboard"
)

func TestGcpWorkloadsListReadsTheRequest(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "workloads", "list", "--project", "p", "--region", "europe-west1", "--json", "--gcloud-configuration", "c")
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "workloads list" {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	want := gcponboard.Request{Project: "p", Region: "europe-west1", Configuration: "c", JSON: true}
	if !reflect.DeepEqual(calls[0].r, want) {
		t.Fatalf("%+v", calls[0].r)
	}
}

func TestGcpWorkloadsWithAnIDTakeItAsTheArgument(t *testing.T) {
	for _, verb := range []string{"show", "offboard", "nap", "wake"} {
		calls, errs, exit := gcpRan(t, 0, "gcp", "workloads", verb, "shop", "--project", "p", "--region", "europe-west1")
		if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "workloads "+verb {
			t.Fatalf("%s: %d %q %v", verb, exit, errs, calls)
		}
		if want := (gcponboard.Request{Project: "p", Region: "europe-west1", ID: "shop"}); !reflect.DeepEqual(calls[0].r, want) {
			t.Fatalf("%s: %+v", verb, calls[0].r)
		}
		for _, args := range [][]string{{"gcp", "workloads", verb, "--project", "p", "--region", "europe-west1"},
			{"gcp", "workloads", verb, "a", "b", "--project", "p", "--region", "europe-west1"}} {
			calls, errs, exit := gcpRan(t, 0, args...)
			if exit != 2 || len(calls) != 0 || errs == "" {
				t.Errorf("%v: exit %d calls %v stderr %q", args, exit, calls, errs)
			}
		}
	}
}

func TestGcpWorkloadsOnboardReadsTheFlags(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m",
		"--backend-service", "be", "--id", "shop", "--idle-minutes", "30", "--hibernate", "false", "--dry-run", "--json")
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "workloads onboard" {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	r := calls[0].r
	if r.URLMap != "m" || r.BackendService != "be" || r.ID != "shop" || !r.DryRun || !r.JSON ||
		r.IdleMinutes == nil || *r.IdleMinutes != 30 || r.Hibernate == nil || *r.Hibernate {
		t.Fatalf("%+v", r)
	}
	calls, _, _ = gcpRan(t, 0, "gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "be")
	if r := calls[0].r; r.IdleMinutes != nil || r.Hibernate != nil || r.ID != "" || r.RequestLogs {
		t.Fatalf("settings not given must stay nil: %+v", r)
	}
	// A zero is a value given, for the range check to refuse.
	calls, _, _ = gcpRan(t, 0, "gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "be", "--idle-minutes", "0")
	if r := calls[0].r; r.IdleMinutes == nil || *r.IdleMinutes != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestGcpWorkloadsOnboardReadsRequestLogs(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m",
		"--backend-service", "be", "--request-logs")
	if exit != 0 || errs != "" || len(calls) != 1 || !calls[0].r.RequestLogs {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
}

func TestGcpWorkloadsOnboardReadsTheFeaturesFlags(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "be",
		"--load-balancer", "regional", "--wake-timeout-seconds", "900", "--database", "db-1", "--database", "db-2", "--gke-cluster", "europe-west1-b/c", "--yes")
	if exit != 0 || errs != untriedWarning || len(calls) != 1 {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	r := calls[0].r
	if r.LoadBalancer != "regional" || r.WakeTimeout == nil || *r.WakeTimeout != 900 || !slices.Equal(r.CloudSQL, []string{"db-1", "db-2"}) ||
		r.GKECluster != "europe-west1-b/c" || !r.Yes {
		t.Fatalf("%+v", r)
	}
	calls, _, _ = gcpRan(t, 0, "gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "be")
	if r := calls[0].r; r.WakeTimeout != nil || r.LoadBalancer != "" || r.CloudSQL != nil || r.GKECluster != "" {
		t.Fatalf("flags not given must stay empty: %+v", r)
	}
	// A zero is a value given, for the range check to refuse.
	calls, _, _ = gcpRan(t, 0, "gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "be", "--wake-timeout-seconds", "0")
	if r := calls[0].r; r.WakeTimeout == nil || *r.WakeTimeout != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestGcpWorkloadsOnboardRefusesAFeaturesFlagGivenEmpty(t *testing.T) {
	for _, flag := range []string{"--load-balancer", "--gke-cluster"} {
		calls, errs, exit := gcpRan(t, 0, "gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "be", flag, "")
		if exit != 2 || len(calls) != 0 || !strings.Contains(errs, flag) {
			t.Errorf("%s: %d %q %v", flag, exit, errs, calls)
		}
	}
}

func TestGcpWorkloadsExitCodes(t *testing.T) {
	if _, _, exit := gcpRan(t, 1, "gcp", "workloads", "list", "--project", "p", "--region", "europe-west1"); exit != 1 {
		t.Fatalf("exit %d", exit)
	}
	for _, args := range [][]string{
		{"gcp", "workloads", "list", "--region", "europe-west1"},
		{"gcp", "workloads", "list", "--project", "p"},
		{"gcp", "workloads", "list", "--project", "p", "--region", "europe-west1", "extra"},
		{"gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "b", "--hibernate", "maybe"},
		{"gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "b", "--idle-minutes", "x"},
		{"gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "b", "--wait-seconds", "20"},
		{"gcp", "workloads", "nap", "shop", "--project", "p", "--region", "europe-west1", "--nope"},
	} {
		calls, errs, exit := gcpRan(t, 0, args...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: exit %d calls %v stderr %q", args, exit, calls, errs)
		}
	}
}

func TestGcpWorkloadsAreWiredInTheRoot(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "workloads", "onboard", "--help")
	for _, flag := range []string{"--url-map", "--backend-service", "--id", "--idle-minutes", "--hibernate", "--dry-run", "--project", "--region", "--json"} {
		if code != 0 || !strings.Contains(out, flag) {
			t.Errorf("%d %s lacks %s", code, out, flag)
		}
	}
	out, _, code = executed(t, build{}, "gcp", "workloads", "--help")
	for _, verb := range []string{"list", "show", "onboard", "offboard", "nap", "wake"} {
		if code != 0 || !strings.Contains(out, verb) {
			t.Errorf("%d %s lacks %s", code, out, verb)
		}
	}
}

func TestGcpWorkloadsOffboardForceReadsIntoTheRequest(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "workloads", "offboard", "shop", "--project", "p", "--region", "europe-west1", "--force")
	if exit != 0 || errs != "" || len(calls) != 1 || !calls[0].r.Force || calls[0].r.ID != "shop" {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
}

func TestGcpWorkloadsOffboardHelpSaysForceDoesNotPassABusyWorkload(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "workloads", "offboard", "--help")
	for _, want := range []string{"NAPPING", "WAKING", "busy", "the router's check"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("%d %s lacks %s", code, out, want)
		}
	}
}

func TestGcpWorkloadsScheduleReadsTheRequest(t *testing.T) {
	file := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(file, []byte(`{"timeZone":"UTC"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"show", "set", "clear"} {
		args := []string{"gcp", "workloads", "schedule", verb, "shop", "--project", "p", "--region", "europe-west1", "--json"}
		want := gcponboard.Request{Project: "p", Region: "europe-west1", ID: "shop", JSON: true}
		if verb == "set" {
			args = append(args, "--file", file)
			want.Schedule = json.RawMessage(`{"timeZone":"UTC"}`)
		}
		calls, errs, exit := gcpRan(t, 0, args...)
		if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "workloads schedule "+verb || !reflect.DeepEqual(calls[0].r, want) {
			t.Fatalf("%s: %d %q %+v", verb, exit, errs, calls)
		}
	}
	for _, args := range [][]string{
		{"gcp", "workloads", "schedule", "set", "shop", "--project", "p", "--region", "europe-west1"},
		{"gcp", "workloads", "schedule", "show", "--project", "p", "--region", "europe-west1"},
		{"gcp", "workloads", "schedule", "show", "a", "b", "--project", "p", "--region", "europe-west1"},
	} {
		calls, errs, exit := gcpRan(t, 0, args...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: exit %d calls %v stderr %q", args, exit, calls, errs)
		}
	}
}

func TestGcpWorkloadsPauseReadsTheRequest(t *testing.T) {
	at(t, time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	calls, errs, exit := gcpRan(t, 0, "gcp", "workloads", "pause", "shop", "--project", "p", "--region", "europe-west1", "--reason", "freeze",
		"--keep-asleep", "--until", "2026-12-01T08:00:00Z")
	want := gcponboard.Request{Project: "p", Region: "europe-west1", ID: "shop", Reason: "freeze", KeepAsleep: true, Until: "2026-12-01T08:00:00Z"}
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "workloads pause" || !reflect.DeepEqual(calls[0].r, want) {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
	calls, _, _ = gcpRan(t, 0, "gcp", "workloads", "resume", "shop", "--project", "p", "--region", "europe-west1")
	if len(calls) != 1 || calls[0].run != "workloads resume" || calls[0].r.ID != "shop" {
		t.Fatalf("%+v", calls)
	}
}

func TestGcpWorkloadsPauseWithoutAReasonIsExitTwo(t *testing.T) {
	for _, args := range [][]string{
		{"gcp", "workloads", "pause", "shop", "--project", "p", "--region", "europe-west1"},
		{"gcp", "workloads", "pause", "shop", "--project", "p", "--region", "europe-west1", "--reason", "  "},
	} {
		calls, errs, exit := gcpRan(t, 0, args...)
		if exit != 2 || len(calls) != 0 || !strings.Contains(errs, "--reason") {
			t.Errorf("%v: exit %d calls %v stderr %q", args, exit, calls, errs)
		}
	}
}

func TestGcpWorkloadsPauseHelpSaysWhatEachKindDoes(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "workloads", "pause", "--help")
	for _, want := range []string{"--reason", "--keep-asleep", "--until", "a visitor still wakes the workload", "scheduled and maintenance wakes wait", "asleep is woken", "RFC 3339"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("%d %s lacks %s", code, out, want)
		}
	}
	out, _, code = executed(t, build{}, "gcp", "workloads", "--help")
	for _, verb := range []string{"schedule", "pause", "resume"} {
		if code != 0 || !strings.Contains(out, verb) {
			t.Errorf("%d %s lacks %s", code, out, verb)
		}
	}
}

func TestGcpWorkloadsAccessReadsTheFlags(t *testing.T) {
	for _, verb := range []string{"show", "grant", "revoke"} {
		args := []string{"gcp", "workloads", "access", verb, "--project", "p", "--region", "europe-west1", "--gke-cluster", "europe-west1-b/c",
			"--namespace", "shop", "--workload", "web", "--json", "--gcloud-configuration", "conf"}
		want := gcponboard.Request{Project: "p", Region: "europe-west1", GKECluster: "europe-west1-b/c", Namespace: "shop", ID: "web", JSON: true, Configuration: "conf"}
		if verb != "show" {
			args = append(args, "--yes")
			want.Yes = true
		}
		calls, errs, exit := gcpRan(t, 0, args...)
		if exit != 0 || errs != untriedWarning || len(calls) != 1 || calls[0].run != "workloads access "+verb {
			t.Fatalf("%s: %d %q %v", verb, exit, errs, calls)
		}
		if !reflect.DeepEqual(calls[0].r, want) {
			t.Fatalf("%s: %+v", verb, calls[0].r)
		}
	}
}

func TestGcpWorkloadsAccessExitCodes(t *testing.T) {
	if _, _, exit := gcpRan(t, 1, "gcp", "workloads", "access", "grant", "--project", "p", "--region", "r", "--gke-cluster", "l/c", "--namespace", "n", "--workload", "w"); exit != 1 {
		t.Fatalf("exit %d", exit)
	}
	full := []string{"--project", "p", "--region", "r", "--gke-cluster", "l/c", "--namespace", "n", "--workload", "w"}
	for _, verb := range []string{"show", "grant", "revoke"} {
		for i := 0; i < len(full); i += 2 {
			args := append([]string{"gcp", "workloads", "access", verb}, append(append([]string{}, full[:i]...), full[i+2:]...)...)
			calls, errs, exit := gcpRan(t, 0, args...)
			if exit != 2 || len(calls) != 0 || !strings.Contains(errs, full[i]) {
				t.Errorf("%v: exit %d calls %v stderr %q", args, exit, calls, errs)
			}
		}
		calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "workloads", "access", verb, "extra"}, full...)...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%s with an argument: exit %d calls %v stderr %q", verb, exit, calls, errs)
		}
	}
	if calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "workloads", "access", "show", "--yes"}, full...)...); exit != 2 || len(calls) != 0 {
		t.Errorf("show changes nothing, so it takes no --yes: exit %d calls %v stderr %q", exit, calls, errs)
	}
}

func TestGcpWorkloadsAccessIsWiredInTheRoot(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "workloads", "--help")
	if code != 0 || !strings.Contains(out, "access") {
		t.Errorf("%d %s lacks access", code, out)
	}
	out, _, code = executed(t, build{}, "gcp", "workloads", "access", "--help")
	for _, verb := range []string{"show", "grant", "revoke"} {
		if code != 0 || !strings.Contains(out, verb) {
			t.Errorf("%d %s lacks %s", code, out, verb)
		}
	}
	out, _, code = executed(t, build{}, "gcp", "workloads", "access", "grant", "--help")
	for _, want := range []string{"--gke-cluster", "--namespace", "--workload", "--yes", "--json", "--project", "--region", "--gcloud-configuration", "dormeto-<workload>", "container.roles.escalate"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("%d %s lacks %s", code, out, want)
		}
	}
}

// sameJSON compares what is sent: a body is what it marshals to.
func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

const gcpSettingsArgs = "--project p --region europe-west1"

func settingsArgs(rest ...string) []string {
	return append(strings.Fields(gcpSettingsArgs), rest...)
}

func TestGcpWorkloadsSettingsReadTheFlagsIntoTheBody(t *testing.T) {
	args := append([]string{"gcp", "workloads", "settings", "shop", "web"}, settingsArgs("--idle-minutes", "30", "--wake-timeout-seconds", "900",
		"--failed-retry-seconds", "120", "--wake-mode", "browser", "--hibernate", "true", "--type", "batch")...)
	calls, errs, exit := gcpRan(t, 0, args...)
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "workloads settings" {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	r := calls[0].r
	want := map[string]any{"idleMinutes": 30, "wakeTimeoutSeconds": 900, "failedRetrySeconds": 120, "wakeMode": "browser", "hibernate": true, "type": "batch"}
	if !slices.Equal(r.IDs, []string{"shop", "web"}) || !sameJSON(r.Levels, want) || r.Project != "p" || r.Region != "europe-west1" {
		t.Fatalf("%+v", r)
	}
}

func TestGcpWorkloadsSettingsInheritSendsNull(t *testing.T) {
	args := append([]string{"gcp", "workloads", "settings", "shop"}, settingsArgs("--idle-minutes", "inherit", "--wake-mode", "inherit", "--hibernate", "inherit",
		"--type", "inherit", "--wake-timeout-seconds", "inherit", "--wait-seconds", "inherit", "--no-wake-rules-file", "inherit", "--wake-rules-file", "inherit")...)
	calls, errs, exit := gcpRan(t, 0, args...)
	if exit != 0 || errs != "" || len(calls) != 1 {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	want := map[string]any{"idleMinutes": nil, "wakeMode": nil, "hibernate": (*bool)(nil), "type": nil, "wakeTimeoutSeconds": nil, "waitSeconds": nil, "noWakeRules": nil, "wakeRules": nil}
	if !sameJSON(calls[0].r.Levels, want) {
		t.Fatalf("%#v", calls[0].r.Levels)
	}
}

func TestGcpWorkloadsSettingsPassWakeModeAnyOn(t *testing.T) {
	// The router decides by the workload's load balancer.
	calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "workloads", "settings", "shop"}, settingsArgs("--wake-mode", "any")...)...)
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].r.Levels["wakeMode"] != "any" {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
}

func TestGcpWorkloadsSettingsReadTheRulesFiles(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(file, []byte(`[{"name":"probe","pathPrefixes":["/health"]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "workloads", "settings", "shop"}, settingsArgs("--no-wake-rules-file", file)...)...)
	if exit != 0 || errs != "" || len(calls) != 1 {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	rules, _ := calls[0].r.Levels["noWakeRules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("%#v", calls[0].r.Levels)
	}
}

// The reasons are the router's (REASONS of gcp/src/levels.mjs), word for word.
var gcpRouterReasons = map[string]string{
	"--wait-seconds":  "waitSeconds is 0 on Google Cloud: the waker holds no request, it answers the waiting page at once and the page waits for the wake",
	"--max-holders":   "maxHolders is 0 on Google Cloud: no request is held",
	"--drain-seconds": "drainSeconds is 0 on Google Cloud: the nap's switch is a URL map change, and the backend service's own connection draining applies",
}

func TestGcpFixedSettingsAreRefusedBeforeAnyCallWithTheRoutersReasons(t *testing.T) {
	for flag, reason := range gcpRouterReasons {
		for _, command := range [][]string{{"gcp", "workloads", "settings", "shop"}, {"gcp", "routers", "settings"}} {
			calls, errs, exit := gcpRan(t, 0, append(slices.Clone(command), settingsArgs(flag, "30")...)...)
			if exit != 2 || len(calls) != 0 || !strings.Contains(errs, reason) {
				t.Errorf("%v %s: exit %d calls %v stderr %q", command, flag, exit, calls, errs)
			}
		}
	}
	// Given back to the levels, they are not refused.
	calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "workloads", "settings", "shop"}, settingsArgs("--wait-seconds", "inherit", "--max-holders", "inherit", "--drain-seconds", "inherit")...)...)
	if exit != 0 || errs != "" || len(calls) != 1 {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
}

func TestGcpWorkloadsSettingsRefuseWhatAwsRefuses(t *testing.T) {
	for _, rest := range [][]string{
		{"--idle-minutes", "2"},
		{"--idle-minutes", "x"},
		{"--wake-mode", "sometimes"},
		{"--hibernate", "maybe"},
		{"--type", "Not A Type"},
		{},
	} {
		calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "workloads", "settings", "shop"}, settingsArgs(rest...)...)...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: exit %d calls %v stderr %q", rest, exit, calls, errs)
		}
	}
	if calls, _, exit := gcpRan(t, 0, append([]string{"gcp", "workloads", "settings"}, settingsArgs("--idle-minutes", "30")...)...); exit != 2 || len(calls) != 0 {
		t.Errorf("no workload: exit %d calls %v", exit, calls)
	}
}

func TestGcpRoutersSettingsReadTheFlagsIntoTheBody(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "routers", "settings"}, settingsArgs("--hibernate", "true", "--join-minutes", "30", "--idle-minutes", "45",
		"--wake-timeout-seconds", "inherit", "--yes", "--json")...)...)
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "routers settings" {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	r := calls[0].r
	want := map[string]any{"hibernate": true, "joinMinutes": 30, "defaults": map[string]any{"idleMinutes": 45, "wakeTimeoutSeconds": nil}}
	if !sameJSON(r.Levels, want) || !r.Yes || !r.JSON || r.DryRun {
		t.Fatalf("%#v", r)
	}
	calls, _, _ = gcpRan(t, 0, append([]string{"gcp", "routers", "settings"}, settingsArgs("--hibernate", "inherit", "--join-minutes", "inherit", "--dry-run")...)...)
	if r := calls[0].r; !r.DryRun || !sameJSON(r.Levels, map[string]any{"hibernate": nil, "joinMinutes": nil}) {
		t.Fatalf("%#v", r)
	}
}

func TestGcpRoutersSettingsWithNoFlagShowTheLevel(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "routers", "settings"}, settingsArgs()...)...)
	if exit != 0 || errs != "" || len(calls) != 1 || len(calls[0].r.Levels) != 0 {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
}

func TestGcpRoutersSettingsRefuseABadValueBeforeAnyCall(t *testing.T) {
	for _, rest := range [][]string{{"--hibernate", "maybe"}, {"--join-minutes", "7"}, {"--idle-minutes", "2"}} {
		calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "routers", "settings"}, settingsArgs(rest...)...)...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: exit %d calls %v stderr %q", rest, exit, calls, errs)
		}
	}
}

func TestGcpFixedSettingsAreRefusedWhateverTheirValueBeforeTheRangeCheck(t *testing.T) {
	for flag, reason := range gcpRouterReasons {
		for _, value := range []string{"0", "200", "5", "x"} {
			for _, command := range [][]string{{"gcp", "workloads", "settings", "shop"}, {"gcp", "routers", "settings"}} {
				calls, errs, exit := gcpRan(t, 0, append(slices.Clone(command), settingsArgs(flag, value)...)...)
				if exit != 2 || len(calls) != 0 || !strings.Contains(errs, reason) || strings.Contains(errs, "whole number") {
					t.Errorf("%v %s %s: exit %d calls %v stderr %q", command, flag, value, exit, calls, errs)
				}
			}
		}
	}
}

func TestGcpSettingsFlagsHelpTellsTheTruthForTheCloud(t *testing.T) {
	for _, command := range [][]string{{"gcp", "workloads", "settings"}, {"gcp", "routers", "settings"}} {
		out, _, code := executed(t, build{}, append(slices.Clone(command), "--help")...)
		if code != 0 {
			t.Fatalf("%v: %d", command, code)
		}
		for _, flag := range []string{"--wait-seconds", "--max-holders", "--drain-seconds"} {
			line := helpLine(out, flag)
			if !strings.Contains(line, "fixed at 0 on Google Cloud") || strings.Contains(line, "to 120") || strings.Contains(line, "to 500") || strings.Contains(line, "to 300") {
				t.Errorf("%v %s: %q", command, flag, line)
			}
		}
		if line := helpLine(out, "--wake-timeout-seconds"); !strings.Contains(line, "60 to 1700") {
			t.Errorf("%v: %q", command, line)
		}
	}
}

// helpLine is the line of a flag in a command's help.
func helpLine(out, flag string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), flag+" ") {
			return l
		}
	}
	return ""
}

func TestGcpWorkloadsSettingsReadDryRunAndYes(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "workloads", "settings", "shop", "web"}, settingsArgs("--idle-minutes", "30", "--dry-run", "--yes")...)...)
	if exit != 0 || errs != "" || len(calls) != 1 || !calls[0].r.DryRun || !calls[0].r.Yes {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
}

const onboardArgs = "gcp workloads onboard --project p --region europe-west1 --url-map m --backend-service be"

// AWS's names: --wake-timeout-seconds, --wake-mode and --fallback-type (repeatable), each read into the request when given.
func TestGcpWorkloadsOnboardReadsTheWakeModeAndTheFallbackTypes(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, append(strings.Fields(onboardArgs), "--wake-mode", "browser", "--fallback-type", "e2-medium", "--fallback-type", "n2-standard-2", "--wake-timeout-seconds", "1700")...)
	if exit != 0 || errs != untriedWarning || len(calls) != 1 {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	r := calls[0].r
	if r.WakeMode != "browser" || !slices.Equal(r.FallbackTypes, []string{"e2-medium", "n2-standard-2"}) || r.WakeTimeout == nil || *r.WakeTimeout != 1700 {
		t.Fatalf("%+v", r)
	}
	calls, _, _ = gcpRan(t, 0, strings.Fields(onboardArgs)...)
	if r := calls[0].r; r.WakeMode != "" || r.FallbackTypes != nil || r.WakeTimeout != nil {
		t.Fatalf("flags not given must stay empty: %+v", r)
	}
}

func TestGcpWorkloadsOnboardRefusesABadWakeModeOrFallbackType(t *testing.T) {
	six := []string{"--fallback-type", "e2-medium", "--fallback-type", "e2-small", "--fallback-type", "n1-standard-1", "--fallback-type", "n2-standard-2", "--fallback-type", "n2-standard-4", "--fallback-type", "c2-standard-4"}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"six types":       {six, "at most 5"},
		"wake mode":       {[]string{"--wake-mode", "never"}, "--wake-mode"},
		"an EC2 type":     {[]string{"--fallback-type", "t3a.medium"}, "machine type"},
		"a type twice":    {[]string{"--fallback-type", "e2-small", "--fallback-type", "e2-small"}, "twice"},
		"an empty type":   {[]string{"--fallback-type", ""}, "--fallback-type"},
		"inherit on wake": {[]string{"--wake-mode", "inherit"}, "--wake-mode"},
		"an empty wake":   {[]string{"--wake-mode", ""}, "--wake-mode: empty"},
	} {
		calls, errs, exit := gcpRan(t, 0, append(strings.Fields(onboardArgs), c.args...)...)
		if exit != 2 || len(calls) != 0 || !strings.Contains(errs, c.want) {
			t.Errorf("%s: exit %d calls %v stderr %q", name, exit, calls, errs)
		}
	}
	calls, _, exit := gcpRan(t, 0, append(strings.Fields(onboardArgs), six[:10]...)...)
	if exit != 0 || len(calls[0].r.FallbackTypes) != 5 {
		t.Fatalf("five types: %d %v", exit, calls)
	}
}

// The VMs of unmanaged instance groups and of zonal network endpoint groups are the ones a fallback type applies to.
func TestGcpWorkloadsOnboardFallbackTypeHelpNamesTheVMsItAppliesTo(t *testing.T) {
	out, _, _ := executed(t, build{}, "gcp", "workloads", "onboard", "--help")
	if !strings.Contains(out, "VMs of unmanaged instance groups and zonal network endpoint groups") {
		t.Fatalf("%s", out)
	}
}

// The old name is a hidden alias that says what it is now.
func TestGcpWorkloadsOnboardTakesTheOldWakeTimeoutAndWarns(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, append(strings.Fields(onboardArgs), "--wake-timeout", "900")...)
	if exit != 0 || len(calls) != 1 || calls[0].r.WakeTimeout == nil || *calls[0].r.WakeTimeout != 900 || !strings.Contains(errs, "--wake-timeout is now --wake-timeout-seconds\n") {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	out, _, _ := executed(t, build{}, "gcp", "workloads", "onboard", "--help")
	if strings.Contains(out, "--wake-timeout ") || !strings.Contains(out, "--wake-timeout-seconds") || !strings.Contains(out, "--wake-mode") || !strings.Contains(out, "--fallback-type") {
		t.Fatalf("%s", out)
	}
	if _, errs, _ := gcpRan(t, 0, append(strings.Fields(onboardArgs), "--wake-timeout-seconds", "900")...); strings.Contains(errs, "is now") {
		t.Fatalf("warned of the new flag: %q", errs)
	}
}

// One range for the wake timeout on Google Cloud: 60 to 1700, onboard and settings alike.
func TestGcpWakeTimeoutRangeIsOneForOnboardAndSettings(t *testing.T) {
	for _, command := range [][]string{{"gcp", "workloads", "settings", "shop"}, {"gcp", "routers", "settings"}} {
		for _, n := range []string{"59", "1701", "3600"} {
			calls, errs, exit := gcpRan(t, 0, append(slices.Clone(command), settingsArgs("--wake-timeout-seconds", n)...)...)
			if exit != 2 || len(calls) != 0 || !strings.Contains(errs, "60 to 1700") {
				t.Errorf("%v %s: exit %d calls %v stderr %q", command, n, exit, calls, errs)
			}
		}
		for _, n := range []string{"60", "1700"} {
			if calls, errs, exit := gcpRan(t, 0, append(slices.Clone(command), settingsArgs("--wake-timeout-seconds", n)...)...); exit != 0 || len(calls) != 1 {
				t.Errorf("%v %s: exit %d calls %v stderr %q", command, n, exit, calls, errs)
			}
		}
	}
}

func pageArgs(rest ...string) []string {
	return append(strings.Fields(gcpSettingsArgs), rest...)
}

func TestGcpWaitingPageSetReadsATemplateIntoThePage(t *testing.T) {
	icon := filepath.Join(t.TempDir(), "i.png")
	if err := os.WriteFile(icon, append([]byte("\x89PNG\r\n\x1a\n"), 1, 2, 3), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		command []string
		run     string
	}{
		{[]string{"gcp", "workloads", "waiting-page", "set", "shop"}, "workloads waiting-page set"},
		{[]string{"gcp", "routers", "waiting-page", "set"}, "routers waiting-page set"}} {
		calls, errs, exit := gcpRan(t, 0, append(slices.Clone(c.command), pageArgs("--title", "Back soon", "--description", "A minute", "--icon", icon)...)...)
		if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != c.run {
			t.Fatalf("%v: %d %q %v", c.command, exit, errs, calls)
		}
		p := calls[0].r.Page
		if p == nil || p.Mode != "template" || *p.Title != "Back soon" || *p.Description != "A minute" || !strings.HasPrefix(*p.Icon, "data:image/png;base64,") || p.HTML != nil {
			t.Fatalf("%+v", p)
		}
	}
	calls, _, _ := gcpRan(t, 0, append([]string{"gcp", "workloads", "waiting-page", "set", "shop"}, pageArgs("--icon", "none")...)...)
	if r := calls[0].r; r.ID != "shop" || r.Page == nil || *r.Page.Icon != "none" || r.Page.Title != nil {
		t.Fatalf("%+v", r)
	}
}

func TestGcpWaitingPageSetReadsACustomPage(t *testing.T) {
	file := filepath.Join(t.TempDir(), "p.html")
	if err := os.WriteFile(file, []byte("<h1>zzz</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "routers", "waiting-page", "set"}, pageArgs("--html", file)...)...)
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].r.Page == nil || calls[0].r.Page.Mode != "custom" || *calls[0].r.Page.HTML != "<h1>zzz</h1>" {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
}

func TestGcpWaitingPageSetRefusesWhatAwsRefusesBeforeAnyCall(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.png")
	if err := os.WriteFile(big, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 70<<10)...), 0o600); err != nil {
		t.Fatal(err)
	}
	html := filepath.Join(dir, "p.html")
	if err := os.WriteFile(html, []byte("<p>x</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		rest []string
		want string
	}{
		{[]string{"--html", html, "--title", "x"}, "--html writes a custom page: it cannot go with --title"},
		{[]string{"--html", html, "--icon", "none"}, "cannot go with --icon"},
		{[]string{"--icon", big}, "the icon is larger than 64 KiB"},
		{[]string{}, "give --title, --description or --icon for a template, or --html for a custom page"},
	} {
		for _, command := range [][]string{{"gcp", "workloads", "waiting-page", "set", "shop"}, {"gcp", "routers", "waiting-page", "set"}} {
			calls, errs, exit := gcpRan(t, 0, append(slices.Clone(command), pageArgs(c.rest...)...)...)
			if exit != 2 || len(calls) != 0 || !strings.Contains(errs, c.want) {
				t.Errorf("%v %v: exit %d calls %v stderr %q", command, c.rest, exit, calls, errs)
			}
		}
	}
}

func TestGcpWaitingPageShowAndClearReadTheTarget(t *testing.T) {
	for _, verb := range []string{"show", "clear"} {
		calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "workloads", "waiting-page", verb, "shop"}, pageArgs("--json")...)...)
		if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "workloads waiting-page "+verb || calls[0].r.ID != "shop" || !calls[0].r.JSON {
			t.Fatalf("%s: %d %q %v", verb, exit, errs, calls)
		}
		calls, _, exit = gcpRan(t, 0, append([]string{"gcp", "routers", "waiting-page", verb}, pageArgs()...)...)
		if exit != 0 || len(calls) != 1 || calls[0].run != "routers waiting-page "+verb {
			t.Fatalf("%s: %d %v", verb, exit, calls)
		}
		if _, _, exit = gcpRan(t, 0, append([]string{"gcp", "workloads", "waiting-page", verb}, pageArgs()...)...); exit != 2 {
			t.Fatalf("%s without a workload: exit %d", verb, exit)
		}
	}
}

// --database is Cloud SQL's flag on both clouds' onboards; --cloud-sql is its hidden alias, saying what it is now.
func TestGcpWorkloadsOnboardTakesDatabaseAndTheOldCloudSQL(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, append(strings.Fields(onboardArgs), "--database", "db1", "--database", "db2")...)
	if exit != 0 || len(calls) != 1 || !slices.Equal(calls[0].r.CloudSQL, []string{"db1", "db2"}) || strings.Contains(errs, "is now") {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	calls, errs, exit = gcpRan(t, 0, append(strings.Fields(onboardArgs), "--cloud-sql", "db1")...)
	if exit != 0 || len(calls) != 1 || !slices.Equal(calls[0].r.CloudSQL, []string{"db1"}) || !strings.Contains(errs, "--cloud-sql is now --database\n") {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	calls, _, _ = gcpRan(t, 0, append(strings.Fields(onboardArgs), "--cloud-sql", "db1", "--database", "db2")...)
	// --database's, then the old flag's: the order the code sends
	if len(calls) != 1 || !slices.Equal(calls[0].r.CloudSQL, []string{"db2", "db1"}) {
		t.Fatalf("%v", calls)
	}
	out, _, _ := executed(t, build{}, "gcp", "workloads", "onboard", "--help")
	if strings.Contains(out, "--cloud-sql ") || !strings.Contains(out, "--database") {
		t.Fatalf("%s", out)
	}
}
