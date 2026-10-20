package main

// `dormeto workloads show`, `history` and `idle-window`: what the manager says of one workload, read from its
// /iam/workloads/{router}/{id} routes. Exit codes as the fleet commands: 0; 1 when the manager refused (an unknown
// workload included); 2 for a bad flag or argument.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

// oneTarget is the single <router>/<workload id> a read command names, or a usage error.
func oneTarget(args []string) (fleet.Target, error) {
	if len(args) != 1 {
		return fleet.Target{}, usagef("name exactly one target (<router>/<workload id>)")
	}
	t, err := fleet.ParseTarget(args[0])
	if err != nil {
		return fleet.Target{}, usageError{err}
	}
	return t, nil
}

func newWorkloadsShow(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show <router>/<workload>", Short: "Show a workload's state, settings, pauses, schedule, week and costs",
		Long: "Show one workload as the manager's workload view does: its state and since when, name, URL, account and\n" +
			"region, router version and tags, the settings, the pauses (who, why, until when), the schedule with its next\n" +
			"events (in the schedule's time zone), the share of the last 7 days in each state, the idle hours, and what the\n" +
			"naps saved, with the energy not used. Other times are in the local time zone. --json prints the manager's answer.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			t, err := oneTarget(args)
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var raw json.RawMessage
				if err := api.Get(ctx, t.Path(""), nil, &raw); err != nil {
					return err
				}
				return printRead(c, raw, asJSON, func(d fleet.WorkloadDetails) string { return d.Text(t.Router, deps.now().Location()) })
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newWorkloadsHistory(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		days   int
		asJSON bool
	)
	cmd := &cobra.Command{
		Use: "history <router>/<workload> [--days 1..31]", Short: "Show a workload's state changes and wakes over the last days",
		Long: "Show a workload's history from the manager's store (never the router): the time in each state, the wakes (how\n" +
			"many, the median and longest, the failed) and the savings estimate of the period, then the latest 50 changes,\n" +
			"newest first, with what started each and how long a wake took. --days is 1 to 31 (7 by default). --json prints\n" +
			"the manager's answer, every change included.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			t, err := oneTarget(args)
			if err == nil && (days < 1 || days > 31) {
				err = usagef("--days: %d is not a whole number from 1 to 31", days)
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var raw json.RawMessage
				if err := api.Get(ctx, t.Path("/history"), url.Values{"days": {strconv.Itoa(days)}}, &raw); err != nil {
					return err
				}
				return printRead(c, raw, asJSON, func(h fleet.WorkloadHistory) string { return h.Text(deps.now().Location()) })
			})
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "the period, 1 to 31 days")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newWorkloadsIdleWindow(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "idle-window <router>/<workload>", Short: "Show the advice on a workload's idle window",
		Long: "Show the manager's advice on the idle window: the last 14 days of requests replayed against windows from 5 to\n" +
			"120 minutes, each with its savings a month (hours asleep a day when nothing is priced), wakes and wait a day,\n" +
			"the current and the recommended window marked. The manager computes it once a day; the first read may take a\n" +
			"while. --json prints the manager's answer.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			t, err := oneTarget(args)
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var raw json.RawMessage
				if err := api.Get(ctx, t.Path("/idle-window"), nil, &raw); err != nil {
					return err
				}
				return printRead(c, raw, asJSON, func(a fleet.IdleWindowAdvice) string { return a.Text(deps.now().Location()) })
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

// printRead prints the manager's answer as it is under --json, else the text of its decoding.
func printRead[T any](c *cobra.Command, raw json.RawMessage, asJSON bool, text func(T) string) error {
	if asJSON {
		return printJSON(c.OutOrStdout(), raw)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("decode the manager's answer: %w", err)
	}
	fmt.Fprint(c.OutOrStdout(), text(v))
	return nil
}
