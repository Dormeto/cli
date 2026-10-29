package gcponboard

// The router's grants of the access (the manifest's router grants on the project, the queue and the service account;
// the controller's is the router part's), the projectBindings step, and the IAM differences every binding step prints:
// each grant added or removed, Dormeto's when it is one of the router's grants, else Google's, recorded with the step
// after which it appeared. Bindings are only ever added, with the policy's etag (Update); none is changed or removed.

import (
	"fmt"
	"strings"

	"github.com/Dormeto/cli/internal/gcp/access"
)

// accessGrant is one of the router's grants, its placeholders filled, and where it is: project, queue or serviceAccount.
type accessGrant struct {
	Grant
	On string
}

// fill replaces the manifest's placeholders.
func (x *run) fill(s string) string {
	return strings.NewReplacer("{project}", x.n.project, "{projectNumber}", x.number, "{region}", x.n.region,
		"{database}", x.n.database).Replace(s)
}

// accessGrants are the router's grants that the access part installs, in the manifest's order.
func (x *run) accessGrants() []accessGrant {
	var out []accessGrant
	for _, g := range access.Load().Router {
		if g.On != "project" && g.On != "queue" && g.On != "serviceAccount" {
			continue
		}
		ag := accessGrant{Grant: Grant{Role: x.fill(g.Role), Member: x.n.member}, On: g.On}
		if g.Condition != nil {
			ag.Condition = &Condition{Title: x.fill(g.Condition.Title), Expression: x.fill(g.Condition.Expression)}
		}
		out = append(out, ag)
	}
	return out
}

func (x *run) grantsOn(on string) []accessGrant {
	var out []accessGrant
	for _, g := range x.accessGrants() {
		if g.On == on {
			out = append(out, g)
		}
	}
	return out
}

// grantOn is the one grant on a queue or a service account.
func (x *run) grantOn(on string) accessGrant { return x.grantsOn(on)[0] }

// ours tells whether a grant is one of the router's.
func (x *run) ours(g Grant) bool {
	for _, a := range x.accessGrants() {
		if grantKey(a.Grant) == grantKey(g) {
			return true
		}
	}
	return false
}

// where names a grant's place in the plan and the IAM lines.
func (x *run) where(on string) string {
	switch on {
	case "queue":
		return "queue " + x.n.queue
	case "serviceAccount":
		return "service account " + x.n.email
	}
	return "the project"
}

// describe is a grant as the plan and the IAM lines say it: <role> to <member> on <where>[, if "<title>": <expression>].
func (x *run) describe(g Grant, on string) string {
	s := g.Role + " to " + g.Member + " on " + x.where(on)
	if g.Condition != nil {
		s += fmt.Sprintf(", if %q: %s", g.Condition.Title, g.Condition.Expression)
	}
	return s
}

// holds tells whether p gives g's member g's role under g's condition.
func holds(p *Policy, g Grant) bool {
	for _, have := range grantsOf(p) {
		if grantKey(have) == grantKey(g) {
			return true
		}
	}
	return false
}

func (x *run) planProjectBindings() (*step, error) {
	var missing []accessGrant
	var lines []string
	for _, g := range x.grantsOn("project") {
		if holds(x.policy, g.Grant) {
			x.foundGrant(g)
			continue
		}
		missing = append(missing, g)
		lines = append(lines, "+ grant "+x.describe(g.Grant, g.On))
	}
	if len(missing) == 0 {
		return nil, nil
	}
	return &step{name: "projectBindings", lines: lines, apply: func() (string, error) {
		if err := x.catchUp(); err != nil {
			return "", err
		}
		grants := make([]Grant, len(missing))
		for i, g := range missing {
			grants[i] = g.Grant
		}
		err := x.settle(func() error {
			_, err := Update(x.ctx, x.d.Google, ProjectPolicy(x.n.project), func(p *Policy) error { Add(p, grants...); return nil })
			return err
		})
		if err != nil {
			return "", err
		}
		for _, g := range missing {
			x.rec.addGrant(recordGrant(g))
		}
		if err := x.policyChanged("projectBindings"); err != nil {
			return "", err
		}
		roles := "roles"
		if len(missing) == 1 {
			roles = "role"
		}
		return fmt.Sprintf("granted %d %s on the project", len(missing), roles), nil
	}}, nil
}

// bind adds a grant to the policy of the queue or the service account and prints the policy's difference.
func (x *run) bind(rp ResourcePolicy, g accessGrant) error {
	var res Result
	err := x.settle(func() error {
		var err error
		res, err = Update(x.ctx, x.d.Google, rp, func(p *Policy) error { Add(p, g.Grant); return nil })
		return err
	})
	if err != nil {
		return err
	}
	x.rec.addGrant(recordGrant(g))
	x.differences(res.Before, res.After, g.On, x.stepOf(g.On))
	return nil
}

func (x *run) stepOf(on string) string {
	if on == "serviceAccount" {
		return "serviceAccountBinding"
	}
	return on
}

// policyChanged reads the project's policy again and prints its difference from the last read, after step.
func (x *run) policyChanged(step string) error {
	p, err := read(x.ctx, x.d.Google, ProjectPolicy(x.n.project))
	if err != nil {
		return err
	}
	x.differences(x.policy, p, "project", step)
	x.policy, x.stale = p, false
	return nil
}

// catchUp reads the project's policy again when a step that may change it ran since the last read (roles,
// serviceAccount, tag, database, queue, serviceAccountBinding: Google may grant its service agents at first use), the
// difference attributed to the last step that ran.
func (x *run) catchUp() error {
	if !x.stale {
		return nil
	}
	return x.policyChanged(x.lastStep)
}

// differences adds the IAM lines of a policy's change to the running step's; the grants that are not the router's are
// Google's, recorded with step.
func (x *run) differences(before, after *Policy, on, step string) {
	added, removed := Diff(before, after)
	line := func(sign string, g Grant, who string) {
		x.iam = append(x.iam, fmt.Sprintf("IAM %s %s (%s)", sign, x.describe(g, on), who))
	}
	for _, g := range added {
		if x.ours(g) {
			line("+", g, "Dormeto")
			continue
		}
		line("+", g, "Google, after "+step)
		x.rec.addGoogle(GoogleGrant{Role: g.Role, Member: g.Member, Step: step})
	}
	for _, g := range removed {
		line("-", g, "Google, after "+step)
	}
}

// found notes, while planning, a resource that is already there: the record lists it too.
func (x *run) found(kind, name string) {
	if x.rec.addResource(kind, name) {
		x.recChanged = true
	}
}

// foundGrant notes, while planning, a grant that is already there.
func (x *run) foundGrant(g accessGrant) {
	if x.rec.addGrant(recordGrant(g)) {
		x.recChanged = true
	}
}
