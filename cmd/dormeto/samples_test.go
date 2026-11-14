package main

// The samples (the CLI pages design §4): for every runnable command of `dormeto aws` and `dormeto gcp`, one command line a
// reader could type and what it prints, as a table and with --json, kept in docs/samples/<path with hyphens>.json, which
// the website shows. Each line runs here against the fake manager (testdata/samples/manager.json, the manager's own
// answer shapes) and the fake Google (internal/gcp/gcptest, a router installed), with the design's fictional fleet and a
// fixed clock. A file that differs from what its command prints now fails the test; rewrite them with
// go test ./cmd/dormeto -run Samples -update. The install's and the estimate's samples run in their own packages
// (internal/install, internal/cli): they need those packages' fake AWS.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/spf13/cobra"

	cli "github.com/Dormeto/cli"
	"github.com/Dormeto/cli/internal/awscheck"
	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/gcp/gcptest"
	"github.com/Dormeto/cli/internal/gcponboard"
)

var updateSamples = flag.Bool("update", false, "rewrite docs/samples from what the commands print")

// sampleFile is a file of docs/samples: the command's path, the line a reader types, what it prints on stdout without and
// with --json (null: the command has no --json), and, for a write, what it says on stderr: its words under --json, else
// what it said there without it (left out when nothing).
type sampleFile struct {
	Command string  `json:"command"`
	Line    string  `json:"line"`
	Table   string  `json:"table"`
	JSON    *string `json:"json"`
	Stderr  string  `json:"stderr,omitempty"`
}

// sample is a line to run: what the fake Google holds beyond the installed router, the files the line names (in its
// working directory) and whether stderr is kept (what a write or a dry run says there). The manager's answers are in
// testdata/samples/manager.json.
type sample struct {
	line   string
	google func(t *testing.T, g *gcptest.Server, d gcponboard.Deps)
	files  map[string]string
	stderr bool
}

// The fictional fleet's fixed points.
var (
	sampleNow     = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	sampleAPI     = "https://abc123.execute-api.eu-west-1.amazonaws.com"
	samplesDir    = absolute(filepath.Join("..", "..", "docs", "samples")) // the lines run in directories of their own
	sampleVersion = `{"manager": "0.24.0", "router": "0.40.0"}`
)

func absolute(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		panic(err)
	}
	return abs
}

// sampleFileName is a command's file: its path with hyphens, aws-routers-waiting-page-set.json.
func sampleFileName(command string) string { return strings.ReplaceAll(command, " ", "-") + ".json" }

// externalSamples are the commands whose samples another package's test writes, with its fakes: the install's
// (internal/install) and the estimate's (internal/cli).
var externalSamples = []string{"aws install", "aws install apply", "aws install check", "aws install status", "aws uninstall",
	"aws uninstall apply", "aws estimate"}

// runnableCommands are the paths of the runnable commands under aws and gcp, the groups themselves left out.
func runnableCommands(t *testing.T) []string {
	t.Helper()
	root := newRoot(build{})
	var out []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" {
			return
		}
		path := strings.TrimPrefix(c.CommandPath(), "dormeto ")
		if c.Runnable() && strings.Contains(path, " ") && (strings.HasPrefix(path, "aws ") || strings.HasPrefix(path, "gcp ")) {
			out = append(out, path)
		}
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(root)
	slices.Sort(out)
	return out
}

// splitLine splits a sample's line as a shell does for these lines: words, and quoted words ("…" or '…').
func splitLine(line string) []string {
	var out []string
	var cur strings.Builder
	in, quote := false, rune(0)
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote, in = r, true
		case r == ' ':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

// commandOf is the path of the command a line runs: its words until the first argument or flag.
func commandOf(t *testing.T, args []string) (string, *cobra.Command) {
	t.Helper()
	c, _, err := newRoot(build{}).Find(args)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return strings.TrimPrefix(c.CommandPath(), "dormeto "), c
}

// Every runnable command has its sample file, and every file is a command's.
func TestSamplesEveryCommandHasItsFile(t *testing.T) {
	commands := runnableCommands(t)
	if len(commands) < 120 {
		t.Fatalf("only %d commands found", len(commands))
	}
	want := map[string]bool{}
	for _, c := range commands {
		want[sampleFileName(c)] = true
		if _, err := os.Stat(filepath.Join(samplesDir, sampleFileName(c))); err != nil {
			t.Errorf("%s has no sample: docs/samples/%s (go test ./cmd/dormeto -run Samples -update)", c, sampleFileName(c))
		}
	}
	entries, err := os.ReadDir(samplesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !want[e.Name()] {
			t.Errorf("docs/samples/%s is no command's", e.Name())
		}
	}
	// Each command is sampled here or by its package's test, once.
	sampled := map[string]int{}
	for _, s := range samples {
		c, _ := commandOf(t, splitLine(s.line)[1:])
		sampled[c]++
	}
	for _, c := range externalSamples {
		sampled[c]++
	}
	for _, c := range commands {
		if sampled[c] != 1 {
			t.Errorf("%s is sampled %d times", c, sampled[c])
		}
	}
	for c := range sampled {
		if !slices.Contains(commands, c) {
			t.Errorf("%s is sampled but is no command", c)
		}
	}
}

// realIDs are what a sample must not hold: the real accounts, project and names, an address outside example.com, a local
// address or port.
var realIDs = []*regexp.Regexp{
	regexp.MustCompile(`121014831971|038606077307|dormeto-dev|(?i)kloudy`),
	regexp.MustCompile(`[A-Za-z0-9._%+-]+@(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,}`), // checked below: example.com only
	regexp.MustCompile(`127\.0\.0\.1|localhost|:[0-9]{2,5}/`),
}

// No sample holds a real id: no real account, project or address, no local URL.
func TestSamplesHoldNoRealID(t *testing.T) {
	entries, err := os.ReadDir(samplesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(samplesDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bad := realIDIn(string(b)); bad != "" {
			t.Errorf("docs/samples/%s holds %q", e.Name(), bad)
		}
	}
}

// realIDIn is the first real id in text, "" when none.
func realIDIn(text string) string {
	if m := realIDs[0].FindString(text); m != "" {
		return m
	}
	for _, m := range realIDs[1].FindAllString(text, -1) {
		if !strings.HasSuffix(m, "@example.com") && !strings.HasSuffix(m, ".iam.gserviceaccount.com") && !strings.HasSuffix(m, ".gserviceaccount.com") {
			return m
		}
	}
	return realIDs[2].FindString(text)
}

func TestSamplesRealIDPattern(t *testing.T) {
	for text, want := range map[string]bool{
		"account 121014831971": true, "project dormeto-dev": true, "profile kloudy-dormeto-prd": true, "me@dormeto.com": true,
		"http://127.0.0.1:52341/iam": true, "ops@example.com": false, "dmt-prod-discovery@my-project.iam.gserviceaccount.com": false,
		"https://abc123.execute-api.eu-west-1.amazonaws.com/iam": false, "account 111122223333": false,
	} {
		if got := realIDIn(text) != ""; got != want {
			t.Errorf("%q: real %v, want %v", text, got, want)
		}
	}
}

// Each sample's file is what its line prints now.
func TestSamples(t *testing.T) {
	// The fixed clock's time zone, and the router version the CLI embeds, whatever the release.
	local, versions, gcpClock := time.Local, cli.Versions, gcpNow
	time.Local, cli.Versions, gcpNow = time.UTC, []byte(sampleVersion), func() time.Time { return sampleNow }
	t.Cleanup(func() { time.Local, cli.Versions, gcpNow = local, versions, gcpClock })
	answers := sampleAnswers(t)
	if *updateSamples {
		if err := os.MkdirAll(samplesDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range samples {
		args := splitLine(s.line)
		if len(args) < 3 || args[0] != "dormeto" {
			t.Fatalf("%q is no dormeto line", s.line)
		}
		command, c := commandOf(t, args[1:])
		t.Run(command, func(t *testing.T) {
			got := sampleFile{Command: command, Line: s.line}
			stdout, stderr, exit, missing := runSample(t, s, command, answers, args[1:])
			if exit != 0 || len(missing) > 0 {
				t.Fatalf("exit %d, routes the fake manager lacks %v\nstdout:\n%s\nstderr:\n%s", exit, missing, stdout, stderr)
			}
			got.Table = stdout
			if s.stderr {
				got.Stderr = stderr
			}
			if c.Flags().Lookup("json") != nil {
				out, errs, exit, missing := runSample(t, s, command, answers, append(slices.Clone(args[1:]), "--json"))
				if exit != 0 || len(missing) > 0 {
					t.Fatalf("--json: exit %d, routes the fake manager lacks %v\nstdout:\n%s\nstderr:\n%s", exit, missing, out, errs)
				}
				if !json.Valid([]byte(out)) {
					t.Errorf("--json printed no JSON:\n%s", out)
				}
				got.JSON = &out
				if s.stderr && errs != "" {
					got.Stderr = errs
				}
			}
			if bad := realIDIn(got.Table + deref(got.JSON) + got.Stderr); bad != "" {
				t.Errorf("the output holds %q, a real or local id", bad)
			}
			b := sampleBytes(t, got)
			path := filepath.Join(samplesDir, sampleFileName(command))
			if *updateSamples {
				if err := os.WriteFile(path, b, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			was, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(was, b) {
				t.Errorf("docs/samples/%s differs from what %q prints now; update it: go test ./cmd/dormeto -run Samples -update\nnow:\n%s", sampleFileName(command), s.line, b)
			}
		})
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// sampleBytes is a sample file's text: indented JSON, HTML left as it is, a final newline.
func sampleBytes(t *testing.T, f sampleFile) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// sampleAnswers are testdata/samples/manager.json: the fake manager's answers by route.
func sampleAnswers(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "samples", "manager.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("testdata/samples/manager.json: %v", err)
	}
	return out
}

// sampleManager is the fake manager of the samples: each request answered by its route's answer, by "<METHOD> <path>"
// (with "?<query>" first when it has one), a line's own first: "<command> | <METHOD> <path>".
// An answer {"status": n, "body": …} answers n, {"sequence": […]} each of its answers in turn (the last one repeated, for
// what a command follows); any other is the body of a 200. A route without an answer is recorded.
type sampleManager struct {
	mu      sync.Mutex
	answers map[string]json.RawMessage
	command string
	reads   map[string]int
	missing []string
}

func (m *sampleManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := []string{r.Method + " " + r.URL.Path}
	if r.URL.RawQuery != "" {
		q, _ := url.QueryUnescape(r.URL.RawQuery)
		keys = []string{r.Method + " " + r.URL.Path + "?" + q, keys[0]}
	}
	w.Header().Set("Content-Type", "application/json")
	for _, k := range append([]string{m.command + " | " + keys[0], m.command + " | " + keys[len(keys)-1]}, keys...) {
		answer, ok := m.answers[k]
		if !ok {
			continue
		}
		var form struct {
			Status   int               `json:"status"`
			Body     json.RawMessage   `json:"body"`
			Sequence []json.RawMessage `json:"sequence"`
		}
		_ = json.Unmarshal(answer, &form)
		if len(form.Sequence) > 0 { // each read the next answer, the last one repeated
			answer = form.Sequence[min(m.reads[k], len(form.Sequence)-1)]
			m.reads[k]++
			form.Status, form.Body = 0, nil
			_ = json.Unmarshal(answer, &form)
		}
		if form.Status != 0 && len(form.Body) > 0 {
			w.WriteHeader(form.Status)
			w.Write(form.Body)
			return
		}
		w.Write(answer)
		return
	}
	body, _ := io.ReadAll(r.Body)
	m.missing = append(m.missing, strings.TrimSpace(keys[0]+" "+string(body)))
	w.WriteHeader(404)
	io.WriteString(w, `{"error":"not_found","message":"no such route"}`)
}

// runSample runs a line's arguments on the whole tree, built with the fakes: the manager's API found at sampleAPI and
// answered by the fake, the fake Google with the router installed, files in a working directory of their own.
func runSample(t *testing.T, s sample, command string, answers map[string]json.RawMessage, args []string) (stdout, stderr string, exit int, missing []string) {
	t.Helper()
	dir := t.TempDir()
	for name, text := range s.files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	m := &sampleManager{answers: answers, command: command, reads: map[string]int{}}
	srv := httptest.NewServer(m)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	deps := fleetDeps{
		load: func(context.Context, string) (awssdk.Config, error) {
			return awssdk.Config{Region: "eu-west-1", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", "")}, nil
		},
		locator: func(awssdk.Config) fleet.Locator {
			return locatorFunc(func(context.Context, string, string) (string, error) { return sampleAPI, nil })
		},
		http:     &http.Client{Transport: toServer{u}},
		now:      func() time.Time { return sampleNow },
		terminal: func(io.Reader) bool { return false },
		sleep:    func(context.Context, time.Duration) error { return nil },
		timeout:  context.WithTimeout,
		confirm: func(context.Context, io.Reader, io.Writer, string) (bool, error) {
			t.Error("asked for a confirmation: give --yes in the line")
			return false, nil
		},
		checkClients: func(awssdk.Config) (awscheck.IAM, awscheck.STS) {
			return checkIAM{allow: allowEverything}, checkSTS{}
		},
	}
	g := sampleGoogle(t, s)
	root := buildRoot(build{version: "0.14.0", versions: []byte(sampleVersion)}, deps, installRuns{}, sampleGcpRuns(g))
	var o, e bytes.Buffer
	root.SetOut(&o)
	root.SetErr(&e)
	root.SetIn(strings.NewReader(""))
	exit = execute(root, args...)
	m.mu.Lock()
	missing = m.missing
	m.mu.Unlock()
	return o.String(), e.String(), exit, missing
}

// The fake Google's fictional project and router.
const (
	sampleProject = "my-project"
	sampleNumber  = "123456789012"
	sampleRegion  = "europe-west1"
	sampleAccount = "ops@example.com"
)

// sampleGoogle is the fake Google of a line: my-project, and for a line of a router's or its workloads' commands, the
// router of Dormeto's public image 0.40.0 installed in europe-west1 (by the install itself), its workloads shop and blog,
// a backend service checkout to onboard and a GKE cluster; then what the sample adds.
func sampleGoogle(t *testing.T, s sample) *gcptest.Server {
	t.Helper()
	g := gcptest.New(t)
	g.ProjectID, g.ProjectNumber, g.Parent = sampleProject, sampleNumber, "organizations/42"
	g.Policy = map[string]any{"version": 1, "etag": "BwAAAA==", "bindings": []any{
		map[string]any{"role": "roles/owner", "members": []any{"user:" + sampleAccount}}}}
	image := "europe-docker.pkg.dev/dormeto-releases/router/router:0.40.0"
	g.Images[image] = true
	g.Digests[image] = "sha256:5d2c8a1b9e4f7a3c6b0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b"
	g.SharedNames = true
	kube := newSampleKube(t)
	g.Route("container.googleapis.com", "/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/"+sampleProject+"/locations/europe-west1-b/clusters/shop-cluster" {
			gcptest.Fail(w, 404, "NOT_FOUND", "cluster not found")
			return
		}
		gcptest.Reply(w, map[string]any{"name": "shop-cluster", "controlPlaneEndpointsConfig": map[string]any{"dnsEndpointConfig": map[string]any{
			"endpoint": sampleKubeHost, "allowExternalTraffic": true}}})
	})
	target, _ := url.Parse(kube.URL)
	g.HTTP = &http.Client{Transport: kubeOrGoogle{google: g.HTTP.Transport, kube: target}}
	routers := []string{"routers show", "routers settings", "routers pause", "routers resume", "routers waiting-page", "routers uninstall", "workloads", "connect", "disconnect"}
	if slices.ContainsFunc(routers, func(r string) bool { return strings.HasPrefix(s.line, "dormeto gcp "+r+" ") }) {
		var out bytes.Buffer
		d := sampleGcpDeps(g, &out, &out)
		install := gcponboard.Request{Project: sampleProject, Region: sampleRegion, Yes: true}
		if strings.HasPrefix(s.line, "dormeto gcp workloads access ") {
			install.With = []string{"gke"} // the router's access to GKE clusters
		}
		if code := gcponboard.Install(context.Background(), d, install); code != 0 {
			t.Fatalf("the router's install: exit %d\n%s", code, out.String())
		}
		g.Lock()
		g.Workloads["shop"], g.Workloads["blog"] = "ASLEEP", "AWAKE"
		g.WorkloadDocs["shop"] = map[string]any{"name": "shop", "urlMap": "shop-lb", "backendService": "shop", "lb": map[string]any{"kind": "global"},
			"tags": map[string]any{"team": "web"}, "stateAt": "2026-11-14T07:10:00.000Z",
			"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-b", "name": "shop-1", "id": "4419283746501928374"}}}
		g.WorkloadDocs["blog"] = map[string]any{"name": "blog", "urlMap": "blog-lb", "backendService": "blog", "lb": map[string]any{"kind": "global"},
			"tags": map[string]any{"team": "web"}, "stateAt": "2026-11-14T09:40:00.000Z",
			"backends": []any{map[string]any{"kind": "vm", "location": "europe-west1-c", "name": "blog-1", "id": "7730192837465019283"}}}
		g.BackendServices["global/checkout"] = &gcptest.BackendService{}
		g.DryRun = gcptest.DryRun{
			VMs:    []gcptest.Resource{{Location: "europe-west1-b", Name: "checkout-1", ID: "5512098347561029384"}},
			Groups: []gcptest.Resource{{Kind: "regional", Location: "europe-west1", Name: "checkout-mig", ID: "6623109458672130495"}},
		}
		g.Unlock()
	}
	if s.google != nil {
		var out bytes.Buffer
		s.google(t, g, sampleGcpDeps(g, &out, &out))
	}
	return g
}

// sampleKubeHost is the DNS endpoint of the GKE cluster shop-cluster, which the fake Kubernetes answers.
const sampleKubeHost = "gke-4f2a9c1e7b3d5a60-123456789012.europe-west1-b.gke.goog"

// kubeOrGoogle sends a cluster's DNS endpoint to the fake Kubernetes, the rest to the fake Google.
type kubeOrGoogle struct {
	google http.RoundTripper
	kube   *url.URL
}

func (k kubeOrGoogle) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(req.URL.Hostname(), ".gke.goog") {
		return k.google.RoundTrip(req)
	}
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host = k.kube.Scheme, k.kube.Host
	out.Host = req.URL.Host
	return http.DefaultTransport.RoundTrip(out)
}

// sampleKube is a Kubernetes API that keeps the Roles and RoleBindings of its namespaces, as JSON objects by path.
type sampleKube struct {
	*httptest.Server
	mu      sync.Mutex
	objects map[string]map[string]any
	version int
}

func newSampleKube(t *testing.T) *sampleKube {
	t.Helper()
	k := &sampleKube{objects: map[string]map[string]any{}}
	k.Server = httptest.NewServer(http.HandlerFunc(k.serve))
	t.Cleanup(k.Close)
	return k
}

func (k *sampleKube) serve(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	failure := func(code int, reason string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "message": r.URL.Path + ": " + reason, "reason": reason, "code": code})
	}
	var o map[string]any
	_ = json.NewDecoder(r.Body).Decode(&o)
	path := r.URL.Path
	if r.Method == http.MethodPost {
		path += "/" + o["metadata"].(map[string]any)["name"].(string)
	}
	old, there := k.objects[path]
	switch {
	case r.Method == http.MethodGet && there:
		json.NewEncoder(w).Encode(old)
	case r.Method == http.MethodPost && !there, r.Method == http.MethodPut && there:
		k.version++
		o["metadata"].(map[string]any)["resourceVersion"] = fmt.Sprint(k.version)
		k.objects[path] = o
		json.NewEncoder(w).Encode(o)
	case r.Method == http.MethodDelete && there:
		delete(k.objects, path)
		json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Success"})
	case there:
		failure(409, "AlreadyExists")
	default:
		failure(404, "NotFound")
	}
}

// sampleGcpDeps are the gcp commands' dependencies on the fake Google: the account ops@example.com, the fixed clock, no
// wait, the cache in the line's working directory.
func sampleGcpDeps(g *gcptest.Server, out, errs io.Writer) gcponboard.Deps {
	cache, _ := os.Getwd()
	return gcponboard.Deps{
		Google: &gcp.Client{HTTP: g.HTTP, Tokens: gcptest.Tokens{AccountName: sampleAccount}, Sleep: func(context.Context, time.Duration) error { return nil }},
		Out:    out, Err: errs,
		Now:     func() time.Time { return sampleNow },
		Sleep:   func(context.Context, time.Duration) error { return nil },
		Cache:   filepath.Join(cache, "cache"),
		Version: "0.14.0",
	}
}

// sampleGcpRuns are internal/gcponboard's runs with the fake Google in place of gcloud's.
func sampleGcpRuns(g *gcptest.Server) gcpRuns {
	on := func(run gcpRun) gcpRun {
		return func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			f := sampleGcpDeps(g, d.Out, d.Err)
			f.Prompter, f.Terminal = d.Prompter, d.Terminal
			return run(ctx, f, r)
		}
	}
	onConnect := func(run gcpConnectRun) gcpConnectRun {
		return func(ctx context.Context, d gcponboard.Deps, r gcponboard.ConnectRequest) int {
			f := sampleGcpDeps(g, d.Out, d.Err)
			f.Prompter, f.Terminal = d.Prompter, d.Terminal
			return run(ctx, f, r)
		}
	}
	w := theGcpWorkloadRuns
	return gcpRuns{whoami: on(theGcpRuns.whoami), check: on(theGcpRuns.check), install: on(theGcpRuns.install), status: on(theGcpRuns.status),
		uninstall: on(theGcpRuns.uninstall), routerSettings: on(theGcpRuns.routerSettings), routerPause: on(theGcpRuns.routerPause),
		routerResume: on(theGcpRuns.routerResume),
		workloads: gcpWorkloadRuns{list: on(w.list), show: on(w.show), onboard: on(w.onboard), offboard: on(w.offboard), nap: on(w.nap),
			wake: on(w.wake), scheduleShow: on(w.scheduleShow), scheduleSet: on(w.scheduleSet), scheduleClear: on(w.scheduleClear),
			pause: on(w.pause), resume: on(w.resume), accessShow: on(w.accessShow), accessGrant: on(w.accessGrant),
			accessRevoke: on(w.accessRevoke), settings: on(w.settings), pageShow: on(w.pageShow), pageSet: on(w.pageSet), pageClear: on(w.pageClear)},
		routerPage: gcpPageRuns{on(theGcpRuns.routerPage.show), on(theGcpRuns.routerPage.set), on(theGcpRuns.routerPage.clear)},
		connect:    onConnect(theGcpRuns.connect), disconnect: onConnect(theGcpRuns.disconnect)}
}
