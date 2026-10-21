package main

// `dormeto users` and `dormeto groups` against a fake manager: the requests they send (escaped paths, bodies), what
// they print, the exit codes, and that a refused argument sends nothing.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// accessAPI is a fake manager for users and groups: the answer of each "METHOD path", and every request received
// ("METHOD requestURI body").
type accessAPI struct {
	mu       sync.Mutex
	routes   map[string]string // "METHOD /iam/path" (decoded) -> answer; a "409:" prefix answers a conflict with the rest as its message
	requests []string
}

func (a *accessAPI) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		a.requests = append(a.requests, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		answer, ok := a.routes[r.Method+" "+r.URL.Path]
		switch {
		case !ok:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"not_found","message":"no route"}`)
		case strings.HasPrefix(answer, "409:"):
			w.WriteHeader(409)
			b, _ := json.Marshal(map[string]string{"error": "conflict", "message": strings.TrimPrefix(answer, "409:")})
			w.Write(b)
		default:
			io.WriteString(w, answer)
		}
	}
}

func runAccess(t *testing.T, a *accessAPI, tune func(*fleetDeps), args ...string) fleetRan {
	t.Helper()
	return runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", tune, args...)
}

const lastAdmin = "the last administrator: give the admin role to another user first"

func TestUsersInvitePostsEmailRoleAndGroups(t *testing.T) {
	a := &accessAPI{routes: map[string]string{"POST /iam/users": `{"email":"ana@x.io","role":"operator","groups":["ops"],"invitation":"pending-verification"}`}}
	r := runAccess(t, a, nil, "users", "invite", " Ana@X.io ", "--role", "operator", "--group", "ops", "--group", "dev")
	if r.exit != 0 || len(a.requests) != 1 || a.requests[0] != `POST /iam/users {"email":"ana@x.io","groups":["ops","dev"],"role":"operator"}` {
		t.Fatalf("exit %d, requests %q, stderr %q", r.exit, a.requests, r.stderr)
	}
	if r.stdout != "invited ana@x.io as operator: invitation pending-verification\n" {
		t.Errorf("stdout %q", r.stdout)
	}
	a.requests = nil
	r = runAccess(t, a, nil, "users", "invite", "ana@x.io", "--role", "viewer", "--json")
	if a.requests[0] != `POST /iam/users {"email":"ana@x.io","groups":[],"role":"viewer"}` || !strings.Contains(r.stdout, `"invitation": "pending-verification"`) {
		t.Errorf("requests %q, stdout %q", a.requests, r.stdout)
	}
}

func TestUsersRefusalsSendNothing(t *testing.T) {
	for _, args := range [][]string{
		{"users", "invite", "not-an-email", "--role", "admin"},
		{"users", "invite", "a@b.c", "--role", "root"},
		{"users", "invite", "a@b.c"},
		{"users", "invite", "a@b.c", "--role", "admin", "--group", ""},
		{"users", "role", "a@b", "admin", "--yes"},
		{"users", "role", "a@b.c", "boss", "--yes"},
		{"users", "role", "a@b.c"},
		{"users", "disable", "x", "--yes"},
		{"users", "enable", "x"},
		{"users", "resend", "x"},
		{"users", "reset-mfa", "x"},
		{"users", "groups", "x"},
		{"users", "groups", "a@b.c", " "},
		{"users", "list", "extra"},
		{"groups", "create", "dmt-x"},
		{"groups", "create", "Bad"},
		{"groups", "create", "ok", "--description", strings.Repeat("a", 201)},
		{"groups", "create", "ok", "--grant", "team=a:write"},
		{"groups", "create", "ok", "--grant", "team=a"},
		{"groups", "update", "x"},
		{"groups", "update", "x", "--grant", ":read", "--no-grants"},
		{"groups", "update", "x", "--grant", "bad"},
		{"groups", "delete", " ", "--yes"},
		{"groups", "delete", "x"}, // no terminal: --yes needed
		{"users", "role", "a@b.c", "admin"},
		{"users", "disable", "a@b.c"},
	} {
		a := &accessAPI{}
		r := runAccess(t, a, nil, args...)
		if r.exit != 2 || r.stderr == "" || len(a.requests) != 0 {
			t.Errorf("%v: exit %d, stderr %q, requests %q", args, r.exit, r.stderr, a.requests)
		}
	}
	// 21 grants.
	args := []string{"groups", "create", "ok"}
	for range 21 {
		args = append(args, "--grant", ":read")
	}
	a := &accessAPI{}
	if r := runAccess(t, a, nil, args...); r.exit != 2 || len(a.requests) != 0 || !strings.Contains(r.stderr, "at most 20") {
		t.Errorf("21 grants: exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestUsersRoleEscapesThePathAndConfirms(t *testing.T) {
	a := &accessAPI{routes: map[string]string{"PUT /iam/users/a+b@x.io/role": `{"email":"a+b@x.io","role":"admin"}`}}
	r := runAccess(t, a, nil, "users", "role", "a+b@x.io", "admin", "--yes")
	if r.exit != 0 || len(a.requests) != 1 || a.requests[0] != `PUT /iam/users/a+b@x.io/role {"role":"admin"}` {
		t.Fatalf("exit %d, requests %q, stderr %q", r.exit, a.requests, r.stderr)
	}
	if r.stdout != "a+b@x.io is now admin\n" {
		t.Errorf("stdout %q", r.stdout)
	}
	// An email with a slash-free but escapable character is escaped as url.PathEscape gives it.
	a = &accessAPI{routes: map[string]string{"PUT /iam/users/a%b@x.io/role": `{}`}}
	runAccess(t, a, nil, "users", "role", "a%b@x.io", "viewer", "--yes")
	if len(a.requests) != 1 || !strings.HasPrefix(a.requests[0], "PUT /iam/users/a%25b@x.io/role ") {
		t.Errorf("requests %q", a.requests)
	}
}

func TestUsersRoleDisableAndGroupsDeleteAreAlwaysConfirmed(t *testing.T) {
	for _, c := range []struct {
		args     []string
		question string
		route    string
	}{
		{[]string{"users", "role", "a@b.c", "viewer"}, "Make a@b.c viewer?", "PUT /iam/users/a@b.c/role"},
		{[]string{"users", "disable", "a@b.c"}, "Disable a@b.c? They can no longer sign in.", "POST /iam/users/a@b.c/disable"},
		{[]string{"groups", "delete", "pay"}, "Delete group pay? Its grants are removed and its members lose them.", "DELETE /iam/groups/pay"},
	} {
		for _, yes := range []bool{false, true} {
			a := &accessAPI{routes: map[string]string{c.route: `{}`}}
			var asked []string
			r := runAccess(t, a, func(d *fleetDeps) {
				d.terminal = func(io.Reader) bool { return true }
				d.confirm = func(_ context.Context, _ io.Reader, _ io.Writer, q string) (bool, error) {
					asked = append(asked, q)
					return yes, nil
				}
			}, c.args...)
			if r.exit != 0 || len(asked) != 1 || asked[0] != c.question || (len(a.requests) == 1) != yes {
				t.Errorf("%v (yes %v): exit %d, asked %q, requests %q", c.args, yes, r.exit, asked, a.requests)
			}
			if !yes && !strings.Contains(r.stdout, "not confirmed: nothing sent") {
				t.Errorf("%v: stdout %q", c.args, r.stdout)
			}
		}
		a := &accessAPI{}
		r := runAccess(t, a, nil, append(c.args, "--dry-run")...)
		if r.exit != 0 || len(a.requests) != 0 || !strings.Contains(r.stdout, "dry run: 1 ") {
			t.Errorf("%v --dry-run: exit %d, stdout %q, requests %q", c.args, r.exit, r.stdout, a.requests)
		}
	}
}

func TestUsersLastAdministratorIsPrintedAndExits1(t *testing.T) {
	for _, args := range [][]string{{"users", "role", "a@b.c", "viewer", "--yes"}, {"users", "disable", "a@b.c", "--yes"}} {
		a := &accessAPI{routes: map[string]string{"PUT /iam/users/a@b.c/role": "409:" + lastAdmin, "POST /iam/users/a@b.c/disable": "409:" + lastAdmin}}
		r := runAccess(t, a, nil, args...)
		if r.exit != 1 || r.stderr != lastAdmin+"\n" || r.stdout != "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, r.exit, r.stdout, r.stderr)
		}
	}
}

func TestUsersGroupsSendsTheListAndNoneClears(t *testing.T) {
	a := &accessAPI{routes: map[string]string{"PUT /iam/users/a@b.c/groups": `{"email":"a@b.c","groups":[]}`}}
	r := runAccess(t, a, nil, "users", "groups", "a@b.c")
	if r.exit != 0 || a.requests[0] != `PUT /iam/users/a@b.c/groups {"groups":[]}` || r.stdout != "a@b.c is in no groups\n" {
		t.Errorf("exit %d, requests %q, stdout %q", r.exit, a.requests, r.stdout)
	}
	a.requests = nil
	r = runAccess(t, a, nil, "users", "groups", "a@b.c", "ops", "dev", "ops")
	if a.requests[0] != `PUT /iam/users/a@b.c/groups {"groups":["ops","dev"]}` || r.stdout != "a@b.c is in ops, dev\n" {
		t.Errorf("requests %q, stdout %q", a.requests, r.stdout)
	}
}

func TestUsersSimpleActions(t *testing.T) {
	a := &accessAPI{routes: map[string]string{
		"POST /iam/users/a@b.c/enable":    `{"email":"a@b.c","enabled":true}`,
		"POST /iam/users/a@b.c/resend":    `{"invitation":"sent"}`,
		"POST /iam/users/a@b.c/mfa/reset": `{"email":"a@b.c","mfa":false,"tokenDeleted":true}`,
	}}
	for args, want := range map[string]string{
		"enable":    "enabled a@b.c\n",
		"resend":    "invitation sent again to a@b.c\n",
		"reset-mfa": "mfa of a@b.c reset (token deleted)\n",
	} {
		a.requests = nil
		r := runAccess(t, a, nil, "users", args, "A@b.c")
		if r.exit != 0 || r.stdout != want || len(a.requests) != 1 || !strings.HasSuffix(a.requests[0], " ") {
			t.Errorf("%s: exit %d, stdout %q, requests %q, stderr %q", args, r.exit, r.stdout, a.requests, r.stderr)
		}
	}
	r := runAccess(t, a, nil, "users", "reset-mfa", "a@b.c", "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || got["tokenDeleted"] != true || got["mfa"] != false {
		t.Errorf("%v: %q", err, r.stdout)
	}
}

const usersAnswer = `{"users":[{"email":"a@x.io","role":"admin","groups":["pay"],"status":"CONFIRMED","enabled":true,"createdAt":"2026-10-17T12:00:00Z","mfa":true,"extra":1},
 {"email":"b@x.io","role":"member","groups":[],"status":"FORCE_CHANGE_PASSWORD","enabled":false,"createdAt":null}]}`

func TestUsersList(t *testing.T) {
	a := &accessAPI{routes: map[string]string{"GET /iam/users": usersAnswer}}
	r := runAccess(t, a, nil, "users", "list")
	if r.exit != 0 || !strings.Contains(r.stdout, "a@x.io") || !strings.Contains(r.stdout, "disabled") || !strings.Contains(r.stdout, "2d") {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
	r = runAccess(t, a, nil, "users", "list", "--json")
	var got struct{ Users []map[string]any } // the API's object
	if json.Unmarshal([]byte(r.stdout), &got) != nil || len(got.Users) != 2 || got.Users[0]["extra"] != 1.0 {
		t.Errorf("--json is not the API's object with every field: %q", r.stdout)
	}
}

const groupsAnswer = `{"groups":[{"name":"pay","description":"payments","grants":[{"tags":{"team":"a"},"permissions":["read","wake"]},{"tags":{},"permissions":["read"]}],"updatedAt":"2026-10-19T09:00:00Z","updatedBy":"arn:aws:sts::1:assumed-role/Ops/ana"}]}`

func TestGroupsListAndShow(t *testing.T) {
	a := &accessAPI{routes: map[string]string{"GET /iam/groups": groupsAnswer}}
	r := runAccess(t, a, nil, "groups", "list")
	if r.exit != 0 || !strings.Contains(r.stdout, "pay") || !strings.Contains(r.stdout, "payments") || !strings.Contains(r.stdout, "3h") {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
	r = runAccess(t, a, nil, "groups", "list", "--json")
	var got struct{ Groups []map[string]any } // the API's object
	if json.Unmarshal([]byte(r.stdout), &got) != nil || len(got.Groups) != 1 || got.Groups[0]["updatedBy"] == nil {
		t.Errorf("groups list --json: %q", r.stdout)
	}
	r = runAccess(t, a, nil, "groups", "show", "pay")
	for _, want := range []string{"2026-10-19 09:00 UTC by Ops/ana", "team=a: read,wake", "every workload: read"} {
		if r.exit != 0 || !strings.Contains(r.stdout, want) {
			t.Errorf("show lacks %q: exit %d, %q", want, r.exit, r.stdout)
		}
	}
	if r = runAccess(t, a, nil, "groups", "show", "pay", "--json"); !strings.Contains(r.stdout, `"updatedBy"`) {
		t.Errorf("stdout %q", r.stdout)
	}
	if r = runAccess(t, a, nil, "groups", "show", "nope"); r.exit != 1 || r.stderr != "no group nope\n" {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestGroupsCreateBody(t *testing.T) {
	a := &accessAPI{routes: map[string]string{"POST /iam/groups": `{"group":{"name":"pay"}}`}}
	r := runAccess(t, a, nil, "groups", "create", "pay", "--description", "payments", "--grant", "team=a,env=dev:read,wake", "--grant", ":read")
	want := `POST /iam/groups {"description":"payments","grants":[{"tags":{"env":"dev","team":"a"},"permissions":["read","wake"]},{"tags":{},"permissions":["read"]}],"name":"pay"}`
	if r.exit != 0 || len(a.requests) != 1 || a.requests[0] != want || r.stdout != "created group pay\n" {
		t.Errorf("exit %d, requests %q, stdout %q, stderr %q", r.exit, a.requests, r.stdout, r.stderr)
	}
}

func TestGroupsUpdateBodies(t *testing.T) {
	for args, want := range map[string]string{
		"--description|new":           `{"description":"new"}`,
		"--description|":              `{"description":""}`,
		"--no-grants":                 `{"grants":[]}`,
		"--grant|team=a:nap":          `{"grants":[{"tags":{"team":"a"},"permissions":["nap"]}]}`,
		"--no-grants|--description|x": `{"description":"x","grants":[]}`,
	} {
		a := &accessAPI{routes: map[string]string{"PUT /iam/groups/my-grp": `{"group":{}}`}}
		r := runAccess(t, a, nil, append([]string{"groups", "update", "my-grp"}, strings.Split(args, "|")...)...)
		if r.exit != 0 || len(a.requests) != 1 || a.requests[0] != "PUT /iam/groups/my-grp "+want || r.stdout != "updated group my-grp\n" {
			t.Errorf("%s: exit %d, requests %q, stdout %q, stderr %q", args, r.exit, a.requests, r.stdout, r.stderr)
		}
	}
}

func TestGroupsDeleteEscapesTheName(t *testing.T) {
	a := &accessAPI{routes: map[string]string{"DELETE /iam/groups/a/b": `{"name":"a/b"}`}}
	r := runAccess(t, a, nil, "groups", "delete", "a/b", "--yes")
	if r.exit != 0 || len(a.requests) != 1 || a.requests[0] != "DELETE /iam/groups/a%2Fb " || r.stdout != "deleted group a/b\n" {
		t.Errorf("exit %d, requests %q, stdout %q, stderr %q", r.exit, a.requests, r.stdout, r.stderr)
	}
}
