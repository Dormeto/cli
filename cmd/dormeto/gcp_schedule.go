package main

// `dormeto gcp schedule list|show|set|clear|add|remove|except|edit`: a workload's schedule through the router's controller, with
// the flags, checks and words of `dormeto aws schedule` (schedule.go). Each command but list names one workload, its argument.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/gcponboard"
	"github.com/Dormeto/cli/internal/schedule"
)

func newGcpSchedule(runs gcpWorkloadRuns, version string) *cobra.Command {
	cmd := &cobra.Command{Use: "schedule", Short: "List, show and change the awake and nap windows of a router's workloads", Args: noArgs, SilenceErrors: true,
		Long: "List, show, set, clear and change the nap and awake windows of the workloads of the router dmtr-<region>, in their time\n" +
			"zones, through its controller."}
	show := newGcpWorkloadsOne("show", "Show a workload's schedule and its next events",
		"Show the schedule of a workload, its windows numbered, a recurrence in words, and its next events, each with its local\n"+
			"time and zone.", runs.scheduleShow, version, false)
	clear := newGcpWorkloadsWrite("clear", "Clear a workload's schedule", "Clear the schedule of a workload: it naps and wakes by its idle window alone.", runs.scheduleClear, version, false)
	cmd.AddCommand(newGcpScheduleList(runs.scheduleList, version), show, newGcpScheduleSet(runs.scheduleSet, version), clear,
		newGcpScheduleChange(runs.scheduleChange, version, "add <workload> --kind nap|awake [window flags]", "Add a nap or awake window to a workload's schedule", addLong, &windowFlags{}),
		newGcpScheduleChange(runs.scheduleChange, version, "remove <workload> --window <n>", "Remove a window from a workload's schedule", removeLong, &removeFlags{}),
		newGcpScheduleChange(runs.scheduleChange, version, "except <workload> --date <YYYY-MM-DD>... [--window <n>] [--remove]", "Add or remove dates without a window in a workload's schedule", exceptLong, &exceptFlags{}),
		newGcpScheduleChange(runs.scheduleChange, version, "edit <workload> [--time-zone <zone>] [--join-minutes <n>|inherit] [--max-nap-days <n>]", "Change the time zone, joined windows or maximum nap of a workload's schedule", editLong, &editFlags{}))
	return cmd
}

func newGcpScheduleList(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	var filters listFilters
	var all bool
	var has map[string]string
	cmd := gcpCommand("list", "List the workloads' schedules: zone, windows and next event",
		"List the router's workloads with a schedule: router, workload, name, cloud, state, the schedule's time zone, its number\n"+
			"of windows and its next event in its zone, as AWS's list. --all adds the workloads without a schedule. --state, --search\n"+
			"and --tag keep the workloads workloads list would keep. Each workload is read for its schedule, 8 at a time; one that\n"+
			"cannot be read is said on stderr and left out (exit 1). --json prints {workloads: [{router, workloadId, name, cloud,\n"+
			"state, schedule, next}]}.",
		&f, []string{"project", "region"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.States, r.Search, r.HasTags, r.All = upper(filters.states), filters.search, has, all
			return run(ctx, d, r)
		})
	prior := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		var err error
		if has, err = fleet.ParseTags(filters.tags); err != nil {
			return finish(c, usageError{err})
		}
		if len(has) == 0 {
			has = nil
		}
		return nil
	}
	fl := cmd.Flags()
	fl.StringVar(&f.region, "region", "", "the router's region, such as europe-west1 (required)")
	filters.bind(cmd)
	fl.BoolVar(&all, "all", false, "also the workloads without a schedule")
	fl.BoolVar(&f.json, "json", false, "print {workloads: [...]}, each with its schedule as the router answers it and its next events")
	return cmd
}

func newGcpScheduleSet(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	var file string
	var raw json.RawMessage
	set := newGcpWorkloadsOneWith("set", "Set a workload's schedule from a JSON file",
		"Set the schedule of a workload from a JSON file (- for standard input): {\"timeZone\", \"windows\": [{\"kind\": \"nap\"|\"awake\",\n"+
			"\"days\", \"start\", \"end\", \"wake\"}], \"exceptions\", \"maxNapDays\"}. The controller checks it and says what is wrong, if\n"+
			"anything. It also takes what 'schedule show --json' prints, as AWS's schedule set does. From router 0.36.0, a window\n"+
			"also takes \"repeat\" (in place of \"days\"), \"from\" and \"until\", and the schedule \"joinMinutes\"; the README has the JSON.",
		func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.Schedule = raw
			return run(ctx, d, r)
		}, version, &f, false)
	set.Flags().StringVar(&file, "file", "", "the schedule's JSON file, - for standard input (required)")
	gcpWriteFlags(set, &f)
	prior := set.PreRunE
	set.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		var err error
		if raw, err = readScheduleFile(c, file); err != nil {
			return finish(c, err)
		}
		return nil
	}
	return set
}

// changeFlags are the flags of one change: they bind themselves and give the change.
type changeFlags interface {
	bind(cmd *cobra.Command)
	change(c *cobra.Command) (schedule.Change, error)
}

// newGcpScheduleChange is add, remove, except or edit on the workload of the argument, with AWS's flags, checked before any call.
func newGcpScheduleChange(run gcpRun, version, use, short, long string, flags changeFlags) *cobra.Command {
	var f gcpFlags
	var change schedule.Change
	verb, _, _ := strings.Cut(use, " ")
	cmd := newGcpWorkloadsOneWith(verb, short, long+"\n\n"+changeLost,
		func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.ScheduleChange = &change
			return run(ctx, d, r)
		}, version, &f, false)
	cmd.Use = use
	gcpWriteFlags(cmd, &f)
	flags.bind(cmd)
	prior := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		var err error
		if change, err = flags.change(c); err != nil {
			return finish(c, err)
		}
		return nil
	}
	return cmd
}
