package gcponboard

// The roles step: the manifest's custom roles, dormetoRouter and dormetoRouterBackends, in the project. A missing one is
// created (stage GA); a deleted one, whose ID cannot be reused for 37 days, is undeleted then updated; one whose title,
// description or permissions differ is updated. The description names the CLI's version: Dormeto <version>: <the
// manifest's description>.

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/Dormeto/cli/internal/gcp/access"
)

const iamAPI = "iam.googleapis.com"

// googleRole is a custom role as IAM answers it.
type googleRole struct {
	Title               string   `json:"title"`
	Description         string   `json:"description"`
	IncludedPermissions []string `json:"includedPermissions"`
	Etag                string   `json:"etag"`
	Deleted             bool     `json:"deleted"`
}

// roleChange is what the roles step does to one role: create, undelete (then update) or update.
type roleChange struct {
	id, action, etag string
	want             access.Role
}

func (x *run) rolesURL() string {
	return "https://iam.googleapis.com/v1/projects/" + esc(x.n.project) + "/roles"
}

func (x *run) roleName(id string) string { return "projects/" + x.n.project + "/roles/" + id }

// roleIDs are the manifest's custom roles, sorted.
func roleIDs() []string {
	var ids []string
	for id := range access.Load().Roles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// wantRole is a custom role as the install writes it.
func (x *run) wantRole(id string) access.Role {
	r := access.Load().Roles[id]
	return access.Role{Title: r.Title, Description: "Dormeto " + x.d.Version + ": " + r.Description, Permissions: slices.Clone(r.Permissions)}
}

func (x *run) planRoles() (*step, error) {
	var changes []roleChange
	var lines []string
	for _, id := range roleIDs() {
		want := x.wantRole(id)
		var got googleRole
		found, err := x.get(iamAPI, x.rolesURL()+"/"+id, &got)
		switch {
		case err != nil:
			return nil, err
		case !found:
			changes = append(changes, roleChange{id: id, action: "create", want: want})
			lines = append(lines, fmt.Sprintf("+ role %s \"%s\" (%d permissions)", id, want.Title, len(want.Permissions)))
		case got.Deleted:
			changes = append(changes, roleChange{id: id, action: "undelete", etag: got.Etag, want: want})
			lines = append(lines, fmt.Sprintf("↺ role %s: undelete, then update", id))
		default:
			if d := roleDifferences(got, want); len(d) > 0 {
				changes = append(changes, roleChange{id: id, action: "update", want: want})
				lines = append(lines, fmt.Sprintf("~ role %s: %s", id, strings.Join(d, ", ")))
			} else {
				x.found("role", x.roleName(id))
			}
		}
	}
	if len(changes) == 0 {
		return nil, nil
	}
	return &step{name: "roles", lines: lines, apply: func() (string, error) { return x.changeRoles(changes) }}, nil
}

// roleDifferences are what an update changes: the title, the description, each permission added and removed.
func roleDifferences(got googleRole, want access.Role) []string {
	var out []string
	if got.Title != want.Title {
		out = append(out, `title "`+want.Title+`"`)
	}
	if got.Description != want.Description {
		out = append(out, `description "`+want.Description+`"`)
	}
	added, removed := setDifference(want.Permissions, got.IncludedPermissions), setDifference(got.IncludedPermissions, want.Permissions)
	for _, p := range added {
		out = append(out, "+ "+p)
	}
	for _, p := range removed {
		out = append(out, "- "+p)
	}
	return out
}

// setDifference are the items of a not in b, sorted.
func setDifference(a, b []string) []string {
	var out []string
	for _, v := range a {
		if !slices.Contains(b, v) {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func (x *run) changeRoles(changes []roleChange) (string, error) {
	var done []string
	for _, c := range changes {
		u := x.rolesURL() + "/" + c.id
		switch c.action {
		case "create":
			in := map[string]any{"roleId": c.id, "role": map[string]any{"title": c.want.Title, "description": c.want.Description,
				"includedPermissions": c.want.Permissions, "stage": "GA"}}
			if err := x.call(http.MethodPost, x.rolesURL(), in, nil); err != nil {
				return "", err
			}
			done = append(done, "created "+c.id)
		case "undelete":
			if err := x.call(http.MethodPost, u+":undelete", map[string]any{"etag": c.etag}, nil); err != nil {
				return "", err
			}
			if err := x.updateRole(c); err != nil {
				return "", err
			}
			done = append(done, "undeleted and updated "+c.id)
		case "update":
			if err := x.updateRole(c); err != nil {
				return "", err
			}
			done = append(done, "updated "+c.id)
		}
		x.rec.addResource("role", x.roleName(c.id))
	}
	return strings.Join(done, ", "), nil
}

func (x *run) updateRole(c roleChange) error {
	in := map[string]any{"title": c.want.Title, "description": c.want.Description, "includedPermissions": c.want.Permissions}
	return x.call(http.MethodPatch, x.rolesURL()+"/"+c.id+"?updateMask=title,description,includedPermissions", in, nil)
}
