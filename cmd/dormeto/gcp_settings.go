package main

// `dormeto gcp workloads settings <id>...` and `dormeto gcp routers settings`: the settings levels of Google Cloud's router,
// on the flags of AWS's (levels.go), inherit giving a value back to the levels. What Google Cloud fixes is refused here, before
// any call, with the router's reasons (REASONS of its gcp/src/levels.mjs).

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/gcponboard"
)

// gcpFixedReasons are what Google Cloud fixes at 0 whatever the level says, and why: the router's own words.
var gcpFixedReasons = []struct{ flag, key, reason, why string }{
	{"wait-seconds", "waitSeconds", "waitSeconds is 0 on Google Cloud: the waker holds no request (behind a swap the switch back takes longer than any hold; behind the policy it receives only the error fetch)", "the waker holds no request"},
	{"max-holders", "maxHolders", "maxHolders is 0 on Google Cloud: no request is held", "no request is held"},
	{"drain-seconds", "drainSeconds", "drainSeconds is 0 on Google Cloud: the nap's switch is a URL map change, and the backend service's own connection draining applies", "the nap's switch is a URL map change"},
}

// gcpFixedRefusal refuses a fixed setting given anything but inherit, whatever its value and before any range check (the range
// is AWS's, and no value is right here), naming each with the router's reason.
func gcpFixedRefusal(c *cobra.Command, levels *levelFlags) error {
	var reasons []string
	for _, f := range gcpFixedReasons {
		if c.Flags().Changed(f.flag) && strings.TrimSpace(*levels.numbers[f.flag]) != levels.unset {
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
			"workload's load balancer. Several workloads are confirmed on a terminal (--yes without one; --dry-run lists them). A refusal is printed for that workload, the others go on (exit 1). A router without the settings levels says so, with the version to install.",
		&f, []string{"project", "region"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.IDs, r.Levels = ids, body
			return run(ctx, d, r)
		})
	levels = bindLevelFlags(cmd, "inherit")
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
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "list the workloads and stop")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "do not ask for confirmation of several workloads (needed without a terminal)")
	return cmd
}

func newGcpRouters(run gcpRun, version string) *cobra.Command {
	cmd := &cobra.Command{Use: "routers", Short: "The Dormeto router of a region in Google Cloud", Args: noArgs, SilenceErrors: true,
		Long: "The settings of the router dmtr-<region>. Its status is `dormeto gcp status`."}
	cmd.AddCommand(newGcpRoutersSettings(run, version))
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
			"nothing; --yes is accepted as AWS's, a single router asking nothing. A router without the settings levels says so, with the version to install.",
		&f, []string{"project", "region"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.Levels = body
			return run(ctx, d, r)
		})
	levels = bindLevelFlags(cmd, "inherit")
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
	fl.BoolVar(&f.dryRun, "dry-run", false, "say what would be set and stop")
	fl.BoolVar(&f.yes, "yes", false, "do not ask for confirmation (a single router asks nothing)")
	return cmd
}
