package gcponboard

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func settingsRig(t *testing.T) (*workloadsRig, Request) {
	t.Helper()
	g := newWorkloadsRig(t)
	g.s.Workloads["shop"] = "AWAKE"
	g.s.Workloads["web"] = "AWAKE"
	r := workloadsEurope
	return g, r
}

func TestWorkloadsSettingsSendTheFlatBodyAsTheOperator(t *testing.T) {
	g, r := settingsRig(t)
	r.IDs = []string{"shop"}
	r.Levels = map[string]any{"idleMinutes": 30, "hibernate": ptr(true), "wakeMode": nil, "type": "batch"}
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsSettings, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	body := g.body(t, "PUT", controllerHost, "/v1/workloads/shop/settings")
	if body["idleMinutes"] != float64(30) || body["hibernate"] != true || body["type"] != "batch" || body["by"] != "me@example.com" {
		t.Fatalf("%v", body)
	}
	if v, there := body["wakeMode"]; !there || v != nil {
		t.Fatalf("a key given back to the levels is sent as null: %v", body)
	}
	if got := g.controllerCalls(mark); !slices.Contains(got, "PUT /v1/workloads/shop/settings") {
		t.Fatalf("%v", got)
	}
	contains(t, out, "shop: hibernate to on, its settings, type batch (effective: on, set on this workload)")
}

func TestWorkloadsSettingsSayInheritAndGoOnPastARefusal(t *testing.T) {
	g, r := settingsRig(t)
	r.IDs = []string{"shop", "missing", "web"}
	r.Levels = map[string]any{"hibernate": (*bool)(nil), "type": nil}
	code, out := g.call(t, WorkloadsSettings, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "shop: hibernate to inherit, type inherit", "web: hibernate to inherit, type inherit", "missing: ")
}

func TestWorkloadsSettingsSayTheRoutersProblems(t *testing.T) {
	g, r := settingsRig(t)
	g.s.SettingsProblem = "wakeTimeoutSeconds must be at most 1700: one controller request lasts at most 1800 s"
	r.IDs = []string{"shop"}
	r.Levels = map[string]any{"wakeTimeoutSeconds": 3000}
	code, out := g.call(t, WorkloadsSettings, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "wakeTimeoutSeconds must be at most 1700")
	if strings.Contains(out, `{"error"`) {
		t.Fatalf("%s", out)
	}
}

func TestWorkloadsSettingsJSONPrintsTheAnswersByWorkload(t *testing.T) {
	g, r := settingsRig(t)
	r.IDs, r.JSON = []string{"shop", "web"}, true
	r.Levels = map[string]any{"idleMinutes": 30}
	code, out := g.call(t, WorkloadsSettings, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var answers map[string]struct{ Level string }
	// The lines go to stderr under --json, the answers to stdout (the rig's buffer holds both): the answers are the last line.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &answers); err != nil || answers["shop"].Level != "workload" || answers["web"].Level != "workload" {
		t.Fatalf("%v:\n%s", err, out)
	}
}

func TestSettingsSayAnOlderRouterByItsVersion(t *testing.T) {
	g, r := settingsRig(t)
	g.s.ControllerBeforeSettings = true
	g.s.HealthVersion = "0.38.0"
	r.IDs = []string{"shop"}
	r.Levels = map[string]any{"idleMinutes": 30}
	code, out := g.call(t, WorkloadsSettings, r)
	want := "router 0.38.0 doesn't have the settings levels: dormeto gcp install --version " + embeddedRouterVersion()
	if code != 1 || !strings.Contains(out, want) {
		t.Fatalf("exit %d, want %q in:\n%s", code, want, out)
	}
	r.Levels = nil
	code, out = g.call(t, RouterSettings, r)
	if code != 1 || !strings.Contains(out, want) {
		t.Fatalf("exit %d, want %q in:\n%s", code, want, out)
	}
}

func TestRouterSettingsSetShowAndDryRun(t *testing.T) {
	g, r := settingsRig(t)
	r.Levels = map[string]any{"hibernate": ptr(true), "joinMinutes": ptr(30), "defaults": map[string]any{"idleMinutes": 45}}
	r.DryRun = true
	mark := len(g.s.Calls())
	code, out := g.call(t, RouterSettings, r)
	if code != 0 || len(g.controllerCalls(mark)) != 0 {
		t.Fatalf("a dry run sends nothing: exit %d %v:\n%s", code, g.controllerCalls(mark), out)
	}
	contains(t, out, "would")
	r.DryRun = false
	code, out = g.call(t, RouterSettings, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	body := g.body(t, "PUT", controllerHost, "/v1/settings")
	defaults, _ := body["defaults"].(map[string]any)
	if body["hibernate"] != true || body["joinMinutes"] != float64(30) || defaults["idleMinutes"] != float64(45) || body["by"] != "me@example.com" {
		t.Fatalf("%v", body)
	}
	contains(t, out, "workload settings set", "hibernate on", "join windows up to 30 min apart")
	// No flag shows the level.
	r.Levels = nil
	code, out = g.call(t, RouterSettings, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "dmtr-europe-west1", "hibernate: on", "join windows: up to 30 min apart", "workload settings: idle window 45 min", "me@example.com")
}

func TestRouterSettingsShowAnEmptyLevel(t *testing.T) {
	g, r := settingsRig(t)
	code, out := g.call(t, RouterSettings, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "hibernate: inherit", "join windows: inherit", "workload settings: none")
}

func TestRouterSettingsSayTheRoutersProblems(t *testing.T) {
	g, r := settingsRig(t)
	g.s.SettingsProblem = "joinMinutes must be a multiple of 5"
	r.Levels = map[string]any{"joinMinutes": ptr(7)}
	code, out := g.call(t, RouterSettings, r)
	if code != 1 || !strings.Contains(out, "joinMinutes must be a multiple of 5") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestWorkloadsShowPrintsEachSettingWithItsOriginThenTheTypeAndTheRules(t *testing.T) {
	g, r := settingsRig(t)
	g.s.WorkloadDocs["shop"] = map[string]any{
		"settings": map[string]any{"idleMinutes": 30, "waitSeconds": 0, "wakeTimeoutSeconds": 900, "failedRetrySeconds": 60, "maxHolders": 0, "drainSeconds": 0, "wakeMode": "browser"},
		"settingsFrom": map[string]any{"idleMinutes": "router", "waitSeconds": "fixed", "maxHolders": "fixed", "drainSeconds": "fixed",
			"wakeTimeoutSeconds": "workload", "failedRetrySeconds": "default", "wakeMode": "fixed"},
		"type":        map[string]any{"name": "instance", "from": "backends"},
		"hibernation": map[string]any{"hibernate": true, "from": "router"},
		"noWakeRules": []any{map[string]any{"name": "scanners", "level": "router", "conditions": map[string]any{"userAgentPattern": "bot"}}},
		"wakeRules":   []any{map[string]any{"name": "cron", "level": "wake", "from": "workload", "conditions": map[string]any{"pathPrefixes": []any{"/jobs"}}}},
	}
	r.ID = "shop"
	code, out := g.call(t, WorkloadsShow, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "idle window: 30 min (router)", "hold: 0 s (fixed)", "wake timeout: 15 min (workload)", "retry after a failed wake: 1 min (default)",
		"wake mode: browser (fixed)", "type: instance (from its backends)", "hibernate: on, from the router",
		"no-wake rules", "scanners", "(router)", "wake rules", "cron", "(workload)")
	// A workload of a router before the levels keeps its settings line.
	g.s.WorkloadDocs["web"] = map[string]any{"settings": map[string]any{"idleMinutes": 15}}
	r.ID = "web"
	_, out = g.call(t, WorkloadsShow, r)
	contains(t, out, "settings: idleMinutes 15")
}
