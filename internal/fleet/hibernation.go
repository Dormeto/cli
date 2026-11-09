package fleet

// What the commands print of the hibernation setting: a router's value and what it inherits, a workload's resolved
// value, where it comes from and which instances were launched with hibernation. And of a router's joinMinutes.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/schedule"
)

// RouterSettings is a router's own settings: Hibernate nil when it inherits, JoinMinutes null too, and empty for a router
// that does not have it.
type RouterSettings struct {
	Hibernate   *bool           `json:"hibernate"`
	JoinMinutes json.RawMessage `json:"joinMinutes"`
	By          string          `json:"by"`
	At          string          `json:"at"`
}

// ManagerSettings is what a router last read of the manager's settings; URL is nil when it reads none. A Google Cloud router's
// URL is always nil: the manager pushes its level to the router, and ReadAt is when.
type ManagerSettings struct {
	URL         *string `json:"url"`
	Hibernate   *bool   `json:"hibernate"`
	JoinMinutes *int    `json:"joinMinutes"`
	ReadAt      string  `json:"readAt"`
	Error       string  `json:"error"`
}

// Hibernation is a workload's resolved hibernation as a router 0.32.0 or later reports it.
type Hibernation struct {
	Hibernate bool   `json:"hibernate"`
	From      string `json:"from"`
	// Unknown: the router has not read the manager's settings yet, or every read failed; naps stop meanwhile.
	Unknown   bool `json:"unknown"`
	FreshBoot bool `json:"freshBoot"`
	Instances *[]struct {
		ID         string `json:"id"`
		Configured bool   `json:"configured"`
		Hibernated bool   `json:"hibernated"`
	} `json:"instances"`
}

// HibernateWord is a value as it is printed: on, off, or inherit for nil.
func HibernateWord(b *bool) string {
	if b == nil {
		return "inherit"
	}
	return OnOff(*b)
}

// OnOff is "on" or "off".
func OnOff(b bool) string { return map[bool]string{true: "on", false: "off"}[b] }

// hibernateText is a router's hibernate row: its own value, or what it inherits. "" for a router that does not
// report it (before 0.32.0).
func (d RouterDetails) hibernateText(loc *time.Location, now time.Time) string {
	s := d.Settings
	if s == nil {
		return ""
	}
	if s.Hibernate != nil {
		return OnOff(*s.Hibernate) + s.setHere(loc)
	}
	m := d.ManagerSettings
	if m == nil || (m.URL == nil && d.Cloud != "gcp") {
		return "inherit (built-in default: on)"
	}
	if m.Hibernate == nil && d.Cloud == "gcp" { // none pushed, or one the router can't read: the levels go on without it
		if m.Error != "" {
			return "inherit (built-in default: on; manager: " + m.Error + ")"
		}
		return "inherit (built-in default: on)"
	}
	if m.Hibernate == nil { // nothing read: the router stops at naps until it has
		return fmt.Sprintf("inherit (manager: unknown, %s: naps stop)", orElse(m.Error, "not read yet"))
	}
	return fmt.Sprintf("inherit (manager: %s, %s)", OnOff(*m.Hibernate), m.readText(now))
}

// setHere is " (set on this router by ann, 2026-10-19 10:00 UTC)".
func (s RouterSettings) setHere(loc *time.Location) string {
	text := " (set on this router"
	if s.By != "" {
		text += " by " + s.By
	}
	if s.At != "" {
		text += ", " + When(s.At, loc)
	}
	return text + ")"
}

// readText is when the router last read the manager's settings, and its last failure.
func (m ManagerSettings) readText(now time.Time) string {
	read := "not read yet"
	if m.ReadAt != "" {
		read = "read " + Age(m.ReadAt, now) + " ago"
	}
	if m.Error != "" {
		read += "; last read failed: " + m.Error
	}
	return read
}

// joinText is a router's join windows row: its own value, or what it inherits. "" for a router that does not report it.
func (d RouterDetails) joinText(loc *time.Location, now time.Time) string {
	s := d.Settings
	if s == nil || len(s.JoinMinutes) == 0 {
		return ""
	}
	var own *int
	if json.Unmarshal(s.JoinMinutes, &own) == nil && own != nil {
		return schedule.JoinGap(*own) + s.setHere(loc)
	}
	m := d.ManagerSettings
	if m == nil || m.JoinMinutes == nil { // a level that cannot be read counts as unset
		return "inherit (built-in default: " + schedule.JoinGap(schedule.JoinDefault) + ")"
	}
	return fmt.Sprintf("inherit (manager: %s, %s)", schedule.JoinGap(*m.JoinMinutes), m.readText(now))
}

// fromText is where a workload's value comes from.
func fromText(from string) string {
	switch from {
	case "workload":
		return "set on this workload"
	case "fallback-types":
		return "off: the workload has fallback instance types"
	case "router":
		return "from the router"
	case "manager":
		return "from the manager's default"
	case "default":
		return "built-in default"
	}
	return from
}

// unknownText is said for a workload whose router has no value of the manager's yet.
const unknownText = "off for now: the router has not read the manager's settings yet (naps stop)"

// freshBootText is said when the instances have been hibernated for as long as a fresh boot is due.
const freshBootText = "the next nap stops the instances (a fresh boot after 59 days hibernated)"

// notApplyText is said for a workload with no standalone EC2 instance.
const notApplyText = "does not apply (standalone EC2 instances only)"

// Text is the hibernation as "on, from the router" ("off: the workload has fallback instance types").
func (h Hibernation) Text() string {
	if h.Unknown {
		return unknownText
	}
	if h.From == "fallback-types" {
		return fromText(h.From)
	}
	return OnOff(h.Hibernate) + ", " + fromText(h.From)
}

// Readiness is which instances were launched with hibernation; "" when the router lists none, or when the value is off
// (every instance is stopped then, whatever it was launched with).
func (h Hibernation) Readiness() string {
	if h.Unknown || !h.Hibernate || h.Instances == nil || len(*h.Instances) == 0 {
		return ""
	}
	var without []string
	for _, i := range *h.Instances {
		if !i.Configured {
			without = append(without, i.ID)
		}
	}
	if len(without) == 0 {
		return "every instance was launched with hibernation"
	}
	n := len(*h.Instances)
	verb := "is"
	if len(without) > 1 {
		verb = "are"
	}
	return fmt.Sprintf("%d of %d instances were launched with hibernation; %s %s stopped instead",
		n-len(without), n, strings.Join(without, ", "), verb)
}
