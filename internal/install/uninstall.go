package install

// `dormeto uninstall` and `dormeto uninstall apply` (spec §10, in the website's order): the routers first (the
// manager installed them by API calls, outside Terraform: refused while any), event-driven refresh off, the deletion
// protection lifted by a plan applied first, the destroy plan confirmed then applied in CodeBuild, the bootstrap
// removed when asked; organization access kept (its roles serve a reinstall), its procedure printed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
)

// routerPrefix begins the names of the routers' functions (dmtr-<env>).
const routerPrefix = "dmtr-"

// Uninstall is `dormeto uninstall`: where it runs; with the manager applied, the routers (refused while any),
// event-driven refresh off (asked in a terminal), the deletion protection lifted (a plan, confirmed in a terminal,
// applied), the destroy plan, its summary, the person's confirmation in a terminal, then the plan applied and followed;
// then the bootstrap removed when the person asks. Without a terminal it stops after a plan with the command that
// applies it. Exit codes: 0; 1 when routers are installed, event-driven refresh is not off, AWS refuses or a build
// fails; 2 for a bad flag; 130 when ctx is cancelled (Ctrl-C).
func Uninstall(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	x.interrupted = "Interrupted: nothing was removed."
	return x.end(x.uninstall())
}

func (x *run) uninstall() error {
	saved, applied, resources, err := x.installed()
	if err != nil {
		return err
	}
	s := *saved
	if resources {
		if err := x.removeManager(s, applied); err != nil {
			return err
		}
	}
	kept, err := x.removeBootstrap(s, true)
	if err != nil {
		return err
	}
	x.accessKept(s, kept)
	return nil
}

// UninstallApply is `dormeto uninstall apply`: the explicit confirmation of the last plan when it is a destroy plan
// (made without a terminal, or left at the question), shown, then applied and followed; the bootstrap kept. Exit codes
// as Uninstall's.
func UninstallApply(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	x.interrupted = "Interrupted: nothing was applied."
	return x.end(x.uninstallApply())
}

func (x *run) uninstallApply() error {
	saved, applied, resources, err := x.installed()
	if err != nil {
		return err
	}
	s := *saved
	switch {
	case s.LastPlan == "":
		return &stop{1, "no destroy plan: run dormeto uninstall"}
	case s.LastPlanKind != planDestroy:
		return &stop{1, "the last plan is not a destroy plan: dormeto uninstall"}
	case !resources: // destroyed already (by a run that detached from it)
		return &stop{1, "nothing applied to destroy: dormeto uninstall " + x.args() + " removes the bootstrap"}
	}
	if err := x.noRouters(false); err != nil {
		return err
	}
	if err := x.checkDestroyPlan(s, applied); err != nil {
		return err
	}
	sum, err := x.summary(s.LastPlan)
	if err != nil {
		return err
	}
	x.showPlan(s.LastPlan, s.Version, sum)
	if err := x.applyDestroy(s, sum); err != nil {
		return err
	}
	kept, err := x.removeBootstrap(s, false)
	if err != nil {
		return err
	}
	x.accessKept(s, kept)
	return nil
}

// installed locates the run and reads the env's install.json (there must be one), its applied.json (nil: none) and
// whether its Terraform state holds resources: whether anything is applied (ruling R29).
func (x *run) installed() (*Settings, *Applied, bool, error) {
	if err := x.locate(); err != nil {
		return nil, nil, false, err
	}
	saved, err := x.environment(false)
	if err != nil {
		return nil, nil, false, err
	}
	if saved == nil {
		return nil, nil, false, &stop{1, fmt.Sprintf("no install of env %s in %s %s", x.env, x.w.Account, x.region)}
	}
	applied, err := ReadApplied(x.ctx, x.c, x.p)
	if err != nil {
		return nil, nil, false, err
	}
	resources, err := StateHasResources(x.ctx, x.c, x.p)
	return saved, applied, resources, err
}

// removeManager destroys the manager applied (s its settings, applied its applied.json, nil when none) with the release
// installed: refused while routers are installed or event-driven refresh is not off; the deletion protection lifted
// first; then the destroy plan, shown, confirmed in a terminal and applied. Without a terminal it stops after a plan, with the command that
// applies it.
func (x *run) removeManager(s Settings, applied *Applied) error {
	if err := x.installedRelease(&s, applied); err != nil {
		return err
	}
	if err := x.noRouters(true); err != nil {
		return err
	}
	if err := x.eventDriven(); err != nil {
		return err
	}
	protected, err := x.protected(s, applied)
	if err != nil {
		return err
	}
	if protected {
		if err := x.liftProtection(&s, applied); err != nil {
			return err
		}
		if applied, err = ReadApplied(x.ctx, x.c, x.p); err != nil {
			return err
		}
	}
	sum, err := x.planDestroy(&s)
	if err != nil {
		return err
	}
	if !x.d.Terminal {
		return &stop{0, "Plan saved. Apply it: dormeto uninstall apply " + x.args()}
	}
	ok, err := x.confirm("Destroy the manager? This deletes its users, its table and its history.")
	if err != nil {
		return err
	}
	if !ok {
		return &stop{0, x.interrupted} // the destroy plan kept, as a Ctrl-C now leaves it
	}
	if err := x.checkDestroyPlan(s, applied); err != nil {
		return err
	}
	return x.applyDestroy(s, sum)
}

// installedRelease makes s's version and release those of the manager installed, which the protection and destroy plans
// run with: install.json's are the last plan's (a declined upgrade's). applied.json's version, with install.json's
// release when it is the same version, else that version's on releases.dormeto.com, or the local release given
// (--release) of that version, uploaded to the install's bucket; without applied.json (a first apply that failed),
// install.json's.
func (x *run) installedRelease(s *Settings, applied *Applied) error {
	if applied == nil || applied.Version == "" || applied.Version == s.Version {
		return nil
	}
	installed := applied.Version
	if x.r.Release == "" {
		rel, err := Resolve(x.ctx, x.d.HTTP, installed, "")
		if err != nil {
			why := err.Error()
			var np *NotPublishedError
			if errors.As(err, &np) {
				why = np.Short()
			}
			return &stop{1, fmt.Sprintf("manager %s is installed, install.json names %s (a later plan's): %s; give its local release: dormeto uninstall %s --release <the directory of release %s>",
				installed, s.Version, why, x.args(), installed)}
		}
		s.Version, s.ReleaseBase = rel.Version, rel.Base
		return nil
	}
	rel, err := Resolve(x.ctx, x.d.HTTP, "", x.r.Release)
	if err != nil {
		return err
	}
	if rel.Version != installed {
		return &stop{1, fmt.Sprintf("manager %s is installed: --release %s is release %s", installed, x.r.Release, rel.Version)}
	}
	base, err := UploadRelease(x.ctx, x.c, x.p, rel)
	if err != nil {
		return err
	}
	s.Version, s.ReleaseBase = rel.Version, base
	return nil
}

// noRouters refuses while routers are installed, or while the manager's own region could not be checked (ruling
// R30). Other regions not checked are said; when ask, in a terminal and without --yes, the person decides whether to
// go on.
func (x *run) noRouters(ask bool) error {
	where, unchecked, err := x.routers()
	switch {
	case err != nil:
		return err
	case len(where) > 0:
		return &stop{1, "Routers are installed (" + strings.Join(where, "; ") + "): offboard the workloads and uninstall the routers in the dashboard (Routers) first."}
	case slices.Contains(unchecked, x.region):
		return &stop{1, "Routers not checked in " + x.region + ", the manager's region (lambda:ListFunctions refused): nothing was removed."}
	case len(unchecked) == 0:
		x.say("Routers: none")
		return nil
	}
	regions := strings.Join(unchecked, ", ")
	count := fmt.Sprintf("%d regions", len(unchecked))
	if len(unchecked) == 1 {
		count = "1 region"
	}
	x.say("Routers: none found (%s not checked: %s)", count, regions)
	if !ask || !x.d.Terminal || x.r.Yes {
		return nil
	}
	ok, err := x.confirm("Continue?")
	if err != nil {
		return err
	}
	if !ok {
		return &stop{1, "Nothing was removed: look for dmtr- functions in " + regions + ", then run dormeto uninstall " + x.args() + " again."}
	}
	return nil
}

// routers says where routers are installed, if anywhere: the router items of the manager's table (none when the
// table does not exist), and the regions enabled in the account that hold a dmtr- function; and the regions whose
// functions could not be listed (lambda:ListFunctions refused), sorted.
func (x *run) routers() (where, unchecked []string, err error) {
	table := "dmt-" + x.env + "-manager"
	items, err := x.c.DynamoDB.Query(x.ctx, &dynamodb.QueryInput{
		TableName:              aws.String(table),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":pk": &ddbtypes.AttributeValueMemberS{Value: "T#default"}, ":prefix": &ddbtypes.AttributeValueMemberS{Value: "ROUTER#"},
		},
		Limit: aws.Int32(1),
	})
	switch {
	case isNotFound(err, "ResourceNotFoundException"):
	case err != nil:
		return nil, nil, fmt.Errorf("query table %s: %w", table, err)
	case len(items.Items) > 0:
		where = append(where, "table "+table)
	}
	regions, err := x.c.EC2.DescribeRegions(x.ctx, &ec2.DescribeRegionsInput{AllRegions: aws.Bool(true)})
	if err != nil {
		return nil, nil, fmt.Errorf("list regions: %w", err)
	}
	var found []string
	for _, r := range regions.Regions {
		if status := aws.ToString(r.OptInStatus); status != "opt-in-not-required" && status != "opted-in" {
			continue
		}
		region := aws.ToString(r.RegionName)
		c, err := x.d.Clients(x.ctx, x.r.Profile, region)
		if err != nil {
			return nil, nil, err
		}
		has, err := routerFunctions(x.ctx, c)
		switch {
		case isDenied(err):
			unchecked = append(unchecked, region)
		case err != nil:
			return nil, nil, fmt.Errorf("list functions in %s: %w", region, err)
		case has:
			found = append(found, region)
		}
	}
	if len(found) > 0 {
		slices.Sort(found)
		where = append(where, routerPrefix+" functions in "+strings.Join(found, ", "))
	}
	slices.Sort(unchecked)
	return where, unchecked, nil
}

// routerFunctions tells whether a region's functions (c its clients) hold a router's, read a page at a time.
func routerFunctions(ctx context.Context, c Clients) (bool, error) {
	in := &lambda.ListFunctionsInput{}
	for {
		out, err := c.Lambda.ListFunctions(ctx, in)
		if err != nil {
			return false, err
		}
		for _, f := range out.Functions {
			if strings.HasPrefix(aws.ToString(f.FunctionName), routerPrefix) {
				return true, nil
			}
		}
		if out.NextMarker == nil {
			return false, nil
		}
		in = &lambda.ListFunctionsInput{Marker: out.NextMarker}
	}
}

// eventDriven has the person confirm in a terminal that event-driven refresh is off, with a discovery run since: its
// forwarders in member accounts are removed only by a discovery run. --yes answers yes; without a terminal, as with
// --yes, it is said.
func (x *run) eventDriven() error {
	if !x.d.Terminal || x.r.Yes {
		x.say("! Event-driven refresh must be off (Discovery, Perimeter settings), with a discovery run since: its forwarders in member accounts are removed only by a run.")
		return nil
	}
	ok, err := x.confirm("Is event-driven refresh off (Discovery, Perimeter settings), with a discovery run since? Its forwarders in member accounts are removed only by a run.")
	if err != nil {
		return err
	}
	if !ok {
		return &stop{1, "Nothing was removed: turn event-driven refresh off in the dashboard (Discovery, Perimeter settings), run discovery, then dormeto uninstall " +
			x.args() + " again."}
	}
	return nil
}

// protected tells whether the deletion protection must be lifted before the destroy. It is lifted only when
// install.json sets deletion_protection to false and either the last plan is a destroy plan (made once it was lifted)
// or an apply came after the last plan (CheckPlan's *StalePlanError: the plan made with false was applied, or a later
// one was). No last plan (the one with false never started), a plan pending, failed or unknown: protected, and one more
// plan lifts it (it goes on when it changes nothing). AWS's errors are returned.
func (x *run) protected(s Settings, applied *Applied) (bool, error) {
	var on bool
	if raw, ok := s.Vars["deletion_protection"]; !ok || json.Unmarshal(raw, &on) != nil || on {
		return true, nil
	}
	switch {
	case s.LastPlanKind == planDestroy:
		return false, nil
	case s.LastPlan == "":
		return true, nil
	}
	err := CheckPlan(x.ctx, x.c, s.LastPlan, applied)
	var stale *StalePlanError
	var unusable *UnusablePlanError
	switch {
	case errors.As(err, &stale):
		return false, nil
	case err == nil, errors.As(err, &unusable):
		return true, nil
	}
	return false, err
}

// liftProtection writes deletion_protection false in the settings s and the configuration, plans it and, once the
// person confirms it in a terminal, applies it; a plan with nothing to change is not applied. Without a terminal it
// stops after the plan, with the commands that apply it and then uninstall.
func (x *run) liftProtection(s *Settings, applied *Applied) error {
	x.say("\nDeletion protection is on: a plan lifts it first.")
	s.Vars = maps.Clone(s.Vars)
	if s.Vars == nil {
		s.Vars = map[string]json.RawMessage{}
	}
	s.Vars["deletion_protection"] = json.RawMessage("false")
	cfg, err := x.valid(*s)
	if err != nil {
		return err
	}
	then := ", then dormeto uninstall " + x.args() + " again"
	x.interrupted = "Interrupted before the plan started: run dormeto uninstall " + x.args() + " again."
	id, sum, err := x.plan(s, cfg, then)
	if err != nil {
		return err
	}
	if sum.Empty() {
		x.say("Nothing to change: the deletion protection is lifted.")
		return nil
	}
	x.showPlan(id, s.Version, sum)
	if !x.d.Terminal {
		return &stop{0, "Plan saved. Apply it: dormeto install apply " + x.args() + ", then run dormeto uninstall " + x.args() + " again"}
	}
	// The plan kept, as a Ctrl-C now leaves it, and install.json with the protection off.
	x.interrupted = "Not applied; the plan is kept: dormeto install apply " + x.args() + then + ". install.json now has deletion_protection=false: " +
		"dormeto install " + x.args() + " --var deletion_protection=true puts it back."
	ok, err := x.confirm("Lift the deletion protection?")
	if err != nil {
		return err
	}
	if !ok {
		return &stop{0, x.interrupted}
	}
	if err := CheckPlan(x.ctx, x.c, id, applied); err != nil {
		return err
	}
	if err := x.applyInstall(*s, sum, then); err != nil {
		return err
	}
	x.interrupted = "Interrupted: the deletion protection is lifted, nothing is destroyed: run dormeto uninstall " + x.args() + " again."
	return nil
}

// planDestroy makes the destroy plan of the settings s (startPlan: a destroy plan, for uninstall apply), then shows its
// summary.
func (x *run) planDestroy(s *Settings) (PlanSummary, error) {
	x.interrupted = "Interrupted before the destroy plan started: run dormeto uninstall " + x.args() + " again."
	id, sum, err := x.startPlan(s, ActionPlanDestroy, planDestroy, "Destroy plan", "dormeto uninstall apply "+x.args())
	if err != nil {
		return PlanSummary{}, err
	}
	x.showPlan(id, s.Version, sum)
	return sum, nil
}

// checkDestroyPlan refuses the saved destroy plan s.LastPlan as CheckPlan does (applied the last apply, nil when none),
// saying what to run.
func (x *run) checkDestroyPlan(s Settings, applied *Applied) error {
	x.interrupted = "Not applied; the destroy plan is kept: dormeto uninstall apply " + x.args()
	err := CheckPlan(x.ctx, x.c, s.LastPlan, applied)
	again := ": run dormeto uninstall " + x.args()
	var unusable *UnusablePlanError
	var stale *StalePlanError
	switch {
	case errors.As(err, &unusable) && unusable.Status == "":
		return &stop{1, "no destroy plan " + unusable.Plan + again}
	case errors.As(err, &unusable):
		return &stop{1, "the destroy plan " + unusable.Plan + " did not succeed (" + unusable.Status + ")" + again}
	case errors.As(err, &stale):
		return &stop{1, "the destroy plan " + stale.Plan + " predates the last apply " + stale.Apply + again}
	}
	return err
}

// applyDestroy applies the saved destroy plan s.LastPlan (sum its summary; checkDestroyPlan accepted it) with s's
// release and follows it; once it succeeded, install.json forgets the plan (the build removed applied.json).
func (x *run) applyDestroy(s Settings, sum PlanSummary) error {
	x.interrupted = "Not applied; the destroy plan is kept: dormeto uninstall apply " + x.args()
	if err := x.applyPlan(s, sum, ActionApplyDestroy, "Destroy", "the destroy goes on in CodeBuild. Follow it: dormeto install status "+x.args()+
		"; once done, dormeto uninstall "+x.args()+" removes the bootstrap."); err != nil {
		return err
	}
	x.interrupted = "Interrupted: the manager is destroyed; dormeto uninstall " + x.args() + " removes the bootstrap."
	x.say("  ✓ destroyed: %d resources", sum.Destroy)
	s.LastPlan, s.LastPlanKind = "", ""
	return WriteSettings(context.WithoutCancel(x.ctx), x.c, x.p, s)
}

// removeBootstrap removes the install's bucket (its state and answers), role, log group and project once the person
// asks (ask: in a terminal, without --yes, whose answer is no); otherwise they are kept (kept), and how to remove them
// is said. Once started, the removal is not cancelled: a bucket left half emptied would have lost install.json, which
// names the install to remove.
func (x *run) removeBootstrap(s Settings, ask bool) (kept bool, err error) {
	if ask && x.d.Terminal && !x.r.Yes {
		ok, err := x.confirm("Remove the install's bucket (its state and answers), role, log group and project too?")
		if err != nil {
			return true, err
		}
		if ok {
			if err := Teardown(context.WithoutCancel(x.ctx), x.c, x.p, s.BuildRole, true); err != nil {
				return true, err
			}
			role := ", the role " + x.p.Role
			if s.BuildRole != "" { // the role given is not the install's to remove
				role = ""
			}
			x.say("Removed the bucket %s%s, the log group %s and the project %s.", x.p.Bucket, role, x.p.LogGroup, x.p.Name)
			return false, nil
		}
	}
	x.say("Kept the bucket %s (its answers serve a reinstall) and the project; dormeto uninstall %s again in a terminal removes them.", x.p.Bucket, x.args())
	return true, nil
}

// accessKept says, in organization mode, that organization access is not removed, and where its removal is described;
// while the bucket is kept (install status reads install.json), the command that removes what a detached organization
// access run left in the management account (its build role and project).
func (x *run) accessKept(s Settings, bucketKept bool) {
	if s.Mode != "organization" {
		return
	}
	x.say("Organization access is not removed: its roles keep working for a reinstall. To remove them and Dormeto's statement of the delegation policy: %s",
		organizationProcedure)
	if bucketKept {
		x.say("A detached organization access run's build role and project in the management account: dormeto install status --management-profile <profile> %s removes them.",
			x.args())
	}
}
