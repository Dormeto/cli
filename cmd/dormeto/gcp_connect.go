package main

// `dormeto gcp connect` and `disconnect` (Google Cloud manager design §4.3): the manager found as `dormeto aws`'s fleet
// commands find it (--profile, --env, --api-url, and --manager-region for their --region, which every dormeto gcp command
// uses for a Google Cloud region), then internal/gcponboard's runs with the real Google dependencies. The manager says its
// version (/health), its AWS account (the caller's ARN, /me: the API admits IAM callers of its own account only), its
// environment (/update), and the date of its last disconnect of a host (its audit). Exit codes as `dormeto gcp routers install`.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/gcponboard"
)

// gcpConnectRun is connect's or disconnect's run: internal/gcponboard's, or a test's.
type gcpConnectRun func(ctx context.Context, d gcponboard.Deps, r gcponboard.ConnectRequest) int

// connectedManager is the manager's /iam API as connect and disconnect use it.
type connectedManager struct{ api *fleet.Client }

func (m connectedManager) About(ctx context.Context) (gcponboard.ManagerInfo, error) {
	version, err := managerVersion(ctx, m.api)
	if err != nil {
		return gcponboard.ManagerInfo{}, err
	}
	var me struct {
		Email string `json:"email"`
	}
	if err := m.api.Get(ctx, "/me", nil, &me); err != nil {
		return gcponboard.ManagerInfo{}, err
	}
	// arn:aws:<service>::<account>:<resource>
	parts := strings.SplitN(me.Email, ":", 6)
	account := ""
	if len(parts) == 6 && parts[0] == "arn" {
		account = parts[4]
	}
	var update struct {
		Env string `json:"env"`
	}
	if err := m.api.Get(ctx, "/update", nil, &update); err != nil {
		return gcponboard.ManagerInfo{}, fmt.Errorf("the manager's environment is not read: %w", err)
	}
	return gcponboard.ManagerInfo{Version: version, Account: account, Env: update.Env, URL: m.api.BaseURL()}, nil
}

func (m connectedManager) GoogleConnection(ctx context.Context) (*fleet.GoogleConnection, error) {
	return m.api.GoogleConnection(ctx)
}

func (m connectedManager) PutGoogleConnection(ctx context.Context, rec fleet.GoogleConnection) (*fleet.GoogleConnection, error) {
	return m.api.PutGoogleConnection(ctx, rec)
}

func (m connectedManager) DeleteGoogleConnection(ctx context.Context) error {
	return m.api.DeleteGoogleConnection(ctx)
}

// Disconnected is the time of the newest disconnect of host in the manager's audit (its last 200 entries, 90 days).
func (m connectedManager) Disconnects(ctx context.Context, host string) ([]time.Time, error) {
	var out struct {
		Entries []struct {
			At     string `json:"at"`
			Action string `json:"action"`
			Target string `json:"target"`
			Result string `json:"result"`
		} `json:"entries"`
	}
	if err := m.api.Get(ctx, "/audit", nil, &out); err != nil {
		return nil, err
	}
	var all []time.Time
	for _, e := range out.Entries {
		if e.Action != "google-connection" || e.Target != host || !strings.HasPrefix(e.Result, "disconnected") {
			continue
		}
		if at, err := time.Parse(time.RFC3339, e.At); err == nil {
			all = append(all, at)
		}
	}
	slices.SortFunc(all, func(a, b time.Time) int { return b.Compare(a) })
	return all, nil
}

// gcpConnectFlags are the flags of connect and disconnect.
type gcpConnectFlags struct {
	project, configuration string
	scopes                 []string
	manager                fleetFlags
	yes, dryRun, json      bool
	deleteRoles            bool // disconnect's --delete-roles
}

// gcpConnectCommand is connect or disconnect: the flags' checks (a bad one exits 2), the manager's API found, then run.
func gcpConnectCommand(use, short, long string, f *gcpConnectFlags, deps fleetDeps, version string, run gcpConnectRun) *cobra.Command {
	cmd := &cobra.Command{
		Use: use, Short: short, Long: long, Args: noArgs, SilenceErrors: true, SilenceUsage: true,
		RunE: func(c *cobra.Command, _ []string) error {
			if f.project == "" {
				fmt.Fprintln(c.ErrOrStderr(), "--project is required")
				return exitCode(2)
			}
			if err := f.manager.check(); err != nil {
				return finish(c, err)
			}
			ctx, signalled, stop := interruptible(c.Context())
			defer stop()
			api, err := f.manager.connect(ctx, deps)
			if s := signalled(); s != nil {
				return exitCode(signalCode(s))
			}
			if err != nil {
				return finish(c, err)
			}
			r := gcponboard.ConnectRequest{Project: f.project, Scopes: f.scopes, Configuration: f.configuration, Profile: f.manager.profile,
				Env: f.manager.env, ManagerRegion: f.manager.region, APIURL: f.manager.apiURL, Yes: f.yes, DryRun: f.dryRun, JSON: f.json,
				DeleteRoles: f.deleteRoles, Manager: connectedManager{api}}
			code := run(ctx, gcpDeps(c, f.configuration, version), r)
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
	fl.StringVar(&f.project, "project", "", "the host project, where the federation and the manager's two service accounts live (required)")
	fl.StringVar(&f.configuration, "gcloud-configuration", "", "the gcloud configuration to use (default: the active one)")
	fl.StringVar(&f.manager.profile, "profile", "", "the AWS profile that reaches the manager (default: the SDK's)")
	fl.StringVar(&f.manager.region, "manager-region", "", "the manager's AWS region (default: the profile's); --region is a Google Cloud region in dormeto gcp")
	bindManager(fl, &f.manager.env, &f.manager.apiURL)
	fl.BoolVar(&f.yes, "yes", false, yesAlways)
	fl.BoolVar(&f.dryRun, "dry-run", false, "print the plan and change nothing")
	fl.BoolVar(&f.json, "json", false, "print the manager's record as JSON on stdout ({\"connection\": …}; a dry run's {\"dryRun\": true, \"targets\": [<host project>], \"plan\": […]}), the words on stderr")
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintln(c.ErrOrStderr(), err)
		return exitCode(2)
	})
	return cmd
}

func newGcpConnect(run gcpConnectRun, deps fleetDeps, version string) *cobra.Command {
	var f gcpConnectFlags
	cmd := gcpConnectCommand("connect", "Let the Dormeto manager reach Google Cloud: workload identity federation to two service accounts",
		"Let the Dormeto manager (in AWS) reach Google Cloud as two service accounts of a host project, dmt-<env>-discovery\n"+
			"(read only, at the scopes) and dmt-<env>-command (invokes the routers' controllers and binds their tag), through\n"+
			"workload identity federation from its AWS roles. The manager is found as dormeto aws's fleet commands find it\n"+
			"(--profile, --env, --manager-region, --api-url) and read first: its version (0.24.0 or later), its AWS account and its\n"+
			"environment, which name the roles the trust admits. Then, as dormeto gcp routers install: your permissions are tested at the\n"+
			"host and at each scope (a missing one stops the run before any change), the routers at the scopes are found (their\n"+
			"Cloud Run services dmtr-<region>-controller), and the plan is printed: the APIs iam, iamcredentials, sts,\n"+
			"cloudresourcemanager and cloudbilling in the host; the custom roles dormetoDiscovery (in each project scope, or in the\n"+
			"organization of a folder or organization scope) and dormetoCommand (in each router project); the workload identity\n"+
			"pool dmt-<env> and its provider aws, whose condition admits only the manager's roles dmt-<env>-manager-*; the two\n"+
			"service accounts; roles/iam.workloadIdentityUser on each to the principal set of each manager role that acts as it;\n"+
			"dormetoDiscovery to the discovery account at each scope; and for each router, dormetoCommand on its project,\n"+
			"roles/run.invoker on its controller and roles/resourcemanager.tagUser on its tag value <project>/dormeto/managed. It\n"+
			"asks before applying (--yes without a terminal), prints every IAM change, Dormeto's and Google's, verifies, and writes\n"+
			"the connection's record in the manager, which registers its routers at once. Run again after installing a router, it\n"+
			"plans only that router's grants; with nothing new it says \"up to date\". A router of the record no longer found has\n"+
			"its project's command grants taken back.\n\n"+
			"--scope is organizations/<n>, folders/<n> or projects/<id>, repeatable; the default is the host project. A later run\n"+
			"keeps the recorded scopes and adds the ones given: to drop a scope, disconnect and connect again. What Google keeps\n"+
			"after a disconnect is undeleted: the pool and its provider (30 days). The custom roles a disconnect left are updated to\n"+
			"this CLI's definition and reused; ones deleted with --delete-roles are undeleted within 7 days, and a custom role's ID\n"+
			"that Google blocks without an undelete (up to 44 days after the delete) stops the run with the date it frees. The manager never changes an IAM policy\n"+
			"itself: every change is this command's.",
		&f, deps, version, run)
	cmd.Flags().StringArrayVar(&f.scopes, "scope", nil, "what discovery sees: organizations/<n>, folders/<n> or projects/<id> (repeatable; default the host project; a later run adds to the recorded ones)")
	return cmd
}

func newGcpDisconnect(run gcpConnectRun, deps fleetDeps, version string) *cobra.Command {
	var f gcpConnectFlags
	cmd := gcpConnectCommand("disconnect", "Remove what dormeto gcp connect set up, from the manager's record",
		"Remove what dormeto gcp connect set up, from the connection's record in the manager: every binding of the two service\n"+
			"accounts at the recorded scopes and router projects (on the projects, folders and organizations, the controllers and\n"+
			"the tag values), the two service accounts with their impersonation grants, the provider and the pool; then it verifies\n"+
			"that no binding of the two accounts is left and clears the record, which unregisters the manager's Google Cloud routers.\n"+
			"The custom roles dormetoDiscovery and dormetoCommand are left in place, granting nothing, and a later connect updates\n"+
			"and reuses them: Google keeps a deleted role's ID for up to 44 days, in which a connect could not create it again.\n"+
			"--delete-roles deletes them (one still granted to another member is kept, and said): a connect then works within 7\n"+
			"days (it undeletes them) or once Google frees the IDs. It turns no API off and lists the APIs, the bindings Google\n"+
			"added and the roles it keeps as left in place. It checks your permissions first, prints the plan and asks before\n"+
			"applying (--yes without a terminal). Google keeps the pool and its provider deleted for 30 days: a later connect\n"+
			"undeletes them.",
		&f, deps, version, run)
	cmd.Flags().BoolVar(&f.deleteRoles, "delete-roles", false, "delete the custom roles dormetoDiscovery and dormetoCommand too (Google then blocks their IDs for up to 44 days)")
	return cmd
}
