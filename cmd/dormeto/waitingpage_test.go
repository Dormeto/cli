package main

// `workloads|routers|settings waiting-page show|set|clear` against the fake manager API: the three levels' routes, the
// icon and the page checked before any call (exit 2), the warnings and the refusals printed.

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

var wpPNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}

const wpViewJSON = `{"own":{"mode":"template","title":"Acme portal","icon":"none"},"inherited":{"mode":"template","icon":"auto","from":{"mode":"default","icon":"default"}},` +
	`"effective":{"mode":"template","icon":"none","title":"Acme portal","from":{"mode":"workload","icon":"workload","title":"workload"}},` +
	`"from":{"mode":"workload","icon":"workload","title":"workload"},"favicon":null,"warnings":[],"terraform":false,"by":"ann@x.io","at":"2026-10-19T10:00:00.000Z"}`

// wpAPI is a fake manager's waiting-page routes at the three levels.
type wpAPI struct {
	mu     sync.Mutex
	writes []string // "METHOD path body"
	reads  []string
	view   string // what every route answers; wpViewJSON by default
	refuse int    // the status the writes answer with, with refusal as the body
	body   string
}

func (a *wpAPI) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	data, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	defer a.mu.Unlock()
	switch r.URL.Path {
	case "/iam/workloads/prod-eu/w-1/waiting-page", "/iam/routers/prod-eu/waiting-page", "/iam/settings/waiting-page":
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"error":"not_found","message":"workload not found"}`)
		return
	}
	if r.Method == http.MethodGet {
		a.reads = append(a.reads, r.URL.Path)
	} else {
		a.writes = append(a.writes, r.Method+" "+r.URL.Path+" "+string(data))
		if a.refuse != 0 {
			w.WriteHeader(a.refuse)
			io.WriteString(w, a.body)
			return
		}
	}
	io.WriteString(w, orString(a.view, wpViewJSON))
}

func runWP(t *testing.T, a *wpAPI, stdin string, args ...string) fleetRan {
	t.Helper()
	return runFleetWith(t, &fakeAPI{custom: a.handler, stdin: stdin}, "eu-west-1", nil, args...)
}

var wpTargets = []struct {
	name   string
	prefix []string // the command up to waiting-page's verb
	args   []string // the target
	path   string
	label  string
}{
	{"workload", []string{"workloads", "waiting-page"}, []string{"prod-eu/w-1"}, "/iam/workloads/prod-eu/w-1/waiting-page", "prod-eu/w-1"},
	{"router", []string{"routers", "waiting-page"}, []string{"prod-eu"}, "/iam/routers/prod-eu/waiting-page", "prod-eu"},
	{"organization", []string{"settings", "waiting-page"}, nil, "/iam/settings/waiting-page", "the organization"},
}

func wpArgs(tg int, verb string, more ...string) []string {
	t := wpTargets[tg]
	return append(append(append(append([]string{}, t.prefix...), verb), t.args...), more...)
}

func TestWaitingPageShowAtTheThreeLevels(t *testing.T) {
	for i, tg := range wpTargets {
		a := &wpAPI{}
		r := runWP(t, a, "", wpArgs(i, "show")...)
		if r.exit != 0 {
			t.Fatalf("%s: exit %d: %s", tg.name, r.exit, r.stderr)
		}
		if !reflect.DeepEqual(a.reads, []string{tg.path}) {
			t.Errorf("%s: reads %q", tg.name, a.reads)
		}
		for _, want := range []string{tg.label + ": waiting page, template", "Acme portal (from the workload)", "set on "} {
			if !strings.Contains(r.stdout, want) {
				t.Errorf("%s: stdout lacks %q:\n%s", tg.name, want, r.stdout)
			}
		}
	}
}

func TestWaitingPageShowPrintsNeitherTheIconNorThePageButJSONDoes(t *testing.T) {
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(wpPNG)
	view := `{"own":{"mode":"template","icon":"` + uri + `"},"effective":{"mode":"template","icon":"` + uri + `","from":{"mode":"workload","icon":"workload"}},"warnings":[]}`
	r := runWP(t, &wpAPI{view: view}, "", wpArgs(0, "show")...)
	if !strings.Contains(r.stdout, "PNG, 11 bytes") || strings.Contains(r.stdout, "base64") {
		t.Errorf("stdout %q", r.stdout)
	}
	r = runWP(t, &wpAPI{view: view}, "", wpArgs(0, "show", "--json")...)
	if !strings.Contains(r.stdout, uri) || !strings.Contains(r.stdout, `"warnings"`) {
		t.Errorf("--json stdout %q", r.stdout)
	}
}

func TestWaitingPageSetATemplate(t *testing.T) {
	for i, tg := range wpTargets {
		a := &wpAPI{}
		r := runWP(t, a, "", wpArgs(i, "set", "--title", "Acme", "--description", "Sleeps at night.\nBack at 8.", "--icon", "none")...)
		if r.exit != 0 {
			t.Fatalf("%s: exit %d: %s", tg.name, r.exit, r.stderr)
		}
		want := []string{`PUT ` + tg.path + ` {"page":{"mode":"template","icon":"none","title":"Acme","description":"Sleeps at night.\nBack at 8."}}`}
		if !reflect.DeepEqual(a.writes, want) {
			t.Errorf("%s: writes %q", tg.name, a.writes)
		}
		if !strings.HasPrefix(r.stdout, tg.label+": waiting page set (template)\n") {
			t.Errorf("%s: stdout %q", tg.name, r.stdout)
		}
	}
	// Only what is given is written: the document replaces the level's.
	a := &wpAPI{}
	runWP(t, a, "", wpArgs(0, "set", "--icon", "auto")...)
	if want := `PUT /iam/workloads/prod-eu/w-1/waiting-page {"page":{"mode":"template","icon":"auto"}}`; len(a.writes) != 1 || a.writes[0] != want {
		t.Errorf("writes %q", a.writes)
	}
}

func TestWaitingPageSetAnIconFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "icon.png")
	os.WriteFile(path, wpPNG, 0o600)
	a := &wpAPI{}
	r := runWP(t, a, "", wpArgs(0, "set", "--icon", path)...)
	want := `PUT /iam/workloads/prod-eu/w-1/waiting-page {"page":{"mode":"template","icon":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(wpPNG) + `"}}`
	if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != want {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestWaitingPageSetACustomPageFromAFileAndFromInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page.html")
	os.WriteFile(path, []byte("<!doctype html><p data-dormeto=\"status\">é</p>\n"), 0o600)
	want := `PUT /iam/workloads/prod-eu/w-1/waiting-page {"page":{"mode":"custom","html":"\u003c!doctype html\u003e\u003cp data-dormeto=\"status\"\u003eé\u003c/p\u003e\n"}}` // JSON as Go writes it: < and > escaped
	for _, c := range []struct{ arg, stdin string }{{path, ""}, {"-", "<!doctype html><p data-dormeto=\"status\">é</p>\n"}} {
		a := &wpAPI{}
		r := runWP(t, a, c.stdin, wpArgs(0, "set", "--html", c.arg)...)
		if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != want {
			t.Errorf("--html %s: exit %d, writes %q, stderr %q", c.arg, r.exit, a.writes, r.stderr)
		}
		if !strings.HasPrefix(r.stdout, "prod-eu/w-1: waiting page set (custom)\n") {
			t.Errorf("stdout %q", r.stdout)
		}
	}
}

func TestWaitingPageSetRefusedBeforeAnyCall(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o600)
		return p
	}
	gif := write("a.gif", []byte("GIF89a"))
	big := write("big.png", append(append([]byte{}, wpPNG...), make([]byte, 64<<10)...))
	empty := write("empty.png", nil)
	blank := write("blank.html", []byte(" \n"))
	huge := write("huge.html", bytes.Repeat([]byte("a"), 256<<10+1))
	latin1 := write("latin1.html", []byte{'<', 'p', '>', 0xe9})
	png := write("ok.png", wpPNG)
	html := write("ok.html", []byte("<p>x</p>"))
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"html with icon":        {[]string{"--html", html, "--icon", "auto"}, "--html"},
		"html with title":       {[]string{"--html", html, "--title", "T"}, "--html"},
		"html with description": {[]string{"--html", html, "--description", "D"}, "--html"},
		"nothing":               {nil, "--title"},
		"icon not a file":       {[]string{"--icon", filepath.Join(dir, "missing.png")}, "--icon"},
		"icon not an image":     {[]string{"--icon", gif}, "PNG, JPEG, WebP, SVG or ICO"},
		"icon too large":        {[]string{"--icon", big}, "64 KiB"},
		"icon empty":            {[]string{"--icon", empty}, "empty"},
		"icon given empty":      {[]string{"--icon", ""}, "--icon"},
		"html not a file":       {[]string{"--html", filepath.Join(dir, "missing.html")}, "--html"},
		"html blank":            {[]string{"--html", blank}, "blank"},
		"html too large":        {[]string{"--html", huge}, "256 KiB"},
		"html not UTF-8":        {[]string{"--html", latin1}, "UTF-8"},
		"html empty input":      {[]string{"--html", "-"}, "blank"},
		"html empty name":       {[]string{"--html", ""}, "--html"},
		"icon and png ok but title empty and html": {[]string{"--icon", png, "--html", html}, "--html"},
	} {
		for i := range wpTargets {
			a := &wpAPI{}
			r := runWP(t, a, "", wpArgs(i, "set", c.args...)...)
			if r.exit != 2 || len(a.writes) != 0 || len(a.reads) != 0 || len(r.api.requests) != 0 {
				t.Errorf("%s at %s: exit %d, %d requests", name, wpTargets[i].name, r.exit, len(r.api.requests))
			}
			if !strings.Contains(r.stderr, c.want) {
				t.Errorf("%s at %s: stderr %q lacks %q", name, wpTargets[i].name, r.stderr, c.want)
			}
		}
	}
}

func TestWaitingPageWrongTargets(t *testing.T) {
	for _, args := range [][]string{
		{"workloads", "waiting-page", "show"}, {"workloads", "waiting-page", "show", "prod-eu"}, {"workloads", "waiting-page", "show", "a/b", "c/d"},
		{"routers", "waiting-page", "show"}, {"routers", "waiting-page", "show", "a", "b"},
		{"settings", "waiting-page", "show", "prod-eu"}, {"settings", "waiting-page", "clear", "prod-eu"},
		{"workloads", "waiting-page", "clear"}, {"workloads", "waiting-page", "set", "--title", "T"},
	} {
		r := runWP(t, &wpAPI{}, "", args...)
		if r.exit != 2 || len(r.api.requests) != 0 {
			t.Errorf("%v: exit %d, %d requests, stderr %q", args, r.exit, len(r.api.requests), r.stderr)
		}
	}
}

func TestWaitingPageClear(t *testing.T) {
	for i, tg := range wpTargets {
		a := &wpAPI{view: `{"own":null,"effective":{"mode":"template","icon":"auto","from":{"mode":"default","icon":"default"}},"warnings":[]}`}
		r := runWP(t, a, "", wpArgs(i, "clear")...)
		if r.exit != 0 || !reflect.DeepEqual(a.writes, []string{"DELETE " + tg.path + " "}) || r.stdout != tg.label+": waiting page cleared\n" {
			t.Errorf("%s: exit %d, writes %q, stdout %q, stderr %q", tg.name, r.exit, a.writes, r.stdout, r.stderr)
		}
	}
}

func TestWaitingPageWarningsAreAnswered(t *testing.T) {
	a := &wpAPI{view: `{"own":{"mode":"custom","html":"<p>"},"warnings":["the page has no element with data-dormeto=\"status\"","it has a Content-Security-Policy meta tag"]}`}
	r := runWP(t, a, "<p>x</p>", wpArgs(0, "set", "--html", "-")...)
	want := "prod-eu/w-1: waiting page set (custom)\nwarning: the page has no element with data-dormeto=\"status\"\nwarning: it has a Content-Security-Policy meta tag\n"
	if r.exit != 0 || r.stdout != want {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
}

func TestWaitingPageRefusalsArePrinted(t *testing.T) {
	for name, c := range map[string]struct {
		status        int
		body, wantErr string
	}{
		"403":                   {403, `{"error":"forbidden","message":"a custom waiting page needs an administrator"}`, "a custom waiting page needs an administrator"},
		"409":                   {409, `{"error":"conflict","message":"upgrade the router to 0.35.0"}`, "upgrade the router to 0.35.0"},
		"409 without a message": {409, `{"error":"conflict"}`, "upgrade the router to 0.35.0"},
		"400": {400, `{"error":"bad_request","message":"the page is not valid","errors":["title must be at most 80 characters","icon is not a png image"]}`,
			"title must be at most 80 characters\n"},
	} {
		a := &wpAPI{refuse: c.status, body: c.body}
		r := runWP(t, a, "", wpArgs(0, "set", "--title", "T")...)
		if r.exit != 1 || r.stdout != "" || !strings.Contains(r.stderr, c.wantErr) || !strings.Contains(r.stderr, "prod-eu/w-1") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, r.exit, r.stdout, r.stderr)
		}
	}
	a := &wpAPI{refuse: 403, body: `{"error":"forbidden"}`}
	if r := runWP(t, a, "", wpArgs(2, "clear")...); !strings.Contains(r.stderr, "needs an administrator") {
		t.Errorf("a 403 without a message: %q", r.stderr)
	}
}

func TestWaitingPageSetJSONPrintsTheAnswer(t *testing.T) {
	r := runWP(t, &wpAPI{}, "", wpArgs(0, "set", "--title", "T", "--json")...)
	if r.exit != 0 || !strings.HasPrefix(r.stdout, "{") || !strings.Contains(r.stdout, `"effective"`) {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
}
