package fleet

// The manager's users and custom groups (GET, POST, PUT and DELETE /iam/users and /iam/groups): the shapes the CLI
// reads, kept raw for --json, the grants a flag spells, the rules of emails, roles and group names, and their text.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Roles are the built-in roles a user holds.
var Roles = []string{"member", "viewer", "operator", "admin"}

// Permissions are what a grant gives on the workloads its tags match.
var Permissions = []string{"read", "wake", "nap"}

const (
	maxGrants           = 20
	maxGroupDescription = 200
)

var (
	emailRule     = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	groupNameRule = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
)

// NormalEmail is the email as the manager keeps it (trimmed, lower-cased), or an error when it is not an address.
func NormalEmail(s string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(s))
	if !emailRule.MatchString(e) {
		return "", fmt.Errorf("%q is not an email address", s)
	}
	return e, nil
}

// CheckRole refuses what is not a built-in role.
func CheckRole(s string) error {
	if !slices.Contains(Roles, s) {
		return fmt.Errorf("role %q is none of %s", s, strings.Join(Roles, ", "))
	}
	return nil
}

// CheckGroupName refuses a name the manager would not accept for a new group.
func CheckGroupName(s string) error {
	switch {
	case !groupNameRule.MatchString(s):
		return fmt.Errorf("group name %q: 1 to 48 lower-case letters, digits and hyphens, starting with a letter or digit", s)
	case strings.HasPrefix(s, "dmt-"):
		return fmt.Errorf("group name %q: the dmt- prefix is reserved for the built-in roles", s)
	}
	return nil
}

// CheckDescription refuses a group description over 200 characters.
func CheckDescription(s string) error {
	if utf8.RuneCountInString(s) > maxGroupDescription {
		return fmt.Errorf("description: at most %d characters", maxGroupDescription)
	}
	return nil
}

// Grant is what a group allows: the permissions on the workloads that carry all of Tags (every workload when empty).
type Grant struct {
	Tags        map[string]string `json:"tags"`
	Permissions []string          `json:"permissions"`
}

// Text is the grant as --grant spells it: "team=a,env=dev:read,wake"; "every workload" stands for no tags.
func (g Grant) Text() string {
	var tags []string
	for _, k := range sortedKeys(g.Tags) {
		tags = append(tags, k+"="+g.Tags[k])
	}
	return orStr(strings.Join(tags, ","), "every workload") + ": " + strings.Join(g.Permissions, ",")
}

// ParseGrant reads a --grant value, <tags>:<permissions>, split at the last ":" (a tag may hold one). The tags are
// comma-separated key=value, none for every workload; the permissions a non-empty subset of read, wake and nap.
func ParseGrant(s string) (Grant, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return Grant{}, fmt.Errorf("--grant: %q is not <tags>:<permissions>", s)
	}
	g := Grant{Tags: map[string]string{}}
	if tags := s[:i]; tags != "" {
		for _, t := range strings.Split(tags, ",") {
			k, v, ok := strings.Cut(t, "=")
			if !ok || k == "" {
				return Grant{}, fmt.Errorf("--grant: tag %q of %q is not key=value", t, s)
			}
			if _, dup := g.Tags[k]; dup {
				return Grant{}, fmt.Errorf("--grant: tag key %q given twice in %q", k, s)
			}
			g.Tags[k] = v
		}
		if p := TagsProblem(g.Tags); p != "" {
			return Grant{}, fmt.Errorf("--grant: %s", p)
		}
	}
	for _, p := range strings.Split(s[i+1:], ",") {
		p = strings.TrimSpace(p)
		if !slices.Contains(Permissions, p) {
			return Grant{}, fmt.Errorf("--grant: %q of %q is none of %s (at least one, comma-separated)", p, s, strings.Join(Permissions, ", "))
		}
		g.Permissions = append(g.Permissions, p)
	}
	return g, nil
}

// ParseGrants reads every --grant value: at most 20.
func ParseGrants(values []string) ([]Grant, error) {
	if len(values) > maxGrants {
		return nil, fmt.Errorf("--grant: at most %d", maxGrants)
	}
	grants := []Grant{}
	for _, v := range values {
		g, err := ParseGrant(v)
		if err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, nil
}

// User is a row of GET /iam/users: what the CLI reads of it. The row as the API sent it is kept, so --json prints
// every field.
type User struct {
	Email     string   `json:"email"`
	Role      string   `json:"role"`
	Groups    []string `json:"groups"`
	Status    string   `json:"status"`
	Enabled   bool     `json:"enabled"`
	CreatedAt string   `json:"createdAt"`
	MFA       *bool    `json:"mfa,omitempty"`

	raw json.RawMessage
}

type userFields User // without the methods below, so that decoding does not recurse

func (u *User) UnmarshalJSON(data []byte) error {
	var f userFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*u = User(f)
	u.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON is the row as the API sent it, or the fields known when it was built here.
func (u User) MarshalJSON() ([]byte, error) {
	if u.raw != nil {
		return u.raw, nil
	}
	return json.Marshal(userFields(u))
}

// Group is a custom group of GET /iam/groups, its grants included; kept raw as User is.
type Group struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Grants      []Grant `json:"grants"`
	UpdatedAt   string  `json:"updatedAt"`
	UpdatedBy   string  `json:"updatedBy"`

	raw json.RawMessage
}

type groupFields Group

func (g *Group) UnmarshalJSON(data []byte) error {
	var f groupFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*g = Group(f)
	g.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON is the row as the API sent it, or the fields known when it was built here.
func (g Group) MarshalJSON() ([]byte, error) {
	if g.raw != nil {
		return g.raw, nil
	}
	return json.Marshal(groupFields(g))
}

// UsersText is the users as a table: email, role, groups, status (disabled when it is), MFA and the age of the
// account. "no users" when there are none.
func UsersText(us []User, now time.Time) string {
	if len(us) == 0 {
		return "no users\n"
	}
	rows := [][]string{{"EMAIL", "ROLE", "GROUPS", "STATUS", "MFA", "CREATED"}}
	for _, u := range us {
		status := u.Status
		if !u.Enabled {
			status = "disabled"
		}
		mfa := none
		if u.MFA != nil {
			mfa = map[bool]string{true: "yes", false: "no"}[*u.MFA]
		}
		rows = append(rows, []string{u.Email, u.Role, orStr(strings.Join(u.Groups, ","), none), orStr(status, none), mfa, Age(u.CreatedAt, now)})
	}
	return Table(rows)
}

// GroupsText is the groups as a table: name, description, grants and the age of the last change.
func GroupsText(gs []Group, now time.Time) string {
	if len(gs) == 0 {
		return "no groups\n"
	}
	rows := [][]string{{"NAME", "DESCRIPTION", "GRANTS", "UPDATED"}}
	for _, g := range gs {
		rows = append(rows, []string{g.Name, orStr(g.Description, none), fmt.Sprint(len(g.Grants)), Age(g.UpdatedAt, now)})
	}
	return Table(rows)
}

// GroupText is one group in full: its description, last change and a line per grant.
func GroupText(g Group, loc *time.Location) string {
	var grants []string
	for _, gr := range g.Grants {
		grants = append(grants, gr.Text())
	}
	return Labelled([][2]string{
		{"name", g.Name}, {"description", g.Description},
		{"updated", strings.TrimSpace(When(g.UpdatedAt, loc) + " " + byName(g.UpdatedBy))},
		{"grants", orStr(strings.Join(grants, "\n"), "none")},
	})
}

// byName is "by <who>" for a person, an ARN shortened as ShortARN does.
func byName(who string) string {
	if who == "" {
		return ""
	}
	return "by " + ShortARN(who)
}

// ListUsers reads GET /users.
func ListUsers(ctx context.Context, c *Client) ([]User, error) {
	var out struct {
		Users []User `json:"users"`
	}
	err := c.Get(ctx, "/users", nil, &out)
	return out.Users, err
}

// ListGroups reads GET /groups.
func ListGroups(ctx context.Context, c *Client) ([]Group, error) {
	var out struct {
		Groups []Group `json:"groups"`
	}
	err := c.Get(ctx, "/groups", nil, &out)
	return out.Groups, err
}
