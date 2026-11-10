package main

// `dormeto gcp workloads list|show|onboard|offboard|nap|wake|schedule|pause|resume|access`: the flags read into a gcponboard.Request, as the other gcp
// commands. The workload's ID of show, offboard, nap and wake is their argument.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/gcponboard"
)

// gcpWorkloadRuns are the runs of the gcp workloads commands.
type gcpWorkloadRuns struct {
	list, show, onboard, offboard, nap, wake, scheduleShow, scheduleSet, scheduleClear, pause, resume, accessShow, accessGrant, accessRevoke, settings gcpRun
	pageShow, pageSet, pageClear                                                                                                                       gcpRun
}

var theGcpWorkloadRuns = gcpWorkloadRuns{list: gcponboard.WorkloadsList, show: gcponboard.WorkloadsShow, onboard: gcponboard.WorkloadsOnboard,
	offboard: gcponboard.WorkloadsOffboard, nap: gcponboard.WorkloadsNap, wake: gcponboard.WorkloadsWake,
	scheduleShow: gcponboard.WorkloadsScheduleShow, scheduleSet: gcponboard.WorkloadsScheduleSet, scheduleClear: gcponboard.WorkloadsScheduleClear,
	pause: gcponboard.WorkloadsPause, resume: gcponboard.WorkloadsResume,
	accessShow: gcponboard.WorkloadsAccessShow, accessGrant: gcponboard.WorkloadsAccessGrant, accessRevoke: gcponboard.WorkloadsAccessRevoke, settings: gcponboard.WorkloadsSettings,
	pageShow: gcponboard.WorkloadsPageShow, pageSet: gcponboard.WorkloadsPageSet, pageClear: gcponboard.WorkloadsPageClear}

// pageRuns are the waiting-page runs of the workloads' level.
func (r gcpWorkloadRuns) pageRuns() gcpPageRuns {
	return gcpPageRuns{r.pageShow, r.pageSet, r.pageClear}
}

func newGcpWorkloads(runs gcpWorkloadRuns, version string) *cobra.Command {
	cmd := &cobra.Command{Use: "workloads", Short: "Workloads of a Dormeto router in Google Cloud", Args: noArgs, SilenceErrors: true,
		Long: "List, show, onboard, offboard, nap, wake, schedule, pause, resume, settings and waiting-page the workloads of the router dmtr-<region>, through its controller\n" +
			"with your identity token (the Cloud Run invoker role on it). 'access' writes a GKE workload's Kubernetes RBAC, in the cluster."}
	cmd.AddCommand(
		newGcpWorkloadsList(runs.list, version),
		newGcpWorkloadsOne("show", "Show a workload", "Show a workload: its state, URL map, backend service, settings and backends.", runs.show, version, false),
		newGcpWorkloadsOnboard(runs.onboard, version),
		newGcpWorkloadsWrite("offboard", "Offboard a workload and remove its tag bindings and Kubernetes access",
			"Offboard a workload: the controller wakes it if needed and restores every reference, then the tag dormeto=managed is\n"+
				"unbound from its VMs, groups and databases, its Role and RoleBinding are removed from its cluster, and the request logs\n"+
				"its onboarding turned on (--request-logs) are set back unless they changed since, as you. A workload\n"+
				"that cannot wake is refused; --force offboards it anyway and leaves its backends down (they are listed). --force passes\n"+
				"only a failed wake: a workload still NAPPING or WAKING is refused as busy, with or without it, until that ends or\n"+
				"the router's check fails it. With --force, a tag or an access it cannot remove does not stop the rest: it names each,\n"+
				"exit 1, and it names what is left when the controller does not answer, removing nothing. It is always confirmed on a\n"+
				"terminal, as AWS's (--yes without one; --dry-run says what it would send and sends nothing).", runs.offboard, version, true),
		newGcpWorkloadsWrite("nap", "Put a workload to sleep now", "Nap a workload now, without waiting for its idle window.", runs.nap, version, false),
		newGcpWorkloadsWake(runs.wake, version),
		newGcpWorkloadsSchedule(runs, version),
		newGcpWorkloadsPause(runs.pause, version),
		newGcpWorkloadsWrite("resume", "Resume a paused workload", "Resume a workload: its pause ends and its naps run again. A pause of the whole router, if any, stays.", runs.resume, version, false),
		newGcpWorkloadsSettings(runs.settings, version),
		newGcpWaitingPage(runs.pageRuns(), version, true),
		newGcpWorkloadsAccess(runs, version))
	return cmd
}

// gcpRouterFlags adds the region and --json of a workloads command.
func gcpRouterFlags(cmd *cobra.Command, f *gcpFlags) {
	cmd.Flags().StringVar(&f.region, "region", "", "the router's region, such as europe-west1 (required)")
	cmd.Flags().BoolVar(&f.json, "json", false, "print the answer as JSON, AWS's: a write's by its target, <router>/<workload> or <router> (its line goes to stderr)")
}

func newGcpWorkloadsList(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	var states, tags []string
	var search string
	var has map[string]string
	cmd := gcpCommand("list", "List the workloads of a router",
		"List the router's workloads and their states. --state, --search and --tag keep the workloads AWS's list would keep,\n"+
			"applied to the controller's list (--tag needs router 0.40.0 or later, which answers the tags); --json prints AWS's\n"+
			"list object, {workloads, total, byState, cursor, errors}: the rows they keep as the manager's rows name theirs (router,\n"+
			"account, region, cloud, workloadId, pauses, the backends' kinds instance, asg, k8s, cloudsql-instance), and the\n"+
			"router's counts by state.",
		&f, []string{"project", "region"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.States, r.Search, r.HasTags = upper(states), search, has
			return run(ctx, d, r)
		})
	prior := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		var err error
		if has, err = fleet.ParseTags(tags); err != nil {
			return finish(c, usageError{err})
		}
		if len(has) == 0 {
			has = nil
		}
		return nil
	}
	gcpRouterFlags(cmd, &f)
	fl := cmd.Flags()
	fl.StringSliceVar(&states, "state", nil, "only the workloads in these states (repeatable or comma-separated; case-insensitive)")
	fl.StringArrayVar(&tags, "tag", nil, "only the workloads with this Dormeto tag, key=value (repeatable: all must match)")
	fl.StringVar(&search, "search", "", "only the workloads whose name or id contains this text")
	return cmd
}

// newGcpWorkloadsWrite is a write on the one workload named by its argument, with AWS's --yes and --dry-run. With asks (the
// offboarding), it is always confirmed on a terminal, --yes needed without one, and it takes --force.
func newGcpWorkloadsWrite(verb, short, long string, run gcpRun, version string, asks bool) *cobra.Command {
	var f gcpFlags
	cmd := newGcpWorkloadsOneWith(verb, short, long, run, version, &f, asks)
	if !asks {
		gcpWriteFlags(cmd, &f)
		return cmd
	}
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "say what would be sent and stop")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "do not ask for confirmation (needed without a terminal)")
	return cmd
}

func newGcpWorkloadsWake(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	var wait bool
	var timeout time.Duration
	cmd := newGcpWorkloadsOneWith("wake", "Wake a workload now",
		"Wake a workload now. --wait follows it every 5 s until it is awake (FAILED ends it with the failure, exit 1;\n"+
			"--timeout, exit 1; Ctrl-C, exit 130), as AWS's.",
		func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			if wait {
				r.Wait, r.Timeout = true, timeout
			}
			return run(ctx, d, r)
		}, version, &f, false)
	gcpWriteFlags(cmd, &f)
	fl := cmd.Flags()
	fl.BoolVar(&wait, "wait", false, "wait until the workload is awake")
	fl.DurationVar(&timeout, "timeout", 15*time.Minute, "with --wait, stop waiting after this long")
	prior := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		if wait && timeout <= 0 {
			return finish(c, usagef("--timeout: %s is not a positive duration", timeout))
		}
		return nil
	}
	return cmd
}

// newGcpWorkloadsOne is a command on the one workload named by its argument.
// With force, --force is a flag of the command.
func newGcpWorkloadsOne(verb, short, long string, run gcpRun, version string, force bool) *cobra.Command {
	var f gcpFlags
	return newGcpWorkloadsOneWith(verb, short, long, run, version, &f, force)
}

// newGcpWorkloadsOneWith is newGcpWorkloadsOne with the flags f, the command's own flags read into the request by run's wrapper.
func newGcpWorkloadsOneWith(verb, short, long string, run gcpRun, version string, f *gcpFlags, force bool) *cobra.Command {
	var id string
	cmd := gcpCommand(verb+" <workload>", short, long, f, []string{"project", "region"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
		r.ID = id
		return run(ctx, d, r)
	})
	cmd.Args = func(c *cobra.Command, args []string) error {
		if len(args) != 1 {
			fmt.Fprintf(c.ErrOrStderr(), "name exactly one workload, as the argument (got %d)\n", len(args))
			return exitCode(2)
		}
		id = args[0]
		return nil
	}
	gcpRouterFlags(cmd, f)
	if force {
		cmd.Flags().BoolVar(&f.force, "force", false, "offboard a workload that cannot wake anyway, leaving its backends down, and name what cannot be removed")
	}
	return cmd
}

func newGcpWorkloadsOnboard(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	var urlMap, backendService, id, hibernate string
	var idle, wakeTimeout int
	var hibernating *bool
	var idleGiven, wakeGiven bool
	var loadBalancer, cluster, wakeMode string
	var databases, oldDatabases, fallbackTypes []string
	var requestLogs bool
	var name string
	var tags []string
	var tagged map[string]string
	cmd := gcpCommand("onboard", "Onboard a backend service as a workload",
		"Onboard a backend service of a global, regional or classic external Application Load Balancer as a workload. The\n"+
			"controller checks the eligibility first and names the VMs, managed groups and databases; the tag dormeto=managed is then\n"+
			"bound to each of them as you (the router holds no permission to tag), and the workload is onboarded awake. --id defaults\n"+
			"to the backend service's name when that is a valid ID (lowercase letters, digits and dashes, at most 40 characters), else\n"+
			"it is asked on a terminal and needed otherwise. --name is the workload's name for people (up to 64 characters, default its\n"+
			"ID) and --set-tag k=v its Dormeto tags, repeatable, with AWS's checks (at most 50, a key of 1 to 128 characters, a value of up\n"+
			"to 256, letters, digits, spaces and _ . : / = + - @, no key starting with dormeto:); the router sets the source (cli).\n"+
			"Both need router 0.40.0 or later: an older one, which drops them, is refused with the install to run.\n"+
			"--dry-run stops after the eligibility check.\n"+
			"--database (a Cloud SQL instance) needs the feature cloud-sql of the router's install, and --gke-cluster its feature gke. With --gke-cluster the\n"+
			"Service's owner is read in the cluster, as you, and the Role and RoleBinding dormeto-<workload> of its namespace are\n"+
			"printed, asked (--yes without a terminal; on stderr under --json) and applied before the onboarding; a refused onboarding removes\n"+
			"what this run added. No tag is bound for pods. On a Standard cluster, scaling pods saves money only with the node autoscaler freeing a node: it warns of that.\n"+
			"The idle check reads the load balancer's request logs when the backend service logs every request, and ignores what the\n"+
			"waker ignores (scanners, IP lists); else it counts every request, and the onboarding warns of that. --request-logs sets\n"+
			"the backend service's logConfig to every request (enable, sample rate 1.0), as you, before the onboarding; the\n"+
			"offboarding sets it back unless it changed since. Cloud Logging bills the logs past its free tier.",
		&f, []string{"project", "region", "url-map", "backend-service"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.URLMap, r.BackendService, r.ID, r.Hibernate = urlMap, backendService, id, hibernating
			r.LoadBalancer, r.CloudSQL, r.GKECluster, r.RequestLogs = loadBalancer, append(slices.Clone(databases), oldDatabases...), cluster, requestLogs
			r.WakeMode, r.FallbackTypes = wakeMode, fallbackTypes
			r.Name, r.Tags = strings.TrimSpace(name), tagged
			if idleGiven {
				r.IdleMinutes = &idle
			}
			if wakeGiven {
				r.WakeTimeout = &wakeTimeout
			}
			return run(ctx, d, r)
		})
	prior := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		idleGiven, wakeGiven = c.Flags().Changed("idle-minutes"), c.Flags().Changed("wake-timeout-seconds") || c.Flags().Changed("wake-timeout")
		// The old name of --wake-timeout-seconds, AWS's.
		if c.Flags().Changed("wake-timeout") {
			fmt.Fprintln(c.ErrOrStderr(), "--wake-timeout is now --wake-timeout-seconds")
		}
		// The old name of --database, 0.11.0's.
		if c.Flags().Changed("cloud-sql") {
			fmt.Fprintln(c.ErrOrStderr(), "--cloud-sql is now --database")
		}
		// Given empty, as a variable with no value: read as not given, it would onboard something else.
		for _, name := range []string{"load-balancer", "gke-cluster", "wake-mode"} {
			if v, _ := c.Flags().GetString(name); v == "" && c.Flags().Changed(name) {
				fmt.Fprintf(c.ErrOrStderr(), "--%s: empty\n", name)
				return exitCode(2)
			}
		}
		// AWS's checks of the name and the tags: the router's too, said before any call.
		if n := strings.TrimSpace(name); utf8.RuneCountInString(n) > 64 || strings.ContainsFunc(n, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			fmt.Fprintln(c.ErrOrStderr(), "--name: at most 64 characters, no control characters")
			return exitCode(2)
		}
		var terr error
		if tagged, terr = parseTagFlag("--set-tag", tags); terr != nil {
			return finish(c, terr)
		}
		if len(tagged) == 0 {
			tagged = nil
		}
		if wakeMode != "" && wakeMode != "any" && wakeMode != "browser" {
			fmt.Fprintf(c.ErrOrStderr(), "--wake-mode: %q is not any or browser\n", wakeMode)
			return exitCode(2)
		}
		if c.Flags().Changed("fallback-type") {
			if err := gcponboard.CheckFallbackTypes(fallbackTypes); err != nil {
				fmt.Fprintln(c.ErrOrStderr(), err)
				return exitCode(2)
			}
		}
		if c.Flags().Changed("hibernate") {
			var err error
			if hibernating, err = parseHibernate(hibernate, false); err != nil {
				fmt.Fprintln(c.ErrOrStderr(), err)
				return exitCode(2)
			}
		}
		return nil
	}
	fl := cmd.Flags()
	fl.SortFlags = false // the shared flags first, in aws workloads onboard's order, then Google Cloud's own
	fl.StringVar(&name, "name", "", sharedOnboardHelp["name"])
	fl.StringArrayVar(&tags, "set-tag", nil, sharedOnboardHelp["set-tag"])
	fl.IntVar(&idle, "idle-minutes", 0, sharedOnboardHelp["idle-minutes"])
	fl.StringVar(&wakeMode, "wake-mode", "", sharedOnboardHelp["wake-mode"])
	fl.IntVar(&wakeTimeout, "wake-timeout-seconds", 0, sharedOnboardHelp["wake-timeout-seconds"])
	fl.StringVar(&hibernate, "hibernate", "", sharedOnboardHelp["hibernate"])
	fl.StringArrayVar(&fallbackTypes, "fallback-type", nil, sharedOnboardHelp["fallback-type"])
	fl.StringArrayVar(&databases, "database", nil, sharedOnboardHelp["database"])
	fl.StringVar(&id, "id", "", "the workload's ID (default: the backend service's name)")
	fl.StringVar(&urlMap, "url-map", "", "the URL map of the load balancer (required)")
	fl.StringVar(&backendService, "backend-service", "", "the application's backend service (required)")
	fl.StringVar(&loadBalancer, "load-balancer", "", "the load balancer's kind: global, regional, classic, internal (a regional map) or cross-region-internal (default: read from Google)")
	fl.BoolVar(&requestLogs, "request-logs", false, "turn the backend service's request logs on, every request, for the idle check to read them (set back at the offboarding)")
	fl.StringVar(&cluster, "gke-cluster", "", "the workload's pods are in this GKE cluster, as <location>/<name>: its Kubernetes RBAC is printed, asked and applied")
	gcpRouterFlags(cmd, &f)
	fl.BoolVar(&f.yes, "yes", false, "do not ask for confirmation (needed without a terminal)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "check the eligibility and change nothing")
	fl.IntVar(&wakeTimeout, "wake-timeout", 0, "the old name of --wake-timeout-seconds")
	fl.MarkHidden("wake-timeout")
	fl.StringArrayVar(&oldDatabases, "cloud-sql", nil, "the old name of --database")
	fl.MarkHidden("cloud-sql")
	return cmd
}

func newGcpWorkloadsSchedule(runs gcpWorkloadRuns, version string) *cobra.Command {
	cmd := &cobra.Command{Use: "schedule", Short: "Show, set or clear a workload's schedule", Args: noArgs, SilenceErrors: true,
		Long: "Show, set or clear the nap and awake windows of a workload, in its time zone, through the router's controller."}
	show := newGcpWorkloadsOne("show", "Show a workload's schedule and its next events",
		"Show the schedule of a workload, a recurrence in words, and its next events, each with its local time and zone.", runs.scheduleShow, version, false)
	clear := newGcpWorkloadsWrite("clear", "Clear a workload's schedule", "Clear the schedule of a workload: it naps and wakes by its idle window alone.", runs.scheduleClear, version, false)

	var f gcpFlags
	var file string
	var schedule json.RawMessage
	set := newGcpWorkloadsOneWith("set", "Set a workload's schedule from a JSON file",
		"Set the schedule of a workload from a JSON file (- for standard input): {\"timeZone\", \"windows\": [{\"kind\": \"nap\"|\"awake\",\n"+
			"\"days\", \"start\", \"end\", \"wake\"}], \"exceptions\", \"maxNapDays\"}. The controller checks it and says what is wrong, if\n"+
			"anything. It also takes what 'schedule show --json' prints, as AWS's schedule set does. From router 0.36.0, a window\n"+
			"also takes \"repeat\" (in place of \"days\"), \"from\" and \"until\", and the schedule \"joinMinutes\"; the README has the JSON.",
		func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.Schedule = schedule
			return runs.scheduleSet(ctx, d, r)
		}, version, &f, false)
	set.Flags().StringVar(&file, "file", "", "the schedule's JSON file, - for standard input (required)")
	gcpWriteFlags(set, &f)
	prior := set.PreRunE
	set.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		var err error
		if schedule, err = readScheduleFile(c, file); err != nil {
			return finish(c, err)
		}
		return nil
	}
	cmd.AddCommand(show, set, clear)
	return cmd
}

func newGcpWorkloadsPause(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	var reason, dur, until, end string
	var keepAsleep bool
	cmd := newGcpWorkloadsOneWith("pause", "Pause a workload's naps",
		"Pause a workload: it is not napped, by its idle window, its schedule or 'nap', until it is resumed or the pause ends.\n"+
			"A workload asleep is woken. With --keep-asleep it is not: it stays as it is, a visitor still wakes the workload,\n"+
			"and scheduled and maintenance wakes wait until the pause ends. As AWS's: --for is a duration up to 366 days (90m,\n"+
			"2h, 3d); --until is an RFC 3339 time (2026-12-01T08:00:00+01:00) or HH:MM today in the local time zone, within 366\n"+
			"days; without either the pause lasts until 'resume'. --reason (1 to 256 characters) is shown in the manager.",
		func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.Reason, r.KeepAsleep, r.Until = reason, keepAsleep, end
			return run(ctx, d, r)
		}, version, &f, false)
	gcpWriteFlags(cmd, &f)
	fl := cmd.Flags()
	fl.StringVar(&reason, "reason", "", "why, 1 to 256 characters, shown in the manager (required)")
	fl.BoolVar(&keepAsleep, "keep-asleep", false, "do not wake a workload asleep: a visitor still wakes it, scheduled and maintenance wakes wait")
	fl.StringVar(&dur, "for", "", "for how long, up to 366 days (90m, 2h, 3d); default until resumed")
	fl.StringVar(&until, "until", "", "until this time: RFC 3339, or HH:MM today in the local time zone")
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

func newGcpWorkloadsAccess(runs gcpWorkloadRuns, version string) *cobra.Command {
	cmd := &cobra.Command{Use: "access", Short: "Show, grant or revoke the Kubernetes access of a GKE workload", Args: noArgs, SilenceErrors: true,
		Long: "Show, grant or revoke what the router dmtr-<region> may do in a namespace of a GKE cluster: a Role and a RoleBinding\n" +
			"dormeto-<workload>, for the router's service account, labelled app.kubernetes.io/managed-by: dormeto. Nothing cluster-wide.\n" +
			"They are written as you, on the cluster's DNS endpoint, which must be open to traffic from outside its network."}
	cmd.AddCommand(
		newGcpWorkloadsAccessVerb("show", "Show what is in the cluster and what grant would write (read-only)",
			"Show, read-only, the Role and RoleBinding dormeto-<workload> in the namespace, and what grant would write.", runs.accessShow, version, false),
		newGcpWorkloadsAccessVerb("grant", "Write a workload's Role and RoleBinding",
			"Print the Role and RoleBinding dormeto-<workload>, ask (--yes without a terminal; on stderr under --json), list them in the install\n"+
				"record, then write what differs: run again, it writes nothing. A Role or RoleBinding of that name without the label is refused, nothing changed.\n"+
				"The binding's other subjects stay; another router's service account (dmtr-*@…) among them is refused, nothing changed.\n"+
				"Kubernetes lets no one grant rights they do not hold: without them you need container.roles.escalate and container.roles.bind.",
			runs.accessGrant, version, true),
		newGcpWorkloadsAccessVerb("revoke", "Remove a workload's Role and RoleBinding",
			"Print what it removes, ask (--yes without a terminal; on stderr under --json), remove the RoleBinding and the Role\n"+
				"dormeto-<workload> that are Dormeto's, and take them out of the install record. One of that name without the label is left and said; such a RoleBinding leaves the\n"+
				"Role too, as it may name it. While another subject holds\n"+
				"the binding, only the router's subject goes, and both objects stay; another router's service account among them is refused,\n"+
				"nothing changed. It works after dormeto gcp routers uninstall, which lists what it leaves in a cluster.",
			runs.accessRevoke, version, true))
	return cmd
}

// newGcpWorkloadsAccessVerb is show, grant or revoke; write commands take --yes.
func newGcpWorkloadsAccessVerb(verb, short, long string, run gcpRun, version string, writes bool) *cobra.Command {
	var f gcpFlags
	var cluster, namespace, workload string
	cmd := gcpCommand(verb, short, long, &f, []string{"project", "region", "gke-cluster", "namespace", "workload"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
		r.GKECluster, r.Namespace, r.ID = cluster, namespace, workload
		return run(ctx, d, r)
	})
	fl := cmd.Flags()
	fl.StringVar(&f.region, "region", "", "the router's region, such as europe-west1 (required)")
	fl.StringVar(&cluster, "gke-cluster", "", "the cluster, as <location>/<name> (required)")
	fl.StringVar(&namespace, "namespace", "", "the workload's namespace (required)")
	fl.StringVar(&workload, "workload", "", "the workload's ID, which names the objects (required)")
	fl.BoolVar(&f.json, "json", false, "print the result as JSON")
	if writes {
		fl.BoolVar(&f.yes, "yes", false, "do not ask for confirmation (needed without a terminal)")
		fl.BoolVar(&f.dryRun, "dry-run", false, "print what it would change and stop")
	}
	return cmd
}
