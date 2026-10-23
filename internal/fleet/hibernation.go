package fleet

// What the commands print of the hibernation setting: a router's value and what it inherits, a workload's resolved
// value, where it comes from and which instances were launched with hibernation.

import (
	"fmt"
	"strings"
	"time"
)

// RouterSettings is a router's own settings: Hibernate nil when it inherits.
type RouterSettings struct {
	Hibernate *bool  `json:"hibernate"`
	By        string `json:"by"`
	At        string `json:"at"`
}

// ManagerSettings is what a router last read of the manager's settings; URL is nil when it reads none.
type ManagerSettings struct {
	URL       *string `json:"url"`
	Hibernate *bool   `json:"hibernate"`
	ReadAt    string  `json:"readAt"`
	Error     string  `json:"error"`
}

// Hibernation is a workload's resolved hibernation as a router 0.32.0 or later reports it.
type Hibernation struct {
	Hibernate bool   `json:"hibernate"`
	From      string `json:"from"`
	Instances *[]struct {
		ID         string `json:"id"`
		Configured bool   `json:"configured"`
		Hibernated bool   `json:"hibernated"`
	} `json:"instances"`
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
		text := OnOff(*s.Hibernate) + " (set on this router"
		if s.By != "" {
			text += " by " + s.By
		}
		if s.At != "" {
			text += ", " + When(s.At, loc)
		}
		return text + ")"
	}
	m := d.ManagerSettings
	if m == nil || m.URL == nil {
		return "inherit (built-in default: on)"
	}
	value := "on" // the router's own default until it has read a value
	if m.Hibernate != nil {
		value = OnOff(*m.Hibernate)
	}
	read := "not read yet"
	if m.ReadAt != "" {
		read = "read " + Age(m.ReadAt, now) + " ago"
	}
	text := fmt.Sprintf("inherit (manager: %s, %s)", value, read)
	if m.Error != "" {
		text += ", " + m.Error
	}
	return text
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

// Text is the hibernation as "on, from the router" ("off: the workload has fallback instance types").
func (h Hibernation) Text() string {
	if h.From == "fallback-types" {
		return fromText(h.From)
	}
	return OnOff(h.Hibernate) + ", " + fromText(h.From)
}

// Readiness is which instances were launched with hibernation; "" when the router lists none.
func (h Hibernation) Readiness() string {
	if h.Instances == nil || len(*h.Instances) == 0 {
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
