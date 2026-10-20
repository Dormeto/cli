package fleet

// The manager's operations as the CLI lists and shows them: the list of GET /operations, newest first and narrowed by
// the CLI, a table of it, and one operation's labelled lines with its steps.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ListOperations reads GET /operations: every record the manager keeps, unpaged.
func ListOperations(ctx context.Context, c *Client) ([]Operation, error) {
	var out struct {
		Operations []Operation `json:"operations"`
	}
	err := c.Get(ctx, "/operations", nil, &out)
	return out.Operations, err
}

// FilterOperations keeps the running operations when running is set, those of one of types when there are any, newest
// first by creation, at most limit of them (all when limit is not positive); total is how many matched before the cut.
func FilterOperations(ops []Operation, running bool, types []string, limit int) (kept []Operation, total int) {
	for _, op := range ops {
		if (running && op.Status != "running") || (len(types) > 0 && !slices.Contains(types, op.Type)) {
			continue
		}
		kept = append(kept, op)
	}
	slices.SortStableFunc(kept, func(a, b Operation) int { return strings.Compare(b.CreatedAt, a.CreatedAt) })
	total = len(kept)
	if limit > 0 && len(kept) > limit {
		kept = kept[:limit]
	}
	return kept, total
}

// ShortARN is who an ARN names: its last two path parts (arn:aws:sts::1:assumed-role/Ci/run-42 is Ci/run-42, a user's ARN
// its name); anything that is not an ARN, or has no path, as it is.
func ShortARN(s string) string {
	if !strings.HasPrefix(s, "arn:") {
		return s
	}
	_, path, ok := strings.Cut(s, "/")
	if !ok {
		return s
	}
	parts := strings.Split(path, "/")
	return strings.Join(parts[max(0, len(parts)-2):], "/")
}

// OperationsTable is the operations as a table, in the order given: id, type, target, status, current step, who (an ARN
// shortened) and the age of its creation. "no operations" when there are none.
func OperationsTable(ops []Operation, now time.Time) string {
	if len(ops) == 0 {
		return "no operations\n"
	}
	rows := [][]string{{"ID", "TYPE", "TARGET", "STATUS", "STEP", "BY", "AGE"}}
	for _, op := range ops {
		step := none
		if s := op.Current(); s != nil {
			step = s.Name
		}
		rows = append(rows, []string{op.ID, op.Type, orNone(op.TargetText()), op.Status, step, orNone(ShortARN(op.By)), Age(op.CreatedAt, now)})
	}
	return Table(rows)
}

// OperationText is one operation: labelled lines (times in loc), then its steps with their status, how long each took
// and what it says.
func OperationText(op Operation, loc *time.Location) string {
	var b strings.Builder
	b.WriteString(Labelled([][2]string{
		{"id", op.ID}, {"type", op.Type}, {"target", op.TargetText()}, {"status", op.Status}, {"by", ShortARN(op.By)},
		{"created", When(op.CreatedAt, loc)}, {"updated", When(op.UpdatedAt, loc)}, {"finished", When(op.FinishedAt, loc)},
		{"error", op.Error},
	}))
	if len(op.Steps) > 0 {
		rows := [][]string{{"STEP", "STATUS", "TOOK", "DETAIL"}}
		for _, s := range op.Steps {
			rows = append(rows, []string{s.Name, s.Status, stepTook(s), s.Detail})
		}
		b.WriteString("\n" + Table(rows))
	}
	return b.String()
}

// stepTook is how long a step ran, to the second, "—" when it did not begin or end.
func stepTook(s OperationStep) string {
	a, err1 := time.Parse(time.RFC3339, s.StartedAt)
	z, err2 := time.Parse(time.RFC3339, s.FinishedAt)
	if err1 != nil || err2 != nil {
		return none
	}
	return fmt.Sprint(z.Sub(a).Round(time.Second))
}
