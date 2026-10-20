package main

// `dormeto exclusions list`, `add` and `lift`: the manager's manual exclusions, which leave a target group, a load
// balancer or an account out of discovery and pause its managed workloads. Adding and lifting are operations, followed
// as the others are (see runOperations); adding is always confirmed on a terminal (--yes without one).

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

func newExclusions(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("exclusions", "List, add and lift manual exclusions from discovery")
	f.bind(cmd)
	cmd.AddCommand(newExclusionsList(f, deps), newExclusionsAdd(f, deps), newExclusionsLift(f, deps))
	return cmd
}

func newExclusionsList(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "list", Short: "List the exclusions: target, reason, who, until, the workloads paused and the state", Args: noArgs, SilenceErrors: true,
		Long: "List the manual exclusions: id, target, reason, who excluded it, until when (local time zone), the workloads it\n" +
			"paused by name, and its state (active, expired, or lifting while a lift runs). --json prints the manager's rows.",
		RunE: func(c *cobra.Command, _ []string) error {
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				es, err := fleet.ListExclusions(ctx, api)
				if err != nil {
					return err
				}
				if asJSON {
					if es == nil {
						es = []fleet.Exclusion{}
					}
					return printJSON(c.OutOrStdout(), es)
				}
				fmt.Fprint(c.OutOrStdout(), fleet.ExclusionsText(es, deps.now().Location()))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's rows")
	return cmd
}

func newExclusionsAdd(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		tg, lb, account, reason, dur, until string
		dryRun, yes                         bool
		wait                                waitFlags
		asJSON                              bool
	)
	cmd := &cobra.Command{
		Use: "add (--target-group <arn> | --load-balancer <arn> | --account <id>) --reason <text> [--for <duration> | --until <time>]", Short: "Exclude a target group, a load balancer or an account from discovery",
		Args: noArgs, SilenceErrors: true,
		Long: "Exclude one target (an application target group or load balancer by ARN, or a whole account by its 12 digits)\n" +
			"from discovery: the manager pauses its managed workloads (an operation, followed). --reason is 1 to 246\n" +
			"characters; --for is a duration up to 366 days (90m, 2h, 3d) and --until an RFC 3339 time or HH:MM today in the\n" +
			"local time zone, within 366 days; without either the exclusion lasts until `dormeto exclusions lift`. It is\n" +
			"always confirmed on a terminal (--yes without one; --dry-run names the target and sends nothing). --no-wait\n" +
			"only starts it; Ctrl-C and --timeout stop following, the operation goes on in the manager:\n" +
			"`dormeto operations watch <id>`. Needs an operator.",
		RunE: func(c *cobra.Command, _ []string) error {
			var target map[string]any
			var text string
			given := 0
			for _, s := range []string{tg, lb, account} {
				if s != "" {
					given++
				}
			}
			var err error
			switch {
			case given != 1:
				err = usagef("give exactly one of --target-group, --load-balancer and --account")
			case tg != "" && !fleet.IsTargetGroupARN(tg):
				err = usagef("--target-group: %q is not the ARN of an application target group", tg)
			case lb != "" && !fleet.IsLoadBalancerARN(lb):
				err = usagef("--load-balancer: %q is not the ARN of an application load balancer", lb)
			case account != "" && !accountID.MatchString(account):
				err = usagef("--account: %q is not 12 digits", account)
			}
			switch {
			case err != nil:
			case tg != "":
				target, text = map[string]any{"kind": "target-group", "arn": tg}, "target group "+fleet.TargetGroupName(tg)
			case lb != "":
				target, text = map[string]any{"kind": "load-balancer", "arn": lb}, "load balancer "+lb[strings.LastIndex(strings.TrimSuffix(lb, "/"), "/")+1:]
			default:
				target, text = map[string]any{"kind": "account", "account": account}, "account "+account
			}
			r, rerr := exclusionReason(reason)
			var end time.Time
			var set bool
			if err == nil {
				err = rerr
			}
			if err == nil {
				end, set, err = endOf(deps.now(), dur, until, maxPause, false)
			}
			if err == nil {
				err = wait.check()
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				if done, err := confirmNames(ctx, c, deps, []string{text}, "exclusion", fmt.Sprintf("Exclude %s from discovery?", text), true, dryRun, yes); done || err != nil {
					return err
				}
				ops, err := runOperations(ctx, c, deps, opItems(api, []string{text}, func(ctx context.Context, _ int) (fleet.Operation, error) {
					return fleet.Exclude(ctx, api, exclusionBody(target, r, end, set))
				}), 1, wait)
				return printStarted(c, asJSON, ops, err)
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&tg, "target-group", "", "the ARN of an application target group")
	fl.StringVar(&lb, "load-balancer", "", "the ARN of an application load balancer")
	fl.StringVar(&account, "account", "", "an AWS account, 12 digits")
	fl.StringVar(&reason, "reason", "", "why, 1 to 246 characters, shown in the manager (required)")
	fl.StringVar(&dur, "for", "", "how long, a duration up to 366 days (90m, 2h, 3d); default until lifted")
	fl.StringVar(&until, "until", "", "until this time: RFC 3339, or HH:MM today in the local time zone")
	fl.BoolVar(&dryRun, "dry-run", false, "name the target and stop")
	fl.BoolVar(&yes, "yes", false, "do not ask for confirmation (needed without a terminal)")
	fl.BoolVar(&asJSON, "json", false, "print the operation, as the manager last said it")
	wait.bind(cmd)
	return cmd
}

func newExclusionsLift(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		wait   waitFlags
		asJSON bool
	)
	cmd := &cobra.Command{
		Use: "lift <id>...", Short: "Lift exclusions: their workloads are resumed and the exclusions removed",
		Long: "Lift each exclusion (its id is in `dormeto exclusions list`) and follow the operations: the workloads it paused\n" +
			"are resumed, then the exclusion is deleted. One that stays listed (a workload could not be resumed) is lifted\n" +
			"again by running this again. An exclusion another operation holds is refused with that operation's id, the\n" +
			"others go on (exit 1). --no-wait only starts them; Ctrl-C and --timeout stop following, the operations go on in\n" +
			"the manager: `dormeto operations watch <id>`. Needs an operator.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			var ids []string
			err := wait.check()
			if err == nil && len(args) == 0 {
				err = usagef("name at least one exclusion id")
			}
			for _, a := range args {
				if err == nil && strings.TrimSpace(a) == "" {
					err = usagef("an exclusion id is empty")
				}
				if !slices.Contains(ids, a) {
					ids = append(ids, a)
				}
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				ops, err := runOperations(ctx, c, deps, opItems(api, ids, func(ctx context.Context, i int) (fleet.Operation, error) {
					return fleet.LiftExclusion(ctx, api, ids[i])
				}), opConcurrency, wait)
				return printStarted(c, asJSON, ops, err)
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the operations, as the manager last said them")
	wait.bind(cmd)
	return cmd
}
