package install

// `dormeto aws install check --json` and `install status --json`: what their text says, as one JSON object on the standard
// output (Deps.JSON), the words going on to Out (the standard error).

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// jsonCheck is a check as --json prints it.
type jsonCheck struct{ Name, Detail, Verdict string }

func TestCheckJSONPrintsTheChecks(t *testing.T) {
	w := rwNew(t)
	w.readOnly = true
	w.json = &bytes.Buffer{}
	r := Request{Profile: "p", Region: "eu-west-1", Flags: Settings{AdminEmails: []string{"you@example.com"}}, Set: map[string]bool{"admin-emails": true}}
	code, out, _ := w.run(InstallCheck, r, true)
	var got struct {
		Env, Account, Region string
		OK                   bool
		Checks               []jsonCheck
	}
	if err := json.Unmarshal(w.json.Bytes(), &got); code != 0 || err != nil || got.Env != "prod" || got.Account != "532105290256" || got.Region != "eu-west-1" || !got.OK {
		t.Fatalf("exit %d, %v: %s", code, err, w.json)
	}
	for _, want := range []jsonCheck{{"organization", "o-vvk0115ixn: all features", "ok"}, {"release", "manager 0.11.0", "ok"},
		{"administrators", "you@example.com", "ok"}, {"organization access", "not deployed (no management profile)", "warn"}} {
		if !slices.Contains(got.Checks, want) {
			t.Errorf("no %+v in %+v", want, got.Checks)
		}
	}
	// The words are said all the same, on the run's Out.
	if !strings.Contains(out, "\nChecks\n") {
		t.Fatalf("words:\n%s", out)
	}
	// A check that fails: ok false, exit 1.
	w = rwNew(t)
	w.readOnly = true
	w.bucket = true
	w.objects["dev/install.json"] = "{}"
	w.json = &bytes.Buffer{}
	code, _, _ = w.run(InstallCheck, rwFlags(), false)
	got.OK = true
	if err := json.Unmarshal(w.json.Bytes(), &got); code != 1 || err != nil || got.OK ||
		!slices.Contains(got.Checks, jsonCheck{"another environment", "one environment per account: dev installed here", "fail"}) {
		t.Fatalf("exit %d, %v: %s", code, err, w.json)
	}
}

func TestStatusJSONPrintsTheVersionsTheLastBuildsAndTheChecks(t *testing.T) {
	w := rwNew(t)
	w.installed("0.11.0")
	w.release("0.12.0", true)
	w.readOnly = true
	w.json = &bytes.Buffer{}
	code, out, _ := w.run(Status, rwStatus, true)
	type build struct{ ID, Status, StartedAt string }
	var got struct {
		Env, Account, Region string
		Version, Latest      string
		UpgradeAvailable     bool
		Builds               struct{ Plan, Apply, DashboardUpdate *build }
		Checks               []jsonCheck
		OK                   bool
	}
	if err := json.Unmarshal(w.json.Bytes(), &got); code != 0 || err != nil || got.Env != "prod" || got.Version != "0.11.0" || got.Latest != "0.12.0" ||
		!got.UpgradeAvailable || !got.OK {
		t.Fatalf("exit %d, %v: %s", code, err, w.json)
	}
	if p := got.Builds.Plan; p == nil || p.ID != "dmt-prod-install:p0" || p.Status != "succeeded" || p.StartedAt != rwT0.Add(-2*time.Hour).UTC().Format(time.RFC3339) ||
		got.Builds.Apply == nil || got.Builds.Apply.ID != "dmt-prod-install:a0" || got.Builds.DashboardUpdate != nil {
		t.Fatalf("builds: %s", w.json)
	}
	for _, want := range []string{"dashboard", "administrators", "organization access"} {
		if !slices.ContainsFunc(got.Checks, func(c jsonCheck) bool { return c.Name == want }) {
			t.Errorf("no check %s: %s", want, w.json)
		}
	}
	if !strings.Contains(out, "Dormeto prod in 532105290256 eu-west-1\n") {
		t.Fatalf("words:\n%s", out)
	}
}
