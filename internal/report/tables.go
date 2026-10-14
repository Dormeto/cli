// The two tables of the totals, shared by the Markdown and the terminal: ports counts, saved, accountsTable and
// costTable of manager packages/estimate/src/report.ts.
package report

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/Dormeto/cli/internal/estimate"
	"github.com/Dormeto/cli/internal/options"
)

// The workloads, then by backend: Auto Scaling groups, EC2 instances, ECS services, Kubernetes owners on EKS.
func counts(t estimate.Totals) []string {
	return []string{num(t.Workloads), num(t.Kinds.ASG), num(t.Kinds.EC2), num(t.Kinds.ECS), num(t.Kinds.EKS), num(t.Databases), num(t.Excluded)}
}

// What Dormeto would save of an actual cost, cost with Dormeto included.
func saved(t estimate.Totals) string {
	if t.ActualMonth == 0 {
		return ""
	}
	return percentOf(((t.ActualMonth - t.WithDormetoMonth) / t.ActualMonth) * 100)
}

// The terminal's accounts: those saving the most.
const terminalAccounts = 10

// accountsTable is the accounts, a row each (at most limit, those saving the most; limit < 0: all), and the total (the
// manager in its cost with Dormeto). more is how many accounts the table leaves out.
func accountsTable(e estimate.Estimate, limit int) (t table, more int) {
	name := func(account string) string {
		if v, ok := e.AccountNames.Get(account); ok && v != "" && v != account {
			return v
		}
		return ""
	}
	keys := e.ByAccount.Keys()
	shown := keys
	if limit >= 0 && limit < len(keys) {
		shown = slices.Clone(keys)
		slices.SortStableFunc(shown, func(a, b string) int {
			x, _ := e.ByAccount.Get(a)
			y, _ := e.ByAccount.Get(b)
			return cmp.Compare(y.USDMonthWindow, x.USDMonthWindow)
		})
		shown = shown[:limit]
	}
	manager := ""
	if e.Dormeto.Manager.Count != 0 && (e.Dormeto.Change == nil || !e.Dormeto.Change.ManagerRemoved) {
		manager = fmt.Sprintf("; the total includes the manager (%s), once per installation", usd(e.Dormeto.Manager.USDMonth))
	}
	var rows []row
	for _, account := range shown {
		a, _ := e.ByAccount.Get(account)
		rows = append(rows, row{cells: slices.Concat([]string{account, name(account)}, counts(a), []string{usd(a.ActualMonth), usd(a.WithDormetoMonth), saved(a)})})
	}
	tot := e.Totals
	note := fmt.Sprintf("Cost with Dormeto enabled: at %s, Dormeto's cost included%s.", figuresWindow(e.Options), manager)
	return table{
		head:    []string{"Account", "Name", "Workloads", "ASG", "EC2", "ECS", "EKS", "RDS", "Excluded", "Actual monthly cost", "Cost with Dormeto enabled", "Savings"},
		figures: []bool{false, false, true, true, true, true, true, true, true, true, true, true},
		groups: [][]row{
			rows,
			{{cells: slices.Concat([]string{"Total", ""}, counts(tot), []string{usd(tot.ActualMonth), usd(tot.WithDormetoMonth), saved(tot)}), strong: true}},
		},
		note: &note,
	}, len(keys) - len(shown)
}

// Each account and region with a router, while they are few enough to read.
const routersListed = 6

// costTable is Dormeto's cost by part, each with its computation, and its total; then the actual cost, the savings and
// the cost with Dormeto applied. Dormeto installed (Go only): the parts are the installation now, then the
// recommendation's change, signed.
func costTable(e estimate.Estimate) table {
	manager, routers, workloads, change := e.Dormeto.Manager, e.Dormeto.Routers, e.Dormeto.Workloads, e.Dormeto.Change
	t := e.Totals
	times := func(p estimate.CostPart) string { return fmt.Sprintf("%d × %s", p.Count, usd(p.UnitMonth)) }
	where := ""
	if len(routers.Where) > 0 && len(routers.Where) <= routersListed {
		places := make([]string, len(routers.Where))
		for i, w := range routers.Where {
			places[i] = w.Account + " " + w.Region
		}
		where = " (" + strings.Join(places, ", ") + ")"
	}
	running := "24/7"
	if e.Options.Baseline.Kind != "always" {
		running = "in business hours (" + e.Options.Baseline.TimeZone + ")"
	}
	parts := []row{
		{cells: []string{"Manager", times(manager) + ": one per installation, whatever the number of accounts and workloads", usd(manager.USDMonth)}},
		{cells: []string{"Routers", times(routers.CostPart) + ": one per account and region with workloads" + where, usd(routers.USDMonth)}},
		{cells: []string{"Workloads", times(workloads) + ": each workload's naps, wakes and idle checks", usd(workloads.USDMonth)}},
	}
	if change != nil {
		estimated := 0
		for _, x := range e.Workloads {
			if x.Workload.Managed {
				estimated++
			}
		}
		notEstimated := ""
		if n := workloads.Count - estimated; n > 0 {
			notEstimated = fmt.Sprintf(" (%d of them not estimated)", n)
		}
		parts = []row{
			parts[0],
			{cells: []string{"Routers", times(routers.CostPart) + ": one per account and region with workloads onboarded now" + where, usd(routers.USDMonth)}},
			{cells: []string{"Workloads", times(workloads) + ": each workload onboarded now: its naps, wakes and idle checks" + notEstimated, usd(workloads.USDMonth)}},
			{cells: []string{"New recommendation", changeText(*change, e.Dormeto), signed(change.USDMonth)}},
		}
	}
	return table{
		head:    []string{"Part", "Computation", "A month"},
		figures: []bool{false, false, true},
		groups: [][]row{
			parts,
			{{cells: []string{"Dormeto's cost", "", usd(e.Dormeto.USDMonth)}, strong: true}},
			{
				{cells: []string{"Actual cost", fmt.Sprintf("what naps would stop in the %s, running %s", n(t.Workloads, "workload"), running), usd(t.ActualMonth)}},
				{cells: []string{"Savings", savingsAt(e.Options), usd(-t.USDMonthWindow)}},
			},
			{{cells: []string{"Cost with Dormeto applied", fmt.Sprintf("%s - %s + %s", usd(t.ActualMonth), usd(t.USDMonthWindow), usd(t.DormetoMonth)), usd(t.WithDormetoMonth)}, strong: true}},
		},
	}
}

// changeText says the recommendation's change, each count with its unit cost: the workloads to onboard, the onboarded
// ones each filter leaves, the routers added and removed, the manager removed.
func changeText(c estimate.Change, d estimate.DormetoCost) string {
	routers := func(n int) string {
		if n == 1 {
			return "router"
		}
		return "routers"
	}
	var items []string
	add := func(sign string, n int, unit float64, what string) {
		if n != 0 {
			items = append(items, fmt.Sprintf("%s%d × %s %s", sign, n, usd(unit), what))
		}
	}
	add("+", c.Onboard, d.Workloads.UnitMonth, "to onboard")
	add("-", c.Excluded, d.Workloads.UnitMonth, "left by --exclude-tags")
	add("-", c.Busy, d.Workloads.UnitMonth, "left by --max-wakes")
	add("-", c.Small, d.Workloads.UnitMonth, "left by --min-saving")
	add("+", c.RoutersAdded, d.Routers.UnitMonth, routers(c.RoutersAdded)+" added")
	add("-", c.RoutersRemoved, d.Routers.UnitMonth, routers(c.RoutersRemoved)+" removed")
	if c.ManagerRemoved {
		add("-", 1, d.Manager.UnitMonth, "manager removed (no workload left)")
	}
	if len(items) == 0 {
		return "0 to onboard, 0 left by the filters, no router more or less"
	}
	return strings.Join(items, ", ")
}

// signed is an amount with its sign, a plus too.
func signed(v float64) string {
	if v > 0 {
		return "+" + usd(v)
	}
	return usd(v)
}

// savingsAt is the Savings row's computation: at --window, else at the recommended windows.
func savingsAt(o options.Options) string {
	if o.Window == nil {
		return "at the recommended idle windows"
	}
	return "at a " + windowAdjective(*o.Window) + " idle window"
}
