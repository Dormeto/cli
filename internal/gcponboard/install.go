package gcponboard

// `dormeto gcp install`: the access of a Dormeto router in a project and a region and, with --image, the router itself
// (spec §5). It checks the caller's permissions (a ✗ stops it before any change), with --image the image too, reads what
// is there, prints the plan, asks (or takes --yes), applies the steps in order — apis, roles, serviceAccount, tag, database
// (and the record), projectBindings, queue (and its binding), serviceAccountBinding, with --image cloudRun, neg and job,
// then record — printing every IAM change after the steps that make one, Dormeto's and Google's, then verifies. Each
// step's file reads what it changes and plans exactly what its apply does. Run again, it plans only the differences;
// none: "up to date".

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/gcp/access"
	"github.com/Dormeto/cli/internal/install"
)

const (
	// settleEvery: a call Google is not ready for yet is tried again every 5 s; accountTries: up to 60 s for a service
	// account just created (IAM's eventual consistency); apiTries: up to 5 minutes for an API that is on but still
	// answers SERVICE_DISABLED (Google says turning one on "may take a few minutes to propagate").
	settleEvery  = 5 * time.Second
	accountTries = 12
	apiTries     = 60

	exitInterrupted = 130
)

// settleTries are the retries of each reason why Google is not ready for a call yet (settle).
var settleTries = map[string]int{"api": apiTries, "account": accountTries}

// operationEvery is how often a long-running operation is polled; tests shorten it.
var operationEvery = 2 * time.Second

var regionPattern = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+$`)

// names are the router's names in its project and region.
type names struct {
	project, region, router, email, member, database, queue string
	controller, waker, neg, job                             string
}

func newNames(project, region string) names {
	router := "dmtr-" + region
	email := router + "@" + project + ".iam.gserviceaccount.com"
	return names{project: project, region: region, router: router, email: email, member: "serviceAccount:" + email,
		database: router, queue: router, controller: router + "-controller", waker: router + "-waker", neg: router + "-waker", job: router + "-check"}
}

// step is one step of the install: its name, its lines in the plan, and its apply, which says what it did.
type step struct {
	name  string
	lines []string
	apply func() (string, error)
}

// stop ends a run with an exit code, after its message (none when empty).
type stop struct {
	code int
	msg  string
}

func (s *stop) Error() string { return s.msg }

// run is one install.
type run struct {
	ctx     context.Context
	d       Deps
	r       Request
	n       names
	number  string // the project's number
	parent  string // the project's organization or folder, "" for none
	account string // the caller, from gcloud
	version string // the router's version: the tag of --image

	turningOn []string        // the APIs off at the start: the apis step turns them on
	turnedOn  bool            // the apis step ran: its APIs are on, though they may answer SERVICE_DISABLED for a while
	waitedFor map[string]bool // the APIs this run said it waits for

	policy   *Policy // the project's IAM policy as last read
	start    *Policy // the project's IAM policy as read at the start of this run, by the plan
	stale    bool    // a step that may change it ran since
	lastStep string  // the last step applied
	// granted and google are the project grants this run added and those it saw Google add (grantKey, role+member).
	granted, google map[string]bool

	rec           *Record
	resumed       bool     // the record is an unfinished install's: from the cache, or the database's with Running set
	sinceLast     []string // the plan's lines of the project's grants that appeared since that install stopped
	enabled       []string // the manifest's APIs on at the start
	recorded      bool     // the record is in the database (read, or written by this run)
	recChanged    bool     // the plan found things the record lacks
	database      bool     // the router's database exists
	accountExists bool     // the router's service account exists
	tagKey        string   // the tag key dormeto's name, tagKeys/<n>, once known

	steps        []step
	kept         []string          // the uninstall's plan lines of what it keeps
	stranded     []string          // what the uninstall could not remove, with Google's reason
	others       map[string]Record // the uninstall's other routers' records of the project, by region
	iam          []string          // the IAM lines of the running step
	editorWarned bool              // the default service account's Editor, appeared during this run, was warned of
}

func newRun(ctx context.Context, d Deps, r Request) *run {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = func(ctx context.Context, wait time.Duration) error {
			t := time.NewTimer(wait)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	_, version, _ := splitImage(r.Image)
	return &run{ctx: ctx, d: d, r: r, n: newNames(r.Project, r.Region), version: version, granted: map[string]bool{}, google: map[string]bool{},
		waitedFor: map[string]bool{}}
}

func (x *run) say(format string, a ...any) { fmt.Fprintf(x.d.Out, format+"\n", a...) }

func (x *run) now() string { return x.d.Now().UTC().Format(time.RFC3339) }

// Install is `dormeto gcp install`: 0 when installed or up to date (or the plan only, with DryRun, or declined), 1 when a
// check fails or Google refuses, 2 for a bad region or image, or no --yes without a terminal, 130 when interrupted.
func Install(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.end(x.install())
}

func (x *run) install() error {
	if err := x.validate(); err != nil {
		return err
	}
	if err := x.checks(); err != nil {
		return err
	}
	if err := x.plan(); err != nil {
		return err
	}
	if len(x.steps) == 0 {
		return x.upToDate()
	}
	x.printPlan()
	if x.r.DryRun {
		return nil
	}
	if !x.r.Yes {
		if !x.d.Terminal {
			return &stop{2, "--yes: needed"}
		}
		question := "Apply these changes to project " + x.n.project + "?"
		ok, err := install.Prompted(x.ctx, x.d.Out, func() (bool, error) { return x.d.Prompter.Confirm(question) })
		if err != nil {
			return err
		}
		if !ok {
			return &stop{0, "Nothing was changed."}
		}
	}
	return x.apply()
}

// upToDate says that nothing differs, after asking the router: a health no step can change is not "up to date".
func (x *run) upToDate() error {
	if x.r.Image == "" {
		x.say("The access of router %s in project %s is up to date.", x.n.router, x.n.project)
		return nil
	}
	if problems := x.verifyRouter(); len(problems) > 0 {
		return &stop{1, "  ✗ verification: " + strings.Join(problems, "; ")}
	}
	x.say("Router %s in project %s is up to date: version %s.", x.n.router, x.n.project, x.version)
	return nil
}

// end is the exit code of the run's result, said: a stop's own; 130 when interrupted; else 1 with the error.
func (x *run) end(err error) int {
	var s *stop
	switch {
	case err == nil:
		return 0
	case errors.As(err, &s):
		if s.msg != "" {
			x.say("%s", s.msg)
		}
		return s.code
	case x.ctx.Err() != nil:
		x.say("Interrupted: nothing was changed.")
		return exitInterrupted
	}
	x.say("%s", err)
	x.say("Nothing was changed.")
	return 1
}

func (x *run) validate() error {
	switch {
	case x.r.Project == "":
		return &stop{2, "--project: needed"}
	case !regionPattern.MatchString(x.r.Region):
		return &stop{2, fmt.Sprintf("--region: %q is not a Google Cloud region such as europe-west1", x.r.Region)}
	case len(x.n.router) > 30:
		return &stop{2, fmt.Sprintf("--region: %s is longer than a service account ID's 30 characters", x.n.router)}
	case x.r.Image != "" && x.version == "":
		return &stop{2, fmt.Sprintf("--image: %s has no tag: its tag is the router's version", x.r.Image)}
	}
	return nil
}

// checks are `dormeto gcp check`'s verdict for install (with --image, install-router too), their permissions alone
// asked: every permission held, or the missing ones named and nothing done.
func (x *run) checks() error {
	commands := []string{"install"}
	if x.r.Image != "" {
		commands = append(commands, "install-router")
	}
	rep, err := check(x.ctx, x.d, x.r, commands)
	if err != nil {
		return err
	}
	x.account = rep.Account
	var refused []string
	for _, c := range rep.Commands {
		if !c.OK() {
			refused = append(refused, fmt.Sprintf("  ✗ %s: missing %s", c.Command, strings.Join(c.Missing, ", ")))
		}
	}
	if len(refused) > 0 {
		msg := ""
		if !rep.Visible {
			msg = fmt.Sprintf("Project %s is not visible to %s: no role in it\n", x.n.project, rep.Account)
		}
		return &stop{1, msg + strings.Join(refused, "\n") + "\nNothing was changed."}
	}
	if rep.APIsNote != "" {
		return &stop{1, rep.APIsNote + "\nNothing was changed."}
	}
	if rep.NoBilling() {
		return &stop{1, "  ✗ " + noBilling(x.n.project, x.r.Configuration) + "\nNothing was changed."}
	}
	x.number, x.parent = rep.Project.Number, rep.Project.Parent
	x.turningOn, x.enabled = rep.APIs.Missing, rep.APIs.Enabled
	x.say("Checked: %s holds every permission dormeto gcp install needs in project %s.", rep.Account, x.n.project)
	for _, w := range rep.Warnings {
		x.say("  ! %s", w)
	}
	return nil
}

// plan checks the image, reads the project's policy (the before snapshot of a first install), the record, then each
// step's resources.
func (x *run) plan() error {
	if x.r.Image != "" {
		if err := x.checkImage(); err != nil {
			return err
		}
	}
	p, err := x.readPolicy(ProjectPolicy(x.n.project))
	if err != nil {
		return err
	}
	x.policy, x.start = p, p
	if err := x.loadRecord(); err != nil {
		return err
	}
	x.resume()
	planners := []func() (*step, error){x.planAPIs, x.planRoles, x.planServiceAccount, x.planTag, x.planDatabase,
		x.planProjectBindings, x.planQueue, x.planAccountBinding}
	if x.r.Image != "" {
		planners = append(planners, x.planCloudRun, x.planNEG, x.planJob)
	}
	for _, plan := range planners {
		s, err := plan()
		if err != nil {
			return err
		}
		if s != nil {
			x.steps = append(x.steps, *s)
		}
	}
	if s := x.planRecord(); s != nil {
		x.steps = append(x.steps, *s)
	}
	return nil
}

func (x *run) printPlan() {
	x.say("Plan for project %s, router %s:", x.n.project, x.n.router)
	for _, s := range x.steps {
		for _, l := range s.lines {
			x.say("  %s", l)
		}
	}
	x.headsUp()
	if len(x.sinceLast) > 0 {
		x.say("IAM changes since the last run, not Dormeto's:")
		for _, l := range x.sinceLast {
			x.say("  %s", l)
		}
	}
}

// headsUp says, after the plan, that turning APIs on and creating the database can make Google add bindings of its own,
// and names those Google added in the live run (2026-10-29) unless the policy holds them already, in the order it added
// them: Editor to the Compute Engine default service account when Compute Engine is turned on in a project without an
// organization (in one, its policy decides); the service agents of Cloud Scheduler, Cloud Tasks and, with Cloud Run, of
// Container Registry, Pub/Sub and Cloud Run when those APIs are; Firestore's and Firebase Rules' when the database is
// created (the latter's late: seen after projectBindings).
func (x *run) headsUp() {
	apis, database := false, false
	for _, s := range x.steps {
		apis, database = apis || s.name == "apis", database || s.name == "database"
	}
	var line string
	switch {
	case apis && database:
		line = "Turning APIs on and creating the database can make Google add bindings of its own: they are shown after those steps."
	case apis:
		line = "Turning APIs on can make Google add bindings of its own: they are shown after that step."
	case database:
		line = "Creating the database can make Google add bindings of its own: they are shown after that step."
	default:
		return
	}
	var expected []string
	expect := func(role, email, step, who string) {
		if g := (Grant{Role: role, Member: "serviceAccount:" + email}); !holds(x.policy, g) {
			expected = append(expected, fmt.Sprintf("%s, after %s (%s)", x.describe(g, "project"), step, who))
		}
	}
	if apis && x.parent == "" && slices.Contains(x.turningOn, "compute.googleapis.com") {
		expect("roles/editor", x.number+"-compute@developer.gserviceaccount.com", "apis",
			"Compute Engine's default service account, in a project without an organization")
	}
	agent := func(domain string) string { return "service-" + x.number + "@" + domain + ".iam.gserviceaccount.com" }
	if apis && slices.Contains(x.turningOn, "cloudscheduler.googleapis.com") {
		expect("roles/cloudscheduler.serviceAgent", agent("gcp-sa-cloudscheduler"), "apis", "Cloud Scheduler's service agent")
	}
	if apis && slices.Contains(x.turningOn, "cloudtasks.googleapis.com") {
		expect("roles/cloudtasks.serviceAgent", agent("gcp-sa-cloudtasks"), "apis", "Cloud Tasks' service agent")
	}
	if apis && slices.Contains(x.turningOn, "run.googleapis.com") {
		expect("roles/containerregistry.ServiceAgent", agent("containerregistry"), "apis", "Container Registry's service agent")
		expect("roles/pubsub.serviceAgent", agent("gcp-sa-pubsub"), "apis", "Pub/Sub's service agent")
		expect("roles/run.serviceAgent", agent("serverless-robot-prod"), "apis", "Cloud Run's service agent")
	}
	if database {
		expect("roles/firestore.serviceAgent", agent("gcp-sa-firestore"), "database", "Firestore's service agent")
		expect("roles/firebaserules.system", agent("firebase-rules"), "projectBindings", "late from the database: Firebase Rules' system account")
	}
	if len(expected) > 0 {
		line += " Expected:"
	}
	x.say("%s", line)
	for _, e := range expected {
		x.say("  %s", e)
	}
}

// warnEditor warns, once, of the Compute Engine default service account's Editor when it appeared during this run
// (there at the start, the checks warned of it): Google grants it when the apis step turns Compute Engine on.
func (x *run) warnEditor() {
	if x.editorWarned || defaultEditor(x.start, x.n.project, x.number) != "" {
		return
	}
	if w := defaultEditor(x.policy, x.n.project, x.number); w != "" {
		x.editorWarned = true
		x.say("  ! %s", w)
	}
}

// apply runs the steps in order. A refused step stops the run: what Google did meanwhile is read again and said, what
// was done and what was not are said, and the done part is kept in the record (or, before the database exists, in the
// cache) for the next run to continue. Each step starts by writing the record with the step it is at (Running): a run
// stopped without a word (killed) is known unfinished by the next one.
func (x *run) apply() error {
	for i, s := range x.steps {
		x.iam = nil
		x.rec.Running = s.name
		if err := x.writeKept(); err != nil {
			if i == 0 {
				return err
			}
			x.say("  ! %s", err)
		}
		done, err := s.apply()
		if err != nil {
			code := 1
			if x.ctx.Err() != nil {
				code = exitInterrupted
			}
			// What is left to do runs to its end: the person stopped the step, not the reading of what it did.
			x.ctx = context.WithoutCancel(x.ctx)
			x.say("  ✗ %s: %s", s.name, err)
			notes := x.salvage(s.name)
			x.printIAM()
			x.warnEditor()
			x.warn(notes)
			x.rec.Running = s.name
			x.persist()
			return &stop{code, fmt.Sprintf("Done: %s. Not done: %s. Run the same command again to continue.",
				stepNames(x.steps[:i]), stepNames(x.steps[i:]))}
		}
		x.say("  ✓ %s: %s", s.name, done)
		x.printIAM()
		x.warnEditor()
		// The others read the project's policy again themselves.
		if !slices.Contains([]string{"apis", "projectBindings", "cloudRun", "neg", "job", "record"}, s.name) {
			x.stale = true
		}
		x.lastStep = s.name
	}
	warnings, err := x.verify()
	if err != nil {
		x.say("  ✗ verification: %s", err)
		x.warn(warnings)
		return &stop{1, "Run the same command again to change what differs."}
	}
	x.say("  ✓ verified: the planned grants, the roles' permissions, the service account, the database and the queue")
	if x.r.Image != "" {
		x.say("  ✓ verified: both Cloud Run services ready, the controller's URL among its URLs, the network endpoint group, the job, /v1/health answering version %s", x.version)
	}
	x.warn(warnings)
	if x.r.Image != "" {
		x.say("Installed router %s in project %s, version %s.", x.n.router, x.n.project, x.version)
		return nil
	}
	x.say("Installed the access of router %s in project %s.", x.n.router, x.n.project)
	return nil
}

// salvage reads, once, what Google did during a step that stopped (refused, interrupted, timed out) after Google may
// have acted on it: the project's policy, its difference printed and recorded as after any step (Google's grants to its
// service agents when an API is turned on or the database created); and, for the apis step, the APIs on now. It
// returns what it could not read.
func (x *run) salvage(step string) []string {
	var notes []string
	if len(x.rec.Enabling) > 0 {
		if on, err := enabledServices(x.ctx, x.d.Google, x.number); err != nil {
			notes = append(notes, "the APIs turned on are not read again: "+err.Error())
		} else {
			x.recordAPIs(on)
		}
	}
	p, err := read(x.ctx, x.d.Google, ProjectPolicy(x.n.project))
	if err != nil {
		return append(notes, fmt.Sprintf("the project's IAM policy is not read again after %s: %s; the next run shows what changed", step, err))
	}
	x.differences(x.policy, p, "project", step)
	x.policy, x.stale = p, false
	return notes
}

func (x *run) warn(warnings []string) {
	for _, w := range warnings {
		x.say("  ! %s", w)
	}
}

func (x *run) printIAM() {
	for _, l := range x.iam {
		x.say("      %s", l)
	}
	x.iam = nil
}

// persist keeps what a stopped run did: in the record when it is in the database, else in the cache.
func (x *run) persist() {
	if err := x.writeKept(); err != nil {
		x.say("  ! what was done is not recorded: %s", err)
	}
}

// writeKept writes the record where it is kept: the database, else the cache.
func (x *run) writeKept() error {
	if x.recorded {
		return x.writeRecord()
	}
	return x.writeCache()
}

func stepNames(steps []step) string {
	if len(steps) == 0 {
		return "none"
	}
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.name
	}
	return strings.Join(out, ", ")
}

// get reads a resource: false when it does not exist, or when its API is off (the apis step turns it on). An API that
// is on but not answering yet is waited for (settle).
func (x *run) get(api, u string, out any) (bool, error) {
	found := false
	err := x.settle(func() error {
		err := x.d.Google.Do(x.ctx, http.MethodGet, u, nil, out)
		switch {
		case err == nil:
			found = true
			return nil
		case gcp.IsNotFound(err), x.off(err, api):
			return nil
		}
		return err
	})
	return found, err
}

// readPolicy reads a policy, waiting for an API that is on but not answering yet (settle).
func (x *run) readPolicy(rp ResourcePolicy) (*Policy, error) {
	var p *Policy
	err := x.settle(func() error {
		var err error
		p, err = read(x.ctx, x.d.Google, rp)
		return err
	})
	return p, err
}

// serviceDisabled is Google's SERVICE_DISABLED: an API off, or on and not propagated yet.
func serviceDisabled(err error) bool {
	var e *gcp.Error
	return errors.As(err, &e) && e.Reason == "SERVICE_DISABLED"
}

// off tells that err is Google's SERVICE_DISABLED for an API that is off and that the apis step will turn on.
func (x *run) off(err error, api string) bool {
	return serviceDisabled(err) && !x.turnedOn && slices.Contains(x.turningOn, api)
}

// call is a call of the apply, tried again while Google is not ready for it (settle).
func (x *run) call(method, u string, in, out any) error {
	return x.settle(func() error { return x.d.Google.Do(x.ctx, method, u, in, out) })
}

// settle runs f again every 5 s while it fails because Google has not caught up yet, in the plan as in the apply: an
// API of the manifest that is on (or was just turned on) still answering SERVICE_DISABLED, up to 5 minutes; a binding
// naming the new service account answering that it does not exist (IAM's eventual consistency), up to 60 s. Then the
// error is the call's.
func (x *run) settle(f func() error) error {
	tries := map[string]int{}
	for {
		err := f()
		kind, api := x.lagging(err)
		if err == nil || kind == "" || tries[kind] == settleTries[kind] {
			return err
		}
		if kind == "api" && !x.waitedFor[api] {
			x.waitedFor[api] = true
			name := api
			if name == "" {
				name = "an API"
			}
			x.say("  … %s is on but does not answer yet: waiting for Google to propagate it (up to 5 minutes)", name)
		}
		tries[kind]++
		if err := x.d.Sleep(x.ctx, settleEvery); err != nil {
			return err
		}
	}
}

// lagging tells why Google is not ready for a call yet ("api" or "account", "" when it is another failure), and the
// API a SERVICE_DISABLED names.
func (x *run) lagging(err error) (kind, api string) {
	var e *gcp.Error
	if !errors.As(err, &e) {
		return "", ""
	}
	if e.Reason == "SERVICE_DISABLED" && (e.Service == "" || slices.Contains(access.Load().APIs, e.Service)) {
		return "api", e.Service
	}
	if e.Code == 400 && strings.Contains(strings.ToLower(e.Message), "service account") && strings.Contains(e.Message, "does not exist") {
		return "account", ""
	}
	return "", ""
}

// longOperation is a long-running operation as a create answers it; Response is the created resource once done.
type longOperation struct {
	Name  string `json:"name"`
	Done  bool   `json:"done"`
	Error *struct {
		Code int `json:"code"`
	} `json:"error"`
	Response json.RawMessage `json:"response"`
}

// wait waits for an operation unless it is done already; base is its API's root (https://<host>/<version>/).
func (x *run) wait(base string, op longOperation, timeout time.Duration) error {
	if op.Done && op.Error == nil {
		return nil
	}
	return gcp.WaitOperation(x.ctx, x.d.Google, base+op.Name, operationEvery, timeout)
}

func esc(s string) string { return url.PathEscape(s) }
