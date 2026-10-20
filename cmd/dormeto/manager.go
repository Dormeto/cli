package main

// `dormeto manager status`, `check`, `changes` and `update`: the dashboard's Update page. A manager installed with the
// CLI plans and applies its own update through its install build; each request is an update-manager operation, followed
// through GET /operations, the only route that brings it up to date. A manager installed with Terraform is updated by
// changing its module's source. Exit codes as the fleet commands.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

// managerChangelog is where the release notes of a manager the CLI did not install are read.
const managerChangelog = "https://github.com/Dormeto/manager/blob/main/CHANGELOG.md"

func newManager(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("manager", "Show the manager's version, daily check and release notes, and update it")
	f.bind(cmd)
	cmd.AddCommand(newManagerStatus(f, deps), newManagerCheck(f, deps), newManagerChanges(f, deps), newManagerUpdate(f, deps))
	return cmd
}

// notFound tells whether err is the manager's 404: a service this manager does not have.
func notFound(err error) bool {
	var ae *fleet.APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// managerVersion is the version the manager's /health gives.
func managerVersion(ctx context.Context, api *fleet.Client) (string, error) {
	var health struct {
		Version string `json:"version"`
	}
	err := api.Get(ctx, "/health", nil, &health)
	return health.Version, err
}

func newManagerStatus(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "status", Short: "Show the manager's version, its update state and the daily check", Args: noArgs, SilenceErrors: true,
		Long: "Show the manager's update as the Update page does: the version it runs, how it was installed (with the CLI,\n" +
			"whose install build updates it, or with Terraform), the version last applied, the newer versions (with their\n" +
			"router version, or why one cannot be applied from the manager), the last install build, a request waiting\n" +
			"for its build, the plan ready to apply and its summary, and the daily check (\"not available\" in a manager\n" +
			"without it). Times in the local time zone. --json prints {version, update, check}: the version /health gives,\n" +
			"the answers of GET /update and GET /update/check (null when not available). Needs an administrator.",
		RunE: func(c *cobra.Command, _ []string) error {
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var m fleet.ManagerStatus
				var err error
				if m.Version, err = managerVersion(ctx, api); err != nil {
					return err
				}
				if m.Update, err = fleet.GetUpdate(ctx, api); err != nil {
					return err
				}
				check, err := fleet.GetUpdateCheck(ctx, api)
				switch {
				case err == nil:
					m.Check = &check
				case !notFound(err):
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), m)
				}
				fmt.Fprint(c.OutOrStdout(), m.Text(deps.now().Location()))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print {version, update, check}: the manager's answers")
	return cmd
}

func newManagerCheck(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var enable, disable, alert, noAlert, asJSON bool
	cmd := &cobra.Command{
		Use: "check (--enable [--alert|--no-alert] | --disable)", Short: "Turn the daily check for new versions on or off", Args: noArgs, SilenceErrors: true,
		Long: "Turn the manager's daily check for new versions on (--enable) or off (--disable). --alert also sends a new\n" +
			"version to the alert topic, --no-alert stops that; with --enable alone the alert stays as it is. Turning the\n" +
			"check off turns its alert off. --json prints the manager's answer. Needs an administrator.",
		RunE: func(c *cobra.Command, _ []string) error {
			var err error
			switch {
			case enable == disable:
				err = usagef("give exactly one of --enable and --disable")
			case alert && noAlert:
				err = usagef("give --alert or --no-alert, not both")
			case disable && (alert || noAlert):
				err = usagef("--alert and --no-alert go with --enable")
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				sendAlert := alert
				if enable && !alert && !noAlert { // the alert as it is
					current, err := fleet.GetUpdateCheck(ctx, api)
					if err != nil {
						return err
					}
					sendAlert = current.Alert
				}
				saved, err := fleet.PutUpdateCheck(ctx, api, enable, enable && sendAlert)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), saved)
				}
				fmt.Fprint(c.OutOrStdout(), saved.Text(deps.now().Location()))
				return nil
			})
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&enable, "enable", false, "check daily for new versions")
	fl.BoolVar(&disable, "disable", false, "stop the daily check (and its alert)")
	fl.BoolVar(&alert, "alert", false, "with --enable, also send a new version to the alert topic")
	fl.BoolVar(&noAlert, "no-alert", false, "with --enable, do not send it to the alert topic")
	fl.BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newManagerChanges(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		to     string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use: "changes [--to <version>]", Short: "Print the release notes of the versions the manager can be updated to", Args: noArgs, SilenceErrors: true,
		Long: "Print the release notes of the versions the manager offers, newest first, up to --to (default: the newest),\n" +
			"with a note where a version asks for an access change that the update does not apply. --json prints the\n" +
			"manager's answer. Needs an administrator and a manager installed with the CLI.",
		RunE: func(c *cobra.Command, _ []string) error {
			if to != "" && !fleet.IsVersion(to) {
				return finish(c, usagef("--to: %q is not a version such as 1.2.3", to))
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				out := c.OutOrStdout()
				if to == "" {
					s, err := fleet.GetUpdate(ctx, api)
					switch {
					case err != nil:
						return err
					case !s.InstalledWithCLI:
						return errors.New("installed with Terraform: its release notes are at " + managerChangelog)
					}
					if to = s.Newest(); to == "" {
						if s.ReleasesUnreachable {
							return errors.New("the releases could not be read: try again later")
						}
						if asJSON {
							return printJSON(out, []any{})
						}
						fmt.Fprintf(out, "the manager is up to date (%s)\n", s.Version)
						return nil
					}
				}
				changes, err := fleet.GetChanges(ctx, api, to)
				if notFound(err) { // the route exists only in a manager installed with the CLI
					return errors.New("installed with Terraform: its release notes are at " + managerChangelog)
				}
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(out, changes)
				}
				fmt.Fprint(out, changes.Text())
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "the last version whose notes are printed (default: the newest offered)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newManagerUpdate(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var u managerUpdate
	cmd := &cobra.Command{
		Use: "update [--version <version>] [--dry-run] [--yes]", Short: "Plan the manager's update, then apply it once confirmed", Args: noArgs, SilenceErrors: true,
		Long: "Update the manager as the Update page does: its install build plans the update to --version (default: the\n" +
			"newest the manager offers), the plan's summary is printed (resources to add, change and destroy, those replaced\n" +
			"or destroyed), then, once confirmed (always on a terminal; --yes without one), the build applies that plan, and\n" +
			"the version the manager then runs is read. Both builds are followed step by step; while the apply replaces the\n" +
			"manager's API, the reads that fail are tried again until --timeout (for each build). --dry-run stops after the\n" +
			"plan. A manager installed with Terraform is updated by changing its module's source. Needs an administrator.",
		RunE: func(c *cobra.Command, _ []string) error {
			err := u.wait.check()
			if err == nil && u.version != "" && !fleet.IsVersion(u.version) {
				err = usagef("--version: %q is not a version such as 1.2.3", u.version)
			}
			if err == nil && !u.dryRun && !u.yes && !deps.terminal(c.InOrStdin()) {
				err = usagef("confirmation needed: pass --yes to update the manager without a terminal")
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				run, err := u.run(ctx, c, deps, api)
				if u.asJSON && run != nil {
					if run.Operations == nil {
						run.Operations = []fleet.Operation{}
					}
					if perr := printJSON(c.OutOrStdout(), run); perr != nil && err == nil {
						err = perr
					}
				}
				return err
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&u.version, "version", "", "the version to update to (default: the newest the manager offers)")
	fl.BoolVar(&u.dryRun, "dry-run", false, "plan the update and print the plan, without applying it")
	fl.BoolVar(&u.yes, "yes", false, "do not ask for confirmation (needed without a terminal)")
	fl.DurationVar(&u.wait.timeout, "timeout", 60*time.Minute, "stop following the plan, or the apply, after this long (the build goes on in the manager)")
	fl.BoolVar(&u.asJSON, "json", false, "print {update, operations}: the update's state as last read and the operations followed (progress goes to stderr)")
	return cmd
}

// managerUpdate is the request of `dormeto manager update`.
type managerUpdate struct {
	version             string
	dryRun, yes, asJSON bool
	wait                waitFlags
}

// updateRun is what an update did, printed by --json: the update's state as last read and the operations followed.
type updateRun struct {
	Update     fleet.UpdateState `json:"update"`
	Operations []fleet.Operation `json:"operations"`
}

// run plans the update, prints the plan, and applies it once confirmed; the plan only of the version asked is applied.
// It gives what --json prints (nil when GET /update could not be read, or the update was not confirmed). Under --json,
// what is said along the way goes to stderr.
func (u managerUpdate) run(ctx context.Context, c *cobra.Command, deps fleetDeps, api *fleet.Client) (*updateRun, error) {
	info := c.OutOrStdout()
	if u.asJSON {
		info = c.ErrOrStderr()
	}
	s, err := fleet.GetUpdate(ctx, api)
	if err != nil {
		return nil, err
	}
	run := &updateRun{Update: s}
	version := u.version
	if !s.InstalledWithCLI {
		if version == "" {
			version = "the version you want"
		}
		return run, fmt.Errorf("installed with Terraform: change its module's source to %s", version)
	}
	running := s.Version
	if version == "" {
		if version = s.Newest(); version == "" {
			if s.ReleasesUnreachable {
				return run, errors.New("the releases could not be read: try again later")
			}
			fmt.Fprintf(info, "the manager is up to date (%s)\n", running)
			return run, nil
		}
	}

	// follow runs one request of the update to its end: its operation, then, when it failed, what its build says.
	follow := func(action string, start func(context.Context) (fleet.Operation, error)) error {
		ops, err := runOperations(ctx, c, deps, []opItem{{label: action + " " + version, start: start, get: fleet.ListedOperation(api)}}, 1, u.wait)
		run.Operations = append(run.Operations, ops...)
		if err != nil && len(ops) == 1 && ops[0].Status == "failed" {
			if s, rerr := fleet.GetUpdate(ctx, api); rerr == nil {
				run.Update = s
				explainBuild(c, s.Build, action, version)
			}
		}
		return err
	}

	if err := follow("plan", func(ctx context.Context) (fleet.Operation, error) { return fleet.PlanUpdate(ctx, api, version) }); err != nil {
		return run, err
	}
	if s, err = fleet.GetUpdate(ctx, api); err != nil {
		return run, err
	}
	run.Update = s
	plan := s.Plan
	switch {
	case plan == nil:
		return run, fmt.Errorf("the manager shows no plan of %s to apply: nothing applied (dormeto manager status)", version)
	case plan.Version != version:
		return run, fmt.Errorf("the manager's plan is of %s, not %s: nothing applied", plan.Version, version)
	}
	fmt.Fprint(info, plan.Text())
	if u.dryRun {
		fmt.Fprintf(info, "dry run: the plan of %s is not applied\n", version)
		return run, nil
	}
	question := fmt.Sprintf("Update the manager from %s to %s (%s)?", running, version, plan.Summary.Counts())
	if declined, err := confirmNames(ctx, c, deps, []string{"manager " + version}, "manager", question, true, false, u.yes); declined || err != nil {
		return nil, err
	}

	if err := follow("apply", func(ctx context.Context) (fleet.Operation, error) { return fleet.ApplyUpdate(ctx, api, plan.Build) }); err != nil {
		return run, err
	}
	runs, err := managerVersion(ctx, api)
	if err != nil {
		return run, err
	}
	if u.asJSON {
		if s, err = fleet.GetUpdate(ctx, api); err != nil {
			return run, err
		}
		run.Update = s
	}
	switch runs {
	case version:
		fmt.Fprintf(info, "manager updated to %s\n", version)
		return run, nil
	case running:
		return run, fmt.Errorf("the apply succeeded but the manager still runs %s", running)
	}
	return run, fmt.Errorf("the manager runs %s, not %s", runs, version)
}

// explainBuild prints, after a failed plan or apply, what its install build says of it (the step that failed, the end of
// its log, its page), as the Update page does; nothing when the newest build is not that request's or did not fail.
func explainBuild(c *cobra.Command, b *fleet.UpdateBuild, action, version string) {
	if b == nil || b.By != "dashboard" || b.Action != action || b.Version != version {
		return
	}
	text := b.FailureText()
	if text == "" {
		return
	}
	errs := c.ErrOrStderr()
	fmt.Fprint(errs, text)
	if action == "apply" {
		fmt.Fprintln(errs, "the manager is as the failure left it: run dormeto manager update again, or dormeto install from a laptop")
	}
}
