package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/progress"
)

// Deps are what a command's run uses outside itself.
type Deps struct {
	Clients func(ctx context.Context, profile, region string) (Clients, error) // internal/aws.Load + installaws.Clients
	Region  func(ctx context.Context, profile string) (string, error)          // the profile's configured region, "" when none
	// Assume is the clients of a role assumed with the profile's credentials (internal/aws.Load + installaws.Assumed +
	// installaws.Clients, session dmt-<env>-install): the member role in each account, for organization access.
	Assume   func(ctx context.Context, profile, region, roleARN string) (Clients, error)
	HTTP     HTTP
	Prompter Prompter
	Out      io.Writer
	Status   progress.Status // nil when stdout is not a terminal
	Terminal bool            // stdin is a terminal: the guide may ask
	Now      func() time.Time
	Every    func(time.Duration) <-chan time.Time
	Version  string // the CLI's own version, recorded in <env>/cli.json
}

// Request is a command line: its profile, region and release, the flags' settings and which flags were given.
type Request struct {
	Profile, Region, Release string          // --profile, --region, --release
	ManagementProfile        string          // --management-profile: the management account's, for organization access
	Flags                    Settings        // the flags' values
	Set                      map[string]bool // the flags given (Merge's keys)
	Yes, Verbose             bool
	Access                   bool // --access: organization access alone, the manager installed
	// --adopt-state and --adopt-module: a manager installed with Terraform taken over by a first install, from its state
	// file and its module's name in it ("": manager).
	AdoptState, AdoptModule string
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
	surveyed    *surveyed // organization access's survey of the prerequisite checks, for after the manager's apply
	adoption    *Adoption // --adopt-state's; nil: not adopting
}

func newRun(ctx context.Context, d Deps, r Request) *run {
	return &run{ctx: ctx, d: d, r: r, interrupted: "Interrupted: nothing was created."}
}

func (x *run) say(format string, a ...any) { fmt.Fprintf(x.d.Out, format+"\n", a...) }

// end is the exit code of the run's result, said: a stop's own; otherwise 130 when the run was cancelled (with what
// that left), else 1 (with AWS's message, that organization access is not deployed when the manager is, and how to sign
// in again with the profile that failed when its SSO session expired).
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
	profile := x.r.Profile
	var access *accessError
	if errors.As(err, &access) {
		if access.installed {
			x.say("Organization access is not deployed; the manager is installed.")
		}
		if access.profile != "" {
			profile = access.profile
		}
	}
	var expired *CredentialsError
	if awserr.SSOToken(err) && !errors.As(err, &expired) { // a build followed says how to sign in and follow it again
		x.say("Run %s and try again.", ssoLogin(profile))
	}
	return 1
}

// ssoLogin is the command that signs in again with profile (the SDK's default: "").
func ssoLogin(profile string) string {
	if profile == "" {
		return "aws sso login"
	}
	return "aws sso login --profile " + profile
}

// args are the flags that name this install again: --management-profile and --profile (when given), --region and
// --env, then --release (when given: a local release is not on releases.dormeto.com).
func (x *run) args() string {
	a := "--region " + x.region + " --env " + x.env
	if x.r.Profile != "" {
		a = "--profile " + x.r.Profile + " " + a
	}
	if x.r.ManagementProfile != "" {
		a = "--management-profile " + x.r.ManagementProfile + " " + a
	}
	if x.r.Release != "" {
		a += " --release " + x.r.Release
	}
	return a
}

// noInstall stops a command that needs the install: there is none of the env here.
func (x *run) noInstall() error {
	return &stop{1, fmt.Sprintf("no install of env %s in %s %s: run dormeto install", x.env, x.w.Account, x.region)}
}

// Prompted is the prompter's answer, or the context's error as soon as the run is cancelled (a terminal's read does
// not end with Ctrl-C), a newline then written to out so that what follows does not land on the question's line.
func Prompted[T any](ctx context.Context, out io.Writer, ask func() (T, error)) (T, error) {
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
	case a := <-got:
		if ctx.Err() == nil {
			return a.v, a.err
		}
	}
	fmt.Fprintln(out)
	return zero, ctx.Err()
}

// asks is how many times an empty answer to a question without a suggestion is asked.
const asks = 3

// ask is an answer no flag gave. Without a terminal, or with --yes, it is the default (ruling R26): an answer without
// one stops the run naming its flag. In a terminal it is the person's, the default suggested: an empty answer to a
// question without a suggestion is asked again, up to three times; the end of the input stops the run at once.
func (x *run) ask(question, def, flag string) (string, error) {
	if x.r.Yes || !x.d.Terminal {
		if def != "" {
			return def, nil
		}
		return "", needed(flag)
	}
	for range asks {
		a, err := Prompted(x.ctx, x.d.Out, func() (string, error) { return x.d.Prompter.Ask(question, def) })
		switch {
		case err != nil && x.ctx.Err() != nil:
			return "", err
		case err != nil:
			return "", needed(flag)
		}
		if a = strings.TrimSpace(a); a != "" {
			return a, nil
		}
	}
	return "", needed(flag)
}

// confirm asks a yes-or-no question in the terminal.
func (x *run) confirm(question string) (bool, error) {
	return Prompted(x.ctx, x.d.Out, func() (bool, error) { return x.d.Prompter.Confirm(question) })
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
	if region == "" && !x.d.Terminal && !x.r.Yes { // eu-west-1 is a suggestion, not a default
		return needed("--region")
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

// merged is the run's settings: the flags' over install.json's (saved, nil on a first install), in the run's env and
// region.
func (x *run) merged(saved *Settings) Settings {
	var prior Settings
	if saved != nil {
		prior = *saved
	}
	s := Merge(x.r.Flags, prior, x.r.Set)
	s.Env, s.Region = x.env, x.region
	return s
}

// wanted is the version the flags ask for: --version's; "" for the local release's (--release) or the latest.
func (x *run) wanted() string {
	if x.r.Set["version"] {
		return x.r.Flags.Version
	}
	return ""
}

// organization sets the organization the account belongs to, which the configuration's trust conditions keep.
func (x *run) organization(s *Settings) {
	s.OrganizationID = ""
	if o := x.w.Organization; o != nil {
		s.OrganizationID = o.ID
	}
}

// valid checks the settings and generates the configuration from them: a bad one stops the run naming its flag.
func (x *run) valid(s Settings) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, &stop{2, err.Error()}
	}
	cfg, err := Config(s, x.w, x.p)
	if err != nil {
		return nil, &stop{2, "--var: " + err.Error()}
	}
	return cfg, nil
}

// resolve reads and checks the release to install: version, else the local release's (--release), else the latest. Its
// error is the release check's.
func (x *run) resolve(version string) (Release, error) {
	if version == "" && x.r.Release == "" {
		latest, err := Latest(x.ctx, x.d.HTTP)
		if err != nil {
			return Release{}, err
		}
		version = latest
	}
	return Resolve(x.ctx, x.d.HTTP, version, x.r.Release)
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
	return Checks(x.ctx, x.c, s, x.w, Found{Saved: recorded(saved, applied), OtherEnvs: others, Release: rel, ReleaseErr: relErr, Adoption: x.adoption}), nil
}

// showChecks prints the checks and, in organization mode without a management profile, that organization access is not
// deployed with what that costs; on an upgrade (upgradeFrom: the version installed), that it is not applied again.
func (x *run) showChecks(s Settings, cs []Check, upgradeFrom string) {
	x.say("\nChecks")
	for _, c := range cs {
		x.say("%s", c.Line())
	}
	o := x.w.Organization
	if _, ok := x.management(s); ok || s.Mode != "organization" || o == nil {
		return
	}
	x.say("  ! organization access: not deployed (no management profile)")
	for _, l := range strings.Split(Disabled(o.Management), "\n") {
		x.say("    %s", l)
	}
	if upgradeFrom != "" {
		x.say("    Organization access is not applied again without --management-profile: each release's notes since %s say whether the roles need it (%s).", upgradeFrom, releasesPage)
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
		if x.w.Organization != nil && x.d.Terminal && !x.r.Yes {
			var err error
			if mode, err = x.ask(modeQuestion(mode, why), mode, "--mode"); err != nil {
				return err
			}
		} else {
			x.say("Mode: %s (%s)", mode, why)
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

// lost ends a run whose build was followed no longer with --profile's credentials, the build going on (goesOn says how
// to follow it): Ctrl-C detaches (130); expired credentials say their own message, with how to sign in again and the
// status command of this run; another failure is said, then goesOn (1).
func (x *run) lost(err error, goesOn string) error { return x.lostWith(err, goesOn, x.r.Profile) }

// lostAccess is lost for the organization build, followed with the management account's credentials.
func (x *run) lostAccess(err error) error {
	profile := x.r.ManagementProfile
	if profile == "" { // the install's own profile is the management account's
		profile = x.r.Profile
	}
	return x.lostWith(err, x.accessGoesOn(), profile)
}

// lostWith is lost, the build followed with profile's credentials.
func (x *run) lostWith(err error, goesOn, profile string) error {
	var expired *CredentialsError
	switch {
	case x.ctx.Err() != nil:
		return &stop{exitInterrupted, "Detached: " + goesOn}
	case errors.As(err, &expired):
		expired.Login, expired.Args = ssoLogin(profile), x.args()
		return err
	}
	return &stop{1, err.Error() + "\n" + strings.ToUpper(goesOn[:1]) + goesOn[1:]}
}

// failed says how a build failed: the log's last lines and the build's page in the console, with its log group (none:
// not named).
func (x *run) failed(label string, res Result, logGroup string) error {
	x.say("%s failed", label)
	for _, l := range res.Tail {
		x.say("  %s", l)
	}
	if logGroup == "" {
		x.say("  %s", res.ConsoleURL)
	} else {
		x.say("  %s (log group %s)", res.ConsoleURL, logGroup)
	}
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
// the dashboard and the invitations checked; with a management profile, organization access after the manager (also
// when the plan changes nothing, and alone with --access). Exit codes: 0; 1 when AWS refuses, a check fails or a build
// fails; 2 for a bad or missing answer; 130 when ctx is cancelled (Ctrl-C).
func Install(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.end(x.install())
}

func (x *run) install() error {
	source, err := x.adoptSource()
	if err != nil {
		return err
	}
	if err := x.locate(); err != nil {
		return err
	}
	saved, err := x.environment(!x.r.Access)
	if err != nil {
		return err
	}
	applied, err := ReadApplied(x.ctx, x.c, x.p)
	if err != nil {
		return err
	}
	if source != nil {
		if x.adoption, err = x.adopting(source, saved, applied); err != nil {
			return err
		}
	}
	s := x.merged(saved)
	if x.r.Access {
		return x.accessAlone(saved, applied, s)
	}
	s.Version = x.wanted()
	if saved == nil {
		// A first install asks the version, the latest suggested; reading the latest failed, the release check says so.
		if s.Version == "" && x.r.Release == "" {
			if latest, err := Latest(x.ctx, x.d.HTTP); err == nil {
				if s.Version, err = x.ask("Manager release", latest, "--version"); err != nil {
					return err
				}
			}
		}
		if err := x.answer(&s); err != nil {
			return err
		}
	}
	x.organization(&s)
	cfg, err := x.valid(s)
	if err != nil {
		return err
	}
	rel, relErr := x.resolve(s.Version)
	if relErr == nil {
		s.Version = rel.Version
		// The configuration depends on the version (the install input from 0.12.0): generate it with the resolved one.
		if cfg, err = x.valid(s); err != nil {
			return err
		}
	}
	cs, err := x.checks(s, saved, applied, rel, relErr)
	if err != nil {
		return err
	}
	access, err := x.surveyChecks(s, rel, relErr)
	if err != nil {
		return err
	}
	cs = append(cs, access...)
	if err := x.ctx.Err(); err != nil {
		return err
	}
	upgradeFrom := ""
	if applied != nil && applied.Version != s.Version {
		upgradeFrom = applied.Version
	}
	x.showChecks(s, cs, upgradeFrom)
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
	if err := WriteCLI(x.ctx, x.c, x.p, x.d.Version); err != nil {
		return err
	}
	role := x.p.Role
	if s.BuildRole != "" {
		role = s.BuildRole
	}
	x.say("\nBootstrap (API calls): bucket %s, role %s, project %s", x.p.Bucket, role, x.p.Name)
	if x.adoption != nil {
		if err := x.adopt(&s); err != nil {
			return err
		}
	}
	s.ReleaseBase = rel.Base
	if rel.Local != "" {
		if s.ReleaseBase, err = UploadRelease(x.ctx, x.c, x.p, rel); err != nil {
			return err
		}
	}
	id, sum, err := x.plan(&s, cfg, "")
	if err != nil {
		return err
	}
	kept := "Not applied; the plan is kept: dormeto install apply " + x.args()
	if sum.Empty() {
		x.say("Nothing to change: manager %s is installed.", s.Version)
		return x.thenAccess(s, rel, false)
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
	if err := x.apply(s, sum, applied); err != nil {
		return err
	}
	return x.thenAccess(s, rel, false)
}

// plan writes the configuration, then makes an install plan of the settings s (startPlan); then is what to run once it
// is applied ("": nothing more), said with the command that applies it.
func (x *run) plan(s *Settings, cfg []byte, then string) (string, PlanSummary, error) {
	if err := PutConfig(x.ctx, x.c, x.p, cfg); err != nil {
		return "", PlanSummary{}, err
	}
	return x.startPlan(s, ActionPlan, planApply, "Plan", "dormeto install apply "+x.args()+then)
}

// startPlan makes a plan of the settings s: its last plan forgotten in s and install.json (a plan of the settings
// before is not theirs: until the new one starts, there is none to apply), a plan build of action a started with s's
// release and followed (label, which names the plan in the messages too), its id and kind kept in s and install.json as
// soon as it runs; then its summary. apply is the command that applies it, said when the run stops.
func (x *run) startPlan(s *Settings, a Action, kind, label, apply string) (string, PlanSummary, error) {
	s.LastPlan, s.LastPlanKind = "", ""
	if err := WriteSettings(x.ctx, x.c, x.p, *s); err != nil {
		return "", PlanSummary{}, err
	}
	id, err := Start(x.ctx, x.c, x.p, a, map[string]string{"RELEASE_BASE": s.ReleaseBase, "RELEASE_VERSION": s.Version})
	if err != nil {
		return "", PlanSummary{}, err
	}
	what := strings.ToLower(label)
	following := "Follow it: dormeto install status " + x.args() + "; apply it once done: " + apply
	x.interrupted = "Interrupted: the " + what + " started (build " + id + ") and goes on in CodeBuild. " + following
	s.LastPlan, s.LastPlanKind = id, kind
	// The plan runs: its id is kept even when Ctrl-C comes now.
	if err := WriteSettings(context.WithoutCancel(x.ctx), x.c, x.p, *s); err != nil {
		return "", PlanSummary{}, err
	}
	res, err := Follow(x.ctx, x.c, x.p, id, x.follow(label, 0))
	if err != nil {
		return "", PlanSummary{}, x.lost(err, "the "+what+" goes on in CodeBuild. "+following)
	}
	if res.Failed {
		return "", PlanSummary{}, x.failed(label, res, x.p.LogGroup)
	}
	x.interrupted = "Not applied; the " + what + " is kept: " + apply
	sum, err := x.summary(id)
	return id, sum, err
}

// applyPlan applies the saved plan s.LastPlan (sum its summary) with s's release by a build of action a and follows it
// (label); goesOn is what a lost follow says of the build. It returns what Terraform's closing line said was done (nil:
// the log has none).
func (x *run) applyPlan(s Settings, sum PlanSummary, a Action, label, goesOn string) (*PlanSummary, error) {
	id, err := Start(x.ctx, x.c, x.p, a, map[string]string{"PLAN_BUILD": s.LastPlan, "RELEASE_BASE": s.ReleaseBase, "RELEASE_VERSION": s.Version})
	if err != nil {
		return nil, err
	}
	o := x.follow(label, sum.Add+sum.Change+sum.Destroy)
	o.Closing = true
	res, err := Follow(x.ctx, x.c, x.p, id, o)
	if err != nil {
		return nil, x.lost(err, goesOn)
	}
	if res.Failed {
		return nil, x.failed(label, res, x.p.LogGroup)
	}
	return res.Applied, nil
}

// applyInstall applies the saved install plan s.LastPlan (sum its summary) and follows it; then is what to run once it
// is applied ("": nothing more), said with the commands that follow and apply it.
func (x *run) applyInstall(s Settings, sum PlanSummary, then string) error {
	x.interrupted = "Not applied; the plan is kept: dormeto install apply " + x.args() + then
	done, err := x.applyPlan(s, sum, ActionApply, "Apply", "the apply goes on in CodeBuild. Follow it: dormeto install status "+x.args()+then)
	if err != nil {
		return err
	}
	if done == nil { // the log lost or cut: only the plan is known
		x.say("  ✓ applied the plan: %d to add, %d to change, %d to destroy", sum.Add, sum.Change, sum.Destroy)
		return nil
	}
	x.say("  ✓ applied: %d added, %d changed, %d destroyed", done.Add, done.Change, done.Destroy)
	return nil
}

// apply applies the saved plan s.LastPlan with s's release, follows it (an adopted manager's first apply, applied nil,
// saying how to detach it from its source), then checks the dashboard and the invitations.
func (x *run) apply(s Settings, sum PlanSummary, applied *Applied) error {
	if err := x.applyInstall(s, sum, ""); err != nil {
		return err
	}
	x.detach(s, applied)
	x.interrupted = "Interrupted: the manager is installed; dormeto install status checks it."
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

// InstallCheck is `dormeto install check` (Check is a check's row): the prerequisites, read-only, without asking
// anything but the region: the version the latest unless given, the mode the suggested one unless given, the
// administrators a warning until given; with a management profile, organization access surveyed. Exit codes: 0; 1
// when a check fails or AWS refuses; 2 for a bad flag; 130 when ctx is cancelled.
func InstallCheck(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.end(x.check())
}

func (x *run) check() error {
	if err := x.locate(); err != nil {
		return err
	}
	saved, err := x.environment(false)
	if err != nil {
		return err
	}
	applied, err := ReadApplied(x.ctx, x.c, x.p)
	if err != nil {
		return err
	}
	s := x.merged(saved)
	s.Version = x.wanted()
	if s.Mode == "" {
		s.Mode, _ = Suggest(x.w)
	}
	x.organization(&s)
	valid := s
	if len(valid.AdminEmails) == 0 { // their row says they are missing
		valid.AdminEmails = []string{"someone@example.com"}
	}
	if _, err := x.valid(valid); err != nil {
		return err
	}
	rel, relErr := x.resolve(s.Version)
	cs, err := x.checks(s, saved, applied, rel, relErr)
	if err != nil {
		return err
	}
	access, err := x.surveyChecks(s, rel, relErr)
	if err != nil {
		return err
	}
	cs = append(cs, access...)
	if err := x.ctx.Err(); err != nil {
		return err
	}
	if len(s.AdminEmails) == 0 {
		for i, c := range cs {
			if c.Name == "administrators" {
				cs[i] = Check{c.Name, "none given yet (--admin-emails)", Warn}
			}
		}
	}
	x.showChecks(s, cs, "")
	if Failed(cs) {
		return &stop{code: 1}
	}
	return nil
}

// Apply is `dormeto install apply`: the explicit confirmation of the last plan (a plan made without a terminal, or
// left at the question), shown, then applied, followed and verified as Install does; with a management profile,
// organization access deployed after it, or alone when there is no plan to apply (ruling R16). Exit codes as Install's.
func Apply(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	x.interrupted = "Interrupted: nothing was applied."
	return x.end(x.applyLast())
}

func (x *run) applyLast() error {
	if err := x.locate(); err != nil {
		return err
	}
	saved, err := x.environment(false)
	if err != nil {
		return err
	}
	if saved == nil {
		return x.noInstall()
	}
	s := x.merged(saved)
	_, access := x.management(s)
	applied, err := ReadApplied(x.ctx, x.c, x.p)
	if err != nil {
		return err
	}
	if saved.LastPlan == "" {
		if !access || applied == nil { // nothing applied: no manager role for the member accounts' roles to trust
			return &stop{1, "no plan to apply: run dormeto install"}
		}
		return x.accessInstalled(s, saved, applied, true)
	}
	if saved.LastPlanKind == planDestroy {
		return &stop{1, "the last plan destroys the manager: dormeto uninstall apply applies it"}
	}
	x.interrupted = "Not applied; the plan is kept: dormeto install apply " + x.args()
	// A stale or failed plan is refused before it is shown; with a management profile, a plan applied already leaves
	// organization access to deploy.
	if err := CheckPlan(x.ctx, x.c, saved.LastPlan, applied); err != nil {
		var stale *StalePlanError
		if access && errors.As(err, &stale) {
			return x.accessInstalled(s, saved, applied, true)
		}
		return err
	}
	// Organization access's prerequisites, before the manager's plan is applied.
	var rel Release
	if access {
		if err := s.Validate(); err != nil {
			return &stop{2, err.Error()}
		}
		if rel, err = x.resolve(saved.Version); err != nil {
			return err
		}
		sv, err := x.survey(s, rel)
		if err != nil {
			return err
		}
		x.showSurvey(sv.cs)
		if Failed(sv.cs) {
			return &stop{1, "Nothing was applied."}
		}
		x.surveyed = &sv
	}
	sum, err := x.summary(saved.LastPlan)
	if err != nil {
		return err
	}
	if sum.Empty() {
		x.say("Nothing to change: manager %s is installed.", saved.Version)
	} else {
		x.showPlan(saved.LastPlan, saved.Version, sum)
		if err := x.apply(*saved, sum, applied); err != nil {
			return err
		}
	}
	if !access {
		return nil
	}
	return x.access(s, rel, true)
}
