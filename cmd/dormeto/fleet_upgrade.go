package main

// `dormeto routers upgrade`: the plan (routers upgraded, routers skipped and why), the confirmation, then one
// upgrade-router operation per router started in the manager and followed until it ends. Exit codes: 0 (also when the
// confirmation is declined: nothing started); 1 when an upgrade failed or did not start, the manager refused or
// --timeout stopped the following; 2 for a bad flag or argument, or no --yes without a terminal; 128 and the signal's number on a signal.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/install"
)

// followEvery is how often an operation is read while it runs.
const followEvery = 5 * time.Second

func newRoutersUpgrade(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		filters          routerFilters
		all, dryRun, yes bool
		asJSON           bool
		concurrency      int
		timeout          time.Duration
	)
	cmd := &cobra.Command{
		Use: "upgrade [<router>...]", Short: "Upgrade routers to the version available",
		Long: "Upgrade the routers named, narrowed by the filters (--all takes neither: every router). A router written in\n" +
			"Terraform, up to date, newer than the version available, of unknown version or busy with an operation is skipped,\n" +
			"and the reason printed. The plan is printed, then confirmed on a terminal (--yes without one; --dry-run stops\n" +
			"there). The upgrades run in the manager, at most --concurrency at once; Ctrl-C or --timeout stops the following,\n" +
			"not the upgrades.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, names []string) error {
			sel, err := filters.selection(names, all, true)
			if err == nil && (concurrency < 1 || concurrency > 20) {
				err = usagef("--concurrency: %d is not from 1 to 20", concurrency)
			}
			if err == nil && timeout <= 0 {
				err = usagef("--timeout: %s is not a positive duration", timeout)
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				return runUpgrade(ctx, c, deps, api, sel, filters.tags, upgradeFlags{dryRun, yes, asJSON, concurrency, timeout})
			})
		},
	}
	filters.bind(cmd)
	fl := cmd.Flags()
	fl.BoolVar(&all, "all", false, "every router (takes no names nor filters)")
	fl.IntVar(&concurrency, "concurrency", 4, "how many upgrades run at once, from 1 to 20")
	fl.DurationVar(&timeout, "timeout", 60*time.Minute, "stop following the upgrades after this long (they go on in the manager)")
	fl.BoolVar(&dryRun, "dry-run", false, "print the plan and stop")
	fl.BoolVar(&yes, "yes", false, "do not ask for confirmation (needed without a terminal)")
	fl.BoolVar(&asJSON, "json", false, "print the result as JSON; the plan and progress go to stderr")
	return cmd
}

type upgradeFlags struct {
	dryRun, yes, asJSON bool
	concurrency         int
	timeout             time.Duration
}

func runUpgrade(ctx context.Context, c *cobra.Command, deps fleetDeps, api *fleet.Client, sel fleet.Selection, tags []string, o upgradeFlags) error {
	var routers fleet.Routers
	if err := api.Get(ctx, "/routers", nil, &routers); err != nil {
		return err
	}
	if routers.Available == "" {
		return errors.New("the manager names no router version to upgrade to")
	}
	var tagged map[string]bool
	if len(sel.Tags) > 0 {
		var err error
		if tagged, err = taggedRouters(ctx, api, c, newRouterWarnings(c), tags); err != nil {
			return err
		}
	}
	rows, err := sel.Select(routers.Rows, tagged)
	if err != nil {
		return usageError{err}
	}
	plan := fleet.PlanUpgrade(rows, routers.Available)
	info := c.OutOrStdout() // the plan and the progress; the result alone is on stdout with --json
	if o.asJSON {
		info = c.ErrOrStderr()
	}
	fmt.Fprint(info, plan.Text())
	result := func(r fleet.Result) error {
		if r.Skipped == nil {
			r.Skipped = []fleet.Skip{}
		}
		if o.asJSON {
			return printJSON(c.OutOrStdout(), r)
		}
		return nil
	}
	if len(plan.Upgrade) == 0 || o.dryRun {
		return result(fleet.Result{Available: plan.Available, Upgraded: []fleet.Outcome{}, Skipped: plan.Skipped})
	}
	if !o.yes {
		if !deps.terminal(c.InOrStdin()) {
			return usagef("confirmation needed: pass --yes to upgrade without a terminal")
		}
		question := fmt.Sprintf("Upgrade %d %s to %s?", len(plan.Upgrade), fleet.Plural(len(plan.Upgrade), "router"), plan.Available)
		ok, err := deps.confirm(ctx, c.InOrStdin(), info, question)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(info, "not confirmed: nothing started")
			return result(fleet.Result{Available: plan.Available, Upgraded: []fleet.Outcome{}, Skipped: plan.Skipped})
		}
	}
	followed, cancel := deps.timeout(ctx, o.timeout)
	defer cancel()
	res := fleet.Upgrade(followed, fleet.NewOperations(api), plan, fleet.UpgradeOptions{
		Concurrency: o.concurrency, Poll: followEvery, Sleep: deps.sleep, Now: deps.now,
		Progress: func(e fleet.Event) {
			if e.Ended {
				fmt.Fprintln(info, e.Line())
			} else {
				fmt.Fprintf(info, "… %s upgrading (operation %s)\n", e.Router, e.Operation)
			}
		},
	})
	if err := result(res); err != nil {
		return err
	}
	summary := res.Summary()
	if res.Stopped {
		cause := fmt.Errorf("timeout of %s reached: %s", o.timeout, summary)
		if ctx.Err() != nil { // a signal or the caller's end, not --timeout
			cause = fmt.Errorf("interrupted: %s", summary)
		}
		fmt.Fprintln(c.ErrOrStderr(), cause)
		if res.Unfinished() > 0 {
			fmt.Fprintln(c.ErrOrStderr(), "the started upgrades go on in the manager: follow them in its Operations view")
		}
		return exitCode(1)
	}
	if res.Failed() > 0 {
		return errors.New(summary)
	}
	fmt.Fprintln(info, summary)
	return nil
}

// sleepFor waits d, or ends with the context's error as soon as it is done.
func sleepFor(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// promptConfirm asks question on a terminal and gives the answer, or the context's error as soon as it is done (a
// terminal's read does not end with Ctrl-C).
func promptConfirm(ctx context.Context, in io.Reader, out io.Writer, question string) (bool, error) {
	return install.Prompted(ctx, out, func() (bool, error) { return install.NewPrompter(in, out, true).Confirm(question) })
}
