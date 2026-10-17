package install

// Organization access in the runs (spec §9, rulings R10 and R16): with the management account's profile, the survey
// read from it, the plan shown, confirmed in a terminal (install) or by the explicit command (install apply), then
// applied by the management account's short-lived CodeBuild project, followed account by account, its project and role
// removed once it ends (decision 17), and the accounts' results said.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/codebuild"
)

const (
	// defaultMemberRole is the member role supposed when none is given: the role AWS creates in the accounts an
	// organization creates.
	defaultMemberRole = "OrganizationAccountAccessRole"
	policyFile        = "dormeto-organization-policy.json"
	releasesPage      = "https://dormeto.com/releases.html"
)

// roleLineRe is organization.sh's line once an account's roles are done: the follow counts them.
var roleLineRe = regexp.MustCompile(`^dormeto: roles of \d{12}: (ok|failed)$`)

// management is the profile organization access reads the management account with, and whether there is one: in
// organization mode, --management-profile, else the install's own profile when its account is the management account.
func (x *run) management(s Settings) (string, bool) {
	switch {
	case s.Mode != "organization" || x.w.Organization == nil:
		return "", false
	case x.r.ManagementProfile != "":
		return x.r.ManagementProfile, true
	case x.w.IsManagement():
		return x.r.Profile, true
	}
	return "", false
}

// memberRole is the role assumed in each member account: --member-role's (its leading slash left out), else the
// supposed one.
func memberRole(s Settings) string {
	if s.MemberRole == "" {
		return defaultMemberRole
	}
	return strings.TrimPrefix(s.MemberRole, "/")
}

// installedVersion is the manager's version installed: applied.json's, else install.json's.
func installedVersion(saved *Settings, applied *Applied) string {
	if applied != nil && applied.Version != "" {
		return applied.Version
	}
	return saved.Version
}

// template is the release's delegation policy: the local release's file (Resolve checked it), else read from the
// release's base and checked against its SHA256SUMS line.
func (x *run) template(rel Release) ([]byte, error) {
	if rel.Local != "" {
		return os.ReadFile(filepath.Join(rel.Local, policyFile))
	}
	b, _, err := get(x.ctx, x.d.HTTP, rel.Base+"/"+policyFile)
	if err != nil {
		return nil, err
	}
	if h := sha256.Sum256(b); hex.EncodeToString(h[:]) != rel.Sums[policyFile] {
		return nil, fmt.Errorf("%s/%s does not match its SHA256SUMS line", rel.Base, policyFile)
	}
	return b, nil
}

// managementClients are the management account's clients, profile its: the install's own when no --management-profile
// is given (its account is the management account).
func (x *run) managementClients(profile string) (Clients, error) {
	if x.r.ManagementProfile == "" {
		return x.c, nil
	}
	return x.d.Clients(x.ctx, profile, x.region)
}

// accessError is a failure of organization access other than a check: profile is the profile of the calls it made
// when not --profile's (the management account's), installed tells that the manager is installed already.
type accessError struct {
	err       error
	profile   string
	installed bool
}

func (e *accessError) Error() string { return e.err.Error() }
func (e *accessError) Unwrap() error { return e.err }

// accessErr marks err as organization access's, made with profile (the install's own: ""), the manager installed or
// not; a stop, and expired credentials while following a build that goes on, stay as they are.
func accessErr(err error, profile string, installed bool) error {
	var s *stop
	var expired *CredentialsError
	var ae *accessError
	switch {
	case err == nil, errors.As(err, &s), errors.As(err, &expired):
		return err
	case errors.As(err, &ae):
		ae.installed = ae.installed || installed
		return err
	}
	return &accessError{err: err, profile: profile, installed: installed}
}

// surveyed is a survey of organization access: the management account's clients, what it found and its checks.
type surveyed struct {
	mgmt Clients
	a    Access
	cs   []Check
}

// survey reads organization access from the management account, changing nothing. Its errors name the management
// profile.
func (x *run) survey(s Settings, rel Release) (surveyed, error) {
	profile, _ := x.management(s)
	mgmt, err := x.managementClients(profile)
	if err != nil {
		return surveyed{}, accessErr(err, x.r.ManagementProfile, false)
	}
	template, err := x.template(rel)
	if err != nil {
		return surveyed{}, accessErr(err, "", false)
	}
	s.MemberRole = memberRole(s)
	assume := func(account string) (Clients, error) {
		return x.d.Assume(x.ctx, profile, x.region, "arn:aws:iam::"+account+":role/"+s.MemberRole)
	}
	a, cs, err := Survey(x.ctx, mgmt, assume, s, x.w, template)
	if err != nil {
		return surveyed{}, accessErr(err, x.r.ManagementProfile, false)
	}
	return surveyed{mgmt, a, cs}, x.ctx.Err() // a cancelled survey reads as unreachable accounts
}

// surveyChecks are organization access's rows among the prerequisite checks (spec §5), with a management profile in
// organization mode: the survey's, kept for after the manager's apply (none without the release to read the delegation
// policy from: its own row fails).
func (x *run) surveyChecks(s Settings, rel Release, relErr error) ([]Check, error) {
	if _, ok := x.management(s); !ok {
		return nil, nil
	}
	if relErr != nil {
		return []Check{{"organization access", "not checked (no release to read the delegation policy from)", Skip}}, nil
	}
	sv, err := x.survey(s, rel)
	if err != nil {
		return nil, err
	}
	x.surveyed = &sv
	return sv.cs, nil
}

// showSurvey prints the survey's checks.
func (x *run) showSurvey(cs []Check) {
	x.say("\nOrganization access")
	for _, c := range cs {
		x.say("%s", c.Line())
	}
}

// thenAccess runs organization access once the manager is installed, when there is a management profile (confirmed:
// by the explicit command, nothing asked); otherwise nothing.
func (x *run) thenAccess(s Settings, rel Release, confirmed bool) error {
	if _, ok := x.management(s); !ok {
		return nil
	}
	return x.access(s, rel, confirmed)
}

// accessInstalled runs organization access for the manager installed (saved, applied), with its release: confirmed by
// the explicit command, else asked.
func (x *run) accessInstalled(s Settings, saved *Settings, applied *Applied, confirmed bool) error {
	if err := s.Validate(); err != nil {
		return &stop{2, err.Error()}
	}
	rel, err := x.resolve(installedVersion(saved, applied))
	if err != nil {
		return accessErr(err, "", true)
	}
	return x.access(s, rel, confirmed)
}

// accessAlone is install --access: organization access alone, the manager installed (applied: its roles exist for the
// member accounts' roles to trust) and not planned again.
func (x *run) accessAlone(saved *Settings, applied *Applied, s Settings) error {
	if saved == nil || applied == nil {
		return x.noInstall()
	}
	if _, ok := x.management(s); !ok {
		return &stop{2, "--access: organization access needs organization mode and the management account's profile (--management-profile)"}
	}
	return x.accessInstalled(s, saved, applied, false)
}

// access is organization access after the manager (s its settings, rel its release): the survey (the one the
// prerequisite checks made, else one now, shown), what a detached run left removed, the plan shown, then, once
// confirmed (asked in a terminal; by the explicit command), deployed. Without a terminal it stops with the command that
// deploys it. Its errors say the manager is installed.
func (x *run) access(s Settings, rel Release, confirmed bool) error {
	return accessErr(x.accessSteps(s, rel, confirmed), x.r.ManagementProfile, true)
}

func (x *run) accessSteps(s Settings, rel Release, confirmed bool) error {
	x.interrupted = "Interrupted: the manager is installed; organization access is not deployed."
	sv := x.surveyed
	if sv == nil {
		now, err := x.survey(s, rel)
		if err != nil {
			return err
		}
		x.showSurvey(now.cs)
		if Failed(now.cs) {
			return &stop{1, "Organization access is not deployed (a check failed); the manager is installed."}
		}
		sv = &now
	}
	mgmt, a := sv.mgmt, sv.a
	op := OrgNames(a.Management, x.region, x.env)
	if running, err := x.clearLeftover(mgmt, op); err != nil || running != "" {
		if err != nil {
			return err
		}
		return &stop{1, "Organization access is running already (build " + running + "): dormeto install status " + x.args() + " follows it"}
	}
	if len(a.Accounts()) == 0 && a.PolicyChange == "unchanged" {
		x.say("Organization access: nothing to do.")
		return nil
	}
	x.showAccessPlan(a)
	if !confirmed {
		notDeployed := &stop{0, "Organization access not deployed: dormeto install apply " + x.args() + " deploys it"}
		if !x.d.Terminal {
			return notDeployed
		}
		ok, err := x.confirm("Deploy organization access?")
		if err != nil {
			return err
		}
		if !ok {
			return notDeployed
		}
	}
	return x.deployAccess(mgmt, op, s, rel, a)
}

// showAccessPlan prints what the build would do: the delegation policy (an existing one warned about), the roles, the
// accounts left out.
func (x *run) showAccessPlan(a Access) {
	x.say("\nPlan (organization access)")
	x.say("  delegation policy: %s", policyDetail(a.PolicyChange))
	if len(a.PolicyBase) > 0 { // one policy per organization (spec §9)
		x.say("  ! the delegation policy exists already: a Terraform configuration that writes it whole (the release's organization module) " +
			"removes the statement merged here at its next apply")
	}
	if len(a.Unreachable) > 0 && len(a.Ready)+len(a.Missing)+len(a.Foreign) == 0 {
		x.say("  roles: none, the member role reaches no account (the procedure for them: %s)", organizationProcedure)
	} else {
		x.say("  roles: %d updated, %d created in %s (%d discovery only)", len(a.Ready), len(a.Missing), accounts(len(a.Accounts())), len(a.DiscoveryOnly))
	}
	if len(a.Unreachable)+len(a.Foreign) > 0 {
		x.say("  left out: %s, %s", counted(a.Unreachable, "unreachable"), counted(a.Foreign, "another manager's"))
	}
}

// accounts is "1 account", "<n> accounts".
func accounts(n int) string {
	if n == 1 {
		return "1 account"
	}
	return fmt.Sprintf("%d accounts", n)
}

// counted is "<n> <what>", the ids after it when there are.
func counted(ids []string, what string) string {
	s := fmt.Sprintf("%d %s", len(ids), what)
	if len(ids) > 0 {
		s += " (" + strings.Join(ids, ", ") + ")"
	}
	return s
}

// policyDone says what the build did to the delegation policy (MergePolicy's change).
func policyDone(change string) string {
	switch change {
	case "new":
		return "created"
	case "unchanged":
		return "unchanged"
	}
	return "updated"
}

// orgRunning tells whether the management account's organization project exists (what a detached run left) and its
// build in progress ("" when none).
func orgRunning(ctx context.Context, mgmt Clients, op Project) (exists bool, id string, err error) {
	got, err := mgmt.CodeBuild.BatchGetProjects(ctx, &codebuild.BatchGetProjectsInput{Names: []string{op.Name}})
	if err != nil {
		return false, "", fmt.Errorf("check project %s: %w", op.Name, err)
	}
	if len(got.Projects) == 0 {
		return false, "", nil
	}
	id, err = Running(ctx, mgmt, op.Name)
	return true, id, err
}

// clearLeftover removes what a detached run left in the management account (the organization project and its role)
// once its build has ended, and says so; it returns the id of that build while it still runs (nothing removed).
func (x *run) clearLeftover(mgmt Clients, op Project) (running string, err error) {
	exists, id, err := orgRunning(x.ctx, mgmt, op)
	if err != nil || !exists || id != "" {
		return id, err
	}
	if err := OrgTeardown(x.ctx, mgmt, op); err != nil {
		return "", err
	}
	x.say("  removed the management account's build role and project left by a detached run")
	return "", nil
}

// orgFlags are the flags of organization access that install.json keeps.
var orgFlags = []string{"member-role", "accounts", "exclude-accounts", "discovery-only"}

// keepOrgFlags writes the organization flags given (s's) to install.json, so that a later run deploys with them: the
// explicit command and --access save no settings otherwise.
func (x *run) keepOrgFlags(s Settings) error {
	if !slices.ContainsFunc(orgFlags, func(f string) bool { return x.r.Set[f] }) {
		return nil
	}
	saved, err := ReadSettings(x.ctx, x.c, x.p)
	if err != nil || saved == nil {
		return accessErr(err, "", true)
	}
	saved.MemberRole, saved.Accounts, saved.ExcludeAccounts, saved.DiscoveryOnly = s.MemberRole, s.Accounts, s.ExcludeAccounts, s.DiscoveryOnly
	return accessErr(WriteSettings(context.WithoutCancel(x.ctx), x.c, x.p, *saved), "", true)
}

// deployAccess applies the plan a from the management account (op its project): the organization flags kept, its
// project bootstrapped, the plan written, the build started and followed, then the project and its role removed and
// the accounts' results said. A failure before the build removes what the bootstrap made; a detached build keeps them,
// for install status to remove once it ends.
func (x *run) deployAccess(mgmt Clients, op Project, s Settings, rel Release, a Access) error {
	if err := x.keepOrgFlags(s); err != nil {
		return err
	}
	id, err := x.startAccess(mgmt, op, s, rel, a)
	if err != nil {
		var running *RunningError
		if errors.As(err, &running) { // a detached run's build: its project and role are its own
			return err
		}
		return errors.Join(err, OrgTeardown(context.WithoutCancel(x.ctx), mgmt, op))
	}
	res, err := Follow(x.ctx, mgmt, op, id, x.followAccess(len(a.Accounts())))
	if err != nil {
		return x.lost(err, x.accessGoesOn())
	}
	failed, err := x.accessEnded(mgmt, op, res, policyDone(a.PolicyChange))
	if err == nil && failed {
		err = &stop{code: 1}
	}
	return err
}

// accessGoesOn is what a detached organization build leaves, and the command that ends it.
func (x *run) accessGoesOn() string {
	return "organization access goes on in CodeBuild. dormeto install status " + x.args() + " follows it and then removes its build role and project."
}

// startAccess bootstraps the management account's project, writes the plan (its accounts among it), gives the release (a local one uploaded to
// its bucket) and starts the build, with the roles' names the manager is given (--var discovery_role_name and
// command_role_name, their paths kept).
func (x *run) startAccess(mgmt Clients, op Project, s Settings, rel Release, a Access) (string, error) {
	role := memberRole(s)
	if err := OrgBootstrap(x.ctx, mgmt, op, role); err != nil {
		return "", err
	}
	x.say("\nBootstrap (API calls) in the management account %s: bucket %s, role %s, project %s", op.Account, op.Bucket, op.Role, op.Name)
	if err := PutOrgPlan(x.ctx, mgmt, op, a); err != nil {
		return "", err
	}
	base := rel.Base
	if rel.Local != "" {
		var err error
		if base, err = UploadRelease(x.ctx, mgmt, op, rel); err != nil {
			return "", err
		}
	}
	return Start(x.ctx, mgmt, op, ActionOrgApply, map[string]string{
		"MANAGER_ACCOUNT": x.w.Account, "ORGANIZATION": x.w.Organization.ID, "MEMBER_ROLE": role,
		"DISCOVERY_ROLE": roleVar(s, "discovery_role_name", "DmtDiscoveryRole"), "COMMAND_ROLE": roleVar(s, "command_role_name", "DmtCommandRole"),
		"RELEASE_BASE": base, "RELEASE_VERSION": rel.Version,
	})
}

// followAccess is how the organization build is shown: the accounts done out of total.
func (x *run) followAccess(total int) FollowOptions {
	o := x.follow("Organization access", total)
	o.Count = roleLineRe.MatchString
	return o
}

// accessEnded closes an organization build that ended (res): its project and role removed, then the accounts' results
// said, policy what it did to the delegation policy; failed when the build, an account or the removal did.
func (x *run) accessEnded(mgmt Clients, op Project, res Result, policy string) (failed bool, err error) {
	x.interrupted = "Interrupted: organization access ended; its results: s3://" + op.Bucket + "/" + op.Key("organization", "roles.json")
	failed = res.Failed
	if failed {
		x.failed("Organization access", res, "") //nolint:errcheck // said; the log group goes with the project
	}
	if err := OrgTeardown(context.WithoutCancel(x.ctx), mgmt, op); err != nil {
		x.say("  ! the management account's build role and project not removed: %s", err)
		failed = true
	} else {
		x.say("  the management account's build role and project removed")
	}
	roles, err := ReadRoles(x.ctx, mgmt, op)
	if err != nil {
		return failed, err
	}
	ok := 0
	var bad []string
	for _, r := range roles {
		if r.OK {
			ok++
		} else {
			bad = append(bad, r.Account+" ("+r.Error+")")
		}
	}
	if !res.Failed {
		x.say("  ✓ organization access: delegation policy %s; roles in %s", policy, accounts(ok))
	}
	if len(bad) > 0 {
		x.say("  ✗ roles failed in %s: %s", accounts(len(bad)), strings.Join(bad, ", "))
		failed = true
	}
	return failed, nil
}
