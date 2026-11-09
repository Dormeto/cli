package gcponboard

// `dormeto gcp check`: which of the permissions of each `dormeto gcp` command the caller lacks in the project, from
// testIamPermissions and the manifest, before anything changes. It reads; it never writes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/fleet"
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

// Request is what the flags ask. Configuration is --gcloud-configuration ("": gcloud's active one), named in the
// gcloud commands the CLI tells to run.
type Request struct {
	Project, Region, Image   string
	Configuration            string
	Yes, DryRun, JSON, Force bool

	// The install's router (design §4): Version is --version (Dormeto's public image of that version), AccessOnly
	// --access-only (the access without the router); Image, --image, is a router image of the customer's own.
	Version    string
	AccessOnly bool

	// The workloads commands: the workload's ID (an argument, or --id of onboard), the URL map and backend service to
	// onboard, and the settings given (nil: not given).
	ID, URLMap, BackendService string
	// Name is --name of onboard (the workload's name for people, trimmed; "": the router's, the ID) and Tags its --tag, Dormeto's
	// labels (nil: none), both checked by the command.
	Name        string
	Tags        map[string]string
	IdleMinutes *int
	Hibernate   *bool

	// The onboard command's load balancer (--load-balancer: global, regional, classic, internal or cross-region-internal), wake timeout (--wake-timeout-seconds, nil:
	// not given) and Cloud SQL companions (--cloud-sql); its GKE cluster is GKECluster. RequestLogs is --request-logs: the
	// backend service's request logs turned on, every request, for the idle check to read (the G4 design's §5).
	LoadBalancer string
	WakeTimeout  *int
	// WakeMode is --wake-mode (any or browser) and FallbackTypes --fallback-type, the machine types a VM of an unmanaged group may
	// run on while its own is out of capacity; none given, none sent.
	WakeMode      string
	FallbackTypes []string
	CloudSQL      []string
	RequestLogs   bool

	// The settings commands: the workloads' IDs (the argument of settings is one or more), and the flat body the level flags
	// give: each key a value, or null to give it back to the levels. A router's: hibernate, joinMinutes and defaults.
	// Empty for the router's: its level is shown.
	IDs    []string
	Levels map[string]any

	// The waiting-page commands: the document `set` writes, checked by the command before any call (ID is the workload's).
	Page *fleet.PageDoc

	// The schedule and pause commands: the schedule's file (set), the pause's reason, --keep-asleep and --until (RFC 3339).
	File, Reason, Until string
	KeepAsleep          bool

	// The access and onboard commands: the cluster as <location>/<name> (--gke-cluster); access's: the workload's namespace,
	// and ID is --workload.
	GKECluster, Namespace string

	// With are the optional features (--with): cloud-sql, gke.
	With []string

	// IPLists are the router's IP lists (--ip-list); NoIPLists clears them (--no-ip-lists).
	IPLists   []IPList
	NoIPLists bool

	// CustomPages is --custom-pages: the router's custom waiting pages on, or off with --custom-pages=false; nil when not
	// given, the install record's switch kept.
	CustomPages *bool
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

// BillingReport tells whether the project is linked to an open billing account (Checked false: it could not be read,
// a warning says why). Without billing, Google refuses to turn the APIs on.
type BillingReport struct {
	Checked bool `json:"checked"`
	Enabled bool `json:"enabled"`
}

// Report is the result of Check.
type Report struct {
	Project  ProjectInfo     `json:"project"`
	Account  string          `json:"account"`
	Commands []CommandReport `json:"commands"`
	APIs     APIReport       `json:"apis"`
	Billing  BillingReport   `json:"billing"`
	Warnings []string        `json:"warnings"`

	// Visible is false when the caller has no role in the project; the APIs were then not checked.
	Visible bool `json:"-"`
	// APIsNote is why the APIs were not checked, when the services listing was refused.
	APIsNote string `json:"-"`
	// configuration is the request's, named in the commands the report tells to run.
	configuration string
}

const apisRefused = "APIs: not checked (serviceusage.services.list refused)"

// OK tells that every command is allowed and that the project is not known to lack billing.
func (r Report) OK() bool {
	for _, c := range r.Commands {
		if !c.OK() {
			return false
		}
	}
	return !r.NoBilling()
}

// NoBilling tells that the project has no open billing account.
func (r Report) NoBilling() bool { return r.Billing.Checked && !r.Billing.Enabled }

// noBilling is the ✗ line of a project without billing, with the command that links an account.
func noBilling(project, configuration string) string {
	link := "gcloud billing projects link " + project + " --billing-account <id>"
	if configuration != "" {
		link += " --configuration " + configuration
	}
	return fmt.Sprintf("billing: not enabled on project %s: link a billing account (%s), then run again", project, link)
}

const (
	cloudBilling  = "https://cloudbilling.googleapis.com/v1/projects/"
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

// testPermissions are the permissions of perms the caller holds in a project, asked 100 at a time. A batch Google refuses
// with a 400 (a permission it does not know) is asked again one permission at a time, still read-only, and the error names
// the permissions refused.
func testPermissions(ctx context.Context, c *gcp.Client, project string, perms []string) ([]string, error) {
	return testPermissionsOn(ctx, c, "projects/"+url.PathEscape(project), perms)
}

// testPermissionsOn is testPermissions on a project, a folder or an organization (projects/<id>, folders/<n>,
// organizations/<n>).
func testPermissionsOn(ctx context.Context, c *gcp.Client, resource string, perms []string) ([]string, error) {
	held := []string{}
	var rejected []string
	var first error
	for batch := range slices.Chunk(perms, permissionMax) {
		got, err := testBatch(ctx, c, resource, batch)
		if err == nil {
			held = append(held, got...)
			continue
		}
		if !invalid(err) {
			return nil, err
		}
		if first == nil {
			first = err
		}
		for _, p := range batch {
			got, err := testBatch(ctx, c, resource, []string{p})
			switch {
			case err == nil:
				held = append(held, got...)
			case invalid(err):
				rejected = append(rejected, p)
			default:
				return nil, err
			}
		}
	}
	switch {
	case len(rejected) > 0:
		return nil, fmt.Errorf("testIamPermissions rejects %s (%w)", strings.Join(rejected, ", "), first)
	case first != nil:
		return nil, first
	}
	return held, nil
}

func testBatch(ctx context.Context, c *gcp.Client, resource string, perms []string) ([]string, error) {
	var out struct {
		Permissions []string `json:"permissions"`
	}
	in := map[string]any{"permissions": perms}
	if err := c.Do(ctx, http.MethodPost, crmV3+resource+":testIamPermissions", in, &out); err != nil {
		return nil, err
	}
	return out.Permissions, nil
}

// invalid is Google's 400 for a request it does not accept as written.
func invalid(err error) bool {
	var e *gcp.Error
	return errors.As(err, &e) && e.Code == http.StatusBadRequest
}

func denied(err error) bool { return gcp.IsDenied(err) || gcp.IsNotFound(err) }

// Check tells, per command, the permissions the caller lacks in the project. A caller with no role in the project is a
// report (every command refused), not an error; any other refusal by Google is an error.
func Check(ctx context.Context, d Deps, r Request) (Report, error) {
	if err := access.CheckFeatures(r.With); err != nil {
		return Report{}, fmt.Errorf("--with: %w", err)
	}
	// The features of the router's install record are tested too, when the region says which router, and the image's
	// permissions and API when it runs an image of --image.
	warning := ""
	if r.Region != "" {
		rec, err := recordInDatabase(newRun(ctx, d, r))
		if err != nil {
			warning = "the install record is not read, so its features and an image of --image are not tested: " + err.Error()
		}
		r.With = featureSet(r.With, rec.Features)
		if rec.Image != "" && rec.Digest == "" {
			r.Image = rec.Image
		}
	}
	// It takes no onboarding flag: it tests the whole onboard list, --request-logs's too.
	r.RequestLogs = true
	rep, err := check(ctx, d, r, access.Commands())
	if err == nil && warning != "" {
		rep.Warnings = append(rep.Warnings, warning)
	}
	return rep, err
}

// check is Check for some commands only: their permissions alone are asked (install asks exactly what its steps need).
// install-router's image step and Artifact Registry's API are asked for an image of the customer's own (r.Image: --image,
// or the recorded one) only; the requestLogs step whole with RequestLogs only: --request-logs of onboard, else its backend
// services' reads; of offboard, a workload whose onboarding turned the logs on, else nothing of it.
func check(ctx context.Context, d Deps, r Request, commands []string) (Report, error) {
	ownImage := r.Image != ""
	needs := func(command string) []string {
		// connect's and disconnect's folder and organization bindings: Google refuses to test them on a project, and the
		// commands test them at those scopes.
		skip := []string{"folderBindings", "organizationBindings"}
		if !ownImage {
			skip = append(skip, imageStep)
		}
		switch {
		case r.RequestLogs || (command != "onboard" && command != "offboard"):
			return access.NeedsWithout(command, r.With, skip...)
		case command == "offboard":
			return access.NeedsWithout(command, r.With, append(skip, requestLogsStep)...)
		}
		out := access.NeedsWithout(command, r.With, append(skip, requestLogsStep)...)
		for _, p := range backendServiceReads {
			if slices.Contains(access.Needs(command), p) {
				out = append(out, p)
			}
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	account, err := d.Google.Tokens.Account(ctx)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Project: ProjectInfo{ID: r.Project}, Account: account, Warnings: []string{}, APIs: APIReport{Enabled: []string{}, Missing: []string{}},
		configuration: r.Configuration}

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
	for _, c := range commands {
		all = append(all, needs(c)...)
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
	for _, c := range commands {
		cr := CommandReport{Command: c, Held: []string{}, Missing: []string{}}
		for _, perm := range needs(c) {
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
		for _, api := range apisWith(r.With, ownImage) {
			if slices.Contains(enabled, api) {
				rep.APIs.Enabled = append(rep.APIs.Enabled, api)
			} else {
				rep.APIs.Missing = append(rep.APIs.Missing, api)
			}
		}
	}

	var billing struct {
		BillingEnabled bool `json:"billingEnabled"`
	}
	// projects.getBillingInfo needs resourcemanager.projects.get, which the read step holds.
	switch err := d.Google.Do(ctx, http.MethodGet, cloudBilling+url.PathEscape(r.Project)+"/billingInfo", nil, &billing); {
	case err == nil:
		rep.Billing = BillingReport{Checked: true, Enabled: billing.BillingEnabled}
	// Refused, or out of quota: gcloud's token has no quota project, so the read counts against a quota Google shares between
	// gcloud's users (seen in the acceptance). The read is advice: Google refuses to turn APIs on without billing anyway.
	case gcp.IsDenied(err), gcp.IsExhausted(err):
		rep.Warnings = append(rep.Warnings, "billing not checked: "+err.Error())
	default:
		return Report{}, err
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
	policy, err := read(ctx, c, ProjectPolicy(p.ID))
	if err != nil {
		return []string{"the IAM policy is not readable: the default service account was not checked"}
	}
	if w := defaultEditor(policy, p.ID, p.Number); w != "" {
		return []string{w}
	}
	return nil
}

// defaultEditor is the warning of the Compute Engine default service account holding Editor in a project's policy (""
// when it does not): Google grants it when Compute Engine is turned on, unless an organization's policy forbids it.
func defaultEditor(policy *Policy, project, number string) string {
	email := number + "-compute@developer.gserviceaccount.com"
	for _, g := range grantsOf(policy) {
		if g.Role == "roles/editor" && g.Member == "serviceAccount:"+email {
			return fmt.Sprintf("the Compute Engine default service account %s has Editor on project %s: Dormeto does not use it; remove the grant when nothing else needs it", email, project)
		}
	}
	return ""
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
	switch {
	case r.NoBilling():
		fmt.Fprintf(&b, "  ✗ %s\n", noBilling(r.Project.ID, r.configuration))
	case r.Billing.Checked:
		b.WriteString("  ✓ billing: enabled\n")
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
	if err := access.CheckFeatures(r.With); err != nil {
		fmt.Fprintln(d.Err, "--with: "+err.Error())
		return 2
	}
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
