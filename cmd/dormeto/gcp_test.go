package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/gcponboard"
)

// gcpCall is one run of a gcp command: which, with what.
type gcpCall struct {
	run string
	r   gcponboard.Request
	d   gcponboard.Deps
}

func gcpRan(t *testing.T, code int, args ...string) (calls []gcpCall, stderr string, exit int) {
	t.Helper()
	ran := func(name string) gcpRun {
		return func(_ context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			calls = append(calls, gcpCall{name, r, d})
			return code
		}
	}
	runs := gcpRuns{check: ran("check"), install: ran("install")}
	root := &cobra.Command{Use: "dormeto", SilenceUsage: true}
	root.AddCommand(newGcp(runs, "1.2.3"))
	var out, errs bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errs)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	exit = execute(root)
	return calls, errs.String(), exit
}

func TestGcpCheckFlagsReadIntoTheRequest(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "check", "--project", "dormeto-dev", "--region", "europe-west1", "--json")
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "check" {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	want := gcponboard.Request{Project: "dormeto-dev", Region: "europe-west1", JSON: true}
	if calls[0].r != want {
		t.Fatalf("%+v", calls[0].r)
	}
}

func TestGcpCheckExitCodes(t *testing.T) {
	if _, _, exit := gcpRan(t, 1, "gcp", "check", "--project", "p"); exit != 1 {
		t.Fatalf("exit %d", exit)
	}
	for _, args := range [][]string{{"gcp", "check"}, {"gcp", "check", "--project", "p", "--nope"}, {"gcp", "check", "--project", "p", "extra"}} {
		calls, _, exit := gcpRan(t, 0, args...)
		if exit != 2 || len(calls) != 0 {
			t.Errorf("%v: exit %d calls %v", args, exit, calls)
		}
	}
}

func TestGcpCheckIsWiredInTheRoot(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "check", "--help")
	if code != 0 || !strings.Contains(out, "--gcloud-configuration") || !strings.Contains(out, "--project") {
		t.Fatalf("%d %s", code, out)
	}
}

func TestGcpInstallFlagsReadIntoTheRequest(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	calls, errs, exit := gcpRan(t, 0, "gcp", "install", "--project", "dormeto-dev", "--region", "europe-west1", "--yes", "--dry-run",
		"--gcloud-configuration", "dormeto-dev")
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "install" {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	want := gcponboard.Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true, DryRun: true}
	if calls[0].r != want {
		t.Fatalf("%+v", calls[0].r)
	}
	d := calls[0].d
	if d.Version != "1.2.3" || d.Cache != cache || d.Now == nil || d.Sleep == nil || d.Prompter == nil || d.Terminal ||
		d.Google == nil || d.Google.Tokens == nil || d.Out == nil || d.Err == nil {
		t.Fatalf("%+v", d)
	}
}

func TestGcpInstallCacheDefaultsToTheHomeCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", home)
	calls, _, exit := gcpRan(t, 0, "gcp", "install", "--project", "p", "--region", "europe-west1")
	if exit != 0 || len(calls) != 1 || calls[0].d.Cache != filepath.Join(home, ".cache") {
		t.Fatalf("%d %+v", exit, calls)
	}
}

func TestGcpInstallExitCodes(t *testing.T) {
	if _, _, exit := gcpRan(t, 1, "gcp", "install", "--project", "p", "--region", "europe-west1"); exit != 1 {
		t.Fatalf("exit %d", exit)
	}
	for _, args := range [][]string{
		{"gcp", "install", "--region", "europe-west1"},
		{"gcp", "install", "--project", "p"},
		{"gcp", "install", "--project", "p", "--region", "europe-west1", "--nope"},
		{"gcp", "install", "--project", "p", "--region", "europe-west1", "extra"},
	} {
		calls, errs, exit := gcpRan(t, 0, args...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: exit %d calls %v stderr %q", args, exit, calls, errs)
		}
	}
}

func TestGcpInstallIsWiredInTheRoot(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "install", "--help")
	for _, flag := range []string{"--project", "--region", "--gcloud-configuration", "--yes", "--dry-run"} {
		if code != 0 || !strings.Contains(out, flag) {
			t.Errorf("%d %s lacks %s", code, out, flag)
		}
	}
}
