package main

// `dormeto aws`: every command of the manager and the install lives under it; the old top-level names are hidden, work
// as before and say once on stderr that they are going.

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// movedToAws are the commands that were at the top level before `dormeto aws`.
var movedToAws = []string{"alerts", "api", "audit", "discovery", "estimate", "exclusions", "groups", "install", "ip-lists", "manager",
	"operations", "overview", "routers", "savings", "settings", "uninstall", "users", "whoami", "workloads"}

// atTheTop are the only commands a root may show at its top level besides cobra's own; a new one goes under aws.
var atTheTop = []string{"help", "completion", "version", "gcp", "aws"}

func child(c *cobra.Command, name string) *cobra.Command {
	for _, sub := range c.Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	return nil
}

// shape is what a command is to its user: its flags and its subcommands, down the tree.
func shape(c *cobra.Command, path string, into map[string]string) {
	var flags []string
	c.LocalFlags().VisitAll(func(f *pflag.Flag) {
		flags = append(flags, f.Name+"|"+f.Shorthand+"|"+f.Value.Type()+"|"+f.DefValue+"|"+f.Usage+"|"+strings.Join(f.Annotations["cobra_annotation_bash_completion_one_required_flag"], ","))
	})
	var subs []string
	for _, sub := range c.Commands() {
		subs = append(subs, sub.Name())
		shape(sub, path+" "+sub.Name(), into)
	}
	slices.Sort(subs)
	into[path] = c.Use + "\n" + c.Short + "\n" + strings.Join(flags, "\n") + "\n" + strings.Join(subs, ",")
}

func TestEveryOldCommandIsUnderAwsAndHidden(t *testing.T) {
	root := newRoot(build{})
	aws := child(root, "aws")
	if aws == nil || aws.Short != "Dormeto in AWS" {
		t.Fatalf("aws: %+v", aws)
	}
	for _, name := range movedToAws {
		old, moved := child(root, name), child(aws, name)
		if old == nil || moved == nil {
			t.Errorf("%s: old %v, under aws %v", name, old != nil, moved != nil)
			continue
		}
		if !old.Hidden || moved.Hidden {
			t.Errorf("%s: old hidden %v, under aws hidden %v", name, old.Hidden, moved.Hidden)
		}
		if old == moved {
			t.Errorf("%s: one command for two parents", name)
		}
		a, b := map[string]string{}, map[string]string{}
		shape(old, name, a)
		shape(moved, name, b)
		if len(a) != len(b) {
			t.Errorf("%s: %d old commands, %d under aws", name, len(a), len(b))
		}
		for path, want := range a {
			if b[path] != want {
				t.Errorf("%s differs under aws:\nold  %q\nnew  %q", path, want, b[path])
			}
		}
	}
	// aws holds exactly these, and none of the old ones is left visible.
	for _, sub := range aws.Commands() {
		if !slices.Contains(movedToAws, sub.Name()) {
			t.Errorf("aws holds %q, which was not at the top level", sub.Name())
		}
	}
	for _, sub := range root.Commands() {
		switch {
		case slices.Contains(atTheTop, sub.Name()):
			if sub.Hidden {
				t.Errorf("%s is hidden", sub.Name())
			}
		case !slices.Contains(movedToAws, sub.Name()):
			t.Errorf("a new top-level command %q: it belongs under aws (or on the allow-list)", sub.Name())
		case !sub.Hidden:
			t.Errorf("the old %q is shown", sub.Name())
		}
	}
}

func TestTopLevelHelpShowsAwsGcpVersionAndCobrasOwn(t *testing.T) {
	out, errs, code := executed(t, build{}, "--help")
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, errors %q", code, errs)
	}
	for _, want := range []string{"aws ", "Dormeto in AWS", "gcp ", "version ", "completion ", "help "} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	for _, name := range movedToAws {
		if strings.Contains(out, "\n  "+name+" ") {
			t.Errorf("the old %q is listed:\n%s", name, out)
		}
	}
}

const notice = "dormeto: \"dormeto %s\" is now \"dormeto aws %s\"; the old name goes in a later release.\n"

func TestOldFormsRunAsTheNewOnesWithOneLineOnStderr(t *testing.T) {
	cases := [][]string{
		{"workloads", "schedule", "show", "--help"}, // nested, help
		{"estimate", "--help"},
		{"install", "check", "--help"},
		{"routers", "list", "--bogus"}, // usage error
		{"routers", "bogus"},
		{"api", "GET"}, // a usage error of the command
		{"help", "routers"},
		{"uninstall", "--help"},
	}
	for _, args := range cases {
		name, newArgs := args[0], append([]string{"aws"}, args...)
		if name == "help" { // dormeto help routers is dormeto help aws routers
			name, newArgs = args[1], []string{"help", "aws", args[1]}
		}
		newOut, newErr, newCode := executed(t, build{}, newArgs...)
		oldOut, oldErr, oldCode := executed(t, build{}, args...)
		if oldOut != newOut || oldCode != newCode {
			t.Errorf("%q: old exit %d out %q, new exit %d out %q", args, oldCode, oldOut, newCode, newOut)
		}
		if want := strings.Replace(notice, "%s", name, 2) + newErr; oldErr != want {
			t.Errorf("%q: old stderr %q, want %q", args, oldErr, want)
		}
		if strings.Contains(newErr, "is now") {
			t.Errorf("%q: the new form says %q", args, newErr)
		}
	}
}

func TestOldFormsWithAJSONCommandAgainstAFakeManager(t *testing.T) {
	both := func(args ...string) (o, n fleetRan) {
		for i, a := range [][]string{args, append([]string{"aws"}, args...)} {
			r := fleetRan{api: &fakeAPI{}}
			root := buildRoot(build{}, fakeFleetDeps(t, &r, "eu-west-1", nil), installRuns{}, gcpRuns{})
			got := runOn(root, r.api, &r, a)
			if i == 0 {
				o = got
			} else {
				n = got
			}
		}
		return
	}
	for _, args := range [][]string{{"routers", "list", "--json", "--profile", "ops"}, {"workloads", "list", "--json"}} {
		o, n := both(args...)
		if n.exit != 0 || n.stdout == "" || o.exit != n.exit || o.stdout != n.stdout {
			t.Errorf("%q: old exit %d out %q; new exit %d out %q err %q", args, o.exit, o.stdout, n.exit, n.stdout, n.stderr)
		}
		if o.stderr != strings.Replace(notice, "%s", args[0], 2)+n.stderr || strings.Contains(n.stderr, "is now") {
			t.Errorf("%q: old stderr %q, new %q", args, o.stderr, n.stderr)
		}
	}
}

func TestTheNoticeNamesOnlyTheTopLevelCommandAndIsHiddenFromCompletion(t *testing.T) {
	_, errs, _ := executed(t, build{}, "operations", "watch", "--help")
	if errs != strings.Replace(notice, "%s", "operations", 2) {
		t.Errorf("stderr %q", errs)
	}
	for _, name := range []string{"version", "gcp", "aws", "help"} {
		if _, errs, _ := executed(t, build{}, name, "--help"); strings.Contains(errs, "is now") {
			t.Errorf("%s: %q", name, errs)
		}
	}
	out, _, _ := executed(t, build{}, "__complete", "")
	for _, name := range movedToAws {
		if strings.Contains(out, name+"\t") {
			t.Errorf("completion offers the old %q:\n%s", name, out)
		}
	}
	if !strings.Contains(out, "aws\t") {
		t.Errorf("completion lacks aws:\n%s", out)
	}
}

// Building a command tree twice from its builder gives two trees that share nothing.
func TestTheBuildersCanBeCalledTwice(t *testing.T) {
	var a, b bytes.Buffer
	for i, buf := range []*bytes.Buffer{&a, &b} {
		root := newRoot(build{})
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"aws", "routers", "list", "--help"})
		if err := root.Execute(); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if a.String() != b.String() || a.Len() == 0 {
		t.Errorf("two roots differ:\n%s\n%s", a.String(), b.String())
	}
}
