package gcponboard

// `dormeto gcp check`: which of the permissions of each `dormeto gcp` command the caller lacks in the project, from
// testIamPermissions and the manifest, before anything changes. It reads; it never writes.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/gcp/access"
	"github.com/Dormeto/cli/internal/install"
)

// Deps are what the runs use: the Google client (its token source names the account), the person's terminal, the output,
// the clock, the cache directory and the CLI's version.
type Deps struct {
	Google *gcp.Client
	// Prompter asks for a confirmation; only when Terminal (stdin is a terminal) and --yes is not given.
	Prompter install.Prompter
	Terminal bool
	Out      io.Writer
	Err      io.Writer
	Now      func() time.Time
	// Sleep waits between the retries of a call Google is not ready for yet.
	Sleep func(ctx context.Context, d time.Duration) error
	// Cache is the user's cache directory ($XDG_CACHE_HOME, else ~/.cache): an install's pending record waits in
	// <Cache>/dormeto/gcp/ until the router's database holds it.
	Cache   string
	Version string // the CLI's own version
}

// Request is what the flags ask.
type Request struct {
	Project, Region, Image   string
	Yes, DryRun, JSON, Force bool
}

// ProjectInfo is the project as the report names it; Number is empty when the caller cannot see it.
type ProjectInfo struct {
	ID     string `json:"id"`
	Number string `json:"number"`
	Parent string `json:"parent"`
}

// CommandReport is the verdict for one command.
type CommandReport struct {
	Command string   `json:"command"`
	Held    []string `json:"held"`
	Missing []string `json:"missing"`
}

// OK tells that the caller holds every permission of the command.
func (c CommandReport) OK() bool { return len(c.Missing) == 0 }

// APIReport are the manifest's APIs, on and off.
type APIReport struct {
	Enabled []string `json:"enabled"`
	Missing []string `json:"missing"`
}

// Report is the result of Check.
type Report struct {
	Project  ProjectInfo     `json:"project"`
	Account  string          `json:"account"`
	Commands []CommandReport `json:"commands"`
	APIs     APIReport       `json:"apis"`
	Warnings []string        `json:"warnings"`

	// Visible is false when the caller has no role in the project; the APIs were then not checked.
	Visible bool `json:"-"`
	// APIsNote is why the APIs were not checked, when the services listing was refused.
	APIsNote string `json:"-"`
}

const apisRefused = "APIs: not checked (serviceusage.services.list refused)"

// OK tells that every command is allowed.
func (r Report) OK() bool {
	for _, c := range r.Commands {
		if !c.OK() {
			return false
		}
	}
	return true
}

const (
	crm           = "https://cloudresourcemanager.googleapis.com/v3/projects/"
	serviceUsage  = "https://serviceusage.googleapis.com/v1/projects/"
	permissionMax = 100
)

type projectBody struct {
	Name      string `json:"name"`
	ProjectID string `json:"projectId"`
	Parent    string `json:"parent"`
	State     string `json:"state"`
}

// testPermissions are the permissions of perms the caller holds, asked 100 at a time.
func testPermissions(ctx context.Context, c *gcp.Client, project string, perms []string) ([]string, error) {
	held := []string{}
	for batch := range slices.Chunk(perms, permissionMax) {
		var out struct {
			Permissions []string `json:"permissions"`
		}
		in := map[string]any{"permissions": batch}
		if err := c.Do(ctx, http.MethodPost, crm+url.PathEscape(project)+":testIamPermissions", in, &out); err != nil {
			return nil, err
		}
		held = append(held, out.Permissions...)
	}
	return held, nil
}

func denied(err error) bool { return gcp.IsDenied(err) || gcp.IsNotFound(err) }

// Check tells, per command, the permissions the caller lacks in the project. A caller with no role in the project is a
// report (every command refused), not an error; any other refusal by Google is an error.
func Check(ctx context.Context, d Deps, r Request) (Report, error) {
	account, err := d.Google.Tokens.Account(ctx)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Project: ProjectInfo{ID: r.Project}, Account: account, Warnings: []string{}, APIs: APIReport{Enabled: []string{}, Missing: []string{}}}

	var p projectBody
	err = d.Google.Do(ctx, http.MethodGet, crm+url.PathEscape(r.Project), nil, &p)
	switch {
	case err == nil:
		if p.State != "" && p.State != "ACTIVE" {
			return Report{}, fmt.Errorf("project %s is %s", r.Project, p.State)
		}
		rep.Visible = true
		rep.Project.Number = strings.TrimPrefix(p.Name, "projects/")
		rep.Project.Parent = p.Parent
	case denied(err):
	default:
		return Report{}, err
	}

	var all []string
	for _, c := range access.Commands() {
		all = append(all, access.Needs(c)...)
	}
	slices.Sort(all)
	all = slices.Compact(all)
	held, err := testPermissions(ctx, d.Google, r.Project, all)
	if err != nil {
		if !denied(err) || rep.Visible {
			return Report{}, err
		}
		held = nil
	}
	for _, c := range access.Commands() {
		cr := CommandReport{Command: c, Held: []string{}, Missing: []string{}}
		for _, perm := range access.Needs(c) {
			if rep.Visible && slices.Contains(held, perm) {
				cr.Held = append(cr.Held, perm)
			} else {
				cr.Missing = append(cr.Missing, perm)
			}
		}
		rep.Commands = append(rep.Commands, cr)
	}
	if !rep.Visible {
		return rep, nil
	}

	enabled, err := enabledServices(ctx, d.Google, rep.Project.Number)
	switch {
	case gcp.IsDenied(err):
		rep.APIsNote = apisRefused
		rep.Warnings = append(rep.Warnings, apisRefused)
	case err != nil:
		return Report{}, err
	default:
		for _, api := range access.Load().APIs {
			if slices.Contains(enabled, api) {
				rep.APIs.Enabled = append(rep.APIs.Enabled, api)
			} else {
				rep.APIs.Missing = append(rep.APIs.Missing, api)
			}
		}
	}

	rep.Warnings = append(rep.Warnings, defaultServiceAccountWarning(ctx, d.Google, rep.Project)...)
	return rep, nil
}

func enabledServices(ctx context.Context, c *gcp.Client, number string) ([]string, error) {
	var names []string
	token := ""
	for {
		u := serviceUsage + url.PathEscape(number) + "/services?filter=state:ENABLED&pageSize=200"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		var out struct {
			Services []struct {
				Name string `json:"name"`
			} `json:"services"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.Do(ctx, http.MethodGet, u, nil, &out); err != nil {
			return nil, err
		}
		for _, s := range out.Services {
			_, api, _ := strings.Cut(s.Name, "/services/")
			names = append(names, api)
		}
		if token = out.NextPageToken; token == "" {
			return names, nil
		}
	}
}

// defaultServiceAccountWarning warns when the Compute Engine default service account has Editor on the project, and
// says so when the policy cannot be read.
func defaultServiceAccountWarning(ctx context.Context, c *gcp.Client, p ProjectInfo) []string {
	var policy struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	in := map[string]any{"options": map[string]int{"requestedPolicyVersion": 3}}
	if err := c.Do(ctx, http.MethodPost, ProjectPolicy(p.ID).Get, in, &policy); err != nil {
		return []string{"the IAM policy is not readable: the default service account was not checked"}
	}
	email := p.Number + "-compute@developer.gserviceaccount.com"
	for _, b := range policy.Bindings {
		if b.Role == "roles/editor" && slices.Contains(b.Members, "serviceAccount:"+email) {
			return []string{fmt.Sprintf("the Compute Engine default service account %s has Editor on project %s: Dormeto does not use it; remove the grant when nothing else needs it", email, p.ID)}
		}
	}
	return nil
}

// PrintText prints the report, one line each.
func PrintText(w io.Writer, r Report) error {
	var b strings.Builder
	if !r.Visible {
		fmt.Fprintf(&b, "Project %s is not visible to %s: no role in it\n", r.Project.ID, r.Account)
	} else {
		where := "no organization"
		switch kind, n, _ := strings.Cut(r.Project.Parent, "/"); kind {
		case "organizations":
			where = "in organization " + n
		case "folders":
			where = "in folder " + n
		}
		fmt.Fprintf(&b, "Project %s (%s), %s, checked as %s\n", r.Project.ID, r.Project.Number, where, r.Account)
	}
	for _, c := range r.Commands {
		if c.OK() {
			fmt.Fprintf(&b, "  ✓ %s: every permission held\n", c.Command)
		} else {
			fmt.Fprintf(&b, "  ✗ %s: missing %s\n", c.Command, strings.Join(c.Missing, ", "))
		}
	}
	switch {
	case !r.Visible:
		b.WriteString("  APIs: not checked\n")
	case r.APIsNote != "":
		fmt.Fprintf(&b, "  %s\n", r.APIsNote)
	default:
		fmt.Fprintf(&b, "  APIs on: %s\n", listOrNone(r.APIs.Enabled))
		fmt.Fprintf(&b, "  APIs off: %s\n", listOrNone(r.APIs.Missing))
	}
	for _, warning := range r.Warnings {
		if warning == r.APIsNote {
			continue
		}
		fmt.Fprintf(&b, "  ! %s\n", warning)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func listOrNone(l []string) string {
	if len(l) == 0 {
		return "none"
	}
	return strings.Join(l, ", ")
}

// PrintJSON prints the report as one JSON object.
func PrintJSON(w io.Writer, r Report) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

// RunCheck is `dormeto gcp check`: the report printed, and the exit code: 0 when every command is allowed, 1 when
// any is not or Google refuses.
func RunCheck(ctx context.Context, d Deps, r Request) int {
	rep, err := Check(ctx, d, r)
	if err != nil {
		fmt.Fprintln(d.Err, err)
		return 1
	}
	if r.JSON {
		err = PrintJSON(d.Out, rep)
	} else {
		err = PrintText(d.Out, rep)
	}
	if err != nil {
		fmt.Fprintln(d.Err, err)
		return 1
	}
	if !rep.OK() {
		return 1
	}
	return 0
}
