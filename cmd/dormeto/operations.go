package main

// `dormeto operations list`, `show` and `watch`: the manager's operations, newest first; one operation's record and
// steps; and the following of one to its end, as the commands that start operations follow theirs. Exit codes as the
// fleet commands; `watch` exits 1 when the operation failed or was not followed to its end.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

func newOperations(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("operations", "List, show and follow the manager's operations")
	f.bind(cmd)
	cmd.AddCommand(newOperationsList(f, deps), newOperationsShow(f, deps), newOperationsWatch(f, deps))
	return cmd
}

func newOperationsList(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		running bool
		types   []string
		limit   int
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use: "list", Short: "List the operations, newest first", Args: noArgs, SilenceErrors: true,
		Long: "List the manager's operations, newest first by creation: id, type, target, status, current step, who started\n" +
			"it (an ARN shortened to its last two parts) and its age. --running keeps those still running; --type keeps the\n" +
			"types given (repeatable or comma-separated: onboard, offboard, install-router, upgrade-router, uninstall-router,\n" +
			"exclude, lift-exclusion, update-manager, ...); --limit caps the rows (50; 0 for all). --json prints the\n" +
			"manager's records, after these filters.",
		RunE: func(c *cobra.Command, _ []string) error {
			if err := checkLimit(limit); err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				ops, err := fleet.ListOperations(ctx, api)
				if err != nil {
					return err
				}
				shown, total := fleet.FilterOperations(ops, running, types, limit)
				if asJSON {
					if shown == nil {
						shown = []fleet.Operation{}
					}
					return printJSON(c.OutOrStdout(), shown)
				}
				out := c.OutOrStdout()
				fmt.Fprint(out, fleet.OperationsTable(shown, deps.now()))
				if len(shown) < total {
					fmt.Fprintf(out, "%d of %d operations shown (--limit 0 lists all)\n", len(shown), total)
				}
				return nil
			})
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&running, "running", false, "only the operations still running")
	fl.StringSliceVar(&types, "type", nil, "only operations of these types (repeatable or comma-separated)")
	fl.IntVar(&limit, "limit", 50, "at most this many operations, the newest (0: all)")
	fl.BoolVar(&asJSON, "json", false, "print the manager's records")
	return cmd
}

// operationID is the one argument of show and watch: an operation's id.
func operationID(args []string) (string, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return "", usagef("name exactly one operation (its id)")
	}
	return args[0], nil
}

// readerOf is how an operation is read for as long as it is followed: the manager's update only through the list, which
// alone refreshes it, the others by their id.
func readerOf(api *fleet.Client, op fleet.Operation) func(context.Context, string) (fleet.Operation, error) {
	if op.Type == "update-manager" {
		return fleet.ListedOperation(api)
	}
	return fleet.GetOperation(api)
}

func newOperationsShow(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show <id>", Short: "Show an operation: its record and its steps", SilenceErrors: true,
		Long: "Show one operation: type, target, status, who started it, its times (local time zone) and error, then its steps\n" +
			"with their status, how long each took and what it says. --json prints the manager's record.",
		RunE: func(c *cobra.Command, args []string) error {
			id, err := operationID(args)
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				op, err := fleet.GetOperation(api)(ctx, id)
				if err == nil && op.Type == "update-manager" { // only the list brings it up to date
					op, err = fleet.ListedOperation(api)(ctx, id)
				}
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), op)
				}
				fmt.Fprint(c.OutOrStdout(), fleet.OperationText(op, deps.now().Location()))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's record")
	return cmd
}

func newOperationsWatch(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		timeout time.Duration
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use: "watch <id>", Short: "Follow an operation to its end", SilenceErrors: true,
		Long: "Follow an operation, printing its steps as they change on stderr, until it ends: exit 0 when it succeeded, 1\n" +
			"when it failed (with the failed step's detail). One already finished prints its outcome at once. Ctrl-C and\n" +
			"--timeout stop following, the operation goes on in the manager (exit 1; 130 on Ctrl-C). --json prints the\n" +
			"operation as last read, on stdout.",
		RunE: func(c *cobra.Command, args []string) error {
			id, err := operationID(args)
			if err == nil && timeout <= 0 {
				err = usagef("--timeout: %s is not a positive duration", timeout)
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				return watch(ctx, c, deps, api, id, timeout, asJSON)
			})
		},
	}
	fl := cmd.Flags()
	fl.DurationVar(&timeout, "timeout", 60*time.Minute, "stop following after this long (the operation goes on in the manager)")
	fl.BoolVar(&asJSON, "json", false, "print the operation as last read (progress goes to stderr)")
	return cmd
}

// watch reads the operation once, then follows it unless it has ended, and says how it ended: "✓ <target>: <type>
// succeeded in <time>" on stdout, "✗ <target>: <type> failed at <step>: <detail> (operation <id>)" on stderr and exit 1.
func watch(ctx context.Context, c *cobra.Command, deps fleetDeps, api *fleet.Client, id string, timeout time.Duration, asJSON bool) error {
	began := deps.now()
	op, err := fleet.GetOperation(api)(ctx, id)
	if err != nil {
		return err
	}
	label := op.TargetText()
	if label == "" {
		label = op.ID
	}
	info, errs := textOut(c), c.ErrOrStderr()
	if op.Status == "running" {
		followed, cancel := deps.timeout(ctx, timeout)
		defer cancel()
		printed := map[int]string{}
		last, err := fleet.Follow(followed, readerOf(api, op), id, fleet.FollowOptions{Poll: followEvery, Sleep: deps.sleep, Progress: func(op fleet.Operation) {
			for _, line := range stepLines(printed, op) {
				fmt.Fprintf(errs, "  %s\n", line)
			}
		}})
		if last.ID != "" {
			op = last
		}
		if err != nil {
			if followed.Err() == nil {
				fmt.Fprintf(errs, "✗ %s: cannot read the operation: %s (operation %s)\n", label, err, id)
				return exitCode(1)
			}
			cause := fmt.Sprintf("timeout of %s reached", timeout)
			if ctx.Err() != nil { // a signal or the caller's end, not --timeout
				cause = "interrupted"
			}
			fmt.Fprintf(errs, "%s\n%s: still running in the manager: dormeto operations watch %s\n", cause, label, id)
			if asJSON {
				_ = printJSON(c.OutOrStdout(), op)
			}
			return exitCode(1)
		}
	}
	if asJSON {
		if err := printJSON(c.OutOrStdout(), op); err != nil {
			return err
		}
	}
	switch op.Status {
	case "succeeded":
		fmt.Fprintf(info, "✓ %s: %s succeeded in %s\n", label, op.Type, took(op, began, deps.now()))
		return nil
	case "failed":
		fmt.Fprintf(errs, "✗ %s: %s (operation %s)\n", label, failedText(op), op.ID)
	default:
		fmt.Fprintf(errs, "✗ %s: the operation is %q (operation %s)\n", label, op.Status, op.ID)
	}
	return exitCode(1)
}
