package main

// The manager's readings: `dormeto overview`, `alerts list`, `ip-lists list`, `savings show` and `discounts`, and
// `audit`. Each prints a table or labelled lines, or with --json the manager's answer (after the CLI's own filters).
// `savings discounts` is the only one that writes.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

// maxDiscount is the highest discount, in percent, the manager accepts.
const maxDiscount = 90

func newOverview(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	var asJSON bool
	cmd := fleetCommand("overview", "Show the fleet at a glance: workloads, savings, alerts, operations, routers")
	cmd.Args, cmd.SilenceErrors = noArgs, true
	cmd.Long = "Show what the dashboard's Overview shows: the workloads by state, the share of the last 7 days in each state,\n" +
		"the estimated savings, the open alerts, the running operations and the routers that are outdated. It reads six\n" +
		"routes; one that fails is reported on stderr and its block left out, the others are printed (exit 1 at the end).\n" +
		"--json prints {workloads: {total, byState}, states, savings, alerts: {open}, operations: {running}, routers:\n" +
		"{total, outdated, available}}."
	f.bind(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the overview as one JSON object")
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
			o := fleet.ReadOverview(ctx, api)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			for _, err := range o.Failed {
				fmt.Fprintln(c.ErrOrStderr(), err)
			}
			var err error
			if asJSON {
				err = printJSON(c.OutOrStdout(), o.JSON())
			} else {
				fmt.Fprint(c.OutOrStdout(), o.Text(deps.now()))
			}
			if err == nil && len(o.Failed) > 0 {
				err = exitCode(1)
			}
			return err
		})
	}
	return cmd
}

func newAlerts(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("alerts", "List the open alerts and where they are sent")
	f.bind(cmd)
	var asJSON bool
	list := &cobra.Command{
		Use: "list", Short: "List the open alarms, then the notification subscriptions", Args: noArgs, SilenceErrors: true,
		Long: "List the open health alarms (level, kind, name, age, problems), then the alert topic's subscriptions: how many\n" +
			"are confirmed and pending, and, for an administrator, each endpoint (masked). \"no alerts configured\" when the\n" +
			"manager has none. --json prints the manager's answer.",
		RunE: func(c *cobra.Command, _ []string) error {
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				a, err := fleet.GetAlerts(ctx, api)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), a)
				}
				fmt.Fprint(c.OutOrStdout(), a.Text(deps.now()))
				return nil
			})
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	cmd.AddCommand(list)
	return cmd
}

func newIPLists(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("ip-lists", "List the manager's IP lists")
	f.bind(cmd)
	var asJSON bool
	list := &cobra.Command{
		Use: "list", Short: "List the IP lists, their action, entries and last download", Args: noArgs, SilenceErrors: true,
		Long: "List the manager's IP lists: name, action (defer: a request from a listed address does not wake a workload by\n" +
			"itself; ignore: it never wakes one), entries, age of the last download and its status. They are set with\n" +
			"ip_lists in the manager's Terraform. --json prints the manager's answer.",
		RunE: func(c *cobra.Command, _ []string) error {
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				a, err := fleet.GetIPLists(ctx, api)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), a)
				}
				fmt.Fprint(c.OutOrStdout(), a.Text(deps.now()))
				return nil
			})
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	cmd.AddCommand(list)
	return cmd
}

func newSavings(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("savings", "Show the estimated savings and set the discounts they are priced with")
	f.bind(cmd)
	cmd.AddCommand(newSavingsShow(f, deps), newSavingsDiscounts(f, deps))
	return cmd
}

func newSavingsShow(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show", Short: "Show the estimated savings and the discounts", Args: noArgs, SilenceErrors: true,
		Long: "Show the estimate of the daily job as the Savings card does: the last 30 days, this month, since the first day\n" +
			"computed, Dormeto's own cost and the net, the top savers, what is not priced, and the discounts for commitments\n" +
			"the prices are reduced by. --json prints the manager's answer.",
		RunE: func(c *cobra.Command, _ []string) error {
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				a, err := fleet.GetSavings(ctx, api)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), a)
				}
				fmt.Fprint(c.OutOrStdout(), a.Text(deps.now().Location()))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newSavingsDiscounts(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		compute, databases int
		asJSON             bool
	)
	cmd := &cobra.Command{
		Use: "discounts --compute <0..90> --databases <0..90>", Short: "Set the discounts for commitments the savings are priced with", Args: noArgs, SilenceErrors: true,
		Long: "Set the average discounts, in whole percent from 0 to 90, that the savings estimate takes off the list prices of\n" +
			"compute (EC2, Fargate, EKS nodes) and of databases. Both are required: what is not given would be reset. They apply\n" +
			"from the next day the job computes. Needs an administrator. --json prints the manager's answer.",
		RunE: func(c *cobra.Command, _ []string) error {
			var err error
			switch fl := c.Flags(); {
			case !fl.Changed("compute") || !fl.Changed("databases"):
				err = usagef("give both --compute and --databases (whole percentages from 0 to %d)", maxDiscount)
			case compute < 0 || compute > maxDiscount:
				err = usagef("--compute: %d is not a whole percentage from 0 to %d", compute, maxDiscount)
			case databases < 0 || databases > maxDiscount:
				err = usagef("--databases: %d is not a whole percentage from 0 to %d", databases, maxDiscount)
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				d := fleet.Discounts{Compute: compute, Databases: databases}
				answer, err := fleet.PutDiscounts(ctx, api, d)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), answer)
				}
				var set struct{ Settings fleet.Discounts }
				if json.Unmarshal(answer, &set) == nil && (set.Settings != fleet.Discounts{}) {
					d = set.Settings
				}
				fmt.Fprintf(c.OutOrStdout(), "discounts set: %s (they apply from the next day computed)\n", fleet.DiscountsText(d))
				return nil
			})
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&compute, "compute", 0, "the discount on compute, whole percent from 0 to 90 (required)")
	fl.IntVar(&databases, "databases", 0, "the discount on databases, whole percent from 0 to 90 (required)")
	fl.BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

// sinceTime is the time --since names: a duration back from now (2d, 12h), an RFC 3339 time, or a YYYY-MM-DD date at
// midnight in now's zone.
func sinceTime(s string, now time.Time) (time.Time, error) {
	if d, err := parseFor(s); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t, nil
	}
	return time.Time{}, usagef("--since: %q is not a duration (2d, 12h), an RFC 3339 time or a date (YYYY-MM-DD)", s)
}

func newAudit(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	var (
		since, who, target string
		actions            []string
		limit              int
		asJSON             bool
	)
	cmd := fleetCommand("audit", "Show the audit log: who did what, newest first")
	cmd.Args, cmd.SilenceErrors = noArgs, true
	cmd.Long = "Show the audit log, newest first (times in the local time zone): when, who (an ARN shortened to its last two\n" +
		"parts), action, target and result, of every mutating call and refused action. The manager sends it whole; the\n" +
		"filters are the CLI's: --since a duration back from now (2d, 12h), an RFC 3339 time or a date (YYYY-MM-DD, local\n" +
		"midnight); --action an action's name (repeatable or comma-separated); --who and --target a case-insensitive part of\n" +
		"the text; --limit the newest entries only (100; 0 for all). --json prints {entries} as the manager sends them,\n" +
		"after the filters. Needs an administrator."
	f.bind(cmd)
	fl := cmd.Flags()
	fl.StringVar(&since, "since", "", "only entries since this: a duration (2d, 12h), an RFC 3339 time or a date (YYYY-MM-DD)")
	fl.StringSliceVar(&actions, "action", nil, "only these actions, such as pause or operation-onboard (repeatable or comma-separated)")
	fl.StringVar(&who, "who", "", "only entries whose who contains this text (any case)")
	fl.StringVar(&target, "target", "", "only entries whose target contains this text (any case)")
	fl.IntVar(&limit, "limit", 100, "at most this many entries, the newest (0: all)")
	fl.BoolVar(&asJSON, "json", false, "print the manager's entries, after the filters")
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		var filter fleet.AuditFilter
		var err error
		if since != "" {
			filter.Since, err = sinceTime(since, deps.now())
		}
		if err == nil && limit < 0 {
			err = usagef("--limit: %d is negative (0 lists all)", limit)
		}
		if err != nil {
			return finish(c, err)
		}
		filter.Actions, filter.Who, filter.Target = actions, strings.TrimSpace(who), strings.TrimSpace(target)
		return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
			entries, err := fleet.ListAudit(ctx, api)
			if err != nil {
				return err
			}
			shown, total := fleet.FilterAudit(entries, filter, limit)
			if asJSON {
				if shown == nil {
					shown = []fleet.AuditEntry{}
				}
				return printJSON(c.OutOrStdout(), map[string]any{"entries": shown})
			}
			out := c.OutOrStdout()
			fmt.Fprint(out, fleet.AuditTable(shown, deps.now().Location()))
			if len(shown) < total {
				fmt.Fprintf(out, "%d of %d entries shown (--limit 0 shows all)\n", len(shown), total)
			}
			return nil
		})
	}
	return cmd
}
