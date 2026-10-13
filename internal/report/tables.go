// The two tables of the totals, shared by the Markdown and the terminal: ports counts, saved, accountsTable and
// costTable of manager packages/estimate/src/report.ts.
package report

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/Dormeto/cli/internal/estimate"
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
	if e.Dormeto.Manager.Count != 0 {
		manager = fmt.Sprintf("; the total includes the manager (%s), once per installation", usd(e.Dormeto.Manager.USDMonth))
	}
	var rows []row
	for _, account := range shown {
		a, _ := e.ByAccount.Get(account)
		rows = append(rows, row{cells: slices.Concat([]string{account, name(account)}, counts(a), []string{usd(a.ActualMonth), usd(a.WithDormetoMonth), saved(a)})})
	}
	tot := e.Totals
	note := fmt.Sprintf("Cost with Dormeto enabled: at a %s idle window, Dormeto's cost included%s.", windowAdjective(e.Options.Window), manager)
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
// the cost with Dormeto applied.
func costTable(e estimate.Estimate) table {
	manager, routers, workloads := e.Dormeto.Manager, e.Dormeto.Routers, e.Dormeto.Workloads
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
	return table{
		head:    []string{"Part", "Computation", "A month"},
		figures: []bool{false, false, true},
		groups: [][]row{
			{
				{cells: []string{"Manager", times(manager) + ": one per installation, whatever the number of accounts and workloads", usd(manager.USDMonth)}},
				{cells: []string{"Routers", times(routers.CostPart) + ": one per account and region with workloads" + where, usd(routers.USDMonth)}},
				{cells: []string{"Workloads", times(workloads) + ": each workload's naps, wakes and idle checks", usd(workloads.USDMonth)}},
			},
			{{cells: []string{"Dormeto's cost", "", usd(e.Dormeto.USDMonth)}, strong: true}},
			{
				{cells: []string{"Actual cost", fmt.Sprintf("what naps would stop in the %s, running %s", n(t.Workloads, "workload"), running), usd(t.ActualMonth)}},
				{cells: []string{"Savings", "at a " + windowAdjective(e.Options.Window) + " idle window", usd(-t.USDMonthWindow)}},
			},
			{{cells: []string{"Cost with Dormeto applied", fmt.Sprintf("%s - %s + %s", usd(t.ActualMonth), usd(t.USDMonthWindow), usd(t.DormetoMonth)), usd(t.WithDormetoMonth)}, strong: true}},
		},
	}
}
