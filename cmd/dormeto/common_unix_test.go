//go:build unix

package main

// Ctrl-C while operations are followed: exit 130, each one still running named with how to follow it, none reported
// failed.

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunOperationsSIGINTExits130AndSaysWhatStillRuns(t *testing.T) {
	a := &opsAPI{stay: true}
	r := runOps(t, a, func(d *fleetDeps) {
		sent := false
		d.sleep = func(ctx context.Context, _ time.Duration) error {
			if !sent {
				sent = true
				_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
			}
			<-ctx.Done()
			return ctx.Err()
		}
	}, "a")
	if r.exit != 130 || !strings.Contains(r.stderr, "a: still running in the manager: dormeto operations watch op-a\n") {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
	if strings.Contains(r.stdout+r.stderr, "✗") {
		t.Errorf("reported as failed: %q %q", r.stdout, r.stderr)
	}
}
