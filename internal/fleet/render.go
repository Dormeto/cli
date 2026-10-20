package fleet

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/install"
)

// none stands for a value the manager does not have.
const none = "—"

// Outdated tells whether the router runs a version below the one available (a pre-release is below its release; an
// unknown version, or none available, is not outdated).
func (r Router) Outdated(available string) bool {
	return r.Version != "" && available != "" && install.CompareVersions(r.Version, available) < 0
}

func orNone(s string) string {
	if s == "" {
		return none
	}
	return s
}

// states renders per-state counts, by state name: "3 ASLEEP, 1 AWAKE".
func states(m map[string]int) string {
	var parts []string
	for _, k := range sortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%d %s", m[k], k))
	}
	return strings.Join(parts, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// RoutersTable is the routers list: the version as "from → to" when below the one available, "upgrading" while an
// upgrade runs, "?" when unknown; the workloads as their total and their count per state.
func RoutersTable(rs []Router, available string) string {
	rows := [][]string{{"NAME", "ACCOUNT", "REGION", "SOURCE", "VERSION", "HEALTH", "WORKLOADS"}}
	for _, r := range rs {
		version := r.Version
		switch {
		case r.Operation != nil && r.Operation.Type == "upgrade-router":
			version = "upgrading"
		case r.Version == "":
			version = "?"
		case r.Outdated(available):
			version += " → " + available
		}
		count := none // a router that did not answer has no count
		if r.Error == "" {
			count = fmt.Sprint(r.Workloads)
			if s := states(r.WorkloadStates); s != "" {
				count += " (" + s + ")"
			}
		}
		rows = append(rows, []string{r.Name, orNone(r.Account), r.Region, r.Source, version, orNone(r.Health.Status), count})
	}
	return Table(rows)
}

// RoutersSummary is the line under the table: the version available and how many of rs are below it.
func RoutersSummary(rs []Router, available string) string {
	if available == "" {
		return fmt.Sprintf("no version available · %d %s", len(rs), Plural(len(rs), "router"))
	}
	n := 0
	for _, r := range rs {
		if r.Outdated(available) {
			n++
		}
	}
	return fmt.Sprintf("available %s · %d of %d outdated", available, n, len(rs))
}

// Plural is word, in the plural unless n is 1.
func Plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// WorkloadsTable is the workloads list: tags as k=v sorted by key, CHANGED the time since the state began (else the
// last update), as 5m, 3h or 2d.
func WorkloadsTable(ws []Workload, now time.Time) string {
	rows := [][]string{{"ROUTER", "WORKLOAD", "NAME", "STATE", "TAGS", "CHANGED"}}
	for _, w := range ws {
		var tags []string
		for _, k := range sortedKeys(w.Tags) {
			tags = append(tags, k+"="+w.Tags[k])
		}
		since := w.StateAt
		if since == "" {
			since = w.UpdatedAt
		}
		rows = append(rows, []string{w.Router, w.WorkloadID, orNone(w.Name), w.State, orNone(strings.Join(tags, ",")), Age(since, now)})
	}
	return Table(rows)
}

// WorkloadsSummary is the line under the table: the total, with the count per state of the rows listed when they are
// all there. The manager's byState counts every workload the caller sees, whatever the filters, so it is not used.
func WorkloadsSummary(w Workloads) string {
	s := fmt.Sprintf("%d %s", w.Total, Plural(w.Total, "workload"))
	if len(w.Rows) < w.Total {
		return s + fmt.Sprintf(" (%d shown)", len(w.Rows))
	}
	counts := map[string]int{}
	for _, r := range w.Rows {
		counts[r.State]++
	}
	if len(counts) > 0 {
		s += " (" + states(counts) + ")"
	}
	return s
}
