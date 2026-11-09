package gcponboard

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/install"
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
	r.IDs, r.Yes = []string{"shop", "missing", "web"}, true
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
	r.IDs, r.JSON, r.Yes = []string{"shop", "web"}, true, true
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
	// AWS's words for its origins, and one for fixed.
	contains(t, out, "idle window: 30 min (router)", "hold: 0 s (fixed on Google Cloud)", "wake timeout: 15 min (set on this workload)", "retry after a failed wake: 1 min (built-in)",
		"wake mode: browser (fixed on Google Cloud)", "type: instance (from its backends)", "hibernate: on, from the router",
		"no-wake rules", "scanners", "(router)", "wake rules", "cron", "(workload)")
	// A workload of a router before the levels keeps its settings line.
	g.s.WorkloadDocs["web"] = map[string]any{"settings": map[string]any{"idleMinutes": 15}}
	r.ID = "web"
	_, out = g.call(t, WorkloadsShow, r)
	contains(t, out, "settings: idleMinutes 15")
}

// The Workload view gives the VMs on a fallback type as AWS's `fallbacks` list.
func TestWorkloadsShowNamesTheVMsRunningOnAFallbackType(t *testing.T) {
	g, r := settingsRig(t)
	g.s.WorkloadDocs["shop"] = map[string]any{"settings": map[string]any{"idleMinutes": 30}, "settingsFrom": map[string]any{"idleMinutes": "router"}}
	g.s.OnFallback = map[string]map[string]map[string]string{"shop": {"web-2": {"from": "n2-standard-2", "to": "n2-standard-4"}, "web-1": {"from": "e2-small", "to": "e2-medium"}}}
	r.ID = "shop"
	code, out := g.call(t, WorkloadsShow, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "running on a fallback type: web-1 e2-medium (was e2-small), web-2 n2-standard-4 (was n2-standard-2)")
}

func TestWorkloadsSettingsDryRunSendsNothingAndSaysWhatItWouldSet(t *testing.T) {
	g, r := settingsRig(t)
	r.IDs, r.DryRun = []string{"shop", "web"}, true
	r.Levels = map[string]any{"idleMinutes": 30}
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsSettings, r)
	if code != 0 || len(g.controllerCalls(mark)) != 0 {
		t.Fatalf("exit %d %v:\n%s", code, g.controllerCalls(mark), out)
	}
	contains(t, out, "dry run: its settings: would act on 2 workloads, nothing sent\n", "  shop", "  web")
}

// Under --json stdout is JSON, as AWS's: the text goes to stderr and a dry run or a declined question prints its shape.
func TestWorkloadsSettingsJSONOfADryRunOrADeclinedQuestionPrintsJSONAndNoText(t *testing.T) {
	g, r := settingsRig(t)
	var errs bytes.Buffer
	g.d.Err = &errs
	r.IDs, r.JSON = []string{"shop", "web"}, true
	r.Levels = map[string]any{"idleMinutes": 30}
	r.DryRun = true
	code, out := g.call(t, WorkloadsSettings, r)
	if want := `{"dryRun":true,"targets":["shop","web"]}` + "\n"; code != 0 || out != want {
		t.Fatalf("dry run: exit %d, stdout %q, want %q", code, out, want)
	}
	contains(t, errs.String(), "dry run: its settings: would act on 2 workloads, nothing sent\n")
	r.DryRun = false
	errs.Reset()
	g.d.Terminal = true
	g.d.Prompter = install.NewPrompter(strings.NewReader("n\n"), &errs, true)
	code, out = g.call(t, WorkloadsSettings, r)
	if want := `{"confirmed":false,"targets":["shop","web"]}` + "\n"; code != 0 || out != want {
		t.Fatalf("declined: exit %d, stdout %q, want %q", code, out, want)
	}
	contains(t, errs.String(), "Set its settings for shop, web? [y/N]: ", "not confirmed: nothing sent")
	// A router's level: its dry run is JSON on stdout too.
	r.DryRun, r.IDs = true, nil
	r.Levels = map[string]any{"hibernate": ptr(true)}
	errs.Reset()
	code, out = g.call(t, RouterSettings, r)
	if want := `{"dryRun":true,"targets":["dmtr-europe-west1"]}` + "\n"; code != 0 || out != want {
		t.Fatalf("router dry run: exit %d, stdout %q, want %q", code, out, want)
	}
	contains(t, errs.String(), "dmtr-europe-west1: would set")
}

func TestWorkloadsSettingsAskForSeveralWorkloadsOnly(t *testing.T) {
	g, r := settingsRig(t)
	r.Levels = map[string]any{"idleMinutes": 30}
	// Several, without a terminal: --yes is needed.
	r.IDs = []string{"shop", "web"}
	mark := len(g.s.Calls())
	code, out := g.call(t, WorkloadsSettings, r)
	if code != 2 || len(g.controllerCalls(mark)) != 0 || !strings.Contains(out, "confirmation needed: pass --yes to act on 2 workloads without a terminal") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// On a terminal: asked, declined, nothing sent.
	g.d.Terminal = true
	g.d.Prompter = install.NewPrompter(strings.NewReader("n\n"), g.out, true)
	code, out = g.call(t, WorkloadsSettings, r)
	if code != 0 || len(g.controllerCalls(mark)) != 0 || !strings.Contains(out, "Set its settings for shop, web? [y/N]: ") || !strings.Contains(out, "not confirmed: nothing sent") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.d.Prompter = install.NewPrompter(strings.NewReader("y\n"), g.out, true)
	code, out = g.call(t, WorkloadsSettings, r)
	if code != 0 || !strings.Contains(out, "shop: its settings") || !strings.Contains(out, "web: its settings") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// --yes skips the question; one workload asks nothing.
	g.d.Prompter = install.NewPrompter(strings.NewReader(""), g.out, true)
	r.Yes = true
	if code, out = g.call(t, WorkloadsSettings, r); code != 0 || strings.Contains(out, "[y/N]") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	r.Yes, r.IDs = false, []string{"shop"}
	g.d.Terminal = false
	if code, out = g.call(t, WorkloadsSettings, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
