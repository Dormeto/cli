package main

// Settings at scale (vision settings at scale design §10): the defaults of the organization, a router and a workload, the
// organization's types, and the source of each workload setting, against the fake manager API.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

func TestSettingsSetSendsTheDefaultsItIsGiven(t *testing.T) {
	rules := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(rules, []byte(`[{"name":"h","pathPrefixes":["/health"]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		sent string
	}{
		{[]string{"--idle-minutes", "30", "--wait-seconds", "default", "--wake-mode", "default"}, `{"defaults":{"idleMinutes":30,"waitSeconds":null,"wakeMode":null}}`},
		{[]string{"--hibernate", "true", "--idle-minutes", "20"}, `{"defaults":{"idleMinutes":20},"hibernate":true}`},
		{[]string{"--wake-mode", "browser", "--max-holders", "40", "--failed-retry-seconds", "120", "--drain-seconds", "0", "--wake-timeout-seconds", "1800"},
			`{"defaults":{"drainSeconds":0,"failedRetrySeconds":120,"maxHolders":40,"wakeMode":"browser","wakeTimeoutSeconds":1800}}`},
		{[]string{"--no-wake-rules-file", rules, "--wake-rules-file", "default"}, `{"defaults":{"noWakeRules":[{"name":"h","pathPrefixes":["/health"]}],"wakeRules":null}}`},
	} {
		a := &globalAPI{}
		r := runSettings(t, a, append([]string{"set"}, c.args...)...)
		if r.exit != 0 || !reflect.DeepEqual(a.writes, []string{c.sent}) {
			t.Errorf("%q: exit %d, writes %q, stderr %q", c.args, r.exit, a.writes, r.stderr)
		}
	}
}

func TestSettingsSetRefusesABadDefaultBeforeSending(t *testing.T) {
	notList := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(notList, []byte(`{"name":"h"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--idle-minutes", "4"}, {"--idle-minutes", "1441"}, {"--wait-seconds", "soon"}, {"--wake-mode", "never"},
		{"--max-holders", "0"}, {"--failed-retry-seconds", "5"}, {"--no-wake-rules-file", notList}, {"--wake-rules-file", filepath.Join(t.TempDir(), "none.json")},
	} {
		a := &globalAPI{}
		r := runSettings(t, a, append([]string{"set"}, args...)...)
		if r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("%q: exit %d, writes %q, stderr %q", args, r.exit, a.writes, r.stderr)
		}
	}
}

func TestRoutersSettingsSendsTheDefaults(t *testing.T) {
	a := &wrAPI{}
	r := runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", nil, "routers", "settings", "r1", "--idle-minutes", "30", "--wake-mode", "inherit")
	if want := []string{`PUT /iam/routers/r1/settings {"defaults":{"idleMinutes":30,"wakeMode":null}}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	b := &wrAPI{}
	if r := runFleetWith(t, &fakeAPI{custom: b.handler()}, "eu-west-1", nil, "routers", "settings", "r1", "--idle-minutes", "default"); r.exit != 2 || len(b.writes) != 0 {
		t.Errorf("default at a router: exit %d, writes %q", r.exit, b.writes)
	}
}

func TestWorkloadsSettingsSendsTheLevelsAndTheType(t *testing.T) {
	for _, c := range []struct {
		args []string
		sent string
	}{
		{[]string{"--idle-minutes", "45", "--type", "api"}, `{"idleMinutes":45,"type":"api"}`},
		{[]string{"--type", "inherit", "--wake-mode", "inherit"}, `{"type":null,"wakeMode":null}`},
		{[]string{"--hibernate", "false", "--wait-seconds", "20"}, `{"hibernate":false,"waitSeconds":20}`},
		{[]string{"--hibernate", "inherit"}, `{"hibernate":null}`},
	} {
		a := &wrAPI{}
		r := runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", nil, append([]string{"workloads", "settings", "prod-eu/w-1"}, c.args...)...)
		if want := []string{`PUT /iam/workloads/prod-eu/w-1/settings ` + c.sent}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
			t.Errorf("%q: exit %d, writes %q, stderr %q", c.args, r.exit, a.writes, r.stderr)
		}
	}
	for _, args := range [][]string{{}, {"--type", "Bad"}, {"--idle-minutes", "default"}} {
		a := &wrAPI{}
		if r := runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", nil, append([]string{"workloads", "settings", "prod-eu/w-1"}, args...)...); r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("%q: exit %d, writes %q", args, r.exit, a.writes)
		}
	}
}

// typesAPI is a fake manager's /settings/types route.
type typesAPI struct {
	mu     sync.Mutex
	types  string
	writes []string
}

func (a *typesAPI) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/iam/settings/types":
		io.WriteString(w, `{"types":`+a.types+`,"builtIn":["instance","asg","ecs","k8s","databases"],"updatedAt":null,"updatedBy":null}`)
	case r.Method == http.MethodPut && r.URL.Path == "/iam/settings/types":
		a.writes = append(a.writes, string(body))
		io.WriteString(w, `{"types":[],"builtIn":["instance","asg","ecs","k8s","databases"],"note":"routers apply it at their next check, within 5 minutes"}`)
	default:
		w.WriteHeader(404)
	}
}

func runTypes(t *testing.T, a *typesAPI, args ...string) fleetRan {
	t.Helper()
	types := func(d fleetDeps) []*cobra.Command { return []*cobra.Command{newTypes(d)} }
	return runCommands(t, &fakeAPI{custom: a.handler}, "eu-west-1", nil, types, append([]string{"types"}, args...)...)
}

func TestTypesListsSetsAndDeletes(t *testing.T) {
	const stored = `[{"name":"api","description":"Public APIs","tags":{"kind":"api"},"settings":{"idleMinutes":30}},{"name":"databases","settings":{"idleMinutes":45}}]`
	list := runTypes(t, &typesAPI{types: stored}, "list")
	if list.exit != 0 {
		t.Fatalf("list: exit %d, %s", list.exit, list.stderr)
	}
	mustContain(t, list.stdout, "api", "kind=api", "idle window 30 min", "databases", "idle window 45 min", "instance", "ecs")
	for _, c := range []struct {
		args []string
		sent string
	}{
		{[]string{"set", "web", "--tag", "team=shop", "--idle-minutes", "20"},
			`{"types":[{"description":"Public APIs","name":"api","settings":{"idleMinutes":30},"tags":{"kind":"api"}},{"name":"databases","settings":{"idleMinutes":45}},{"name":"web","settings":{"idleMinutes":20},"tags":{"team":"shop"}}]}`},
		{[]string{"set", "api", "--idle-minutes", "default", "--wake-mode", "browser"},
			`{"types":[{"description":"Public APIs","name":"api","settings":{"wakeMode":"browser"},"tags":{"kind":"api"}},{"name":"databases","settings":{"idleMinutes":45}}]}`},
		{[]string{"set", "ecs", "--wait-seconds", "20"},
			`{"types":[{"description":"Public APIs","name":"api","settings":{"idleMinutes":30},"tags":{"kind":"api"}},{"name":"databases","settings":{"idleMinutes":45}},{"name":"ecs","settings":{"waitSeconds":20}}]}`},
		{[]string{"delete", "api"}, `{"types":[{"name":"databases","settings":{"idleMinutes":45}}]}`},
	} {
		a := &typesAPI{types: stored}
		r := runTypes(t, a, c.args...)
		if r.exit != 0 || !reflect.DeepEqual(a.writes, []string{c.sent}) {
			t.Errorf("%q: exit %d, writes %q, stderr %q", c.args, r.exit, a.writes, r.stderr)
		}
	}
	for _, args := range [][]string{{"set", "ecs", "--tag", "x=y"}, {"set", "web"}, {"set", "Bad", "--tag", "a=b"}, {"delete", "gone"}, {"delete", "ecs"}} {
		a := &typesAPI{types: stored}
		if r := runTypes(t, a, args...); r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("%q: exit %d, writes %q, stderr %q", args, r.exit, a.writes, r.stderr)
		}
	}
}

func TestWorkloadsShowSaysWhereEachSettingComesFrom(t *testing.T) {
	a := &showAPI{status: 200, body: `{"workloadId":"w-1","state":"AWAKE","settings":{"idleMinutes":30,"waitSeconds":20,"drainSeconds":30,"wakeTimeoutSeconds":600,"maxHolders":50,"failedRetrySeconds":60},` +
		`"settingsFrom":{"idleMinutes":"type","waitSeconds":"workload","drainSeconds":"router","wakeTimeoutSeconds":"default","maxHolders":"organization","failedRetrySeconds":"default","wakeMode":"default"},` +
		`"type":{"name":"api","from":"tags"}}`}
	r := runShow(t, a, "show", "prod-eu/w-1")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	mustContain(t, r.stdout, "type", "api (from its tags)", "idle window", "30 min (type api)", "20 s (set on this workload)", "30 s (router)", "10 min (built-in)", "≤ 50 (organization)", "retry after a failed wake", "1 min (built-in)")
	old := runShow(t, &showAPI{status: 200, body: `{"workloadId":"w-1","state":"AWAKE","settings":{"idleMinutes":15}}`}, "show", "prod-eu/w-1")
	if strings.Contains(old.stdout, "(built-in)") || !strings.Contains(old.stdout, "15 min") {
		t.Errorf("an older router's values have no source: %s", old.stdout)
	}
}
