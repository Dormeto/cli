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
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version, commit and date, and the versions of the shared test cases",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprint(cmd.OutOrStdout(), b.describe())
		},
	})
	v, _, _ := b.resolve()
	// The AWS commands live under `dormeto aws`; the old top-level names are the same commands built a second time
	// (a cobra command has one parent), hidden, and run through aws by execute with a notice.
	aws := &cobra.Command{Use: "aws", Short: "Dormeto in AWS", Args: noArgs, SilenceErrors: true}
	aws.AddCommand(awsCommands(b, fleet, installs, orDefault(v, "dev"))...)
	// Born under aws (settings at scale): no hidden twin at the top level.
	aws.AddCommand(newTypes(fleet))
	root.AddCommand(aws)
	for _, old := range awsCommands(b, fleet, installs, orDefault(v, "dev")) {
		old.Hidden = true
		old.Annotations = map[string]string{movedAnnotation: "aws"}
		root.AddCommand(old)
	}
	root.AddCommand(newGcp(gcps, orDefault(v, "dev")))
	return root
}

// awsCommands are the commands of `dormeto aws`, built anew at each call.
func awsCommands(b build, fleet fleetDeps, installs installRuns, version string) []*cobra.Command {
	return append([]*cobra.Command{newEstimate(b), newInstall(installs, version), newUninstall(installs, version)}, managerCommands(fleet)...)
}

// managerCommands are the commands that call the manager's API, with deps.
func managerCommands(deps fleetDeps) []*cobra.Command {
	return []*cobra.Command{newRouters(deps), newWorkloads(deps), newDiscovery(deps), newExclusions(deps), newUsers(deps), newGroups(deps), newOperations(deps), newOverview(deps), newAlerts(deps), newIPLists(deps), newSavings(deps), newAudit(deps), newManager(deps), newSettings(deps), newWhoami(deps), newAPI(deps)}
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
