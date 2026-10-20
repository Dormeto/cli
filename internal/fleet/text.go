package fleet

// The text the commands print: tables, labelled lines, times in a zone and ages.

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"
)

// Table aligns rows (the first is the header) in columns two spaces apart.
func Table(rows [][]string) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(w, strings.TrimRight(strings.Join(r, "\t"), "\t"))
	}
	_ = w.Flush()
	return b.String()
}

// Labelled is a line per row, "label  value", the labels padded to the longest; a row with an empty value is left out,
// and the lines of a value on several lines are aligned with its first.
func Labelled(rows [][2]string) string {
	width := 0
	for _, r := range rows {
		if r[1] != "" {
			width = max(width, utf8.RuneCountInString(r[0]))
		}
	}
	indent := "\n" + strings.Repeat(" ", width+2)
	var b strings.Builder
	for _, r := range rows {
		if r[1] != "" {
			fmt.Fprintf(&b, "%-*s  %s\n", width, r[0], strings.ReplaceAll(r[1], "\n", indent))
		}
	}
	return b.String()
}

// When is the RFC 3339 instant at as "2006-01-02 15:04 MST" in loc; "" for none, at itself when it is not a time.
func When(at string, loc *time.Location) string {
	if at == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return at
	}
	return t.In(loc).Format("2006-01-02 15:04 MST")
}

// Age is the time from the RFC 3339 instant to now, in its largest whole unit; "—" when unknown.
func Age(at string, now time.Time) string {
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
