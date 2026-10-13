// The totals as a table, Markdown in the report and aligned text in the terminal: ports the Row and Table types,
// markdownTable and textTable of manager packages/estimate/src/report.ts.
package report

import (
	"strings"
)

// row is a table's row; a strong row's label and figures are bold as Markdown.
type row struct {
	cells  []string
	strong bool
}

// table has its heading, which columns are figures (right-aligned as text), its groups of rows (a rule between them as
// text) and a note.
type table struct {
	head    []string
	figures []bool
	groups  [][]row
	note    *string
}

func (t table) rows() []row {
	var all []row
	for _, g := range t.groups {
		all = append(all, g...)
	}
	return all
}

func markdownTable(t table) []string {
	line := func(cells []string) string {
		parts := make([]string, len(cells))
		for i, c := range cells {
			if c == "" {
				parts[i] = " "
			} else {
				parts[i] = " " + c + " "
			}
		}
		return "|" + strings.Join(parts, "|") + "|"
	}
	strong := func(r row) []string {
		cells := make([]string, len(r.cells))
		for i, c := range r.cells {
			if r.strong && c != "" && (i == 0 || t.figures[i]) {
				cells[i] = "**" + cell(c) + "**"
			} else {
				cells[i] = cell(c)
			}
		}
		return cells
	}
	rule := make([]string, len(t.head))
	for i := range rule {
		rule[i] = "---"
	}
	out := []string{line(t.head), "|" + strings.Join(rule, "|") + "|"}
	for _, r := range t.rows() {
		out = append(out, line(strong(r)))
	}
	if t.note != nil {
		out = append(out, "", *t.note)
	}
	return out
}

// textTable is bordered: around the table, under the heading, between its groups. Widths count UTF-16 units, as
// String.length does.
func textTable(t table) []string {
	widths := make([]int, len(t.head))
	for _, cells := range append([][]string{t.head}, cellsOf(t.rows())...) {
		for i, c := range cells {
			widths[i] = max(widths[i], len16(c))
		}
	}
	line := func(cells []string) string {
		parts := make([]string, len(cells))
		for i, c := range cells {
			if t.figures[i] {
				parts[i] = padStart(c, widths[i])
			} else {
				parts[i] = padEnd(c, widths[i])
			}
		}
		return "  │ " + strings.Join(parts, " │ ") + " │"
	}
	rule := func(left, joint, right string) string {
		bars := make([]string, len(widths))
		for i, w := range widths {
			bars[i] = strings.Repeat("─", w+2)
		}
		return "  " + left + strings.Join(bars, joint) + right
	}
	out := []string{rule("┌", "┬", "┐"), line(t.head)}
	for _, g := range t.groups {
		out = append(out, rule("├", "┼", "┤"))
		for _, r := range g {
			out = append(out, line(r.cells))
		}
	}
	out = append(out, rule("└", "┴", "┘"))
	if t.note != nil {
		out = append(out, "  "+*t.note)
	}
	return out
}

func cellsOf(rows []row) [][]string {
	out := make([][]string, len(rows))
	for i, r := range rows {
		out[i] = r.cells
	}
	return out
}
