// The Markdown report: ports markdownReport, companionLines, uptimeCell, backendCell and target of manager
// packages/estimate/src/report.ts (the sections, tables, columns and texts are Node's).
package report

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/estimate"
	"github.com/Dormeto/cli/internal/options"
)

// The share of the day it ran, measured; else all the time, assumed (with why when its metric could not be read).
func uptimeCell(u *estimate.Uptime) string {
	switch {
	case u == nil:
		return "100% assumed"
	case u.HoursPerDay != nil:
		return percentOf((*u.HoursPerDay / 24) * 100)
	case u.Unreadable != nil:
		return "100% assumed (uptime unreadable: " + *u.Unreadable + ")"
	}
	return "100% assumed (uptime unreadable: undefined)"
}

// managedMark marks a workload Dormeto already manages.
func managedMark(w discover.Workload) string {
	if w.Managed {
		return " (managed by Dormeto)"
	}
	return ""
}

func backendCell(w discover.Workload) string {
	return w.Backend.Kind + " " + cell(lastSegment(w.Backend.ID))
}

func target(e estimate.Estimate) string {
	if e.Organization != nil && *e.Organization != "" {
		accounts := n(e.Accounts, "account")
		if restricted(e) {
			accounts = fmt.Sprintf("%d of its %s (--accounts)", e.Accounts, n(e.AccountsListed, "account"))
		}
		return fmt.Sprintf("Organization %s, %s", *e.Organization, accounts)
	}
	if len(e.Workloads) > 0 {
		return strings.TrimSpace("Account " + e.Workloads[0].Workload.Account)
	}
	return "Account"
}

// The companion databases attached (their saving, included in their workload's), those left out with why, and the
// regions whose databases could not be read.
func companionLines(e estimate.Estimate) []string {
	type attachedDB struct {
		x estimate.WorkloadEstimate
		d estimate.Companion
	}
	var attached []attachedDB
	for _, x := range e.Workloads {
		for _, d := range x.Databases {
			attached = append(attached, attachedDB{x, d})
		}
	}
	if len(attached) == 0 && len(e.DatabasesLeftOut) == 0 && len(e.DatabasesUnread) == 0 {
		return nil
	}
	where := func(w discover.Workload) string {
		return fmt.Sprintf("| %s | %s | %s |", w.Account, w.Region, cell(hostsOr(w.Hosts, w.TargetGroup)))
	}
	out := []string{
		"## Companion databases", "",
		"Found by security group: a database whose security group lets a workload's in naps with it (RDS instances and Aurora provisioned clusters, not yet Aurora Serverless); its saving is in the workload's.", "",
	}
	if len(attached) > 0 {
		out = append(out, "| Account | Region | Workload | Database | Uptime | Saving |", "|---|---|---|---|---|---|")
		for _, a := range attached {
			out = append(out, fmt.Sprintf("%s %s %s | %s | %s |", where(a.x.Workload), a.d.Kind, cell(a.d.ID), cell(uptimeCell(a.d.Uptime)), usd(a.d.RecommendedUSDMonth)))
		}
		out = append(out, "")
	}
	if len(e.DatabasesLeftOut) > 0 {
		out = append(out, "### Left out", "", "| Account | Region | Workload | Database | Why |", "|---|---|---|---|---|")
		for _, l := range e.DatabasesLeftOut {
			out = append(out, fmt.Sprintf("%s %s %s | %s |", where(l.Workload), l.Database.Kind, cell(l.Database.ID), cell(l.Reason)))
		}
		out = append(out, "")
	}
	if len(e.DatabasesUnread) > 0 {
		for _, u := range e.DatabasesUnread {
			out = append(out, fmt.Sprintf("- %s %s: not read (%s)", u.Account, u.Region, u.Reason))
		}
		out = append(out, "")
	}
	return out
}

// Markdown is the Markdown report, readable as it is (to paste into a proposal).
func Markdown(e estimate.Estimate) string {
	t, o := e.Totals, e.Options
	generated := e.GeneratedAt
	if len(generated) > 16 {
		generated = generated[:16]
	}
	generated = strings.Replace(generated, "T", " ", 1)
	out := []string{
		"# Dormeto savings estimate", "",
		fmt.Sprintf("%s, estimated %s UTC from the last 14 days of requests.", target(e), generated), "",
	}
	if e.Why != nil && *e.Why != "" {
		out = append(out, "> "+*e.Why, "")
	}
	out = append(out,
		"## Totals", "",
		fmt.Sprintf("Dormeto would save about **%s a month** with a %s idle window (%s with each workload's recommended window), for %s. Dormeto itself costs about %s a month: **net about %s**.",
			usd(t.USDMonthWindow), windowAdjective(o.Window), usd(t.USDMonthRecommended), n(t.Workloads, "workload"), usd(t.DormetoMonth), usd(net(t))), "",
	)
	if visual := costVisual(e); len(visual) > 0 {
		out = append(out, "```text")
		out = append(out, visual...)
		out = append(out, "```", "")
	}
	accounts, _ := accountsTable(e, -1)
	out = append(out, "### By account", "")
	out = append(out, markdownTable(accounts)...)
	out = append(out, "", "### Dormeto's cost", "")
	out = append(out, markdownTable(costTable(e))...)
	out = append(out, "", "## Workloads", "",
		"| Account | Region | Load balancer | Hosts | Backend | Uptime | Recommended | Saving | Wakes a day |", "|---|---|---|---|---|---|---|---|---|")
	byRecommended := slices.Clone(e.Workloads)
	slices.SortStableFunc(byRecommended, func(a, b estimate.WorkloadEstimate) int {
		return cmp.Compare(b.Recommended.USDMonth, a.Recommended.USDMonth)
	})
	for _, x := range byRecommended {
		w := x.Workload
		hosts := hostsOr(w.Hosts, w.TargetGroup)
		if x.Quiet {
			hosts += " (no request in 14 days)"
		}
		hosts += managedMark(w)
		databases := ""
		for _, d := range x.Databases {
			databases += " + " + d.Kind + " " + d.ID
		}
		out = append(out, fmt.Sprintf("| %s | %s | %s | %s | %s %s%s | %s | %d min | %s | %s |",
			w.Account, w.Region, cell(w.LoadBalancer), cell(hosts), w.Backend.Kind, cell(lastSegment(w.Backend.ID)), cell(databases),
			cell(uptimeCell(x.Uptime)), x.Recommended.Minutes, usd(x.Recommended.USDMonth), day(x.Recommended.WakesPerDay)))
	}
	out = append(out, "", "## Left out", "")
	if len(e.LeftOut) > 0 {
		out = append(out, "| Account | Region | Load balancer | Target group | Why |", "|---|---|---|---|---|")
		for _, l := range e.LeftOut {
			out = append(out, fmt.Sprintf("| %s | %s | %s | %s | %s |", l.Workload.Account, l.Workload.Region, cell(l.Workload.LoadBalancer), cell(l.Workload.TargetGroup+managedMark(l.Workload)), cell(l.Reason)))
		}
	} else {
		out = append(out, "None.")
	}
	out = append(out, "")
	if len(e.Busy) > 0 {
		out = append(out, fmt.Sprintf("## More than %s wakes a day", nullable(o.MaxWakes)), "",
			fmt.Sprintf("Left out: even at its recommended window, each would wake its visitors more than %s times a day (--max-wakes).", nullable(o.MaxWakes)), "",
			"| Account | Region | Load balancer | Hosts | Backend | Recommended | Wakes a day | Saving left out |", "|---|---|---|---|---|---|---|---|")
		for _, b := range e.Busy {
			out = append(out, fmt.Sprintf("| %s | %s | %s | %s | %s | %d min | %s | %s |", b.Workload.Account, b.Workload.Region, cell(b.Workload.LoadBalancer),
				cell(hostsOr(b.Workload.Hosts, b.Workload.TargetGroup)+managedMark(b.Workload)), backendCell(b.Workload), b.Minutes, day(b.WakesPerDay), usd(b.USDMonth)))
		}
		out = append(out, "")
	}
	if len(e.Small) > 0 {
		minSaving := 0.0
		if o.MinSaving != nil {
			minSaving = *o.MinSaving
		}
		out = append(out, fmt.Sprintf("## Saving less than %s a month", usd(minSaving)), "",
			fmt.Sprintf("Left out: at its recommended window, each would save less than %s a month, not worth installing Dormeto for (--min-saving).", usd(minSaving)), "",
			"| Account | Region | Load balancer | Hosts | Backend | Recommended | Saving left out |", "|---|---|---|---|---|---|---|")
		for _, x := range e.Small {
			out = append(out, fmt.Sprintf("| %s | %s | %s | %s | %s | %d min | %s |", x.Workload.Account, x.Workload.Region, cell(x.Workload.LoadBalancer),
				cell(hostsOr(x.Workload.Hosts, x.Workload.TargetGroup)+managedMark(x.Workload)), backendCell(x.Workload), x.Minutes, usd(x.USDMonth)))
		}
		out = append(out, "")
	}
	out = append(out, companionLines(e)...)
	if len(o.ExcludeTags) > 0 {
		out = append(out, "## Excluded by tags", "")
		if len(e.Excluded) > 0 {
			out = append(out, "| Account | Region | Load balancer | Target group | Hosts | Tags matched |", "|---|---|---|---|---|---|")
			for _, x := range e.Excluded {
				out = append(out, fmt.Sprintf("| %s | %s | %s | %s | %s | %s |", x.Workload.Account, x.Workload.Region, cell(x.Workload.LoadBalancer),
					cell(x.Workload.TargetGroup), cell(strings.Join(x.Workload.Hosts, ", ")+managedMark(x.Workload)), cell(x.Criterion)))
			}
		} else {
			out = append(out, "None.")
		}
		out = append(out, "")
	}
	out = append(out, "## Not eligible", "")
	if len(e.Ineligible) > 0 {
		out = append(out, "| Account | Region | Load balancer | Target group | Why |", "|---|---|---|---|---|")
		for _, i := range e.Ineligible {
			targetGroup := ""
			if i.TargetGroup != nil {
				targetGroup = *i.TargetGroup
			}
			out = append(out, fmt.Sprintf("| %s | %s | %s | %s | %s |", i.Account, i.Region, cell(i.LoadBalancer), cell(targetGroup), cell(i.Reason)))
		}
	} else {
		out = append(out, "None.")
	}
	out = append(out, "")
	if len(e.UnreadableClusters) > 0 {
		out = append(out, "## Clusters not read", "", "| Account | Region | Cluster | Why |", "|---|---|---|---|")
		for _, c := range e.UnreadableClusters {
			out = append(out, fmt.Sprintf("| %s | %s | %s | %s |", c.Account, c.Region, cell(c.Name), cell(c.Reason)))
		}
		out = append(out, "")
	}
	out = append(out, "## Accounts not reached", "")
	if len(e.Unreachable) > 0 {
		out = append(out, "| Account | Name | Why | To include it |", "|---|---|---|---|")
		for _, u := range e.Unreachable {
			out = append(out, fmt.Sprintf("| %s | %s | %s | %s |", u.Account, cell(u.Name), cell(u.Reason), cell(u.Fix)))
		}
	} else {
		out = append(out, "None.")
	}
	out = append(out, "", "## Regions that failed", "")
	if len(e.FailedRegions) > 0 {
		for _, r := range e.FailedRegions {
			out = append(out, fmt.Sprintf("- %s %s: %s", r.Account, r.Region, r.Error))
		}
	} else {
		out = append(out, "None.")
	}
	out = append(out, "", "## Options", "")
	out = append(out, optionLines(o)...)
	out = append(out, "", "## Assumptions", "")
	for _, a := range e.Assumptions {
		out = append(out, "- "+a)
	}
	out = append(out, "")
	return strings.Join(out, "\n")
}

// The options section's lines.
func optionLines(o options.Options) []string {
	accounts := "every account reached"
	if o.Accounts != nil {
		accounts = strings.Join(o.Accounts, ", ") + " (--accounts)"
	}
	regions := "each account's enabled regions"
	if o.Regions != nil {
		regions = strings.Join(o.Regions, ", ")
	}
	ec2 := "24/7 assumed (--ec2-uptime measures it)"
	if o.Ec2Uptime {
		ec2 = "measured over the 14 days (--ec2-uptime)"
	}
	ecs := "at the desired count when discovered, all the time (--ecs-uptime measures it)"
	if o.EcsUptime {
		ecs = "measured over the 14 days (--ecs-uptime)"
	}
	rds := "companion databases available all the time assumed, one stopped left out (--rds-uptime measures it)"
	if o.RdsUptime {
		rds = "companion databases measured over the 14 days (--rds-uptime)"
	}
	excluded := "none"
	if len(o.ExcludeTags) > 0 {
		described := make([]string, len(o.ExcludeTags))
		for i, c := range o.ExcludeTags {
			described[i] = estimate.DescribeTags(c)
		}
		excluded = strings.Join(described, "; or ")
	}
	baseline := "business hours, " + o.Baseline.TimeZone
	if o.Baseline.Kind == "always" {
		baseline = "24/7"
	}
	wakes := "any (--max-wakes none)"
	if o.MaxWakes != nil {
		wakes = fmt.Sprintf("at most %s at each workload's recommended window, the busier left out (--max-wakes)", estimate.JSNumber(*o.MaxWakes))
	}
	minSaving := "none (--min-saving none)"
	if o.MinSaving != nil {
		minSaving = fmt.Sprintf("%s a month at each workload's recommended window, the smaller left out (--min-saving)", usd(*o.MinSaving))
	}
	rdsWakes := "whatever their workload's wakes (--max-wakes-rds none)"
	if o.MaxWakesRds != nil {
		rdsWakes = fmt.Sprintf("left out when their workload wakes more than %s times a day at its recommended window (--max-wakes-rds)", estimate.JSNumber(*o.MaxWakesRds))
	}
	return []string{
		"- Accounts: " + accounts,
		"- Regions: " + regions,
		"- EC2 uptime: " + ec2,
		"- ECS uptime: " + ecs,
		"- RDS uptime: " + rds,
		"- Excluded tags: " + excluded,
		"- Role in member accounts: " + o.Role,
		"- Baseline: " + baseline,
		fmt.Sprintf("- Discounts: compute %s%%, databases %s%%", estimate.JSNumber(o.Discounts.Compute), estimate.JSNumber(o.Discounts.Databases)),
		"- Idle window of the figures: " + windowWords(o.Window) + " (the recommended windows besides)",
		"- Wakes a day: " + wakes,
		"- Minimum saving: " + minSaving,
		"- Companion databases: found by security group, " + rdsWakes,
	}
}
