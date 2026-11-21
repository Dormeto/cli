package main

// `dormeto gcp schedule …`: the flags read into the request, the same flags and checks as `dormeto aws schedule`'s, and the
// same words in both subtrees for the same schedule.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	cli "github.com/Dormeto/cli"
	"github.com/Dormeto/cli/internal/gcp/gcptest"
	"github.com/Dormeto/cli/internal/gcponboard"
	"github.com/Dormeto/cli/internal/schedule"
)

var gcpWhere = []string{"--project", "p", "--region", "europe-west1"}

func TestGcpScheduleShowSetClearReadTheRequest(t *testing.T) {
	file := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(file, []byte(`{"timeZone":"UTC"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"show", "set", "clear"} {
		args := slices.Concat([]string{"gcp", "schedule", verb, "shop"}, gcpWhere, []string{"--json"})
		want := gcponboard.Request{Project: "p", Region: "europe-west1", ID: "shop", JSON: true}
		if verb == "set" {
			args = append(args, "--file", file)
			want.Schedule = json.RawMessage(`{"timeZone":"UTC"}`)
		}
		calls, errs, exit := gcpRan(t, 0, args...)
		if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "schedule "+verb || !reflect.DeepEqual(calls[0].r, want) {
			t.Fatalf("%s: %d %q %+v", verb, exit, errs, calls)
		}
	}
	for _, args := range [][]string{
		slices.Concat([]string{"gcp", "schedule", "set", "shop"}, gcpWhere),
		slices.Concat([]string{"gcp", "schedule", "show"}, gcpWhere),
		slices.Concat([]string{"gcp", "schedule", "show", "a", "b"}, gcpWhere),
	} {
		calls, errs, exit := gcpRan(t, 0, args...)
		if exit != 2 || len(calls) != 0 || errs == "" {
			t.Errorf("%v: exit %d calls %v stderr %q", args, exit, calls, errs)
		}
	}
}

func TestGcpScheduleListReadsItsFilters(t *testing.T) {
	calls, errs, exit := gcpRan(t, 0, slices.Concat([]string{"gcp", "schedule", "list"}, gcpWhere, []string{"--all", "--state", "asleep", "--tag", "team=shop", "--search", "sh", "--json"})...)
	want := gcponboard.Request{Project: "p", Region: "europe-west1", All: true, States: []string{"ASLEEP"}, HasTags: map[string]string{"team": "shop"}, Search: "sh", JSON: true}
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "schedule list" || !reflect.DeepEqual(calls[0].r, want) {
		t.Fatalf("%d %q %+v", exit, errs, calls)
	}
}

// Each change's flags give the same change as AWS's, in the request.
func TestGcpScheduleChangesReadTheirFlags(t *testing.T) {
	two, last := 2, -1
	for name, c := range map[string]struct {
		args []string
		want schedule.Change
	}{
		"add": {[]string{"add", "shop", "--kind", "awake", "--repeat", "monthly", "--interval", "2", "--nth", "-1", "--weekday", "fri", "--start", "08:00",
			"--end", "18:00", "--from", "2027-01-01", "--time-zone", "Europe/Paris"},
			schedule.Change{Add: &schedule.NewWindow{Kind: "awake", Start: "08:00", End: "18:00", From: "2027-01-01",
				Repeat: &schedule.NewRepeat{Pattern: "monthly", Interval: &two, Nth: &last, Weekday: "fri"}}, TimeZone: "Europe/Paris"}},
		"add weekly": {[]string{"add", "shop", "--kind", "nap", "--days", "sat,sun", "--start", "00:00", "--end", "00:00", "--wake", "header"},
			schedule.Change{Add: &schedule.NewWindow{Kind: "nap", Days: []string{"sat", "sun"}, Start: "00:00", End: "00:00", Wake: "header"}}},
		"remove":       {[]string{"remove", "shop", "--window", "2"}, schedule.Change{Remove: 2}},
		"except":       {[]string{"except", "shop", "--date", "2026-12-25", "--date", "2027-01-01", "--window", "1", "--remove"}, schedule.Change{Except: &schedule.Exceptions{Dates: []string{"2026-12-25", "2027-01-01"}, Window: 1, Remove: true}}},
		"edit":         {[]string{"edit", "shop", "--time-zone", "UTC", "--join-minutes", "inherit", "--max-nap-days", "14"}, schedule.Change{Fields: map[string]json.RawMessage{"timeZone": json.RawMessage(`"UTC"`), "joinMinutes": nil, "maxNapDays": json.RawMessage("14")}}},
		"edit removes": {[]string{"edit", "shop", "--max-nap-days", ""}, schedule.Change{Fields: map[string]json.RawMessage{"maxNapDays": nil}}},
	} {
		calls, errs, exit := gcpRan(t, 0, slices.Concat([]string{"gcp", "schedule"}, c.args, gcpWhere)...)
		if exit != 0 || errs != "" || len(calls) != 1 || calls[0].run != "schedule change" || calls[0].r.ID != "shop" || calls[0].r.ScheduleChange == nil {
			t.Errorf("%s: %d %q %+v", name, exit, errs, calls)
			continue
		}
		if got := *calls[0].r.ScheduleChange; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: change %+v, want %+v", name, got, c.want)
		}
	}
}

// A flag of the wrong form exits 2 with AWS's words, before any call.
func TestGcpScheduleFlagsOfTheWrongFormExit2AsAws(t *testing.T) {
	for name, args := range map[string][]string{
		"add no kind":            {"add", "shop", "--days", "mon"},
		"add bad start":          {"add", "shop", "--kind", "nap", "--start", "8:00"},
		"add interval no repeat": {"add", "shop", "--kind", "nap", "--interval", "2"},
		"remove no window":       {"remove", "shop"},
		"except bad date":        {"except", "shop", "--date", "25/12"},
		"edit nothing":           {"edit", "shop"},
		"edit bad join":          {"edit", "shop", "--join-minutes", "soon"},
	} {
		calls, errs, exit := gcpRan(t, 0, slices.Concat([]string{"gcp", "schedule"}, args, gcpWhere)...)
		r := runSC(t, &scAPI{}, slices.Concat(args[:1], []string{"prod-eu/w-1"}, args[2:])...)
		if exit != 2 || len(calls) != 0 || errs == "" || errs != r.stderr {
			t.Errorf("%s: exit %d, calls %v, stderr %q, AWS's %q", name, exit, calls, errs, r.stderr)
		}
	}
	// One workload on Google Cloud, whose commands name one by their argument (AWS's add takes several).
	calls, _, exit := gcpRan(t, 0, slices.Concat([]string{"gcp", "schedule", "add", "shop", "api", "--kind", "nap"}, gcpWhere)...)
	if exit != 2 || len(calls) != 0 {
		t.Errorf("two workloads: exit %d, calls %v", exit, calls)
	}
}

// The same flags in both subtrees, the same help for each.
func TestScheduleFlagsAreTheSameInBothSubtrees(t *testing.T) {
	root := newRoot(build{})
	for _, verb := range []string{"list", "show", "set", "clear", "add", "remove", "except", "edit"} {
		aws, _, err := root.Find([]string{"aws", "schedule", verb})
		if err != nil || aws.Name() != verb {
			t.Fatalf("aws schedule %s: %v", verb, err)
		}
		gcp, _, err := root.Find([]string{"gcp", "schedule", verb})
		if err != nil || gcp.Name() != verb {
			t.Fatalf("gcp schedule %s: %v", verb, err)
		}
		// AWS's own: where the manager is, the targets by tag, the manager's filters; Google Cloud's: where the router is.
		awsOnly := []string{"tag", "router", "cloud", "state", "search"}
		gcpOnly := []string{"project", "region", "gcloud-configuration", "state", "tag", "search"}
		af, gf := flagUsages(aws, awsOnly), flagUsages(gcp, gcpOnly)
		for name, u := range af {
			// --json and --yes say each subtree's answers; set's --file is today's.
			if g, ok := gf[name]; !ok || (g != u && name != "json" && name != "yes" && !(verb == "set" && name == "file")) {
				t.Errorf("schedule %s --%s: aws %q, gcp %q", verb, name, u, g)
			}
		}
		for name := range gf {
			if _, ok := af[name]; !ok {
				t.Errorf("schedule %s --%s: gcp only", verb, name)
			}
		}
	}
	// workloads schedule is gone from both.
	for _, cloud := range []string{"aws", "gcp"} {
		if c, _, _ := root.Find([]string{cloud, "workloads", "schedule"}); c.Name() == "schedule" {
			t.Errorf("%s workloads schedule is still there", cloud)
		}
	}
}

// For the same schedule and next events, both subtrees print the same words after their targets.
func TestScheduleChangesPrintTheSameWordsInBothSubtrees(t *testing.T) {
	// The router version the CLI embeds, whatever the release: the samples' fake Google has its image.
	versions := cli.Versions
	cli.Versions = []byte(sampleVersion)
	t.Cleanup(func() { cli.Versions = versions })
	answers := sampleAnswers(t) // before the lines run in directories of their own
	stored := `{"timeZone":"Europe/Paris","windows":[{"kind":"awake","days":["mon","tue"],"start":"08:00","end":"19:00"}],"maxNapDays":7}`
	var asMap map[string]any
	if err := json.Unmarshal([]byte(stored), &asMap); err != nil {
		t.Fatal(err)
	}
	google := func(t *testing.T, g *gcptest.Server, _ gcponboard.Deps) {
		g.Lock()
		defer g.Unlock()
		g.Schedules["shop"] = asMap
		var next []any
		_ = json.Unmarshal([]byte(scNext), &next)
		g.NextEvents = next
	}
	for _, args := range [][]string{
		{"add", "--kind", "nap", "--days", "sat", "--start", "00:00", "--end", "00:00", "--wake", "header"},
		{"remove", "--window", "1"},
		{"except", "--date", "2026-12-25"},
		{"edit", "--max-nap-days", "14"},
		{"show"},
		{"list"},
	} {
		a := &scAPI{schedules: map[string]string{"prod-eu/w-1": stored}}
		awsArgs := slices.Concat([]string{args[0]}, []string{"prod-eu/w-1"}, args[1:])
		if args[0] == "list" {
			awsArgs = []string{"list", "--cloud", "aws"}
		}
		aws := runSC(t, a, awsArgs...)
		gcpLine := "dormeto " + strings.Join(slices.Concat([]string{"gcp", "schedule", args[0], "shop"}, args[1:], []string{"--project", "my-project", "--region", "europe-west1"}), " ")
		if args[0] == "list" {
			gcpLine = "dormeto gcp schedule list --project my-project --region europe-west1"
		}
		gout, gerr, gexit := runGcpLine(t, gcpLine, answers, google)
		if aws.exit != 0 || gexit != 0 {
			t.Fatalf("%v: aws exit %d %q, gcp exit %d %q", args, aws.exit, aws.stderr, gexit, gerr)
		}
		words := func(out, target string) string { return strings.ReplaceAll(out, target, "<target>") }
		if args[0] == "list" { // the rows differ by router, workload and state; the zone, windows and next event are the same
			cut := func(out string) string {
				_, row, _ := strings.Cut(out, "\n")
				return strings.Join(strings.Fields(row[strings.Index(row, "Europe/Paris"):]), " ")
			}
			if cut(aws.stdout) != cut(gout) {
				t.Errorf("list: aws %q, gcp %q", aws.stdout, gout)
			}
			continue
		}
		if words(aws.stdout, "prod-eu/w-1") != words(gout, "dmtr-my-project-europe-west1/shop") {
			t.Errorf("%v:\naws:\n%s\ngcp:\n%s", args, aws.stdout, gout)
		}
	}
}

// flagUsages are the command's own flags and their help, those named in skip left out.
func flagUsages(c *cobra.Command, skip []string) map[string]string {
	out := map[string]string{}
	c.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if !slices.Contains(skip, f.Name) && f.Name != "help" {
			out[f.Name] = f.Usage
		}
	})
	return out
}

// runGcpLine runs a dormeto gcp line against the samples' fake Google, google adding what it holds.
func runGcpLine(t *testing.T, line string, answers map[string]json.RawMessage, google func(*testing.T, *gcptest.Server, gcponboard.Deps)) (stdout, stderr string, exit int) {
	t.Helper()
	args := splitLine(line)[1:]
	command, _ := commandOf(t, args)
	stdout, stderr, exit, _ = runSample(t, sample{line: line, google: google}, command, answers, args)
	return stdout, stderr, exit
}
