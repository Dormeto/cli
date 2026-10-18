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
	os.Exit(execute(newRoot(b)))
}

func newRoot(b build) *cobra.Command {
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
	root.AddCommand(newEstimate(b))
	v, _, _ := b.resolve()
	root.AddCommand(newInstall(theInstallRuns, orDefault(v, "dev")), newUninstall(theInstallRuns, orDefault(v, "dev")))
	return root
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
