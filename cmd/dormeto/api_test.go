package main

// `dormeto api` against the fake manager API: the request it signs and sends, the body printed, the exit codes.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// rawAPI answers every request with status and body, recording the method, path, query and body of each.
type rawAPI struct {
	mu     sync.Mutex
	status int
	body   string
	seen   []string // "GET /iam/workloads?limit=1 <body>"
	types  []string // the Content-Type of each
}

func (a *rawAPI) handler(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.seen = append(a.seen, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+string(b)))
	a.types = append(a.types, r.Header.Get("Content-Type"))
	a.mu.Unlock()
	if a.status != 0 {
		w.WriteHeader(a.status)
	}
	io.WriteString(w, a.body)
}

func runAPI(t *testing.T, a *rawAPI, stdin string, args ...string) fleetRan {
	t.Helper()
	return runFleet(t, &fakeAPI{custom: a.handler, stdin: stdin}, "eu-west-1", append([]string{"api"}, args...)...)
}

func TestAPIGetWithQueryPrintsTheBodyIndented(t *testing.T) {
	a := &rawAPI{body: `{"workloads":[],"total":0}`}
	r := runAPI(t, a, "", "get", "/iam/workloads?state=ASLEEP", "-q", "limit=1", "-q", "tag=team=a", "--query", "tag=env=dev")
	if r.exit != 0 || len(a.seen) != 1 {
		t.Fatalf("exit %d, seen %q, stderr %q", r.exit, a.seen, r.stderr)
	}
	if want := "GET /iam/workloads?limit=1&state=ASLEEP&tag=env%3Ddev&tag=team%3Da"; a.seen[0] != want {
		t.Errorf("sent %q, want %q", a.seen[0], want)
	}
	if want := "{\n  \"workloads\": [],\n  \"total\": 0\n}\n"; r.stdout != want {
		t.Errorf("stdout %q, want %q", r.stdout, want)
	}
	if r.stderr != "" {
		t.Errorf("stderr %q", r.stderr)
	}
}

func TestAPIPostDataFromAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(file, []byte(`{"reason":"release"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &rawAPI{status: 202, body: `{"accepted":true}`}
	r := runAPI(t, a, "", "POST", "/routers/prod-eu/pause", "--data", "@"+file)
	if r.exit != 0 || len(a.seen) != 1 || a.seen[0] != `POST /iam/routers/prod-eu/pause {"reason":"release"}` || a.types[0] != "application/json" {
		t.Errorf("exit %d, seen %q, types %q, stderr %q", r.exit, a.seen, a.types, r.stderr)
	}
}

func TestAPIDataFromStdinAndInline(t *testing.T) {
	a := &rawAPI{body: `{}`}
	r := runAPI(t, a, `{"role":"viewer"}`, "PUT", "/users/a%40b.c/role", "--data", "-")
	if r.exit != 0 || a.seen[0] != `PUT /iam/users/a%40b.c/role {"role":"viewer"}` {
		t.Errorf("stdin: exit %d, seen %q, stderr %q", r.exit, a.seen, r.stderr)
	}
	a = &rawAPI{status: 204}
	r = runAPI(t, a, "", "delete", "/exclusions/ex-1", "--data", `{"x":1}`)
	if r.exit != 0 || a.seen[0] != `DELETE /iam/exclusions/ex-1 {"x":1}` || r.stdout != "" {
		t.Errorf("inline: exit %d, seen %q, stdout %q", r.exit, a.seen, r.stdout)
	}
}

func TestAPINon2xxExits1WithTheBodyOnStdout(t *testing.T) {
	a := &rawAPI{status: 404, body: `{"error":"not_found","message":"no route for GET /iam/nope"}`}
	r := runAPI(t, a, "", "GET", "/nope")
	if r.exit != 1 || r.stderr != "HTTP 404\n" || !strings.Contains(r.stdout, `"message": "no route for GET /iam/nope"`) {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
	a = &rawAPI{status: 302, body: "moved"}
	r = runAPI(t, a, "", "POST", "/x")
	if r.exit != 1 || r.stderr != "HTTP 302\n" || r.stdout != "moved\n" {
		t.Errorf("302: exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
}

func TestAPIBadArgumentsExit2AndSendNothing(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"GET"},
		{"GET", "/a", "/b"},
		{"PATCH", "/routers"},
		{"GET", "routers"},
		{"GET", "/routers", "-q", "limit"},
		{"GET", "/routers", "-q", "=1"},
		{"POST", "/routers", "--data", "@/nonexistent/body.json"},
		{"GET", "/routers?%zz"},
	} {
		a := &rawAPI{}
		r := runAPI(t, a, "", args...)
		if r.exit != 2 || len(a.seen) != 0 || r.stderr == "" {
			t.Errorf("%q: exit %d, seen %q, stderr %q", args, r.exit, a.seen, r.stderr)
		}
	}
}
