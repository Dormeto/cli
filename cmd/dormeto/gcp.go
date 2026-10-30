package main

// `dormeto gcp check`, `install`, `status`, `uninstall` and `workloads` (gcp_workloads.go): the flags read into a gcponboard.Request and the run called with the
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
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/gcp/access"
	"github.com/Dormeto/cli/internal/gcponboard"
	"github.com/Dormeto/cli/internal/install"
)

// gcpRun is one of internal/gcponboard's commands.
type gcpRun func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int

// gcpRuns are the runs of the gcp commands: internal/gcponboard's, or a test's.
type gcpRuns struct {
	check, install, status, uninstall gcpRun
	workloads                         gcpWorkloadRuns
}

// theGcpRuns are internal/gcponboard's.
var theGcpRuns = gcpRuns{check: gcponboard.RunCheck, install: gcponboard.Install, status: gcponboard.RunStatus, uninstall: gcponboard.Uninstall, workloads: theGcpWorkloadRuns}

func newGcp(runs gcpRuns, version string) *cobra.Command {
	cmd := &cobra.Command{Use: "gcp", Short: "Dormeto in Google Cloud", Args: noArgs, SilenceErrors: true}
	cmd.AddCommand(newGcpCheck(runs.check, version), newGcpInstall(runs.install, version), newGcpStatus(runs.status, version), newGcpUninstall(runs.uninstall, version), newGcpWorkloads(runs.workloads, version))
	return cmd
}

// gcpFlags are the flags of the gcp commands.
type gcpFlags struct {
	project, region, image, configuration string
	json, yes, dryRun, force              bool
	with                                  []string
	ipLists                               []string
	noIPLists                             bool
}

// gcpCommand is a gcp command calling run with the flags' request; a bad flag, an argument or a missing required flag
// exit 2.
func gcpCommand(use, short, long string, f *gcpFlags, required []string, version string, run gcpRun) *cobra.Command {
	cmd := &cobra.Command{
		Use: use, Short: short, Long: long, Args: noArgs, SilenceErrors: true,
		RunE: func(c *cobra.Command, _ []string) error {
			var lists []gcponboard.IPList
			for _, s := range f.ipLists {
				l, err := gcponboard.ParseIPList(s)
				if err != nil {
					fmt.Fprintln(c.ErrOrStderr(), err)
					return exitCode(2)
				}
				lists = append(lists, l)
			}
			if err := gcponboard.CheckIPLists(lists, f.noIPLists); err != nil {
				fmt.Fprintln(c.ErrOrStderr(), err)
				return exitCode(2)
			}
			ctx, signalled, stop := interruptible(c.Context())
			defer stop()
			code := run(ctx, gcpDeps(c, f.configuration, version), gcponboard.Request{
				Project: f.project, Region: f.region, Image: f.image, Configuration: f.configuration, JSON: f.json, Yes: f.yes, DryRun: f.dryRun, Force: f.force, With: f.with,
				IPLists: lists, NoIPLists: f.noIPLists})
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
	withFlag(cmd, &f, "also test the permissions and APIs of these optional features (those of the install record are tested too with --region)")
	return cmd
}

// withFlag is --with of check, install and uninstall: the optional access, comma-separated or repeated.
func withFlag(cmd *cobra.Command, f *gcpFlags, usage string) {
	cmd.Flags().StringSliceVar(&f.with, "with", nil, usage+": "+strings.Join(access.FeatureNames(), ", "))
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
			"answers that version. A new image changes the two services only.\n\n"+
			"With --with cloud-sql,gke it also installs the optional access of those features: their APIs, their custom\n"+
			"roles (dormetoRouterDatabasesRead and dormetoRouterDatabases; dormetoRouterKubernetes) and the router's grants,\n"+
			"kept in the install record. Without it, neither API is turned on (Google then adds service agents of its own). A\n"+
			"later run with more features adds them; a run without --with keeps the record's; only uninstall removes them.\n\n"+
			"With --ip-list name=<n>,url=<u>[,refresh=<hours>] (repeatable) the router ignores the addresses of those lists: it\n"+
			"downloads each over http(s), every 24 hours unless refresh says otherwise, and an address on one never wakes a\n"+
			"workload by a visit (only the action ignore exists on Google Cloud; an API client's explicit POST /_dormeto/wake\n"+
			"still wakes). The lists go in DMT_IP_LISTS on both Cloud Run services and in the install record, so the URL is\n"+
			"printed in the plan, and one with credentials is refused. The set is replaced by each run with --ip-list, cleared by\n"+
			"--no-ip-lists, and kept by a run with neither (a later --image included). Either flag changes the two services, so it\n"+
			"needs the router: without --image the recorded image is reused. The waker runs at 512 MiB with a list, else 256 MiB.",
		&f, []string{"project", "region"}, version, run)
	fl := cmd.Flags()
	fl.StringVar(&f.region, "region", "", "the router's region, such as europe-west1 (required)")
	fl.StringVar(&f.image, "image", "", "the router's container image, with its version as tag: installs the router too")
	fl.BoolVar(&f.yes, "yes", false, "apply without asking (needed without a terminal)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "print the plan and change nothing")
	fl.StringArrayVar(&f.ipLists, "ip-list", nil, "an IP list the router ignores: name=<n>,url=<http(s) URL>[,refresh=<hours, default 24>], repeatable; replaces the set; needs the router (the recorded image is reused without --image)")
	fl.BoolVar(&f.noIPLists, "no-ip-lists", false, "clear the router's IP lists")
	withFlag(cmd, &f, "also install the optional access of these features (a later run adds more and a run without --with keeps the record's; only uninstall removes them)")
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

func newGcpUninstall(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	cmd := gcpCommand("uninstall", "Remove what dormeto gcp install added for a router in a Google Cloud project and region",
		"Remove exactly what dormeto gcp install added for the router dmtr-<region>, from its install record: the check job,\n"+
			"the waker's network endpoint group, the Cloud Run services, the queue, the database with the record, the grants it\n"+
			"added to the project (never a binding that existed before it, nor someone else's member of a role), the secure tag,\n"+
			"the service account and the custom roles dormetoRouter and dormetoRouterBackends. The roles and the tag stay while\n"+
			"another router's record in the project lists them. It checks your permissions first, refuses while workloads are\n"+
			"onboarded (--force offboards each one first, and checks the offboard permissions too), prints the plan, asks before\n"+
			"applying (--yes without a terminal) and removes in reverse order. It turns no API off and lists the APIs and the\n"+
			"bindings Google added as left in place. Run again after a stop, it continues.",
		&f, []string{"project", "region"}, version, run)
	fl := cmd.Flags()
	fl.StringVar(&f.region, "region", "", "the router's region, such as europe-west1 (required)")
	fl.BoolVar(&f.yes, "yes", false, "apply without asking (needed without a terminal)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "print the plan and change nothing")
	fl.BoolVar(&f.force, "force", false, "offboard the onboarded workloads first")
	withFlag(cmd, &f, "only validated and, with --force, tests these features' offboard permissions too: the uninstall always removes every feature the install record lists and tests theirs")
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
