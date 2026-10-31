package main

import (
	"reflect"
	"strings"
	"testing"

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
	if r := calls[0].r; r.IdleMinutes != nil || r.Hibernate != nil || r.ID != "" {
		t.Fatalf("settings not given must stay nil: %+v", r)
	}
	// A zero is a value given, for the range check to refuse.
	calls, _, _ = gcpRan(t, 0, "gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "be", "--idle-minutes", "0")
	if r := calls[0].r; r.IdleMinutes == nil || *r.IdleMinutes != 0 {
		t.Fatalf("%+v", r)
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
	for _, verb := range []string{"show", "set", "clear"} {
		args := []string{"gcp", "workloads", "schedule", verb, "shop", "--project", "p", "--region", "europe-west1", "--json"}
		want := gcponboard.Request{Project: "p", Region: "europe-west1", ID: "shop", JSON: true}
		if verb == "set" {
			args = append(args, "--file", "s.json")
			want.File = "s.json"
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
		if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "workloads access "+verb {
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
