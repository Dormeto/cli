package main

// `dormeto install`, `install check`, `install apply`, `install status`, `dormeto uninstall` and `uninstall apply`: the
// flags read into an install.Request and the run of internal/install called with the real dependencies — the profile's
// AWS over the AWS SDK for Go v2, HTTPS with a 30 s timeout, the terminal's prompts when stdin is one, its status line
// when stdout is one (not with --verbose) — and its exit code returned: 0; 1 when AWS refuses, a check fails or a build
// fails; 2 for a bad flag or a missing answer; 128 and the signal's number when SIGINT (Ctrl-C) or SIGTERM stopped it (a
// running build goes on).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/aws"
	"github.com/Dormeto/cli/internal/cli"
	"github.com/Dormeto/cli/internal/install"
	"github.com/Dormeto/cli/internal/installaws"
)

// installRun is one of internal/install's commands.
type installRun func(ctx context.Context, d install.Deps, r install.Request) int

// installRuns are the runs of install, install check, install apply, install status, uninstall and uninstall apply:
// internal/install's, or a test's.
type installRuns struct{ install, check, apply, status, uninstall, uninstallApply installRun }

// theInstallRuns are internal/install's.
var theInstallRuns = installRuns{install: install.Install, check: install.InstallCheck, apply: install.Apply, status: install.Status,
	uninstall: install.Uninstall, uninstallApply: install.UninstallApply}

// installFlags are the flags of install, uninstall and their subcommands.
type installFlags struct {
	profile, region, env, version, mode        string
	domain, certificateARN, zoneID             string
	sesIdentity, emailFrom, buildRole, release string
	managementProfile, memberRole              string
	adminEmails, vars                          []string
	accounts, excludeAccounts, discoveryOnly   []string
	yes, verbose, access                       bool
}

// installFlagNames are the flags, as Request.Set names them; the domain's three are one, "domain".
var (
	installFlagNames = []string{"profile", "region", "env", "version", "admin-emails", "mode", "domain", "certificate-arn", "zone-id",
		"ses-identity", "email-from", "var", "build-role", "release", "yes", "verbose",
		"management-profile", "member-role", "accounts", "exclude-accounts", "discovery-only", "access"}
	domainFlags = []string{"domain", "certificate-arn", "zone-id"}
)

// command is a command of the flags f calling run; a bad flag exits 2 (its subcommands inherit it).
func (f *installFlags) command(use, short, long string, run installRun) *cobra.Command {
	cmd := &cobra.Command{
		Use: use, Short: short, Long: long, Args: noArgs, SilenceErrors: true,
		RunE: func(c *cobra.Command, _ []string) error { return f.run(c, run) },
	}
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintln(c.ErrOrStderr(), err)
		return exitCode(2)
	})
	return cmd
}

// profileFlags are cmd's flags, its subcommands' too, that name an install: --profile, --region, --env; and --yes
// (what it does: yes), --verbose.
func (f *installFlags) profileFlags(cmd *cobra.Command, yes string) {
	p := cmd.PersistentFlags()
	p.StringVar(&f.profile, "profile", "", "the AWS profile (default: the SDK's)")
	p.StringVar(&f.region, "region", "", "the manager's region (default: the profile's, else asked; eu-west-1 suggested)")
	p.StringVar(&f.env, "env", "", "the environment: 1 to 10 lowercase letters, digits or inner hyphens (default prod)")
	p.BoolVar(&f.yes, "yes", false, yes)
	p.BoolVar(&f.verbose, "verbose", false, "print Terraform's lines instead of the status line")
}

func newInstall(runs installRuns, version string) *cobra.Command {
	f := &installFlags{}
	command := f.command
	cmd := command("install", "Install the Dormeto manager in an AWS account, with CodeBuild",
		"Install the Dormeto manager in an AWS account: the questions a first install needs (each has its flag), read-only\n"+
			"checks, the install's bucket, role and CodeBuild project by API calls, a Terraform plan in CodeBuild, your\n"+
			"confirmation, then that saved plan applied and followed. Without a terminal, it stops after the plan: dormeto install\n"+
			"apply applies it. --yes takes the suggested answers; it never confirms a plan.\n\n"+
			"In organization mode, with the management account's profile (--management-profile, or --profile when it is the\n"+
			"management account's), organization access follows the manager: the delegation policy and the member accounts'\n"+
			"roles, shown, confirmed, then deployed by a CodeBuild project of the management account removed once it ends.", runs.install)
	cmd.AddCommand(
		command("check", "Check the prerequisites of the install (read-only)",
			"Check the prerequisites of the install, read-only: nothing is created, nothing is asked but the region.", runs.check),
		command("apply", "Apply the last saved plan of the install",
			"Apply the last saved plan of the install (made without a terminal, or not confirmed): it is shown, then applied and\n"+
				"followed. With --management-profile, organization access is deployed after it, or alone when there is no plan to\n"+
				"apply. Running this command is the confirmation.", runs.apply),
		command("status", "Show where the install stands; follow a running build",
			"Show where the install stands: a running build followed, the manager's version and the latest release, the last\n"+
				"plan and apply, the dashboard and the invitations, organization access. With --management-profile, what a detached\n"+
				"organization access run left in the management account (its build role and project) is removed once it ends.", runs.status),
	)

	f.profileFlags(cmd, "take the suggested answers without asking; never confirms a plan")
	p := cmd.PersistentFlags()
	p.StringVar(&f.version, "version", "", "the manager release (default: the latest)")
	p.StringSliceVar(&f.adminEmails, "admin-emails", nil, "the administrators' emails, comma-separated")
	p.StringVar(&f.mode, "mode", "", "organization or account (default: suggested from the account's organization)")
	p.StringVar(&f.domain, "domain", "", "the dashboard's custom domain (default: a cloudfront.net host)")
	p.StringVar(&f.certificateARN, "certificate-arn", "", "the domain's ACM certificate, in us-east-1")
	p.StringVar(&f.zoneID, "zone-id", "", "the domain's Route 53 zone")
	p.StringVar(&f.sesIdentity, "ses-identity", "", "the SES identity that sends the invitations (default: Cognito sends them)")
	p.StringVar(&f.emailFrom, "email-from", "", "the invitations' sender, with --ses-identity")
	p.StringArrayVar(&f.vars, "var", nil, "another input of the manager module, its value as JSON: name=value (repeatable)")
	p.StringVar(&f.buildRole, "build-role", "", "an existing role's ARN for CodeBuild (default: created by the install)")
	p.StringVar(&f.release, "release", "", "a local release directory, uploaded to the install's bucket (an unreleased manager)")
	p.StringVar(&f.managementProfile, "management-profile", "", "the management account's profile: organization access (organization mode)")
	p.StringVar(&f.memberRole, "member-role", "", "the role assumed in each member account from the management account (default OrganizationAccountAccessRole)")
	p.StringSliceVar(&f.accounts, "accounts", nil, "the member accounts that get the roles, comma-separated (default: every active account)")
	p.StringSliceVar(&f.excludeAccounts, "exclude-accounts", nil, "member accounts left out, comma-separated")
	p.StringSliceVar(&f.discoveryOnly, "discovery-only", nil, "member accounts without the command role, comma-separated")
	p.BoolVar(&f.access, "access", false, "run organization access alone, the manager installed (needs the management account's profile)")
	return cmd
}

// newUninstall is `dormeto uninstall` and its subcommand apply, with the flags that name the install and --release (the
// manager installed from a local release).
func newUninstall(runs installRuns, version string) *cobra.Command {
	f := &installFlags{}
	cmd := f.command("uninstall", "Remove the Dormeto manager that dormeto install installed",
		"Remove the Dormeto manager that dormeto install installed, in this order: refused while routers are installed\n"+
			"(offboard the workloads and uninstall them in the dashboard first); event-driven refresh confirmed off; the\n"+
			"deletion protection lifted by a plan applied first; the destroy plan in CodeBuild, your confirmation, then that\n"+
			"saved plan applied and followed; then, if you say so, the install's bucket (its state and answers), role, log group\n"+
			"and project. Without a terminal, it stops after each plan: dormeto install apply applies the deletion protection's,\n"+
			"dormeto uninstall apply the destroy plan. --yes never confirms a plan.\n\n"+
			"Organization access is not removed: its roles keep working for a reinstall. To remove them and Dormeto's statement\n"+
			"of the delegation policy: https://dormeto.com/organization-access.html", runs.uninstall)
	cmd.AddCommand(f.command("apply", "Apply the last saved destroy plan of the uninstall",
		"Apply the last saved destroy plan of the uninstall (made without a terminal, or not confirmed): it is shown, then\n"+
			"applied and followed; the install's bucket, role, log group and project are kept (dormeto uninstall removes them).\n"+
			"Running this command is the confirmation.", runs.uninstallApply))
	f.profileFlags(cmd, "answer that event-driven refresh is off and keep the bootstrap, without asking; never confirms a plan")
	cmd.PersistentFlags().StringVar(&f.release, "release", "", "the local release directory of the manager installed, when it is not on releases.dormeto.com")
	return cmd
}

// noArgs refuses arguments: the commands take flags only.
func noArgs(c *cobra.Command, args []string) error {
	if len(args) > 0 {
		fmt.Fprintf(c.ErrOrStderr(), "unexpected argument %q\n", args[0])
		return exitCode(2)
	}
	return nil
}

// run calls the command's run with the flags' request and the real dependencies.
func (f *installFlags) run(c *cobra.Command, run installRun) error {
	r, err := f.request(c)
	if err != nil {
		fmt.Fprintln(c.ErrOrStderr(), err)
		return exitCode(2)
	}
	ctx, signalled, stop := interruptible(c.Context())
	defer stop()
	code := run(ctx, installDeps(c, f.verbose, f.env, version), r)
	if s := signalled(); s != nil {
		code = signalCode(s)
	}
	if code != 0 {
		return exitCode(code)
	}
	return nil
}

// request is the run's request: the flags' values, and which were given.
func (f *installFlags) request(c *cobra.Command) (install.Request, error) {
	set := map[string]bool{}
	for _, name := range installFlagNames {
		if c.Flags().Changed(name) {
			if slices.Contains(domainFlags, name) {
				name = "domain"
			}
			set[name] = true
		}
	}
	r := install.Request{
		Profile: f.profile, Region: f.region, Release: f.release, ManagementProfile: f.managementProfile, Set: set,
		Yes: f.yes, Verbose: f.verbose, Access: f.access,
		Flags: install.Settings{Env: f.env, Region: f.region, Version: f.version, Mode: f.mode, SESIdentity: f.sesIdentity,
			EmailFrom: f.emailFrom, BuildRole: f.buildRole, MemberRole: f.memberRole, AdminEmails: items(f.adminEmails),
			Accounts: items(f.accounts), ExcludeAccounts: items(f.excludeAccounts), DiscoveryOnly: items(f.discoveryOnly)},
	}
	if f.domain != "" || f.certificateARN != "" || f.zoneID != "" {
		r.Flags.Domain = &install.Domain{Dashboard: f.domain, CertificateARN: f.certificateARN, ZoneID: f.zoneID}
	}
	for _, v := range f.vars {
		name, value, ok := strings.Cut(v, "=")
		if !ok || name == "" {
			return install.Request{}, fmt.Errorf("--var: %q is not name=value", v)
		}
		if r.Flags.Vars == nil {
			r.Flags.Vars = map[string]json.RawMessage{}
		}
		r.Flags.Vars[name] = json.RawMessage(value)
	}
	return r, nil
}

// items are a list flag's values, trimmed, the empty ones left out.
func items(values []string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// installDeps are the real dependencies: each profile's configuration loaded once (its credentials shared by the
// install's clients and the member roles assumed with them, session dmt-<env>-install), HTTPS with a 30 s timeout, the
// prompts when stdin is a terminal, the status line when stdout is one (not with --verbose).
func installDeps(c *cobra.Command, verbose bool, env, version string) install.Deps {
	in, out := c.InOrStdin(), c.OutOrStdout()
	terminal := isTerminal(in)
	status := cli.StatusOf(out)
	if verbose {
		status = nil
	}
	var mu sync.Mutex
	loaded := map[string]awssdk.Config{}
	load := func(ctx context.Context, profile string) (awssdk.Config, error) {
		mu.Lock()
		defer mu.Unlock()
		if cfg, ok := loaded[profile]; ok {
			return cfg, nil
		}
		cfg, err := aws.Load(ctx, profile)
		if err == nil {
			loaded[profile] = cfg
		}
		return cfg, err
	}
	session := "dmt-" + orDefault(env, "prod") + "-install"
	return install.Deps{
		Clients: func(ctx context.Context, profile, region string) (install.Clients, error) {
			cfg, err := load(ctx, profile)
			if err != nil {
				return install.Clients{}, err
			}
			return installaws.Clients(cfg, region), nil
		},
		Region: func(ctx context.Context, profile string) (string, error) {
			cfg, err := load(ctx, profile)
			return cfg.Region, err
		},
		Assume: func(ctx context.Context, profile, region, roleARN string) (install.Clients, error) {
			cfg, err := load(ctx, profile)
			if err != nil {
				return install.Clients{}, err
			}
			return installaws.Clients(installaws.Assumed(cfg, region, roleARN, session), region), nil
		},
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		Prompter: install.NewPrompter(in, out, terminal),
		Out:      out,
		Status:   status,
		Terminal: terminal,
		Now:      time.Now,
		Every:    time.After,
		Version:  version,
	}
}

// isTerminal tells whether r is a terminal to ask: a file that is a character device, but not the null device (stdin
// of a Kubernetes Job, of systemd, of docker run without -i).
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}
