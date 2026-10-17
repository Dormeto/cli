package main

// `dormeto install`, `install check` and `install apply`: the flags read into an install.Request and the run of
// internal/install called with the real dependencies — the profile's AWS over the AWS SDK for Go v2, HTTPS with a 30 s
// timeout, the terminal's prompts when stdin is one, its status line when stdout is one (not with --verbose) — and its
// exit code returned: 0; 1 when AWS refuses, a check fails or a build fails; 2 for a bad flag or a missing answer; 128 and
// the signal's number when SIGINT (Ctrl-C) or SIGTERM stopped it (a running build goes on).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/aws"
	"github.com/Dormeto/cli/internal/cli"
	"github.com/Dormeto/cli/internal/install"
	"github.com/Dormeto/cli/internal/installaws"
)

// installRun is one of internal/install's commands.
type installRun func(ctx context.Context, d install.Deps, r install.Request) int

// installRuns are the runs of install, install check and install apply: internal/install's, or a test's.
type installRuns struct{ install, check, apply installRun }

// theInstallRuns are internal/install's.
var theInstallRuns = installRuns{install: install.Install, check: install.InstallCheck, apply: install.Apply}

// installFlags are the flags of install and its subcommands.
type installFlags struct {
	profile, region, env, version, mode        string
	domain, certificateARN, zoneID             string
	sesIdentity, emailFrom, buildRole, release string
	adminEmails, vars                          []string
	yes, verbose                               bool
}

// installFlagNames are the flags, as Request.Set names them; the domain's three are one, "domain".
var (
	installFlagNames = []string{"profile", "region", "env", "version", "admin-emails", "mode", "domain", "certificate-arn", "zone-id",
		"ses-identity", "email-from", "var", "build-role", "release", "yes", "verbose"}
	domainFlags = []string{"domain", "certificate-arn", "zone-id"}
)

func newInstall(runs installRuns) *cobra.Command {
	f := &installFlags{}
	command := func(use, short, long string, run installRun) *cobra.Command {
		return &cobra.Command{
			Use: use, Short: short, Long: long, Args: noArgs, SilenceErrors: true,
			RunE: func(c *cobra.Command, _ []string) error { return f.run(c, run) },
		}
	}
	cmd := command("install", "Install the Dormeto manager in an AWS account, with CodeBuild",
		"Install the Dormeto manager in an AWS account: the questions a first install needs (each has its flag), read-only\n"+
			"checks, the install's bucket, role and CodeBuild project by API calls, a Terraform plan in CodeBuild, your\n"+
			"confirmation, then that saved plan applied and followed. Without a terminal, it stops after the plan: dormeto install\n"+
			"apply applies it. --yes takes the suggested answers; it never confirms a plan.", runs.install)
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintln(c.ErrOrStderr(), err)
		return exitCode(2)
	})
	cmd.AddCommand(
		command("check", "Check the prerequisites of the install (read-only)",
			"Check the prerequisites of the install, read-only: nothing is created, nothing is asked but the region.", runs.check),
		command("apply", "Apply the last saved plan of the install",
			"Apply the last saved plan of the install (made without a terminal, or not confirmed): it is shown, then applied and\n"+
				"followed. Running this command is the confirmation.", runs.apply),
	)

	p := cmd.PersistentFlags()
	p.StringVar(&f.profile, "profile", "", "the AWS profile (default: the SDK's)")
	p.StringVar(&f.region, "region", "", "the manager's region (default: the profile's, else asked; eu-west-1 suggested)")
	p.StringVar(&f.env, "env", "", "the environment: 1 to 10 lowercase letters, digits or inner hyphens (default prod)")
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
	p.BoolVar(&f.yes, "yes", false, "take the suggested answers without asking; never confirms a plan")
	p.BoolVar(&f.verbose, "verbose", false, "print Terraform's lines instead of the status line")
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
	code := run(ctx, installDeps(c, f.verbose), r)
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
		Profile: f.profile, Region: f.region, Release: f.release, Set: set, Yes: f.yes, Verbose: f.verbose,
		Flags: install.Settings{Env: f.env, Region: f.region, Version: f.version, Mode: f.mode, SESIdentity: f.sesIdentity,
			EmailFrom: f.emailFrom, BuildRole: f.buildRole},
	}
	for _, e := range f.adminEmails {
		if e = strings.TrimSpace(e); e != "" {
			r.Flags.AdminEmails = append(r.Flags.AdminEmails, e)
		}
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

// installDeps are the real dependencies: the profile's configuration and the install's clients, HTTPS with a 30 s
// timeout, the prompts when stdin is a terminal, the status line when stdout is one (not with --verbose).
func installDeps(c *cobra.Command, verbose bool) install.Deps {
	in, out := c.InOrStdin(), c.OutOrStdout()
	terminal := isCharDevice(in)
	status := cli.StatusOf(out)
	if verbose {
		status = nil
	}
	return install.Deps{
		Clients: func(ctx context.Context, profile, region string) (install.Clients, error) {
			cfg, err := aws.Load(ctx, profile)
			if err != nil {
				return install.Clients{}, err
			}
			return installaws.Clients(cfg, region), nil
		},
		Region: func(ctx context.Context, profile string) (string, error) {
			cfg, err := aws.Load(ctx, profile)
			return cfg.Region, err
		},
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		Prompter: install.NewPrompter(in, out, terminal),
		Out:      out,
		Status:   status,
		Terminal: terminal,
		Now:      time.Now,
		Every:    time.After,
	}
}

// isCharDevice tells whether r is a terminal to ask: a file that is a character device.
func isCharDevice(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
