package main

// `dormeto routers upgrade` against the fake manager API: the plan, the confirmation, what is started, what is printed
// and the exit codes.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// operations answers POST /iam/operations (202, or 409 for a router in startErr) and GET /iam/operations/<id>.
func (f *fakeAPI) operations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		id := strings.TrimPrefix(r.URL.Path, "/iam/operations/")
		switch f.endWith {
		case "failed":
			fmt.Fprintf(w, `{"id":%q,"type":"upgrade-router","status":"failed","steps":[{"name":"backup","status":"done"},{"name":"update-stack","status":"failed","detail":"role missing"}]}`, id)
		case "running":
			fmt.Fprintf(w, `{"id":%q,"type":"upgrade-router","status":"running","steps":[]}`, id)
		default:
			fmt.Fprintf(w, `{"id":%q,"type":"upgrade-router","status":"succeeded","steps":[]}`, id)
		}
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct{ Router string }
	_ = json.Unmarshal(body, &req)
	f.mu.Lock()
	f.posted = append(f.posted, string(body))
	f.mu.Unlock()
	if msg := f.startErr[req.Router]; msg != "" {
		w.WriteHeader(409)
		fmt.Fprintf(w, `{"error":"conflict","message":%q,"operationId":"op-x"}`, msg)
		return
	}
	w.WriteHeader(202)
	fmt.Fprintf(w, `{"operation":{"id":"op-%s","type":"upgrade-router","status":"running","steps":[]}}`, req.Router)
}

const upgradeRouters = `{"routers":[
 {"name":"r1","region":"eu-west-1","account":"1","source":"manager","version":"0.29.0"},
 {"name":"r2","region":"eu-west-1","account":"1","source":"manager","version":"0.28.0"},
 {"name":"r3","region":"eu-west-1","account":"1","source":"manager","version":"0.30.0"},
 {"name":"tf","region":"eu-west-1","source":"terraform","version":"0.20.0"}
],"available":{"version":"0.30.0"}}`

func withTerminal(answer bool, asked *[]string) func(*fleetDeps) {
	return func(d *fleetDeps) {
		d.terminal = func(io.Reader) bool { return true }
		d.confirm = func(_ context.Context, _ io.Reader, _ io.Writer, q string) (bool, error) {
			*asked = append(*asked, q)
			return answer, nil
		}
	}
}

func TestUpgradeWithoutTerminalNorYesExits2AfterThePlanAndStartsNothing(t *testing.T) {
	api := &fakeAPI{routers: upgradeRouters}
	r := runFleet(t, api, "eu-west-1", "routers", "upgrade", "--all")
	if r.exit != 2 || len(api.posted) != 0 {
		t.Fatalf("exit %d, posted %v, stderr %q", r.exit, api.posted, r.stderr)
	}
	if !strings.Contains(r.stdout, "2 routers to upgrade to 0.30.0") || !strings.Contains(r.stderr, "--yes") {
		t.Errorf("stdout %q, stderr %q", r.stdout, r.stderr)
	}
}

func TestUpgradeDryRunPrintsThePlanAndStartsNothing(t *testing.T) {
	api := &fakeAPI{routers: upgradeRouters}
	r := runFleet(t, api, "eu-west-1", "routers", "upgrade", "--all", "--dry-run")
	t.Logf("\n%s", r.stdout)
	if r.exit != 0 || len(api.posted) != 0 {
		t.Fatalf("exit %d, posted %v, stderr %q", r.exit, api.posted, r.stderr)
	}
	for _, w := range []string{"r1  0.29.0 → 0.30.0", "r2  0.28.0 → 0.30.0", "2 skipped", "r3  up to date", "written in Terraform"} {
		if !strings.Contains(r.stdout, w) {
			t.Errorf("stdout lacks %q", w)
		}
	}
}

func TestUpgradeYesStartsThePlannedRoutersOnly(t *testing.T) {
	api := &fakeAPI{routers: upgradeRouters}
	r := runFleet(t, api, "eu-west-1", "routers", "upgrade", "r1", "r1", "r3", "--name", "r*", "--yes")
	t.Logf("\n%s", r.stdout)
	if r.exit != 0 || len(api.posted) != 1 || api.posted[0] != `{"router":"r1","type":"upgrade-router"}` {
		t.Fatalf("exit %d, posted %v, stderr %q", r.exit, api.posted, r.stderr)
	}
	if !strings.Contains(r.stdout, "✓ r1 0.29.0 → 0.30.0 in ") || !strings.Contains(r.stdout, "1 upgraded") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestUpgradeBadArgumentsExit2(t *testing.T) {
	for _, args := range [][]string{
		{"routers", "upgrade"},
		{"routers", "upgrade", "--all", "r1"},
		{"routers", "upgrade", "nope", "--yes"},
		{"routers", "upgrade", "--all", "--concurrency", "0"},
		{"routers", "upgrade", "--all", "--concurrency", "21"},
		{"routers", "upgrade", "--all", "--timeout", "0s"},
	} {
		api := &fakeAPI{routers: upgradeRouters}
		r := runFleet(t, api, "eu-west-1", args...)
		if r.exit != 2 || len(api.posted) != 0 || r.stderr == "" {
			t.Errorf("%v: exit %d, posted %v, stderr %q", args, r.exit, api.posted, r.stderr)
		}
	}
}

func TestUpgradeAsksOnATerminal(t *testing.T) {
	var asked []string
	api := &fakeAPI{routers: upgradeRouters}
	r := runFleetWith(t, api, "eu-west-1", withTerminal(true, &asked), "routers", "upgrade", "--all")
	if r.exit != 0 || len(api.posted) != 2 || len(asked) != 1 || asked[0] != "Upgrade 2 routers to 0.30.0?" {
		t.Fatalf("exit %d, posted %v, asked %q, stderr %q", r.exit, api.posted, asked, r.stderr)
	}
	asked, api = nil, &fakeAPI{routers: upgradeRouters}
	r = runFleetWith(t, api, "eu-west-1", withTerminal(false, &asked), "routers", "upgrade", "--all")
	if r.exit != 0 || len(api.posted) != 0 || !strings.Contains(r.stdout, "not confirmed: nothing started") {
		t.Errorf("declined: exit %d, posted %v, stderr %q", r.exit, api.posted, r.stderr)
	}
}

func TestUpgradeNothingToUpgradeStartsNothingAndExits0(t *testing.T) {
	api := &fakeAPI{routers: upgradeRouters}
	r := runFleet(t, api, "eu-west-1", "routers", "upgrade", "r3", "tf") // no --yes needed: nothing to confirm
	if r.exit != 0 || len(api.posted) != 0 || !strings.Contains(r.stdout, "nothing to upgrade") {
		t.Errorf("exit %d, posted %v, stdout %q, stderr %q", r.exit, api.posted, r.stdout, r.stderr)
	}
}

func TestUpgradeFailedExits1WithTheStepAndOperation(t *testing.T) {
	api := &fakeAPI{routers: upgradeRouters, endWith: "failed"}
	r := runFleet(t, api, "eu-west-1", "routers", "upgrade", "r1", "--yes")
	if r.exit != 1 || !strings.Contains(r.stdout, "✗ r1: update-stack: role missing (operation op-r1)") {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
}

func TestUpgradeRefusedStartCountsAsFailed(t *testing.T) {
	api := &fakeAPI{routers: upgradeRouters, startErr: map[string]string{"r1": "an operation already runs on router r1"}}
	r := runFleet(t, api, "eu-west-1", "routers", "upgrade", "r1", "r2", "--yes")
	if r.exit != 1 || !strings.Contains(r.stdout, "✗ r1: an operation already runs on router r1") || !strings.Contains(r.stdout, "✓ r2") {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
}

func TestUpgradeJSONResultOnStdoutPlanAndProgressOnStderr(t *testing.T) {
	api := &fakeAPI{routers: upgradeRouters}
	r := runFleet(t, api, "eu-west-1", "routers", "upgrade", "--all", "--yes", "--json")
	var got struct {
		Available string
		Upgraded  []struct{ Router, From, To, Operation, Status string }
		Skipped   []struct{ Router, Reason string }
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("%v: %q", err, r.stdout)
	}
	if r.exit != 0 || got.Available != "0.30.0" || len(got.Upgraded) != 2 || got.Upgraded[0].Status != "succeeded" || got.Upgraded[0].Operation != "op-r1" || len(got.Skipped) != 2 {
		t.Errorf("exit %d, %+v", r.exit, got)
	}
	if !strings.Contains(r.stderr, "2 routers to upgrade to 0.30.0") || !strings.Contains(r.stderr, "✓ r1") {
		t.Errorf("stderr %q", r.stderr)
	}
}

func TestUpgradeNoVersionAvailableExits1(t *testing.T) {
	api := &fakeAPI{routers: `{"routers":[{"name":"r1","version":"0.29.0"}]}`}
	r := runFleet(t, api, "eu-west-1", "routers", "upgrade", "--all", "--yes")
	if r.exit != 1 || !strings.Contains(r.stderr, "names no router version to upgrade to") || len(api.posted) != 0 {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestUpgradeTimeoutStopsFollowingAndExits1(t *testing.T) {
	api := &fakeAPI{routers: upgradeRouters, endWith: "running"}
	r := runFleetWith(t, api, "eu-west-1", func(d *fleetDeps) {
		var fire context.CancelFunc // the deadline, fired once the upgrade is started and followed
		d.timeout = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
			ctx, fire = context.WithCancel(ctx)
			return ctx, fire
		}
		d.sleep = func(ctx context.Context, _ time.Duration) error { fire(); <-ctx.Done(); return ctx.Err() }
	}, "routers", "upgrade", "r1", "--yes", "--timeout", "50ms")
	if r.exit != 1 || len(api.posted) != 1 {
		t.Fatalf("exit %d, posted %v, stderr %q", r.exit, api.posted, r.stderr)
	}
	for _, w := range []string{"timeout of 50ms reached", "1 still running", "the started upgrades go on in the manager: follow them in its Operations view"} {
		if !strings.Contains(r.stdout+r.stderr, w) {
			t.Errorf("output lacks %q: %q %q", w, r.stdout, r.stderr)
		}
	}
	if strings.Contains(r.stderr, "exit code") {
		t.Errorf("stderr %q", r.stderr)
	}
}
