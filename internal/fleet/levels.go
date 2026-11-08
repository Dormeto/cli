package fleet

// The workload settings of a level as the commands name and print them: AWS's and Google Cloud's share them.

import (
	"fmt"
)

// LevelSetting is a numeric setting of a level: its flag, its key in the router's words, its label and its range.
type LevelSetting struct {
	Flag, Key, Label string
	Min, Max         int
	Format           func(int) string
}

// LevelSettings are the numeric settings, in the order of the flags.
var LevelSettings = []LevelSetting{
	{"idle-minutes", "idleMinutes", "idle window", 5, 1440, func(v int) string { return fmt.Sprintf("%d min", v) }},
	{"wait-seconds", "waitSeconds", "hold", 5, 120, Seconds},
	{"drain-seconds", "drainSeconds", "drain", 0, 300, Seconds},
	{"wake-timeout-seconds", "wakeTimeoutSeconds", "wake timeout", 60, 3600, Seconds},
	{"max-holders", "maxHolders", "held requests", 1, 500, func(v int) string { return fmt.Sprintf("≤ %d", v) }},
	{"failed-retry-seconds", "failedRetrySeconds", "retry after a failed wake", 10, 600, Seconds},
}

// LevelLines says the settings a level sets, one each, in the order of the flags.
func LevelLines(d map[string]any) []string {
	var lines []string
	for _, s := range LevelSettings {
		if n, ok := d[s.Key].(float64); ok {
			lines = append(lines, s.Label+" "+s.Format(int(n)))
		}
	}
	if m, ok := d["wakeMode"].(string); ok {
		lines = append(lines, "wake mode "+m)
	}
	for _, r := range []struct{ key, label string }{{"noWakeRules", "no-wake rules"}, {"wakeRules", "wake rules"}} {
		if list, ok := d[r.key].([]any); ok && len(list) > 0 {
			lines = append(lines, fmt.Sprintf("%s %d", r.label, len(list)))
		}
	}
	return lines
}

// RuleText is a described rule as one line: its name and its conditions, or why it is invalid.
func RuleText(r NoWake) string {
	if r.Invalid != "" {
		return r.Name + ": invalid, " + r.Invalid
	}
	return r.Name + ": " + conditionsText(r.Conditions)
}
