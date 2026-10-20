package fleet

// What the manager reports without anything to change: the overview's routes, the savings estimate, the alerts, the IP
// lists and the audit log. Each answer keeps the API's JSON, so that --json prints every field.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// kept is the JSON an answer was decoded from, printed back by --json in place of the fields known.
type kept struct{ raw json.RawMessage }

func (k kept) marshal(fields any) ([]byte, error) {
	if k.raw != nil {
		return k.raw, nil
	}
	return json.Marshal(fields)
}

// SavingsSummary is the daily job's estimate of what the naps saved (GET /savings).
type SavingsSummary struct {
	ComputedAt string `json:"computedAt"`
	Day        string `json:"day"`
	Baseline   struct {
		Kind     string `json:"kind"`
		TimeZone string `json:"timeZone,omitempty"`
	} `json:"baseline"`
	Last30 struct {
		USD         float64 `json:"usd"`
		AsleepHours float64 `json:"asleepHours"`
	} `json:"last30"`
	Month struct {
		Month string  `json:"month"`
		USD   float64 `json:"usd"`
	} `json:"month"`
	Cumulative struct {
		USD   float64 `json:"usd"`
		Since string  `json:"since"`
	} `json:"cumulative"`
	Top []struct {
		Router     string  `json:"router"`
		WorkloadID string  `json:"workloadId"`
		Name       string  `json:"name,omitempty"`
		USD        float64 `json:"usd"`
	} `json:"top"`
	Unpriced struct {
		Workloads int     `json:"workloads"`
		Hours     float64 `json:"hours"`
	} `json:"unpriced"`
	Workloads int `json:"workloads"`
	Dormeto   struct {
		USDMonth float64 `json:"usdMonth"`
	} `json:"dormeto"`
	Discounts *Discounts `json:"discounts,omitempty"`
}

// Discounts are the discounts for commitments, in percent, that the estimate prices with.
type Discounts struct {
	Compute   int `json:"compute"`
	Databases int `json:"databases"`
}

// SavingsSettings are the discounts as an administrator last set them.
type SavingsSettings struct {
	Discounts
	UpdatedAt string `json:"updatedAt,omitempty"`
	UpdatedBy string `json:"updatedBy,omitempty"`
}

// SavingsAnswer is the answer of GET /savings: the summary (null before the first night's run) and the settings.
type SavingsAnswer struct {
	Summary  *SavingsSummary  `json:"summary"`
	Settings *SavingsSettings `json:"settings"`
	kept
}

func (a *SavingsAnswer) UnmarshalJSON(data []byte) error {
	var f struct {
		Summary  *SavingsSummary  `json:"summary"`
		Settings *SavingsSettings `json:"settings"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*a = SavingsAnswer{Summary: f.Summary, Settings: f.Settings, kept: kept{append(json.RawMessage(nil), data...)}}
	return nil
}

// MarshalJSON is the answer as the API sent it.
func (a SavingsAnswer) MarshalJSON() ([]byte, error) {
	return a.marshal(struct {
		Summary  *SavingsSummary  `json:"summary"`
		Settings *SavingsSettings `json:"settings"`
	}{a.Summary, a.Settings})
}

// GetSavings reads GET /savings.
func GetSavings(ctx context.Context, c *Client) (SavingsAnswer, error) {
	var a SavingsAnswer
	err := c.Get(ctx, "/savings", nil, &a)
	return a, err
}

// PutDiscounts sets the discounts: PUT /savings/settings, the answer as the API sent it.
func PutDiscounts(ctx context.Context, c *Client, d Discounts) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Put(ctx, "/savings/settings", d, &out)
	return out, err
}

// DiscountsText is the discounts in a few words: compute 10%, databases 0%.
func DiscountsText(d Discounts) string {
	return fmt.Sprintf("compute %d%%, databases %d%%", d.Compute, d.Databases)
}

// Text is the estimate as the Savings card tells it: the figures as labelled lines, the top savers as a table, the
// discounts and what is not counted. Times in loc.
func (a SavingsAnswer) Text(loc *time.Location) string {
	var b strings.Builder
	s := a.Summary
	if s == nil {
		b.WriteString("no estimate yet: the first one is computed tonight at 00:30 UTC, from the history of the naps\n")
	} else {
		baseline := "24/7"
		if s.Baseline.Kind == "business-hours" {
			baseline = "business hours (weekdays 08:00 to 20:00, " + s.Baseline.TimeZone + ")"
		}
		covered := 30
		if since, err := time.Parse("2006-01-02", s.Cumulative.Since); err == nil {
			if day, err := time.Parse("2006-01-02", s.Day); err == nil {
				covered = max(1, min(30, int(day.Sub(since).Hours()/24)+1))
			}
		}
		net := s.Last30.USD - s.Dormeto.USDMonth*float64(covered)/30
		b.WriteString(Labelled([][2]string{
			{"last 30 days", fmt.Sprintf("%s, %s hours asleep", USD(s.Last30.USD), commas(int(math.Round(s.Last30.AsleepHours))))},
			{"this month", USD(s.Month.USD)},
			{"since " + s.Cumulative.Since, USD(s.Cumulative.USD)},
			{"dormeto cost", "about " + USD(s.Dormeto.USDMonth) + " a month"},
			{"net", fmt.Sprintf("about %s over %s", USD(net), map[bool]string{true: "the last 30 days", false: fmt.Sprintf("the %d %s computed", covered, Plural(covered, "day"))}[covered == 30])},
			{"workloads", strconv.Itoa(s.Workloads)},
			{"not priced", unpricedText(s)},
			{"against", baseline},
			{"computed", When(s.ComputedAt, loc) + " (day " + s.Day + ")"},
		}))
		if len(s.Top) > 0 {
			rows := [][]string{{"TOP SAVERS", "NAME", "SAVED"}}
			for _, w := range s.Top {
				rows = append(rows, []string{w.Router + "/" + w.WorkloadID, orNone(w.Name), USD(w.USD)})
			}
			b.WriteString("\n" + Table(rows))
		}
	}
	d := Discounts{}
	if a.Settings != nil {
		d = a.Settings.Discounts
	}
	line := "no discount set"
	if d.Compute != 0 || d.Databases != 0 {
		line = DiscountsText(d) + " off the list prices"
	}
	if s != nil {
		used := Discounts{}
		if s.Discounts != nil {
			used = *s.Discounts
		}
		if used != d {
			line += "; applies from the next day computed"
		}
	}
	if a.Settings != nil && a.Settings.UpdatedBy != "" {
		line += " (set by " + ShortARN(a.Settings.UpdatedBy) + " " + When(a.Settings.UpdatedAt, loc) + ")"
	}
	b.WriteString("\n" + Labelled([][2]string{{"discounts", line}}))
	return b.String()
}

func unpricedText(s *SavingsSummary) string {
	if s.Unpriced.Workloads == 0 {
		return ""
	}
	return fmt.Sprintf("%d %s (a router before 0.27.0, or what AWS's list prices do not name)", s.Unpriced.Workloads, Plural(s.Unpriced.Workloads, "workload"))
}

// commas writes n with thousands separators.
func commas(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		if s[i-1] == '-' {
			break
		}
		s = s[:i] + "," + s[i:]
	}
	return s
}

// Alarm is an open health alarm of GET /alerts: its subject's fields beside its level and problems.
type Alarm struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"` // router, list or workload
	Name       string `json:"name"`
	Account    string `json:"account,omitempty"`
	Region     string `json:"region,omitempty"`
	Router     string `json:"router,omitempty"`
	WorkloadID string `json:"workloadId,omitempty"`
	Level      string `json:"level"` // warning or error
	Since      string `json:"since"`
	Problems   []struct {
		Code string `json:"code"`
		Text string `json:"text"`
	} `json:"problems"`
}

// Notifications are where the alerts go: the topic and its subscriptions (the endpoints only for an administrator,
// masked).
type Notifications struct {
	TopicARN  string `json:"topicArn"`
	Confirmed int    `json:"confirmed"`
	Pending   int    `json:"pending"`
	Endpoints []struct {
		Protocol string `json:"protocol"`
		Endpoint string `json:"endpoint"`
		Pending  bool   `json:"pending"`
	} `json:"endpoints,omitempty"`
}

// AlertsAnswer is the answer of GET /alerts; Notifications is nil when alerts are not configured (or for a member).
type AlertsAnswer struct {
	Alarms        []Alarm        `json:"alarms"`
	Notifications *Notifications `json:"notifications"`
	kept
}

func (a *AlertsAnswer) UnmarshalJSON(data []byte) error {
	var f struct {
		Alarms        []Alarm        `json:"alarms"`
		Notifications *Notifications `json:"notifications"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*a = AlertsAnswer{Alarms: f.Alarms, Notifications: f.Notifications, kept: kept{append(json.RawMessage(nil), data...)}}
	return nil
}

// MarshalJSON is the answer as the API sent it.
func (a AlertsAnswer) MarshalJSON() ([]byte, error) {
	alarms := a.Alarms
	if alarms == nil {
		alarms = []Alarm{}
	}
	return a.marshal(struct {
		Alarms        []Alarm        `json:"alarms"`
		Notifications *Notifications `json:"notifications"`
	}{alarms, a.Notifications})
}

// GetAlerts reads GET /alerts.
func GetAlerts(ctx context.Context, c *Client) (AlertsAnswer, error) {
	var a AlertsAnswer
	err := c.Get(ctx, "/alerts", nil, &a)
	return a, err
}

// Text is the open alarms as a table (level, kind, name, age, problems), then the notification subscriptions;
// "no alerts configured" when the manager has none.
func (a AlertsAnswer) Text(now time.Time) string {
	if len(a.Alarms) == 0 && a.Notifications == nil {
		return "no alerts configured\n"
	}
	var b strings.Builder
	if len(a.Alarms) == 0 {
		b.WriteString("no open alarm\n")
	} else {
		rows := [][]string{{"LEVEL", "KIND", "NAME", "SINCE", "PROBLEMS"}}
		for _, al := range a.Alarms {
			var problems []string
			for _, p := range al.Problems {
				problems = append(problems, p.Text)
			}
			rows = append(rows, []string{al.Level, al.Kind, orStr(al.Name, al.ID), Age(al.Since, now), orNone(strings.Join(problems, "; "))})
		}
		b.WriteString(Table(rows))
	}
	if n := a.Notifications; n != nil {
		b.WriteString("\n" + Labelled([][2]string{{"notifications", fmt.Sprintf("%s: %d confirmed, %d pending", n.TopicARN, n.Confirmed, n.Pending)}}))
		if len(n.Endpoints) > 0 {
			rows := [][]string{{"PROTOCOL", "ENDPOINT", "STATUS"}}
			for _, e := range n.Endpoints {
				rows = append(rows, []string{e.Protocol, e.Endpoint, map[bool]string{true: "pending", false: "confirmed"}[e.Pending]})
			}
			b.WriteString(Table(rows))
		}
	}
	return b.String()
}

// CatalogList is a list of the manager's IP lists catalog with what its download last did.
type CatalogList struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	Action       string `json:"action"` // defer or ignore
	RefreshHours int    `json:"refreshHours"`
	Version      string `json:"version,omitempty"`
	Entries      *int   `json:"entries,omitempty"`
	Dropped      int    `json:"dropped,omitempty"`
	FetchedAt    string `json:"fetchedAt,omitempty"`
	Error        string `json:"error,omitempty"`
}

// IPListsAnswer is the answer of GET /ip-lists: the catalog's URL (nil when none is configured) and the lists.
type IPListsAnswer struct {
	Catalog *string       `json:"catalog"`
	Lists   []CatalogList `json:"lists"`
	kept
}

func (a *IPListsAnswer) UnmarshalJSON(data []byte) error {
	var f struct {
		Catalog *string       `json:"catalog"`
		Lists   []CatalogList `json:"lists"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*a = IPListsAnswer{Catalog: f.Catalog, Lists: f.Lists, kept: kept{append(json.RawMessage(nil), data...)}}
	return nil
}

// MarshalJSON is the answer as the API sent it.
func (a IPListsAnswer) MarshalJSON() ([]byte, error) {
	lists := a.Lists
	if lists == nil {
		lists = []CatalogList{}
	}
	return a.marshal(struct {
		Catalog *string       `json:"catalog"`
		Lists   []CatalogList `json:"lists"`
	}{a.Catalog, lists})
}

// GetIPLists reads GET /ip-lists.
func GetIPLists(ctx context.Context, c *Client) (IPListsAnswer, error) {
	var a IPListsAnswer
	err := c.Get(ctx, "/ip-lists", nil, &a)
	return a, err
}

// status is how the dashboard reads a list: ok, not downloaded yet, or the failure of its last download.
func (l CatalogList) status() string {
	switch {
	case l.Error != "" && l.Version != "":
		return "last download failed: " + l.Error
	case l.Error != "":
		return "never downloaded: " + l.Error
	case l.Version != "":
		return "ok"
	}
	return "not downloaded yet"
}

// Text is the lists as a table (name, action, entries, last download, status), then the catalog; "no IP lists
// configured" when the manager has no catalog.
func (a IPListsAnswer) Text(now time.Time) string {
	if a.Catalog == nil {
		return "no IP lists configured\n"
	}
	if len(a.Lists) == 0 {
		return "no list in the catalog " + *a.Catalog + ": set ip_lists in the manager's Terraform\n"
	}
	rows := [][]string{{"NAME", "ACTION", "ENTRIES", "DOWNLOADED", "STATUS"}}
	for _, l := range a.Lists {
		entries := none
		if l.Entries != nil {
			entries = commas(*l.Entries)
		}
		rows = append(rows, []string{l.Name, l.Action, entries, Age(l.FetchedAt, now), l.status()})
	}
	return Table(rows) + "catalog " + *a.Catalog + "\n"
}

// AuditEntry is a row of GET /audit; the row as the API sent it is kept.
type AuditEntry struct {
	ID     string `json:"id"`
	At     string `json:"at"`
	Who    string `json:"who"`
	Role   string `json:"role"`
	Action string `json:"action"`
	Target string `json:"target"`
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`

	raw json.RawMessage
}

type auditFields AuditEntry

func (e *AuditEntry) UnmarshalJSON(data []byte) error {
	var f auditFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*e = AuditEntry(f)
	e.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON is the row as the API sent it, or the fields known when it was built here.
func (e AuditEntry) MarshalJSON() ([]byte, error) {
	if e.raw != nil {
		return e.raw, nil
	}
	return json.Marshal(auditFields(e))
}

// ListAudit reads GET /audit: the whole log, unpaged.
func ListAudit(ctx context.Context, c *Client) ([]AuditEntry, error) {
	var out struct {
		Entries []AuditEntry `json:"entries"`
	}
	err := c.Get(ctx, "/audit", nil, &out)
	return out.Entries, err
}

// AuditFilter narrows the log: entries since a time (zero for any), of one of the actions (none for any), whose who
// and target contain the texts, whatever their case.
type AuditFilter struct {
	Since       time.Time
	Actions     []string
	Who, Target string
}

// FilterAudit keeps what f lets through, newest first, at most limit entries (all when limit is not positive); total
// is how many matched before the cut.
func FilterAudit(entries []AuditEntry, f AuditFilter, limit int) (out []AuditEntry, total int) {
	has := func(s, sub string) bool { return strings.Contains(strings.ToLower(s), strings.ToLower(sub)) }
	for _, e := range entries {
		if !f.Since.IsZero() {
			if at, err := time.Parse(time.RFC3339, e.At); err != nil || at.Before(f.Since) {
				continue
			}
		}
		if len(f.Actions) > 0 && !slices.ContainsFunc(f.Actions, func(a string) bool { return strings.EqualFold(a, e.Action) }) {
			continue
		}
		if !has(e.Who, f.Who) || !has(e.Target, f.Target) {
			continue
		}
		out = append(out, e)
	}
	slices.SortStableFunc(out, func(a, b AuditEntry) int { return strings.Compare(b.At, a.At) })
	total = len(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, total
}

// shortTarget is an audit target with its ARN shortened: the target itself, or the ARN of "<arn> as <name>" (an
// onboarding's), the name kept; any other target as it is.
func shortTarget(t string) string {
	arn, name, as := strings.Cut(t, " as ")
	if as {
		return ShortARN(arn) + " as " + name
	}
	return ShortARN(t)
}

// AuditTable is the entries as a table in the order given: time (in loc), who and target (ARNs shortened), action and
// result. "no audit entries" when there are none.
func AuditTable(entries []AuditEntry, loc *time.Location) string {
	if len(entries) == 0 {
		return "no audit entries\n"
	}
	rows := [][]string{{"WHEN", "WHO", "ACTION", "TARGET", "RESULT"}}
	for _, e := range entries {
		rows = append(rows, []string{When(e.At, loc), ShortARN(e.Who), e.Action, orNone(shortTarget(e.Target)), orNone(e.Result)})
	}
	return Table(rows)
}

// FleetStates is what GET /workloads/states says of the time in each state over 7 days.
type FleetStates struct {
	Workloads int `json:"workloads"`
	States7d  struct {
		Totals map[string]float64 `json:"totals"` // seconds per state
	} `json:"states7d"`
}

// Shares are the states' shares of the 7 days, as "ASLEEP 62%, AWAKE 38%" (largest first); "" when no time is counted.
func (s FleetStates) Shares() string {
	total := 0.0
	for _, v := range s.States7d.Totals {
		total += v
	}
	if total <= 0 {
		return ""
	}
	keys := sortedKeys(s.States7d.Totals)
	slices.SortStableFunc(keys, func(a, b string) int { return int(s.States7d.Totals[b] - s.States7d.Totals[a]) })
	var parts []string
	for _, k := range keys {
		if v := s.States7d.Totals[k]; v > 0 {
			parts = append(parts, fmt.Sprintf("%s %d%%", k, int(math.Round(v/total*100))))
		}
	}
	return strings.Join(parts, ", ")
}

// Overview is what the six routes of the overview answered: a block is nil when its route failed (Failed says why) or is not in this manager (Missing).
type Overview struct {
	Workloads *Workloads
	States    *FleetStates
	StatesRaw json.RawMessage
	Savings   *SavingsAnswer
	Alerts    *AlertsAnswer
	Running   *[]Operation
	Routers   *Routers
	Missing   map[string]bool // the blocks whose route answered 404: that service is not in this manager
	Failed    []error         // one per route that failed otherwise, naming it
}

// blockOf is the overview block (its key in --json) of each route's name.
var blockOf = map[string]string{"workloads": "workloads", "workload states": "states", "savings": "savings", "alerts": "alerts", "operations": "operations", "routers": "routers"}

// ReadOverview reads the routes of the overview in turn; one that answers 404 is recorded in Missing, one that fails
// otherwise in Failed, and its block is left out.
func ReadOverview(ctx context.Context, c *Client) Overview {
	var o Overview
	o.Missing = map[string]bool{}
	fail := func(route string, err error) {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			o.Missing[blockOf[route]] = true
			return
		}
		o.Failed = append(o.Failed, fmt.Errorf("%s: %w", route, err))
	}
	var w Workloads
	if err := c.Get(ctx, "/workloads", url.Values{"limit": {"1"}}, &w); err != nil {
		fail("workloads", err)
	} else {
		o.Workloads = &w
	}
	var st FleetStates
	var raw json.RawMessage
	if err := c.Get(ctx, "/workloads/states", nil, &raw); err != nil {
		fail("workload states", err)
	} else if err := json.Unmarshal(raw, &st); err != nil {
		fail("workload states", err)
	} else {
		o.States, o.StatesRaw = &st, raw
	}
	if s, err := GetSavings(ctx, c); err != nil {
		fail("savings", err)
	} else {
		o.Savings = &s
	}
	if a, err := GetAlerts(ctx, c); err != nil {
		fail("alerts", err)
	} else {
		o.Alerts = &a
	}
	if ops, err := ListOperations(ctx, c); err != nil {
		fail("operations", err)
	} else {
		running, _ := FilterOperations(ops, true, nil, 0)
		if running == nil {
			running = []Operation{}
		}
		o.Running = &running
	}
	var rs Routers
	if err := c.Get(ctx, "/routers", nil, &rs); err != nil {
		fail("routers", err)
	} else {
		o.Routers = &rs
	}
	return o
}

// outdated is how many routers run a version below the one available.
func (o Overview) outdated() (n int) {
	for _, r := range o.Routers.Rows {
		if r.Outdated(o.Routers.Available) {
			n++
		}
	}
	return n
}

// JSON is the composite --json prints: workloads {total, byState}, states (the API's answer), savings (the API's answer),
// alerts {open}, operations {running}, routers {total, outdated, available}; a block whose route is not in this manager is null, one whose route failed is absent.
func (o Overview) JSON() map[string]any {
	out := map[string]any{}
	if o.Workloads != nil {
		by := o.Workloads.ByState
		if by == nil {
			by = map[string]int{}
		}
		out["workloads"] = map[string]any{"total": o.Workloads.Total, "byState": by}
	}
	if o.States != nil {
		out["states"] = o.StatesRaw
	}
	if o.Savings != nil {
		out["savings"] = o.Savings
	}
	if o.Alerts != nil {
		out["alerts"] = map[string]any{"open": len(o.Alerts.Alarms)}
	}
	if o.Running != nil {
		out["operations"] = map[string]any{"running": *o.Running}
	}
	for key := range o.Missing {
		out[key] = nil
	}
	if o.Routers != nil {
		out["routers"] = map[string]any{"total": len(o.Routers.Rows), "outdated": o.outdated(), "available": o.Routers.Available}
	}
	return out
}

// Text is the overview as labelled lines, a block per route that answered, then the running operations.
func (o Overview) Text(now time.Time) string {
	var rows [][2]string
	absent := func(key, label string) {
		if o.Missing[key] {
			rows = append(rows, [2]string{label, "not available"})
		}
	}
	absent("workloads", "workloads")
	if o.Workloads != nil {
		s := fmt.Sprintf("%d", o.Workloads.Total)
		if len(o.Workloads.ByState) > 0 {
			s += " (" + states(o.Workloads.ByState) + ")"
		}
		rows = append(rows, [2]string{"workloads", s})
	}
	absent("states", "last 7 days")
	if o.States != nil {
		rows = append(rows, [2]string{"last 7 days", o.States.Shares()})
	}
	absent("savings", "savings")
	if o.Savings != nil {
		if s := o.Savings.Summary; s != nil {
			rows = append(rows, [2]string{"savings", fmt.Sprintf("%s in the last 30 days, %s this month (estimated)", USD(s.Last30.USD), USD(s.Month.USD))})
		} else {
			rows = append(rows, [2]string{"savings", "no estimate yet"})
		}
	}
	absent("alerts", "alerts")
	if o.Alerts != nil {
		n := len(o.Alerts.Alarms)
		rows = append(rows, [2]string{"alerts", fmt.Sprintf("%d open", n)})
	}
	absent("routers", "routers")
	if o.Routers != nil {
		s := fmt.Sprintf("%d, %d outdated", len(o.Routers.Rows), o.outdated())
		if o.Routers.Available != "" {
			s += " (available " + o.Routers.Available + ")"
		}
		rows = append(rows, [2]string{"routers", s})
	}
	absent("operations", "operations")
	if o.Running != nil {
		rows = append(rows, [2]string{"operations", fmt.Sprintf("%d running", len(*o.Running))})
	}
	text := Labelled(rows)
	if o.Running != nil && len(*o.Running) > 0 {
		text += "\n" + OperationsTable(*o.Running, now)
	}
	return text
}
