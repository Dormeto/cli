package main

// `dormeto gcp workloads settings <id>...` and `dormeto gcp routers settings`: the settings levels of Google Cloud's router,
// on the flags of AWS's (levels.go), inherit giving a value back to the levels. What Google Cloud fixes is refused here, before
// any call, with the router's reasons (REASONS of its gcp/src/levels.mjs).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/gcponboard"
)

// gcpFixedReasons are what Google Cloud fixes at 0 whatever the level says, and why: the router's own words.
var gcpFixedReasons = []struct{ flag, key, reason, why string }{
	{"wait-seconds", "waitSeconds", "waitSeconds is 0 on Google Cloud: the waker holds no request, it answers the waiting page at once and the page waits for the wake", "the waker holds no request"},
	{"max-holders", "maxHolders", "maxHolders is 0 on Google Cloud: no request is held", "no request is held"},
	{"drain-seconds", "drainSeconds", "drainSeconds is 0 on Google Cloud: the nap's switch is a URL map change, and the backend service's own connection draining applies", "the nap's switch is a URL map change"},
}

// gcpFixedRefusal refuses a fixed setting given anything but inherit, whatever its value and before any range check (the range
// is AWS's, and no value is right here), naming each with the router's reason.
func gcpFixedRefusal(c *cobra.Command, levels *levelFlags) error {
	var reasons []string
	for _, f := range gcpFixedReasons {
		if c.Flags().Changed(f.flag) && !levels.unsets(strings.TrimSpace(*levels.numbers[f.flag])) {
			reasons = append(reasons, fmt.Sprintf("--%s: %s", f.flag, f.reason))
		}
	}
	if len(reasons) == 0 {
		return nil
	}
	return usagef("%s", strings.Join(reasons, "\n"))
}

// gcpLevelFlagsHelp tells the truth of the level flags for Google Cloud: three are fixed, and the wake timeout stops at 1700.
func gcpLevelFlagsHelp(cmd *cobra.Command, levels *levelFlags) {
	levels.max = map[string]int{"wake-timeout-seconds": gcponboard.MaxWakeTimeout}
	for _, f := range gcpFixedReasons {
		cmd.Flags().Lookup(f.flag).Usage = "fixed at 0 on Google Cloud, so only inherit is accepted (" + f.why + ")"
	}
	cmd.Flags().Lookup("wake-timeout-seconds").Usage = fmt.Sprintf("the wake timeout, %d to %d, or inherit (one controller request lasts at most 1800 s)", gcponboard.MinWakeTimeout, gcponboard.MaxWakeTimeout)
}

func newGcpWorkloadsSettings(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	var hibernate, kind string
	var levels *levelFlags
	var ids []string
	var body map[string]any
	cmd := gcpCommand("settings <workload>...", "Set the settings of workloads",
		"Set the settings of workloads of the router dmtr-<region>, through its controller, as AWS's workloads settings does: whether\n"+
			"the VMs hibernate (--hibernate true, false or inherit, the router's), the workload's own settings (--idle-minutes,\n"+
			"--wake-timeout-seconds, --failed-retry-seconds, --wake-mode and the rules files) and its --type (a name, or inherit),\n"+
			"inherit giving each back to its router, then its type's. --wait-seconds, --max-holders and --drain-seconds are 0 on\n"+
			"Google Cloud, so any value is refused; inherit is accepted. --wake-mode any is passed on: the router decides by the\n"+
			"workload's load balancer. Several workloads are confirmed on a terminal (--yes without one; --dry-run says each request). A refusal is printed for that workload, the others go on (exit 1). A router without the settings levels says so, with the version to install.",
		&f, []string{"project", "region"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.IDs, r.Levels = ids, body
			return run(ctx, d, r)
		})
	levels = bindLevelFlags(cmd, googleCloudRefuses, "inherit")
	gcpLevelFlagsHelp(cmd, levels)
	cmd.Args = func(c *cobra.Command, args []string) error {
		if len(args) == 0 {
			fmt.Fprintln(c.ErrOrStderr(), "name at least one workload, as the argument")
			return exitCode(2)
		}
		ids = args
		return nil
	}
	prior := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		err := gcpFixedRefusal(c, levels)
		var own map[string]any
		if err == nil {
			own, err = levels.body(c)
		}
		body = own
		if err == nil && c.Flags().Changed("hibernate") {
			var value *bool
			if value, err = parseHibernate(hibernate, true); err == nil {
				body["hibernate"] = value
			}
		}
		if err == nil && c.Flags().Changed("type") {
			switch {
			case kind == "inherit":
				body["type"] = nil
			case typeName.MatchString(kind):
				body["type"] = kind
			default:
				err = usagef("--type: give a type's name (lower-case letters, digits and hyphens) or inherit")
			}
		}
		if err == nil && len(body) == 0 {
			err = usagef("give --hibernate, --type or a setting")
		}
		if err != nil {
			return finish(c, err)
		}
		return nil
	}
	gcpRouterFlags(cmd, &f)
	cmd.Flags().StringVar(&hibernate, "hibernate", "", "true, false, or inherit (the router's)")
	cmd.Flags().StringVar(&kind, "type", "", "a type's name, or inherit (its backends')")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, dryRunUsage)
	cmd.Flags().BoolVar(&f.yes, "yes", false, "do not ask for confirmation of several targets (needed without a terminal)")
	return cmd
}

func newGcpRouters(runs gcpRuns, version string) *cobra.Command {
	cmd := &cobra.Command{Use: "routers", Short: "The Dormeto router of a region in Google Cloud", Args: noArgs, SilenceErrors: true,
		Long: "Install, uninstall, show, settings, pause and waiting page of the router dmtr-<region> of a project."}
	cmd.AddCommand(newGcpInstall(runs.install, version), newGcpUninstall(runs.uninstall, version), newGcpStatus(runs.status, "show", version), newGcpRoutersSettings(runs.routerSettings, version), newGcpRoutersPause(runs.routerPause, version), newGcpRoutersResume(runs.routerResume, version),
		newGcpWaitingPage(runs.routerPage, version, false))
	return cmd
}

func newGcpRoutersSettings(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	var hibernate, join string
	var levels *levelFlags
	var body map[string]any
	cmd := gcpCommand("settings", "Show or set the settings of the router",
		"Set the router's level, as AWS's routers settings does: --hibernate (true, false or inherit), --join-minutes (the largest gap\n"+
			"that joins two schedule windows of one kind, 0 to 120, a multiple of 5, or inherit) and the workload settings that are\n"+
			"its workloads' defaults (--idle-minutes, --wake-timeout-seconds, --failed-retry-seconds, --wake-mode and the rules files;\n"+
			"inherit removes one). --wait-seconds, --max-holders and --drain-seconds are 0 on Google Cloud: any value is refused.\n"+
			"What is not given stays as it was. With no flag, the router's level is shown. --dry-run says what would be set and sends\n"+
			"nothing; --yes is accepted as AWS's, a single router asking nothing. --json prints, as AWS's, by router: the controller's\n"+
			"answer, or with no flag the router's level. A router without the settings levels says so, with the version to install.",
		&f, []string{"project", "region"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.Levels = body
			return run(ctx, d, r)
		})
	levels = bindLevelFlags(cmd, googleCloudRefuses, "inherit")
	gcpLevelFlagsHelp(cmd, levels)
	prior := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		err := gcpFixedRefusal(c, levels)
		var defaults map[string]any
		if err == nil {
			defaults, err = levels.body(c)
		}
		settings := levelSettings{body: map[string]any{}}
		if err == nil && (c.Flags().Changed("hibernate") || c.Flags().Changed("join-minutes")) {
			settings, err = readLevelSettings(c, hibernate, join, true)
		}
		if err == nil && len(defaults) > 0 {
			settings.body["defaults"] = defaults
		}
		if err != nil {
			return finish(c, err)
		}
		body = settings.body
		return nil
	}
	gcpRouterFlags(cmd, &f)
	fl := cmd.Flags()
	fl.StringVar(&hibernate, "hibernate", "", "true, false, or inherit")
	fl.StringVar(&join, "join-minutes", "", "the largest gap that joins two windows, 0 to 120, a multiple of 5, or inherit")
	fl.BoolVar(&f.dryRun, "dry-run", false, dryRunUsage)
	fl.BoolVar(&f.yes, "yes", false, asksNothingYes)
	return cmd
}

// gcpNow is the clock of the gcp commands that read a time: a pause's --for and --until.
var gcpNow = time.Now

// gcpPauseEnd reads a pause's --reason (1 to 256 characters, trimmed) and its end, --for or --until (RFC 3339, or HH:MM today in
// the local time zone; within 366 days), before any call, as AWS's: the end in RFC 3339 and UTC, "" for none.
func gcpPauseEnd(reason, dur, until string) (string, string, error) {
	reason, err := pauseReason(reason)
	if err != nil {
		return "", "", err
	}
	t, set, err := endOf(gcpNow(), dur, until, maxPause, false)
	if err != nil || !set {
		return reason, "", err
	}
	return reason, t.UTC().Format(time.RFC3339), nil
}

func newGcpRoutersPause(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	var reason, dur, until string
	var keepAsleep bool
	var end string
	cmd := gcpCommand("pause", "Pause the router: no automatic nap of its workloads",
		"Pause the router, as `dormeto aws routers pause` does: none of its workloads is napped automatically, and the workloads\n"+
			"asleep are woken unless --keep-asleep (the woken ones are printed). --reason (1 to 256 characters) is shown in the manager.\n"+
			"--for is a duration up to 366 days (90m, 2h, 3d); --until is an RFC 3339 time or HH:MM today in the local time zone,\n"+
			"within 366 days; without either the pause lasts until `dormeto gcp routers resume`. A pause of one workload stays when\n"+
			"the router is resumed. --json prints the controller's answer by router, {\"<router>\": …}, as AWS's. Routers 0.40.0 and\n"+
			"later; an older one is refused with the version to install.",
		&f, []string{"project", "region"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.Reason, r.KeepAsleep, r.Until = reason, keepAsleep, end
			return run(ctx, d, r)
		})
	gcpRouterFlags(cmd, &f)
	gcpRouterDryRunFlags(cmd, &f)
	fl := cmd.Flags()
	fl.StringVar(&reason, "reason", "", "why, 1 to 256 characters, shown in the manager (required)")
	fl.StringVar(&dur, "for", "", "for how long, up to 366 days (90m, 2h, 3d)")
	fl.StringVar(&until, "until", "", "until this time: RFC 3339, or HH:MM today in the local time zone")
	fl.BoolVar(&keepAsleep, "keep-asleep", false, "do not wake the workloads that are asleep")
	prior := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		var err error
		if reason, end, err = gcpPauseEnd(reason, dur, until); err != nil {
			return finish(c, err)
		}
		return nil
	}
	return cmd
}

func newGcpRoutersResume(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	cmd := gcpCommand("resume", "End the pause of the router",
		"End the pause of the router: its workloads follow their schedule and idle rules again. A router that was not paused says\n"+
			"\"not paused\". --json prints the controller's answer by router, {\"<router>\": …}, as AWS's. Routers 0.40.0 and later.",
		&f, []string{"project", "region"}, version, run)
	gcpRouterFlags(cmd, &f)
	gcpRouterDryRunFlags(cmd, &f)
	return cmd
}

// asksNothingYes is AWS's --yes of a command that asks nothing: a script passes it to every writing command.
const asksNothingYes = "accepted and ignored: this command asks no confirmation"

// gcpWriteFlags are AWS's --dry-run and --yes of a write that asks nothing: --dry-run says the request and sends nothing,
// --yes is accepted, a no-op, so that a script passes it to every write.
func gcpWriteFlags(cmd *cobra.Command, f *gcpFlags) {
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "say what would be sent and stop")
	cmd.Flags().BoolVar(&f.yes, "yes", false, asksNothingYes)
}

// gcpRouterDryRunFlags are AWS's --dry-run and --yes of a router's pause and resume: a single router asks nothing.
func gcpRouterDryRunFlags(cmd *cobra.Command, f *gcpFlags) {
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "say what would be sent and stop")
	cmd.Flags().BoolVar(&f.yes, "yes", false, asksNothingYes)
}
