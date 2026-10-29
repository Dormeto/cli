package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/gcponboard"
)

func gcpRan(t *testing.T, code int, args ...string) (calls []gcponboard.Request, stderr string, exit int) {
	t.Helper()
	runs := gcpRuns{check: func(_ context.Context, _ gcponboard.Deps, r gcponboard.Request) int {
		calls = append(calls, r)
		return code
	}}
	root := &cobra.Command{Use: "dormeto", SilenceUsage: true}
	root.AddCommand(newGcp(runs))
	var out, errs bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errs)
	root.SetArgs(args)
	exit = execute(root)
	return calls, errs.String(), exit
}

func TestGcpCheckFlagsReadIntoTheRequest(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "check", "--project", "dormeto-dev", "--region", "europe-west1", "--json")
	if exit != 0 || errs != "" || len(calls) != 1 {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	want := gcponboard.Request{Project: "dormeto-dev", Region: "europe-west1", JSON: true}
	if calls[0] != want {
		t.Fatalf("%+v", calls[0])
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
