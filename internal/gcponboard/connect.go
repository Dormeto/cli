package gcponboard

// `dormeto gcp connect` (Google Cloud manager design §4.3): the manager reaches Google Cloud as two service accounts of a
// host project, dmt-<env>-discovery and dmt-<env>-command, through workload identity federation from its AWS roles
// dmt-<env>-manager-<x> (the manifest's manager section). The manager is read first: its version (0.24.0 or later), its AWS
// account and environment, which name the roles the trust admits, and its connection's record. Then as the install: the
// caller's permissions at the host and at each scope, each tested for the steps that act on it (a ✗ stops the run before
// any change); the projects at the scopes and their routers (the Cloud Run services dmtr-<region>-controller, connect_scan.go);
// the plan, each step reading what it changes — apis, roles (dormetoDiscovery where each scope defines it, dormetoCommand in
// each router project), federation (the pool and its AWS provider), serviceAccounts, impersonation
// (roles/iam.workloadIdentityUser on each account to the principal set of each manager role that acts as it), the bindings
// of the scopes and router projects, controller (roles/run.invoker), tag (roles/resourcemanager.tagUser on
// <project>/dormeto/managed); it asks (or takes --yes), applies in that order printing every IAM change, Dormeto's and
// Google's, verifies, and writes the manager's record, whose routers the manager registers at once. Run again, it plans only
// the differences (a new router's grants); none: "up to date". What Google keeps after a deletion is undeleted (a pool and
// its provider for 30 days, a custom role for 7); a role ID Google blocks without an undelete stops the run with the date it
// frees. No local state: the record is the manager's. `disconnect` is disconnect.go.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/gcp/access"
	"github.com/Dormeto/cli/internal/install"
)

// Manager is the Dormeto manager as connect and disconnect use it, through its /iam API: what it says of itself, its
// connection's record, and when its audit last recorded a disconnect.
type Manager interface {
	About(ctx context.Context) (ManagerInfo, error)
	// GoogleConnection is the record, nil when there is none.
	GoogleConnection(ctx context.Context) (*fleet.GoogleConnection, error)
	PutGoogleConnection(ctx context.Context, rec fleet.GoogleConnection) (*fleet.GoogleConnection, error)
	DeleteGoogleConnection(ctx context.Context) error
	// Disconnected is when the manager's audit last recorded the disconnect of a host project, zero when it holds none.
	Disconnected(ctx context.Context, host string) (time.Time, error)
}

// ManagerInfo is what the manager says of itself: its version (/health), its AWS account (the caller's: the manager's API
// admits IAM callers of its own account only), its environment (/update; empty: --env's) and its API's URL.
type ManagerInfo struct {
	Version, Account, Env, URL string
}

// ConnectRequest is what the flags of connect and disconnect ask: the host project, the scopes (connect's --scope,
// organizations/<n>, folders/<n> or projects/<id>), the gcloud configuration, the AWS profile, environment, region and API
// URL of the manager (named in the commands the runs tell to run; Env is used when the manager does not say its own), and
// the manager.
type ConnectRequest struct {
	Project               string
	Scopes                []string
	Configuration         string
	Profile, Env          string
	ManagerRegion, APIURL string
	Yes, DryRun, JSON     bool
	Manager               Manager
}

const (
	managerMinimum    = "0.24.0"
	iamV1             = "https://iam.googleapis.com/v1/"
	federationTimeout = 2 * time.Minute
	// roleIDBlockedDays: Google frees a deleted custom role's ID at the latest 44 days after its deletion: its permanent
	// deletion is scheduled 7 to 14 days after the request and takes 30 ("Delete a custom role", docs.cloud.google.com/iam/
	// docs/creating-custom-roles, read on 2026-11-09).
	roleIDBlockedDays = 44
)

var (
	projectIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	scopePattern      = regexp.MustCompile(`^(organizations/[0-9]+|folders/[0-9]+|projects/[a-z][a-z0-9-]{4,28}[a-z0-9])$`)
	awsAccountPattern = regexp.MustCompile(`^[0-9]{12}$`)
	envPattern        = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,8}[a-z0-9])?$`)
	runAppPattern     = regexp.MustCompile(`^https://[a-z0-9.-]+\.run\.app$`)
	corePattern       = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

// place is a resource with an IAM policy: how the plan and the IAM lines name it, where its policy is read and written, and
// the step that changes it. fresh: created by this run, its policy is not read while planning. note: why its removals are
// planned, said after each.
type place struct {
	what  string
	rp    ResourcePolicy
	step  string
	fresh bool
	note  string
}

// binding is one grant on a place.
type binding struct {
	place
	Grant
}

// roleAt is one of the manager's custom roles where it is defined: parent is projects/<id> or organizations/<n>.
type roleAt struct{ id, parent string }

func (r roleAt) name() string  { return r.parent + "/roles/" + r.id }
func (r roleAt) url() string   { return iamV1 + r.parent + "/roles/" + r.id }
func (r roleAt) where() string { return resourceWords(r.parent) }

// roleOf is the custom role a role's full name names (false for a predefined role).
func roleOf(name string) (roleAt, bool) {
	parent, id, ok := strings.Cut(name, "/roles/")
	if !ok || strings.HasPrefix(name, "roles/") {
		return roleAt{}, false
	}
	return roleAt{id, parent}, true
}

// resourceWords is a resource as the lines say it: project <id>, folder <n>, organization <n>.
func resourceWords(resource string) string {
	kind, id, _ := strings.Cut(resource, "/")
	return strings.TrimSuffix(kind, "s") + " " + id
}

// need is the steps of a command whose permissions are tested at a resource.
type need struct {
	resource string
	steps    []string
}

// link is one connect or disconnect: the install's run for its helpers (say, settle, get, call, wait, end, the policy of the
// host project as last read), and the connection's own state.
type link struct {
	*run
	c       ConnectRequest
	command string    // connect or disconnect
	jsonOut io.Writer // stdout: under --json, the JSON alone (the words go to stderr)
	m       ManagerInfo
	host    string
	rec     *fleet.GoogleConnection // the manager's record as read

	scopes        []string
	orgOf         map[string]string    // a folder scope's organization, organizations/<n>
	projects      []string             // the projects at the scopes, sorted
	routers       []fleet.GoogleRouter // the routers at the scopes, by project and region
	tagValue      map[string]string    // a router project's tag value <project>/dormeto/managed, tagValues/<n>
	unseen        map[string][]string  // the regions Cloud Run did not list, by project
	unseenRouters []fleet.GoogleRouter // connect: the record's routers in those regions: kept as they are
	warnings      []string             // what the scan warned about, in --json's warnings
	gone          []string             // connect: the projects of the record's routers that hold none any more
	former        []string             // disconnect: the projects at the scopes holding a dormetoCommand but no router of the record
	removing      map[string]bool      // the grants this run takes out, by place and grantKey

	tested      map[string][]string // the steps tested, by resource
	testedOrder []string
	accounts    map[string]bool    // the service accounts there, by email
	wanted      []binding          // every grant of the connection
	roles       []roleAt           // every custom role of the connection
	hostStart   *Policy            // the host project's policy as the plan read it
	googleAdded []string           // the grants Google added during this run, as the record lists them
	google      map[string]bool    // their keys (grantKey on the place)
	recordLine  string             // the plan's line of the record, "" when the record is written for the last run's fields only
	early       string             // connect: what the record written before the first step holds, "" when none was
	unknownOrgs []string           // disconnect: the folder scopes whose organization is not known
	left        map[string]planned // disconnect: each policy read, as its removals leave it, by place
	kept        []string           // disconnect: the plan's lines of what it keeps
}

// planned is a policy the disconnect's plan read: where it is, and as the removals leave it.
type planned struct {
	rp    ResourcePolicy
	after *Policy
}

func newLink(ctx context.Context, d Deps, r ConnectRequest, command string) *link {
	out := d.Out
	if r.JSON {
		d.Out = d.Err
	}
	x := &link{run: newRun(ctx, d, Request{Project: r.Project, Configuration: r.Configuration, Yes: r.Yes, DryRun: r.DryRun, JSON: r.JSON}),
		c: r, command: command, jsonOut: out, host: r.Project, orgOf: map[string]string{}, unseen: map[string][]string{}, tagValue: map[string]string{},
		tested: map[string][]string{}, accounts: map[string]bool{}, google: map[string]bool{}, left: map[string]planned{},
		removing: map[string]bool{}}
	return x
}

// Connect is `dormeto gcp connect`: 0 when connected or up to date (or the plan only, with DryRun, or declined), 1 when the
// manager or a check refuses or Google does, 2 for a bad project or scope or no --yes without a terminal, 130 when
// interrupted.
func Connect(ctx context.Context, d Deps, r ConnectRequest) int {
	x := newLink(ctx, d, r, "connect")
	return x.end(x.connect())
}

func (x *link) connect() error {
	if err := x.validate(); err != nil {
		return err
	}
	if err := x.readManager(); err != nil {
		return err
	}
	if x.rec != nil && x.rec.Project != x.host {
		return &stop{1, fmt.Sprintf("manager %s is connected to Google Cloud through project %s: disconnect it first (%s)\nNothing was changed.",
			x.m.Env, x.rec.Project, x.commandLine("disconnect", x.rec.Project))}
	}
	if err := x.recordNames(); err != nil {
		return err
	}
	x.useScopes()
	if err := x.checkHostAndScopes(); err != nil {
		return err
	}
	on, err := enabledServices(x.ctx, x.d.Google, x.number)
	if err != nil {
		return err
	}
	for _, api := range access.Load().Manager.APIs {
		if !slices.Contains(on, api) {
			x.turningOn = append(x.turningOn, api)
		}
	}
	if err := x.scan(); err != nil {
		return err
	}
	if err := x.checkRest(); err != nil {
		return err
	}
	if err := x.readTagValues(); err != nil {
		return err
	}
	if err := x.planConnect(); err != nil {
		return err
	}
	want := x.record()
	x.recordLine = x.recordChange(want)
	if len(x.steps) == 0 && x.recordLine == "" {
		x.say("The connection of manager %s to Google Cloud through project %s is up to date: %s, %s.", x.m.Env, x.host,
			count(len(x.routers), "router"), count(len(x.scopes), "scope"))
		return x.printJSON(map[string]any{"connection": x.rec})
	}
	if x.recordLine == "" {
		x.recordLine = "~ record of the connection in manager " + x.m.Env + ": the last connect's time, account and CLI"
	}
	x.printConnectPlan()
	if done, err := x.confirm("Connect manager " + x.m.Env + " to Google Cloud through project " + x.host + "?"); done || err != nil {
		return err
	}
	if err := x.recordBefore(); err != nil {
		return err
	}
	return x.applyConnect()
}

// recordBefore writes the record before the first step when there is none, or when this run adds scopes: the union of the
// scopes, with the routers already recorded (none for a first connect: the manager would call a new router before its
// grants). A run that stops partway leaves what it made known to disconnect, which sweeps the recorded scopes. early says
// what the record then holds.
func (x *link) recordBefore() error {
	first := x.rec == nil
	if !first && !slices.ContainsFunc(x.scopes, func(s string) bool { return !slices.Contains(x.rec.Scopes, s) }) {
		return nil
	}
	rec := x.record()
	rec.Routers = []fleet.GoogleRouter{}
	if !first {
		rec.Routers = append(rec.Routers, x.rec.Routers...)
	}
	if _, err := x.c.Manager.PutGoogleConnection(x.ctx, rec); err != nil {
		return &stop{1, fmt.Sprintf("the manager's record is not written: %s\nNothing was changed.", err)}
	}
	if first {
		x.early = "the connection without its routers"
		x.say("  ✓ record: wrote the connection in manager %s before any change, its routers to come at the end", x.m.Env)
		return nil
	}
	x.early = "the connection's new scopes without their routers"
	x.say("  ✓ record: wrote the connection's new scopes in manager %s before any change, its new routers to come at the end", x.m.Env)
	return nil
}

func (x *link) validate() error {
	if !projectIDPattern.MatchString(x.host) {
		return &stop{2, fmt.Sprintf("--project: %q is not a Google Cloud project ID such as dormeto-dev", x.host)}
	}
	for _, s := range x.c.Scopes {
		if !scopePattern.MatchString(s) {
			return &stop{2, fmt.Sprintf("--scope: %q is not organizations/<number>, folders/<number> or projects/<project ID>", s)}
		}
	}
	return nil
}

// readManager reads what the manager says of itself, refuses one before 0.24.0 (a dev build of 0.24.0 counts), then reads
// its record.
func (x *link) readManager() error {
	m, err := x.c.Manager.About(x.ctx)
	if err != nil {
		return fmt.Errorf("the manager is not read: %w", err)
	}
	if !install.IsVersion(m.Version) || !install.AtLeast(m.Version, managerMinimum) {
		return &stop{1, fmt.Sprintf("manager %s can't connect to Google Cloud: it needs %s or later (dormeto aws manager update)\nNothing was changed.",
			cmp.Or(m.Version, "(version unknown)"), managerMinimum)}
	}
	if !awsAccountPattern.MatchString(m.Account) {
		return fmt.Errorf("the manager's AWS account is not read: %q is not 12 digits", m.Account)
	}
	m.Env = cmp.Or(m.Env, x.c.Env)
	if !envPattern.MatchString(m.Env) {
		return fmt.Errorf("the manager's environment is not read: %q", m.Env)
	}
	x.m = m
	x.say("Manager %s, environment %s, AWS account %s (%s).", m.Version, m.Env, m.Account, m.URL)
	rec, err := x.c.Manager.GoogleConnection(x.ctx)
	if err != nil {
		return fmt.Errorf("the manager's connection record is not read: %w", err)
	}
	x.rec = rec
	return nil
}

// recordNames refuses a record whose pool, provider or accounts are not this environment's: acting under the names of
// another --env would find nothing, and a disconnect would then clear the record of the real connection. With them equal,
// every name a run acts on is the record's. The refusal names every field that differs, and those only.
func (x *link) recordNames() error {
	if x.rec == nil {
		return nil
	}
	pool, provider, discovery, command := x.poolID(), x.providerID(), x.email("discovery"), x.email("command")
	r := x.rec
	var has, want []string
	if r.Pool != pool {
		has, want = append(has, "pool "+r.Pool), append(want, pool)
	}
	if r.Provider != provider {
		has, want = append(has, "provider "+r.Provider), append(want, provider)
	}
	if r.ServiceAccounts != (fleet.GoogleServiceAccounts{Discovery: discovery, Command: command}) {
		has = append(has, fmt.Sprintf("the accounts %s and %s", r.ServiceAccounts.Discovery, r.ServiceAccounts.Command))
		want = append(want, discovery, command)
	}
	if len(has) == 0 {
		return nil
	}
	return &stop{2, fmt.Sprintf("--env: the manager's connection names %s, not those of environment %s (%s)\nNothing was changed.",
		andList(has), x.m.Env, andList(want))}
}

// andList is "a", "a and b" or "a, b and c".
func andList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// commandLine is a dormeto gcp command for this manager, as the runs tell to run it: the manager found as this run found it.
func (x *link) commandLine(command, project string) string {
	parts := []string{"dormeto gcp", command, "--project", project}
	if x.c.Profile != "" {
		parts = append(parts, "--profile", x.c.Profile)
	}
	parts = append(parts, "--env", x.m.Env)
	if x.c.ManagerRegion != "" {
		parts = append(parts, "--manager-region", x.c.ManagerRegion)
	}
	if x.c.APIURL != "" {
		parts = append(parts, "--api-url", x.c.APIURL)
	}
	if x.c.Configuration != "" {
		parts = append(parts, "--gcloud-configuration", x.c.Configuration)
	}
	return strings.Join(parts, " ")
}

// useScopes are the record's scopes and those given (a scope is removed by a disconnect only); none: the host project.
func (x *link) useScopes() {
	var scopes []string
	if x.rec != nil {
		scopes = slices.Clone(x.rec.Scopes)
	}
	for _, s := range x.c.Scopes {
		if !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	if len(scopes) == 0 {
		scopes = []string{"projects/" + x.host}
	}
	x.scopes = scopes
}

// The names, from the manifest's manager section.

func (x *link) fill(s string) string {
	return strings.NewReplacer("{env}", x.m.Env, "{account}", x.m.Account, "{hostNumber}", x.number, "{project}", x.host).Replace(s)
}

func (x *link) email(account string) string {
	return x.fill(access.Load().Manager.ServiceAccounts[account]) + "@" + x.host + ".iam.gserviceaccount.com"
}

func (x *link) member(account string) string { return "serviceAccount:" + x.email(account) }

func (x *link) poolID() string     { return x.fill(access.Load().Manager.Federation.Pool) }
func (x *link) providerID() string { return x.fill(access.Load().Manager.Federation.Provider) }

func (x *link) poolsURL() string {
	return iamV1 + "projects/" + esc(x.host) + "/locations/global/workloadIdentityPools"
}

func (x *link) poolURL() string     { return x.poolsURL() + "/" + esc(x.poolID()) }
func (x *link) providerURL() string { return x.poolURL() + "/providers/" + esc(x.providerID()) }

func (x *link) serviceAccountURL(email string) string {
	return iamV1 + "projects/" + esc(x.host) + "/serviceAccounts/" + esc(email)
}

func (x *link) condition() string { return x.fill(access.Load().Manager.Federation.Condition) }

func (x *link) mapping() map[string]string {
	out := map[string]string{}
	for _, a := range access.Load().Manager.Federation.Mapping {
		out[a.Name] = x.fill(a.Expression)
	}
	return out
}

func (x *link) mappingWords() string {
	var out []string
	for _, a := range access.Load().Manager.Federation.Mapping {
		out = append(out, a.Name+"="+x.fill(a.Expression))
	}
	return strings.Join(out, ", ")
}

// principal is the principal set of one of the manager's AWS roles in the pool.
func (x *link) principal(awsRole string) string {
	return x.fill(strings.ReplaceAll(access.Load().Manager.Impersonation.PrincipalSet, "{awsRole}", x.fill(awsRole)))
}

// accountKinds are the manager's two accounts, discovery first.
func accountKinds() []string { return []string{"discovery", "command"} }

// The places.

func (x *link) scopePlace(scope string) place {
	kind, id, _ := strings.Cut(scope, "/")
	switch kind {
	case "projects":
		return place{what: "project " + id, rp: ProjectPolicy(id), step: "projectBindings"}
	case "folders":
		return place{what: "folder " + id, rp: newResourcePolicy(crmV3+scope, false), step: "folderBindings"}
	}
	return place{what: "organization " + id, rp: newResourcePolicy(crmV3+scope, false), step: "organizationBindings"}
}

func (x *link) controllerPlace(r fleet.GoogleRouter) place {
	service := "dmtr-" + r.Region + "-controller"
	return place{what: "controller " + service + " of project " + r.Project, rp: RunPolicy(r.Project, r.Region, service), step: "controller"}
}

func (x *link) tagPlace(project string) place {
	return place{what: "tag value " + project + "/dormeto/managed", rp: newResourcePolicy(crmV3+x.tagValue[project], false), step: "tag"}
}

func (x *link) accountPlace(email string) place {
	return place{what: "service account " + email, rp: ServiceAccountPolicy(x.host, email), step: "impersonation", fresh: !x.accounts[email]}
}

// discoveryParent is where a scope's dormetoDiscovery is defined: a project scope's project, a folder's organization, an
// organization.
func (x *link) discoveryParent(scope string) string {
	if strings.HasPrefix(scope, "folders/") {
		return x.orgOf[scope]
	}
	return scope
}

// routerProjects are the projects of the routers, in order.
func (x *link) routerProjects() []string {
	var out []string
	for _, r := range x.routers {
		if !slices.Contains(out, r.Project) {
			out = append(out, r.Project)
		}
	}
	return out
}

// grants are the manifest's grants of the scopes and the router projects, their roles' names where they are defined.
func (x *link) grants() []binding {
	mg := access.Load().Manager
	role := func(id, parent string) string {
		if _, custom := mg.Roles[id]; custom {
			return roleAt{id, parent}.name()
		}
		return id
	}
	var out []binding
	for _, g := range mg.Grants.Scopes {
		for _, s := range x.scopes {
			if parent := x.discoveryParent(s); parent != "" {
				out = append(out, binding{x.scopePlace(s), Grant{Role: role(g.Role, parent), Member: x.member(g.Account)}})
			}
		}
	}
	for _, g := range mg.Grants.RouterProjects {
		switch g.On {
		case "controller":
			for _, r := range x.routers {
				out = append(out, binding{x.controllerPlace(r), Grant{Role: role(g.Role, "projects/"+r.Project), Member: x.member(g.Account)}})
			}
		case "tagValue":
			for _, p := range x.routerProjects() {
				out = append(out, binding{x.tagPlace(p), Grant{Role: role(g.Role, "projects/"+p), Member: x.member(g.Account)}})
			}
		case "project":
			for _, p := range x.routerProjects() {
				out = append(out, binding{x.scopePlace("projects/" + p), Grant{Role: role(g.Role, "projects/"+p), Member: x.member(g.Account)}})
			}
		}
	}
	return out
}

// customRoles are the custom roles the grants name, in their order.
func customRoles(bs []binding) []roleAt {
	var out []roleAt
	for _, b := range bs {
		if r, ok := roleOf(b.Role); ok && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// The checks.

// hostSteps are the steps of the command that act on the host project; scopeSteps those that act on a scope.
func (x *link) hostSteps() []string {
	if x.command == "disconnect" {
		// It sweeps the host's bindings and deletes a dormetoCommand there.
		return []string{"read", "projectBindings", "roles", "serviceAccounts", "federation"}
	}
	return []string{"read", "apis", "federation", "serviceAccounts"}
}

func scopeSteps(scope string) []string {
	switch kind, _, _ := strings.Cut(scope, "/"); kind {
	case "projects":
		return []string{"read", "roles", "projectBindings"}
	case "folders":
		return []string{"read", "folderBindings"}
	}
	return []string{"read", "roles", "organizationBindings"}
}

// checkHostAndScopes reads the host project and tests the permissions at the host and at each scope.
func (x *link) checkHostAndScopes() error {
	account, err := x.d.Google.Tokens.Account(x.ctx)
	if err != nil {
		return err
	}
	x.account = account
	var p projectBody
	switch err := x.d.Google.Do(x.ctx, http.MethodGet, crm+url.PathEscape(x.host), nil, &p); {
	case err != nil && denied(err):
		return &stop{1, fmt.Sprintf("Project %s is not visible to %s: no role in it\nNothing was changed.", x.host, account)}
	case err != nil:
		return err
	case p.State != "" && p.State != "ACTIVE":
		return &stop{1, fmt.Sprintf("project %s is %s\nNothing was changed.", x.host, p.State)}
	}
	x.number, x.parent = strings.TrimPrefix(p.Name, "projects/"), p.Parent
	needs := []need{{"projects/" + x.host, x.hostSteps()}}
	for _, s := range x.scopes {
		needs = append(needs, need{s, scopeSteps(s)})
	}
	return x.test(needs)
}

// checkRest tests, once the routers are known, the organizations of the folder scopes for the roles and the router projects
// for their grants and role, then says what was checked.
func (x *link) checkRest() error {
	var needs []need
	for _, s := range x.scopes {
		if org := x.orgOf[s]; strings.HasPrefix(s, "folders/") && org != "" {
			needs = append(needs, need{org, []string{"roles"}})
		}
	}
	for _, p := range x.routerProjects() {
		needs = append(needs, need{"projects/" + p, []string{"controller", "tag", "roles", "projectBindings"}})
	}
	for _, p := range x.gone {
		needs = append(needs, need{"projects/" + p, []string{"controller", "tag", "projectBindings"}})
	}
	for _, p := range x.former {
		needs = append(needs, need{"projects/" + p, []string{"controller", "tag", "roles", "projectBindings"}})
	}
	if err := x.test(needs); err != nil {
		return err
	}
	var where []string
	for _, r := range x.testedOrder {
		w := resourceWords(r)
		if r == "projects/"+x.host {
			w += " (host)"
		}
		where = append(where, w)
	}
	x.say("Checked: %s holds every permission dormeto gcp %s needs in %s.", x.account, x.command, strings.Join(where, ", "))
	return nil
}

// test asks, at each resource, the permissions of the steps not tested there yet: every one held, or the missing ones named
// and nothing done. A resource the caller does not see holds none.
func (x *link) test(needs []need) error {
	var refused []string
	for _, n := range needs {
		var steps []string
		for _, s := range n.steps {
			if !slices.Contains(x.tested[n.resource], s) {
				steps = append(steps, s)
			}
		}
		if len(steps) == 0 {
			continue
		}
		if _, seen := x.tested[n.resource]; !seen {
			x.testedOrder = append(x.testedOrder, n.resource)
		}
		x.tested[n.resource] = append(x.tested[n.resource], steps...)
		perms := access.StepNeeds(x.command, steps...)
		held, err := testPermissionsOn(x.ctx, x.d.Google, n.resource, perms)
		if err != nil && !denied(err) {
			return err
		}
		var missing []string
		for _, p := range perms {
			if !slices.Contains(held, p) {
				missing = append(missing, p)
			}
		}
		if len(missing) > 0 {
			refused = append(refused, fmt.Sprintf("  ✗ %s in %s: missing %s", x.command, resourceWords(n.resource), strings.Join(missing, ", ")))
		}
	}
	if len(refused) > 0 {
		return &stop{1, strings.Join(refused, "\n") + "\nNothing was changed."}
	}
	return nil
}

// The plan.

func (x *link) planConnect() error {
	p, err := x.readPolicy(ProjectPolicy(x.host))
	if err != nil {
		return err
	}
	x.policy, x.hostStart = p, p
	grants := x.grants()
	x.roles = customRoles(grants)
	byStep := map[string][]binding{}
	for _, g := range grants {
		byStep[g.step] = append(byStep[g.step], g)
	}
	planners := []func() (*step, error){x.planManagerAPIs, x.planManagerRoles, x.planFederation, x.planAccounts,
		func() (*step, error) { return x.planGrants("impersonation", x.impersonation()) }}
	for _, name := range []string{"projectBindings", "folderBindings", "organizationBindings", "controller", "tag"} {
		planners = append(planners, func() (*step, error) { return x.planGrants(name, byStep[name]) })
	}
	planners = append(planners, x.planGoneRouterProjects)
	for _, plan := range planners {
		s, err := plan()
		if err != nil {
			return err
		}
		if s != nil {
			x.steps = append(x.steps, *s)
		}
	}
	return nil
}

// planGoneRouterProjects takes the command account's bindings out of the projects whose routers are all gone (their
// project's policy, their tag value's, and their recorded routers' controllers: a project that left the scopes keeps them);
// their role dormetoCommand stays, granting nothing: connect's permissions don't delete a role.
func (x *link) planGoneRouterProjects() (*step, error) {
	if len(x.gone) == 0 {
		return nil, nil
	}
	var places []place
	for _, p := range x.gone {
		note := "no router left"
		if !slices.Contains(x.projects, p) {
			note = "not at the scopes any more"
		}
		at := []place{x.scopePlace("projects/" + p)}
		if x.tagValue[p] != "" {
			at = append(at, x.tagPlace(p))
		}
		for _, r := range x.rec.Routers {
			if r.Project == p {
				at = append(at, x.controllerPlace(r))
			}
		}
		for _, pl := range at {
			pl.note = note
			places = append(places, pl)
		}
	}
	s, err := x.planRemovals("routersGone", places, []string{x.member("command")}, "")
	if err != nil {
		return nil, err
	}
	for _, p := range x.gone {
		at := roleAt{"dormetoCommand", "projects/" + p}
		var got googleRole
		if found, err := x.get(iamAPI, at.url(), &got); err != nil {
			return nil, err
		} else if found && !got.Deleted && x.stillGranted(at.name()) == "" {
			x.kept = append(x.kept, fmt.Sprintf("= role %s in %s: left in place, granted to no one", at.id, at.where()))
		}
	}
	return s, nil
}

func (x *link) printConnectPlan() {
	x.say("Plan for manager %s's connection through project %s:", x.m.Env, x.host)
	for _, l := range x.planLines() {
		x.say("  %s", l)
	}
	if len(x.steps) > 0 && x.steps[0].name == "apis" {
		x.say("Turning APIs on can make Google add bindings of its own: they are shown after that step.")
	}
}

// planLines are the plan's lines, the record's last.
func (x *link) planLines() []string {
	var out []string
	for _, s := range x.steps {
		out = append(out, s.lines...)
	}
	out = append(out, x.kept...)
	if x.recordLine != "" {
		out = append(out, x.recordLine)
	}
	return out
}

// confirm prints the dry run's JSON and ends it, or asks (unless --yes): done when the run ends here. AWS's lines: --yes without
// a terminal, "not confirmed: nothing sent" when declined.
func (x *link) confirm(question string) (bool, error) {
	// The released shape of a dry run and of a declined question, what it addresses the host project, its plan beside.
	if x.r.DryRun {
		return true, x.printJSON(map[string]any{"dryRun": true, "targets": []string{x.host}, "plan": x.planLines()})
	}
	if x.r.Yes {
		return false, nil
	}
	if !x.d.Terminal {
		return true, needsYes("connection")
	}
	ok, err := install.Prompted(x.ctx, x.d.Out, func() (bool, error) { return x.d.Prompter.Confirm(question) })
	if err != nil {
		return true, err
	}
	if !ok {
		if err := x.printJSON(map[string]any{"confirmed": false, "targets": []string{x.host}, "plan": x.planLines()}); err != nil {
			return true, err
		}
		return true, &stop{0, notConfirmed}
	}
	return false, nil
}

// printJSON prints v on stdout under --json.
func (x *link) printJSON(v any) error {
	if !x.c.JSON {
		return nil
	}
	if m, ok := v.(map[string]any); ok && len(x.warnings) > 0 {
		v = maps.Clone(m)
		v.(map[string]any)["warnings"] = x.warnings
	}
	return writeJSON(x.jsonOut, v)
}

// planManagerAPIs turns on the manager's APIs that are off in the host project, then reads its policy again: what Google
// added is shown and recorded.
func (x *link) planManagerAPIs() (*step, error) {
	if len(x.turningOn) == 0 {
		return nil, nil
	}
	var lines []string
	for _, api := range x.turningOn {
		lines = append(lines, "+ API "+api)
	}
	return &step{name: "apis", lines: lines, apply: func() (string, error) {
		for batch := range slices.Chunk(x.turningOn, enableBatch) {
			var op longOperation
			if err := x.call(http.MethodPost, serviceUsage+esc(x.number)+"/services:batchEnable", map[string]any{"serviceIds": batch}, &op); err != nil {
				return "", err
			}
			x.turnedOn = true
			if err := x.wait("https://serviceusage.googleapis.com/v1/", op, apisTimeout); err != nil {
				return "", err
			}
		}
		after, err := x.readPolicy(ProjectPolicy(x.host))
		if err != nil {
			return "", err
		}
		added, _ := Diff(x.policy, after)
		for _, g := range added {
			x.saw(g, "project "+x.host, "apis")
		}
		x.policy = after
		return "turned on " + strings.Join(x.turningOn, ", "), nil
	}}, nil
}

// saw notes a grant Google added on a place after a step: an IAM line, and the record's googleAdded.
func (x *link) saw(g Grant, what, step string) {
	x.iam = append(x.iam, fmt.Sprintf("IAM + %s (Google, after %s)", describeOn(g, what), step))
	key := what + "\x00" + grantKey(g)
	if !x.google[key] {
		x.google[key] = true
		x.googleAdded = append(x.googleAdded, describeOn(g, what)+" (after "+step+")")
	}
}

func describeOn(g Grant, what string) string { return g.Role + " to " + g.Member + " on " + what }

// wantManagerRole is a manager role as connect writes it where it is defined: in an organization with its organization
// permissions too.
func (x *link) wantManagerRole(at roleAt) access.Role {
	r := access.Load().Manager.Roles[at.id]
	perms := slices.Clone(r.Permissions)
	if strings.HasPrefix(at.parent, "organizations/") {
		perms = append(perms, r.OrganizationPermissions...)
	}
	return access.Role{Title: r.Title, Description: roleDescription(x.d.Version, r.Description), Permissions: perms}
}

// planManagerRoles creates, undeletes then updates, or updates each custom role where it is defined, as the roles step of
// the install (roles.go).
func (x *link) planManagerRoles() (*step, error) {
	type change struct {
		at           roleAt
		action, etag string
		want         access.Role
	}
	var changes []change
	var lines []string
	for _, at := range x.roles {
		want := x.wantManagerRole(at)
		var got googleRole
		found, err := x.get(iamAPI, at.url(), &got)
		switch {
		case err != nil:
			return nil, err
		case !found:
			changes = append(changes, change{at, "create", "", want})
			lines = append(lines, fmt.Sprintf("+ role %s \"%s\" (%d permissions) in %s", at.id, want.Title, len(want.Permissions), at.where()))
		case got.Deleted:
			changes = append(changes, change{at, "undelete", got.Etag, want})
			lines = append(lines, fmt.Sprintf("↺ role %s in %s: undelete, then update", at.id, at.where()))
		default:
			if d := roleDifferences(got, want); len(d) > 0 {
				changes = append(changes, change{at, "update", "", want})
				lines = append(lines, fmt.Sprintf("~ role %s in %s: %s", at.id, at.where(), strings.Join(d, ", ")))
			}
		}
	}
	if len(changes) == 0 {
		return nil, nil
	}
	update := func(c change) error {
		in := map[string]any{"title": c.want.Title, "description": c.want.Description, "includedPermissions": c.want.Permissions, "stage": roleStage}
		return x.call(http.MethodPatch, c.at.url()+"?updateMask="+roleFields, in, nil)
	}
	return &step{name: "roles", lines: lines, apply: func() (string, error) {
		var done []string
		for _, c := range changes {
			switch c.action {
			case "create":
				in := map[string]any{"roleId": c.at.id, "role": map[string]any{"title": c.want.Title, "description": c.want.Description,
					"includedPermissions": c.want.Permissions, "stage": roleStage}}
				if err := x.call(http.MethodPost, iamV1+c.at.parent+"/roles", in, nil); err != nil {
					if gcp.IsPrecondition(err) || gcp.IsAlreadyExists(err) {
						return "", x.blocked(c.at)
					}
					return "", err
				}
				x.made(c.at.url())
				done = append(done, "created "+c.at.id+" in "+c.at.where())
			case "undelete":
				if err := x.call(http.MethodPost, c.at.url()+":undelete", map[string]any{"etag": c.etag}, nil); err != nil {
					if gcp.IsPrecondition(err) || invalid(err) {
						return "", x.blocked(c.at)
					}
					return "", err
				}
				if err := update(c); err != nil {
					return "", err
				}
				done = append(done, "undeleted and updated "+c.at.id+" in "+c.at.where())
			case "update":
				if err := update(c); err != nil {
					return "", err
				}
				done = append(done, "updated "+c.at.id+" in "+c.at.where())
			}
		}
		return strings.Join(done, ", "), nil
	}}, nil
}

// blocked is the refusal of a role ID that Google holds and will not undelete: deleted more than 7 days ago, it is freed at
// the latest 44 days after its deletion, a date the manager's audit gives when its disconnect deleted it.
func (x *link) blocked(at roleAt) error {
	msg := fmt.Sprintf("Google blocks the ID of role %s in %s: it was deleted more than 7 days ago and can't be undeleted; ", at.id, at.where())
	when, err := x.c.Manager.Disconnected(x.ctx, x.host)
	if err != nil || when.IsZero() {
		return errors.New(msg + fmt.Sprintf("Google frees it at the latest %d days after its deletion", roleIDBlockedDays))
	}
	return errors.New(msg + fmt.Sprintf("Google frees it by %s (%d days after the manager's disconnect of %s)",
		when.UTC().AddDate(0, 0, roleIDBlockedDays).Format(time.DateOnly), roleIDBlockedDays, when.UTC().Format(time.DateOnly)))
}

// googlePool and googleProvider are a workload identity pool and its AWS provider as IAM answers them.
type googlePool struct {
	State    string `json:"state"`
	Disabled bool   `json:"disabled"`
}

type googleProvider struct {
	State              string            `json:"state"`
	Disabled           bool              `json:"disabled"`
	AttributeMapping   map[string]string `json:"attributeMapping"`
	AttributeCondition string            `json:"attributeCondition"`
	AWS                *struct {
		AccountID string `json:"accountId"`
	} `json:"aws"`
}

// providerDifferences are what an update of the provider changes.
func (x *link) providerDifferences(p googleProvider) []string {
	var out []string
	if p.AWS == nil || p.AWS.AccountID != x.m.Account {
		out = append(out, "AWS account "+x.m.Account)
	}
	if p.AttributeCondition != x.condition() {
		out = append(out, "condition "+x.condition())
	}
	if !maps.Equal(p.AttributeMapping, x.mapping()) {
		out = append(out, "mapping "+x.mappingWords())
	}
	if p.Disabled {
		out = append(out, "enabled")
	}
	return out
}

func (x *link) providerBody() map[string]any {
	return map[string]any{
		"displayName": "AWS " + x.m.Account,
		"description": fmt.Sprintf("The Dormeto manager %s's AWS account %s, its roles %s: set up by dormeto gcp connect", x.m.Env, x.m.Account, x.fill("dmt-{env}-manager-*")),
		"aws":         map[string]string{"accountId": x.m.Account}, "attributeMapping": x.mapping(), "attributeCondition": x.condition(), "disabled": false,
	}
}

// planFederation creates, undeletes or enables the pool, and creates, undeletes or updates its provider.
func (x *link) planFederation() (*step, error) {
	var pool googlePool
	poolFound, err := x.get(iamAPI, x.poolURL(), &pool)
	if err != nil {
		return nil, err
	}
	var lines []string
	var actions []func() (string, error)
	pooled := "workload identity pool " + x.poolID()
	switch {
	case !poolFound:
		lines = append(lines, "+ "+pooled)
		actions = append(actions, x.createPool)
	case pool.State == "DELETED":
		lines = append(lines, "↺ "+pooled+": undelete")
		actions = append(actions, x.operation(http.MethodPost, x.poolURL()+":undelete", map[string]any{}, "undeleted pool "+x.poolID()))
	case pool.Disabled:
		lines = append(lines, "~ "+pooled+": enable")
		actions = append(actions, x.operation(http.MethodPatch, x.poolURL()+"?updateMask=disabled", map[string]any{"disabled": false}, "enabled pool "+x.poolID()))
	}
	var provider googleProvider
	providerFound := false
	if poolFound {
		if providerFound, err = x.get(iamAPI, x.providerURL(), &provider); err != nil {
			return nil, err
		}
	}
	named := "provider " + x.providerID() + " of pool " + x.poolID()
	diffs := x.providerDifferences(provider)
	update := x.operation(http.MethodPatch, x.providerURL()+"?updateMask=aws,attributeMapping,attributeCondition,disabled", x.providerBody(), "updated provider "+x.providerID())
	undelete := x.operation(http.MethodPost, x.providerURL()+":undelete", map[string]any{}, "undeleted provider "+x.providerID())
	switch {
	case !providerFound:
		lines = append(lines, fmt.Sprintf("+ %s: AWS account %s, condition %s, mapping %s", named, x.m.Account, x.condition(), x.mappingWords()))
		actions = append(actions, func() (string, error) {
			done, err := x.operation(http.MethodPost, x.poolURL()+"/providers?workloadIdentityPoolProviderId="+url.QueryEscape(x.providerID()), x.providerBody(),
				"created provider "+x.providerID())()
			// Deleted, it is Google's still: undeleted, then updated.
			if gcp.IsAlreadyExists(err) {
				if _, err := undelete(); err != nil {
					return "", err
				}
				return update()
			}
			if err == nil {
				x.made(x.providerURL())
			}
			return done, err
		})
	case provider.State == "DELETED":
		line := "↺ " + named + ": undelete"
		if len(diffs) > 0 {
			line += ", then update: " + strings.Join(diffs, ", ")
		}
		lines = append(lines, line)
		actions = append(actions, undelete)
		if len(diffs) > 0 {
			actions = append(actions, update)
		}
	case len(diffs) > 0:
		lines = append(lines, "~ "+named+": "+strings.Join(diffs, ", "))
		actions = append(actions, update)
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return &step{name: "federation", lines: lines, apply: func() (string, error) {
		var done []string
		for _, a := range actions {
			d, err := a()
			if err != nil {
				return "", err
			}
			done = append(done, d)
		}
		return strings.Join(done, ", "), nil
	}}, nil
}

func (x *link) createPool() (string, error) {
	in := map[string]any{"displayName": "Dormeto manager " + x.m.Env,
		"description": fmt.Sprintf("The Dormeto manager %s's AWS roles (account %s): set up by dormeto gcp connect", x.m.Env, x.m.Account)}
	done, err := x.operation(http.MethodPost, x.poolsURL()+"?workloadIdentityPoolId="+url.QueryEscape(x.poolID()), in, "created pool "+x.poolID())()
	if err == nil {
		x.made(x.poolURL())
	}
	return done, err
}

// operation is a call answering a long-running operation, awaited.
func (x *link) operation(method, u string, in any, done string) func() (string, error) {
	return func() (string, error) {
		var op longOperation
		if err := x.call(method, u, in, &op); err != nil {
			return "", err
		}
		if err := x.wait(iamV1, op, federationTimeout); err != nil {
			return "", err
		}
		return done, nil
	}
}

// planAccounts creates the manager's service accounts that are missing; a disabled one stops the run, as the install's.
func (x *link) planAccounts() (*step, error) {
	var lines, missing []string
	for _, kind := range accountKinds() {
		email := x.email(kind)
		var got struct {
			Disabled bool `json:"disabled"`
		}
		found, err := x.get(iamAPI, x.serviceAccountURL(email), &got)
		if err != nil {
			return nil, err
		}
		if found && got.Disabled {
			enable := "gcloud iam service-accounts enable " + email
			if x.r.Configuration != "" {
				enable += " --configuration " + x.r.Configuration
			}
			return nil, &stop{1, fmt.Sprintf("The service account %s is disabled: enable it (%s), then run the same command again.\nNothing was changed.", email, enable)}
		}
		if found {
			x.accounts[email] = true
			continue
		}
		lines = append(lines, "+ service account "+email)
		missing = append(missing, kind)
	}
	if len(missing) == 0 {
		return nil, nil
	}
	return &step{name: "serviceAccounts", lines: lines, apply: func() (string, error) {
		var done []string
		for _, kind := range missing {
			var roles []string
			for _, role := range access.Load().Manager.Impersonation.Roles[kind] {
				roles = append(roles, x.fill(role))
			}
			in := map[string]any{"accountId": x.fill(access.Load().Manager.ServiceAccounts[kind]), "serviceAccount": map[string]any{
				"displayName": "Dormeto manager " + x.m.Env + " " + kind,
				"description": fmt.Sprintf("The Dormeto manager %s's %s account, impersonated by its roles %s: created by dormeto gcp connect", x.m.Env, kind, strings.Join(roles, ", ")),
			}}
			if err := x.call(http.MethodPost, iamV1+"projects/"+esc(x.host)+"/serviceAccounts", in, nil); err != nil {
				return "", err
			}
			x.accounts[x.email(kind)] = true
			x.made(x.serviceAccountURL(x.email(kind)))
			done = append(done, x.email(kind))
		}
		return "created " + strings.Join(done, ", "), nil
	}}, nil
}

// impersonation are the grants on each account to the principal set of each manager role that acts as it.
func (x *link) impersonation() []binding {
	imp := access.Load().Manager.Impersonation
	var out []binding
	for _, kind := range accountKinds() {
		for _, role := range imp.Roles[kind] {
			out = append(out, binding{x.accountPlace(x.email(kind)), Grant{Role: imp.Role, Member: x.principal(role)}})
		}
	}
	return out
}

// planGrants adds the grants a place's policy lacks, each place's at once with its etag (Update); bindings are only added.
func (x *link) planGrants(name string, bs []binding) (*step, error) {
	type group struct {
		place   place
		missing []Grant
	}
	var groups []*group
	byWhat := map[string]*group{}
	for _, b := range bs {
		x.wanted = append(x.wanted, b)
		if byWhat[b.what] == nil {
			byWhat[b.what] = &group{place: b.place}
			groups = append(groups, byWhat[b.what])
		}
	}
	var lines []string
	for _, g := range groups {
		var p *Policy
		if !g.place.fresh {
			var err error
			if p, err = x.readPolicy(g.place.rp); err != nil {
				return nil, fmt.Errorf("the policy of %s: %w", g.place.what, err)
			}
		}
		for _, b := range bs {
			if b.what == g.place.what && !holds(p, b.Grant) {
				g.missing = append(g.missing, b.Grant)
				lines = append(lines, "+ grant "+describeOn(b.Grant, b.what))
			}
		}
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return &step{name: name, lines: lines, apply: func() (string, error) {
		n := 0
		for _, g := range groups {
			if len(g.missing) == 0 {
				continue
			}
			var res Result
			err := x.settle(func() error {
				var err error
				res, err = Update(x.ctx, x.d.Google, g.place.rp, func(p *Policy) error { Add(p, g.missing...); return nil })
				return err
			})
			if err != nil {
				return "", fmt.Errorf("%s: %w", g.place.what, err)
			}
			x.changed(res, g.place.what, name)
			n += len(g.missing)
		}
		return fmt.Sprintf("granted %s", count(n, "role")), nil
	}}, nil
}

// changed adds the IAM lines of a policy's change: the grants of the connection are Dormeto's, the others Google's.
func (x *link) changed(res Result, what, step string) {
	added, removed := Diff(res.Before, res.After)
	for _, g := range added {
		if x.ours(g, what) {
			x.iam = append(x.iam, fmt.Sprintf("IAM + %s (Dormeto)", describeOn(g, what)))
		} else {
			x.saw(g, what, step)
		}
	}
	for _, g := range removed {
		x.iam = append(x.iam, fmt.Sprintf("IAM - %s (Google, after %s)", describeOn(g, what), step))
	}
}

func (x *link) ours(g Grant, what string) bool {
	return slices.ContainsFunc(x.wanted, func(b binding) bool { return b.what == what && grantKey(b.Grant) == grantKey(g) })
}

// The record.

// record is the manager's record of this run: the record's bindings of Google with this run's.
func (x *link) record() fleet.GoogleConnection {
	added := []string{}
	if x.rec != nil {
		added = append(added, x.rec.GoogleAdded...)
	}
	for _, g := range x.googleAdded {
		if !slices.Contains(added, g) {
			added = append(added, g)
		}
	}
	routers := append([]fleet.GoogleRouter{}, x.routers...)
	routers = append(routers, x.unseenRouters...)
	slices.SortFunc(routers, func(a, b fleet.GoogleRouter) int {
		return cmp.Or(strings.Compare(a.Project, b.Project), strings.Compare(a.Region, b.Region))
	})
	return fleet.GoogleConnection{Project: x.host, ProjectNumber: x.number, Pool: x.poolID(), Provider: x.providerID(), Scopes: slices.Clone(x.scopes),
		ServiceAccounts: fleet.GoogleServiceAccounts{Discovery: x.email("discovery"), Command: x.email("command")}, Routers: routers,
		GoogleAdded: added, ConnectedAt: x.now(), ConnectedBy: x.account, CLI: x.d.Version}
}

// recordChange is the plan's line of the record: new, or what changes in it ("" for nothing but the last connect's time,
// account and CLI).
func (x *link) recordChange(want fleet.GoogleConnection) string {
	routerName := func(r fleet.GoogleRouter) string { return r.Project + "/" + r.Region }
	if x.rec == nil {
		var routers []string
		for _, r := range want.Routers {
			routers = append(routers, routerName(r))
		}
		return fmt.Sprintf("+ record of the connection in manager %s: %s, %s", x.m.Env, listed(routers, "router"), listed(want.Scopes, "scope"))
	}
	old := *x.rec
	var changes []string
	field := func(name, was, is string) {
		if was != is {
			changes = append(changes, name+" "+is)
		}
	}
	field("project number", old.ProjectNumber, want.ProjectNumber)
	field("pool", old.Pool, want.Pool)
	field("provider", old.Provider, want.Provider)
	field("discovery account", old.ServiceAccounts.Discovery, want.ServiceAccounts.Discovery)
	field("command account", old.ServiceAccounts.Command, want.ServiceAccounts.Command)
	for _, s := range want.Scopes {
		if !slices.Contains(old.Scopes, s) {
			changes = append(changes, "+ scope "+s)
		}
	}
	find := func(rs []fleet.GoogleRouter, r fleet.GoogleRouter) (fleet.GoogleRouter, bool) {
		i := slices.IndexFunc(rs, func(o fleet.GoogleRouter) bool { return o.Project == r.Project && o.Region == r.Region })
		if i < 0 {
			return fleet.GoogleRouter{}, false
		}
		return rs[i], true
	}
	for _, r := range want.Routers {
		was, ok := find(old.Routers, r)
		switch {
		case !ok:
			changes = append(changes, "+ router "+routerName(r))
		case was.Version != r.Version:
			changes = append(changes, fmt.Sprintf("router %s: version %s → %s", routerName(r), cmp.Or(was.Version, "unknown"), cmp.Or(r.Version, "unknown")))
		case was.Controller != r.Controller:
			changes = append(changes, fmt.Sprintf("router %s: controller %s", routerName(r), cmp.Or(r.Controller, "unknown")))
		}
	}
	for _, r := range old.Routers {
		if _, ok := find(want.Routers, r); !ok {
			changes = append(changes, "- router "+routerName(r)+" (not found at the scopes)")
		}
	}
	for _, g := range want.GoogleAdded {
		if !slices.Contains(old.GoogleAdded, g) {
			changes = append(changes, "+ Google's "+g)
		}
	}
	if len(changes) == 0 {
		return ""
	}
	return "~ record of the connection in manager " + x.m.Env + ": " + strings.Join(changes, ", ")
}

// count is "1 router", "2 routers".
func count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// listed is "1 router (a)", "2 routers (a, b)", "no router".
func listed(items []string, word string) string {
	if len(items) == 0 {
		return "no " + word
	}
	return count(len(items), word) + " (" + strings.Join(items, ", ") + ")"
}

// The apply.

func (x *link) applyConnect() error {
	if err := x.applySteps(); err != nil {
		return err
	}
	warnings, err := x.verifyConnect()
	if err != nil {
		x.say("  ✗ verification: %s", err)
		x.warn(warnings)
		if x.early != "" {
			return &stop{1, "The manager's record holds " + x.early + ": run the same command again to change what differs."}
		}
		return &stop{1, "The manager's record is not written: run the same command again to change what differs."}
	}
	x.say("  ✓ verified: the pool and its provider, the two service accounts, the roles' permissions and every planned grant")
	x.warn(warnings)
	if len(x.googleAdded) > 0 {
		x.say("Google added:")
		for _, g := range x.googleAdded {
			x.say("  %s", g)
		}
	}
	want := x.record()
	stored, err := x.c.Manager.PutGoogleConnection(x.ctx, want)
	if err != nil {
		x.say("  ✗ record: %s", err)
		if x.early != "" {
			return &stop{1, "Google Cloud is set up, but the manager's record holds only " + x.early + ": run the same command again."}
		}
		return &stop{1, "Google Cloud is set up, but the manager's record is not written: run the same command again."}
	}
	x.say("  ✓ record: wrote the connection in manager %s: %s registered", x.m.Env, count(len(want.Routers), "router"))
	if err := x.printJSON(map[string]any{"connection": stored}); err != nil {
		return err
	}
	x.say("Connected manager %s to Google Cloud through project %s: %s, %s.", x.m.Env, x.host, count(len(want.Routers), "router"), count(len(want.Scopes), "scope"))
	return nil
}

// applySteps runs the steps in order, every IAM change printed after its step. A refused step stops the run: what was done
// and what was not are said, and the same command continues.
func (x *link) applySteps() error {
	for i, s := range x.steps {
		x.iam = nil
		done, err := s.apply()
		if err != nil {
			code := 1
			if x.ctx.Err() != nil {
				code = exitInterrupted
			}
			x.ctx = context.WithoutCancel(x.ctx)
			x.say("  ✗ %s: %s", s.name, err)
			x.printIAM()
			return &stop{code, fmt.Sprintf("Done: %s. Not done: %s. Run the same command again to continue.", stepNames(x.steps[:i]), stepNames(x.steps[i:]))}
		}
		x.say("  ✓ %s: %s", s.name, done)
		x.printIAM()
	}
	return nil
}

// verifyConnect reads again what the plan set up: the pool and its provider, the accounts, the roles and every grant; the
// host project's policy differing from its first read by more than this run's grants and Google's is a warning.
func (x *link) verifyConnect() (warnings []string, err error) {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	var pool googlePool
	if err := x.seen(x.poolURL(), func() error { return x.d.Google.Do(x.ctx, http.MethodGet, x.poolURL(), nil, &pool) }); err != nil {
		add("pool %s: %s", x.poolID(), err)
	} else if pool.State != "ACTIVE" || pool.Disabled {
		add("pool %s is not active", x.poolID())
	}
	var provider googleProvider
	if err := x.seen(x.providerURL(), func() error { return x.d.Google.Do(x.ctx, http.MethodGet, x.providerURL(), nil, &provider) }); err != nil {
		add("provider %s: %s", x.providerID(), err)
	} else if d := x.providerDifferences(provider); provider.State != "ACTIVE" || len(d) > 0 {
		add("provider %s is %s, differs by %s", x.providerID(), provider.State, strings.Join(d, ", "))
	}
	for _, kind := range accountKinds() {
		var account struct {
			Disabled bool `json:"disabled"`
		}
		accountURL := x.serviceAccountURL(x.email(kind))
		if err := x.seen(accountURL, func() error { return x.d.Google.Do(x.ctx, http.MethodGet, accountURL, nil, &account) }); err != nil {
			add("service account %s: %s", x.email(kind), err)
		} else if account.Disabled {
			add("service account %s is disabled", x.email(kind))
		}
	}
	for _, at := range x.roles {
		want := x.wantManagerRole(at)
		var got googleRole
		if err := x.seen(at.url(), func() error { return x.d.Google.Do(x.ctx, http.MethodGet, at.url(), nil, &got) }); err != nil {
			add("role %s in %s: %s", at.id, at.where(), err)
			continue
		}
		if got.Deleted || got.Stage != roleStage {
			add("role %s in %s is deleted or not at stage %s", at.id, at.where(), roleStage)
		}
		if missing := setDifference(want.Permissions, got.IncludedPermissions); len(missing) > 0 {
			add("role %s in %s lacks %s", at.id, at.where(), strings.Join(missing, ", "))
		}
		if extra := setDifference(got.IncludedPermissions, want.Permissions); len(extra) > 0 {
			add("role %s in %s has %s beyond the manifest", at.id, at.where(), strings.Join(extra, ", "))
		}
	}
	read := map[string]*Policy{}
	for _, b := range x.wanted {
		p, ok := read[b.what]
		if !ok {
			var err error
			if p, err = read1(x, b.rp); err != nil {
				add("the policy of %s: %s", b.what, err)
			}
			read[b.what] = p
		}
		if p != nil && !holds(p, b.Grant) {
			add("%s lacks the grant of %s to %s", b.what, b.Role, b.Member)
		}
	}
	host := "project " + x.host
	p, ok := read[host]
	if !ok {
		p, _ = read1(x, ProjectPolicy(x.host))
	}
	if p != nil {
		added, removed := Diff(x.hostStart, p)
		for _, g := range added {
			if !x.ours(g, host) && !x.google[host+"\x00"+grantKey(g)] {
				warnings = append(warnings, "IAM change not made by this connect: + "+describeOn(g, host))
			}
		}
		for _, g := range removed {
			if !x.removing[host+"\x00"+grantKey(g)] {
				warnings = append(warnings, "IAM change not made by this connect: - "+describeOn(g, host))
			}
		}
	}
	if len(problems) > 0 {
		return warnings, errors.New(strings.Join(problems, "; "))
	}
	return warnings, nil
}

func read1(x *link, rp ResourcePolicy) (p *Policy, err error) {
	err = x.seen(rp.Get, func() (err error) { p, err = read(x.ctx, x.d.Google, rp); return })
	return p, err
}
