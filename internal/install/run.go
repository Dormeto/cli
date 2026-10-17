package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/progress"
)

// Deps are what a command's run uses outside itself.
type Deps struct {
	Clients  func(ctx context.Context, profile, region string) (Clients, error) // internal/aws.Load + installaws.Clients
	Region   func(ctx context.Context, profile string) (string, error)          // the profile's configured region, "" when none
	HTTP     HTTP
	Prompter Prompter
	Out      io.Writer
	Status   progress.Status // nil when stdout is not a terminal
	Terminal bool            // stdin is a terminal: the guide may ask
	Now      func() time.Time
	Every    func(time.Duration) <-chan time.Time
}

// Request is a command line: its profile, region and release, the flags' settings and which flags were given.
type Request struct {
	Profile, Region, Release string          // --profile, --region, --release
	Flags                    Settings        // the flags' values
	Set                      map[string]bool // the flags given (Merge's keys)
	Yes, Verbose             bool
}

// exitInterrupted is the exit code of a run Ctrl-C stopped: 128 + SIGINT.
const exitInterrupted = 130

// none is the suggestion of an optional answer left out.
const none = "none"

// stop ends a run with an exit code, after its message (none when empty).
type stop struct {
	code int
	msg  string
}

func (s *stop) Error() string { return s.msg }

// needed stops a run on an answer it cannot get (no terminal to ask, or no answer), naming its flag.
func needed(flag string) error { return &stop{2, flag + ": needed"} }

// run is one command's run: what it was given, then where it runs.
type run struct {
	ctx context.Context
	d   Deps
	r   Request
	// interrupted is what a Ctrl-C now leaves, said when the run ends cancelled.
	interrupted string
	region, env string
	c           Clients
	w           Where
	p           Project
}

func newRun(ctx context.Context, d Deps, r Request) *run {
	return &run{ctx: ctx, d: d, r: r, interrupted: "Interrupted: nothing was created."}
}

func (x *run) say(format string, a ...any) { fmt.Fprintf(x.d.Out, format+"\n", a...) }

// end is the exit code of the run's result, said: a stop's own; otherwise 130 when the run was cancelled (with what
// that left), else 1 (with AWS's message).
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
		x.say("%s", x.interrupted)
		return exitInterrupted
	}
	x.say("%s", err)
	return 1
}

// args are the flags that name this install again: --profile (when given), --region and --env.
func (x *run) args() string {
	a := "--region " + x.region + " --env " + x.env
	if x.r.Profile != "" {
		a = "--profile " + x.r.Profile + " " + a
	}
	return a
}

// prompted is the prompter's answer, or the context's error as soon as the run is cancelled: a terminal's read does
// not end with Ctrl-C.
func prompted[T any](ctx context.Context, ask func() (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	type answer struct {
		v   T
		err error
	}
	got := make(chan answer, 1)
	go func() {
		v, err := ask()
		got <- answer{v, err}
	}()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case a := <-got:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return a.v, a.err
	}
}

// ask is an answer no flag gave: the suggestion with --yes, else the person's in a terminal. Without a terminal, or
// without an answer, the run stops naming the flag.
func (x *run) ask(question, suggestion, flag string) (string, error) {
	if x.r.Yes || !x.d.Terminal {
		if x.r.Yes && suggestion != "" {
			return suggestion, nil
		}
		return "", needed(flag)
	}
	a, err := prompted(x.ctx, func() (string, error) { return x.d.Prompter.Ask(question, suggestion) })
	if err != nil && x.ctx.Err() != nil {
		return "", err
	}
	if a = strings.TrimSpace(a); err != nil || a == "" {
		return "", needed(flag)
	}
	return a, nil
}

// confirm asks a yes-or-no question in the terminal.
func (x *run) confirm(question string) (bool, error) {
	return prompted(x.ctx, func() (bool, error) { return x.d.Prompter.Confirm(question) })
}

// locate finds where the run happens: the region (the flag's, else the profile's, else asked), the clients, the
// account and its organization.
func (x *run) locate() error {
	region := x.r.Region
	if region == "" {
		got, err := x.d.Region(x.ctx, x.r.Profile)
		if err != nil {
			return err
		}
		region = got
	}
	if region == "" {
		got, err := x.ask("Region", "eu-west-1", "--region")
		if err != nil {
			return err
		}
		region = got
	}
	if err := validRegion(region); err != nil {
		return &stop{2, err.Error()}
	}
	c, err := x.d.Clients(x.ctx, x.r.Profile, region)
	if err != nil {
		return err
	}
	w, err := Locate(x.ctx, c, region)
	if err != nil {
		return err
	}
	x.region, x.c, x.w = region, c, w
	if x.r.Profile != "" {
		x.say("Profile %s: %s", x.r.Profile, w.Describe())
	} else {
		x.say("AWS credentials: %s", w.Describe())
	}
	return nil
}

// environment is the env's install.json (nil: none): the env is the flag's, else prod, which a first install in a
// terminal asks about when ask.
func (x *run) environment(ask bool) (*Settings, error) {
	env := "prod"
	if x.r.Set["env"] {
		env = x.r.Flags.Env
	}
	saved, err := x.settings(env)
	if err != nil || saved != nil || !ask || x.r.Set["env"] || !x.d.Terminal || x.r.Yes {
		return saved, err
	}
	answer, err := x.ask("Environment", env, "--env")
	if err != nil || answer == env {
		return saved, err
	}
	return x.settings(answer)
}

// settings makes env the run's, with its project, and reads its install.json.
func (x *run) settings(env string) (*Settings, error) {
	if err := validEnv(env); err != nil {
		return nil, &stop{2, err.Error()}
	}
	x.env, x.p = env, Names(x.w.Account, x.region, env)
	return ReadSettings(x.ctx, x.c, x.p)
}

// recorded is install.json as the checks read it: what the apply build recorded in applied.json is its installed
// version and last apply (nil: no install.json).
func recorded(saved *Settings, applied *Applied) *Settings {
	if saved == nil {
		return nil
	}
	s := *saved
	if applied != nil {
		s.Installed, s.LastApply = applied.Version, applied.Build
	}
	return &s
}

// checks runs the prerequisites, the bucket's other environments read first.
func (x *run) checks(s Settings, saved *Settings, applied *Applied, rel Release, relErr error) ([]Check, error) {
	others, err := OtherEnvs(x.ctx, x.c, x.p)
	if err != nil {
		return nil, err
	}
	return Checks(x.ctx, x.c, s, x.w, Found{Saved: recorded(saved, applied), OtherEnvs: others, Release: rel, ReleaseErr: relErr}), nil
}

// showChecks prints the checks and, in organization mode, that organization access is not deployed (no management
// profile) with what that costs.
func (x *run) showChecks(s Settings, cs []Check) {
	x.say("\nChecks")
	for _, c := range cs {
		x.say("%s", c.Line())
	}
	if o := x.w.Organization; s.Mode == "organization" && o != nil {
		x.say("  ! organization access: not deployed (no management profile)")
		for _, l := range strings.Split(Disabled(o.Management), "\n") {
			x.say("    %s", l)
		}
	}
}

// list is a comma-separated answer's items, trimmed, the empty ones left out.
func list(answer string) []string {
	var out []string
	for _, a := range strings.Split(answer, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// modeQuestion asks the mode, with why the suggested one is.
func modeQuestion(mode, why string) string {
	if mode == "organization" {
		return "Mode: organization (" + why + ") or account"
	}
	return "Mode: organization or account (" + why + ")"
}

// answer asks, in order, what neither a flag nor an install.json gives: a first install's.
func (x *run) answer(s *Settings) error {
	set := x.r.Set
	if !set["admin-emails"] {
		a, err := x.ask("Administrators (emails, comma-separated)", "", "--admin-emails")
		if err != nil {
			return err
		}
		s.AdminEmails = list(a)
	}
	if !set["mode"] {
		mode, why := Suggest(x.w)
		if x.w.Organization != nil {
			var err error
			if mode, err = x.ask(modeQuestion(mode, why), mode, "--mode"); err != nil {
				return err
			}
		}
		s.Mode = mode
	}
	if !set["domain"] {
		host, err := x.ask("Custom domain", none, "--domain")
		if err != nil {
			return err
		}
		if !strings.EqualFold(host, none) {
			cert, err := x.ask("Certificate ARN (us-east-1)", "", "--certificate-arn")
			if err != nil {
				return err
			}
			zone, err := x.ask("Route 53 zone id", "", "--zone-id")
			if err != nil {
				return err
			}
			s.Domain = &Domain{Dashboard: host, CertificateARN: cert, ZoneID: zone}
		}
	}
	if !set["ses-identity"] {
		id, err := x.ask("SES identity", none, "--ses-identity")
		if err != nil {
			return err
		}
		if !strings.EqualFold(id, none) {
			s.SESIdentity = id
		}
	}
	if s.SESIdentity != "" && !set["email-from"] {
		from, err := x.ask("Sender (email_from)", none, "--email-from")
		if err != nil {
			return err
		}
		if !strings.EqualFold(from, none) {
			s.EmailFrom = from
		}
	}
	return nil
}

// follow is how a build is shown: the status line in a terminal, Terraform's lines otherwise or with --verbose.
func (x *run) follow(label string, total int) FollowOptions {
	return FollowOptions{Label: label, Total: total, Status: x.d.Status, Out: x.d.Out, Lines: x.r.Verbose, Every: x.d.Every, Now: x.d.Now}
}

// failed says how a build failed: the log's last lines and the build's page in the console.
func (x *run) failed(label string, res Result) error {
	x.say("%s failed", label)
	for _, l := range res.Tail {
		x.say("  %s", l)
	}
	x.say("  %s (log group %s)", res.ConsoleURL, x.p.LogGroup)
	return &stop{code: 1}
}

// summary reads a plan build's plan.json and counts it.
func (x *run) summary(build string) (PlanSummary, error) {
	b, err := ReadPlan(x.ctx, x.c, x.p, build)
	if err != nil {
		return PlanSummary{}, err
	}
	if b == nil {
		return PlanSummary{}, fmt.Errorf("no plan.json for build %s in s3://%s/%s: plan again (dormeto install)", build, x.p.Bucket, x.p.Key("plans", build))
	}
	return Summarize(b)
}

// showPlan prints what the plan does to the manager, a destroy or a replacement warned about.
func (x *run) showPlan(build, version string, sum PlanSummary) {
	lines := sum.Lines()
	x.say("Plan (build %s)", build)
	x.say("  manager %s: %s", version, lines[0])
	for _, l := range lines[1:] {
		x.say("  %s", l)
	}
	if sum.Destroy > 0 {
		x.say("  ! this plan destroys or replaces resources (above)")
	}
}

// Install is `dormeto install`: where it runs, the answers (asked on a first install), the checks, the bootstrap, the
// plan in CodeBuild, its summary, the person's confirmation in a terminal, the saved plan applied and followed, then
// the dashboard and the invitations checked. Exit codes: 0; 1 when AWS refuses, a check fails or a build fails; 2 for a
// bad or missing answer; 130 when ctx is cancelled (Ctrl-C).
func Install(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.end(x.install())
}

func (x *run) install() error {
	if err := x.locate(); err != nil {
		return err
	}
	saved, err := x.environment(true)
	if err != nil {
		return err
	}
	applied, err := ReadApplied(x.ctx, x.c, x.p)
	if err != nil {
		return err
	}
	var prior Settings
	if saved != nil {
		prior = *saved
	}
	s := Merge(x.r.Flags, prior, x.r.Set)
	s.Env, s.Region = x.env, x.region

	// The version: the flag's; with --release, the local release's; else the latest, which a first install asks about.
	version := ""
	if x.r.Set["version"] {
		version = x.r.Flags.Version
	}
	var latestErr error
	if version == "" && x.r.Release == "" {
		var latest string
		switch latest, latestErr = Latest(x.ctx, x.d.HTTP); {
		case latestErr != nil: // the release check says it
		case saved == nil:
			if version, err = x.ask("Manager release", latest, "--version"); err != nil {
				return err
			}
		default:
			version = latest
		}
	}
	s.Version = version
	if saved == nil {
		if err := x.answer(&s); err != nil {
			return err
		}
	}
	s.OrganizationID = ""
	if o := x.w.Organization; o != nil {
		s.OrganizationID = o.ID
	}
	if err := s.Validate(); err != nil {
		return &stop{2, err.Error()}
	}
	cfg, err := Config(s, x.w, x.p)
	if err != nil {
		return &stop{2, "--var: " + err.Error()}
	}

	rel, relErr := Release{}, latestErr
	if relErr == nil {
		if rel, relErr = Resolve(x.ctx, x.d.HTTP, s.Version, x.r.Release); relErr == nil {
			s.Version = rel.Version
		}
	}
	cs, err := x.checks(s, saved, applied, rel, relErr)
	if err != nil {
		return err
	}
	if err := x.ctx.Err(); err != nil {
		return err
	}
	x.showChecks(s, cs)
	if Failed(cs) {
		return &stop{1, "Nothing was created."}
	}
	if len(Warnings(cs)) > 0 && x.d.Terminal && !x.r.Yes {
		ok, err := x.confirm("Continue despite the warnings?")
		if err != nil {
			return err
		}
		if !ok {
			return &stop{1, "Nothing was created."}
		}
	}

	x.interrupted = "Interrupted before the plan started: run dormeto install again."
	if err := Bootstrap(x.ctx, x.c, x.p, s.BuildRole); err != nil {
		return err
	}
	role := x.p.Role
	if s.BuildRole != "" {
		role = s.BuildRole
	}
	x.say("\nBootstrap (API calls): bucket %s, role %s, project %s", x.p.Bucket, role, x.p.Name)
	s.ReleaseBase = rel.Base
	if rel.Local != "" {
		if s.ReleaseBase, err = UploadRelease(x.ctx, x.c, x.p, rel); err != nil {
			return err
		}
	}
	if err := PutConfig(x.ctx, x.c, x.p, cfg); err != nil {
		return err
	}
	if err := WriteSettings(x.ctx, x.c, x.p, s); err != nil {
		return err
	}

	id, err := Start(x.ctx, x.c, x.p, ActionPlan, map[string]string{"RELEASE_BASE": s.ReleaseBase, "RELEASE_VERSION": s.Version})
	if err != nil {
		return err
	}
	s.LastPlan = id
	if err := WriteSettings(x.ctx, x.c, x.p, s); err != nil {
		return err
	}
	res, err := Follow(x.ctx, x.c, x.p, id, x.follow("Plan", 0))
	if err != nil {
		if x.ctx.Err() != nil {
			return &stop{exitInterrupted, "Detached: the plan goes on in CodeBuild. Follow it: dormeto install status " + x.args() +
				"; apply it once done: dormeto install apply " + x.args()}
		}
		return err
	}
	if res.Failed {
		return x.failed("Plan", res)
	}
	kept := "Not applied; the plan is kept: dormeto install apply " + x.args()
	x.interrupted = kept
	sum, err := x.summary(id)
	if err != nil {
		return err
	}
	if sum.Empty() {
		return &stop{0, "Nothing to change: manager " + s.Version + " is installed."}
	}
	x.showPlan(id, s.Version, sum)
	if !x.d.Terminal {
		return &stop{0, "Plan saved. Apply it: dormeto install apply " + x.args()}
	}
	ok, err := x.confirm("Apply this plan?")
	if err != nil {
		return err
	}
	if !ok {
		return &stop{0, kept}
	}
	if err := CheckPlan(x.ctx, x.c, s.LastPlan, applied); err != nil {
		return err
	}
	return x.apply(s, sum)
}

// apply applies the saved plan s.LastPlan with s's release, follows it, then checks the dashboard and the invitations.
func (x *run) apply(s Settings, sum PlanSummary) error {
	x.interrupted = "Not applied; the plan is kept: dormeto install apply " + x.args()
	id, err := Start(x.ctx, x.c, x.p, ActionApply, map[string]string{"PLAN_BUILD": s.LastPlan, "RELEASE_BASE": s.ReleaseBase, "RELEASE_VERSION": s.Version})
	if err != nil {
		return err
	}
	res, err := Follow(x.ctx, x.c, x.p, id, x.follow("Apply", sum.Add+sum.Change+sum.Destroy))
	if err != nil {
		if x.ctx.Err() != nil {
			return &stop{exitInterrupted, "Detached: the apply goes on in CodeBuild. Follow it: dormeto install status " + x.args()}
		}
		return err
	}
	if res.Failed {
		return x.failed("Apply", res)
	}
	x.interrupted = "Interrupted: the manager is installed; dormeto install status checks it."
	x.say("  ✓ applied: %d added, %d changed, %d destroyed", sum.Add, sum.Change, sum.Destroy)
	out, err := ReadOutputs(x.ctx, x.c, x.p)
	if err != nil {
		return err
	}
	if out == nil {
		x.say("  ! no outputs.json: the dashboard and the invitations are not checked")
		return nil
	}
	for _, c := range Verify(x.ctx, x.c, x.d.HTTP, *out, s.AdminEmails, VerifyOptions{Every: x.d.Every, Now: x.d.Now}) {
		x.say("%s", c.Line())
	}
	if err := x.ctx.Err(); err != nil {
		return err
	}
	x.say("Next: sign in at %s; in Discovery, Perimeter settings, add a filter, then Run now.", out.DashboardURL)
	return nil
}

// InstallCheck is `dormeto install check` (Check is a check's row).
func InstallCheck(ctx context.Context, d Deps, r Request) int { return -1 }

// Apply is `dormeto install apply`.
func Apply(ctx context.Context, d Deps, r Request) int { return -1 }
