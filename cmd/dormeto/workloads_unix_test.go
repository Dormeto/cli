//go:build unix

package main

// Ctrl-C while keep-awake --wait follows: exit 130, the pause stays and the output says so.

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestKeepAwakeWaitSIGINTExits130AndSaysThePauseStays(t *testing.T) {
	a := &wlAPI{statuses: []string{`{"state":"WAKING"}`}}
	r := runWL(t, a, func(d *fleetDeps) {
		sent := false
		d.sleep = func(ctx context.Context, _ time.Duration) error {
			if !sent {
				sent = true
				syscall.Kill(os.Getpid(), syscall.SIGINT)
			}
			<-ctx.Done()
			return ctx.Err()
		}
	}, "keep-awake", "prod-eu/w-1", "--for", "1h", "--wait")
	if r.exit != 130 || !strings.Contains(r.stderr, "the pause stays") || len(a.writes) != 1 {
		t.Errorf("exit %d stderr %q writes %q", r.exit, r.stderr, a.writes)
	}
}
