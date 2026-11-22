package main

// `dormeto aws install local [directory]`: a Terraform configuration of the manager's source checked, planned and applied
// from this machine (internal/installlocal), Terraform run with os/exec in the directory, its standard input closed and
// Ctrl-C passed to it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dormeto/cli/internal/install"
	"github.com/Dormeto/cli/internal/installlocal"
)

// installLocalLong is install local's help.
const installLocalLong = "Check, plan and apply a Terraform configuration of the manager's source from this machine (its infra/terraform/org or\n" +
	"platform stack, or one laid out the same way), the recommended way over cd <directory> and the Terraform commands by\n" +
	"hand. The directory is the argument (default: the current one); it never needs a cd.\n\n" +
	"First, read-only checks, each a ✓ or ✗ line, the run stopping at the first one that fails, before any Terraform call:\n" +
	"the directory holds .tf files; with an env/ folder, env/<env>.tfvars exists (else the environments there are listed),\n" +
	"and env/<env>.backend.hcl and env/<env>.local.tfvars are used when present (without an env/ folder: no var file, no\n" +
	"backend configuration); each file the configuration reads from ${path.module} (file, filebase64, templatefile) exists,\n" +
	"a missing one named with build it first; Terraform answers its version: tfenv exec when tfenv is on PATH, else\n" +
	"terraform, the configuration's required_version shown beside it (Terraform enforces it).\n\n" +
	"Then, in the directory, with Terraform's own output and -input=false: init (-backend-config=env/<env>.backend.hcl), plan\n" +
	"(-detailed-exitcode, the var files, saved to <env>.tfplan), and, when it has changes, that saved plan applied with no\n" +
	"question: running this command is the confirmation, the plan just shown being the one applied (Terraform's\n" +
	"auto-approve, without planning twice). --dry-run stops after the plan and applies nothing. It uses the credentials\n" +
	"Terraform finds itself: the configuration's providers and backend name the accounts, so the install's --profile,\n" +
	"--region and other flags are refused (exit 2); --yes is accepted and ignored. Exit 0; 1 when a check or a Terraform\n" +
	"step fails (the step named); 128 and the signal's number when Ctrl-C stopped it, the signal passed to Terraform."

func newInstallLocal(f *installFlags, runs installRuns) *cobra.Command {
	var dryRun bool
	run := runs.local
	if run == nil {
		run = installlocal.Run
	}
	cmd := &cobra.Command{
		Use: "local [directory]", Short: "Check, plan and apply a Terraform configuration of the manager's source (recommended over cd and terraform)",
		Long: installLocalLong, SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) > 1 {
				fmt.Fprintln(c.ErrOrStderr(), "give one directory at most: the Terraform configuration's (default: the current one)")
				return exitCode(2)
			}
			for _, name := range installFlagNames {
				if name != "env" && name != "verbose" && name != "yes" && c.Flags().Changed(name) {
					fmt.Fprintf(c.ErrOrStderr(), "--%s: not used by install local: the configuration's providers and backend name the accounts\n", name)
					return exitCode(2)
				}
			}
			env := f.env
			if env == "" {
				env = "prod"
			}
			if err := install.ValidEnv(env); err != nil {
				fmt.Fprintln(c.ErrOrStderr(), err)
				return exitCode(2)
			}
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			ctx, signalled, stop := interruptible(c.Context())
			defer stop()
			d := installlocal.Deps{Out: c.OutOrStdout(), Err: c.ErrOrStderr(), LookPath: exec.LookPath, Run: execRunner(signalled)}
			code := run(ctx, d, installlocal.Request{Dir: dir, Env: env, DryRun: dryRun, Verbose: f.verbose})
			if s := signalled(); s != nil {
				code = signalCode(s)
			}
			if code != 0 {
				return exitCode(code)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "check, init and plan, and apply nothing (the plan stays saved in <env>.tfplan)")
	cmd.Flags().Bool("yes", false, "accepted and ignored: running this command is the confirmation (scripts pass --yes to every write)")
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintln(c.ErrOrStderr(), err)
		return exitCode(2)
	})
	// Its help lists, of the install's flags, the two it takes: --env and --verbose.
	parentHelp := cmd.HelpFunc()
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		var hidden []*pflag.Flag
		c.InheritedFlags().VisitAll(func(fl *pflag.Flag) {
			if fl.Name != "env" && fl.Name != "verbose" && !fl.Hidden {
				fl.Hidden = true
				hidden = append(hidden, fl)
			}
		})
		defer func() {
			for _, fl := range hidden {
				fl.Hidden = false
			}
		}()
		parentHelp(c, args)
	})
	return cmd
}

// execRunner runs a command in a directory with its standard input closed, its output to the writers given; the context's
// end passes the signal received (Ctrl-C) to it, and waits a minute for it to stop.
func execRunner(signalled func() os.Signal) func(ctx context.Context, dir, name string, args []string, stdout, stderr io.Writer) (int, error) {
	return func(ctx context.Context, dir, name string, args []string, stdout, stderr io.Writer) (int, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, nil, stdout, stderr
		cmd.Cancel = func() error {
			s := signalled()
			if s == nil {
				s = os.Interrupt
			}
			return cmd.Process.Signal(s)
		}
		cmd.WaitDelay = time.Minute
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) && ctx.Err() == nil {
			return exit.ExitCode(), nil
		}
		if err != nil {
			return -1, err
		}
		return 0, nil
	}
}
