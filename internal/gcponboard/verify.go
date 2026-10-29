package gcponboard

// The verification after the apply, read only: the router's grants in the project's, the queue's and the service
// account's policies, the custom roles' permissions, the service account, the database and the queue.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

func (x *run) verify() error {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	lacks := func(rp ResourcePolicy, what string, grants ...accessGrant) {
		p, err := read(x.ctx, x.d.Google, rp)
		if err != nil {
			add("the policy of %s: %s", what, err)
			return
		}
		for _, g := range grants {
			if !holds(p, g.Grant) {
				add("%s lacks the grant of %s to %s", what, g.Role, g.Member)
			}
		}
	}
	lacks(ProjectPolicy(x.n.project), "the project", x.grantsOn("project")...)

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
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
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
