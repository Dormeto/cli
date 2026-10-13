// The terminal's summary and the visual of the cost: ports summaryLines and costVisual of manager
// packages/estimate/src/report.ts.
package report

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/Dormeto/cli/internal/estimate"
)

// Why companion databases were left out, in the summary's words and order.
var leftOutWords = []struct {
	code  estimate.LeftOutCode
	words string
}{
	{estimate.CodeShared, "shared with other workloads"}, {estimate.CodeTags, "excluded by tags"}, {estimate.CodeStoppable, "cannot be stopped"},
	{estimate.CodeWakes, "of a workload waking too often"}, {estimate.CodeStopped, "stopped, not measured"}, {estimate.CodeIdle, "idle for 14 days"},
	{estimate.CodePrice, "without a price"},
}

func net(t estimate.Totals) float64 { return t.USDMonthWindow - t.DormetoMonth }

// Restricted by --accounts: how many of the organization's accounts.
func restricted(e estimate.Estimate) bool {
	return e.Organization != nil && *e.Organization != "" && e.Options.Accounts != nil
}

// Summary is the terminal's summary: the totals, the bars, the tables aligned with borders, the ten workloads saving
// the most, what was left out.
func Summary(e estimate.Estimate) []string {
	t := e.Totals
	accountsIn := n(e.Accounts, "account")
	if restricted(e) {
		accountsIn = fmt.Sprintf("%d of the organization's %s", e.Accounts, n(e.AccountsListed, "account"))
	}
	lines := []string{
		fmt.Sprintf("About %s a month at a %s idle window (%s at the recommended windows) for %s in %s; Dormeto costs about %s a month: net about %s.",
			usd(t.USDMonthWindow), windowAdjective(e.Options.Window), usd(t.USDMonthRecommended), n(t.Workloads, "workload"), accountsIn, usd(t.DormetoMonth), usd(net(t))),
	}
	if e.Why != nil && *e.Why != "" {
		lines = append(lines, *e.Why)
	}
	if visual := costVisual(e); len(visual) > 0 {
		lines = append(lines, "")
		lines = append(lines, visual...)
	}
	if t.Workloads != 0 {
		accounts, more := accountsTable(e, terminalAccounts)
		lines = append(lines, "", "By account:")
		lines = append(lines, textTable(accounts)...)
		if more != 0 {
			lines = append(lines, fmt.Sprintf("  And %s: see the report.", n(more, "more account")))
		}
		lines = append(lines, "", "Dormeto's cost:")
		lines = append(lines, textTable(costTable(e))...)
		lines = append(lines, "")
	}
	top := slices.Clone(e.Workloads)
	slices.SortStableFunc(top, func(a, b estimate.WorkloadEstimate) int { return cmp.Compare(b.AtWindow.USDMonth, a.AtWindow.USDMonth) })
	if len(top) > 10 {
		top = top[:10]
	}
	if len(top) > 0 {
		lines = append(lines, "Workloads that would save the most:")
		for _, x := range top {
			w := x.Workload
			host := w.TargetGroup
			if len(w.Hosts) > 0 {
				host = w.Hosts[0]
			}
			quiet := ""
			if x.Quiet {
				quiet = ", no request in 14 days"
			}
			lines = append(lines, fmt.Sprintf("  %s  %s (%s, %s, %s)%s; recommended %d min: %s",
				padStart(usd(x.AtWindow.USDMonth), 10), host, w.LoadBalancer, w.Region, w.Account, quiet, x.Recommended.Minutes, usd(x.Recommended.USDMonth)))
		}
	}
	if len(e.LeftOut) > 0 {
		lines = append(lines, fmt.Sprintf("Left out: %s (see the report for why)", n(len(e.LeftOut), "workload")))
	}
	if len(e.Excluded) > 0 {
		lines = append(lines, fmt.Sprintf("Excluded by tags: %s (see the report)", n(len(e.Excluded), "workload")))
	}
	if len(e.Busy) > 0 {
		lines = append(lines, fmt.Sprintf("More than %s wakes a day: %s left out (see the report)", nullable(e.Options.MaxWakes), n(len(e.Busy), "workload")))
	}
	if len(e.Small) > 0 {
		minSaving := 0.0
		if e.Options.MinSaving != nil {
			minSaving = *e.Options.MinSaving
		}
		lines = append(lines, fmt.Sprintf("Saving less than %s a month: %s left out (see the report)", usd(minSaving), n(len(e.Small), "workload")))
	}
	attached := 0
	for _, x := range e.Workloads {
		attached += len(x.Databases)
	}
	if attached != 0 || len(e.DatabasesLeftOut) > 0 {
		var why []string
		for _, l := range leftOutWords {
			count := 0
			for _, d := range e.DatabasesLeftOut {
				if d.Code == l.code {
					count++
				}
			}
			if count != 0 {
				why = append(why, fmt.Sprintf("%d %s", count, l.words))
			}
		}
		reasons := ""
		if len(why) > 0 {
			reasons = ": " + strings.Join(why, ", ")
		}
		lines = append(lines, fmt.Sprintf("Companion databases: %d attached, %d left out%s (see the report)", attached, len(e.DatabasesLeftOut), reasons))
	}
	if len(e.Unreachable) > 0 {
		lines = append(lines, fmt.Sprintf("Not reached: %s (see the report for how to include them)", n(len(e.Unreachable), "account")))
	}
	if len(e.FailedRegions) > 0 {
		failed := make([]string, len(e.FailedRegions))
		for i, r := range e.FailedRegions {
			failed[i] = r.Account + "/" + r.Region
		}
		lines = append(lines, "Regions that failed: "+strings.Join(failed, ", "))
	}
	return lines
}

// The visual: the actual cost against the cost with Dormeto, a month, bars on one scale (the larger 40 characters, to an
// eighth), in dollars and percent of the actual cost.
const bar = 40

func costVisual(e estimate.Estimate) []string {
	t := e.Totals
	// Node's !t.actualMonth: zero and NaN are both falsy.
	if t.ActualMonth == 0 || math.IsNaN(t.ActualMonth) {
		return nil
	}
	most := math.Max(t.ActualMonth, t.WithDormetoMonth)
	eighthsOf := []rune("▏▎▍▌▋▊▉")
	barOf := func(v float64) string {
		scaled := jsRound((math.Max(0, v) / most) * bar * 8)
		// A non-finite scale (a NaN total) gives Node an empty bar ('█'.repeat(NaN) is ''); a negative one would throw
		// there, and is an empty bar here.
		if math.IsNaN(scaled) || math.IsInf(scaled, 0) || scaled < 0 {
			return padEnd("", bar)
		}
		eighths := int(scaled)
		s := strings.Repeat("█", eighths/8)
		if eighths%8 != 0 {
			s += string(eighthsOf[eighths%8-1])
		}
		return padEnd(s, bar)
	}
	values := []string{usd(t.ActualMonth), usd(t.WithDormetoMonth)}
	width := max(len16(values[0]), len16(values[1]))
	percent := func(v float64) string { return padStart(percentOf((v/t.ActualMonth)*100), 4) }
	saving := t.ActualMonth - t.WithDormetoMonth
	share := percentOf((math.Abs(saving)/t.ActualMonth)*100) + " of the actual cost"
	last := fmt.Sprintf("  Dormeto would cost %s a month more than it saves, %s", usd(-saving), share)
	if saving >= 0 {
		last = fmt.Sprintf("  Saved: %s a month, %s", usd(saving), share)
	}
	return []string{
		fmt.Sprintf("Monthly cost of the %s estimated, at a %s idle window:", n(t.Workloads, "workload"), windowAdjective(e.Options.Window)),
		fmt.Sprintf("  Actual cost   %s  %s  %s", barOf(t.ActualMonth), padStart(values[0], width), percent(t.ActualMonth)),
		fmt.Sprintf("  With Dormeto  %s  %s  %s", barOf(t.WithDormetoMonth), padStart(values[1], width), percent(t.WithDormetoMonth)),
		last,
	}
}
