package main

// `dormeto gcp workloads list|show|onboard|offboard|nap|wake`: the flags read into a gcponboard.Request, as the other gcp
// commands. The workload's ID of show, offboard, nap and wake is their argument.

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/gcponboard"
)

// gcpWorkloadRuns are the runs of the gcp workloads commands.
type gcpWorkloadRuns struct{ list, show, onboard, offboard, nap, wake gcpRun }

var theGcpWorkloadRuns = gcpWorkloadRuns{list: gcponboard.WorkloadsList, show: gcponboard.WorkloadsShow, onboard: gcponboard.WorkloadsOnboard,
	offboard: gcponboard.WorkloadsOffboard, nap: gcponboard.WorkloadsNap, wake: gcponboard.WorkloadsWake}

func newGcpWorkloads(runs gcpWorkloadRuns, version string) *cobra.Command {
	cmd := &cobra.Command{Use: "workloads", Short: "Workloads of a Dormeto router in Google Cloud", Args: noArgs, SilenceErrors: true,
		Long: "List, show, onboard, offboard, nap and wake the workloads of the router dmtr-<region>, through its controller\n" +
			"with your identity token (the Cloud Run invoker role on it)."}
	cmd.AddCommand(
		newGcpWorkloadsList(runs.list, version),
		newGcpWorkloadsOne("show", "Show a workload", "Show a workload: its state, URL map, backend service, settings and backends.", runs.show, version),
		newGcpWorkloadsOnboard(runs.onboard, version),
		newGcpWorkloadsOne("offboard", "Offboard a workload and remove its tag bindings",
			"Offboard a workload: the controller wakes it if needed and restores every reference, then the tag dormeto=managed is\n"+
				"unbound from its VMs and groups, as you.", runs.offboard, version),
		newGcpWorkloadsOne("nap", "Put a workload to sleep now", "Nap a workload now, without waiting for its idle window.", runs.nap, version),
		newGcpWorkloadsOne("wake", "Wake a workload now", "Wake a workload now.", runs.wake, version))
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
func newGcpWorkloadsOne(verb, short, long string, run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	var id string
	cmd := gcpCommand(verb+" <workload>", short, long, &f, []string{"project", "region"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
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
	gcpRouterFlags(cmd, &f)
	return cmd
}

func newGcpWorkloadsOnboard(run gcpRun, version string) *cobra.Command {
	var f gcpFlags
	var urlMap, backendService, id, hibernate string
	var idle, wait int
	var hibernating *bool
	var idleGiven, waitGiven bool
	cmd := gcpCommand("onboard", "Onboard a backend service as a workload",
		"Onboard a backend service of a global external Application Load Balancer as a workload. The controller checks the\n"+
			"eligibility first and names the VMs and managed groups; the tag dormeto=managed is then bound to each of them as you\n"+
			"(the router holds no permission to tag), and the workload is onboarded awake. --id defaults to the backend service's\n"+
			"name when that is a valid ID (lowercase letters, digits and dashes, at most 40 characters), else it is asked on a\n"+
			"terminal and needed otherwise. --dry-run stops after the eligibility check.",
		&f, []string{"project", "region", "url-map", "backend-service"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.URLMap, r.BackendService, r.ID, r.Hibernate = urlMap, backendService, id, hibernating
			if idleGiven {
				r.IdleMinutes = &idle
			}
			if waitGiven {
				r.WaitSeconds = &wait
			}
			return run(ctx, d, r)
		})
	prior := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		idleGiven, waitGiven = c.Flags().Changed("idle-minutes"), c.Flags().Changed("wait-seconds")
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
	fl.IntVar(&idle, "idle-minutes", 0, "the idle window, 5 to 1440 minutes (default: the router's, 15)")
	fl.IntVar(&wait, "wait-seconds", 0, "how long a visitor's wake waits, 5 to 120 seconds (default: the router's, 60)")
	fl.StringVar(&hibernate, "hibernate", "", "suspend instead of stopping the VMs that can be suspended: true or false")
	fl.BoolVar(&f.dryRun, "dry-run", false, "check the eligibility and change nothing")
	return cmd
}
