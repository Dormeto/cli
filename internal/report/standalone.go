// The standalone resources in the reports (vision standalone workloads design §7, --standalone on): the totals for
// HTTP, standalone and both, the assumption once beside them, the resources estimated with their kind, those left out
// with why, and what the scan could not read. With --standalone off the reports are as before them.
package report

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/estimate"
)

// The Kind column's words.
const (
	kindHTTP       = "http"
	kindStandalone = "standalone"
)

// estimatedWhat is what the figures are of: "3 workloads", or with the standalone resources "3 workloads and 2
// standalone resources".
func estimatedWhat(e estimate.Estimate) string {
	if e.Standalone == nil {
		return n(e.Totals.Workloads, "workload")
	}
	return n(e.Totals.Workloads, "workload") + " and " + n(e.Totals.Standalone, "standalone resource")
}

// figuresAt is figuresWindow, with the standalone resources' window after it.
func figuresAt(e estimate.Estimate) string {
	if e.Standalone == nil {
		return figuresWindow(e.Options)
	}
	return figuresWindow(e.Options) + " (standalone resources: a " + windowAdjective(e.Standalone.Window) + " idle window)"
}

// resourceCell is a resource as the tables say it: its kind in the router's words, its name, and its id when the name
// is not it.
func resourceCell(r estimate.ResourceRef) string {
	kind := discover.ResourceKindText[r.Kind]
	if kind == "" {
		kind = r.Kind
	}
	id := lastSegment(r.ID)
	if r.Name == "" || r.Name == id {
		return kind + " " + id
	}
	return kind + " " + r.Name + " (" + id + ")"
}

func managedResource(r estimate.ResourceRef) string {
	if r.Managed {
		return " (managed by Dormeto)"
	}
	return ""
}

// splitTable is the totals for HTTP, standalone and both.
func splitTable(e estimate.Estimate) table {
	s := e.Standalone.Totals
	line := func(label string, t estimate.SplitTotal, strong bool) row {
		return row{cells: []string{label, num(t.Count), usd(t.ActualMonth), usd(t.USDMonthWindow), usd(t.USDMonthRecommended)}, strong: strong}
	}
	note := "Savings at the report's windows: HTTP workloads at " + figuresWindow(e.Options) + ", standalone resources at a " + windowAdjective(e.Standalone.Window) + " idle window (--standalone-window)."
	return table{
		head:    []string{"Kind", "Counted", "Actual monthly cost", "Savings", "Savings at the recommended windows"},
		figures: []bool{false, true, true, true, true},
		groups:  [][]row{{line("HTTP", s.HTTP, false), line("Standalone", s.Standalone, false)}, {line("Both", s.Both, true)}},
		note:    &note,
	}
}

// standaloneTotals are the Markdown's totals for HTTP, standalone and both, and the assumption, once.
func standaloneTotals(e estimate.Estimate) []string {
	if e.Standalone == nil {
		return nil
	}
	out := []string{"", "### HTTP and standalone", ""}
	out = append(out, markdownTable(splitTable(e))...)
	return append(out, "", e.Standalone.Assumption)
}

// standaloneLines are the Markdown's standalone resources, those left out and what the scan could not read.
func standaloneLines(e estimate.Estimate) []string {
	p := e.Standalone
	if p == nil {
		return nil
	}
	out := []string{"## Standalone resources", "",
		fmt.Sprintf("Resources no route uses (EC2 instances, Auto Scaling groups, ECS services, RDS instances and clusters), idle by their default signals; their figures at a %s idle window (--standalone-window) and at each one's recommended window.", windowAdjective(p.Window)), ""}
	if len(p.Resources) > 0 {
		out = append(out, "| Kind | Account | Region | Resource | Ran | Recommended | Saving | Saving at the recommended window | Starts a day |", "|---|---|---|---|---|---|---|---|---|")
		sorted := slices.Clone(p.Resources)
		slices.SortStableFunc(sorted, func(a, b estimate.ResourceEstimate) int { return cmp.Compare(b.AtWindow.USDMonth, a.AtWindow.USDMonth) })
		for _, x := range sorted {
			r := x.Resource
			out = append(out, fmt.Sprintf("| %s | %s | %s | %s | %s | %d min | %s | %s | %s |", kindStandalone, r.Account, r.Region, cell(resourceCell(r)+managedResource(r)),
				percentOf((x.RanHoursPerDay/24)*100), x.Recommended.Minutes, usd(x.AtWindow.USDMonth), usd(x.Recommended.USDMonth), day(x.AtWindow.WakesPerDay)))
		}
	} else {
		out = append(out, "None.")
	}
	out = append(out, "")
	if len(p.LeftOut) > 0 {
		out = append(out, "### Left out", "", "| Account | Region | Resource | Why |", "|---|---|---|---|")
		for _, l := range p.LeftOut {
			r := l.Resource
			out = append(out, fmt.Sprintf("| %s | %s | %s | %s |", r.Account, r.Region, cell(resourceCell(r)+managedResource(r)), cell(l.Reason)))
		}
		out = append(out, "")
	}
	if len(p.Unread) > 0 {
		for _, u := range p.Unread {
			out = append(out, fmt.Sprintf("- %s %s: %s", u.Account, u.Region, u.Reason))
		}
		out = append(out, "")
	}
	return out
}

// standaloneSummary is the terminal's lines of the standalone resources: the totals for HTTP, standalone and both, the
// assumption, those saving the most and how many are left out.
func standaloneSummary(e estimate.Estimate) []string {
	p := e.Standalone
	if p == nil {
		return nil
	}
	out := []string{"", "HTTP and standalone:"}
	out = append(out, textTable(splitTable(e))...)
	out = append(out, p.Assumption, "")
	top := slices.Clone(p.Resources)
	slices.SortStableFunc(top, func(a, b estimate.ResourceEstimate) int { return cmp.Compare(b.AtWindow.USDMonth, a.AtWindow.USDMonth) })
	if len(top) > 10 {
		top = top[:10]
	}
	if len(top) > 0 {
		out = append(out, "Standalone resources that would save the most:")
		for _, x := range top {
			r := x.Resource
			out = append(out, fmt.Sprintf("  %s  %s (%s, %s); at its recommended %s window: %s", padStart(usd(x.AtWindow.USDMonth), 10), resourceCell(r), r.Region, r.Account,
				windowAdjective(x.Recommended.Minutes), usd(x.Recommended.USDMonth)))
		}
	}
	if len(p.LeftOut) > 0 {
		out = append(out, fmt.Sprintf("Standalone resources left out: %d (see the report for why)", len(p.LeftOut)))
	}
	for _, u := range p.Unread {
		out = append(out, fmt.Sprintf("Standalone scan of %s %s: %s", u.Account, u.Region, u.Reason))
	}
	return out
}
