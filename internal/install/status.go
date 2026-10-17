package install

// `dormeto install status` (spec §8): where the install stands. A running build is followed first; then the manager's
// version and the latest release, the last plan and apply, the dashboard and the invitations, and organization access.
// It writes nothing, but for a detached organization run's project and role in the management account, removed once
// its build has ended (decision 17).

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
)

// buildLabels are the follow's label of each action of the manager's project.
var buildLabels = map[string]string{"plan": "Plan", "apply": "Apply", "plan-destroy": "Destroy plan", "apply-destroy": "Destroy"}

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
	builds, lastFailed, err := x.lastBuilds(saved.LastPlan, applied)
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
		if b.Environment == nil {
			continue
		}
		for _, v := range b.Environment.EnvironmentVariables {
			vars[aws.ToString(v.Name)] = aws.ToString(v.Value)
		}
	}
	return vars, nil
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

// lastBuilds is the line of the last plan and the last apply, each's status and start time (local), and whether the
// later of them failed.
func (x *run) lastBuilds(plan string, applied *Applied) (string, bool, error) {
	apply := ""
	if applied != nil {
		apply = applied.Build
	}
	var ids []string
	for _, id := range []string{plan, apply} {
		if id != "" {
			ids = append(ids, id)
		}
	}
	found := map[string]cbtypes.Build{}
	if len(ids) > 0 {
		got, err := x.c.CodeBuild.BatchGetBuilds(x.ctx, &codebuild.BatchGetBuildsInput{Ids: ids})
		if err != nil {
			return "", false, fmt.Errorf("get builds of %s: %w", x.p.Name, err)
		}
		for _, b := range got.Builds {
			found[aws.ToString(b.Id)] = b
		}
	}
	var last *cbtypes.Build
	for _, id := range ids {
		if b, ok := found[id]; ok && (last == nil || aws.ToTime(b.StartTime).After(aws.ToTime(last.StartTime))) {
			last = &b
		}
	}
	describe := func(kind, id string) string {
		b, ok := found[id]
		switch {
		case id == "":
			return "last " + kind + ": none"
		case !ok:
			return "last " + kind + " " + id + ": not found"
		}
		status := strings.ReplaceAll(strings.ToLower(string(b.BuildStatus)), "_", " ")
		return fmt.Sprintf("last %s %s: %s %s", kind, id, status, aws.ToTime(b.StartTime).Local().Format("2006-01-02 15:04"))
	}
	lastFailed := last != nil && last.BuildStatus != cbtypes.StatusTypeSucceeded && last.BuildStatus != cbtypes.StatusTypeInProgress
	return "  " + describe("plan", plan) + "; " + describe("apply", apply), lastFailed, nil
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
	_, _, cs, err := x.survey(s, rel)
	if err != nil {
		return failed, err
	}
	x.showSurvey(cs)
	return failed || Failed(cs), nil
}

// leftover removes what a detached organization run left in the management account (its project and role) once its
// build has ended, following that build first while it runs: whether it failed.
func (x *run) leftover(mgmt Clients) (bool, error) {
	op := OrgNames(x.w.Organization.Management, x.region, x.env)
	exists, id, err := orgRunning(x.ctx, mgmt, op)
	if err != nil || !exists {
		return false, err
	}
	if id == "" {
		if err := OrgTeardown(x.ctx, mgmt, op); err != nil {
			return false, err
		}
		x.say("  removed the management account's build role and project left by a detached run")
		return false, nil
	}
	vars, err := buildVars(x.ctx, mgmt, id)
	if err != nil {
		return false, err
	}
	x.say("Build %s is running (organization access)", id)
	res, err := Follow(x.ctx, mgmt, op, id, x.followAccess(len(strings.Fields(vars["ACCOUNTS"]))))
	if err != nil {
		return false, x.lost(err, x.accessGoesOn())
	}
	return x.accessEnded(mgmt, op, res, "put")
}
