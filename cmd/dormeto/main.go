// Command dormeto is the Dormeto command line.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	cli "github.com/Dormeto/cli"
)

// Set at link time: -ldflags "-X main.version=… -X main.commit=… -X main.date=…".
var (
	version = ""
	commit  = ""
	date    = ""
)

// build is what `dormeto version` reports.
type build struct {
	version, commit, date string
	info                  *debug.BuildInfo // the module's build information, for a `go install` build
	versions              []byte           // testdata/parity/versions.json
}

func main() {
	info, _ := debug.ReadBuildInfo()
	b := build{version: version, commit: commit, date: date, info: info, versions: cli.Versions}
	os.Exit(execute(newRoot(b), os.Args[1:]...))
}

func newRoot(b build) *cobra.Command {
	return buildRoot(b, theFleetDeps(), theInstallRuns, theGcpRuns)
}

// buildRoot is the command tree with the given dependencies (a test's, or newRoot's real ones).
func buildRoot(b build, fleet fleetDeps, installs installRuns, gcps gcpRuns) *cobra.Command {
	// --version (and -v) prints the first line of `dormeto version`.
	first, _, _ := strings.Cut(b.describe(), "\n")
	root := &cobra.Command{Use: "dormeto", Short: "Dormeto command line", SilenceUsage: true, Version: first}
	root.SetVersionTemplate("{{.Version}}\n")
	root.SilenceErrors = true // execute says the error, an unknown command as the groups do
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version, commit and date, and the versions of the shared test cases",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprint(cmd.OutOrStdout(), b.describe())
		},
	})
	v, _, _ := b.resolve()
	// The AWS commands live under `dormeto aws` only.
	aws := &cobra.Command{Use: "aws", Short: "Dormeto in AWS: the manager, which serves AWS and Google Cloud, and the AWS routers", Args: noArgs, SilenceErrors: true,
		Long: "Dormeto in AWS: the manager, which serves AWS and Google Cloud, and the AWS routers. The manager's commands (routers list,\n" +
			"workloads list, discovery, savings, users, settings...) cover both clouds, a Google Cloud project being the account of\n" +
			"its rows, and change a Google Cloud router's pause, settings and workloads; the commands that install, upgrade and\n" +
			"remove a Google Cloud router are under `dormeto gcp`.\n\n" + regionRule}
	refuseUnknownSubcommands(aws)
	aws.AddCommand(awsCommands(b, fleet, installs, orDefault(v, "dev"))...)
	types := newTypes(fleet)
	sayBothClouds(types)
	aws.AddCommand(types)
	root.AddCommand(aws)
	root.AddCommand(newGcp(gcps, fleet, orDefault(v, "dev")))
	return root
}

// awsCommands are the commands of `dormeto aws`, built anew at each call.
func awsCommands(b build, fleet fleetDeps, installs installRuns, version string) []*cobra.Command {
	return append([]*cobra.Command{newEstimate(b), newInstall(installs, version), newUninstall(installs, version)}, managerCommands(fleet)...)
}

// managerCommands are the commands that call the manager's API, with deps.
func managerCommands(deps fleetDeps) []*cobra.Command {
	cmds := []*cobra.Command{newRouters(deps), newWorkloads(deps), newDiscovery(deps), newExclusions(deps), newUsers(deps), newGroups(deps), newOperations(deps), newOverview(deps), newAlerts(deps), newIPLists(deps), newSavings(deps), newAudit(deps), newManager(deps), newSettings(deps), newWhoami(deps), newAPI(deps)}
	for _, c := range cmds {
		acceptYes(c)
		acceptDryRun(c)
		sayBothClouds(c)
		aliasProject(c, "routers install")
	}
	return cmds
}

// asksNothing are the writing commands of the manager that ask no confirmation: they take --yes all the same, a no-op,
// so a script passes it to every writing command (as `dormeto gcp routers pause` does), by the path under their top command.
var asksNothing = map[string]bool{
	"discovery dismiss": true, "discovery restore": true, "discovery run": true, "exclusions lift": true, "groups create": true,
	"groups update": true, "manager check": true, "routers description": true, "routers install": true,
	"routers waiting-page set": true, "routers waiting-page clear": true, "savings discounts": true, "settings set": true,
	"settings waiting-page set": true, "settings waiting-page clear": true, "types set": true, "types delete": true,
	"users enable": true, "users groups": true, "users invite": true, "users resend": true, "users reset-mfa": true,
	"workloads description": true, "workloads idle-window": true, "workloads onboard": true,
	"workloads waiting-page set": true, "workloads waiting-page clear": true,
}

// acceptYes adds the no-op --yes to the commands of asksNothing under top.
func acceptYes(top *cobra.Command) {
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		if asksNothing[path] && c.Flags().Lookup("yes") == nil {
			c.Flags().Bool("yes", false, asksNothingYes)
		}
		for _, sub := range c.Commands() {
			walk(sub, path+" "+sub.Name())
		}
	}
	walk(top, top.Name())
}

// dryRunAnnotation marks a command whose --dry-run is its client's: every write said, none sent (fleetFlags.run).
const dryRunAnnotation = "dormeto-client-dry-run"

// withClientDryRun are the writing commands of the manager without a --dry-run of their own: those of asksNothing but
// workloads idle-window, a read, and discovery config set, which asks.
var withClientDryRun = func() map[string]bool {
	out := map[string]bool{"discovery config set": true}
	for path := range asksNothing {
		out[path] = path != "workloads idle-window"
	}
	return out
}()

// dryRunUsage is the help of every --dry-run that says each request it would send.
const dryRunUsage = "say what would be sent and stop"

// acceptDryRun adds --dry-run to the commands of withClientDryRun under top (the AWS and Google Cloud review's §4.2: --yes and
// --dry-run on every write): their client then says each write it would send and sends none.
func acceptDryRun(top *cobra.Command) {
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		if withClientDryRun[path] && c.Flags().Lookup("dry-run") == nil {
			c.Flags().Bool("dry-run", false, dryRunUsage)
			if c.Annotations == nil {
				c.Annotations = map[string]string{}
			}
			c.Annotations[dryRunAnnotation] = "true"
		}
		for _, sub := range c.Commands() {
			walk(sub, path+" "+sub.Name())
		}
	}
	walk(top, top.Name())
}

// clientDryRun tells whether the command runs its client's --dry-run.
func clientDryRun(c *cobra.Command) bool {
	if c.Annotations[dryRunAnnotation] == "" {
		return false
	}
	dry, _ := c.Flags().GetBool("dry-run")
	return dry
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// resolve is the version, commit and date: link-time values win; without them
// (a `go install` build) they come from the build information ("" when unknown).
func (b build) resolve() (v, c, d string) {
	v, c, d = b.version, b.commit, b.date
	if v == "" && b.info != nil {
		v = strings.TrimPrefix(b.info.Main.Version, "v")
		if v == "(devel)" {
			v = ""
		}
		for _, s := range b.info.Settings {
			switch {
			case s.Key == "vcs.revision" && c == "":
				c = s.Value
			case s.Key == "vcs.time" && d == "":
				d = s.Value
			}
		}
	}
	return v, c, d
}

// describe renders the version output.
func (b build) describe() string {
	v, c, d := b.resolve()
	var cases struct{ Manager, Router string }
	_ = json.Unmarshal(b.versions, &cases)
	return fmt.Sprintf("dormeto %s\ncommit: %s\ndate: %s\nmanager: %s\nrouter: %s\n",
		orDefault(v, "dev"), orDefault(c, "unknown"), orDefault(d, "unknown"),
		orDefault(cases.Manager, "none"), orDefault(cases.Router, "none"))
}

// refuseUnknownSubcommands makes a group command (aws, gcp) answer a word that is none of its commands, and the flags
// after it, as one unknown command: said on stderr, exit 2. Bare, it prints its help.
func refuseUnknownSubcommands(group *cobra.Command) {
	group.Args = cobra.ArbitraryArgs
	group.FParseErrWhitelist.UnknownFlags = true
	group.RunE = func(c *cobra.Command, args []string) error {
		if len(args) > 0 {
			fmt.Fprintf(c.ErrOrStderr(), "unknown command %q for %q\n", args[0], c.CommandPath())
			return exitCode(2)
		}
		return c.Help()
	}
}
