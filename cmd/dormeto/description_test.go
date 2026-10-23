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

func TestDescriptionWithoutOneSaysSoWhetherNullOrAbsent(t *testing.T) {
	for _, desc := range []string{"", "absent"} {
		a := &descAPI{description: desc}
		h := func(w http.ResponseWriter, r *http.Request) { // a manager before 0.19.0 sends no description at all
			if desc == "absent" {
				io.WriteString(w, `{"workloadId":"w-1","state":"AWAKE"}`)
				return
			}
			a.handler(w, r)
		}
		r := runFleetWith(t, &fakeAPI{custom: h}, "eu-west-1", nil, "workloads", "description", "prod-eu/w-1")
		if r.exit != 0 || r.stdout != "no description\n" {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", desc, r.exit, r.stdout, r.stderr)
		}
	}
}

func TestDescriptionJSONIsTheAnswer(t *testing.T) {
	a := &descAPI{description: descriptionJSON}
	r := runDesc(t, a, "", "workloads", "description", "prod-eu/w-1", "--json")
	mustContain(t, r.stdout, `"by": "ann@x.io"`, `"workloadId": "w-1"`)
	r = runDesc(t, a, "", "routers", "description", "prod-eu", "--set", "x", "--json")
	mustContain(t, r.stdout, `"description"`, `"by": "ann@x.io"`)
}

func TestDescriptionSetSendsTheTextAndSaysHowLong(t *testing.T) {
	a := &descAPI{}
	r := runDesc(t, a, "", "workloads", "description", "prod-eu/w-1", "--set", "x")
	if want := []string{`PUT /iam/workloads/prod-eu/w-1/description {"text":"x"}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Fatalf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	if r.stdout != "prod-eu/w-1: description set (1 characters)\n" {
		t.Errorf("stdout %q", r.stdout)
	}
	a = &descAPI{}
	r = runDesc(t, a, "", "routers", "description", "prod-eu", "--set", "x")
	if want := []string{`PUT /iam/routers/prod-eu/description {"text":"x"}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) || r.stdout != "prod-eu: description set (1 characters)\n" {
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
