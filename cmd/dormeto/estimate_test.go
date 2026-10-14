package main

// `dormeto estimate`: its arguments passed as they are to the command, its exit codes (0, 1, 2, a signal's), its
// usage; the real dependencies.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/options"
)

// execute runs the command line, its output and errors captured.
func executed(t *testing.T, b build, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errs bytes.Buffer
	root := newRoot(b)
	root.SetOut(&out)
	root.SetErr(&errs)
	root.SetArgs(args)
	code = execute(root)
	return out.String(), errs.String(), code
}

func TestEstimateHelpPrintsTheUsageExitCode0(t *testing.T) {
	for _, args := range [][]string{{"estimate", "--help"}, {"estimate", "-h"}, {"help", "estimate"}} {
		out, errs, code := executed(t, build{}, args...)
		if code != 0 || out != options.Usage+"\n" || errs != "" {
			t.Errorf("%q: exit code %d, out %q, errors %q", args, code, out, errs)
		}
	}
	if out, _, _ := executed(t, build{}, "--help"); !strings.Contains(out, "estimate") {
		t.Errorf("the commands listed without estimate:\n%s", out)
	}
}

func TestEstimateABadOptionItsMessageAndTheUsageExitCode2(t *testing.T) {
	out, errs, code := executed(t, build{}, "estimate", "--profile", "p", "--window", "3")
	if code != 2 || out != "" || errs != "--window: whole minutes from 5 to 1440\n"+options.Usage+"\n" {
		t.Fatalf("exit code %d, out %q, errors %q", code, out, errs)
	}
	// Flags are the command's, never cobra's.
	if _, errs, code := executed(t, build{}, "estimate", "--profile=p"); code != 2 || !strings.HasPrefix(errs, "unknown option: --profile=p\n") {
		t.Fatalf("exit code %d, errors %q", code, errs)
	}
}

func TestEstimateAProfileMissingFromTheConfigAWSsMessageExitCode1NothingSent(t *testing.T) {
	home := t.TempDir()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	for _, k := range []string{"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("AWS_ENDPOINT_URL", srv.URL)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(home, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, "credentials"))
	if err := os.WriteFile(filepath.Join(home, "config"), []byte("[profile other]\nregion = eu-west-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errs, code := executed(t, build{}, "estimate", "--profile", "nope", "--out", t.TempDir())
	// What it was reading, then AWS's message.
	if code != 1 || out != "Reading the profile nope: its account and its organization\n" || errs != "Error: failed to get shared config profile, nope\n" {
		t.Fatalf("exit code %d, out %q, errors %q", code, out, errs)
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests sent", hits.Load())
	}
}

func TestTheEstimatesDependenciesTheBuildsVersionTheCacheDirectoryNoTerminalStatusForAWriter(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/somewhere")
	var out, errs bytes.Buffer
	d := dependencies(t.Context(), &out, &errs, build{version: "1.2.3"})
	if d.Version != "1.2.3" || d.CacheRoot != filepath.Join("/somewhere", "dormeto", "estimate") || d.Status != nil || d.AWS == nil || d.Context == nil {
		t.Fatalf("%+v", d)
	}
	if now := d.Now(); time.Since(now) > time.Minute {
		t.Fatalf("now %v", now)
	}
	d.Log("a line")
	d.Error("an error")
	if out.String() != "a line\n" || errs.String() != "an error\n" {
		t.Fatalf("out %q, errors %q", out.String(), errs.String())
	}
	if d := dependencies(t.Context(), &out, &errs, build{}); d.Version != "dev" {
		t.Fatalf("version %q", d.Version)
	}
}

func TestASignalsExitCodeIs128AndItsNumberAsAShellSaysOfAProcessItKilled(t *testing.T) {
	if got := signalCode(os.Interrupt); got != 130 {
		t.Errorf("SIGINT: %d", got)
	}
	if got := signalCode(syscall.SIGTERM); got != 143 {
		t.Errorf("SIGTERM: %d", got)
	}
}
