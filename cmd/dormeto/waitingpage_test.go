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

// fixture is an answer of the manager's API as it sends it (testdata/waitingpage).
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../../testdata/waitingpage/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// wpAPI is a fake manager's waiting-page routes at the three levels.
type wpAPI struct {
	mu       sync.Mutex
	writes   []string // "METHOD path body"
	reads    []string
	view     string            // what every route answers; the level's fixture by default
	fixtures map[string]string // the default answer of each route, by path
	refuse   int               // the status the writes answer with, with refusal as the body
	body     string
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
	io.WriteString(w, orString(a.view, a.fixtures[r.URL.Path]))
}

func runWP(t *testing.T, a *wpAPI, stdin string, args ...string) fleetRan {
	t.Helper()
	if a.fixtures == nil {
		a.fixtures = map[string]string{
			"/iam/workloads/prod-eu/w-1/waiting-page": fixture(t, "wl-template"),
			"/iam/routers/prod-eu/waiting-page":       fixture(t, "rt-template"),
			"/iam/settings/waiting-page":              fixture(t, "org-template-put"),
		}
	}
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
		for _, want := range []string{tg.label + ": waiting page, template", "icon", "set on ", "arn:aws:iam::121014831971:user/cli"} {
			if !strings.Contains(r.stdout, want) {
				t.Errorf("%s: stdout lacks %q:\n%s", tg.name, want, r.stdout)
			}
		}
	}
}

func TestWaitingPageShowOfTheOrganizationsRealAnswers(t *testing.T) {
	for name, c := range map[string]struct{ fixture, want string }{
		"none set":   {"org-none", "the organization: no waiting page set\nicon  auto (from built-in default)\n"},
		"unreadable": {"org-unreadable", "the organization: the waiting page is not known: settings.json is unreadable\n"},
		"template":   {"org-template", "set on the organization by arn:aws:iam::121014831971:user/cli, 2027-01-15 08:00 UTC\n"},
	} {
		r := runWP(t, &wpAPI{view: fixture(t, c.fixture)}, "", "settings", "waiting-page", "show")
		if r.exit != 0 || !strings.Contains(r.stdout, c.want) {
			t.Errorf("%s: exit %d, stdout %q", name, r.exit, r.stdout)
		}
	}
}

func TestWaitingPageShowPrintsNeitherTheIconNorThePageButJSONDoes(t *testing.T) {
	for _, c := range []struct {
		fixture string
		args    []string
		secret  string
	}{
		{"wl-custom", wpArgs(0, "show"), "SECRET-HTML"},
		{"org-custom", wpArgs(2, "show"), "SECRET-HTML"},
		{"wl-template", wpArgs(0, "show"), "data:image/png"},
		{"org-template", wpArgs(2, "show"), "data:image/png"},
	} {
		r := runWP(t, &wpAPI{view: fixture(t, c.fixture)}, "", c.args...)
		if r.exit != 0 || strings.Contains(r.stdout, c.secret) || strings.Contains(r.stdout, "iVBOR") || strings.Contains(r.stdout, "<p>") {
			t.Errorf("%s: exit %d, stdout %q", c.fixture, r.exit, r.stdout)
		}
		j := runWP(t, &wpAPI{view: fixture(t, c.fixture)}, "", append(c.args, "--json")...)
		if !strings.Contains(j.stdout, c.secret) || !strings.Contains(j.stdout, `"warnings"`) {
			t.Errorf("%s --json: %q", c.fixture, j.stdout)
		}
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
		if note := strings.Contains(r.stdout, "routers apply it within about a minute\n"); note != (i == 2) {
			t.Errorf("%s: the organization's note is printed there alone: %q", tg.name, r.stdout)
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
		a := &wpAPI{}
		if i == 2 {
			a.view = fixture(t, "org-clear")
		}
		r := runWP(t, a, "", wpArgs(i, "clear")...)
		want := tg.label + ": waiting page cleared\n"
		if i == 2 {
			want += "routers apply it within about a minute\n"
		}
		if r.exit != 0 || !reflect.DeepEqual(a.writes, []string{"DELETE " + tg.path + " "}) || r.stdout != want {
			t.Errorf("%s: exit %d, writes %q, stdout %q, stderr %q", tg.name, r.exit, a.writes, r.stdout, r.stderr)
		}
	}
}

func TestWaitingPageWarningsAreAnswered(t *testing.T) {
	a := &wpAPI{view: fixture(t, "wl-custom")}
	r := runWP(t, a, "<p>x</p>", wpArgs(0, "set", "--html", "-")...)
	want := "prod-eu/w-1: waiting page set (custom)\nwarning: the page has no element data-dormeto=\"status\": the visitor will not read why it waits\n"
	if r.exit != 0 || r.stdout != want {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
}

func TestWaitingPageRefusalsArePrinted(t *testing.T) {
	for name, c := range map[string]struct {
		status  int
		body    string
		wantErr string
		not     string
	}{
		"403":     {403, fixture(t, "err-403"), "admin role required: a custom waiting page runs scripts on the application's own address", ""},
		"409":     {409, `{"error":"conflict","message":"upgrade the router to 0.35.0"}`, "upgrade the router to 0.35.0", ""},
		"409 tf":  {409, fixture(t, "err-409-tf"), "set waiting_page in its workload module", ""},
		"400":     {400, fixture(t, "err-400"), "prod-eu/w-1: icon must be auto, none, or a data:image/png, jpeg, webp, svg+xml or x-icon base64 URI of at most 64 KiB\ntitle must be at most 80 characters\n", "; "},
		"gateway": {403, `{"message":"Forbidden"}`, "execute-api:Invoke", ""},
	} {
		a := &wpAPI{refuse: c.status, body: c.body}
		r := runWP(t, a, "", wpArgs(0, "set", "--title", "T")...)
		if r.exit != 1 || r.stdout != "" || !strings.Contains(r.stderr, c.wantErr) || !strings.Contains(r.stderr, "prod-eu/w-1") || (c.not != "" && strings.Contains(r.stderr, c.not)) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, r.exit, r.stdout, r.stderr)
		}
	}
}

func TestWaitingPageIconFromInputIsAUsageError(t *testing.T) {
	r := runWP(t, &wpAPI{}, "\x89PNG", wpArgs(0, "set", "--icon", "-")...)
	if r.exit != 2 || len(r.api.requests) != 0 || !strings.Contains(r.stderr, "--icon") || strings.Contains(r.stderr, "panic") {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestWaitingPageHTMLKeepsItsByteOrderMark(t *testing.T) {
	a := &wpAPI{}
	runWP(t, a, "\ufeff<p>x</p>", wpArgs(0, "set", "--html", "-")...)
	if len(a.writes) != 1 || !strings.Contains(a.writes[0], "\ufeff\\u003cp") {
		t.Errorf("writes %q", a.writes)
	}
}

func TestWaitingPageSetJSONPrintsTheAnswer(t *testing.T) {
	r := runWP(t, &wpAPI{}, "", wpArgs(2, "set", "--title", "T", "--json")...)
	if r.exit != 0 || !strings.HasPrefix(r.stdout, "{") || !strings.Contains(r.stdout, `"note"`) {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
}
