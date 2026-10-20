package main

// `dormeto whoami` against the fake manager API: the caller, its role and the manager it reached.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const meAnswer = `{"email":"arn:aws:sts::111111111111:assumed-role/ops/ci","groups":[],"role":"admin","effectiveRole":"admin","grants":[{"tags":{},"permissions":["read","wake","nap"]}]}`

// whoamiAPI answers GET /iam/me and GET /iam/health.
func whoamiAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/iam/me":
		io.WriteString(w, meAnswer)
	case "/iam/health":
		io.WriteString(w, `{"status":"ok","version":"0.17.1"}`)
	default:
		w.WriteHeader(404)
	}
}

func TestWhoamiPrintsTheCallerAndTheManager(t *testing.T) {
	api := &fakeAPI{custom: whoamiAPI}
	r := runFleet(t, api, "eu-west-1", "whoami", "--env", "dev")
	t.Logf("\n%s", r.stdout)
	if r.exit != 0 || len(api.requests) != 2 {
		t.Fatalf("exit %d, %d requests, stderr %q", r.exit, len(api.requests), r.stderr)
	}
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	want := []string{"arn      arn:aws:sts::111111111111:assumed-role/ops/ci", "role     admin", "manager  0.17.1", "api url  http://127.0.0.1:",
		"env      dev", "region   eu-west-1"}
	if len(lines) != len(want) {
		t.Fatalf("lines %q", lines)
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w) {
			t.Errorf("line %d %q, want %q", i, lines[i], w)
		}
	}
}

func TestWhoamiWithAnAPIURLHasNoEnv(t *testing.T) {
	srv := httptest.NewServer(&fakeAPI{custom: whoamiAPI})
	t.Cleanup(srv.Close)
	r := runFleet(t, &fakeAPI{}, "eu-west-1", "whoami", "--api-url", srv.URL)
	if r.exit != 0 || len(r.located) != 0 || strings.Contains(r.stdout, "env") || !strings.Contains(r.stdout, "api url  "+srv.URL+"\n") {
		t.Errorf("exit %d, located %v, stdout %q, stderr %q", r.exit, r.located, r.stdout, r.stderr)
	}
	r = runFleet(t, &fakeAPI{custom: whoamiAPI}, "eu-west-1", "whoami")
	if r.exit != 0 || !strings.Contains(r.stdout, "env      prod\n") {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
}

func TestWhoamiJSON(t *testing.T) {
	r := runFleet(t, &fakeAPI{custom: whoamiAPI}, "eu-west-1", "whoami", "--json")
	var got struct {
		Me      map[string]any `json:"me"`
		Version string         `json:"version"`
		APIURL  string         `json:"apiUrl"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || r.exit != 0 {
		t.Fatalf("exit %d, %v: %q", r.exit, err, r.stdout)
	}
	if got.Me["role"] != "admin" || got.Me["grants"] == nil || got.Version != "0.17.1" || !strings.HasPrefix(got.APIURL, "http://127.0.0.1:") {
		t.Errorf("%+v", got)
	}
}

func TestWhoamiRefusedExits1(t *testing.T) {
	r := runFleet(t, &fakeAPI{status: 403, body: `{"message":"Forbidden"}`}, "eu-west-1", "whoami")
	if r.exit != 1 || !strings.Contains(r.stderr, "execute-api:Invoke") || r.stdout != "" {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
	r = runFleet(t, &fakeAPI{}, "eu-west-1", "whoami", "extra")
	if r.exit != 2 {
		t.Errorf("an argument: exit %d, stderr %q", r.exit, r.stderr)
	}
}
