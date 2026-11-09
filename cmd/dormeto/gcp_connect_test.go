package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dormeto/cli/internal/gcponboard"
)

// managerAnswers is a manager's /iam API as connect reads it: /health, /me, /update and /audit.
func managerAnswers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/iam/health":
		io.WriteString(w, `{"status":"ok","version":"0.24.0-dev.2"}`)
	case "/iam/me":
		io.WriteString(w, `{"email":"arn:aws:sts::121014831971:assumed-role/Admin/fab","role":"admin"}`)
	case "/iam/update":
		io.WriteString(w, `{"installedWithCli":true,"version":"0.24.0-dev.2","env":"dev","region":"eu-west-1"}`)
	case "/iam/audit":
		io.WriteString(w, `{"entries":[
			{"at":"2026-11-08T09:00:00.000Z","action":"google-connection","target":"other","result":"disconnected, 0 routers unregistered"},
			{"at":"2026-11-07T09:00:00.000Z","action":"google-connection","target":"dormeto-dev","result":"1 router, 1 scope"},
			{"at":"2026-11-02T09:30:00.000Z","action":"google-connection","target":"dormeto-dev","result":"disconnected, 1 router unregistered"},
			{"at":"2026-10-01T09:30:00.000Z","action":"google-connection","target":"dormeto-dev","result":"disconnected, 2 routers unregistered"}]}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"not_found","message":"no route"}`)
	}
}

// gcpConnectRan runs a command line on the gcp commands with fake connect and disconnect runs and a fake manager API.
func gcpConnectRan(t *testing.T, code int, args ...string) (calls []gcpConnectCall, out fleetRan) {
	t.Helper()
	ran := func(name string) gcpConnectRun {
		return func(_ context.Context, d gcponboard.Deps, r gcponboard.ConnectRequest) int {
			calls = append(calls, gcpConnectCall{name, r, d})
			return code
		}
	}
	api := &fakeAPI{custom: managerAnswers}
	out = fleetRan{api: api}
	deps := fakeFleetDeps(t, &out, "eu-west-3", nil)
	root := &cobra.Command{Use: "dormeto", SilenceUsage: true}
	root.AddCommand(newGcp(gcpRuns{connect: ran("connect"), disconnect: ran("disconnect")}, deps, "1.2.3"))
	var o, e bytes.Buffer
	root.SetOut(&o)
	root.SetErr(&e)
	root.SetIn(strings.NewReader(""))
	out.exit = execute(root, args...)
	out.stdout, out.stderr = o.String(), e.String()
	return calls, out
}

type gcpConnectCall struct {
	run string
	r   gcponboard.ConnectRequest
	d   gcponboard.Deps
}

// The flags go into the request; the manager is found as the fleet commands find it, --manager-region for their --region;
// it says its version (/health), its account (the caller's ARN, /me) and its environment (/update).
func TestGcpConnectFlagsReadIntoTheRequestAndTheManagerIsRead(t *testing.T) {
	calls, out := gcpConnectRan(t, 0, "gcp", "connect", "--project", "dormeto-dev", "--scope", "projects/dormeto-dev", "--scope", "folders/7",
		"--profile", "kloudy", "--env", "dev", "--manager-region", "eu-west-1", "--gcloud-configuration", "dormeto-dev", "--dry-run", "--yes", "--json")
	if out.exit != 0 || out.stderr != "" || len(calls) != 1 || calls[0].run != "connect" {
		t.Fatalf("exit %d %q %+v", out.exit, out.stderr, calls)
	}
	r := calls[0].r
	m := r.Manager
	r.Manager = nil
	want := gcponboard.ConnectRequest{Project: "dormeto-dev", Scopes: []string{"projects/dormeto-dev", "folders/7"}, Configuration: "dormeto-dev",
		Profile: "kloudy", Env: "dev", ManagerRegion: "eu-west-1", Yes: true, DryRun: true, JSON: true}
	if !reflect.DeepEqual(r, want) || out.profile != "kloudy" || !reflect.DeepEqual(out.located, []string{"dev/eu-west-1"}) {
		t.Fatalf("%+v profile %q located %v", r, out.profile, out.located)
	}
	// --api-url too, for the commands the runs tell to run.
	calls, out = gcpConnectRan(t, 0, "gcp", "disconnect", "--project", "dormeto-dev", "--env", "dev", "--api-url", "https://abc123.execute-api.eu-west-1.amazonaws.com")
	if out.exit != 0 || len(calls) != 1 || calls[0].r.APIURL != "https://abc123.execute-api.eu-west-1.amazonaws.com" || calls[0].r.ManagerRegion != "" {
		t.Fatalf("exit %d %q %+v", out.exit, out.stderr, calls)
	}
	info, err := m.About(context.Background())
	if err != nil || info.Version != "0.24.0-dev.2" || info.Account != "121014831971" || info.Env != "dev" || !strings.HasPrefix(info.URL, "http://127.0.0.1:") {
		t.Fatalf("%+v %v", info, err)
	}
	if calls[0].d.Version != "1.2.3" || calls[0].d.Google == nil {
		t.Fatalf("deps %+v", calls[0].d)
	}
}

// The manager's audit gives the last disconnect of the host: the date a blocked role ID frees is counted from it.
func TestGcpManagerDisconnectedIsTheAuditsLastDisconnectOfTheHost(t *testing.T) {
	calls, out := gcpConnectRan(t, 0, "gcp", "connect", "--project", "dormeto-dev", "--env", "dev")
	if out.exit != 0 || len(calls) != 1 {
		t.Fatalf("exit %d %q", out.exit, out.stderr)
	}
	at, err := calls[0].r.Manager.Disconnected(context.Background(), "dormeto-dev")
	if err != nil || !at.Equal(time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("%v %v", at, err)
	}
	if at, err := calls[0].r.Manager.Disconnected(context.Background(), "never"); err != nil || !at.IsZero() {
		t.Fatalf("%v %v", at, err)
	}
}

func TestGcpConnectAndDisconnectExitCodes(t *testing.T) {
	if calls, out := gcpConnectRan(t, 1, "gcp", "disconnect", "--project", "dormeto-dev"); out.exit != 1 || len(calls) != 1 || calls[0].run != "disconnect" {
		t.Fatalf("exit %d %+v", out.exit, calls)
	}
	for _, args := range [][]string{
		{"gcp", "connect"},
		{"gcp", "connect", "--project", "p", "--region", "eu-west-1"},
		{"gcp", "connect", "--project", "p", "--env", "Not An Env"},
		{"gcp", "connect", "--project", "p", "--api-url", "ftp://x"},
		{"gcp", "disconnect", "--project", "p", "--scope", "projects/p"},
		{"gcp", "connect", "--project", "p", "extra"},
	} {
		if calls, out := gcpConnectRan(t, 0, args...); out.exit != 2 || len(calls) != 0 {
			t.Errorf("%v: exit %d calls %+v stderr %q", args, out.exit, calls, out.stderr)
		}
	}
}

func TestGcpConnectIsWiredInTheRoot(t *testing.T) {
	for _, name := range []string{"connect", "disconnect"} {
		out, _, code := executed(t, build{}, "gcp", name, "--help")
		if code != 0 || !strings.Contains(out, "--manager-region") || !strings.Contains(out, "--gcloud-configuration") || !strings.Contains(out, "--dry-run") {
			t.Fatalf("%s: %d %s", name, code, out)
		}
	}
}

// flagNames are a command's flags, its own and those it inherits, but help.
func flagNames(t *testing.T, root *cobra.Command, path ...string) []string {
	t.Helper()
	cmd, _, err := root.Find(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	add := func(f *pflag.Flag) {
		if f.Name != "help" && !slices.Contains(out, f.Name) {
			out = append(out, f.Name)
		}
	}
	cmd.LocalFlags().VisitAll(add)
	cmd.InheritedFlags().VisitAll(add)
	slices.Sort(out)
	return out
}

// readmeFlags are the flags a text of the README names, --name; with quoted, only those opening a code span, `--name` (a
// span quoting a command line, such as an install a refusal names, is that command's).
func readmeFlags(text string, quoted bool) []string {
	pattern := "--([a-z][a-z0-9-]*)"
	if quoted {
		pattern = "`" + pattern
	}
	var out []string
	for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(text, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	slices.Sort(out)
	return out
}

// The README's "Connecting the manager" names exactly the flags of gcp connect and disconnect (and --region, which it names as
// the fleet commands' flag), and its routers pause bullet and examples exactly those of gcp routers pause and resume but the
// ones every gcp command takes (--project, --region, --gcloud-configuration, said in the section's introduction).
func TestReadmeNamesTheFlagsOfConnectDisconnectAndRoutersPause(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, connect, ok := strings.Cut(string(b), "\n### Connecting the manager\n")
	if !ok {
		t.Fatal("README.md has no '### Connecting the manager'")
	}
	connect, _, _ = strings.Cut(connect, "\n### ")
	root := &cobra.Command{Use: "dormeto"}
	root.AddCommand(newGcp(gcpRuns{}, fleetDeps{}, "1.2.3"))
	want := flagNames(t, root, "gcp", "connect")
	for _, f := range flagNames(t, root, "gcp", "disconnect") {
		if !slices.Contains(want, f) {
			t.Errorf("disconnect has --%s, which connect has not", f)
		}
	}
	want = append(want, "region")
	slices.Sort(want)
	if got := readmeFlags(connect, false); !slices.Equal(got, want) {
		t.Errorf("Connecting the manager names %v, connect and disconnect take %v (and --region)", got, want)
	}

	_, pause, ok := strings.Cut(string(b), "\n### Google Cloud schedules and pauses\n")
	if !ok {
		t.Fatal("README.md has no '### Google Cloud schedules and pauses'")
	}
	pause, _, _ = strings.Cut(pause, "\n### ")
	var named []string
	for _, line := range strings.Split(pause, "\n") {
		if strings.HasPrefix(line, "dormeto gcp routers pause") || strings.HasPrefix(line, "dormeto gcp routers resume") {
			named = append(named, line)
		}
	}
	_, bullet, _ := strings.Cut(pause, "\n- `routers pause` pauses the whole router")
	bullet, _, _ = strings.Cut(bullet, "\n- ")
	got := readmeFlags(strings.Join(named, "\n"), false)
	for _, f := range readmeFlags(bullet, true) {
		if !slices.Contains(got, f) {
			got = append(got, f)
		}
	}
	common := []string{"gcloud-configuration", "project", "region"}
	var takes []string
	for _, path := range [][]string{{"gcp", "routers", "pause"}, {"gcp", "routers", "resume"}} {
		for _, f := range flagNames(t, root, path...) {
			if !slices.Contains(takes, f) {
				takes = append(takes, f)
			}
		}
	}
	for _, f := range got {
		if !slices.Contains(takes, f) {
			t.Errorf("the routers pause bullet names --%s, which pause and resume don't take", f)
		}
	}
	for _, f := range takes {
		if !slices.Contains(common, f) && !slices.Contains(got, f) {
			t.Errorf("the routers pause bullet doesn't name --%s", f)
		}
	}
}
