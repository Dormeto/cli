package main

// `dormeto aws`: every command of the manager and the install lives under it; the old top-level names are gone, unknown
// commands as any other.

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// movedToAws are the commands that were at the top level before `dormeto aws`, and are not any more.
var movedToAws = []string{"alerts", "api", "audit", "discovery", "estimate", "exclusions", "groups", "install", "ip-lists", "manager",
	"operations", "overview", "routers", "savings", "settings", "uninstall", "users", "whoami", "workloads"}

// bornUnderAws are the commands added under aws since.
var bornUnderAws = []string{"types"}

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

func TestEveryOldCommandIsUnderAwsAndNotAtTheTop(t *testing.T) {
	root := newRoot(build{})
	aws := child(root, "aws")
	if aws == nil || aws.Short != awsShort {
		t.Fatalf("aws: %+v", aws)
	}
	for _, name := range movedToAws {
		if child(root, name) != nil {
			t.Errorf("%s is still at the top level", name)
		}
		if child(aws, name) == nil {
			t.Errorf("%s is not under aws", name)
		}
	}
	// aws holds exactly these.
	for _, sub := range aws.Commands() {
		if !slices.Contains(movedToAws, sub.Name()) && !slices.Contains(bornUnderAws, sub.Name()) {
			t.Errorf("aws holds %q, which was not at the top level", sub.Name())
		}
	}
	for _, sub := range root.Commands() {
		if !slices.Contains(atTheTop, sub.Name()) {
			t.Errorf("a new top-level command %q: it belongs under aws (or on the allow-list)", sub.Name())
		}
		if sub.Hidden {
			t.Errorf("%s is hidden", sub.Name())
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

// The old top-level names answer as cobra does for any unknown command, with no notice and nothing run.
func TestOldTopLevelFormsAreUnknownCommands(t *testing.T) {
	for _, args := range [][]string{
		{"workloads", "schedule", "show", "--help"},
		{"estimate", "--help"},
		{"install", "check", "--help"},
		{"routers", "list", "--bogus"},
		{"api", "GET"},
		{"help", "routers"},
		{"uninstall", "--help"},
		{"operations", "watch"},
	} {
		out, errs, code := executed(t, build{}, args...)
		if args[0] == "help" { // cobra's own answer to a help topic it does not have
			if code != 0 || !strings.Contains(out, "Unknown help topic") || errs != "" {
				t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, out, errs)
			}
			continue
		}
		if code == 0 || out != "" || !strings.Contains(errs, "unknown command") || strings.Contains(errs, "is now") {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, out, errs)
		}
	}
	for _, name := range movedToAws {
		if _, errs, code := executed(t, build{}, name); code == 0 || !strings.Contains(errs, "unknown command") {
			t.Errorf("%s: exit %d, stderr %q", name, code, errs)
		}
	}
	// A word that is no command of aws or gcp is refused the same: exit 2, nothing run.
	for _, args := range [][]string{{"aws", "bogus"}, {"aws", "bogus", "--profile", "p"}, {"gcp", "bogus"}} {
		if out, errs, code := executed(t, build{}, args...); code != 2 || out != "" || errs != fmt.Sprintf("unknown command \"bogus\" for \"dormeto %s\"\n", args[0]) {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, out, errs)
		}
	}
	// Bare, each prints its help, exit 0.
	for _, name := range []string{"aws", "gcp"} {
		if out, errs, code := executed(t, build{}, name); code != 0 || errs != "" || !strings.Contains(out, "Available Commands:") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, code, out, errs)
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

// The manager's writing commands all take --yes (a no-op where they ask nothing), so a script can pass it to each.
func TestEveryWritingAwsCommandTakesYes(t *testing.T) {
	root := newRoot(build{})
	for _, path := range [][]string{
		{"types", "set"}, {"types", "delete"}, {"users", "groups"}, {"users", "invite"}, {"users", "enable"}, {"users", "resend"},
		{"users", "reset-mfa"}, {"users", "role"}, {"users", "disable"}, {"groups", "create"}, {"groups", "update"}, {"groups", "delete"},
		{"discovery", "dismiss"}, {"discovery", "restore"}, {"discovery", "run"}, {"discovery", "config", "set"}, {"exclusions", "add"},
		{"exclusions", "lift"}, {"manager", "check"}, {"manager", "update"}, {"routers", "description"}, {"routers", "install"},
		{"routers", "waiting-page", "set"}, {"routers", "waiting-page", "clear"}, {"savings", "discounts"}, {"settings", "set"},
		{"settings", "waiting-page", "set"}, {"settings", "waiting-page", "clear"}, {"workloads", "description"},
		{"workloads", "idle-window"}, {"workloads", "onboard"}, {"workloads", "waiting-page", "set"}, {"workloads", "waiting-page", "clear"},
	} {
		c, _, err := root.Find(append([]string{"aws"}, path...))
		if err != nil || c.Flags().Lookup("yes") == nil {
			t.Errorf("aws %s: no --yes (%v)", strings.Join(path, " "), err)
		}
	}
}

func TestAwsTypesSetAcceptsYes(t *testing.T) {
	_, errs, _ := executed(t, build{}, "aws", "types", "set", "--yes")
	if strings.Contains(errs, "unknown flag") {
		t.Fatal(errs)
	}
}

func TestAwsWorkloadsSettingsHelpSaysWhatHibernateDoesOnEachCloud(t *testing.T) {
	out, _, code := executed(t, build{}, "aws", "workloads", "settings", "--help")
	out = strings.Join(strings.Fields(out), " ")
	for _, want := range []string{"AWS: EC2 instances launched with hibernation are hibernated, not stopped", "Google Cloud: VMs are suspended, not stopped"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("exit %d, no %q:\n%s", code, want, out)
		}
	}
	if strings.Contains(out, "Set whether the EC2 instances") {
		t.Fatalf("still says EC2 only:\n%s", out)
	}
}

const awsShort = "Dormeto in AWS: the manager, which serves AWS and Google Cloud, and the AWS routers"

// The commands of the manager that print both clouds say so in their help.
func TestTheManagersCommandsThatPrintBothCloudsNameGoogleCloudInTheirHelp(t *testing.T) {
	aws := child(newRoot(build{}), "aws")
	for _, path := range []string{"routers list", "routers show", "workloads list", "workloads show", "discovery candidates", "discovery access", "settings", "types", "savings",
		"workloads keep-awake", "workloads resume", "workloads nap", "workloads wake", "workloads offboard"} {
		c := aws
		for _, name := range strings.Fields(path) {
			if c = child(c, name); c == nil {
				t.Fatalf("no aws %s", path)
			}
		}
		if !strings.Contains(c.Short+" "+c.Long, "Google Cloud") {
			t.Errorf("aws %s: its help does not name Google Cloud:\n%s\n%s", path, c.Short, c.Long)
		}
	}
}

// The manager's commands change a Google Cloud router too (its pause, settings, workloads): only its install, upgrade and
// removal are under dormeto gcp, which aws's help says.
func TestAwsHelpSaysWhichGoogleCloudRouterCommandsAreUnderGcp(t *testing.T) {
	out, _, code := executed(t, build{}, "aws", "--help")
	out = strings.Join(strings.Fields(out), " ")
	if code != 0 || !strings.Contains(out, "the commands that install, upgrade and remove a Google Cloud router are under `dormeto gcp`") ||
		strings.Contains(out, "the commands that change a Google Cloud router are under") {
		t.Errorf("%d\n%s", code, out)
	}
}

func TestBothSubtreesHelpSayWhatRegionIs(t *testing.T) {
	for _, args := range [][]string{{"gcp", "--help"}, {"aws", "--help"}} {
		out, _, code := executed(t, build{}, args...)
		if code != 0 || !strings.Contains(strings.Join(strings.Fields(out), " "), "--region: on the manager's commands it is the manager's own region") {
			t.Errorf("%v: %d\n%s", args, code, out)
		}
	}
}

// --project is --account on the manager's commands that take --account; the others do not have it.
func TestProjectIsAnAliasOfAccountOnTheManagersCommands(t *testing.T) {
	aws := child(newRoot(build{}), "aws")
	for _, path := range []string{"routers list", "routers upgrade", "discovery candidates", "exclusions add"} {
		c := aws
		for _, name := range strings.Fields(path) {
			c = child(c, name)
		}
		account, project := c.Flags().Lookup("account"), c.Flags().Lookup("project")
		if account == nil || project == nil || account.Value != project.Value {
			t.Errorf("aws %s: --account %v, --project %v", path, account != nil, project != nil)
		}
	}
	if f := child(child(aws, "routers"), "install").Flags().Lookup("project"); f != nil {
		t.Errorf("routers install takes an AWS account only, yet has --project")
	}
}
