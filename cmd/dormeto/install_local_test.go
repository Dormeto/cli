package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/installlocal"
)

// localRan runs `dormeto aws install local <args>` with a fake run, and gives what it was called with.
func localRan(t *testing.T, args ...string) (calls []installlocal.Request, stderr string, exit int) {
	t.Helper()
	root := &cobra.Command{Use: "dormeto", SilenceUsage: true}
	aws := &cobra.Command{Use: "aws"}
	root.AddCommand(aws)
	runs := installRuns{local: func(_ context.Context, d installlocal.Deps, r installlocal.Request) int {
		if d.Run == nil || d.LookPath == nil || d.Out == nil {
			t.Error("dependencies missing")
		}
		calls = append(calls, r)
		return 0
	}}
	aws.AddCommand(newInstall(runs, "0.2.0"))
	var out, errs bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errs)
	exit = execute(root, append([]string{"aws", "install", "local"}, args...)...)
	return calls, errs.String(), exit
}

// The directory is the argument (the current one without it), --env and --dry-run read; the install's AWS flags refused.
func TestInstallLocalReadsItsArgumentAndFlags(t *testing.T) {
	calls, errs, exit := localRan(t, "infra/terraform/org", "--env", "dev", "--dry-run", "--verbose")
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0] != (installlocal.Request{Dir: "infra/terraform/org", Env: "dev", DryRun: true, Verbose: true}) {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
	calls, _, _ = localRan(t)
	if len(calls) != 1 || calls[0] != (installlocal.Request{Dir: ".", Env: "prod"}) {
		t.Fatalf("defaults: %+v", calls)
	}
	// --yes, as every write takes it: a no-op.
	if calls, errs, exit := localRan(t, "--yes"); exit != 0 || len(calls) != 1 {
		t.Fatalf("--yes: %d %q", exit, errs)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--profile", "x"}, "--profile: not used by install local: the configuration's providers and backend name the accounts"},
		{[]string{"--region", "eu-west-1"}, "--region: not used by install local"},
		{[]string{"--version", "0.24.0"}, "--version: not used by install local"},
		{[]string{"--env", "Dev"}, `--env: "Dev" is not 1 to 10 lowercase letters, digits or inner hyphens`},
		{[]string{"a", "b"}, "give one directory at most"},
	} {
		calls, errs, exit := localRan(t, c.args...)
		if exit != 2 || len(calls) != 0 || !strings.Contains(errs, c.want) {
			t.Errorf("%v: exit %d %q", c.args, exit, errs)
		}
	}
}

// The help says what it checks and runs, that running it is the confirmation, and the commands it replaces.
func TestInstallLocalHelp(t *testing.T) {
	c, _, err := newRoot(build{}).Find([]string{"aws", "install", "local"})
	if err != nil || c.Name() != "local" {
		t.Fatalf("%v %v", c, err)
	}
	long := strings.ReplaceAll(c.Long, "\n", " ")
	for _, w := range []string{"tfenv exec", "env/<env>.tfvars", "env/<env>.backend.hcl", "env/<env>.local.tfvars", "build it first",
		"-detailed-exitcode", "--dry-run", "running this command is the confirmation", "cd <directory>"} {
		if !strings.Contains(long, w) {
			t.Errorf("help lacks %q:\n%s", w, c.Long)
		}
	}
}

// The real runner: the directory, the exit code, the output, the standard input closed.
func TestExecRunnerRunsInTheDirectoryWithoutInput(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	run := execRunner(func() os.Signal { return nil })
	code, err := run(context.Background(), dir, "sh", []string{"-c", "pwd; read x || echo closed; exit 2"}, &out, &out)
	if err != nil || code != 2 || !strings.Contains(out.String(), "closed") || !strings.Contains(out.String(), filepath.Base(dir)) {
		t.Fatalf("%d %v %q", code, err, out.String())
	}
}
