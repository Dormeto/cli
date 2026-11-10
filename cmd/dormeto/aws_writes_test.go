package main

// The writing commands of `dormeto aws` as the AWS and Google Cloud review's §4.2 has them on both clouds: a router's settings
// without a flag shown, inherit unsetting a level everywhere (default its alias at the organization's and the types'), and
// --dry-run on every write, which sends nothing.

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
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
