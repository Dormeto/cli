package fleet

// The optional Markdown description of a router or a workload, as the commands print it: the text as written (raw
// Markdown), then who wrote it and when.

import (
	"strings"
	"time"
)

// Description is a description as the manager keeps it.
type Description struct {
	Text string `json:"text"`
	By   string `json:"by"`
	At   string `json:"at"`
}

// Printed is the description as written, a final line feed ensured, then "— by, at" (what is known of them), times in loc.
func (d Description) Printed(loc *time.Location) string {
	s := strings.TrimRight(d.Text, "\n") + "\n"
	var by []string
	for _, p := range []string{d.By, When(d.At, loc)} {
		if p != "" {
			by = append(by, p)
		}
	}
	if len(by) > 0 {
		s += "— " + strings.Join(by, ", ") + "\n"
	}
	return s
}

// block is a description of a show command: a "description" section, its lines indented by two spaces; "" for none.
func (d *Description) block(loc *time.Location) string {
	if d == nil || strings.TrimSpace(d.Text) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(d.Printed(loc), "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = "  " + l
		}
	}
	return "\ndescription\n" + strings.Join(lines, "\n") + "\n"
}
