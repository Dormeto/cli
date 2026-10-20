package fleet

// The manager's discovery (GET /iam/discovery, /discovery/access, /discovery/config): the shapes the CLI reads, kept raw
// for --json, and the text `dormeto discovery` prints, as the dashboard's Discovery views show them.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// CandidateStatuses are the statuses a candidate has, in the order the dashboard shows them.
var CandidateStatuses = []string{"pending-onboarding", "pending-offboarding", "managed", "ineligible", "dismissed", "excluded", "out-of-scope", "gone"}

// RunRecord is a discovery run as the manager records it.
type RunRecord struct {
	StartedAt     string `json:"startedAt"`
	FinishedAt    string `json:"finishedAt"`
	Trigger       string `json:"trigger"`
	By            string `json:"by"`
	Status        string `json:"status"` // running, ok, partial, failed or skipped
	SkippedReason string `json:"skippedReason"`
	Error         string `json:"error"`
	Targets       []struct {
		Account string `json:"account"`
		Region  string `json:"region"`
		Status  string `json:"status"`
		Error   string `json:"error"`
	} `json:"targets"`
	EmptyPairs int `json:"emptyPairs"`
	Skipped    []struct {
		Reason string `json:"reason"`
		Pairs  int    `json:"pairs"`
	} `json:"skipped"`
	Lint       []LintFinding `json:"lint"`
	Delegation string        `json:"delegation"`
}

// LintFinding is something the discovery configuration's filters do that is probably not meant.
type LintFinding struct {
	Code    string `json:"code"`
	Level   string `json:"level"`
	Filter  string `json:"filter"`
	Include int    `json:"include"`
	By      string `json:"by"`
	Message string `json:"message"`
}

// Ended tells whether the run is over (it shows as running while its accounts are scanned).
func (r RunRecord) Ended() bool { return r.Status != "running" }

// DiscoveryStatus is the answer of GET /iam/discovery: the last run, a Run now refused since, the candidates per
// status, and the roles set in the manager's Terraform. The answer as the API sent it is kept.
type DiscoveryStatus struct {
	LastRun *RunRecord     `json:"lastRun"`
	Refused *RunRecord     `json:"refused"`
	Counts  map[string]int `json:"counts"`
	Access  *struct {
		DiscoveryRole string `json:"discoveryRole"`
		CommandRole   string `json:"commandRole"`
	} `json:"access"`

	raw json.RawMessage
}

type discoveryStatusFields DiscoveryStatus // without the methods below, so that decoding does not recurse

func (d *DiscoveryStatus) UnmarshalJSON(data []byte) error {
	var f discoveryStatusFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*d = DiscoveryStatus(f)
	d.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON is the answer as the API sent it, or the fields known when it was built here.
func (d DiscoveryStatus) MarshalJSON() ([]byte, error) {
	if d.raw != nil {
		return d.raw, nil
	}
	return json.Marshal(discoveryStatusFields(d))
}

// Text is the last run (when, by whom, what it scanned and found wrong), the run refused since, the candidates per
// status, the roles and the lint of the run, times in loc.
func (d DiscoveryStatus) Text(loc *time.Location) string {
	rows := [][2]string{{"last run", "none yet"}}
	if r := d.LastRun; r != nil {
		rows[0][1] = runText(*r, loc)
		scanned, skipped := r.EmptyPairs, 0
		var failed []string
		for _, t := range r.Targets {
			switch t.Status {
			case "ok":
				scanned++
			case "error":
				failed = append(failed, fmt.Sprintf("%s %s: %s", t.Account, t.Region, t.Error))
			}
		}
		if len(r.Skipped) > 0 {
			for _, s := range r.Skipped {
				skipped += s.Pairs
			}
		} else {
			for _, t := range r.Targets {
				if t.Status == "skipped" {
					skipped++
				}
			}
		}
		rows = append(rows, [2]string{"scanned", fmt.Sprintf("%d account-regions, %d skipped, %d failed", scanned, skipped, len(failed))},
			[2]string{"failed", strings.Join(failed, "\n")}, [2]string{"error", r.Error}, [2]string{"skipped because", r.SkippedReason})
		if r.Delegation == "missing" {
			rows = append(rows, [2]string{"delegation", "missing: the organization's accounts could not be listed"})
		}
	}
	if r := d.Refused; r != nil {
		rows = append(rows, [2]string{"refused", fmt.Sprintf("a run requested %s%s was refused: %s", When(r.StartedAt, loc), byText(r.By), r.SkippedReason)})
	}
	var counts []string
	for _, s := range CandidateStatuses {
		if n := d.Counts[s]; n > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", n, s))
		}
	}
	if len(counts) == 0 {
		counts = []string{"none"}
	}
	rows = append(rows, [2]string{"candidates", strings.Join(counts, ", ")})
	if a := d.Access; a != nil {
		rows = append(rows, [2]string{"discovery role", a.DiscoveryRole}, [2]string{"command role", a.CommandRole})
	}
	text := Labelled(rows)
	if d.LastRun != nil && len(d.LastRun.Lint) > 0 {
		text += "\n" + LintText(d.LastRun.Lint)
	}
	return text
}

// runText is a run in one line: when it started, how, its status and how long it took.
func runText(r RunRecord, loc *time.Location) string {
	s := When(r.StartedAt, loc) + ", " + r.Trigger + byText(r.By) + ", " + r.Status
	if r.Ended() {
		if secs, ok := seconds(r.StartedAt, r.FinishedAt); ok {
			s += fmt.Sprintf(", %ds", secs)
		}
	}
	return s
}

func byText(by string) string {
	if by == "" {
		return ""
	}
	return " by " + by
}

func seconds(from, to string) (int, bool) {
	a, err1 := time.Parse(time.RFC3339, from)
	b, err2 := time.Parse(time.RFC3339, to)
	if err1 != nil || err2 != nil || b.Before(a) {
		return 0, false
	}
	return int(b.Sub(a).Round(time.Second) / time.Second), true
}

// LintText is a line per finding, "warning: <message> (filter <name>, include <n>)"; "no lint findings" for none.
func LintText(fs []LintFinding) string {
	if len(fs) == 0 {
		return "no lint findings\n"
	}
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "%s: %s (filter %s, include %d)\n", f.Level, f.Message, f.Filter, f.Include)
	}
	return b.String()
}

// AccessRow is an account of the perimeter whose access is not complete.
type AccessRow struct {
	Account   string      `json:"account"`
	Name      string      `json:"name"`
	Discovery AccessState `json:"discovery"`
	Command   AccessState `json:"command"`
	Forwarder string      `json:"forwarder"`
	Reason    string      `json:"reason"`
}

// AccessState is whether a role of an account works, and why not.
type AccessState struct {
	OK            bool   `json:"ok"`
	Reason        string `json:"reason"`
	DiscoveryOnly bool   `json:"discoveryOnly"`
}

func (s AccessState) text() string {
	switch {
	case s.OK:
		return "ok"
	case s.DiscoveryOnly:
		return "none (discovery only)"
	case s.Reason != "":
		return "missing: " + s.Reason
	}
	return "missing"
}

// AccessReport is the organization access of the last run: GET /iam/discovery/access, its accounts the ones read so
// far (every page). The first page's answer is kept, with the accounts as read, so --json prints every field.
type AccessReport struct {
	Counts struct {
		Ready            int `json:"ready"`
		DiscoveryMissing int `json:"discoveryMissing"`
		CommandMissing   int `json:"commandMissing"`
		DiscoveryOnly    int `json:"discoveryOnly"`
		Unchecked        int `json:"unchecked"`
	} `json:"counts"`
	Accounts   []AccessRow `json:"accounts"`
	Cursor     *string     `json:"cursor"`
	Delegation string      `json:"delegation"`
	CheckedAt  string      `json:"checkedAt"`
	Forwarders *struct {
		Connected  int `json:"connected"`
		Connecting int `json:"connecting"`
		Impossible []struct {
			Account string `json:"account"`
			Name    string `json:"name"`
			Reason  string `json:"reason"`
		} `json:"impossible"`
		Failed []struct {
			Account string `json:"account"`
			Name    string `json:"name"`
			Reason  string `json:"reason"`
		} `json:"failed"`
	} `json:"forwarders"`

	raw  map[string]json.RawMessage
	rows []json.RawMessage
}

func (r *AccessReport) UnmarshalJSON(data []byte) error {
	type fields AccessReport // without the methods, so that decoding does not recurse
	var f fields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	var page struct {
		Accounts []json.RawMessage `json:"accounts"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return err
	}
	*r = AccessReport(f)
	r.rows = page.Accounts
	return json.Unmarshal(data, &r.raw)
}

// MarshalJSON is the first page's answer with the accounts of every page read and the cursor after the last.
func (r AccessReport) MarshalJSON() ([]byte, error) {
	if r.raw == nil {
		type fields AccessReport
		return json.Marshal(fields(r))
	}
	m := make(map[string]json.RawMessage, len(r.raw))
	for k, v := range r.raw {
		m[k] = v
	}
	rows := r.rows
	if rows == nil {
		rows = []json.RawMessage{}
	}
	m["accounts"], _ = json.Marshal(rows)
	m["cursor"], _ = json.Marshal(r.Cursor)
	return json.Marshal(m)
}

// accessPage is the most accounts the manager gives in a page.
const accessPage = 1000

// ReadAccess reads the access report page by page and stops at limit accounts when it is above zero.
func ReadAccess(ctx context.Context, c *Client, limit int) (AccessReport, error) {
	var out AccessReport
	cursor := ""
	for first := true; ; first = false {
		size := accessPage
		if limit > 0 {
			size = min(size, limit-len(out.Accounts))
		}
		q := url.Values{"limit": {strconv.Itoa(size)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page AccessReport
		if err := c.Get(ctx, "/discovery/access", q, &page); err != nil {
			return out, err
		}
		if first {
			out = page
		} else {
			out.Accounts = append(out.Accounts, page.Accounts...)
			out.rows = append(out.rows, page.rows...)
			out.Cursor = page.Cursor
		}
		if limit > 0 && len(out.Accounts) >= limit {
			out.Accounts, out.rows = out.Accounts[:limit], out.rows[:limit]
			return out, nil
		}
		if page.Cursor == nil || *page.Cursor == "" || *page.Cursor == cursor {
			return out, nil
		}
		cursor = *page.Cursor
	}
}

// Text is the counts, the delegation and forwarders, then a table of the accounts not ready, times in loc.
func (r AccessReport) Text(loc *time.Location) string {
	n := r.Counts
	counts := fmt.Sprintf("%d ready, %d without the discovery role, %d without the command role, %d not checked", n.Ready, n.DiscoveryMissing, n.CommandMissing, n.Unchecked)
	if n.DiscoveryOnly > 0 {
		counts += fmt.Sprintf(", %d for discovery only", n.DiscoveryOnly)
	}
	rows := [][2]string{{"checked", When(r.CheckedAt, loc)}, {"accounts", counts}}
	if r.Delegation == "missing" {
		rows = append(rows, [2]string{"delegation", "missing: the organization's accounts could not be listed"})
	}
	if f := r.Forwarders; f != nil {
		var impossible, failed []string
		for _, i := range f.Impossible {
			impossible = append(impossible, fmt.Sprintf("%s (%s): %s", i.Name, i.Account, i.Reason))
		}
		for _, i := range f.Failed {
			failed = append(failed, fmt.Sprintf("%s (%s): %s", i.Name, i.Account, i.Reason))
		}
		rows = append(rows, [2]string{"forwarders", fmt.Sprintf("%d connected, %d connecting", f.Connected, f.Connecting)},
			[2]string{"not possible", strings.Join(impossible, "\n")}, [2]string{"failed", strings.Join(failed, "\n")})
	}
	text := Labelled(rows)
	if len(r.Accounts) == 0 {
		return text + "\nevery account checked is ready\n"
	}
	table := [][]string{{"ACCOUNT", "NAME", "DISCOVERY ROLE", "COMMAND ROLE", "FORWARDER"}}
	for _, a := range r.Accounts {
		table = append(table, []string{a.Account, orNone(a.Name), a.Discovery.text(), a.Command.text(), orNone(a.Forwarder)})
	}
	return text + "\n" + Table(table)
}

// DiscoveryConfig is the answer of GET and PUT /iam/discovery/config: the configuration (kept as it is, to be printed
// and edited), when and by whom it was last set, and what each of its filters matches.
type DiscoveryConfig struct {
	Config    json.RawMessage `json:"config"`
	UpdatedAt string          `json:"updatedAt"`
	UpdatedBy string          `json:"updatedBy"`
	Lint      []LintFinding   `json:"lint"`

	raw json.RawMessage
}

type discoveryConfigFields DiscoveryConfig // without the methods below, so that decoding does not recurse

func (d *DiscoveryConfig) UnmarshalJSON(data []byte) error {
	var f discoveryConfigFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*d = DiscoveryConfig(f)
	d.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON is the answer as the API sent it, or the fields known when it was built here.
func (d DiscoveryConfig) MarshalJSON() ([]byte, error) {
	if d.raw != nil {
		return d.raw, nil
	}
	return json.Marshal(discoveryConfigFields(d))
}

// Text is the configuration as indented JSON, then the lint.
func (d DiscoveryConfig) Text() string {
	config := []byte(d.Config)
	if indented, err := json.MarshalIndent(d.Config, "", "  "); err == nil {
		config = indented
	}
	return string(config) + "\n\n" + LintText(d.Lint)
}
