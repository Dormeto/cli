package install

// `dormeto aws install status` (spec §8): where the install stands. A running build is followed first; then the manager's
// version and the latest release, the last plan and apply, the dashboard and the invitations, and organization access.
// It writes nothing, but for a detached organization run's project and role in the management account, removed once
// its build has ended (decision 17).

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
)

// maxLastBuilds is how many of the project's latest builds status reads for the last plan and apply (BatchGetBuilds
// takes 100 ids).
const maxLastBuilds = 100

// buildLabels are the follow's label of each action of the manager's project.
var buildLabels = map[string]string{"plan": "Plan", "apply": "Apply", "plan-destroy": "Destroy plan", "apply-destroy": "Destroy", "request": "Dashboard update"}

// Status is `dormeto aws install status`. Exit codes: 0; 1 when a check fails, the last build failed or AWS refuses; 130
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
	if line := tagsLine(saved.Tags); line != "" {
		x.say("%s", line)
	}
	builds, err := x.lastBuilds()
	if err != nil {
		return err
	}
	x.say("%s", builds.line())
	failed = failed || builds.lastFailed()
	if out != nil && !verified {
		cs := Verify(x.ctx, x.c, x.d.HTTP, *out, saved.AdminEmails, VerifyOptions{Wait: -1, Every: x.d.Every, Now: x.d.Now})
		x.sayChecks(cs...)
		failed = failed || Failed(cs)
	}
	accessFailed, err := x.accessStatus(x.merged(saved), saved, applied)
	if err != nil {
		return err
	}
	if err := x.ctx.Err(); err != nil {
		return err
	}
	if x.d.JSON != nil {
		v := map[string]any{"env": x.env, "account": x.w.Account, "region": x.region, "version": nil, "latest": nil, "upgradeAvailable": false,
			"tags": saved.Tags, "builds": builds.json(), "checks": checksJSON(x.reported), "ok": !failed && !accessFailed}
		if applied != nil {
			v["version"] = applied.Version
		}
		if latestErr == nil {
			v["latest"] = latest
			v["upgradeAvailable"] = applied != nil && CompareVersions(latest, applied.Version) > 0
		}
		if saved.Tags == nil {
			v["tags"] = map[string]string{}
		}
		if err := x.printJSON(v); err != nil {
			return err
		}
	}
	if failed || accessFailed {
		return &stop{code: 1}
	}
	return nil
}

// sayChecks says each check on its line, and keeps it for status's --json.
func (x *run) sayChecks(cs ...Check) {
	for _, c := range cs {
		x.say("%s", c.Line())
	}
	x.reported = append(x.reported, cs...)
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
		return false, false, x.lost(err, "the build goes on in CodeBuild. dormeto aws install status "+x.args()+" follows it again.")
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
	x.sayChecks(Verify(x.ctx, x.c, x.d.HTTP, *out, s.AdminEmails, VerifyOptions{Every: x.d.Every, Now: x.d.Now})...)
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
		return line + latest + ": an upgrade is available (dormeto aws install)"
	}
	return line + latest
}

// tagsLine is the install's tags, sorted by key; empty without tags.
func tagsLine(tags map[string]string) string {
	var l []string
	for _, k := range slices.Sorted(maps.Keys(tags)) {
		l = append(l, k+"="+tags[k])
	}
	if len(l) == 0 {
		return ""
	}
	return "  tags: " + strings.Join(l, ", ")
}

// lastBuildsOf are the project's last plan, last apply and last dashboard update: the newest of each kind (by their ACTION)
// among its latest builds (applied.json knows only the applies that succeeded); nil, none.
type lastBuildsOf struct{ plan, apply, request *cbtypes.Build }

// lastBuilds reads the project's last builds.
func (x *run) lastBuilds() (lastBuildsOf, error) {
	list, err := x.c.CodeBuild.ListBuildsForProject(x.ctx, &codebuild.ListBuildsForProjectInput{ProjectName: &x.p.Name, SortOrder: cbtypes.SortOrderTypeDescending})
	if err != nil {
		return lastBuildsOf{}, fmt.Errorf("list builds of %s: %w", x.p.Name, err)
	}
	var plan, apply, request *cbtypes.Build
	if ids := list.Ids[:min(len(list.Ids), maxLastBuilds)]; len(ids) > 0 {
		got, err := x.c.CodeBuild.BatchGetBuilds(x.ctx, &codebuild.BatchGetBuildsInput{Ids: ids})
		if err != nil {
			return lastBuildsOf{}, fmt.Errorf("get builds of %s: %w", x.p.Name, err)
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
	return lastBuildsOf{plan, apply, request}, nil
}

// buildStatus is a build's status in words: succeeded, in progress, failed.
func buildStatus(b *cbtypes.Build) string {
	return strings.ReplaceAll(strings.ToLower(string(b.BuildStatus)), "_", " ")
}

// line is the last builds' line, each's status and start time (local).
func (l lastBuildsOf) line() string {
	describe := func(kind string, b *cbtypes.Build) string {
		if b == nil {
			return "last " + kind + ": none"
		}
		return fmt.Sprintf("last %s %s: %s %s", kind, aws.ToString(b.Id), buildStatus(b), aws.ToTime(b.StartTime).Local().Format("2006-01-02 15:04"))
	}
	line := "  " + describe("plan", l.plan) + "; " + describe("apply", l.apply)
	if l.request != nil { // only when the dashboard made one
		line += "; " + describe("dashboard update", l.request)
	}
	return line
}

// lastFailed tells whether the latest of the last builds failed.
func (l lastBuildsOf) lastFailed() bool {
	var last *cbtypes.Build
	for _, b := range []*cbtypes.Build{l.plan, l.apply, l.request} {
		if b != nil && (last == nil || aws.ToTime(b.StartTime).After(aws.ToTime(last.StartTime))) {
			last = b
		}
	}
	return last != nil && last.BuildStatus != cbtypes.StatusTypeSucceeded && last.BuildStatus != cbtypes.StatusTypeInProgress
}

// json is the last builds as --json prints them: each {id, status, startedAt} (RFC 3339), null when none.
func (l lastBuildsOf) json() map[string]any {
	of := func(b *cbtypes.Build) any {
		if b == nil {
			return nil
		}
		return map[string]string{"id": aws.ToString(b.Id), "status": buildStatus(b), "startedAt": aws.ToTime(b.StartTime).UTC().Format(time.RFC3339)}
	}
	return map[string]any{"plan": of(l.plan), "apply": of(l.apply), "dashboardUpdate": of(l.request)}
}

// accessStatus says where organization access stands: not used in an account deployment; with a management profile, a
// detached run's leftovers removed, then the survey; without, where it is checked. It tells whether a check or a
// followed organization build failed.
func (x *run) accessStatus(s Settings, saved *Settings, applied *Applied) (bool, error) {
	if s.Mode != "organization" {
		x.sayChecks(Check{"organization access", "not used (account deployment)", Skip})
		return false, nil
	}
	profile, ok := x.management(s)
	if !ok {
		x.sayChecks(Check{"organization access", "checked by the manager at each discovery run (dashboard: Discovery, Organization access); " +
			"dormeto aws install check --management-profile <profile> " + x.args() + " checks it here", Skip})
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
