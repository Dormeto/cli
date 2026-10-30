package main

import (
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
	if calls[0].r != want {
		t.Fatalf("%+v", calls[0].r)
	}
}

func TestGcpWorkloadsWithAnIDTakeItAsTheArgument(t *testing.T) {
	for _, verb := range []string{"show", "offboard", "nap", "wake"} {
		calls, errs, exit := gcpRan(t, 0, "gcp", "workloads", verb, "shop", "--project", "p", "--region", "europe-west1")
		if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "workloads "+verb {
			t.Fatalf("%s: %d %q %v", verb, exit, errs, calls)
		}
		if want := (gcponboard.Request{Project: "p", Region: "europe-west1", ID: "shop"}); calls[0].r != want {
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
		"--backend-service", "be", "--id", "shop", "--idle-minutes", "30", "--wait-seconds", "20", "--hibernate", "false", "--dry-run", "--json")
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "workloads onboard" {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	r := calls[0].r
	if r.URLMap != "m" || r.BackendService != "be" || r.ID != "shop" || !r.DryRun || !r.JSON ||
		r.IdleMinutes == nil || *r.IdleMinutes != 30 || r.WaitSeconds == nil || *r.WaitSeconds != 20 || r.Hibernate == nil || *r.Hibernate {
		t.Fatalf("%+v", r)
	}
	calls, _, _ = gcpRan(t, 0, "gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "be")
	if r := calls[0].r; r.IdleMinutes != nil || r.WaitSeconds != nil || r.Hibernate != nil || r.ID != "" {
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
	for _, flag := range []string{"--url-map", "--backend-service", "--id", "--idle-minutes", "--wait-seconds", "--hibernate", "--dry-run", "--project", "--region", "--json"} {
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
