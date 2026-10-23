package main

// `workloads description` and `routers description` against the fake manager API, and the description in the two
// `show` commands.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const descriptionJSON = `{"text":"# Shop\n\nOwned by **payments**.","by":"ann@x.io","at":"2026-10-19T10:00:00.000Z"}`

// descAPI is a fake manager's description routes of the workload prod-eu/w-1 and of the router prod-eu.
type descAPI struct {
	mu          sync.Mutex
	writes      []string
	description string // what GET reports; "" for none
	refuse      int    // the status every write answers, when set
}

func (a *descAPI) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	defer a.mu.Unlock()
	field := `"description":` + orString(a.description, "null") + `,`
	if a.description == "absent" { // a manager before 0.19.0
		field = ""
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/iam/workloads/prod-eu/w-1":
		io.WriteString(w, `{"workloadId":"w-1","state":"AWAKE",`+field+`"settings":{}}`)
	case r.Method == http.MethodGet && r.URL.Path == "/iam/routers/prod-eu":
		io.WriteString(w, strings.Replace(routerDetailsAnswer, `"pauses":`, field+`"pauses":`, 1))
	case r.Method != http.MethodGet && strings.HasSuffix(r.URL.Path, "/description") && !strings.Contains(r.URL.Path, "/nope/"):
		a.writes = append(a.writes, r.Method+" "+r.URL.Path+" "+string(body))
		switch {
		case a.refuse == 403:
			w.WriteHeader(403)
			io.WriteString(w, `{"error":"forbidden","message":"administrators only"}`)
		case a.refuse == 400:
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"bad_request","message":"the description has a control character"}`)
		case r.Method == http.MethodDelete || strings.Contains(string(body), `"text":""`):
			io.WriteString(w, `{"description":null}`)
		default:
			io.WriteString(w, `{"description":{"text":"x","by":"ann@x.io","at":"2026-10-19T12:00:00.000Z"}}`)
		}
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"error":"not_found","message":"workload not found"}`)
	}
}

func runDesc(t *testing.T, a *descAPI, stdin string, args ...string) fleetRan {
	t.Helper()
	return runFleetWith(t, &fakeAPI{custom: a.handler, stdin: stdin}, "eu-west-1", nil, args...)
}

func TestDescriptionWithoutAFlagPrintsTheTextAsWritten(t *testing.T) {
	for _, args := range [][]string{{"workloads", "description", "prod-eu/w-1"}, {"routers", "description", "prod-eu"}} {
		r := runDesc(t, &descAPI{description: descriptionJSON}, "", args...)
		want := "# Shop\n\nOwned by **payments**.\n— ann@x.io, 2026-10-19 10:00 UTC\n"
		if r.exit != 0 || r.stdout != want {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, r.exit, r.stdout, r.stderr)
		}
	}
}

func TestDescriptionWithoutOneSaysSoWhenNull(t *testing.T) {
	for _, args := range [][]string{{"workloads", "description", "prod-eu/w-1"}, {"routers", "description", "prod-eu"}} {
		r := runDesc(t, &descAPI{}, "", args...)
		if r.exit != 0 || r.stdout != "no description\n" {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, r.exit, r.stdout, r.stderr)
		}
	}
}

// An answer without the key is a manager before 0.19.0, or one whose read failed: never "no description", so that
// nobody writes over a text that could not be seen.
func TestDescriptionAbsentFromTheAnswerIsAnErrorNotNoDescription(t *testing.T) {
	want := "the manager did not return the description (manager 0.19.0 or later, or its read failed)"
	for _, args := range [][]string{{"workloads", "description", "prod-eu/w-1"}, {"routers", "description", "prod-eu"}, {"routers", "description", "prod-eu", "--json"}} {
		r := runDesc(t, &descAPI{description: "absent"}, "", args...)
		if r.exit != 1 || r.stdout != "" || !strings.Contains(r.stderr, want) {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, r.exit, r.stdout, r.stderr)
		}
	}
}

func TestDescriptionJSONIsTheAnswer(t *testing.T) {
	a := &descAPI{description: descriptionJSON}
	r := runDesc(t, a, "", "workloads", "description", "prod-eu/w-1", "--json")
	mustContain(t, r.stdout, `"description"`, `"by": "ann@x.io"`)
	if strings.Contains(r.stdout, "workloadId") {
		t.Errorf("the whole detail is printed: %s", r.stdout)
	}
	r = runDesc(t, &descAPI{}, "", "workloads", "description", "prod-eu/w-1", "--json")
	if r.exit != 0 || strings.TrimSpace(r.stdout) != `{
  "description": null
}` {
		t.Errorf("no description: exit %d, stdout %q", r.exit, r.stdout)
	}
	r = runDesc(t, a, "", "routers", "description", "prod-eu", "--set", "x", "--json")
	mustContain(t, r.stdout, `"description"`, `"by": "ann@x.io"`)
}

func TestDescriptionSetSendsTheTextAndSaysHowLong(t *testing.T) {
	a := &descAPI{}
	r := runDesc(t, a, "", "workloads", "description", "prod-eu/w-1", "--set", "x")
	if want := []string{`PUT /iam/workloads/prod-eu/w-1/description {"text":"x"}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Fatalf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	if r.stdout != "prod-eu/w-1: description set (1 character)\n" {
		t.Errorf("stdout %q", r.stdout)
	}
	a = &descAPI{}
	r = runDesc(t, a, "", "routers", "description", "prod-eu", "--set", "x")
	if want := []string{`PUT /iam/routers/prod-eu/description {"text":"x"}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) || r.stdout != "prod-eu: description set (1 character)\n" {
		t.Errorf("exit %d, writes %q, stdout %q, stderr %q", r.exit, a.writes, r.stdout, r.stderr)
	}
}

func TestDescriptionSetEmptyClears(t *testing.T) {
	a := &descAPI{}
	r := runDesc(t, a, "", "workloads", "description", "prod-eu/w-1", "--set", "")
	if want := []string{`PUT /iam/workloads/prod-eu/w-1/description {"text":""}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) || r.stdout != "prod-eu/w-1: description cleared\n" {
		t.Errorf("exit %d, writes %q, stdout %q, stderr %q", r.exit, a.writes, r.stdout, r.stderr)
	}
}

func TestDescriptionFileFromStdinAndFromAPath(t *testing.T) {
	a := &descAPI{}
	r := runDesc(t, a, "line one\nline \"two\"\n", "workloads", "description", "prod-eu/w-1", "--file", "-")
	if want := []string{`PUT /iam/workloads/prod-eu/w-1/description {"text":"line one\nline \"two\"\n"}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Errorf("stdin: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	path := filepath.Join(t.TempDir(), "d.md")
	os.WriteFile(path, []byte("from a file"), 0o600)
	a = &descAPI{}
	r = runDesc(t, a, "", "routers", "description", "prod-eu", "--file", path)
	if want := []string{`PUT /iam/routers/prod-eu/description {"text":"from a file"}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Errorf("path: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	r = runDesc(t, a, "", "routers", "description", "prod-eu", "--file", filepath.Join(t.TempDir(), "none"))
	if r.exit != 2 || len(a.writes) != 1 {
		t.Errorf("missing file: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestDescriptionFileThatIsEmptyOrNotTextOrTooBigSendsNothing(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o600)
		return p
	}
	for name, c := range map[string]struct {
		stdin, path, want string
	}{
		"empty file":      {path: write("empty.md", nil), want: "--file: " + filepath.Join(dir, "empty.md") + " is empty (use --clear to remove the description)"},
		"blank file":      {path: write("blank.md", []byte(" \n\t\n")), want: "is empty (use --clear to remove the description)"},
		"empty stdin":     {stdin: "", path: "-", want: "--file: standard input is empty (use --clear to remove the description)"},
		"bom alone":       {stdin: "\ufeff\n", path: "-", want: "is empty"},
		"not UTF-8":       {stdin: "ok \xff\xfe", path: "-", want: "--file: not UTF-8 text"},
		"larger than 64k": {stdin: strings.Repeat("a", 64*1024+1), path: "-", want: "--file: larger than 64 KiB"},
	} {
		a := &descAPI{}
		r := runDesc(t, a, c.stdin, "workloads", "description", "prod-eu/w-1", "--file", c.path)
		if r.exit != 2 || len(a.writes) != 0 || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%s: exit %d, writes %q, stderr %q", name, r.exit, a.writes, r.stderr)
		}
	}
	// exactly 64 KiB passes the size guard (the manager's own 4,000 characters limit is its to say)
	a := &descAPI{}
	r := runDesc(t, a, strings.Repeat("a", 64*1024), "workloads", "description", "prod-eu/w-1", "--file", "-")
	if r.exit != 0 || len(a.writes) != 1 {
		t.Errorf("64 KiB: exit %d, writes %d, stderr %q", r.exit, len(a.writes), r.stderr)
	}
}

func TestDescriptionFileDropsALeadingBOM(t *testing.T) {
	a := &descAPI{}
	r := runDesc(t, a, "\ufeff# Shop\n", "routers", "description", "prod-eu", "--file", "-")
	if want := []string{`PUT /iam/routers/prod-eu/description {"text":"# Shop\n"}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestDescriptionClearSendsDelete(t *testing.T) {
	a := &descAPI{}
	r := runDesc(t, a, "", "workloads", "description", "prod-eu/w-1", "--clear")
	if want := []string{`DELETE /iam/workloads/prod-eu/w-1/description `}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) || r.stdout != "prod-eu/w-1: description cleared\n" {
		t.Errorf("exit %d, writes %q, stdout %q, stderr %q", r.exit, a.writes, r.stdout, r.stderr)
	}
	a = &descAPI{}
	r = runDesc(t, a, "", "routers", "description", "prod-eu", "--clear")
	if want := []string{`DELETE /iam/routers/prod-eu/description `}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestDescriptionMoreThanOneFlagOrABadTargetSendsNothing(t *testing.T) {
	for _, args := range [][]string{
		{"workloads", "description", "prod-eu/w-1", "--set", "x", "--clear"},
		{"workloads", "description", "prod-eu/w-1", "--set", "x", "--file", "-"},
		{"routers", "description", "prod-eu", "--file", "-", "--clear"},
		{"routers", "description", "prod-eu", "--set", "x", "--file", "-", "--clear"},
		{"workloads", "description", "prod-eu"},
		{"workloads", "description"},
		{"routers", "description"},
		{"routers", "description", "a", "b"},
	} {
		a := &descAPI{description: descriptionJSON}
		r := runDesc(t, a, "", args...)
		if r.exit != 2 || len(a.writes) != 0 || len(r.api.requests) != 0 {
			t.Errorf("%q: exit %d, writes %q, requests %d, stderr %q", args, r.exit, a.writes, len(r.api.requests), r.stderr)
		}
	}
}

func TestDescriptionRefusalIsPrintedWithExit1(t *testing.T) {
	r := runDesc(t, &descAPI{refuse: 403}, "", "routers", "description", "prod-eu", "--set", "x")
	if r.exit != 1 || !strings.Contains(r.stderr, "administrators only") {
		t.Errorf("403: exit %d, stderr %q", r.exit, r.stderr)
	}
	r = runDesc(t, &descAPI{refuse: 400}, "", "workloads", "description", "prod-eu/w-1", "--set", "x")
	if r.exit != 1 || !strings.Contains(r.stderr, "control character") {
		t.Errorf("400: exit %d, stderr %q", r.exit, r.stderr)
	}
	r = runDesc(t, &descAPI{}, "", "workloads", "description", "prod-eu/nope", "--clear")
	if r.exit != 1 || !strings.Contains(r.stderr, "workload not found") {
		t.Errorf("404: exit %d, stderr %q", r.exit, r.stderr)
	}
}

// A manager before 0.19.0 has no description route: its "no route for" answer says what to do.
func TestDescriptionWritesOnAnOldManagerSayWhichVersionIsNeeded(t *testing.T) {
	old := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		io.WriteString(w, `{"error":"not_found","message":"no route for `+r.Method+` `+r.URL.Path+`"}`)
	}
	for _, args := range [][]string{
		{"workloads", "description", "prod-eu/w-1", "--set", "x"},
		{"routers", "description", "prod-eu", "--clear"},
	} {
		r := runFleetWith(t, &fakeAPI{custom: old}, "eu-west-1", nil, args...)
		if r.exit != 1 || !strings.Contains(r.stderr, "needs manager 0.19.0 or later (no route for ") {
			t.Errorf("%q: exit %d, stderr %q", args, r.exit, r.stderr)
		}
	}
}

func TestShowsPrintTheDescriptionIndented(t *testing.T) {
	want := "\ndescription\n  # Shop\n\n  Owned by **payments**.\n  — ann@x.io, 2026-10-19 10:00 UTC\n"
	for _, args := range [][]string{{"workloads", "show", "prod-eu/w-1"}, {"routers", "show", "prod-eu"}} {
		r := runDesc(t, &descAPI{description: descriptionJSON}, "", args...)
		if r.exit != 0 || !strings.Contains(r.stdout, want) {
			t.Errorf("%q: exit %d, stdout:\n%s", args, r.exit, r.stdout)
		}
		for _, desc := range []string{"", "absent"} {
			r = runDesc(t, &descAPI{description: desc}, "", args...)
			if r.exit != 0 || strings.Contains(r.stdout, "description") {
				t.Errorf("%q without one: exit %d, stdout:\n%s", args, r.exit, r.stdout)
			}
		}
	}
}
