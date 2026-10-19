package fleet

import (
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"
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

// table aligns rows (the first is the header) in columns two spaces apart.
func table(rows [][]string) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(w, strings.TrimRight(strings.Join(r, "\t"), "\t"))
	}
	_ = w.Flush()
	return b.String()
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
		count := fmt.Sprint(r.Workloads)
		if s := states(r.WorkloadStates); s != "" {
			count += " (" + s + ")"
		}
		rows = append(rows, []string{r.Name, orNone(r.Account), r.Region, r.Source, version, orNone(r.Health.Status), count})
	}
	return table(rows)
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
		rows = append(rows, []string{w.Router, w.WorkloadID, orNone(w.Name), w.State, orNone(strings.Join(tags, ",")), age(since, now)})
	}
	return table(rows)
}

// age is the time from the RFC 3339 instant to now, in its largest whole unit; "—" when unknown.
func age(at string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return none
	}
	switch d := now.Sub(t); {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// WorkloadsSummary is the line under the table: the total, with the count per state when every workload is listed.
func WorkloadsSummary(w Workloads) string {
	s := fmt.Sprintf("%d %s", w.Total, Plural(w.Total, "workload"))
	switch {
	case len(w.Rows) < w.Total:
		s += fmt.Sprintf(" (%d shown)", len(w.Rows))
	case len(w.ByState) > 0:
		s += " (" + states(w.ByState) + ")"
	}
	return s
}
