package install

// The install's samples (the CLI pages design §4; cmd/dormeto/samples_test.go writes the others): `dormeto aws install`, its
// apply, check and status, `dormeto aws uninstall` and its apply, each run here through its command's real code against the
// fake AWS of these tests (rwWorld), as account 111122223333 with the manager 0.24.0 released, and kept in docs/samples. The
// line is what a reader types; its request is written beside it, the flags being read by cmd/dormeto (its tests hold that
// reading). A file that differs from what its run prints now fails the test; rewrite them with
// go test ./internal/install -run Samples -update.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// installSample is a line, its request, the world it runs in (made by its earlier steps) and its run.
type installSample struct {
	command, line string
	r             Request
	world         func(t *testing.T) *rwWorld
	run           func(context.Context, Deps, Request) int
	json          bool // the command takes --json
}

// samplesAccount is the fictional fleet's account.
const samplesAccount = "111122223333"

// samplesWorld is account 111122223333, alone (no organization), in eu-west-1, the manager 0.24.0 the latest release.
func samplesWorld(t *testing.T) *rwWorld {
	w := rwNew(t)
	w.rwAcct = rwNewAcct(samplesAccount, "dmt-prod-install", "")
	w.org = nil
	w.aliasName, w.callerRole, w.callerSession = "prod", "ops", "ci"
	w.release("0.24.0", true)
	w.clock = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	return w
}

// samplesInstalled is the manager 0.24.0 installed in account mode by ops@example.com, its plan and apply done an hour ago.
func samplesInstalled(t *testing.T) *rwWorld {
	w := samplesWorld(t)
	w.bucket, w.role, w.project, w.logGroup = true, true, true, true
	w.save(Settings{Env: "prod", Region: "eu-west-1", Version: "0.24.0", Mode: "account", AdminEmails: []string{"ops@example.com"},
		ReleaseBase: ReleasesURL + "/0.24.0", LastPlan: "dmt-prod-install:3f1c2a4e-6b8d-4c0e-9a1f-2b3c4d5e6f70"})
	w.objects["prod/applied.json"] = `{"version":"0.24.0","build":"dmt-prod-install:7a9b0c1d-2e3f-4a5b-8c6d-7e8f9a0b1c2d"}`
	w.objects["prod/outputs.json"] = strings.ReplaceAll(rwOutputs, "0.11.0", "0.24.0")
	w.objects["prod/manager.tfstate"] = rwState
	w.builds = append(w.builds,
		&rwBuild{id: "dmt-prod-install:3f1c2a4e-6b8d-4c0e-9a1f-2b3c4d5e6f70", env: map[string]string{"ACTION": "plan"}, status: "SUCCEEDED", start: w.clock.Add(-2 * time.Hour)},
		&rwBuild{id: "dmt-prod-install:7a9b0c1d-2e3f-4a5b-8c6d-7e8f9a0b1c2d", env: map[string]string{"ACTION": "apply", "PLAN_BUILD": "dmt-prod-install:3f1c2a4e-6b8d-4c0e-9a1f-2b3c4d5e6f70"},
			status: "SUCCEEDED", start: w.clock.Add(-time.Hour)})
	return w
}

// samplesFirst is the request of a samplesFirst install: the administrator given, the suggested answers taken.
var samplesFirst = Request{Yes: true, Flags: Settings{AdminEmails: []string{"ops@example.com"}}, Set: map[string]bool{"admin-emails": true}}

var installSamples = []installSample{
	{command: "aws install", line: "dormeto aws install --admin-emails ops@example.com --yes", r: samplesFirst, world: samplesWorld, run: Install},
	{command: "aws install check", line: "dormeto aws install check --admin-emails ops@example.com", r: Request{Flags: samplesFirst.Flags, Set: samplesFirst.Set},
		world: samplesWorld, run: InstallCheck, json: true},
	{command: "aws install apply", line: "dormeto aws install apply", world: func(t *testing.T) *rwWorld {
		w := samplesWorld(t)
		if code, out := samplesRun(t, w, Install, samplesFirst, nil); code != 0 {
			t.Fatalf("the install's plan: exit %d\n%s", code, out)
		}
		return w
	}, run: Apply},
	{command: "aws install status", line: "dormeto aws install status", world: samplesInstalled, run: Status, json: true},
	{command: "aws uninstall", line: "dormeto aws uninstall --yes", r: Request{Yes: true}, world: func(t *testing.T) *rwWorld {
		w := samplesInstalled(t)
		w.plan = rwEmpty // the deletion protection is off: nothing to lift
		s := w.saved()
		s.Vars = map[string]json.RawMessage{"deletion_protection": json.RawMessage("false")}
		w.save(s)
		return w
	}, run: Uninstall},
	{command: "aws uninstall apply", line: "dormeto aws uninstall apply", world: func(t *testing.T) *rwWorld {
		w := samplesInstalled(t)
		w.plan = rwEmpty
		s := w.saved()
		s.Vars = map[string]json.RawMessage{"deletion_protection": json.RawMessage("false")}
		w.save(s)
		if code, out := samplesRun(t, w, Uninstall, Request{Yes: true}, nil); code != 0 {
			t.Fatalf("the uninstall's plan: exit %d\n%s", code, out)
		}
		return w
	}, run: UninstallApply},
}

// samplesRun runs a command in the world, without a terminal, with the profile's clients whatever its name; with js, what
// --json prints goes there and the words to the output.
func samplesRun(t *testing.T, w *rwWorld, cmd func(context.Context, Deps, Request) int, r Request, js *bytes.Buffer) (int, string) {
	t.Helper()
	var b bytes.Buffer
	d := Deps{
		Clients:  func(context.Context, string, string) (Clients, error) { return w.clients(), nil },
		Region:   func(context.Context, string) (string, error) { return w.region, nil },
		Assume:   w.assume,
		HTTP:     w.http,
		Prompter: &rwPrompter{t: t, cancel: func() {}},
		Out:      &b,
		Now:      w.now,
		Every:    w.every,
		Version:  "0.14.0",
	}
	if js != nil {
		d.JSON = js
	}
	return cmd(context.Background(), d, r), b.String()
}

// samplesFile is a file of docs/samples, as cmd/dormeto's test writes it.
type samplesFile struct {
	Command string  `json:"command"`
	Line    string  `json:"line"`
	Table   string  `json:"table"`
	JSON    *string `json:"json"`
	Stderr  string  `json:"stderr,omitempty"`
}

func TestSamples(t *testing.T) {
	local := time.Local
	time.Local = time.UTC // the times a command prints in the local time zone
	t.Cleanup(func() { time.Local = local })
	for _, s := range installSamples {
		t.Run(s.command, func(t *testing.T) {
			code, out := samplesRun(t, s.world(t), s.run, s.r, nil)
			if code != 0 {
				t.Fatalf("exit %d\n%s", code, out)
			}
			f := samplesFile{Command: s.command, Line: s.line, Table: out}
			if s.json {
				var js bytes.Buffer
				code, words := samplesRun(t, s.world(t), s.run, s.r, &js)
				if code != 0 || !json.Valid(js.Bytes()) {
					t.Fatalf("--json: exit %d\n%s\n%s", code, js.String(), words)
				}
				text := js.String()
				f.JSON, f.Stderr = &text, words
			}
			writeOrCompare(t, f)
		})
	}
}

// writeOrCompare writes the sample's file with -update, else fails when it differs from it.
func writeOrCompare(t *testing.T, f samplesFile) {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"532105290256", "809706483662", "121014831971", "038606077307", "dormeto-dev", "kloudy", "127.0.0.1"} {
		if strings.Contains(b.String(), bad) {
			t.Errorf("the sample holds %q", bad)
		}
	}
	path := filepath.Join("..", "..", "docs", "samples", strings.ReplaceAll(f.Command, " ", "-")+".json")
	if *update { // config_test.go's -update
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if was, err := os.ReadFile(path); err != nil || !bytes.Equal(was, b.Bytes()) {
		t.Errorf("docs/samples/%s differs from what %q prints now; update it: go test ./internal/install -run Samples -update\nnow:\n%s",
			filepath.Base(path), f.Line, b.String())
	}
}
