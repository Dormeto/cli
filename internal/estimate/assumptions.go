// What the estimate assumes, as the reports say it.
//
// Ports manager packages/estimate/src/estimate.ts (assumptionsOf).
package estimate

import (
	"github.com/Dormeto/cli/internal/options"
	"github.com/Dormeto/cli/internal/prices"
)

// assumptionsOf: the Kubernetes one only when a Kubernetes workload was discovered.
func assumptionsOf(baseline prices.Baseline, o options.Options, kubernetes bool) []string {
	within := "every hour (24/7)"
	if baseline.Kind != "always" {
		within = "business hours (weekdays 08:00 to 20:00, " + baseline.TimeZone + ")"
	}
	ec2 := "EC2 instances without an Auto Scaling group are assumed to run 24/7 (--ec2-uptime measures it): one stopped at night by a schedule saves less than estimated here."
	if o.Ec2Uptime {
		ec2 = "EC2 instances without an Auto Scaling group: their uptime measured over the 14 days (CloudWatch CPUUtilization, published every 5 minutes while an instance runs); their actual cost counts the hours they ran, their savings only the hours Dormeto would nap them while they ran. Instances Dormeto already manages and Auto Scaling groups are assumed to run all the time."
	}
	ecs := "ECS services are assumed to run all the time at their desired count when discovered (--ecs-uptime measures when they had a task): one scaled to 0 at night by a schedule saves less than estimated here."
	if o.EcsUptime {
		ecs = "ECS services: their uptime measured over the 14 days (CloudWatch CPUUtilization of the service, published every minute while it has a task); their actual cost counts the hours they had a task, their savings only the hours Dormeto would nap them then, at their desired count when discovered (one task for a service found at 0). Services Dormeto already manages are assumed to run all the time."
	}
	out := []string{
		"Every request counts as activity and as a wake: no waker is installed yet to set scanners and other filtered requests apart, so the workloads stay awake longer here than they would (smaller savings).",
		"A wake lasts 60 seconds in the replay; the visitors' wait is not estimated before install.",
		"Databases are not attached to load balancers: they are not counted (an installed router naps the ones attached to a workload).",
		"Prices: AWS's on-demand list prices, Spot prices for Spot capacity, less the discounts given (compute " + JSNumber(o.Discounts.Compute) + "%, databases " + JSNumber(o.Discounts.Databases) + "%); storage, load balancers and data transfer are not counted.",
		"Savings counted within " + within + ".",
		ec2,
		ecs,
		"Companion databases are found by security group (a database whose security group lets a workload's in), RDS instances and Aurora provisioned clusters, not yet Aurora Serverless; one another workload reaches, a read replica or one with replicas is left out; with --rds-uptime their CPUUtilization says when they were available (one stopped when discovered, started by a schedule, included), else they are assumed available all the time and one stopped is left out; their storage keeps billing, and a database takes minutes to start (its workload's wakes are held that long, not counted here).",
	}
	if kubernetes {
		out = append(out, "Kubernetes pods: their share of their nodes (the larger of CPU and memory over the node's allocatable) at the node's price, saved only where a node autoscaler removes the nodes they free; the EKS control plane and the nodes' DaemonSets keep running.")
	}
	cost := prices.DormetoCost
	return append(out, "Dormeto's cost: about $"+JSNumber(cost.Manager)+" a month for the manager, $"+JSNumber(cost.Router)+" per account and region with workloads, $"+JSNumber(cost.Workload)+" per workload.")
}
