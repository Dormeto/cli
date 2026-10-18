package install

// `dormeto install status` (spec §8): where the install stands. A running build is followed first; then the manager's
// version and the latest release, the last plan and apply, the dashboard and the invitations, and organization access.
// It writes nothing, but for a detached organization run's project and role in the management account, removed once
// its build has ended (decision 17).

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
)

// maxLastBuilds is how many of the project's latest builds status reads for the last plan and apply (BatchGetBuilds
// takes 100 ids).
const maxLastBuilds = 100

// buildLabels are the follow's label of each action of the manager's project.
var buildLabels = map[string]string{"plan": "Plan", "apply": "Apply", "plan-destroy": "Destroy plan", "apply-destroy": "Destroy", "request": "Dashboard update"}

// Status is `dormeto install status`. Exit codes: 0; 1 when a check fails, the last build failed or AWS refuses; 130
// when ctx is cancelled (a followed build goes on).
func Status(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	x.interrupted = "Interrupted."
	return x.end(x.status())
}

func (x *run) status() error {
	if err := x.locate(); err != nil {
		return err
	}
	saved, err := x.environment(false)
	if err != nil {
		return err
	}
	if saved == nil {
		return &stop{1, fmt.Sprintf("no install of env %s in %s %s", x.env, x.w.Account, x.region)}
	}
	failed, verified, err := x.followRunning(*saved)
	if err != nil {
		return err
	}
	applied, err := ReadApplied(x.ctx, x.c, x.p)
	if err != nil {
		return err
	}
	out, err := ReadOutputs(x.ctx, x.c, x.p)
	if err != nil {
		return err
	}
	latest, latestErr := Latest(x.ctx, x.d.HTTP)

	x.say("Dormeto %s in %s %s", x.env, x.w.Account, x.region)
	x.say("%s", versionsLine(applied, latest, latestErr))
	builds, lastFailed, err := x.lastBuilds()
	if err != nil {
		return err
	}
	x.say("%s", builds)
	failed = failed || lastFailed
	if out != nil && !verified {
		cs := Verify(x.ctx, x.c, x.d.HTTP, *out, saved.AdminEmails, VerifyOptions{Wait: -1, Every: x.d.Every, Now: x.d.Now})
		for _, c := range cs {
			x.say("%s", c.Line())
		}
		failed = failed || Failed(cs)
	}
	accessFailed, err := x.accessStatus(x.merged(saved), saved, applied)
	if err != nil {
		return err
	}
	if err := x.ctx.Err(); err != nil {
		return err
	}
	if failed || accessFailed {
		return &stop{code: 1}
	}
	return nil
}

// followRunning follows the manager's build in progress, if any, and after an apply checks the dashboard and the
// invitations (s's administrators): whether the build failed, and whether they were checked.
func (x *run) followRunning(s Settings) (failed, verified bool, err error) {
	id, err := Running(x.ctx, x.c, x.p.Name)
	if err != nil || id == "" {
		return false, false, err
	}
	vars, err := buildVars(x.ctx, x.c, id)
	if err != nil {
		return false, false, err
	}
	action := vars["ACTION"]
	x.say("Build %s is running (%s)", id, action)
	label := buildLabels[action]
	if label == "" {
		label = "Build"
	}
	total := 0
	if plan := vars["PLAN_BUILD"]; plan != "" {
		if sum, err := x.summary(plan); err == nil {
			total = sum.Add + sum.Change + sum.Destroy
		}
	}
	res, err := Follow(x.ctx, x.c, x.p, id, x.follow(label, total))
	if err != nil {
		return false, false, x.lost(err, "the build goes on in CodeBuild. dormeto install status "+x.args()+" follows it again.")
	}
	if res.Failed {
		x.failed(label, res, x.p.LogGroup) //nolint:errcheck // said; status goes on
		return true, false, nil
	}
	if action != string(ActionApply) {
		return false, false, nil
	}
	out, err := ReadOutputs(x.ctx, x.c, x.p)
	if err != nil || out == nil {
		return false, false, err
	}
	for _, c := range Verify(x.ctx, x.c, x.d.HTTP, *out, s.AdminEmails, VerifyOptions{Every: x.d.Every, Now: x.d.Now}) {
		x.say("%s", c.Line())
	}
	return false, true, x.ctx.Err()
}

// buildVars are a build's environment variables, its overrides among them.
func buildVars(ctx context.Context, c Clients, id string) (map[string]string, error) {
	got, err := c.CodeBuild.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: []string{id}})
	if err != nil {
		return nil, fmt.Errorf("get build %s: %w", id, err)
	}
	vars := map[string]string{}
	for _, b := range got.Builds {
		maps.Copy(vars, envOf(b))
	}
	return vars, nil
}

// envOf is a build's environment variables.
func envOf(b cbtypes.Build) map[string]string {
	vars := map[string]string{}
	if b.Environment != nil {
		for _, v := range b.Environment.EnvironmentVariables {
			vars[aws.ToString(v.Name)] = aws.ToString(v.Value)
		}
	}
	return vars
}

// versionsLine is the manager's version applied and the latest release (its error when unknown), an upgrade said.
func versionsLine(applied *Applied, latest string, latestErr error) string {
	installed := "not applied yet"
	if applied != nil {
		installed = applied.Version
	}
	line := "  manager " + installed + "; latest release "
	switch {
	case latestErr != nil:
		return line + "unknown (" + latestErr.Error() + ")"
	case applied != nil && CompareVersions(latest, applied.Version) > 0:
		return line + latest + ": an upgrade is available (dormeto install)"
	}
	return line + latest
}

// lastBuilds is the line of the project's last plan and last apply, each's status and start time (local), and whether
// the later of them failed. They are the newest of each kind (by their ACTION) among its latest builds: applied.json
// knows only the applies that succeeded.
func (x *run) lastBuilds() (string, bool, error) {
	list, err := x.c.CodeBuild.ListBuildsForProject(x.ctx, &codebuild.ListBuildsForProjectInput{ProjectName: &x.p.Name, SortOrder: cbtypes.SortOrderTypeDescending})
	if err != nil {
		return "", false, fmt.Errorf("list builds of %s: %w", x.p.Name, err)
	}
	var plan, apply, request *cbtypes.Build
	if ids := list.Ids[:min(len(list.Ids), maxLastBuilds)]; len(ids) > 0 {
		got, err := x.c.CodeBuild.BatchGetBuilds(x.ctx, &codebuild.BatchGetBuildsInput{Ids: ids})
		if err != nil {
			return "", false, fmt.Errorf("get builds of %s: %w", x.p.Name, err)
		}
		found := map[string]cbtypes.Build{}
		for _, b := range got.Builds {
			found[aws.ToString(b.Id)] = b
		}
		for _, id := range ids { // newest first
			b, ok := found[id]
			if !ok {
				continue
			}
			switch envOf(b)["ACTION"] {
			case string(ActionPlan), string(ActionPlanDestroy):
				if plan == nil {
					plan = &b
				}
			case "request":
				if request == nil {
					request = &b
				}
			case string(ActionApply), string(ActionApplyDestroy):
				if apply == nil {
					apply = &b
				}
			}
		}
	}
	describe := func(kind string, b *cbtypes.Build) string {
		if b == nil {
			return "last " + kind + ": none"
		}
		status := strings.ReplaceAll(strings.ToLower(string(b.BuildStatus)), "_", " ")
		return fmt.Sprintf("last %s %s: %s %s", kind, aws.ToString(b.Id), status, aws.ToTime(b.StartTime).Local().Format("2006-01-02 15:04"))
	}
	last := plan
	if last == nil || (apply != nil && aws.ToTime(apply.StartTime).After(aws.ToTime(plan.StartTime))) {
		last = apply
	}
	lastFailed := last != nil && last.BuildStatus != cbtypes.StatusTypeSucceeded && last.BuildStatus != cbtypes.StatusTypeInProgress
	line := "  " + describe("plan", plan) + "; " + describe("apply", apply)
	if request != nil { // only when the dashboard made one
		line += "; " + describe("dashboard update", request)
	}
	return line, lastFailed, nil
}

// accessStatus says where organization access stands: not used in an account deployment; with a management profile, a
// detached run's leftovers removed, then the survey; without, where it is checked. It tells whether a check or a
// followed organization build failed.
func (x *run) accessStatus(s Settings, saved *Settings, applied *Applied) (bool, error) {
	if s.Mode != "organization" {
		x.say("%s", Check{"organization access", "not used (account deployment)", Skip}.Line())
		return false, nil
	}
	profile, ok := x.management(s)
	if !ok {
		x.say("%s", Check{"organization access", "checked by the manager at each discovery run (dashboard: Discovery, Organization access); " +
			"dormeto install check --management-profile <profile> " + x.args() + " checks it here", Skip}.Line())
		return false, nil
	}
	mgmt, err := x.managementClients(profile)
	if err != nil {
		return false, err
	}
	failed, err := x.leftover(mgmt)
	if err != nil {
		return failed, err
	}
	rel, err := x.resolve(installedVersion(saved, applied))
	if err != nil {
		return failed, err
	}
	sv, err := x.survey(s, rel)
	if err != nil {
		return failed, err
	}
	x.showSurvey(sv.cs)
	return failed || Failed(sv.cs), nil
}

// leftover removes what a detached organization run left in the management account (its project and role) once its
// build has ended, following that build first while it runs: whether it failed.
func (x *run) leftover(mgmt Clients) (bool, error) {
	op := OrgNames(x.w.Organization.Management, x.region, x.env)
	id, err := x.clearLeftover(mgmt, op)
	if err != nil || id == "" {
		return false, err
	}
	accounts, err := ReadOrgAccounts(x.ctx, mgmt, op)
	if err != nil {
		return false, err
	}
	x.say("Build %s is running (organization access)", id)
	res, err := Follow(x.ctx, mgmt, op, id, x.followAccess(len(accounts)))
	if err != nil {
		return false, x.lostAccess(err)
	}
	return x.accessEnded(mgmt, op, res, "put")
}
