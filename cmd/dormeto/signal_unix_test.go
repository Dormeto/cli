//go:build unix

package main

// Ctrl-C (SIGINT) or a kill (SIGTERM) cancels the run's context and is said, so the command exits with its code.

import (
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/install"
)

func TestTheFirstSignalCancelsTheRunAndIsKept(t *testing.T) {
	for _, s := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		ctx, signalled, stop := interruptible(t.Context())
		if signalled() != nil {
			t.Fatal("a signal before any")
		}
		if err := syscall.Kill(os.Getpid(), s); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("%v: the run's context not cancelled", s)
		}
		if got := signalled(); got != s {
			t.Fatalf("signalled %v, want %v", got, s)
		}
		stop()
	}
}

func TestStopReleasesTheSignalsAndCancelsNothingElse(t *testing.T) {
	ctx, signalled, stop := interruptible(t.Context())
	stop()
	stop() // twice: harmless
	if signalled() != nil {
		t.Fatal("a signal")
	}
	<-ctx.Done() // the run is over: its context is released
}

// SIGTERM while an install command runs: its run sees the context cancelled, the command exits with 143.
func TestInstallSIGTERMExitCode143(t *testing.T) {
	root := &cobra.Command{Use: "dormeto"}
	run := func(ctx context.Context, _ install.Deps, _ install.Request) int {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the run's context not cancelled")
		}
		return 130
	}
	root.AddCommand(newInstall(installRuns{install: run, check: run, apply: run}, "dev"))
	root.SetArgs([]string{"install", "apply"})
	root.SetIn(strings.NewReader(""))
	root.SetOut(io.Discard)
	if code := execute(root); code != 143 {
		t.Fatalf("exit code %d, want 143", code)
	}
}
