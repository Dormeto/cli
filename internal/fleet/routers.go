package fleet

// The details of one router (GET /iam/routers/{router}): the router's own `router` action answer spread with the
// manager's fields, and the text `dormeto routers show` prints, as the dashboard's router view shows it.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RouterDetails is the answer of GET /iam/routers/{router}: what the CLI prints of it. The answer as the API sent it is
// kept, so --json prints every field.
type RouterDetails struct {
	Name    string `json:"name"`
	Region  string `json:"region"`
	Account string `json:"account"`
	Source  string `json:"source"`
	Router  struct {
		Version string `json:"version"`
		Build   string `json:"build"`
	} `json:"router"`
	Clusters      []ClusterCheck `json:"clusters"`
	Configuration struct {
		CheckSchedule  Configured[*string]    `json:"checkSchedule"`
		IPLists        Configured[[]IPList]   `json:"ipLists"`
		IPListsCatalog *Configured[IPCatalog] `json:"ipListsCatalog"`
		NoWakeRules    Configured[[]NoWake]   `json:"noWakeRules"`
		Metrics        Configured[*bool]      `json:"metrics"`
	} `json:"configuration"`
	Description     *Description     `json:"description"`
	Settings        *RouterSettings  `json:"settings"`
	ManagerSettings *ManagerSettings `json:"managerSettings"`
	Pauses          []Pause          `json:"pauses"`
	Workloads       struct {
		Total          int            `json:"total"`
		ByState        map[string]int `json:"byState"`
		Paused         int            `json:"paused"`
		LastActivityAt string         `json:"lastActivityAt"`
	} `json:"workloads"`
	Health struct {
		Status               string    `json:"status"`
		Problems             []Problem `json:"problems"`
		CheckSchedule        *string   `json:"checkSchedule"`
		LastScheduledCheckAt string    `json:"lastScheduledCheckAt"`
		Lambda               struct {
			Error      string                           `json:"error"`
			Controller *struct{ Errors, Throttles int } `json:"controller"`
			Waker      *struct {
				Errors, Throttles int
				PeakConcurrency   int `json:"peakConcurrency"`
			} `json:"waker"`
		} `json:"lambda"`
		Queues struct {
			Error      string `json:"error"`
			Check      *int   `json:"check"`
			Controller *int   `json:"controller"`
		} `json:"queues"`
		IPLists []struct {
			Name      string `json:"name"`
			Entries   int    `json:"entries"`
			FetchedAt string `json:"fetchedAt"`
			Error     string `json:"error"`
			Pending   bool   `json:"pending"`
			Source    string `json:"source"`
		} `json:"ipLists"`
		Concurrency *struct {
			Limit, Needed int
			WorstCase     *int   `json:"worstCase"`
			Error         string `json:"error"`
		} `json:"concurrency"`
	} `json:"health"`

	raw json.RawMessage
}

// Configured is a setting with where it is set: terraform (the router's module) or manager.
type Configured[T any] struct {
	Value  T      `json:"value"`
	Source string `json:"source"`
}

// IPList is a router's IP list as configured.
type IPList struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	Action       string `json:"action"`
	RefreshHours int    `json:"refreshHours"`
}

// IPCatalog is the manager's IP lists a router reads.
type IPCatalog struct {
	URL   string   `json:"url"`
	Lists []IPList `json:"lists"`
}

// NoWake is a no-wake rule: its conditions are kept as the router described them.
type NoWake struct {
	Name       string          `json:"name"`
	Invalid    string          `json:"invalid"`
	Conditions json.RawMessage `json:"conditions"`
}

// ClusterCheck is an EKS cluster of the router's account and region as its last check found it.
type ClusterCheck struct {
	Name      string   `json:"name"`
	Reachable bool     `json:"reachable"`
	Allowed   bool     `json:"allowed"`
	Missing   []string `json:"missing"`
	Reason    string   `json:"reason"`
	CheckedAt string   `json:"checkedAt"`
	// Via is how the router reaches the cluster: "direct" or "proxy" (empty for a router before 0.33.0).
	Via   string        `json:"via"`
	Proxy *ClusterProxy `json:"proxy"`
}

// ClusterProxy is the proxy function of a cluster; Version is nil when it has not answered a ping.
type ClusterProxy struct {
	Name    string  `json:"name"`
	Version *string `json:"version"`
}

// Pause is an active pause of the router (or of one of its workloads).
type Pause struct {
	Scope      string `json:"scope"`
	By         string `json:"by"`
	At         string `json:"at"`
	Reason     string `json:"reason"`
	Until      string `json:"until"`
	KeepAsleep bool   `json:"keepAsleep"`
}

// Problem is a finding of the router's health.
type Problem struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

type routerDetailsFields RouterDetails // without the methods below, so that decoding does not recurse

func (d *RouterDetails) UnmarshalJSON(data []byte) error {
	var f routerDetailsFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*d = RouterDetails(f)
	d.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON is the answer as the API sent it, or the fields known when it was built here.
func (d RouterDetails) MarshalJSON() ([]byte, error) {
	if d.raw != nil {
		return d.raw, nil
	}
	return json.Marshal(routerDetailsFields(d))
}

// Text is the details as labelled lines, times in loc (ages from now): the router, its health and figures, its pauses, then its
// configuration with where each setting is set.
func (d RouterDetails) Text(loc *time.Location, now time.Time) string {
	h := d.Health
	version := orNone(d.Router.Version)
	if d.Router.Build != "" {
		version += " (build " + d.Router.Build + ")"
	}
	schedule := orNone(deref(h.CheckSchedule))
	if h.LastScheduledCheckAt != "" {
		schedule += ", last scheduled run " + When(h.LastScheduledCheckAt, loc)
	} else {
		schedule += ", no scheduled run recorded"
	}
	var problems []string
	for _, p := range h.Problems {
		problems = append(problems, p.Level+": "+p.Text)
	}
	var pauses []string
	for _, p := range d.Pauses {
		pauses = append(pauses, pauseText(p, loc))
	}
	workloads := fmt.Sprintf("%d", d.Workloads.Total)
	if len(d.Workloads.ByState) > 0 {
		workloads += " (" + states(d.Workloads.ByState) + ")"
	}
	workloads += fmt.Sprintf(", %d paused", d.Workloads.Paused)

	rows := [][2]string{
		{"router", d.Name}, {"account", d.Account}, {"region", d.Region}, {"source", d.Source},
		{"version", version}, {"health", orNone(h.Status)}, {"problems", strings.Join(problems, "\n")},
		{"idle check", schedule}, {"lambda", lambdaText(d)}, {"failure queues", queuesText(d)},
		{"concurrency", concurrencyText(d)},
		{"workloads", workloads}, {"last activity", When(d.Workloads.LastActivityAt, loc)},
		{"pauses", strings.Join(pauses, "\n")}, {"clusters", clustersText(d.Clusters, loc)}, {"ip lists", ipListsText(d, loc)},
	}
	c := d.Configuration
	metrics := none
	if c.Metrics.Value != nil {
		metrics = map[bool]string{true: "published", false: "off"}[*c.Metrics.Value]
	}
	conf := [][2]string{
		{"idle check schedule", withSource(orNone(deref(c.CheckSchedule.Value)), c.CheckSchedule.Source)},
		{"wake statistics metrics", withSource(metrics, c.Metrics.Source)},
		{"ip lists", withSource(listsText(c.IPLists.Value), c.IPLists.Source)},
	}
	if c.IPListsCatalog != nil {
		conf = append(conf, [2]string{"manager's ip lists", withSource(listsText(c.IPListsCatalog.Value.Lists), c.IPListsCatalog.Source)})
	}
	conf = append(conf, [2]string{"no-wake rules", withSource(rulesText(c.NoWakeRules.Value), c.NoWakeRules.Source)},
		[2]string{"hibernate", d.hibernateText(loc, now)})
	return Labelled(rows) + d.Description.block(loc) + "\nconfiguration\n" + indent(Labelled(conf))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ") + "\n"
}

// withSource appends where a setting is set, "(terraform)", to the last line of its text.
func withSource(text, source string) string {
	if source == "" {
		return text
	}
	return text + " (" + source + ")"
}

func pauseText(p Pause, loc *time.Location) string {
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

func lambdaText(d RouterDetails) string {
	l := d.Health.Lambda
	if l.Error != "" {
		return l.Error
	}
	var parts []string
	if l.Controller != nil {
		parts = append(parts, fmt.Sprintf("controller %d errors, %d throttles", l.Controller.Errors, l.Controller.Throttles))
	}
	if l.Waker != nil {
		parts = append(parts, fmt.Sprintf("waker %d errors, %d throttles, peak concurrency %d", l.Waker.Errors, l.Waker.Throttles, l.Waker.PeakConcurrency))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + " (24 hours)"
}

func queuesText(d RouterDetails) string {
	q := d.Health.Queues
	if q.Error != "" {
		return q.Error
	}
	count := func(n *int) string {
		if n == nil {
			return none
		}
		return fmt.Sprint(*n)
	}
	return fmt.Sprintf("check-failures %s, controller-failures %s", count(q.Check), count(q.Controller))
}

func concurrencyText(d RouterDetails) string {
	c := d.Health.Concurrency
	switch {
	case c == nil:
		return ""
	case c.Error != "":
		return c.Error
	case c.WorstCase == nil:
		return fmt.Sprintf("limit %d, needs up to %d", c.Limit, c.Needed)
	}
	return fmt.Sprintf("limit %d, needs up to %d (waker 24-hour peak and queues); %d if every workload not awake woke at once", c.Limit, c.Needed, *c.WorstCase)
}

func clustersText(cs []ClusterCheck, loc *time.Location) string {
	var lines []string
	for _, c := range cs {
		var s string
		switch {
		case !c.Reachable:
			s = "unreachable: " + c.Reason
		case c.Allowed:
			s = "reachable, every right"
		case len(c.Missing) > 0:
			s = "missing " + strings.Join(c.Missing, "; ")
		default:
			s = c.Reason
		}
		switch {
		case c.Via == "direct":
			s += ", directly"
		case c.Via == "proxy" && c.Proxy != nil:
			s += ", through its proxy " + c.Proxy.Name
			if c.Proxy.Version != nil && *c.Proxy.Version != "" {
				s += " (" + *c.Proxy.Version + ")"
			}
		}
		if c.CheckedAt != "" {
			s += ", checked " + When(c.CheckedAt, loc)
		}
		lines = append(lines, c.Name+": "+s)
	}
	return strings.Join(lines, "\n")
}

// ipListsText is the lists the router read: their entries and when they were downloaded, or why not.
func ipListsText(d RouterDetails, loc *time.Location) string {
	var lines []string
	for _, l := range d.Health.IPLists {
		s := l.Name + ": "
		if l.Error != "" {
			s += l.Error
		} else {
			s += fmt.Sprintf("%d entries", l.Entries)
			if l.FetchedAt != "" {
				s += ", downloaded " + When(l.FetchedAt, loc)
			}
		}
		lines = append(lines, s)
	}
	return strings.Join(lines, "\n")
}

func listsText(ls []IPList) string {
	var lines []string
	for _, l := range ls {
		lines = append(lines, fmt.Sprintf("%s (%s, every %d h)", l.Name, l.Action, l.RefreshHours))
	}
	if len(lines) == 0 {
		return "none"
	}
	return strings.Join(lines, "\n")
}

func rulesText(rs []NoWake) string {
	var lines []string
	for _, r := range rs {
		if r.Invalid != "" {
			lines = append(lines, r.Name+": invalid, "+r.Invalid)
		} else {
			lines = append(lines, r.Name+": "+conditionsText(r.Conditions))
		}
	}
	if len(lines) == 0 {
		return "none"
	}
	return strings.Join(lines, "\n")
}

// conditionsText is a rule's conditions as the dashboard writes them: source …, path …*, path ~ …, user agent ~ …,
// header … = … or ~ ….
func conditionsText(raw json.RawMessage) string {
	var c struct {
		SourceIPs        []string `json:"sourceIps"`
		PathPrefixes     []string `json:"pathPrefixes"`
		PathPattern      string   `json:"pathPattern"`
		UserAgentPattern string   `json:"userAgentPattern"`
		Headers          []struct{ Name, Value, Pattern string }
	}
	if json.Unmarshal(raw, &c) != nil {
		return string(raw)
	}
	var parts []string
	if len(c.SourceIPs) > 0 {
		parts = append(parts, "source "+strings.Join(c.SourceIPs, " | "))
	}
	if len(c.PathPrefixes) > 0 {
		prefixes := make([]string, len(c.PathPrefixes))
		for i, p := range c.PathPrefixes {
			prefixes[i] = p + "*"
		}
		parts = append(parts, "path "+strings.Join(prefixes, " | "))
	}
	if c.PathPattern != "" {
		parts = append(parts, "path ~ "+c.PathPattern)
	}
	if c.UserAgentPattern != "" {
		parts = append(parts, "user agent ~ "+c.UserAgentPattern)
	}
	for _, h := range c.Headers {
		if h.Pattern != "" {
			parts = append(parts, "header "+h.Name+" ~ "+h.Pattern)
		} else {
			parts = append(parts, "header "+h.Name+" = "+h.Value)
		}
	}
	return strings.Join(parts, ", ")
}
