package fleet

// The manager's own update, as the dashboard's Update page shows it: its state (GET /update), the daily check (GET|PUT
// /update/check), the release notes (GET /update/changes), and the requests that plan and apply it (POST
// /update/plan|apply), each recorded as an update-manager operation. Each answer keeps the API's JSON for --json.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/install"
)

// accessProcedure is the page that says how an organization's access is applied again.
const accessProcedure = "https://dormeto.com/organization-access.html"

// versionText is a version as the manager accepts one: 1.2.3, with an optional pre-release.
var versionText = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// IsVersion tells whether v is a version the manager accepts (1.2.3, 1.2.3-rc.1).
func IsVersion(v string) bool { return versionText.MatchString(v) }

// VersionEntry is a version the manager can be updated to: its router version, the CLI it needs, when it was published,
// whether it is the release staged in the install bucket, and why it cannot be applied from the manager (Disabled).
type VersionEntry struct {
	Version   string `json:"version"`
	Router    string `json:"router"`
	MinCLI    string `json:"minCli"`
	Published string `json:"published"`
	Staged    bool   `json:"staged,omitempty"`
	Disabled  string `json:"disabled,omitempty"`
}

// PlanSummary is what a plan changes: resources added, changed and destroyed, and the addresses replaced or destroyed.
type PlanSummary struct {
	Add       int      `json:"add"`
	Change    int      `json:"change"`
	Destroy   int      `json:"destroy"`
	Replaced  []string `json:"replaced"`
	Destroyed []string `json:"destroyed"`
}

// UpdatePlan is the plan ready to apply: its build (what POST /update/apply takes), its version and summary.
type UpdatePlan struct {
	Build   string      `json:"build"`
	Version string      `json:"version"`
	Summary PlanSummary `json:"summary"`
}

// UpdatePending is a request waiting for its build (the answer of POST /update/plan and apply too): Operation is the
// update-manager operation it is recorded as; NotStarted, no build took it in time.
type UpdatePending struct {
	ID         string `json:"id"`
	Action     string `json:"action"`
	Version    string `json:"version"`
	PlanBuild  string `json:"planBuild,omitempty"`
	At         string `json:"at"`
	Who        string `json:"who"`
	Operation  string `json:"operation,omitempty"`
	NotStarted bool   `json:"notStarted,omitempty"`
}

// UpdateBuild is the install project's newest build: started by the manager (dashboard) or the CLI, its steps and, when
// it failed, the end of its log.
type UpdateBuild struct {
	ID         string   `json:"id"`
	By         string   `json:"by"`
	Action     string   `json:"action"`
	Version    string   `json:"version"`
	Status     string   `json:"status"`
	Phase      string   `json:"phase"`
	StartedAt  string   `json:"startedAt"`
	EndedAt    string   `json:"endedAt"`
	Steps      []string `json:"steps"`
	Tail       []string `json:"tail"`
	ConsoleURL string   `json:"consoleUrl"`
}

// AppliedVersion is the version the install last applied, by its build.
type AppliedVersion struct {
	Version string `json:"version"`
	Build   string `json:"build"`
	At      string `json:"at"`
}

// UpdateState is the answer of GET /update. A manager not installed with the CLI (InstalledWithCLI false) gives only its
// version, env and region.
type UpdateState struct {
	InstalledWithCLI    bool            `json:"installedWithCli"`
	Version             string          `json:"version"`
	Env                 string          `json:"env"`
	Region              string          `json:"region"`
	Applied             *AppliedVersion `json:"applied,omitempty"`
	CLI                 string          `json:"cli,omitempty"`
	Versions            []VersionEntry  `json:"versions,omitempty"`
	Build               *UpdateBuild    `json:"build,omitempty"`
	Pending             *UpdatePending  `json:"pending,omitempty"`
	Plan                *UpdatePlan     `json:"plan,omitempty"`
	ReleasesUnreachable bool            `json:"releasesUnreachable,omitempty"`
	kept
}

type updateStateFields UpdateState // without the methods below, so that decoding does not recurse

func (s *UpdateState) UnmarshalJSON(data []byte) error {
	var f updateStateFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*s = UpdateState(f)
	s.kept = kept{append(json.RawMessage(nil), data...)}
	return nil
}

// MarshalJSON is the answer as the API sent it.
func (s UpdateState) MarshalJSON() ([]byte, error) { return s.marshal(updateStateFields(s)) }

// Newest is the highest version offered above the running one; "" when there is none.
func (s UpdateState) Newest() string {
	newest := ""
	for _, v := range s.Versions {
		if IsVersion(v.Version) && install.CompareVersions(v.Version, s.Version) > 0 && (newest == "" || install.CompareVersions(v.Version, newest) > 0) {
			newest = v.Version
		}
	}
	return newest
}

// UpdateResult is the daily check's last result: the newest version above the one running, or why it is unknown.
type UpdateResult struct {
	CheckedAt string `json:"checkedAt"`
	Running   string `json:"running"`
	Latest    string `json:"latest"`
	Error     string `json:"error,omitempty"`
	Notified  string `json:"notified,omitempty"`
}

// UpdateCheck is the answer of GET and PUT /update/check: the daily check's setting and its last result.
type UpdateCheck struct {
	Enabled   bool          `json:"enabled"`
	Alert     bool          `json:"alert"`
	Newer     bool          `json:"newer"`
	UpdatedAt string        `json:"updatedAt,omitempty"`
	UpdatedBy string        `json:"updatedBy,omitempty"`
	Result    *UpdateResult `json:"result"`
	kept
}

type updateCheckFields UpdateCheck

func (u *UpdateCheck) UnmarshalJSON(data []byte) error {
	var f updateCheckFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*u = UpdateCheck(f)
	u.kept = kept{append(json.RawMessage(nil), data...)}
	return nil
}

// MarshalJSON is the answer as the API sent it.
func (u UpdateCheck) MarshalJSON() ([]byte, error) { return u.marshal(updateCheckFields(u)) }

// ReleaseNotes is a version's section of the changelog (Notes "" when it could not be read), and the access change it
// asks for, which the update does not apply (AccessNeeded).
type ReleaseNotes struct {
	Version      string `json:"version"`
	Notes        string `json:"notes"`
	Access       string `json:"access"`
	AccessNeeded bool   `json:"accessNeeded"`
}

// Changes is the answer of GET /update/changes: the release notes of the versions up to the one asked, newest first.
type Changes struct {
	Sections []ReleaseNotes
	kept
}

func (c *Changes) UnmarshalJSON(data []byte) error {
	var sections []ReleaseNotes
	if err := json.Unmarshal(data, &sections); err != nil {
		return err
	}
	*c = Changes{Sections: sections, kept: kept{append(json.RawMessage(nil), data...)}}
	return nil
}

// MarshalJSON is the answer as the API sent it.
func (c Changes) MarshalJSON() ([]byte, error) {
	if c.Sections == nil {
		return c.marshal([]ReleaseNotes{})
	}
	return c.marshal(c.Sections)
}

// GetUpdate reads GET /update.
func GetUpdate(ctx context.Context, c *Client) (UpdateState, error) {
	var s UpdateState
	err := c.Get(ctx, "/update", nil, &s)
	return s, err
}

// GetUpdateCheck reads GET /update/check (a 404 when the manager has no daily check).
func GetUpdateCheck(ctx context.Context, c *Client) (UpdateCheck, error) {
	var u UpdateCheck
	err := c.Get(ctx, "/update/check", nil, &u)
	return u, err
}

// PutUpdateCheck sets the daily check: PUT /update/check, the setting as written.
func PutUpdateCheck(ctx context.Context, c *Client, enabled, alert bool) (UpdateCheck, error) {
	var u UpdateCheck
	err := c.Put(ctx, "/update/check", map[string]bool{"enabled": enabled, "alert": alert}, &u)
	return u, err
}

// GetChanges reads the release notes up to the version to: GET /update/changes?to=.
func GetChanges(ctx context.Context, c *Client, to string) (Changes, error) {
	var ch Changes
	err := c.Get(ctx, "/update/changes", url.Values{"to": {to}}, &ch)
	return ch, err
}

// PlanUpdate asks the manager to plan its update to version (POST /update/plan): the update-manager operation its
// request is recorded as, running; none (no id) when the answer names no operation.
func PlanUpdate(ctx context.Context, c *Client, version string) (Operation, error) {
	return requestUpdate(ctx, c, "/update/plan", map[string]string{"version": version})
}

// ApplyUpdate asks the manager to apply the plan of its build (POST /update/apply), as PlanUpdate does.
func ApplyUpdate(ctx context.Context, c *Client, planBuild string) (Operation, error) {
	return requestUpdate(ctx, c, "/update/apply", map[string]string{"planBuild": planBuild})
}

func requestUpdate(ctx context.Context, c *Client, path string, body map[string]string) (Operation, error) {
	var p UpdatePending
	if err := c.Post(ctx, path, body, &p); err != nil || p.Operation == "" {
		return Operation{}, err
	}
	return Operation{ID: p.Operation, Type: "update-manager", Status: "running", Target: OperationTarget{Version: p.Version}}, nil
}

// Counts is what the plan changes in a few words: "2 to add, 1 to change, 0 to destroy".
func (p PlanSummary) Counts() string {
	if p.Add == 0 && p.Change == 0 && p.Destroy == 0 {
		return "none: the plan changes nothing"
	}
	return fmt.Sprintf("%d to add, %d to change, %d to destroy", p.Add, p.Change, p.Destroy)
}

// Rows are the plan as labelled lines: its version and build, what it changes, the addresses replaced and destroyed.
func (p UpdatePlan) Rows() [][2]string {
	return [][2]string{
		{"plan", p.Version + ", build " + p.Build},
		{"changes", p.Summary.Counts()},
		{"replaced", strings.Join(p.Summary.Replaced, "\n")},
		{"destroyed", strings.Join(p.Summary.Destroyed, "\n")},
	}
}

// Text is the plan's summary, as the Update page shows it.
func (p UpdatePlan) Text() string { return Labelled(p.Rows()) }

// failed tells whether the build ended otherwise than with success.
func (b UpdateBuild) failed() bool {
	return b.Status != "" && b.Status != "IN_PROGRESS" && b.Status != "SUCCEEDED"
}

// FailureText is what a failed build says of its failure: the step that failed, the end of its log and its page in the
// CodeBuild console; "" for a build that did not fail.
func (b UpdateBuild) FailureText() string {
	if !b.failed() {
		return ""
	}
	var s strings.Builder
	if n := len(b.Steps); n > 0 {
		fmt.Fprintf(&s, "the step that failed: %s\n", b.Steps[n-1])
	}
	for _, line := range b.Tail {
		fmt.Fprintf(&s, "  %s\n", line)
	}
	if link := consoleLink(b.ConsoleURL); link != "" {
		fmt.Fprintf(&s, "the build: %s\n", link)
	}
	return s.String()
}

// consoleLink is the build's console page, only when it is an https: link.
func consoleLink(u string) string {
	if strings.HasPrefix(u, "https://") {
		return u
	}
	return ""
}

// text is the build in a line: what it does, its status and when, then the step that failed and its page.
func (b *UpdateBuild) text(loc *time.Location) string {
	if b == nil {
		return ""
	}
	what := strings.TrimSpace(b.Action + " " + b.Version)
	switch {
	case b.By == "cli":
		what = "started from the CLI"
	case what == "":
		what = b.ID
	}
	s := what + ": " + b.Status
	if b.Status == "IN_PROGRESS" {
		if b.Phase != "" {
			s += " (" + b.Phase + ")"
		}
		if b.StartedAt != "" {
			s += ", started " + When(b.StartedAt, loc)
		}
	} else if b.EndedAt != "" {
		s += ", " + When(b.EndedAt, loc)
	}
	if b.failed() && len(b.Steps) > 0 {
		s += "\nthe step that failed: " + b.Steps[len(b.Steps)-1]
	}
	if link := consoleLink(b.ConsoleURL); link != "" {
		s += "\n" + link
	}
	return s
}

// newerText is the versions offered, a line each: version, publication, router version, staged, why it cannot be
// applied from the manager.
func (s UpdateState) newerText(loc *time.Location) string {
	var rows [][]string
	for _, v := range s.Versions {
		var notes []string
		if v.Router != "" {
			notes = append(notes, "router "+v.Router)
		}
		if v.Staged {
			notes = append(notes, "staged")
		}
		if v.Disabled != "" {
			notes = append(notes, v.Disabled)
		}
		rows = append(rows, []string{v.Version, orNone(When(v.Published, loc)), strings.Join(notes, ", ")})
	}
	table := strings.TrimSuffix(Table(rows), "\n")
	switch {
	case s.ReleasesUnreachable:
		return strings.TrimSuffix("the releases could not be read: try again later\n"+table, "\n")
	case table == "":
		return "none: up to date"
	}
	return table
}

// pendingText is the request waiting for its build.
func pendingText(p *UpdatePending, loc *time.Location) string {
	if p == nil {
		return ""
	}
	s := fmt.Sprintf("%s %s, requested by %s %s", p.Action, p.Version, ShortARN(p.Who), When(p.At, loc))
	if p.Operation != "" {
		s += " (operation " + p.Operation + ")"
	}
	if p.NotStarted {
		s += "\nit did not start: run it again"
	}
	return s
}

// appliedText is the version last applied, by its build and when it ended.
func appliedText(a *AppliedVersion, loc *time.Location) string {
	if a == nil {
		return ""
	}
	if a.At == "" {
		return fmt.Sprintf("%s (build %s)", a.Version, a.Build)
	}
	return fmt.Sprintf("%s (build %s, %s)", a.Version, a.Build, When(a.At, loc))
}

// rows are the daily check as labelled lines: on or off and the alert, then the last result.
func (u UpdateCheck) rows(loc *time.Location) [][2]string {
	state := "off"
	if u.Enabled {
		state = "on, alert off"
		if u.Alert {
			state = "on, alert on"
		}
	}
	last := ""
	switch r := u.Result; {
	case r != nil:
		outcome := "up to date"
		if r.Error != "" {
			outcome = r.Error
		} else if r.Latest != "" {
			outcome = r.Latest + " available"
		}
		last = When(r.CheckedAt, loc) + ": " + outcome
	case u.Enabled:
		last = "not checked yet"
	}
	return [][2]string{{"daily check", state}, {"last check", last}}
}

// Text is the daily check's setting and last result, times in loc.
func (u UpdateCheck) Text(loc *time.Location) string { return Labelled(u.rows(loc)) }

// ManagerStatus is what `dormeto manager status` reads: the version /health gives, the update's state and the daily
// check (nil when the manager has none).
type ManagerStatus struct {
	Version string       `json:"version"`
	Update  UpdateState  `json:"update"`
	Check   *UpdateCheck `json:"check"`
}

// Text is the status as labelled lines, as the Update page shows it; times in loc.
func (m ManagerStatus) Text(loc *time.Location) string {
	s := m.Update
	rows := [][2]string{{"version", m.Version}}
	if s.InstalledWithCLI {
		installed := "with the CLI"
		if s.CLI != "" {
			installed += " " + s.CLI
		}
		rows = append(rows, [][2]string{
			{"installed", installed}, {"env", s.Env}, {"region", s.Region}, {"applied", appliedText(s.Applied, loc)},
			{"newer", s.newerText(loc)}, {"last build", s.Build.text(loc)}, {"pending", pendingText(s.Pending, loc)},
		}...)
		if s.Plan != nil {
			rows = append(rows, s.Plan.Rows()...)
		}
	} else {
		rows = append(rows, [][2]string{{"installed", "with Terraform: change its module's source to update it"}, {"env", s.Env}, {"region", s.Region}}...)
	}
	if m.Check == nil {
		rows = append(rows, [2]string{"daily check", "not available"})
	} else {
		rows = append(rows, m.Check.rows(loc)...)
	}
	return Labelled(rows)
}

// Text is the release notes, newest first, each with a note when its access change is not applied by the update.
func (c Changes) Text() string {
	if len(c.Sections) == 0 {
		return "release notes unavailable\n"
	}
	parts := make([]string, 0, len(c.Sections))
	for _, s := range c.Sections {
		text := strings.TrimSpace(s.Notes)
		if text == "" {
			text = "## " + s.Version + "\nrelease notes unavailable"
		}
		if s.AccessNeeded {
			text += "\n\nnote: " + s.Access + "\n      this update does not apply it: " + accessProcedure
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n\n") + "\n"
}
