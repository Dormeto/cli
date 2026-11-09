package main

// `dormeto gcp workloads list|show|onboard|offboard|nap|wake|schedule|pause|resume|access`: the flags read into a gcponboard.Request, as the other gcp
// commands. The workload's ID of show, offboard, nap and wake is their argument.

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

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
		newGcpWorkloadsOne("offboard", "Offboard a workload and remove its tag bindings and Kubernetes access",
			"Offboard a workload: the controller wakes it if needed and restores every reference, then the tag dormeto=managed is\n"+
				"unbound from its VMs, groups and databases, its Role and RoleBinding are removed from its cluster, and the request logs\n"+
				"its onboarding turned on (--request-logs) are set back unless they changed since, as you. A workload\n"+
				"that cannot wake is refused; --force offboards it anyway and leaves its backends down (they are listed). --force passes\n"+
				"only a failed wake: a workload still NAPPING or WAKING is refused as busy, with or without it, until that ends or\n"+
				"the router's check fails it. With --force, a tag or an access it cannot remove does not stop the rest: it names each,\n"+
				"exit 1, and it names what is left when the controller does not answer, removing nothing.", runs.offboard, version, true),
		newGcpWorkloadsOne("nap", "Put a workload to sleep now", "Nap a workload now, without waiting for its idle window.", runs.nap, version, false),
		newGcpWorkloadsOne("wake", "Wake a workload now", "Wake a workload now.", runs.wake, version, false),
		newGcpWorkloadsSchedule(runs, version),
		newGcpWorkloadsPause(runs.pause, version),
		newGcpWorkloadsOne("resume", "Resume a paused workload", "Resume a workload: its pause ends and its naps run again. A pause of the whole router, if any, stays.", runs.resume, version, false),
		newGcpWorkloadsSettings(runs.settings, version),
		newGcpWaitingPage(runs.pageRuns(), version, true),
		newGcpWorkloadsAccess(runs, version))
	return cmd
}

// gcpRouterFlags adds the region and --json of a workloads command.
func gcpRouterFlags(cmd *cobra.Command, f *gcpFlags) {
	cmd.Flags().StringVar(&f.region, "region", "", "the router's region, such as europe-west1 (required)")
	cmd.Flags().BoolVar(&f.json, "json", false, "print the controller's answer as JSON")
}

func newGcpWorkloadsList(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	cmd := gcpCommand("list", "List the workloads of a router", "List the router's workloads and their states.", &f, []string{"project", "region"}, version, run)
	gcpRouterFlags(cmd, &f)
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
	var databases, fallbackTypes []string
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
			"--dry-run stops after the eligibility check.\n"+
			"--cloud-sql needs the feature cloud-sql of the router's install, and --gke-cluster its feature gke. With --gke-cluster the\n"+
			"Service's owner is read in the cluster, as you, and the Role and RoleBinding dormeto-<workload> of its namespace are\n"+
			"printed, asked and applied before the onboarding; a refused onboarding removes what this run added. No tag is bound for\n"+
			"pods. On a Standard cluster, scaling pods saves money only with the node autoscaler freeing a node: it warns of that.\n"+
			"The idle check reads the load balancer's request logs when the backend service logs every request, and ignores what the\n"+
			"waker ignores (scanners, IP lists); else it counts every request, and the onboarding warns of that. --request-logs sets\n"+
			"the backend service's logConfig to every request (enable, sample rate 1.0), as you, before the onboarding; the\n"+
			"offboarding sets it back unless it changed since. Cloud Logging bills the logs past its free tier.",
		&f, []string{"project", "region", "url-map", "backend-service"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.URLMap, r.BackendService, r.ID, r.Hibernate = urlMap, backendService, id, hibernating
			r.LoadBalancer, r.CloudSQL, r.GKECluster, r.RequestLogs = loadBalancer, databases, cluster, requestLogs
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
	gcpRouterFlags(cmd, &f)
	fl := cmd.Flags()
	fl.StringVar(&urlMap, "url-map", "", "the URL map of the load balancer (required)")
	fl.StringVar(&backendService, "backend-service", "", "the application's backend service (required)")
	fl.StringVar(&id, "id", "", "the workload's ID (default: the backend service's name)")
	fl.StringVar(&name, "name", "", "the workload's name for people, up to 64 characters (default: its ID)")
	fl.StringArrayVar(&tags, "set-tag", nil, "a Dormeto tag of the workload, key=value (repeatable, at most 50; the dormeto: prefix is reserved)")
	fl.IntVar(&idle, "idle-minutes", 0, "the idle window, 5 to 1440 minutes (default: the router's, 15)")
	fl.StringVar(&hibernate, "hibernate", "", "suspend instead of stopping the VMs that can be suspended: true or false")
	fl.StringVar(&loadBalancer, "load-balancer", "", "the load balancer's kind: global, regional, classic, internal (a regional map) or cross-region-internal (default: read from Google)")
	fl.IntVar(&wakeTimeout, "wake-timeout-seconds", 0, fmt.Sprintf("how long a wake may take, %d to %d seconds (default: the router's, by what the workload holds)", gcponboard.MinWakeTimeout, gcponboard.MaxWakeTimeout))
	fl.IntVar(&wakeTimeout, "wake-timeout", 0, "the old name of --wake-timeout-seconds")
	fl.MarkHidden("wake-timeout")
	fl.StringVar(&wakeMode, "wake-mode", "", "what wakes the workload: any request that passes the filters, or browser (default: the router's; the global load balancer's waiting page wakes by browser alone)")
	fl.StringArrayVar(&fallbackTypes, "fallback-type", nil, "a fallback machine type such as e2-medium (repeatable, at most 5; for the VMs of unmanaged instance groups and zonal network endpoint groups: a wake out of capacity runs them on it)")
	fl.StringArrayVar(&databases, "cloud-sql", nil, "a Cloud SQL instance to nap with the workload: tagged as you, down after the application, up before it; repeatable")
	fl.BoolVar(&requestLogs, "request-logs", false, "turn the backend service's request logs on, every request, for the idle check to read them (set back at the offboarding)")
	fl.StringVar(&cluster, "gke-cluster", "", "the workload's pods are in this GKE cluster, as <location>/<name>: its Kubernetes RBAC is printed, asked and applied")
	fl.BoolVar(&f.yes, "yes", false, "apply a GKE workload's Kubernetes RBAC without asking (needed without a terminal, and with --json)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "check the eligibility and change nothing")
	return cmd
}

func newGcpWorkloadsSchedule(runs gcpWorkloadRuns, version string) *cobra.Command {
	cmd := &cobra.Command{Use: "schedule", Short: "Show, set or clear a workload's schedule", Args: noArgs, SilenceErrors: true,
		Long: "Show, set or clear the nap and awake windows of a workload, in its time zone, through the router's controller."}
	show := newGcpWorkloadsOne("show", "Show a workload's schedule and its next events",
		"Show the schedule of a workload, a recurrence in words, and its next events, each with its local time and zone.", runs.scheduleShow, version, false)
	clear := newGcpWorkloadsOne("clear", "Clear a workload's schedule", "Clear the schedule of a workload: it naps and wakes by its idle window alone.", runs.scheduleClear, version, false)

	var f gcpFlags
	var file string
	set := newGcpWorkloadsOneWith("set", "Set a workload's schedule from a JSON file",
		"Set the schedule of a workload from a JSON file: {\"timeZone\", \"windows\": [{\"kind\": \"nap\"|\"awake\", \"days\", \"start\", \"end\", \"wake\"}],\n"+
			"\"exceptions\", \"maxNapDays\"}. The controller checks it and says what is wrong, if anything. The file is the one\n"+
			"'schedule show --json' prints under \"schedule\". From router 0.36.0, a window also takes \"repeat\" (in place of \"days\"),\n"+
			"\"from\" and \"until\", and the schedule \"joinMinutes\"; the README has the JSON.",
		func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.File = file
			return runs.scheduleSet(ctx, d, r)
		}, version, &f, false)
	set.Flags().StringVar(&file, "file", "", "the schedule as a JSON file (required)")
	prior := set.PreRunE
	set.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		if file == "" {
			fmt.Fprintln(c.ErrOrStderr(), "--file: needed, the schedule as a JSON file")
			return exitCode(2)
		}
		return nil
	}
	cmd.AddCommand(show, set, clear)
	return cmd
}

func newGcpWorkloadsPause(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	var reason, until string
	var keepAsleep bool
	cmd := newGcpWorkloadsOneWith("pause", "Pause a workload's naps",
		"Pause a workload: it is not napped, by its idle window, its schedule or 'nap', until it is resumed or --until.\n"+
			"A workload asleep is woken. With --keep-asleep it is not: it stays as it is, a visitor still wakes the workload,\n"+
			"and scheduled and maintenance wakes wait until the pause ends. --until takes a time in RFC 3339 (2026-12-01T08:00:00+01:00), at most 366 days away.",
		func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.Reason, r.KeepAsleep, r.Until = reason, keepAsleep, until
			return run(ctx, d, r)
		}, version, &f, false)
	fl := cmd.Flags()
	fl.StringVar(&reason, "reason", "", "why the workload is paused, at most 256 characters (required)")
	fl.BoolVar(&keepAsleep, "keep-asleep", false, "do not wake a workload asleep: a visitor still wakes it, scheduled and maintenance wakes wait")
	fl.StringVar(&until, "until", "", "end the pause at this time, in RFC 3339 (default: when resumed)")
	prior := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		if strings.TrimSpace(reason) == "" {
			fmt.Fprintln(c.ErrOrStderr(), "--reason: needed, why the workload is paused")
			return exitCode(2)
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
			"Print the Role and RoleBinding dormeto-<workload>, ask (--yes without a terminal), list them in the install record, then write what\n"+
				"differs: run again, it writes nothing. A Role or RoleBinding of that name without the label is refused, nothing changed.\n"+
				"The binding's other subjects stay; another router's service account (dmtr-*@…) among them is refused, nothing changed.\n"+
				"Kubernetes lets no one grant rights they do not hold: without them you need container.roles.escalate and container.roles.bind.",
			runs.accessGrant, version, true),
		newGcpWorkloadsAccessVerb("revoke", "Remove a workload's Role and RoleBinding",
			"Print what it removes, ask (--yes without a terminal), remove the RoleBinding and the Role dormeto-<workload> that are Dormeto's,\n"+
				"and take them out of the install record. One of that name without the label is left and said; such a RoleBinding leaves the\n"+
				"Role too, as it may name it. While another subject holds\n"+
				"the binding, only the router's subject goes, and both objects stay; another router's service account among them is refused,\n"+
				"nothing changed. It works after dormeto gcp uninstall, which lists what it leaves in a cluster.",
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
		fl.BoolVar(&f.yes, "yes", false, "apply without asking (needed without a terminal, and with --json)")
	}
	return cmd
}
