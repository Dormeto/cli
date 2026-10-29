package gcponboard

// `dormeto gcp install` without --image: the access of a Dormeto router in a project and a region (spec §5). It checks
// the caller's permissions (a ✗ stops it before any change), reads what is there, prints the plan, asks (or takes
// --yes), applies the steps in order — apis, roles, serviceAccount, tag, database (and the record), projectBindings,
// queue (and its binding), serviceAccountBinding, record — printing every IAM change after the steps that make one,
// Dormeto's and Google's, then verifies. Each step's file reads what it changes and plans exactly what its apply does.
// Run again, it plans only the differences; none: "up to date".

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/install"
)

const (
	// settleEvery and settleTries: a call Google is not ready for yet (an API just turned on, a service account just
	// created) is tried again every 5 s for up to 60 s.
	settleEvery = 5 * time.Second
	settleTries = 12

	exitInterrupted = 130
)

// operationEvery is how often a long-running operation is polled; tests shorten it.
var operationEvery = 2 * time.Second

var regionPattern = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+$`)

// names are the router's names in its project and region.
type names struct {
	project, region, router, email, member, database, queue string
}

func newNames(project, region string) names {
	router := "dmtr-" + region
	email := router + "@" + project + ".iam.gserviceaccount.com"
	return names{project: project, region: region, router: router, email: email, member: "serviceAccount:" + email,
		database: router, queue: router}
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
	account string // the caller, from gcloud

	turningOn []string // the APIs off at the start: the apis step turns them on
	turnedOn  bool     // the apis step ran: its APIs may still answer SERVICE_DISABLED for a while

	policy   *Policy // the project's IAM policy as last read
	start    *Policy // the project's IAM policy as read at the start of this run, by the plan
	stale    bool    // a step that may change it ran since
	lastStep string  // the last step applied
	// granted and google are the project grants this run added and those it saw Google add (grantKey, role+member).
	granted, google map[string]bool

	rec           *Record
	recorded      bool   // the record is in the database (read, or written by this run)
	recChanged    bool   // the plan found things the record lacks
	database      bool   // the router's database exists
	accountExists bool   // the router's service account exists
	tagKey        string // the tag key dormeto's name, tagKeys/<n>, once known

	steps []step
	iam   []string // the IAM lines of the running step
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
	return &run{ctx: ctx, d: d, r: r, n: newNames(r.Project, r.Region), granted: map[string]bool{}, google: map[string]bool{}}
}

func (x *run) say(format string, a ...any) { fmt.Fprintf(x.d.Out, format+"\n", a...) }

func (x *run) now() string { return x.d.Now().UTC().Format(time.RFC3339) }

// Install is `dormeto gcp install` without --image: 0 when installed or up to date (or the plan only, with DryRun, or
// declined), 1 when a check fails or Google refuses, 2 for a bad region or no --yes without a terminal, 130 when
// interrupted.
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
		x.say("The access of router %s in project %s is up to date.", x.n.router, x.n.project)
		return nil
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
	}
	return nil
}

// checks are `dormeto gcp check`'s verdict for install, its permissions alone asked: every permission held, or the
// missing ones named and nothing done.
func (x *run) checks() error {
	rep, err := check(x.ctx, x.d, x.r, []string{"install"})
	if err != nil {
		return err
	}
	x.account = rep.Account
	for _, c := range rep.Commands {
		if c.Command != "install" || c.OK() {
			continue
		}
		msg := ""
		if !rep.Visible {
			msg = fmt.Sprintf("Project %s is not visible to %s: no role in it\n", x.n.project, rep.Account)
		}
		return &stop{1, msg + fmt.Sprintf("  ✗ install: missing %s\nNothing was changed.", strings.Join(c.Missing, ", "))}
	}
	if rep.APIsNote != "" {
		return &stop{1, rep.APIsNote + "\nNothing was changed."}
	}
	if rep.NoBilling() {
		return &stop{1, "  ✗ " + noBilling(x.n.project, x.r.Configuration) + "\nNothing was changed."}
	}
	x.number = rep.Project.Number
	x.turningOn = rep.APIs.Missing
	x.say("Checked: %s holds every permission dormeto gcp install needs in project %s.", rep.Account, x.n.project)
	for _, w := range rep.Warnings {
		x.say("  ! %s", w)
	}
	return nil
}

// plan reads the project's policy (the before snapshot of a first install), the record, then each step's resources.
func (x *run) plan() error {
	p, err := read(x.ctx, x.d.Google, ProjectPolicy(x.n.project))
	if err != nil {
		return err
	}
	x.policy, x.start = p, p
	if err := x.loadRecord(); err != nil {
		return err
	}
	planners := []func() (*step, error){x.planAPIs, x.planRoles, x.planServiceAccount, x.planTag, x.planDatabase,
		x.planProjectBindings, x.planQueue, x.planAccountBinding}
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
	apis := false
	for _, s := range x.steps {
		for _, l := range s.lines {
			x.say("  %s", l)
		}
		apis = apis || s.name == "apis"
	}
	if apis {
		x.say("Turning APIs on can make Google add bindings of its own: they are shown after that step.")
	}
}

// apply runs the steps in order. A refused step stops the run: what was done and what was not are said, and the done
// part is kept in the record (or, before the database exists, in the cache) for the next run to continue.
func (x *run) apply() error {
	if !x.recorded {
		if err := x.writeCache(); err != nil {
			return err
		}
	}
	for i, s := range x.steps {
		x.iam = nil
		done, err := s.apply()
		if err != nil {
			code := 1
			if x.ctx.Err() != nil {
				code = exitInterrupted
			}
			x.say("  ✗ %s: %s", s.name, err)
			x.printIAM()
			x.persist()
			return &stop{code, fmt.Sprintf("Done: %s. Not done: %s. Run the same command again to continue.",
				stepNames(x.steps[:i]), stepNames(x.steps[i:]))}
		}
		x.say("  ✓ %s: %s", s.name, done)
		x.printIAM()
		if s.name != "apis" && s.name != "projectBindings" && s.name != "record" {
			x.stale = true
		}
		x.lastStep = s.name
		if !x.recorded {
			if err := x.writeCache(); err != nil {
				x.say("  ! %s", err)
			}
		}
	}
	warnings, err := x.verify()
	if err != nil {
		x.say("  ✗ verification: %s", err)
		x.warn(warnings)
		return &stop{1, "Run the same command again to change what differs."}
	}
	x.say("  ✓ verified: the planned grants, the roles' permissions, the service account, the database and the queue")
	x.warn(warnings)
	x.say("Installed the access of router %s in project %s.", x.n.router, x.n.project)
	return nil
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
	x.ctx = context.WithoutCancel(x.ctx)
	var err error
	if x.recorded {
		err = x.writeRecord()
	} else {
		err = x.writeCache()
	}
	if err != nil {
		x.say("  ! what was done is not recorded: %s", err)
	}
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

// get reads a resource: false when it does not exist, or when its API is off (the apis step turns it on).
func (x *run) get(api, u string, out any) (bool, error) {
	err := x.d.Google.Do(x.ctx, "GET", u, nil, out)
	switch {
	case err == nil:
		return true, nil
	case gcp.IsNotFound(err), x.off(err, api):
		return false, nil
	}
	return false, err
}

// off tells that err is Google's SERVICE_DISABLED for an API the apis step turns on.
func (x *run) off(err error, api string) bool {
	var e *gcp.Error
	if !errors.As(err, &e) || e.Reason != "SERVICE_DISABLED" {
		return false
	}
	for _, a := range x.turningOn {
		if a == api {
			return true
		}
	}
	return false
}

// call is a call of the apply, tried again while Google is not ready for it (settle).
func (x *run) call(method, u string, in, out any) error {
	return x.settle(func() error { return x.d.Google.Do(x.ctx, method, u, in, out) })
}

// settle runs f again every 5 s, up to 60 s, while it fails because Google has not caught up with this run: an API
// just turned on still answering SERVICE_DISABLED, or a binding naming the new service account answering that it does
// not exist (IAM's eventual consistency).
func (x *run) settle(f func() error) error {
	for try := 0; ; try++ {
		err := f()
		if err == nil || try == settleTries || !x.lagging(err) {
			return err
		}
		if err := x.d.Sleep(x.ctx, settleEvery); err != nil {
			return err
		}
	}
}

func (x *run) lagging(err error) bool {
	var e *gcp.Error
	if !errors.As(err, &e) {
		return false
	}
	if e.Reason == "SERVICE_DISABLED" {
		return x.turnedOn
	}
	return e.Code == 400 && strings.Contains(strings.ToLower(e.Message), "service account") && strings.Contains(e.Message, "does not exist")
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
