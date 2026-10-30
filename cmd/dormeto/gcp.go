package main

// `dormeto gcp check`, `install` and `status`: the flags read into a gcponboard.Request and the run called with the
// real dependencies — the tokens of gcloud (its active configuration, or --gcloud-configuration), HTTPS with a 30 s
// timeout, the terminal's prompts when stdin is one, the user's cache directory ($XDG_CACHE_HOME, else ~/.cache) and
// the CLI's version. Exit codes: 0; 1 when a command lacks a permission or Google refuses; 2 for a bad flag or a missing
// answer; 128 and the signal's number when stopped.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/gcponboard"
	"github.com/Dormeto/cli/internal/install"
)

// gcpRun is one of internal/gcponboard's commands.
type gcpRun func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int

// gcpRuns are the runs of the gcp commands: internal/gcponboard's, or a test's.
type gcpRuns struct{ check, install, status gcpRun }

// theGcpRuns are internal/gcponboard's.
var theGcpRuns = gcpRuns{check: gcponboard.RunCheck, install: gcponboard.Install, status: gcponboard.RunStatus}

func newGcp(runs gcpRuns, version string) *cobra.Command {
	cmd := &cobra.Command{Use: "gcp", Short: "Dormeto in Google Cloud", Args: noArgs, SilenceErrors: true}
	cmd.AddCommand(newGcpCheck(runs.check, version), newGcpInstall(runs.install, version), newGcpStatus(runs.status, version))
	return cmd
}

// gcpFlags are the flags of the gcp commands.
type gcpFlags struct {
	project, region, image, configuration string
	json, yes, dryRun                     bool
}

// gcpCommand is a gcp command calling run with the flags' request; a bad flag, an argument or a missing required flag
// exit 2.
func gcpCommand(use, short, long string, f *gcpFlags, required []string, version string, run gcpRun) *cobra.Command {
	cmd := &cobra.Command{
		Use: use, Short: short, Long: long, Args: noArgs, SilenceErrors: true,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx, signalled, stop := interruptible(c.Context())
			defer stop()
			code := run(ctx, gcpDeps(c, f.configuration, version), gcponboard.Request{
				Project: f.project, Region: f.region, Image: f.image, Configuration: f.configuration, JSON: f.json, Yes: f.yes, DryRun: f.dryRun})
			if s := signalled(); s != nil {
				code = signalCode(s)
			}
			if code != 0 {
				return exitCode(code)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.project, "project", "", "the Google Cloud project's ID (required)")
	fl.StringVar(&f.configuration, "gcloud-configuration", "", "the gcloud configuration to use (default: the active one)")
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintln(c.ErrOrStderr(), err)
		return exitCode(2)
	})
	cmd.PreRunE = func(c *cobra.Command, _ []string) error {
		for _, name := range required {
			if v, _ := c.Flags().GetString(name); v == "" {
				fmt.Fprintf(c.ErrOrStderr(), "--%s is required\n", name)
				return exitCode(2)
			}
		}
		return nil
	}
	return cmd
}

func newGcpCheck(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	cmd := gcpCommand("check", "Check which permissions you lack for each dormeto gcp command (read-only)",
		"Check, read-only, the permissions you hold in a Google Cloud project against what each dormeto gcp command\n"+
			"needs, which of Dormeto's APIs are on, and that a billing account is linked. Nothing is changed. Exit 1 when\n"+
			"a command lacks a permission or the project has no billing.",
		&f, []string{"project"}, version, run)
	cmd.Flags().StringVar(&f.region, "region", "", "the router's region")
	cmd.Flags().BoolVar(&f.json, "json", false, "print the report as JSON")
	return cmd
}

func newGcpInstall(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	cmd := gcpCommand("install", "Install the access of a Dormeto router in a Google Cloud project and region",
		"Install what a Dormeto router dmtr-<region> needs in a Google Cloud project: the APIs, the custom roles\n"+
			"dormetoRouter and dormetoRouterBackends, the service account dmtr-<region>, its grants, the secure tag\n"+
			"dormeto=managed, the Firestore database dmtr-<region> with the install record, the Cloud Tasks queue\n"+
			"dmtr-<region>. It checks your permissions first and changes nothing when one is missing, prints the plan\n"+
			"(every IAM binding it adds), asks before applying (--yes without a terminal), prints every IAM change after\n"+
			"the steps that make one, Dormeto's and Google's, and records them. Run again, it plans only what differs.\n"+
			"It never changes or removes a binding it did not add.\n\n"+
			"With --image it also installs the router from that image, whose tag is its version: the Cloud Run services\n"+
			"dmtr-<region>-controller and dmtr-<region>-waker, the controller's invoker binding, the waker's network endpoint\n"+
			"group and the check job dmtr-<region>-check; then it checks that both services are ready and that the controller\n"+
			"answers that version. A new image changes the two services only.",
		&f, []string{"project", "region"}, version, run)
	fl := cmd.Flags()
	fl.StringVar(&f.region, "region", "", "the router's region, such as europe-west1 (required)")
	fl.StringVar(&f.image, "image", "", "the router's container image, with its version as tag: installs the router too")
	fl.BoolVar(&f.yes, "yes", false, "apply without asking (needed without a terminal)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "print the plan and change nothing")
	return cmd
}

func newGcpStatus(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	cmd := gcpCommand("status", "Show a project's Dormeto routers: their install records and live states (read-only)",
		"Show, read-only, the Dormeto router of a project and region, or every router of the project without --region:\n"+
			"what its install record says (version, image, who installed it, the bindings Google added), and the live state of\n"+
			"its Cloud Run services, the controller's health, its network endpoint group, queue, job, database and grants. Exit 1\n"+
			"when something differs from the record or there is no router.",
		&f, []string{"project"}, version, run)
	cmd.Flags().StringVar(&f.region, "region", "", "the router's region (default: every router of the project)")
	cmd.Flags().BoolVar(&f.json, "json", false, "print the report as JSON")
	return cmd
}

// gcpDeps are the real dependencies.
func gcpDeps(c *cobra.Command, configuration, version string) gcponboard.Deps {
	in, out := c.InOrStdin(), c.OutOrStdout()
	terminal := isTerminal(in)
	return gcponboard.Deps{
		Google:   &gcp.Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Tokens: gcp.NewGcloud(configuration, nil)},
		Prompter: install.NewPrompter(in, out, terminal),
		Terminal: terminal,
		Out:      out, Err: c.ErrOrStderr(),
		Now: time.Now,
		Sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
		Cache:   userCache(),
		Version: version,
	}
}

// userCache is $XDG_CACHE_HOME, else ~/.cache, on every OS (as the estimate's cache); "" when there is no home.
func userCache() string {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return x
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache")
}
