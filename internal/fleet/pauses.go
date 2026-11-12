package fleet

// A pause in words, on both subtrees: what a pause write says it did, a workload's or a router's alike, and an active pause as
// `workloads show` and `routers show` list it.

import (
	"fmt"
	"strings"
	"time"
)

// PauseResult is what a pause write says after its target, a workload's or a router's: "paused until resumed" or "paused until
// <the end in loc>", ", keeps asleep" for a pause asked to (PauseText's words), then "; woken <workloads>" when the answer names
// the workloads it woke. A zero end is none.
func PauseResult(end time.Time, loc *time.Location, keepAsleep bool, woken []string) string {
	s := "paused until resumed"
	if !end.IsZero() {
		s = "paused until " + end.In(loc).Format("2006-01-02 15:04 MST")
	}
	if keepAsleep {
		s += ", keeps asleep"
	}
	if len(woken) > 0 {
		s += "; woken " + strings.Join(woken, ", ")
	}
	return s
}

// PauseText is an active pause, times in loc: "paused by X on <time>: <reason>, until <time>, keeps asleep", "router paused by
// …" for the router's.
func PauseText(p Pause, loc *time.Location) string {
	s := fmt.Sprintf("%spaused by %s", map[bool]string{true: "router "}[p.Scope == "router"], p.By)
	if p.At != "" {
		s += " on " + When(p.At, loc)
	}
	s += ": " + p.Reason
	if p.Until != "" {
		s += ", until " + When(p.Until, loc)
	}
	if p.KeepAsleep {
		s += ", keeps asleep"
	}
	return s
}
