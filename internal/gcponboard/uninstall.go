package gcponboard

// `dormeto gcp uninstall`: removes exactly what `dormeto gcp install` added (spec §5), from the install record. It checks
// the caller's permissions (with --force the offboard command's too: a ✗ stops it before any change), reads the record,
// refuses while workloads are onboarded (--force offboards each one first), prints the plan, asks (or takes --yes), then
// removes in reverse order — offboard, job, neg, cloudRun (the waker, then the controller), queue, database, projectBindings
// (the recorded grants that the project's bindings before the first install did not hold), tag, serviceAccount, roles —
// and lists the APIs and the bindings Google added as left in place, and the Kubernetes access of workloads as left in the
// cluster, with the command to remove it (it reaches no cluster), every IAM change printed, then reads the project's
// policy and warns of what differs from its bindings before the install and Google's. The custom roles and the tag are the
// project's: they stay while another router's record lists them. The record goes with the database: the step before it
// keeps it in the cache, from which a run after a stopped one continues, and which the end removes, unless a tag Google
// refused to delete is left for a rerun.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/install"
)

// Uninstall is `dormeto gcp uninstall`: 0 when uninstalled (or the plan only, with DryRun, or declined), 1 when a check
// fails, workloads are onboarded without Force, there is no router or Google refuses, 2 for a bad region or no --yes
// without a terminal, 130 when interrupted.
func Uninstall(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.end(x.uninstall())
}

// workloadRef is a workload as the controller lists it.
type workloadRef struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

func (x *run) uninstall() error {
	if err := x.validate(); err != nil {
		return err
	}
	if err := x.uninstallChecks(); err != nil {
		return err
	}
	if err := x.planUninstall(); err != nil {
		return err
	}
	x.printUninstallPlan()
	if x.r.DryRun {
		return nil
	}
	if !x.r.Yes {
		if !x.d.Terminal {
			return &stop{2, "--yes: needed"}
		}
		question := "Remove router " + x.n.router + " from project " + x.n.project + "?"
		ok, err := install.Prompted(x.ctx, x.d.Out, func() (bool, error) { return x.d.Prompter.Confirm(question) })
		if err != nil {
			return err
		}
		if !ok {
			return &stop{0, "Nothing was changed."}
		}
	}
	return x.applyUninstall()
}

// uninstallChecks asks the permissions of uninstall, and of offboard with Force; the missing ones are named and nothing
// is done.
func (x *run) uninstallChecks() error {
	commands := []string{"uninstall"}
	if x.r.Force {
		commands = append(commands, "offboard")
	}
	r := x.r
	if x.r.Force {
		// The record is read after the checks; its features' offboard permissions are asked too.
		recorded, err := recordedFeatures(x)
		if err != nil {
			return fmt.Errorf("the install record is not read, so its features' offboard permissions are not tested: %w", err)
		}
		r.With = featureSet(x.r.With, recorded)
	}
	rep, err := check(x.ctx, x.d, r, commands)
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
	// An API that is off has no resource to remove: reading one is a miss, not a wait for Google to propagate it.
	x.number, x.turningOn = rep.Project.Number, rep.APIs.Missing
	x.say("Checked: %s holds every permission dormeto gcp uninstall needs in project %s.", rep.Account, x.n.project)
	return nil
}

// planUninstall reads the policy and the record, then plans each step's removals.
func (x *run) planUninstall() error {
	p, err := x.readPolicy(ProjectPolicy(x.n.project))
	if err != nil {
		return err
	}
	x.policy, x.start = p, p
	if err := x.loadRecord(); err != nil {
		return err
	}
	if !x.recorded && !x.resumed {
		return &stop{1, fmt.Sprintf("no router %s is installed in project %s: there is no install record\nNothing was changed.", x.n.router, x.n.project)}
	}
	x.features = featureSet(x.rec.Features, x.r.With)
	x.sinceRecord()
	workloads, unknown, err := x.workloads()
	if err != nil {
		return err
	}
	if unknown != "" {
		if !x.r.Force {
			return &stop{1, fmt.Sprintf("The controller %s does not answer (%s): the workloads cannot be listed.\nRun the same command with --force to go on without offboarding them.\nNothing was changed.", x.n.controller, unknown)}
		}
		x.workloadsUnknown = true
		x.say("  ! the workloads are unknown, the controller %s does not answer (%s): none is offboarded", x.n.controller, unknown)
	}
	if len(workloads) > 0 && !x.r.Force {
		var names []string
		for _, w := range workloads {
			names = append(names, w.ID+" ("+w.State+")")
		}
		return &stop{1, fmt.Sprintf("Workloads are onboarded in router %s: %s.\nOffboard them, or run the same command with --force to offboard each first.\nNothing was changed.",
			x.n.router, strings.Join(names, ", "))}
	}
	if len(workloads) > 0 {
		x.steps = append(x.steps, x.planOffboard(workloads))
	}
	if x.r.Force {
		if s := x.planPendingOffboards(workloads); s != nil {
			x.steps = append(x.steps, *s)
		}
	}
	planners := []func() (*step, error){x.planJobRemoval, x.planNEGRemoval, x.planCloudRunRemoval, x.planQueueRemoval,
		x.planDatabaseRemoval, x.planUnbind, x.planTagRemoval, x.planAccountRemoval, x.planRolesRemoval}
	for _, plan := range planners {
		s, err := plan()
		if err != nil {
			return err
		}
		if s != nil {
			x.steps = append(x.steps, *s)
		}
	}
	for _, l := range x.rbacLeft() {
		x.kept = append(x.kept, "= "+l)
	}
	return nil
}

func (x *run) printUninstallPlan() {
	x.say("Plan for project %s, uninstalling router %s:", x.n.project, x.n.router)
	for _, s := range x.steps {
		for _, l := range s.lines {
			x.say("  %s", l)
		}
	}
	for _, l := range x.kept {
		x.say("  %s", l)
	}
	if len(x.steps) == 0 {
		x.say("  nothing is left of router %s to remove", x.n.router)
	}
	x.printSinceLast()
	for _, s := range x.steps {
		if s.name == "queue" || s.name == "database" {
			x.say("Reinstalling in this region after this: Google may keep a deleted queue's name blocked for up to 3 days (a create can appear to succeed in that window), and a deleted database's ID for about 5 minutes.")
			break
		}
	}
}

// listed tells whether the record lists a resource.
func (x *run) listed(kind, name string) bool {
	for _, r := range x.rec.Resources {
		if r.Kind == kind && r.Name == name {
			return true
		}
	}
	return false
}

// workloads are the controller's onboarded workloads; none when the record lists no controller or it is gone. unknown
// says why they cannot be listed when the controller answers 404 or 5xx; any other refusal (a 403) is an error.
func (x *run) workloads() (list []workloadRef, unknown string, err error) {
	if !x.listed("service", x.serviceName(x.n.controller)) {
		return nil, "", nil
	}
	found, err := x.get(runAPI, x.servicesURL()+"/"+esc(x.n.controller), nil)
	if err != nil || !found {
		return nil, "", err
	}
	err = x.d.Google.Invoke(x.ctx, http.MethodGet, x.controllerURL()+"/v1/workloads", x.controllerURL(), nil, &list)
	var e *gcp.Error
	switch {
	case err == nil:
		return list, "", nil
	case errors.As(err, &e) && (e.Code == http.StatusNotFound || e.Code >= 500):
		return nil, err.Error(), nil
	}
	return nil, "", fmt.Errorf("the controller's workloads are not read: %w", err)
}

func (x *run) planOffboard(workloads []workloadRef) step {
	var lines []string
	for _, w := range workloads {
		lines = append(lines, "- offboard workload "+w.ID+" ("+w.State+")")
	}
	return step{name: "offboard", lines: lines, apply: func() (string, error) {
		for _, w := range workloads {
			// The controller's workload, then its tag bindings: Google refuses to delete a tag value still bound.
			// --force is what asked for this step: a workload that cannot wake is offboarded with its backends left down.
			_, left, err := x.offboardWorkload(w.ID, true)
			if err != nil && !gcp.IsNotFound(err) {
				return "", fmt.Errorf("workload %s: %s", w.ID, refusal(err))
			}
			if len(left) > 0 {
				x.say("  ! workload %s is offboarded with its backends left down: %s", w.ID, strings.Join(leftDown(left), ", "))
			}
		}
		return fmt.Sprintf("offboarded %d workloads", len(workloads)), nil
	}}
}

// withGrants adds to a removal the recorded grants on its resource, which go with it: named in the plan, and said once gone.
func (x *run) withGrants(s *step, on string) *step {
	if s == nil {
		return nil
	}
	var iam []string
	for _, g := range x.rec.Grants {
		if g.On == on {
			what := x.describe(Grant{Role: g.Role, Member: g.Member, Condition: g.Condition}, on)
			s.lines = append(s.lines, "- grant "+what+" (goes with it)")
			iam = append(iam, "IAM - "+what+" (Dormeto)")
		}
	}
	remove := s.apply
	s.apply = func() (string, error) {
		done, err := remove()
		if err == nil {
			x.iam = append(x.iam, iam...)
		}
		return done, err
	}
	return s
}

// removal plans a step that removes one resource the record lists: nil when it is not there any more. del removes it,
// a resource gone meanwhile is not an error.
func (x *run) removal(name, kind, resource, api, getURL, what string, del func() error) (*step, error) {
	if !x.listed(kind, resource) {
		return nil, nil
	}
	found, err := x.get(api, getURL, nil)
	if err != nil || !found {
		return nil, err
	}
	return &step{name: name, lines: []string{"- " + what}, apply: func() (string, error) {
		if err := del(); err != nil && !gcp.IsNotFound(err) {
			return "", err
		}
		return "removed " + what, nil
	}}, nil
}

func (x *run) planJobRemoval() (*step, error) {
	return x.removal("job", "job", x.jobName(), schedulerAPI, x.jobsURL()+"/"+esc(x.n.job), "job "+x.n.job, func() error {
		return x.call(http.MethodDelete, x.jobsURL()+"/"+esc(x.n.job), nil, nil)
	})
}

func (x *run) planNEGRemoval() (*step, error) {
	return x.removal("neg", "neg", x.negName(), computeAPI, x.negsURL()+"/"+esc(x.n.neg), "network endpoint group "+x.n.neg, func() error {
		var op struct {
			SelfLink string `json:"selfLink"`
		}
		err := x.call(http.MethodDelete, x.negsURL()+"/"+esc(x.n.neg), nil, &op)
		if err == nil {
			err = gcp.WaitCompute(x.ctx, x.d.Google, op.SelfLink, negTimeout)
		}
		if err != nil && !gcp.IsNotFound(err) && x.workloadsUnknown {
			flags := "--project " + x.n.project + " --region " + x.n.region
			if x.r.Configuration != "" {
				flags += " --gcloud-configuration " + x.r.Configuration
			}
			return fmt.Errorf("%w\nA workload still onboarded keeps this group in use, and its controller does not answer. The way out: install a good image (dormeto gcp install %s --image <a good image>), offboard the workloads (dormeto gcp workloads offboard <workload> %s), then run this command again.", err, flags, flags)
		}
		return err
	})
}

// planCloudRunRemoval removes the waker, then the controller, and with it its invoker binding.
func (x *run) planCloudRunRemoval() (*step, error) {
	var steps []*step
	for _, name := range []string{x.n.waker, x.n.controller} {
		s, err := x.removal("cloudRun", "service", x.serviceName(name), runAPI, x.servicesURL()+"/"+esc(name), "service "+name, func() error {
			var op longOperation
			if err := x.call(http.MethodDelete, x.servicesURL()+"/"+esc(name), nil, &op); err != nil {
				return err
			}
			return x.wait(runV2, op, serviceTimeout)
		})
		if err != nil {
			return nil, err
		}
		if name == x.n.controller {
			s = x.withGrants(s, "controller")
		}
		if s != nil {
			steps = append(steps, s)
		}
	}
	return combine("cloudRun", steps), nil
}

// combine is one step that does the steps in order; nil for none.
func combine(name string, steps []*step) *step {
	if len(steps) == 0 {
		return nil
	}
	out := &step{name: name}
	for _, s := range steps {
		out.lines = append(out.lines, s.lines...)
	}
	out.apply = func() (string, error) {
		var done []string
		for _, s := range steps {
			d, err := s.apply()
			if err != nil {
				return "", err
			}
			done = append(done, d)
		}
		return strings.Join(done, ", "), nil
	}
	return out
}

func (x *run) planQueueRemoval() (*step, error) {
	s, err := x.removal("queue", "queue", x.queueName(), tasksAPI, x.queuesURL()+"/"+esc(x.n.queue), "queue "+x.n.queue, func() error {
		return x.call(http.MethodDelete, x.queuesURL()+"/"+esc(x.n.queue), nil, nil)
	})
	return x.withGrants(s, "queue"), err
}

// planDatabaseRemoval removes the database and with it the install record, which the cache keeps meanwhile.
func (x *run) planDatabaseRemoval() (*step, error) {
	if !x.listed("database", x.databaseName()) || !x.database {
		return nil, nil
	}
	return &step{name: "database", lines: []string{"- database " + x.n.database + " (and the install record)"}, apply: func() (string, error) {
		if err := x.writeCache(); err != nil {
			return "", fmt.Errorf("the record is not kept in the cache before the database goes: %w", err)
		}
		var op longOperation
		if err := x.call(http.MethodDelete, x.databaseURL(), nil, &op); err != nil && !gcp.IsNotFound(err) {
			return "", err
		} else if err == nil {
			// Its operation lives under the database's path: a 404 says the database is gone.
			if err := x.wait(firestoreV1, op, databaseTimeout); err != nil && !gcp.IsNotFound(err) {
				return "", err
			}
		}
		return "removed database " + x.n.database, nil
	}}, nil
}

// planUnbind removes the recorded project grants that the project's bindings before the first install did not hold,
// from the project's policy as read: a service account deleted by hand left them as deleted:serviceAccount:…?uid=…
// members, which Remove matches by the email.
func (x *run) planUnbind() (*step, error) {
	before := x.rec.beforePolicy()
	var grants []Grant
	for _, g := range x.rec.Grants {
		if grant := (Grant{Role: g.Role, Member: g.Member, Condition: g.Condition}); g.On == "project" && !holds(before, grant) {
			grants = append(grants, grant)
		}
	}
	preview := &Policy{Bindings: append([]Binding(nil), x.policy.Bindings...)}
	removed := Remove(preview, grants...)
	if len(removed) == 0 {
		return nil, nil
	}
	var lines []string
	for _, g := range removed {
		lines = append(lines, "- grant "+x.describe(g, "project"))
	}
	return &step{name: "projectBindings", lines: lines, apply: func() (string, error) {
		var res Result
		err := x.settle(func() error {
			var err error
			res, err = Update(x.ctx, x.d.Google, ProjectPolicy(x.n.project), func(p *Policy) error { Remove(p, grants...); return nil })
			return err
		})
		if err != nil {
			return "", err
		}
		_, gone := Diff(res.Before, res.After)
		for _, g := range gone {
			x.iam = append(x.iam, "IAM - "+x.describe(g, "project")+" (Dormeto)")
		}
		return fmt.Sprintf("removed %d grants from the project", len(gone)), nil
	}}, nil
}

// usedBy is the router of another record in the project that lists a resource ("" for none): the roles and the tag are
// the project's, not this router's alone.
func (x *run) usedBy(kind, name string) (string, error) {
	if x.others == nil {
		regions, err := routerRegions(x.ctx, x.d, x.n.project)
		if err != nil {
			return "", err
		}
		x.others = map[string]Record{}
		for _, region := range regions {
			if region == x.n.region {
				continue
			}
			var doc recordDocument
			u := firestoreV1 + "projects/" + esc(x.n.project) + "/databases/dmtr-" + esc(region) + "/documents/" + recordPath
			if err := x.d.Google.Do(x.ctx, http.MethodGet, u, nil, &doc); err != nil {
				if gcp.IsNotFound(err) {
					continue
				}
				return "", err
			}
			var rec Record
			if err := json.Unmarshal([]byte(doc.Fields.Data.StringValue), &rec); err != nil {
				return "", fmt.Errorf("%s in database dmtr-%s: %w", recordPath, region, err)
			}
			x.others[region] = rec
		}
	}
	for region, rec := range x.others {
		for _, r := range rec.Resources {
			if r.Kind == kind && r.Name == name {
				return "dmtr-" + region, nil
			}
		}
	}
	return "", nil
}

// keep tells whether another router uses a resource, and says so in the plan.
func (x *run) keep(kind, name, what string) (bool, error) {
	router, err := x.usedBy(kind, name)
	if err != nil || router == "" {
		return false, err
	}
	x.kept = append(x.kept, "= "+what+" kept: used by router "+router)
	return true, nil
}

// planTagRemoval removes the value dormeto/managed, then the key, unless another router lists them.
func (x *run) planTagRemoval() (*step, error) {
	var steps []*step
	for _, t := range []struct{ kind, resource, namespaced string }{
		{"tagValues", "tagValue", x.n.project + "/dormeto/managed"}, {"tagKeys", "tagKey", x.n.project + "/dormeto"}} {
		name, found, err := x.tag(t.kind, t.namespaced)
		if err != nil {
			return nil, err
		}
		if !found || (!x.listed(t.resource, name) && !x.listed(t.resource, t.namespaced)) {
			continue
		}
		if kept, err := x.keep(t.resource, name, "tag "+strings.TrimPrefix(t.namespaced, x.n.project+"/")); err != nil || kept {
			if err != nil {
				return nil, err
			}
			continue
		}
		label := "tag " + strings.TrimPrefix(t.namespaced, x.n.project+"/")
		steps = append(steps, &step{name: "tag", lines: []string{"- " + label}, apply: func() (string, error) {
			var op longOperation
			err := x.call(http.MethodDelete, crmV3+name, nil, &op)
			if gcp.IsNotFound(err) {
				return "removed " + name, nil
			}
			if err == nil {
				err = x.wait(crmV3, op, tagTimeout)
			}
			if gcp.IsPrecondition(err) {
				// Still bound (or holding values), said by the answer or by its operation: removing the bindings is the
				// offboarding's; the rest goes on.
				x.stranded = append(x.stranded, fmt.Sprintf("%s: %s", label, strings.TrimSuffix(precondition(err), ".")))
				return "left " + label + " (Google refuses to delete it)", nil
			}
			if err != nil {
				return "", err
			}
			return "removed " + name, nil
		}})
	}
	return combine("tag", steps), nil
}

func (x *run) planAccountRemoval() (*step, error) {
	s, err := x.removal("serviceAccount", "serviceAccount", x.accountName(), iamAPI, x.accountURL(), "service account "+x.n.email, func() error {
		return x.call(http.MethodDelete, x.accountURL(), nil, nil)
	})
	return x.withGrants(s, "serviceAccount"), err
}

// planRolesRemoval deletes the custom roles (IAM keeps a deleted role for 37 days, undeletable), unless another router
// lists them.
func (x *run) planRolesRemoval() (*step, error) {
	var steps []*step
	for _, id := range allRoleIDs() {
		name := x.roleName(id)
		if !x.listed("role", name) {
			continue
		}
		var got googleRole
		found, err := x.get(iamAPI, x.rolesURL()+"/"+id, &got)
		if err != nil {
			return nil, err
		}
		if !found || got.Deleted {
			continue
		}
		if kept, err := x.keep("role", name, "role "+id); err != nil || kept {
			if err != nil {
				return nil, err
			}
			continue
		}
		s, err := x.removal("roles", "role", name, iamAPI, x.rolesURL()+"/"+id, "role "+id, func() error {
			return x.call(http.MethodDelete, x.rolesURL()+"/"+id, nil, nil)
		})
		if err != nil {
			return nil, err
		}
		// Gone since the read above: nothing to remove.
		if s != nil {
			steps = append(steps, s)
		}
	}
	return combine("roles", steps), nil
}

// applyUninstall runs the steps in order. A refused step stops the run: what was done and what was not are said; the
// next run continues from the record (the database's, or the cache's once the database is gone).
func (x *run) applyUninstall() error {
	for i, s := range x.steps {
		done, err := s.apply()
		if err != nil {
			code := 1
			if x.ctx.Err() != nil {
				code = exitInterrupted
			}
			x.say("  ✗ %s: %s", s.name, err)
			return &stop{code, fmt.Sprintf("Done: %s. Not done: %s. Run the same command again to continue.", stepNames(x.steps[:i]), stepNames(x.steps[i:]))}
		}
		x.say("  ✓ %s: %s", s.name, done)
		x.printIAM()
	}
	x.checkPolicy()
	if len(x.stranded) > 0 {
		if err := x.writeCache(); err != nil {
			x.say("  ! the record is not kept for a rerun: %s", err)
		}
	} else if path := x.cachePath(); path != "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			x.say("  ! %s is not removed: %s", path, err)
		}
	}
	x.leftInPlace()
	if len(x.stranded) > 0 {
		x.say("Run the same command again to remove the tag once Google allows it: the record waits in the cache.")
	}
	x.say("Uninstalled router %s from project %s.", x.n.router, x.n.project)
	return nil
}

// checkPolicy reads the project's policy where the uninstall ended, and warns of what differs from its bindings before the
// install and Google's, but another router's: a warning, as the install's verification: someone else's change does not
// fail the uninstall.
func (x *run) checkPolicy() {
	p, err := read(x.ctx, x.d.Google, ProjectPolicy(x.n.project))
	if err != nil {
		x.say("  ! the project's IAM policy is not read again: %s", err)
		return
	}
	expected := x.rec.beforePolicy()
	for _, g := range x.rec.Google {
		Add(expected, Grant{Role: g.Role, Member: g.Member})
	}
	added, removed := Diff(expected, p)
	clean := true
	warn := func(sign string, grants []Grant) {
		for _, g := range grants {
			if !x.otherRouter(g.Member) {
				clean = false
				x.say("  ! IAM difference from before the install, not this uninstall's: %s %s", sign, x.describe(g, "project"))
			}
		}
	}
	warn("+", added)
	warn("-", removed)
	if clean {
		x.say("  ✓ verified: the project's IAM policy holds its bindings from before the install, and Google's")
	}
}

// leftInPlace lists what the uninstall does not touch: the APIs the install turned on (other things may use them), the
// bindings Google added (its own service agents') and the Kubernetes access of workloads.
func (x *run) leftInPlace() {
	var lines []string
	for _, r := range x.rec.Resources {
		if r.Kind == "api" {
			lines = append(lines, "API "+r.Name)
		}
	}
	for _, g := range x.rec.Google {
		lines = append(lines, x.describe(Grant{Role: g.Role, Member: g.Member}, "project")+" (added by Google)")
	}
	lines = append(lines, x.stranded...)
	lines = append(lines, x.rbacLeft()...)
	if len(lines) == 0 {
		return
	}
	x.say("Left in place:")
	for _, l := range lines {
		x.say("  %s", l)
	}
}

// precondition is Google's message of a failed precondition.
func precondition(err error) string {
	var e *gcp.Error
	if errors.As(err, &e) && e.Message != "" {
		return e.Message
	}
	return err.Error()
}
