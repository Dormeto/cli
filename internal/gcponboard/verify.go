package gcponboard

// The verification after the apply, read only: the router's grants in the project's, the queue's and the service
// account's policies, the custom roles' permissions, the service account, the database and the queue. The project's
// policy must hold exactly what it held at the start of this run, the grants this run added and those Google added
// during it: any other difference is said as a warning (someone else's change during the run, not the install's to
// undo). The record's before, the first install's, is the uninstall's, not this comparison's.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

func (x *run) verify() (warnings []string, err error) {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	lacks := func(rp ResourcePolicy, what string, grants ...accessGrant) *Policy {
		p, err := read(x.ctx, x.d.Google, rp)
		if err != nil {
			add("the policy of %s: %s", what, err)
			return nil
		}
		for _, g := range grants {
			if !holds(p, g.Grant) {
				add("%s lacks the grant of %s to %s", what, g.Role, g.Member)
			}
		}
		return p
	}
	if p := lacks(ProjectPolicy(x.n.project), "the project", x.grantsOn("project")...); p != nil {
		warnings = x.othersChanges(p)
	}

	for _, id := range roleIDs() {
		want := x.wantRole(id)
		var got googleRole
		if err := x.d.Google.Do(x.ctx, http.MethodGet, x.rolesURL()+"/"+id, nil, &got); err != nil {
			add("role %s: %s", id, err)
			continue
		}
		if got.Deleted {
			add("role %s is deleted", id)
		}
		if got.Stage != roleStage {
			add("role %s is at stage %s, not %s", id, got.Stage, roleStage)
		}
		if missing := setDifference(want.Permissions, got.IncludedPermissions); len(missing) > 0 {
			add("role %s lacks %s", id, strings.Join(inOrder(want.Permissions, missing), ", "))
		}
		if extra := setDifference(got.IncludedPermissions, want.Permissions); len(extra) > 0 {
			add("role %s has %s beyond the manifest", id, strings.Join(extra, ", "))
		}
	}

	var account struct {
		Disabled bool `json:"disabled"`
	}
	if err := x.d.Google.Do(x.ctx, http.MethodGet, x.accountURL(), nil, &account); err != nil {
		add("service account %s: %s", x.n.email, err)
	} else {
		if account.Disabled {
			add("service account %s is disabled", x.n.email)
		}
		lacks(ServiceAccountPolicy(x.n.project, x.n.email), "service account "+x.n.email, x.grantOn("serviceAccount"))
	}

	var db struct {
		Type string `json:"type"`
	}
	if err := x.d.Google.Do(x.ctx, http.MethodGet, x.databaseURL(), nil, &db); err != nil {
		add("database %s: %s", x.n.database, err)
	} else if db.Type != "FIRESTORE_NATIVE" {
		add("database %s is %s, not Firestore Native", x.n.database, db.Type)
	}

	var q googleQueue
	if err := x.d.Google.Do(x.ctx, http.MethodGet, x.queuesURL()+"/"+esc(x.n.queue), nil, &q); err != nil {
		add("queue %s: %s", x.n.queue, err)
	} else {
		if !queueAsWanted(q) {
			add("queue %s has other settings than %s", x.n.queue, queueAllSettings)
		}
		lacks(QueuePolicy(x.n.project, x.n.region, x.n.queue), "queue "+x.n.queue, x.grantOn("queue"))
	}

	if len(problems) > 0 {
		return warnings, errors.New(strings.Join(problems, "; "))
	}
	return warnings, nil
}

// othersChanges are the differences of the project's policy from its read at the start of this run that are neither
// grants this run added nor Google's grants seen during it.
func (x *run) othersChanges(p *Policy) []string {
	added, removed := Diff(x.start, p)
	var out []string
	for _, g := range added {
		if !x.granted[grantKey(g)] && !x.google[g.Role+"\x00"+g.Member] {
			out = append(out, "IAM change not made by this install: + "+x.describe(g, "project"))
		}
	}
	for _, g := range removed {
		out = append(out, "IAM change not made by this install: - "+x.describe(g, "project"))
	}
	return out
}

// inOrder are the items of some in the order of all.
func inOrder(all, some []string) []string {
	var out []string
	for _, v := range all {
		for _, s := range some {
			if v == s {
				out = append(out, v)
				break
			}
		}
	}
	return out
}
