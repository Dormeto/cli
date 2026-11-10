package main

// The writing commands of `dormeto aws` as the AWS and Google Cloud review's §4.2 has them on both clouds: a router's settings
// without a flag shown, inherit unsetting a level everywhere (default its alias at the organization's and the types'), and
// --dry-run on every write, which sends nothing.

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
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

// inherit unsets a level on every level; at the organization's and the types', default is its alias and sends the same.
func TestSettingsAndTypesSetTakeInheritAsDefault(t *testing.T) {
	for _, args := range [][]string{
		{"--idle-minutes", "inherit", "--wake-mode", "inherit", "--wake-rules-file", "inherit"},
		{"--idle-minutes", "default", "--wake-mode", "default", "--wake-rules-file", "default"},
	} {
		a := &globalAPI{}
		r := runSettings(t, a, append([]string{"set"}, args...)...)
		if want := []string{`{"defaults":{"idleMinutes":null,"wakeMode":null,"wakeRules":null}}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
			t.Errorf("settings set %q: exit %d, writes %q, stderr %q", args, r.exit, a.writes, r.stderr)
		}
	}
	for _, args := range [][]string{{"--ended-window-days", "inherit"}, {"--ended-window-days", "default"}} {
		a := &globalAPI{join: `"endedWindowDays":null,`}
		r := runSettings(t, a, append([]string{"set"}, args...)...)
		if want := []string{`{"endedWindowDays":null}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
			t.Errorf("settings set %q: exit %d, writes %q, stderr %q", args, r.exit, a.writes, r.stderr)
		}
	}
	for _, args := range [][]string{{"--join-minutes", "inherit"}, {"--join-minutes", "default"}} {
		a := &globalAPI{join: `"joinMinutes":null,`}
		r := runSettings(t, a, append([]string{"set"}, args...)...)
		if want := []string{`{"joinMinutes":null}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
			t.Errorf("settings set %q: exit %d, writes %q, stderr %q", args, r.exit, a.writes, r.stderr)
		}
	}
	const stored = `[{"name":"api","tags":{"kind":"api"},"settings":{"idleMinutes":30,"wakeMode":"any"}}]`
	for _, word := range []string{"inherit", "default"} {
		a := &typesAPI{types: stored}
		r := runTypes(t, a, "set", "api", "--idle-minutes", word)
		if want := []string{`{"types":[{"name":"api","settings":{"wakeMode":"any"},"tags":{"kind":"api"}}]}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
			t.Errorf("types set %s: exit %d, writes %q, stderr %q", word, r.exit, a.writes, r.stderr)
		}
	}
}

func TestSettingsAndTypesSetHelpNameInheritAndItsAlias(t *testing.T) {
	for _, args := range [][]string{{"aws", "settings", "set", "--help"}, {"aws", "types", "set", "--help"}} {
		out, _, code := executed(t, build{}, args...)
		if code != 0 || !strings.Contains(out, "or inherit") || !strings.Contains(out, "default, its old name") {
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
}{
	{[]string{"discovery", "config", "set", "--file", "-"}, "PUT /iam/discovery/config"},
	{[]string{"discovery", "dismiss", dismissedARN}, "POST /iam/discovery/dismiss"},
	{[]string{"discovery", "restore", dismissedARN}, "POST /iam/discovery/restore"},
	{[]string{"discovery", "run"}, "POST /iam/discovery/run"},
	{[]string{"exclusions", "lift", "ex-1", "ex-2"}, "DELETE /iam/exclusions/ex-2"},
	{[]string{"groups", "create", "payments"}, "POST /iam/groups"},
	{[]string{"groups", "update", "payments", "--description", "the payments team"}, "PUT /iam/groups/payments"},
	{[]string{"manager", "check", "--enable", "--no-alert"}, "PUT /iam/update/check"},
	{[]string{"routers", "description", "r1", "--set", "the shop's router"}, "PUT /iam/routers/r1/description"},
	{[]string{"routers", "install", "--account", "123456789012", "--router-region", "eu-west-1"}, "POST /iam/operations"},
	{[]string{"routers", "waiting-page", "set", "r1", "--title", "Back soon"}, "PUT /iam/routers/r1/waiting-page"},
	{[]string{"routers", "waiting-page", "clear", "r1"}, "DELETE /iam/routers/r1/waiting-page"},
	{[]string{"savings", "discounts", "--compute", "20", "--databases", "10"}, "PUT /iam/savings/settings"},
	{[]string{"settings", "set", "--hibernate", "true"}, "PUT /iam/settings/workloads"},
	{[]string{"settings", "waiting-page", "set", "--title", "Back soon"}, "PUT /iam/settings/waiting-page"},
	{[]string{"settings", "waiting-page", "clear"}, "DELETE /iam/settings/waiting-page"},
	{[]string{"users", "enable", "ann@x.io"}, "POST /iam/users/ann@x.io/enable"},
	{[]string{"users", "groups", "ann@x.io", "payments"}, "PUT /iam/users/ann@x.io/groups"},
	{[]string{"users", "invite", "ann@x.io", "--role", "viewer"}, "POST /iam/users"},
	{[]string{"users", "resend", "ann@x.io"}, "POST /iam/users/ann@x.io/resend"},
	{[]string{"users", "reset-mfa", "ann@x.io"}, "POST /iam/users/ann@x.io/mfa/reset"},
	{[]string{"workloads", "description", "r1/w-1", "--set", "the shop"}, "PUT /iam/workloads/r1/w-1/description"},
	{[]string{"workloads", "waiting-page", "set", "r1/w-1", "--title", "Back soon"}, "PUT /iam/workloads/r1/w-1/waiting-page"},
	{[]string{"workloads", "waiting-page", "clear", "r1/w-1"}, "DELETE /iam/workloads/r1/w-1/waiting-page"},
}

func TestEveryWriteWithoutItsOwnDryRunTakesOneThatSendsNothing(t *testing.T) {
	for _, c := range dryRunWrites {
		a := &writesAPI{}
		api := &fakeAPI{custom: a.handler, stdin: `{"rules":[]}`}
		r := runFleetWith(t, api, "eu-west-1", nil, append(c.args, "--dry-run")...)
		if r.exit != 0 || len(a.writes) != 0 || !strings.Contains(r.stdout, "dry run: would "+c.says+", nothing sent\n") {
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
		// --json: {"dryRun":true,"requests":[{method, path, body}]} alone on stdout, the words on stderr.
		r = runFleetWith(t, &fakeAPI{custom: a.handler, stdin: `{"rules":[]}`}, "eu-west-1", nil, append(c.args, "--dry-run", "--json")...)
		var got struct {
			DryRun   bool `json:"dryRun"`
			Requests []struct{ Method, Path string }
		}
		method, path, _ := strings.Cut(c.says, " ")
		err := json.Unmarshal([]byte(r.stdout), &got)
		said := false
		for _, req := range got.Requests {
			said = said || (req.Method == method && "/iam"+req.Path == path)
		}
		if r.exit != 0 || err != nil || !got.DryRun || !said || len(a.writes) != 0 || !strings.Contains(r.stderr, "dry run: would ") {
			t.Errorf("%q --json: exit %d, %v, stdout %q, stderr %q", c.args, r.exit, err, r.stdout, r.stderr)
		}
	}
	a := &writesAPI{}
	for _, args := range [][]string{{"set", "api", "--idle-minutes", "20", "--dry-run"}, {"delete", "api", "--dry-run", "--yes"}} {
		types := func(d fleetDeps) []*cobra.Command { return []*cobra.Command{newTypes(d)} }
		r := runCommands(t, &fakeAPI{custom: a.handler}, "eu-west-1", nil, types, append([]string{"types"}, args...)...)
		if r.exit != 0 || len(a.writes) != 0 || !strings.Contains(r.stdout, "dry run: would PUT /iam/settings/types, nothing sent\n") {
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
	if r.exit != 0 || !strings.Contains(r.stdout, "dry run: would PUT /iam/settings/workloads, nothing sent\n  "+`{"defaults":{"idleMinutes":20},"hibernate":true}`+"\n") {
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
		"dry run: would POST /iam/operations, nothing sent\n" +
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
		Requests []struct {
			Method, Path string
			Body         map[string]any
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || r.exit != 0 || !got.DryRun || got.Candidate.ID != tgARN("111111111111", tgAPI) ||
		got.Candidate.Status != "pending-onboarding" || len(got.Requests) != 1 || got.Requests[0].Path != "/operations" || got.Requests[0].Body["type"] != "onboard" || len(a.writes) != 0 {
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
