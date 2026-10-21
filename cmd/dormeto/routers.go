package main

// `dormeto routers show`, `install`, `uninstall`, `pause` and `resume`: one router's details, and the operations on
// routers through the manager's /iam routes. Install and uninstall are operations, followed as `routers upgrade`'s are;
// pause and resume are a call per router, one refused does not stop the others (exit 1 at the end). Several routers, and
// always an uninstall, are confirmed on a terminal (--yes without one; --dry-run lists them). Exit codes as the fleet
// commands.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/fleet"
)

// maxReason is the longest reason of a pause, in characters.
const maxReason = 256

// pauseReason is --reason of a pause as the manager keeps it (pauseRequest, a router's or a workload's): trimmed, 1 to
// 256 characters.
func pauseReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > maxReason {
		return "", usagef("--reason: give 1 to %d characters", maxReason)
	}
	return reason, nil
}

// uninstallConcurrency bounds the uninstalls started at once.
const uninstallConcurrency = 3

// routerPath is the manager's route of a router, its name escaped.
func routerPath(name, suffix string) string { return "/routers/" + url.PathEscape(name) + suffix }

// routerNames are the arguments, each a router name: at least one, none empty, repeats dropped.
func routerNames(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, usagef("name at least one router")
	}
	var out []string
	for _, a := range args {
		if strings.TrimSpace(a) == "" {
			return nil, usagef("a router name is empty")
		}
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out, nil
}

func newRoutersShow(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show <router>", Short: "Show a router's version, health, pauses and configuration", SilenceErrors: true,
		Long: "Show one router as it tells its details: version, health and its problems, the idle check, the Lambda and\n" +
			"queue figures, the workloads by state, the pauses, the EKS clusters and IP lists, then each configuration\n" +
			"value with where it is set (terraform: the router's module; manager). Times are in the local time zone.\n" +
			"--json prints the manager's answer.",
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
				return finish(c, usagef("name exactly one router"))
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var d fleet.RouterDetails
				if err := api.Get(ctx, routerPath(args[0], ""), nil, &d); err != nil {
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), d)
				}
				fmt.Fprint(c.OutOrStdout(), d.Text(deps.now().Location()))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newRoutersInstall(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		account, region string
		wait            waitFlags
		asJSON          bool
	)
	cmd := &cobra.Command{
		Use: "install --account <12 digits> --router-region <region>", Short: "Install a router in an AWS account and region", Args: noArgs, SilenceErrors: true,
		Long: "Start the manager's install of a router in the account and region and follow it step by step. The account\n" +
			"needs the command role the manager assumes: a refusal \"no_access\" says it has none. --region is the\n" +
			"manager's, --router-region the router's. --no-wait only starts it; Ctrl-C and --timeout stop following,\n" +
			"the operation goes on in the manager: `dormeto operations watch <id>` follows it again. Needs an\n" +
			"administrator.",
		RunE: func(c *cobra.Command, _ []string) error {
			err := wait.check()
			if err == nil && !discover.IsAccountID(account) {
				err = usagef("--account: %q is not 12 digits", account)
			}
			if err == nil && !regionName.MatchString(region) {
				err = usagef("--router-region: %q is not a region such as eu-west-1", region)
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				body := map[string]any{"type": "install-router", "account": account, "region": region}
				ops, err := runOperations(ctx, c, deps, []opItem{{
					label: account + "/" + region,
					start: func(ctx context.Context) (fleet.Operation, error) { return fleet.StartOperation(ctx, api, body) },
					get:   fleet.GetOperation(api),
				}}, 1, wait)
				return printStarted(c, asJSON, ops, err)
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&account, "account", "", "the AWS account (12 digits)")
	fl.StringVar(&region, "router-region", "", "the AWS region of the router, such as eu-west-1 (--region is the manager's)")
	fl.BoolVar(&asJSON, "json", false, "print the operation, as the manager last said it")
	wait.bind(cmd)
	return cmd
}

func newRoutersUninstall(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		wait        waitFlags
		dryRun, yes bool
		asJSON      bool
	)
	cmd := &cobra.Command{
		Use: "uninstall <router>...", Short: "Uninstall routers installed by the manager", SilenceErrors: true,
		Long: "Start the manager's uninstall of each router and follow them (3 at once at most). It is always confirmed on\n" +
			"a terminal (--yes without one; --dry-run lists the routers and sends nothing). The operation's \"check\n" +
			"empty\" step refuses a router that still has workloads. --no-wait only starts them; Ctrl-C and --timeout\n" +
			"stop following, the operations go on in the manager: `dormeto operations watch <id>`. Needs an administrator.",
		RunE: func(c *cobra.Command, args []string) error {
			names, err := routerNames(args)
			if err == nil {
				err = wait.check()
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				question := fmt.Sprintf("Uninstall %s?", strings.Join(names, ", "))
				if done, err := confirmNames(ctx, c, deps, names, "router", question, true, dryRun, yes); done || err != nil {
					return err
				}
				items := make([]opItem, len(names))
				for i, name := range names {
					body := map[string]any{"type": "uninstall-router", "router": name}
					items[i] = opItem{
						label: name,
						start: func(ctx context.Context) (fleet.Operation, error) { return fleet.StartOperation(ctx, api, body) },
						get:   fleet.GetOperation(api),
					}
				}
				ops, err := runOperations(ctx, c, deps, items, uninstallConcurrency, wait)
				return printStarted(c, asJSON, ops, err)
			})
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&dryRun, "dry-run", false, "list the routers and stop")
	fl.BoolVar(&yes, "yes", false, "do not ask for confirmation (needed without a terminal)")
	fl.BoolVar(&asJSON, "json", false, "print the operations, as the manager last said them")
	wait.bind(cmd)
	return cmd
}

// routerCalls POSTs body to each router's route in turn: its line (what say makes of the answer) goes to stdout, its
// failure to stderr as "router: message"; the others go on, and a failure makes the exit 1. Under --json the answers of
// those that succeeded are printed instead, by router.
func routerCalls(ctx context.Context, c *cobra.Command, api *fleet.Client, names []string, suffix string, body any, asJSON bool,
	say func(answer json.RawMessage) string) error {
	answers := map[string]json.RawMessage{}
	failed := false
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := api.Post(ctx, routerPath(name, suffix), body, &raw); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintf(c.ErrOrStderr(), "%s: %s\n", name, err)
			failed = true
			continue
		}
		answers[name] = raw
		if !asJSON {
			fmt.Fprintf(c.OutOrStdout(), "%s: %s\n", name, say(raw))
		}
	}
	if asJSON && len(answers) > 0 {
		if err := printJSON(c.OutOrStdout(), answers); err != nil {
			return err
		}
	}
	if failed {
		return exitCode(1)
	}
	return nil
}

func newRoutersPause(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		reason, dur, until string
		keepAsleep         bool
		dryRun, yes        bool
		asJSON             bool
	)
	cmd := &cobra.Command{
		Use: "pause <router>... --reason <text> [--for <duration> | --until <time>] [--keep-asleep]", Short: "Pause routers: no automatic nap of their workloads",
		SilenceErrors: true,
		Long: "Pause the routers: none of their workloads is napped automatically, and the workloads asleep are woken unless\n" +
			"--keep-asleep (the woken ones are printed). --reason (1 to 256 characters) is shown in the manager. --for is a\n" +
			"duration up to 366 days (90m, 2h, 3d); --until is an RFC 3339 time or HH:MM today in the local time zone, within\n" +
			"366 days; without either the pause lasts until `dormeto routers resume`. Several routers are confirmed on a\n" +
			"terminal (--yes without one; --dry-run lists them). One refused does not stop the others (exit 1). Needs an\n" +
			"operator.",
		RunE: func(c *cobra.Command, args []string) error {
			names, err := routerNames(args)
			if err == nil {
				reason, err = pauseReason(reason)
			}
			var end time.Time
			var set bool
			if err == nil {
				end, set, err = endOf(deps.now(), dur, until, maxPause, false)
			}
			if err != nil {
				return finish(c, err)
			}
			body := struct {
				Reason     string `json:"reason"`
				Until      string `json:"until,omitempty"`
				KeepAsleep bool   `json:"keepAsleep"`
			}{Reason: reason, KeepAsleep: keepAsleep}
			if set {
				body.Until = end.UTC().Format("2006-01-02T15:04:05Z")
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				if done, err := confirmNames(ctx, c, deps, names, "router", fmt.Sprintf("Pause %s?", strings.Join(names, ", ")), false, dryRun, yes); done || err != nil {
					return err
				}
				return routerCalls(ctx, c, api, names, "/pause", body, asJSON, func(raw json.RawMessage) string {
					var a struct{ Woken []string }
					_ = json.Unmarshal(raw, &a)
					s := "paused"
					if set {
						s += " until " + end.In(deps.now().Location()).Format("2006-01-02 15:04 MST")
					}
					if keepAsleep {
						s += ", keeping asleep"
					}
					if len(a.Woken) > 0 {
						s += ", woke " + strings.Join(a.Woken, ", ")
					}
					return s
				})
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&reason, "reason", "", "why, 1 to 256 characters, shown in the manager")
	fl.StringVar(&dur, "for", "", "for how long, up to 366 days (90m, 2h, 3d)")
	fl.StringVar(&until, "until", "", "until this time: RFC 3339, or HH:MM today in the local time zone")
	fl.BoolVar(&keepAsleep, "keep-asleep", false, "do not wake the workloads that are asleep")
	fl.BoolVar(&dryRun, "dry-run", false, "list the routers and stop")
	fl.BoolVar(&yes, "yes", false, "do not ask for confirmation of several routers (needed without a terminal)")
	fl.BoolVar(&asJSON, "json", false, "print the manager's answers, by router")
	return cmd
}

func newRoutersResume(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		dryRun, yes bool
		asJSON      bool
	)
	cmd := &cobra.Command{
		Use: "resume <router>...", Short: "End the pause of routers", SilenceErrors: true,
		Long: "End the pause of the routers: their workloads follow their schedule and idle rules again. A router that was\n" +
			"not paused says \"not paused\". Several routers are confirmed on a terminal (--yes without one; --dry-run\n" +
			"lists them). One refused does not stop the others (exit 1). Needs an operator.",
		RunE: func(c *cobra.Command, args []string) error {
			names, err := routerNames(args)
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				if done, err := confirmNames(ctx, c, deps, names, "router", fmt.Sprintf("Resume %s?", strings.Join(names, ", ")), false, dryRun, yes); done || err != nil {
					return err
				}
				return routerCalls(ctx, c, api, names, "/resume", nil, asJSON, func(raw json.RawMessage) string {
					var a struct{ Resumed bool }
					_ = json.Unmarshal(raw, &a)
					if a.Resumed {
						return "resumed"
					}
					return "not paused"
				})
			})
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&dryRun, "dry-run", false, "list the routers and stop")
	fl.BoolVar(&yes, "yes", false, "do not ask for confirmation of several routers (needed without a terminal)")
	fl.BoolVar(&asJSON, "json", false, "print the manager's answers, by router")
	return cmd
}
