//go:build unix

package main

// Ctrl-C (SIGINT) or a kill (SIGTERM) cancels the run's context and is said, so the command exits with its code.

import (
	"os"
	"syscall"
	"testing"
	"time"
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
