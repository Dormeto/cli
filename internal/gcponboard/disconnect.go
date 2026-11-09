package gcponboard

// `dormeto gcp disconnect` (Google Cloud manager design §4.3): undoes the connection from the manager's record. It reads the
// manager (0.24.0 or later) and its record, checks the caller's permissions at the host, the recorded scopes and router
// projects, then plans, in the manifest's order, the removal of every binding of the two service accounts — on each router's
// controller, on each router project's tag value, on the projects (the project scopes, the router projects and the host),
// the folders and the organizations of the scopes — then the two custom roles where connect defines them (one still granted
// to another member is kept and said), the accounts with their impersonation grants, the provider and the pool (Google keeps
// them deleted for 30 days, the roles for 7: a later connect undeletes them). It asks (or takes --yes), applies, verifies
// that no binding of the two accounts is left, clears the record (the manager unregisters its Google Cloud routers), and lists
// what it leaves: the APIs (other things may use them) and the bindings Google added during the connects.

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/gcp/access"
)

// Disconnect is `dormeto gcp disconnect`: 0 when disconnected (or the plan only, with DryRun, or declined), 1 when the
// manager has no connection through the project, a check fails or Google refuses, 2 for a bad project or no --yes without a
// terminal, 130 when interrupted.
func Disconnect(ctx context.Context, d Deps, r ConnectRequest) int {
	x := newLink(ctx, d, r, "disconnect")
	return x.end(x.disconnect())
}

func (x *link) disconnect() error {
	if err := x.validate(); err != nil {
		return err
	}
	if err := x.readManager(); err != nil {
		return err
	}
	switch {
	case x.rec == nil:
		return &stop{1, fmt.Sprintf("manager %s has no Google Cloud connection: nothing to disconnect\nNothing was changed.", x.m.Env)}
	case x.rec.Project != x.host:
		return &stop{1, fmt.Sprintf("manager %s is connected to Google Cloud through project %s, not %s: %s\nNothing was changed.",
			x.m.Env, x.rec.Project, x.host, x.commandLine("disconnect", x.rec.Project))}
	}
	if err := x.recordNames(); err != nil {
		return err
	}
	x.scopes, x.routers = slices.Clone(x.rec.Scopes), slices.Clone(x.rec.Routers)
	if err := x.checkHostAndScopes(); err != nil {
		return err
	}
	if err := x.findProjects(); err != nil {
		return err
	}
	if err := x.findFormerRouterProjects(); err != nil {
		return err
	}
	// The tag values once their projects are tested: a value the caller may not read answers as one that does not exist.
	if err := x.checkRest(); err != nil {
		return err
	}
	if err := x.findTagValues(false); err != nil {
		return err
	}
	if err := x.planDisconnect(); err != nil {
		return err
	}
	x.printDisconnectPlan()
	if done, err := x.confirm("Disconnect manager " + x.m.Env + " from Google Cloud (project " + x.host + ")?"); done || err != nil {
		return err
	}
	return x.applyDisconnect()
}

// findFormerRouterProjects finds the projects at the scopes, and the host, that hold a dormetoCommand but no router of the
// record: a router there once (its role is connect's alone), or a first connect that stopped before its record named its
// routers. Their command grants and role go too, with the grants on the controllers they hold now.
func (x *link) findFormerRouterProjects() error {
	var candidates []string
	for _, p := range append(slices.Clone(x.projects), x.host) {
		if !slices.Contains(x.routerProjects(), p) && !slices.Contains(candidates, p) {
			candidates = append(candidates, p)
		}
	}
	held := make([]bool, len(candidates))
	err := x.each(len(candidates), func(i int) error {
		var got googleRole
		err := x.d.Google.Do(x.ctx, http.MethodGet, roleAt{"dormetoCommand", "projects/" + candidates[i]}.url(), nil, &got)
		switch {
		case gcp.IsNotFound(err), serviceDisabled(err):
			return nil
		case err != nil:
			return fmt.Errorf("the role dormetoCommand of project %s is not read: %w", candidates[i], err)
		}
		held[i] = !got.Deleted
		return nil
	})
	if err != nil {
		return err
	}
	for i, p := range candidates {
		if held[i] {
			x.former = append(x.former, p)
		}
	}
	// The record's router projects are listed too, only for what Cloud Run did not list there: the warning, since their
	// routers are the record's.
	if _, err := x.findRouters(x.routerProjects()); err != nil {
		return err
	}
	x.former = slices.DeleteFunc(x.former, func(p string) bool { return slices.Contains(x.routerProjects(), p) })
	routers, err := x.findRouters(x.former)
	if err != nil {
		return err
	}
	x.routers = append(x.routers, routers...)
	x.former = slices.DeleteFunc(x.former, func(p string) bool { return slices.Contains(x.routerProjects(), p) })
	return nil
}

// commandProjects are the projects where the command account may hold grants: the routers' and the former ones'.
func (x *link) commandProjects() []string {
	out := x.routerProjects()
	for _, p := range x.former {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// members are the two accounts' members.
func (x *link) members() []string { return []string{x.member("discovery"), x.member("command")} }

// planDisconnect plans the removals in the manifest's order: controller, tag, projectBindings, folderBindings,
// organizationBindings, roles, serviceAccounts, federation; the record's last.
func (x *link) planDisconnect() error {
	var controllers, tags []place
	for _, r := range x.routers {
		controllers = append(controllers, x.controllerPlace(r))
	}
	for _, p := range x.commandProjects() {
		if x.tagValue[p] != "" {
			tags = append(tags, x.tagPlace(p))
		}
	}
	var projects []string
	for _, s := range x.scopes {
		if id, ok := strings.CutPrefix(s, "projects/"); ok {
			projects = append(projects, id)
		}
	}
	projects = append(projects, x.routerProjects()...)
	projects = append(projects, x.former...)
	projects = append(projects, x.host)
	byStep := map[string][]place{"controller": controllers, "tag": tags}
	seen := map[string]bool{}
	for _, id := range projects {
		if !seen[id] {
			seen[id] = true
			byStep["projectBindings"] = append(byStep["projectBindings"], x.scopePlace("projects/"+id))
		}
	}
	for _, s := range x.scopes {
		if !strings.HasPrefix(s, "projects/") {
			p := x.scopePlace(s)
			byStep[p.step] = append(byStep[p.step], p)
		}
	}
	for _, name := range []string{"controller", "tag", "projectBindings", "folderBindings", "organizationBindings"} {
		s, err := x.planRemovals(name, byStep[name], x.members(), "")
		if err != nil {
			return err
		}
		if s != nil {
			x.steps = append(x.steps, *s)
		}
	}
	for _, plan := range []func() (*step, error){x.planRoleRemovals, x.planAccountRemovals, x.planFederationRemoval} {
		s, err := plan()
		if err != nil {
			return err
		}
		if s != nil {
			x.steps = append(x.steps, *s)
		}
	}
	// The manager's DELETE unregisters every Google Cloud router it holds, the record's or not.
	x.recordLine = fmt.Sprintf("- record of the connection in manager %s (the manager's Google Cloud routers unregistered)", x.m.Env)
	return nil
}

// planRemovals takes every binding of the members out of each place's policy (a place gone has none), each place's at once
// with its etag (Update), its lines followed by the place's note, else note, when there is one; the policies as they will be
// are kept for the roles' check.
func (x *link) planRemovals(name string, places []place, members []string, note string) (*step, error) {
	type group struct {
		place  place
		grants []Grant
	}
	var groups []group
	var lines []string
	for _, pl := range places {
		p, err := read(x.ctx, x.d.Google, pl.rp)
		if gcp.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("the policy of %s: %w", pl.what, err)
		}
		var grants []Grant
		for _, g := range grantsOf(p) {
			for _, m := range members {
				if sameMember(g.Member, m) {
					grants = append(grants, Grant{Role: g.Role, Member: m, Condition: g.Condition})
				}
			}
		}
		after := &Policy{Bindings: slices.Clone(p.Bindings)}
		Remove(after, grants...)
		x.left[pl.what] = planned{pl.rp, after}
		if len(grants) == 0 {
			continue
		}
		groups = append(groups, group{pl, grants})
		for _, g := range grants {
			x.removing[pl.what+"\x00"+grantKey(g)] = true
			line := "- grant " + describeOn(g, pl.what)
			if n := cmp.Or(pl.note, note); n != "" {
				line += " (" + n + ")"
			}
			lines = append(lines, line)
		}
	}
	if len(groups) == 0 {
		return nil, nil
	}
	return &step{name: name, lines: lines, apply: func() (string, error) {
		n := 0
		for _, g := range groups {
			var res Result
			err := x.settle(func() error {
				var err error
				res, err = Update(x.ctx, x.d.Google, g.place.rp, func(p *Policy) error { Remove(p, g.grants...); return nil })
				return err
			})
			if gcp.IsNotFound(err) {
				continue
			}
			if err != nil {
				return "", fmt.Errorf("%s: %w", g.place.what, err)
			}
			_, gone := Diff(res.Before, res.After)
			for _, r := range gone {
				x.iam = append(x.iam, "IAM - "+describeOn(r, g.place.what)+" (Dormeto)")
			}
			n += len(gone)
		}
		return fmt.Sprintf("removed %s", count(n, "grant")), nil
	}}, nil
}

// planRoleRemovals deletes the two custom roles where connect defines them; one still granted to another member in a policy
// this run read is kept, and one whose folder's organization is unknown is left.
func (x *link) planRoleRemovals() (*step, error) {
	for _, s := range x.unknownOrgs {
		x.kept = append(x.kept, fmt.Sprintf("= role dormetoDiscovery of %s's organization: left in place, the organization is unknown (no project of the folder is visible)", resourceWords(s)))
	}
	// Where connect defines them: the scopes' dormetoDiscovery, and dormetoCommand in each router project and in each project
	// swept, which held a router once (connect alone defines a role of that ID).
	candidates := customRoles(x.grants())
	for _, what := range slices.Sorted(maps.Keys(x.left)) {
		if id, ok := strings.CutPrefix(what, "project "); ok && !slices.Contains(candidates, roleAt{"dormetoCommand", "projects/" + id}) {
			candidates = append(candidates, roleAt{"dormetoCommand", "projects/" + id})
		}
	}
	var lines []string
	var gone []roleAt
	for _, at := range candidates {
		var got googleRole
		found, err := x.get(iamAPI, at.url(), &got)
		if err != nil {
			return nil, err
		}
		if !found || got.Deleted {
			continue
		}
		if by := x.stillGranted(at.name()); by != "" {
			x.kept = append(x.kept, fmt.Sprintf("= role %s in %s: kept, still granted to %s", at.id, at.where(), by))
			continue
		}
		gone = append(gone, at)
		lines = append(lines, fmt.Sprintf("- role %s in %s", at.id, at.where()))
	}
	if len(gone) == 0 {
		return nil, nil
	}
	return &step{name: "roles", lines: lines, apply: func() (string, error) {
		var done []string
		for _, at := range gone {
			if err := x.call(http.MethodDelete, at.url(), nil, nil); err != nil && !gcp.IsNotFound(err) {
				return "", err
			}
			done = append(done, at.id+" in "+at.where())
		}
		return "deleted " + strings.Join(done, ", "), nil
	}}, nil
}

// stillGranted is a member a policy read by this run still grants role after the removals, and where ("" for none).
func (x *link) stillGranted(role string) string {
	for _, what := range slices.Sorted(maps.Keys(x.left)) {
		for _, g := range grantsOf(x.left[what].after) {
			if g.Role == role {
				return g.Member + " on " + what
			}
		}
	}
	return ""
}

// planAccountRemovals deletes the accounts that are there; their impersonation grants go with them.
func (x *link) planAccountRemovals() (*step, error) {
	var lines, emails, iam []string
	for _, kind := range accountKinds() {
		email := x.email(kind)
		found, err := x.get(iamAPI, x.serviceAccountURL(email), nil)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		emails = append(emails, email)
		lines = append(lines, "- service account "+email)
		p, err := read(x.ctx, x.d.Google, ServiceAccountPolicy(x.host, email))
		if err != nil {
			return nil, fmt.Errorf("the policy of service account %s: %w", email, err)
		}
		for _, g := range grantsOf(p) {
			lines = append(lines, "- grant "+describeOn(g, "service account "+email)+" (goes with it)")
			iam = append(iam, "IAM - "+describeOn(g, "service account "+email)+" (Dormeto)")
		}
	}
	if len(emails) == 0 {
		return nil, nil
	}
	return &step{name: "serviceAccounts", lines: lines, apply: func() (string, error) {
		for _, email := range emails {
			if err := x.call(http.MethodDelete, x.serviceAccountURL(email), nil, nil); err != nil && !gcp.IsNotFound(err) {
				return "", err
			}
		}
		x.iam = append(x.iam, iam...)
		return "deleted " + strings.Join(emails, ", "), nil
	}}, nil
}

// planFederationRemoval deletes the provider, then the pool, those not deleted already.
func (x *link) planFederationRemoval() (*step, error) {
	var lines []string
	var actions []func() (string, error)
	var provider googleProvider
	found, err := x.get(iamAPI, x.providerURL(), &provider)
	if err != nil {
		return nil, err
	}
	if found && provider.State != "DELETED" {
		lines = append(lines, "- provider "+x.providerID()+" of pool "+x.poolID())
		actions = append(actions, x.operation(http.MethodDelete, x.providerURL(), nil, "deleted provider "+x.providerID()))
	}
	var pool googlePool
	if found, err = x.get(iamAPI, x.poolURL(), &pool); err != nil {
		return nil, err
	}
	if found && pool.State != "DELETED" {
		lines = append(lines, "- workload identity pool "+x.poolID())
		actions = append(actions, x.operation(http.MethodDelete, x.poolURL(), nil, "deleted pool "+x.poolID()))
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return &step{name: "federation", lines: lines, apply: func() (string, error) {
		var done []string
		for _, a := range actions {
			d, err := a()
			if err != nil && !gcp.IsNotFound(err) {
				return "", err
			}
			done = append(done, d)
		}
		return strings.Join(done, ", "), nil
	}}, nil
}

func (x *link) printDisconnectPlan() {
	x.say("Plan for disconnecting manager %s from Google Cloud (project %s):", x.m.Env, x.host)
	for _, l := range x.planLines() {
		x.say("  %s", l)
	}
}

func (x *link) applyDisconnect() error {
	if err := x.applySteps(); err != nil {
		return err
	}
	if left := x.leftBindings(); len(left) > 0 {
		x.say("  ✗ verification: still granted: %s", strings.Join(left, "; "))
		return &stop{1, "The manager's record is kept: run the same command again to remove what is left."}
	}
	x.say("  ✓ verified: no binding of %s or %s is left at the scopes and router projects", x.email("discovery"), x.email("command"))
	if err := x.c.Manager.DeleteGoogleConnection(x.ctx); err != nil {
		x.say("  ✗ record: %s", err)
		return &stop{1, "Google Cloud is disconnected, but the manager's record is not cleared: run the same command again."}
	}
	x.say("  ✓ record: cleared the connection in manager %s (the manager's Google Cloud routers unregistered)", x.m.Env)
	x.leftInPlaceAfterDisconnect()
	if err := x.printJSON(map[string]any{"connection": nil}); err != nil {
		return err
	}
	x.say("Disconnected manager %s from Google Cloud (project %s).", x.m.Env, x.host)
	return nil
}

// leftBindings are the bindings of the two accounts still in the policies the plan read.
func (x *link) leftBindings() []string {
	var out []string
	for _, what := range slices.Sorted(maps.Keys(x.left)) {
		p, err := read(x.ctx, x.d.Google, x.left[what].rp)
		if gcp.IsNotFound(err) {
			continue
		}
		if err != nil {
			out = append(out, fmt.Sprintf("the policy of %s is not read again: %s", what, err))
			continue
		}
		for _, g := range grantsOf(p) {
			if slices.ContainsFunc(x.members(), func(m string) bool { return sameMember(g.Member, m) }) {
				out = append(out, describeOn(g, what))
			}
		}
	}
	return out
}

// leftInPlaceAfterDisconnect lists what disconnect does not touch: the manager's APIs on in the host project (other things
// may use them) and the bindings Google added during the connects.
func (x *link) leftInPlaceAfterDisconnect() {
	var lines []string
	if on, err := enabledServices(x.ctx, x.d.Google, x.number); err != nil {
		lines = append(lines, "the APIs are not read: "+err.Error())
	} else {
		for _, api := range access.Load().Manager.APIs {
			if slices.Contains(on, api) {
				lines = append(lines, "API "+api)
			}
		}
	}
	for _, g := range x.rec.GoogleAdded {
		lines = append(lines, g+" (added by Google)")
	}
	if len(lines) == 0 {
		return
	}
	x.say("Left in place:")
	for _, l := range lines {
		x.say("  %s", l)
	}
}
