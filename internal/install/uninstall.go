package install

// `dormeto uninstall` and `dormeto uninstall apply` (spec §10, in the website's order): the routers first (the
// manager installed them by API calls, outside Terraform: refused while any), event-driven refresh off, the deletion
// protection lifted by a plan applied first, the destroy plan confirmed then applied in CodeBuild, the bootstrap
// removed when asked; organization access kept (its roles serve a reinstall), its procedure printed.

import (
	"context"
	"encoding/json"
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
	saved, applied, err := x.installed()
	if err != nil {
		return err
	}
	s := *saved
	if applied != nil {
		if err := x.removeManager(s, applied); err != nil {
			return err
		}
	}
	if err := x.removeBootstrap(s, true); err != nil {
		return err
	}
	x.accessKept(s)
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
	saved, applied, err := x.installed()
	if err != nil {
		return err
	}
	s := *saved
	switch {
	case s.LastPlan == "":
		return &stop{1, "no destroy plan: run dormeto uninstall"}
	case s.LastPlanKind != planDestroy:
		return &stop{1, "the last plan is not a destroy plan: dormeto uninstall"}
	case applied == nil: // destroyed already (by a run that detached from it)
		return &stop{1, "nothing applied to destroy: dormeto uninstall " + x.args() + " removes the bootstrap"}
	}
	sum, err := x.summary(s.LastPlan)
	if err != nil {
		return err
	}
	x.showPlan(s.LastPlan, s.Version, sum)
	if err := x.applyDestroy(s, applied, sum); err != nil {
		return err
	}
	if err := x.removeBootstrap(s, false); err != nil {
		return err
	}
	x.accessKept(s)
	return nil
}

// installed locates the run and reads the env's install.json and applied.json (nil: nothing applied); there must be an
// install.json.
func (x *run) installed() (*Settings, *Applied, error) {
	if err := x.locate(); err != nil {
		return nil, nil, err
	}
	saved, err := x.environment(false)
	if err != nil {
		return nil, nil, err
	}
	if saved == nil {
		return nil, nil, &stop{1, fmt.Sprintf("no install of env %s in %s %s", x.env, x.w.Account, x.region)}
	}
	applied, err := ReadApplied(x.ctx, x.c, x.p)
	return saved, applied, err
}

// removeManager destroys the manager applied (s its settings): refused while routers are installed or event-driven
// refresh is not off; the deletion protection lifted first; then the destroy plan, shown, confirmed in a terminal and
// applied. Without a terminal it stops after a plan, with the command that applies it.
func (x *run) removeManager(s Settings, applied *Applied) error {
	where, err := x.routers()
	if err != nil {
		return err
	}
	if len(where) > 0 {
		return &stop{1, "Routers are installed (" + strings.Join(where, "; ") + "): offboard the workloads and uninstall the routers in the dashboard (Routers) first."}
	}
	x.say("Routers: none")
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
	return x.applyDestroy(s, applied, sum)
}

// routers says where routers are installed, if anywhere: the router items of the manager's table (none when the
// table does not exist), and the regions enabled in the account that hold a dmtr- function. A region whose functions
// cannot be listed is said, not counted.
func (x *run) routers() ([]string, error) {
	var where []string
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
		return nil, fmt.Errorf("query table %s: %w", table, err)
	case len(items.Items) > 0:
		where = append(where, "table "+table)
	}
	regions, err := x.c.EC2.DescribeRegions(x.ctx, &ec2.DescribeRegionsInput{AllRegions: aws.Bool(true)})
	if err != nil {
		return nil, fmt.Errorf("list regions: %w", err)
	}
	var found []string
	for _, r := range regions.Regions {
		if status := aws.ToString(r.OptInStatus); status != "opt-in-not-required" && status != "opted-in" {
			continue
		}
		region := aws.ToString(r.RegionName)
		c, err := x.d.Clients(x.ctx, x.r.Profile, region)
		if err != nil {
			return nil, err
		}
		has, err := routerFunctions(x.ctx, c)
		switch {
		case isDenied(err):
			x.say("  ! routers in %s: not checked (lambda:ListFunctions refused)", region)
		case err != nil:
			return nil, fmt.Errorf("list functions in %s: %w", region, err)
		case has:
			found = append(found, region)
		}
	}
	if len(found) > 0 {
		slices.Sort(found)
		where = append(where, routerPrefix+" functions in "+strings.Join(found, ", "))
	}
	return where, nil
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

// protected tells whether the deletion protection must be lifted before the destroy: install.json does not set
// deletion_protection to false, or its last plan, an install plan, is not applied yet (the protection lifted in
// install.json only, its plan then left to install apply). A last plan CheckPlan refuses is not pending.
func (x *run) protected(s Settings, applied *Applied) (bool, error) {
	var on bool
	if raw, ok := s.Vars["deletion_protection"]; !ok || json.Unmarshal(raw, &on) != nil || on {
		return true, nil
	}
	if s.LastPlan == "" || s.LastPlanKind == planDestroy {
		return false, nil
	}
	return CheckPlan(x.ctx, x.c, s.LastPlan, applied) == nil, x.ctx.Err()
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
	ok, err := x.confirm("Lift the deletion protection?")
	if err != nil {
		return err
	}
	if !ok {
		return &stop{0, x.interrupted} // the plan kept, as a Ctrl-C now leaves it
	}
	if err := CheckPlan(x.ctx, x.c, id, applied); err != nil {
		return err
	}
	if err := x.applyPlan(*s, sum, then); err != nil {
		return err
	}
	x.interrupted = "Interrupted: the deletion protection is lifted, nothing is destroyed: run dormeto uninstall " + x.args() + " again."
	return nil
}

// planDestroy starts the destroy plan with s's release and follows it, its id kept in s and install.json as soon as it
// runs (a destroy plan, for uninstall apply), then shows its summary.
func (x *run) planDestroy(s *Settings) (PlanSummary, error) {
	x.interrupted = "Interrupted before the destroy plan started: run dormeto uninstall " + x.args() + " again."
	id, err := Start(x.ctx, x.c, x.p, ActionPlanDestroy, map[string]string{"RELEASE_BASE": s.ReleaseBase, "RELEASE_VERSION": s.Version})
	if err != nil {
		return PlanSummary{}, err
	}
	following := "Follow it: dormeto install status " + x.args() + "; apply it once done: dormeto uninstall apply " + x.args()
	x.interrupted = "Interrupted: the destroy plan started (build " + id + ") and goes on in CodeBuild. " + following
	s.LastPlan, s.LastPlanKind = id, planDestroy
	// The plan runs: its id is kept even when Ctrl-C comes now.
	if err := WriteSettings(context.WithoutCancel(x.ctx), x.c, x.p, *s); err != nil {
		return PlanSummary{}, err
	}
	res, err := Follow(x.ctx, x.c, x.p, id, x.follow("Destroy plan", 0))
	if err != nil {
		return PlanSummary{}, x.lost(err, "the destroy plan goes on in CodeBuild. "+following)
	}
	if res.Failed {
		return PlanSummary{}, x.failed("Destroy plan", res, x.p.LogGroup)
	}
	x.interrupted = "Not applied; the destroy plan is kept: dormeto uninstall apply " + x.args()
	sum, err := x.summary(id)
	if err != nil {
		return PlanSummary{}, err
	}
	x.showPlan(id, s.Version, sum)
	return sum, nil
}

// applyDestroy applies the saved destroy plan s.LastPlan (sum its summary) with s's release once CheckPlan accepts it,
// and follows it; once it succeeded, install.json forgets the plan (the build removed applied.json).
func (x *run) applyDestroy(s Settings, applied *Applied, sum PlanSummary) error {
	x.interrupted = "Not applied; the destroy plan is kept: dormeto uninstall apply " + x.args()
	if err := CheckPlan(x.ctx, x.c, s.LastPlan, applied); err != nil {
		return err
	}
	id, err := Start(x.ctx, x.c, x.p, ActionApplyDestroy, map[string]string{"PLAN_BUILD": s.LastPlan, "RELEASE_BASE": s.ReleaseBase, "RELEASE_VERSION": s.Version})
	if err != nil {
		return err
	}
	res, err := Follow(x.ctx, x.c, x.p, id, x.follow("Destroy", sum.Destroy))
	if err != nil {
		return x.lost(err, "the destroy goes on in CodeBuild. Follow it: dormeto install status "+x.args()+"; once done, dormeto uninstall "+x.args()+
			" removes the bootstrap.")
	}
	if res.Failed {
		return x.failed("Destroy", res, x.p.LogGroup)
	}
	x.interrupted = "Interrupted: the manager is destroyed; dormeto uninstall " + x.args() + " removes the bootstrap."
	x.say("  ✓ destroyed: %d resources", sum.Destroy)
	s.LastPlan, s.LastPlanKind = "", ""
	return WriteSettings(context.WithoutCancel(x.ctx), x.c, x.p, s)
}

// removeBootstrap removes the install's bucket (its state and answers), role, log group and project once the person
// asks (ask: in a terminal, without --yes, whose answer is no); otherwise they are kept, and how to remove them is
// said. Once started, the removal is not cancelled: a bucket left half emptied would have lost install.json, which
// names the install to remove.
func (x *run) removeBootstrap(s Settings, ask bool) error {
	if ask && x.d.Terminal && !x.r.Yes {
		ok, err := x.confirm("Remove the install's bucket (its state and answers), role, log group and project too?")
		if err != nil {
			return err
		}
		if ok {
			if err := Teardown(context.WithoutCancel(x.ctx), x.c, x.p, true); err != nil {
				return err
			}
			role := ", the role " + x.p.Role
			if s.BuildRole != "" { // the role given is not the install's to remove
				role = ""
			}
			x.say("Removed the bucket %s%s, the log group %s and the project %s.", x.p.Bucket, role, x.p.LogGroup, x.p.Name)
			return nil
		}
	}
	x.say("Kept the bucket %s (its answers serve a reinstall) and the project; dormeto uninstall %s again removes them.", x.p.Bucket, x.args())
	return nil
}

// accessKept says, in organization mode, that organization access is not removed, and where its removal is described.
func (x *run) accessKept(s Settings) {
	if s.Mode == "organization" {
		x.say("Organization access is not removed: its roles keep working for a reinstall. To remove them and Dormeto's statement of the delegation policy: %s",
			organizationProcedure)
	}
}
