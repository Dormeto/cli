//go:build unix

package main

// Ctrl-C while the upgrade asks, and while it follows.

import (
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestUpgradeCtrlCAtThePromptExits130AndStartsNothing(t *testing.T) {
	api := &fakeAPI{routers: upgradeRouters}
	r := runFleetWith(t, api, "eu-west-1", func(d *fleetDeps) {
		d.terminal = func(io.Reader) bool { return true }
		d.confirm = func(ctx context.Context, _ io.Reader, _ io.Writer, _ string) (bool, error) {
			if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
				t.Error(err)
			}
			<-ctx.Done()
			return false, ctx.Err()
		}
	}, "routers", "upgrade", "--all")
	if r.exit != 130 || len(api.posted) != 0 {
		t.Errorf("exit %d, posted %v, stderr %q", r.exit, api.posted, r.stderr)
	}
}

func TestUpgradeCtrlCWhileFollowingExits130AndSaysTheUpgradesGoOn(t *testing.T) {
	api := &fakeAPI{routers: upgradeRouters, endWith: "running"}
	r := runFleetWith(t, api, "eu-west-1", func(d *fleetDeps) {
		d.sleep = func(ctx context.Context, _ time.Duration) error {
			_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
			<-ctx.Done()
			return ctx.Err()
		}
	}, "routers", "upgrade", "r1", "--yes")
	if r.exit != 130 || !strings.Contains(r.stdout+r.stderr, "the started upgrades go on in the manager: follow them in its Operations view") {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
}

func TestPromptConfirmStopsWhenTheContextEnds(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(20*time.Millisecond, cancel)
	if ok, err := promptConfirm(ctx, pr, io.Discard, "Upgrade?"); ok || err == nil {
		t.Errorf("ok %v, err %v", ok, err)
	}
}
