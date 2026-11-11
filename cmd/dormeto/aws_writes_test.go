package main

// The writing commands of `dormeto aws` as the AWS and Google Cloud review's §4.2 has them on both clouds: a router's settings
// without a flag shown, inherit unsetting a level everywhere (default its alias at the organization's and the types'), and
// --dry-run on every write, which sends nothing.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/gcp/gcptest"
	"github.com/Dormeto/cli/internal/gcponboard"
)

// routerLevelAPI is a fake manager whose routers answer their level, and that records the writes.
type routerLevelAPI struct {
	mu     sync.Mutex
	writes []string
}

func (a *routerLevelAPI) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method != http.MethodGet {
		a.writes = append(a.writes, r.Method+" "+r.URL.Path+" "+string(body))
		w.WriteHeader(500)
		return
	}
	switch r.URL.Path {
	case "/iam/routers/r1":
		io.WriteString(w, `{"name":"r1","cloud":"aws","settings":{"hibernate":true,"joinMinutes":30,"defaults":{"idleMinutes":45,"wakeMode":"browser"},"by":"ann@x.io","at":"2026-10-19T10:00:00.000Z"}}`)
	case "/iam/routers/r2":
		io.WriteString(w, `{"name":"r2","cloud":"aws","settings":{"hibernate":null,"joinMinutes":null,"by":null,"at":null}}`)
	case "/iam/routers/old":
		io.WriteString(w, `{"name":"old","cloud":"aws"}`)
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"error":"not_found","message":"no router `+strings.TrimPrefix(r.URL.Path, "/iam/routers/")+`"}`)
	}
}

// As `dormeto gcp routers settings` does, a router's settings without a flag show its level (it was a usage error).
func TestRoutersSettingsWithoutAFlagShowTheRoutersLevel(t *testing.T) {
	a := &routerLevelAPI{}
	r := runFleetWith(t, &fakeAPI{custom: a.handler}, "eu-west-1", nil, "routers", "settings", "r1", "r2")
	want := "Settings of router r1 (changed by ann@x.io at 2026-10-19 10:00 UTC):\n" +
		"  hibernate: on\n  join windows: up to 30 min apart\n  workload settings: idle window 45 min, wake mode browser\n" +
		"Settings of router r2 (never changed):\n" +
		"  hibernate: inherit\n  join windows: inherit\n  workload settings: none\n"
	if r.exit != 0 || r.stdout != want || len(a.writes) != 0 {
		t.Fatalf("exit %d, writes %q, stdout:\n%s\nstderr %q", r.exit, a.writes, r.stdout, r.stderr)
	}
	r = runFleetWith(t, &fakeAPI{custom: a.handler}, "eu-west-1", nil, "routers", "settings", "r1", "--json")
	if r.exit != 0 || !strings.Contains(r.stdout, `"r1": {`) || !strings.Contains(r.stdout, `"idleMinutes": 45`) {
		t.Fatalf("--json: exit %d, stdout %s", r.exit, r.stdout)
	}
	// One that cannot be read, or a router before the levels, is said for itself: the others are shown (exit 1).
	r = runFleetWith(t, &fakeAPI{custom: a.handler}, "eu-west-1", nil, "routers", "settings", "ghost", "old", "r2")
	if r.exit != 1 || !strings.Contains(r.stderr, "ghost: no router ghost") || !strings.Contains(r.stderr, "old: the router reports no settings: they need router 0.32.0 or later") ||
		!strings.Contains(r.stdout, "Settings of router r2") || len(a.writes) != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
}

// inherit unsets a level on every level; default is no word for it, at the organization's and the types' too.
func TestSettingsAndTypesSetTakeInherit(t *testing.T) {
	a := &globalAPI{}
	r := runSettings(t, a, "set", "--idle-minutes", "inherit", "--wake-mode", "inherit", "--wake-rules-file", "inherit")
	if want := []string{`{"defaults":{"idleMinutes":null,"wakeMode":null,"wakeRules":null}}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Errorf("settings set: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	a = &globalAPI{join: `"endedWindowDays":null,`}
	r = runSettings(t, a, "set", "--ended-window-days", "inherit")
	if want := []string{`{"endedWindowDays":null}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Errorf("settings set: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	a = &globalAPI{join: `"joinMinutes":null,`}
	r = runSettings(t, a, "set", "--join-minutes", "inherit")
	if want := []string{`{"joinMinutes":null}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Errorf("settings set: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	const stored = `[{"name":"api","tags":{"kind":"api"},"settings":{"idleMinutes":30,"wakeMode":"any"}}]`
	ta := &typesAPI{types: stored}
	r = runTypes(t, ta, "set", "api", "--idle-minutes", "inherit")
	if want := []string{`{"types":[{"name":"api","settings":{"wakeMode":"any"},"tags":{"kind":"api"}}]}`}; r.exit != 0 || !reflect.DeepEqual(ta.writes, want) {
		t.Errorf("types set: exit %d, writes %q, stderr %q", r.exit, ta.writes, r.stderr)
	}
}

// default, the old word of inherit, is not read: a usage error before any call, at the organization's and the types'.
func TestSettingsAndTypesSetRefuseDefaultAsAWord(t *testing.T) {
	for _, args := range [][]string{{"--idle-minutes", "default"}, {"--wake-mode", "default"}, {"--wake-rules-file", "default"}, {"--ended-window-days", "default"}, {"--join-minutes", "default"}} {
		a := &globalAPI{}
		if r := runSettings(t, a, append([]string{"set"}, args...)...); r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("settings set %q: exit %d, writes %q, stderr %q", args, r.exit, a.writes, r.stderr)
		}
	}
	ta := &typesAPI{types: `[{"name":"api","tags":{"kind":"api"},"settings":{"idleMinutes":30}}]`}
	if r := runTypes(t, ta, "set", "api", "--idle-minutes", "default"); r.exit != 2 || len(ta.writes) != 0 {
		t.Errorf("types set: exit %d, writes %q, stderr %q", r.exit, ta.writes, r.stderr)
	}
}

func TestSettingsAndTypesSetHelpNameInheritOnly(t *testing.T) {
	for _, args := range [][]string{{"aws", "settings", "set", "--help"}, {"aws", "types", "set", "--help"}} {
		out, _, code := executed(t, build{}, args...)
		if code != 0 || !strings.Contains(out, "or inherit") || strings.Contains(out, "old name") {
			t.Errorf("%v: %d\n%s", args, code, out)
		}
	}
}

// writesAPI is a fake manager that answers every read as the writes below need it and records every write, answering it
// 500: a dry run must send none.
type writesAPI struct {
	mu     sync.Mutex
	writes []string
}

const dismissedARN = "arn:aws:elasticloadbalancing:eu-west-1:123456789012:targetgroup/web/0123456789abcdef"

func (a *writesAPI) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method != http.MethodGet {
		a.writes = append(a.writes, r.Method+" "+r.URL.Path+" "+string(body))
		w.WriteHeader(500)
		io.WriteString(w, `{"error":"internal","message":"sent"}`)
		return
	}
	switch {
	case r.URL.Path == "/iam/discovery/candidates":
		io.WriteString(w, `{"candidates":[{"id":"`+dismissedARN+`","cloud":"aws","account":"123456789012","region":"eu-west-1","status":"`+r.URL.Query().Get("status")+`"}],"cursor":null}`)
	case r.URL.Path == "/iam/settings/types":
		io.WriteString(w, `{"types":[{"name":"api","tags":{"kind":"api"},"settings":{"idleMinutes":30}}],"builtIn":["instance","asg","ecs","k8s","databases"]}`)
	default:
		io.WriteString(w, `{}`)
	}
}

// dryRunWrites are the writes that had no --dry-run of their own: each takes one that sends nothing and says the request.
var dryRunWrites = []struct {
	args []string
	says string
	// targets are the --json's: as the command addresses them, the organization's own writes "organization".
	targets []string
}{
	{[]string{"discovery", "config", "set", "--file", "-"}, "PUT /iam/discovery/config", []string{"organization"}},
	{[]string{"discovery", "dismiss", dismissedARN}, "POST /iam/discovery/dismiss", []string{dismissedARN}},
	{[]string{"discovery", "restore", dismissedARN}, "POST /iam/discovery/restore", []string{dismissedARN}},
	{[]string{"discovery", "run"}, "POST /iam/discovery/run", []string{"organization"}},
	{[]string{"exclusions", "lift", "ex-1", "ex-2"}, "DELETE /iam/exclusions/ex-2", []string{"ex-1", "ex-2"}},
	{[]string{"groups", "create", "payments"}, "POST /iam/groups", []string{"payments"}},
	{[]string{"groups", "update", "payments", "--description", "the payments team"}, "PUT /iam/groups/payments", []string{"payments"}},
	{[]string{"manager", "check", "--enable", "--no-alert"}, "PUT /iam/update/check", []string{"organization"}},
	{[]string{"routers", "description", "r1", "--set", "the shop's router"}, "PUT /iam/routers/r1/description", []string{"r1"}},
	{[]string{"routers", "install", "--account", "123456789012", "--router-region", "eu-west-1"}, "POST /iam/operations", []string{"123456789012"}},
	{[]string{"routers", "waiting-page", "set", "r1", "--title", "Back soon"}, "PUT /iam/routers/r1/waiting-page", []string{"r1"}},
	{[]string{"routers", "waiting-page", "clear", "r1"}, "DELETE /iam/routers/r1/waiting-page", []string{"r1"}},
	{[]string{"savings", "discounts", "--compute", "20", "--databases", "10"}, "PUT /iam/savings/settings", []string{"organization"}},
	{[]string{"settings", "set", "--hibernate", "true"}, "PUT /iam/settings/workloads", []string{"organization"}},
	{[]string{"settings", "waiting-page", "set", "--title", "Back soon"}, "PUT /iam/settings/waiting-page", []string{"organization"}},
	{[]string{"settings", "waiting-page", "clear"}, "DELETE /iam/settings/waiting-page", []string{"organization"}},
	{[]string{"users", "enable", "ann@x.io"}, "POST /iam/users/ann@x.io/enable", []string{"ann@x.io"}},
	{[]string{"users", "groups", "ann@x.io", "payments"}, "PUT /iam/users/ann@x.io/groups", []string{"ann@x.io"}},
	{[]string{"users", "invite", "ann@x.io", "--role", "viewer"}, "POST /iam/users", []string{"ann@x.io"}},
	{[]string{"users", "resend", "ann@x.io"}, "POST /iam/users/ann@x.io/resend", []string{"ann@x.io"}},
	{[]string{"users", "reset-mfa", "ann@x.io"}, "POST /iam/users/ann@x.io/mfa/reset", []string{"ann@x.io"}},
	{[]string{"workloads", "description", "r1/w-1", "--set", "the shop"}, "PUT /iam/workloads/r1/w-1/description", []string{"r1/w-1"}},
	{[]string{"workloads", "waiting-page", "set", "r1/w-1", "--title", "Back soon"}, "PUT /iam/workloads/r1/w-1/waiting-page", []string{"r1/w-1"}},
	{[]string{"workloads", "waiting-page", "clear", "r1/w-1"}, "DELETE /iam/workloads/r1/w-1/waiting-page", []string{"r1/w-1"}},
}

func TestEveryWriteWithoutItsOwnDryRunTakesOneThatSendsNothing(t *testing.T) {
	for _, c := range dryRunWrites {
		a := &writesAPI{}
		api := &fakeAPI{custom: a.handler, stdin: `{"rules":[]}`}
		r := runFleetWith(t, api, "eu-west-1", nil, append(c.args, "--dry-run")...)
		if r.exit != 0 || len(a.writes) != 0 || !strings.Contains(r.stdout, "would "+c.says+", nothing sent\n") || !strings.HasPrefix(r.stdout, "dry run: ") {
			t.Errorf("%q: exit %d, writes %q, stdout %q, stderr %q", c.args, r.exit, a.writes, r.stdout, r.stderr)
			continue
		}
		// --yes is accepted beside it.
		if r := runFleetWith(t, &fakeAPI{custom: a.handler, stdin: `{"rules":[]}`}, "eu-west-1", nil, append(c.args, "--dry-run", "--yes")...); r.exit != 0 || len(a.writes) != 0 {
			t.Errorf("%q --yes: exit %d, writes %q, stderr %q", c.args, r.exit, a.writes, r.stderr)
		}
		if !jsonCommand(t, c.args) {
			continue
		}
		// --json: the released {"dryRun":true,"targets":[…]} alone on stdout, the words (each request) on stderr.
		r = runFleetWith(t, &fakeAPI{custom: a.handler, stdin: `{"rules":[]}`}, "eu-west-1", nil, append(c.args, "--dry-run", "--json")...)
		var got struct {
			DryRun  bool     `json:"dryRun"`
			Targets []string `json:"targets"`
		}
		err := json.Unmarshal([]byte(r.stdout), &got)
		if r.exit != 0 || err != nil || !got.DryRun || !slices.Equal(got.Targets, c.targets) || len(a.writes) != 0 || !strings.Contains(r.stderr, "would "+c.says+", nothing sent") {
			t.Errorf("%q --json: exit %d, %v, stdout %q, stderr %q", c.args, r.exit, err, r.stdout, r.stderr)
		}
	}
	a := &writesAPI{}
	for _, args := range [][]string{{"set", "api", "--idle-minutes", "20", "--dry-run"}, {"delete", "api", "--dry-run", "--yes"}} {
		types := func(d fleetDeps) []*cobra.Command { return []*cobra.Command{newTypes(d)} }
		r := runCommands(t, &fakeAPI{custom: a.handler}, "eu-west-1", nil, types, append([]string{"types"}, args...)...)
		if r.exit != 0 || len(a.writes) != 0 || !strings.Contains(r.stdout, "dry run: organization: would PUT /iam/settings/types, nothing sent\n") {
			t.Errorf("types %q: exit %d, writes %q, stdout %q, stderr %q", args, r.exit, a.writes, r.stdout, r.stderr)
		}
	}
}

// jsonCommand tells whether the command takes --json.
func jsonCommand(t *testing.T, args []string) bool {
	t.Helper()
	out, _, _ := executed(t, build{}, append(append([]string{"aws"}, args[:min(len(args), 3)]...), "--help")...)
	return strings.Contains(out, "--json")
}

// A dry run prints each write it would send, and the body as it would go, a long value by its size.
func TestADryRunSaysTheBodyItWouldSend(t *testing.T) {
	a := &writesAPI{}
	r := runFleetWith(t, &fakeAPI{custom: a.handler}, "eu-west-1", nil, "settings", "set", "--hibernate", "true", "--idle-minutes", "20", "--dry-run")
	if r.exit != 0 || !strings.Contains(r.stdout, "dry run: organization: would PUT /iam/settings/workloads, nothing sent\n  "+`{"defaults":{"idleMinutes":20},"hibernate":true}`+"\n") {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
	r = runFleetWith(t, &fakeAPI{custom: a.handler}, "eu-west-1", nil, "exclusions", "lift", "ex-1", "ex-2", "--dry-run")
	if r.exit != 0 || !strings.Contains(r.stdout, "DELETE /iam/exclusions/ex-1") || !strings.Contains(r.stdout, "DELETE /iam/exclusions/ex-2") || strings.Contains(r.stderr, "✗") {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
}

// Every writing command takes --yes and --dry-run, on both subtrees: --yes a no-op where nothing is asked, --dry-run sending
// nothing. idle-window takes the no-op --yes, but is a read.
func TestEveryWritingCommandTakesYesAndDryRun(t *testing.T) {
	root := newRoot(build{})
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		if c.Hidden {
			return
		}
		if c.HasSubCommands() {
			for _, sub := range c.Commands() {
				walk(sub, path+" "+sub.Name())
			}
			return
		}
		yes, dry := c.Flags().Lookup("yes") != nil, c.Flags().Lookup("dry-run") != nil
		reads := map[string]bool{"dormeto aws workloads idle-window": true}
		// The manager's own install and uninstall: their --yes takes the suggested answers, and their plan, in CodeBuild, is
		// itself a write (the bucket, the role and the project it runs in).
		install := strings.HasPrefix(path, "dormeto aws install") || strings.HasPrefix(path, "dormeto aws uninstall")
		if yes && !dry && !reads[path] && !install {
			t.Errorf("%s takes --yes but no --dry-run", path)
		}
		if dry && !yes {
			t.Errorf("%s takes --dry-run but no --yes", path)
		}
	}
	walk(root, "dormeto")
	for _, path := range [][]string{{"aws", "workloads", "onboard"}, {"aws", "settings", "set"}, {"gcp", "workloads", "nap"}, {"gcp", "workloads", "offboard"}} {
		out, _, _ := executed(t, build{}, append(path, "--help")...)
		if !strings.Contains(out, "--dry-run") || !strings.Contains(out, "--yes") {
			t.Errorf("%v: %s", path, out)
		}
	}
}

// The manager has no preview of an onboarding (the router's runs as the operation's step): a dry run says the candidate
// resolved, its eligibility as discovery listed it, and the body it would send.
func TestOnboardDryRunSaysTheCandidateItsEligibilityAndTheBody(t *testing.T) {
	a := pendingAPI()
	r := runOB(t, a, nil, "workloads", "onboard", tgAPI, "--idle-minutes", "30", "--dry-run")
	want := "candidate " + tgAPI + ": aws, account 111111111111, eu-west-1 (" + tgARN("111111111111", tgAPI) + ")\n" +
		"eligibility, as discovery listed it: pending-onboarding; idle 7d 120h, requests 7d 42\n" +
		"dry run: " + tgARN("111111111111", tgAPI) + ": would POST /iam/operations, nothing sent\n" +
		`  {"candidateId":"` + tgARN("111111111111", tgAPI) + `","databases":[],"settings":{"idleMinutes":30},"type":"onboard"}` + "\n"
	if r.exit != 0 || len(a.writes) != 0 || r.stdout != want {
		t.Fatalf("exit %d, writes %q, stdout:\n%s\nwant:\n%s\nstderr %q", r.exit, a.writes, r.stdout, want, r.stderr)
	}
	// A Google Cloud candidate, its reason when discovery gave one.
	a = &obAPI{pending: []string{strings.Replace(gcpCandidate(), `"status":"pending-onboarding"`, `"status":"pending-onboarding","statusReason":"idle most nights"`, 1)}}
	r = runOB(t, a, nil, "workloads", "onboard", "web", "--dry-run")
	if r.exit != 0 || len(a.writes) != 0 || !strings.Contains(r.stdout, "candidate web: gcp, project dormeto-dev, global ("+gcpBS+")\n") ||
		!strings.Contains(r.stdout, "eligibility, as discovery listed it: pending-onboarding: idle most nights\n") {
		t.Fatalf("exit %d, writes %q, stdout %q", r.exit, a.writes, r.stdout)
	}
	// --json: the candidate as discovery listed it beside the request.
	a = pendingAPI()
	r = runOB(t, a, nil, "workloads", "onboard", tgAPI, "--dry-run", "--json")
	var got struct {
		DryRun    bool `json:"dryRun"`
		Candidate struct {
			ID, Status string
		}
		Targets []string
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || r.exit != 0 || !got.DryRun || got.Candidate.ID != tgARN("111111111111", tgAPI) ||
		got.Candidate.Status != "pending-onboarding" || !slices.Equal(got.Targets, []string{tgARN("111111111111", tgAPI)}) || len(a.writes) != 0 {
		t.Fatalf("%v: exit %d, stdout %s, stderr %q", err, r.exit, r.stdout, r.stderr)
	}
}

// keep-awake is a pause: its --reason is 1 to 256 characters as every pause's, refused before any call, and trimmed.
func TestKeepAwakeReasonIsAPausesOneTo256Characters(t *testing.T) {
	for _, reason := range []string{"  ", strings.Repeat("x", 257)} {
		a := &wlAPI{}
		r := runWL(t, a, nil, "keep-awake", "prod-eu/w-1", "--for", "2h", "--reason", reason)
		if r.exit != 2 || len(a.writes) != 0 || !strings.Contains(r.stderr, "--reason: give 1 to 256 characters") {
			t.Errorf("%q: exit %d, writes %q, stderr %q", reason, r.exit, a.writes, r.stderr)
		}
	}
	a := &wlAPI{}
	if r := runWL(t, a, nil, "keep-awake", "prod-eu/w-1", "--for", "2h", "--reason", " release ci "); r.exit != 0 || len(a.writes) != 1 || !strings.Contains(a.writes[0], `"reason":"release ci"`) {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

// gcpTwinRig is a fake Google with the router of dormeto-dev installed in europe-west1, and the gcp commands that run
// internal/gcponboard's against it: the twins of the AWS commands, run for what they print.
func gcpTwinRig(t *testing.T) gcpRuns {
	t.Helper()
	s := gcptest.New(t)
	s.Policy = map[string]any{"version": 1, "etag": "BwAAAA==", "bindings": []any{
		map[string]any{"role": "roles/owner", "members": []any{"user:owner@example.com"}}}}
	const image = "europe-west1-docker.pkg.dev/dormeto-dev/dormeto/router:0.34.0"
	s.Images[image] = true
	fake := func(d gcponboard.Deps) gcponboard.Deps {
		d.Google = &gcp.Client{HTTP: s.HTTP, Tokens: gcptest.Tokens{AccountName: "me@example.com"}, Sleep: func(context.Context, time.Duration) error { return nil }}
		d.Terminal, d.Cache = false, t.TempDir()
		d.Sleep = func(context.Context, time.Duration) error { return nil }
		return d
	}
	var out bytes.Buffer
	install := gcponboard.Request{Project: "dormeto-dev", Region: "europe-west1", Image: image, Yes: true}
	if code := gcponboard.Install(context.Background(), fake(gcponboard.Deps{Out: &out, Err: &out, Now: time.Now}), install); code != 0 {
		t.Fatalf("install: exit %d:\n%s", code, out.String())
	}
	run := func(f gcpRun) gcpRun {
		return func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int { return f(ctx, fake(d), r) }
	}
	return gcpRuns{routerPause: run(gcponboard.RouterPause),
		workloads: gcpWorkloadRuns{nap: run(gcponboard.WorkloadsNap), settings: run(gcponboard.WorkloadsSettings)}}
}

// gcpRanOn runs a gcp command line on runs, for its standard output, its standard error and its exit code.
func gcpRanOn(runs gcpRuns, args ...string) (stdout, stderr string, exit int) {
	root := &cobra.Command{Use: "dormeto", SilenceUsage: true}
	root.AddCommand(newGcp(runs, fleetDeps{}, "1.2.3"))
	var o, e bytes.Buffer
	root.SetOut(&o)
	root.SetErr(&e)
	root.SetIn(strings.NewReader(""))
	exit = execute(root, args...)
	return o.String(), e.String(), exit
}

// One dry-run text on both subtrees: each request a twin would send said as the other says its own, "dry run: <target>: would
// <method> <path>, nothing sent", its body on the next line, and the released {"dryRun": true, "targets": […]} under --json.
func TestTheTwinsDryRunsSayTheirRequestsAlike(t *testing.T) {
	runs := gcpTwinRig(t)
	where := []string{"--project", "dormeto-dev", "--region", "europe-west1"}
	const router = "dmtr-dormeto-dev-europe-west1"
	for _, c := range []struct {
		aws, gcp             []string
		awsSays, gcpSays     string
		awsTarget, gcpTarget string
	}{
		{[]string{"workloads", "nap", "r1/w-1"}, []string{"workloads", "nap", "shop"},
			"dry run: r1/w-1: would POST /iam/workloads/r1/w-1/nap, nothing sent\n",
			"dry run: " + router + "/shop: would POST /v1/workloads/shop/nap, nothing sent\n", "r1/w-1", router + "/shop"},
		{[]string{"workloads", "settings", "r1/w-1", "--idle-minutes", "30"}, []string{"workloads", "settings", "shop", "--idle-minutes", "30"},
			"dry run: r1/w-1: would PUT /iam/workloads/r1/w-1/settings, nothing sent\n  {\"idleMinutes\":30}\n",
			"dry run: " + router + "/shop: would PUT /v1/workloads/shop/settings, nothing sent\n  {\"idleMinutes\":30}\n", "r1/w-1", router + "/shop"},
		{[]string{"routers", "pause", "r1", "--reason", "freeze"}, []string{"routers", "pause", "--reason", "freeze"},
			"dry run: r1: would POST /iam/routers/r1/pause, nothing sent\n  {\"keepAsleep\":false,\"reason\":\"freeze\"}\n",
			"dry run: " + router + ": would POST /v1/pause, nothing sent\n  {\"reason\":\"freeze\"}\n", "r1", router},
	} {
		a := &writesAPI{}
		r := runFleetWith(t, &fakeAPI{custom: a.handler}, "eu-west-1", nil, append(slices.Clone(c.aws), "--dry-run")...)
		if r.exit != 0 || len(a.writes) != 0 || r.stdout != c.awsSays {
			t.Errorf("aws %q: exit %d, writes %q, stdout %q, want %q, stderr %q", c.aws, r.exit, a.writes, r.stdout, c.awsSays, r.stderr)
		}
		stdout, stderr, exit := gcpRanOn(runs, slices.Concat([]string{"gcp"}, c.gcp, where, []string{"--dry-run"})...)
		if exit != 0 || stdout != c.gcpSays {
			t.Errorf("gcp %q: exit %d, stdout %q, want %q, stderr %q", c.gcp, exit, stdout, c.gcpSays, stderr)
		}
		// --json: the released shape alone on stdout, the request on stderr, on both.
		r = runFleetWith(t, &fakeAPI{custom: a.handler}, "eu-west-1", nil, append(slices.Clone(c.aws), "--dry-run", "--json")...)
		if want := `{"dryRun":true,"targets":["` + c.awsTarget + `"]}`; r.exit != 0 || compactJSON(t, r.stdout) != want || r.stderr != c.awsSays {
			t.Errorf("aws %q --json: exit %d, stdout %q, want %s, stderr %q", c.aws, r.exit, r.stdout, want, r.stderr)
		}
		stdout, stderr, exit = gcpRanOn(runs, slices.Concat([]string{"gcp"}, c.gcp, where, []string{"--dry-run", "--json"})...)
		if want := `{"dryRun":true,"targets":["` + c.gcpTarget + `"]}`; exit != 0 || compactJSON(t, stdout) != want || stderr != c.gcpSays {
			t.Errorf("gcp %q --json: exit %d, stdout %q, want %s, stderr %q", c.gcp, exit, stdout, want, stderr)
		}
	}
}

// compactJSON is s as compact JSON, its keys in their order ("" when s is not JSON).
func compactJSON(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		return ""
	}
	return b.String()
}
