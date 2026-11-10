package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/gcponboard"
)

// gcpCall is one run of a gcp command: which, with what.
type gcpCall struct {
	run string
	r   gcponboard.Request
	d   gcponboard.Deps
}

func gcpRan(t *testing.T, code int, args ...string) (calls []gcpCall, stderr string, exit int) {
	t.Helper()
	return gcpRanWith(t, "", code, args...)
}

// gcpRanWith is gcpRan with stdin as the standard input.
func gcpRanWith(t *testing.T, stdin string, code int, args ...string) (calls []gcpCall, stderr string, exit int) {
	t.Helper()
	ran := func(name string) gcpRun {
		return func(_ context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			calls = append(calls, gcpCall{name, r, d})
			return code
		}
	}
	runs := gcpRuns{check: ran("check"), install: ran("install"), status: ran("status")}
	runs.uninstall = ran("uninstall")
	runs.routerSettings = ran("routers settings")
	runs.routerPause, runs.routerResume = ran("routers pause"), ran("routers resume")
	runs.workloads = gcpWorkloadRuns{list: ran("workloads list"), show: ran("workloads show"), onboard: ran("workloads onboard"),
		offboard: ran("workloads offboard"), nap: ran("workloads nap"), wake: ran("workloads wake"),
		scheduleShow: ran("workloads schedule show"), scheduleSet: ran("workloads schedule set"), scheduleClear: ran("workloads schedule clear"),
		pause: ran("workloads pause"), resume: ran("workloads resume"),
		accessShow: ran("workloads access show"), accessGrant: ran("workloads access grant"), accessRevoke: ran("workloads access revoke"), settings: ran("workloads settings"),
		pageShow: ran("workloads waiting-page show"), pageSet: ran("workloads waiting-page set"), pageClear: ran("workloads waiting-page clear")}
	runs.routerPage = gcpPageRuns{show: ran("routers waiting-page show"), set: ran("routers waiting-page set"), clear: ran("routers waiting-page clear")}
	root := &cobra.Command{Use: "dormeto", SilenceUsage: true}
	root.AddCommand(newGcp(runs, fleetDeps{}, "1.2.3"))
	var out, errs bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errs)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	exit = execute(root, args...)
	return calls, errs.String(), exit
}

func TestGcpCheckFlagsReadIntoTheRequest(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "check", "--project", "dormeto-dev", "--region", "europe-west1", "--json")
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "check" {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	want := gcponboard.Request{Project: "dormeto-dev", Region: "europe-west1", JSON: true}
	if !reflect.DeepEqual(calls[0].r, want) {
		t.Fatalf("%+v", calls[0].r)
	}
}

func TestGcpCheckExitCodes(t *testing.T) {
	if _, _, exit := gcpRan(t, 1, "gcp", "check", "--project", "p"); exit != 1 {
		t.Fatalf("exit %d", exit)
	}
	for _, args := range [][]string{{"gcp", "check"}, {"gcp", "check", "--project", "p", "--nope"}, {"gcp", "check", "--project", "p", "extra"}} {
		calls, _, exit := gcpRan(t, 0, args...)
		if exit != 2 || len(calls) != 0 {
			t.Errorf("%v: exit %d calls %v", args, exit, calls)
		}
	}
}

func TestGcpCheckIsWiredInTheRoot(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "check", "--help")
	if code != 0 || !strings.Contains(out, "--gcloud-configuration") || !strings.Contains(out, "--project") {
		t.Fatalf("%d %s", code, out)
	}
}

func TestGcpInstallFlagsReadIntoTheRequest(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	calls, errs, exit := gcpRan(t, 0, "gcp", "routers", "install", "--project", "dormeto-dev", "--region", "europe-west1", "--yes", "--dry-run",
		"--gcloud-configuration", "dormeto-dev")
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "install" {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	want := gcponboard.Request{Project: "dormeto-dev", Region: "europe-west1", Configuration: "dormeto-dev", Yes: true, DryRun: true}
	if !reflect.DeepEqual(calls[0].r, want) {
		t.Fatalf("%+v", calls[0].r)
	}
	d := calls[0].d
	if d.Version != "1.2.3" || d.Cache != cache || d.Now == nil || d.Sleep == nil || d.Prompter == nil || d.Terminal ||
		d.Google == nil || d.Google.Tokens == nil || d.Out == nil || d.Err == nil {
		t.Fatalf("%+v", d)
	}
}

func TestGcpInstallCacheDefaultsToTheHomeCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", home)
	calls, _, exit := gcpRan(t, 0, "gcp", "routers", "install", "--project", "p", "--region", "europe-west1")
	if exit != 0 || len(calls) != 1 || calls[0].d.Cache != filepath.Join(home, ".cache") {
		t.Fatalf("%d %+v", exit, calls)
	}
}

func TestGcpInstallExitCodes(t *testing.T) {
	if _, _, exit := gcpRan(t, 1, "gcp", "routers", "install", "--project", "p", "--region", "europe-west1"); exit != 1 {
		t.Fatalf("exit %d", exit)
	}
	for _, args := range [][]string{
		{"gcp", "routers", "install", "--region", "europe-west1"},
		{"gcp", "routers", "install", "--project", "p"},
		{"gcp", "routers", "install", "--project", "p", "--region", "europe-west1", "--nope"},
		{"gcp", "routers", "install", "--project", "p", "--region", "europe-west1", "extra"},
	} {
		calls, errs, exit := gcpRan(t, 0, args...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: exit %d calls %v stderr %q", args, exit, calls, errs)
		}
	}
}

func TestGcpInstallIsWiredInTheRoot(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "routers", "install", "--help")
	for _, flag := range []string{"--project", "--region", "--gcloud-configuration", "--yes", "--dry-run"} {
		if code != 0 || !strings.Contains(out, flag) {
			t.Errorf("%d %s lacks %s", code, out, flag)
		}
	}
}

func TestGcpInstallImageReadIntoTheRequest(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "routers", "install", "--project", "p", "--region", "europe-west1", "--image", "europe-west1-docker.pkg.dev/p/d/router:0.34.0")
	if exit != 0 || errs != "" || len(calls) != 1 {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	want := gcponboard.Request{Project: "p", Region: "europe-west1", Image: "europe-west1-docker.pkg.dev/p/d/router:0.34.0"}
	if !reflect.DeepEqual(calls[0].r, want) {
		t.Fatalf("%+v", calls[0].r)
	}
}

func TestGcpCheckImageReadIntoTheRequestAndItsHelp(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "check", "--project", "p", "--image", "europe-west1-docker.pkg.dev/p/d/router:0.34.0")
	want := gcponboard.Request{Project: "p", Image: "europe-west1-docker.pkg.dev/p/d/router:0.34.0"}
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "check" || !reflect.DeepEqual(calls[0].r, want) {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
	out, _, code := executed(t, build{}, "gcp", "check", "--help")
	for _, w := range []string{"--image", "artifactregistry.googleapis.com", "only for a router of --image"} {
		if code != 0 || !strings.Contains(out, w) {
			t.Errorf("%d: lacks %q in:\n%s", code, w, out)
		}
	}
}

func TestGcpInstallVersionAndAccessOnlyReadIntoTheRequest(t *testing.T) {
	for _, tc := range []struct {
		flags []string
		want  gcponboard.Request
	}{
		{[]string{"--version", "0.38.0"}, gcponboard.Request{Project: "p", Region: "europe-west1", Version: "0.38.0"}},
		{[]string{"--access-only"}, gcponboard.Request{Project: "p", Region: "europe-west1", AccessOnly: true}},
	} {
		calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "routers", "install", "--project", "p", "--region", "europe-west1"}, tc.flags...)...)
		if exit != 0 || errs != "" || len(calls) != 1 || !reflect.DeepEqual(calls[0].r, tc.want) {
			t.Fatalf("%v: %d %q %+v", tc.flags, exit, errs, calls)
		}
	}
}

func TestGcpInstallHelpNamesThePublicImageAndItsFlags(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "routers", "install", "--help")
	for _, want := range []string{"--version", "--access-only", "dormeto-releases/router/router:<version>", "europe, us or asia"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("%d: lacks %q in:\n%s", code, want, out)
		}
	}
	if strings.Contains(out, "Install the access of a Dormeto router") || strings.Contains(out, "without --image the recorded image is reused") {
		t.Errorf("still says the access only by default:\n%s", out)
	}
}

func TestGcpInstallImageIsInItsHelp(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "routers", "install", "--help")
	if code != 0 || !strings.Contains(out, "--image") || !strings.Contains(out, "dmtr-<region>-controller") {
		t.Fatalf("%d %s", code, out)
	}
}

func TestGcpStatusFlagsReadIntoTheRequest(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "routers", "show", "--project", "p", "--region", "europe-west1", "--json", "--gcloud-configuration", "c")
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "status" {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	want := gcponboard.Request{Project: "p", Region: "europe-west1", Configuration: "c", JSON: true}
	if !reflect.DeepEqual(calls[0].r, want) {
		t.Fatalf("%+v", calls[0].r)
	}
	calls, _, exit = gcpRan(t, 0, "gcp", "routers", "show", "--project", "p")
	if exit != 0 || len(calls) != 1 || !reflect.DeepEqual(calls[0].r, gcponboard.Request{Project: "p"}) {
		t.Fatalf("%d %+v", exit, calls)
	}
}

func TestGcpStatusExitCodes(t *testing.T) {
	if _, _, exit := gcpRan(t, 1, "gcp", "routers", "show", "--project", "p"); exit != 1 {
		t.Fatalf("exit %d", exit)
	}
	for _, args := range [][]string{{"gcp", "routers", "show"}, {"gcp", "routers", "show", "--project", "p", "--nope"}, {"gcp", "routers", "show", "--project", "p", "extra"}} {
		calls, errs, exit := gcpRan(t, 0, args...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: exit %d calls %v stderr %q", args, exit, calls, errs)
		}
	}
}

func TestGcpStatusIsWiredInTheRoot(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "routers", "show", "--help")
	for _, flag := range []string{"--project", "--region", "--gcloud-configuration", "--json"} {
		if code != 0 || !strings.Contains(out, flag) {
			t.Errorf("%d %s lacks %s", code, out, flag)
		}
	}
}

func TestGcpUninstallFlagsReadIntoTheRequest(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "routers", "uninstall", "--project", "p", "--region", "europe-west1", "--yes", "--force", "--dry-run", "--gcloud-configuration", "c")
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "uninstall" {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	want := gcponboard.Request{Project: "p", Region: "europe-west1", Configuration: "c", Yes: true, Force: true, DryRun: true}
	if !reflect.DeepEqual(calls[0].r, want) {
		t.Fatalf("%+v", calls[0].r)
	}
}

func TestGcpUninstallExitCodes(t *testing.T) {
	if _, _, exit := gcpRan(t, 1, "gcp", "routers", "uninstall", "--project", "p", "--region", "europe-west1"); exit != 1 {
		t.Fatalf("exit %d", exit)
	}
	for _, args := range [][]string{
		{"gcp", "routers", "uninstall", "--region", "europe-west1"},
		{"gcp", "routers", "uninstall", "--project", "p"},
		{"gcp", "routers", "uninstall", "--project", "p", "--region", "europe-west1", "--nope"},
	} {
		calls, errs, exit := gcpRan(t, 0, args...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: exit %d calls %v stderr %q", args, exit, calls, errs)
		}
	}
}

func TestGcpUninstallIsWiredInTheRoot(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "routers", "uninstall", "--help")
	for _, flag := range []string{"--project", "--region", "--gcloud-configuration", "--yes", "--dry-run", "--force"} {
		if code != 0 || !strings.Contains(out, flag) {
			t.Errorf("%d %s lacks %s", code, out, flag)
		}
	}
}

func TestGcpWithIsCommaSeparatedAndRepeatable(t *testing.T) {
	for _, cmd := range []string{"check", "routers install", "routers uninstall"} {
		calls, errs, exit := gcpRan(t, 0, append(append([]string{"gcp"}, strings.Fields(cmd)...), "--project", "p", "--region", "europe-west1", "--with", "cloud-sql,gke", "--with", "gke")...)
		if exit != 0 || errs != untriedWarning || len(calls) != 1 || !reflect.DeepEqual(calls[0].r.With, []string{"cloud-sql", "gke", "gke"}) {
			t.Errorf("%s: %d %q %+v", cmd, exit, errs, calls)
		}
		calls, _, _ = gcpRan(t, 0, append(append([]string{"gcp"}, strings.Fields(cmd)...), "--project", "p", "--region", "europe-west1")...)
		if len(calls) != 1 || calls[0].r.With != nil {
			t.Errorf("%s without --with: %+v", cmd, calls)
		}
	}
}

func TestGcpInstallIPListFlagsReadIntoTheRequest(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "routers", "install", "--project", "p", "--region", "europe-west1",
		"--ip-list", "name=scanners,url=https://example.com/s.txt", "--ip-list", "name=bots,url=http://example.com/b.txt?a=1,refresh_hours=6")
	want := []gcponboard.IPList{{Name: "scanners", URL: "https://example.com/s.txt", RefreshHours: 24}, {Name: "bots", URL: "http://example.com/b.txt?a=1", RefreshHours: 6}}
	if exit != 0 || errs != "" || len(calls) != 1 || !reflect.DeepEqual(calls[0].r.IPLists, want) || calls[0].r.NoIPLists {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
	calls, _, exit = gcpRan(t, 0, "gcp", "routers", "install", "--project", "p", "--region", "europe-west1", "--no-ip-lists")
	if exit != 0 || len(calls) != 1 || !calls[0].r.NoIPLists || calls[0].r.IPLists != nil {
		t.Fatalf("%d %+v", exit, calls)
	}
}

// --custom-pages is the switch as given: on, off with =false, nil when omitted (the record's is kept).
func TestGcpInstallCustomPagesReadIntoTheRequest(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		flags []string
		want  *bool
	}{
		{nil, nil},
		{[]string{"--custom-pages"}, &on},
		{[]string{"--custom-pages=true"}, &on},
		{[]string{"--custom-pages=false"}, &off},
	} {
		calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "routers", "install", "--project", "p", "--region", "europe-west1"}, tc.flags...)...)
		if exit != 0 || errs != "" || len(calls) != 1 || !reflect.DeepEqual(calls[0].r.CustomPages, tc.want) {
			t.Fatalf("%v: %d %q %+v", tc.flags, exit, errs, calls)
		}
	}
}

func TestGcpInstallHelpNamesCustomPages(t *testing.T) {
	out, _, code := executed(t, build{}, "gcp", "routers", "install", "--help")
	for _, want := range []string{"--custom-pages", "DMT_CUSTOM_PAGES", "--custom-pages=false"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("%d: lacks %q in:\n%s", code, want, out)
		}
	}
}

func TestGcpInstallRefusesABadIPListBeforeAnyRun(t *testing.T) {
	for _, args := range [][]string{
		{"--ip-list", "name=a,url=s3://x/y"},
		{"--ip-list", "name=a,url=https://example.com,action=defer"},
		{"--ip-list", "name=a/b,url=https://example.com"},
		{"--ip-list", "name=a,url=https://u:p@example.com"},
		{"--ip-list", "name=a,url=https://example.com,refresh=0"},
		{"--ip-list", "name=a,url=https://example.com", "--ip-list", "name=a,url=https://example.org"},
		{"--ip-list", "name=a,url=https://example.com", "--no-ip-lists"},
	} {
		calls, errs, exit := gcpRan(t, 0, append([]string{"gcp", "routers", "install", "--project", "p", "--region", "europe-west1"}, args...)...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: exit %d calls %v stderr %q", args, exit, calls, errs)
		}
	}
}

// Cloud SQL and GKE were accepted in dormeto-dev on 2026-11-04 (vision plan 2026-10-30): no warning of them any more.
const untriedWarning = ""

func TestGcpNoLongerWarnsOfCloudSQLAndGKE(t *testing.T) {
	for name, args := range map[string][]string{
		"check --with":      {"gcp", "check", "--project", "p", "--with", "cloud-sql"},
		"install --with":    {"gcp", "routers", "install", "--project", "p", "--region", "europe-west1", "--yes", "--with", "cloud-sql,gke"},
		"uninstall --with":  {"gcp", "routers", "uninstall", "--project", "p", "--region", "europe-west1", "--yes", "--with", "gke"},
		"onboard cloud-sql": {"gcp", "workloads", "onboard", "--project", "p", "--region", "r", "--url-map", "m", "--backend-service", "b", "--database", "db"},
		"onboard gke":       {"gcp", "workloads", "onboard", "--project", "p", "--region", "r", "--url-map", "m", "--backend-service", "b", "--gke-cluster", "z/c"},
		"onboard both":      {"gcp", "workloads", "onboard", "--project", "p", "--region", "r", "--url-map", "m", "--backend-service", "b", "--gke-cluster", "z/c", "--database", "a", "--database", "b2"},
		"access show":       {"gcp", "workloads", "access", "show", "--project", "p", "--region", "r", "--gke-cluster", "z/c", "--namespace", "n", "--workload", "w"},
		"access grant":      {"gcp", "workloads", "access", "grant", "--project", "p", "--region", "r", "--gke-cluster", "z/c", "--namespace", "n", "--workload", "w", "--yes"},
	} {
		t.Run(name, func(t *testing.T) {
			calls, errs, exit := gcpRan(t, 0, args...)
			if exit != 0 || len(calls) != 1 {
				t.Fatalf("%d %q %v", exit, errs, calls)
			}
			if errs != untriedWarning {
				t.Fatalf("stderr %q", errs)
			}
		})
	}
}

func TestGcpDoesNotWarnWithoutThoseFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"check":   {"gcp", "check", "--project", "p"},
		"install": {"gcp", "routers", "install", "--project", "p", "--region", "europe-west1", "--yes"},
		"onboard": {"gcp", "workloads", "onboard", "--project", "p", "--region", "r", "--url-map", "m", "--backend-service", "b"},
	} {
		t.Run(name, func(t *testing.T) {
			calls, errs, exit := gcpRan(t, 0, args...)
			if exit != 0 || len(calls) != 1 || errs != "" {
				t.Fatalf("%d %q %v", exit, errs, calls)
			}
		})
	}
}

func TestGcpRoutersPauseReadsTheRequest(t *testing.T) {
	end := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	calls, errs, exit := gcpRan(t, 0, "gcp", "routers", "pause", "--project", "p", "--region", "europe-west1", "--reason", " freeze ",
		"--until", end.In(time.FixedZone("", 3600)).Format(time.RFC3339), "--keep-asleep", "--json")
	want := gcponboard.Request{Project: "p", Region: "europe-west1", Reason: "freeze", Until: end.UTC().Format(time.RFC3339), KeepAsleep: true, JSON: true}
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "routers pause" || !reflect.DeepEqual(calls[0].r, want) {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
	// --for is a duration from now, sent as the time it ends.
	calls, _, exit = gcpRan(t, 0, "gcp", "routers", "pause", "--project", "p", "--region", "europe-west1", "--reason", "x", "--for", "2h")
	if exit != 0 || len(calls) != 1 || calls[0].r.Until == "" {
		t.Fatalf("%d %+v", exit, calls)
	}
	until, err := time.Parse(time.RFC3339, calls[0].r.Until)
	if err != nil || time.Until(until) < 119*time.Minute || time.Until(until) > 121*time.Minute {
		t.Fatalf("%v %s", err, calls[0].r.Until)
	}
	// Neither: until it is resumed.
	calls, _, _ = gcpRan(t, 0, "gcp", "routers", "pause", "--project", "p", "--region", "europe-west1", "--reason", "x")
	if len(calls) != 1 || calls[0].r.Until != "" || calls[0].r.KeepAsleep {
		t.Fatalf("%+v", calls)
	}
}

func TestGcpRoutersPauseRefusesBeforeAnyCall(t *testing.T) {
	base := []string{"gcp", "routers", "pause", "--project", "p", "--region", "europe-west1"}
	for _, extra := range [][]string{
		{},
		{"--reason", "  "},
		{"--reason", strings.Repeat("x", 257)},
		{"--reason", "x", "--for", "1h", "--until", "2099-12-01T08:00:00Z"},
		{"--reason", "x", "--for", "400d"},
		{"--reason", "x", "--until", "tomorrow"},
		{"--reason", "x", "--until", "2001-01-01T00:00:00Z"},
		{"--reason", "x", "extra"},
	} {
		calls, errs, exit := gcpRan(t, 0, append(slices.Clone(base), extra...)...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: exit %d %q calls %v", extra, exit, errs, calls)
		}
	}
	calls, _, exit := gcpRan(t, 0, "gcp", "routers", "pause", "--project", "p", "--reason", "x")
	if exit != 2 || len(calls) != 0 {
		t.Errorf("no region: %d %v", exit, calls)
	}
}

func TestGcpRoutersResumeReadsTheRequest(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "routers", "resume", "--project", "p", "--region", "europe-west1", "--json")
	want := gcponboard.Request{Project: "p", Region: "europe-west1", JSON: true}
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "routers resume" || !reflect.DeepEqual(calls[0].r, want) {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
	if calls, _, exit := gcpRan(t, 0, "gcp", "routers", "resume", "--project", "p"); exit != 2 || len(calls) != 0 {
		t.Fatalf("%d %v", exit, calls)
	}
}

func TestGcpWorkloadsOnboardReadsTheNameAndTheTags(t *testing.T) {
	onboard := []string{"gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "be"}
	calls, errs, exit := gcpRan(t, 0, append(slices.Clone(onboard), "--name", "  The shop ", "--set-tag", "team=web", "--set-tag", "env=prod")...)
	if exit != 0 || errs != "" || len(calls) != 1 {
		t.Fatalf("%d %q %v", exit, errs, calls)
	}
	if r := calls[0].r; r.Name != "The shop" || !reflect.DeepEqual(r.Tags, map[string]string{"team": "web", "env": "prod"}) {
		t.Fatalf("%+v", r)
	}
	calls, _, _ = gcpRan(t, 0, onboard...)
	if r := calls[0].r; r.Name != "" || r.Tags != nil {
		t.Fatalf("not given, not sent: %+v", r)
	}
}

func TestGcpWorkloadsOnboardRefusesABadNameOrTagBeforeAnyCall(t *testing.T) {
	onboard := []string{"gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "be"}
	for _, extra := range [][]string{
		{"--set-tag", "team"},
		{"--set-tag", "=web"},
		{"--set-tag", "team=web", "--set-tag", "team=api"},
		{"--set-tag", "dormeto:x=1"},
		{"--set-tag", "a=b#c"},
		{"--set-tag", "k=" + strings.Repeat("v", 257)},
		{"--name", strings.Repeat("n", 65)},
		{"--name", "a\nb"},
	} {
		calls, errs, exit := gcpRan(t, 0, append(slices.Clone(onboard), extra...)...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: exit %d %q calls %v", extra, exit, errs, calls)
		}
	}
	// More than 50 tags.
	extra := []string{}
	for i := range 51 {
		extra = append(extra, "--set-tag", fmt.Sprintf("k%d=v", i))
	}
	if calls, _, exit := gcpRan(t, 0, append(slices.Clone(onboard), extra...)...); exit != 2 || len(calls) != 0 {
		t.Errorf("51 tags: exit %d %v", exit, calls)
	}
}

func TestGcpWorkloadsOnboardTagIsNotAFlag(t *testing.T) {
	calls, _, exit := gcpRan(t, 0, "gcp", "workloads", "onboard", "--project", "p", "--region", "europe-west1", "--url-map", "m", "--backend-service", "be", "--tag", "team=a")
	if exit != 2 || len(calls) != 0 {
		t.Fatalf("%d %v", exit, calls)
	}
}

func TestGcpRoutersPauseAndResumeTakeDryRunAndYes(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, "gcp", "routers", "pause", "--project", "p", "--region", "europe-west1", "--reason", "x", "--dry-run", "--yes")
	if exit != 0 || errs != "" || len(calls) != 1 || !calls[0].r.DryRun || !calls[0].r.Yes {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
	calls, errs, exit = gcpRan(t, 0, "gcp", "routers", "resume", "--project", "p", "--region", "europe-west1", "--dry-run", "--yes")
	if exit != 0 || errs != "" || len(calls) != 1 || !calls[0].r.DryRun || !calls[0].r.Yes {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
}

const gcpMoved = "dormeto: \"dormeto gcp %s\" is now \"dormeto gcp routers %s\"; the old name goes in a later release.\n"

// A script written for 0.11.0 runs the old names, hidden, with one notice line and the new form's run and exit code.
func TestGcpOldInstallUninstallAndStatusRunAsTheRoutersCommandsWithOneNotice(t *testing.T) {
	for _, tc := range []struct{ old, new, run string }{{"install", "install", "install"}, {"uninstall", "uninstall", "uninstall"}, {"status", "show", "status"}} {
		for _, code := range []int{0, 1} {
			args := []string{"--project", "p", "--region", "europe-west1"}
			oldCalls, oldErr, oldExit := gcpRan(t, code, append([]string{"gcp", tc.old}, args...)...)
			newCalls, newErr, newExit := gcpRan(t, code, append([]string{"gcp", "routers", tc.new}, args...)...)
			if oldExit != code || newExit != code || len(oldCalls) != 1 || len(newCalls) != 1 || oldCalls[0].run != tc.run || !reflect.DeepEqual(oldCalls[0].r, newCalls[0].r) {
				t.Errorf("%s: old exit %d calls %+v, new exit %d calls %+v", tc.old, oldExit, oldCalls, newExit, newCalls)
			}
			if want := fmt.Sprintf(gcpMoved, tc.old, tc.new); oldErr != want || newErr != "" {
				t.Errorf("%s: old stderr %q (want %q), new %q", tc.old, oldErr, want, newErr)
			}
		}
	}
}

func TestGcpHelpOfAnOldNameSaysItAndGivesTheNewOnes(t *testing.T) {
	oldOut, oldErr, oldCode := executed(t, build{}, "help", "gcp", "install")
	newOut, newErr, newCode := executed(t, build{}, "help", "gcp", "routers", "install")
	if oldOut != newOut || oldCode != newCode || oldErr != fmt.Sprintf(gcpMoved, "install", "install")+newErr {
		t.Errorf("old %d %q %q, new %d %q %q", oldCode, oldOut, oldErr, newCode, newOut, newErr)
	}
	_, errs, _ := executed(t, build{}, "gcp", "status", "--help")
	if errs != fmt.Sprintf(gcpMoved, "status", "show") {
		t.Errorf("stderr %q", errs)
	}
}

func TestGcpHelpListsRoutersNotTheOldNamesAndSaysWhereTheManagersCommandsAre(t *testing.T) {
	out, errs, code := executed(t, build{}, "gcp", "--help")
	if code != 0 || errs != "" {
		t.Fatalf("%d %q", code, errs)
	}
	for _, old := range []string{"install", "uninstall", "status"} {
		if strings.Contains(out, "\n  "+old+" ") {
			t.Errorf("the old %q is listed:\n%s", old, out)
		}
	}
	if !strings.Contains(out, "\n  routers ") || strings.Count(out, "dormeto aws") != 1 {
		t.Errorf("routers, or one mention of dormeto aws, missing:\n%s", out)
	}
}

func TestTheGcpRoutersCommandListsInstallUninstallAndShow(t *testing.T) {
	out, _, _ := executed(t, build{}, "gcp", "routers", "--help")
	for _, want := range []string{"install", "uninstall", "show"} {
		if !strings.Contains(out, "\n  "+want+" ") {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "gcp status") {
		t.Errorf("the old name in\n%s", out)
	}
}
