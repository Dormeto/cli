// Package access is the manifest of what Dormeto needs in Google Cloud: the APIs, the custom roles, the predefined
// roles' permissions, the router's grants, the manager's connection (its section manager) and, per `dormeto gcp` command,
// the permissions the operator needs by step.
// access.json is a byte for byte copy of router/gcp/access.json (a test compares them).
package access

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
)

//go:embed access.json
var raw []byte

// Condition is an IAM condition, its placeholders ({project}, {projectNumber}, {region}, {database}) not yet filled.
type Condition struct {
	Title      string `json:"title"`
	Expression string `json:"expression"`
}

// Role is a custom role.
type Role struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

// Grant is one of the router's grants: a role on a resource kind.
type Grant struct {
	Role      string     `json:"role"`
	On        string     `json:"on"`
	Condition *Condition `json:"condition,omitempty"`
}

// Feature is an optional access (`--with`): its APIs, custom roles, the router's grants and, per `dormeto gcp` command, the
// permissions the operator needs for it.
type Feature struct {
	APIs     []string                       `json:"apis"`
	Roles    map[string]Role                `json:"roles"`
	Router   []Grant                        `json:"router"`
	Operator map[string]map[string][]string `json:"operator"`
}

// ManagerRole is a custom role of the manager's accounts: Permissions wherever it is defined, OrganizationPermissions
// added only when it is defined in an organization (Google refuses them in a project's role).
type ManagerRole struct {
	Title                   string   `json:"title"`
	Description             string   `json:"description"`
	Permissions             []string `json:"permissions"`
	OrganizationPermissions []string `json:"organizationPermissions"`
}

// ManagerGrant is one of the manager accounts' grants: a role (a custom role's ID, or a predefined role) to an account
// (discovery or command) on a resource (scope, controller, tagValue or project).
type ManagerGrant struct {
	Account string `json:"account"`
	Role    string `json:"role"`
	On      string `json:"on"`
}

// Attribute is one entry of the provider's attribute mapping: Google's attribute and the expression that sets it.
type Attribute struct {
	Name, Expression string
}

// Federation is the workload identity pool and its AWS provider, placeholders ({env}, {account}) not yet filled; Mapping
// in the manifest's order.
type Federation struct {
	Pool      string      `json:"pool"`
	Provider  string      `json:"provider"`
	Condition string      `json:"condition"`
	Mapping   []Attribute `json:"-"`
}

// Impersonation is the grant on each service account to the principal set of each of the manager's roles that act as it:
// Roles by account (discovery, command).
type Impersonation struct {
	Role         string              `json:"role"`
	PrincipalSet string              `json:"principalSet"`
	Roles        map[string][]string `json:"-"`
}

// Manager is the manifest's manager section: what `dormeto gcp connect` sets up for the manager.
type Manager struct {
	APIs            []string               `json:"apis"`
	Federation      Federation             `json:"federation"`
	ServiceAccounts map[string]string      `json:"serviceAccounts"`
	Roles           map[string]ManagerRole `json:"roles"`
	Grants          struct {
		Scopes         []ManagerGrant `json:"scopes"`
		RouterProjects []ManagerGrant `json:"routerProjects"`
	} `json:"grants"`
	Impersonation Impersonation `json:"impersonation"`
}

// Manifest is access.json.
type Manifest struct {
	Version    int                 `json:"version"`
	APIs       []string            `json:"apis"`
	Roles      map[string]Role     `json:"roles"`
	Predefined map[string][]string `json:"predefined"`
	Router     []Grant             `json:"router"`
	Features   map[string]Feature  `json:"features"`
	Manager    Manager             `json:"manager"`

	commands []string
	steps    map[string][]string
	perms    map[string]map[string][]string
}

var (
	once     sync.Once
	manifest Manifest
)

// Load is the embedded manifest. It panics on an invalid embedded file, which a test proves never happens.
func Load() Manifest {
	once.Do(func() {
		m, err := parse(raw)
		if err != nil {
			panic("access.json: " + err.Error())
		}
		manifest = m
	})
	return manifest
}

func parse(b []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	var top struct {
		Operator json.RawMessage `json:"operator"`
	}
	if err := json.Unmarshal(b, &top); err != nil {
		return m, err
	}
	var err error
	if m.commands, err = keys(top.Operator); err != nil {
		return m, err
	}
	if err := json.Unmarshal(top.Operator, &m.perms); err != nil {
		return m, err
	}
	m.steps = map[string][]string{}
	var each map[string]json.RawMessage
	if err := json.Unmarshal(top.Operator, &each); err != nil {
		return m, err
	}
	for _, c := range m.commands {
		if m.steps[c], err = keys(each[c]); err != nil {
			return m, err
		}
	}
	if m.Version != 1 || len(m.commands) == 0 {
		return m, fmt.Errorf("version %d with %d commands", m.Version, len(m.commands))
	}
	return m, parseManager(b, &m.Manager)
}

// parseManager reads the manager section's mapping in file order and the impersonation's roles by account.
func parseManager(b []byte, m *Manager) error {
	var top struct {
		Manager struct {
			Federation struct {
				Mapping json.RawMessage `json:"mapping"`
			} `json:"federation"`
			Impersonation map[string]json.RawMessage `json:"impersonation"`
		} `json:"manager"`
	}
	if err := json.Unmarshal(b, &top); err != nil {
		return err
	}
	if top.Manager.Federation.Mapping == nil {
		return fmt.Errorf("manager: no federation mapping")
	}
	names, err := keys(top.Manager.Federation.Mapping)
	if err != nil {
		return fmt.Errorf("manager mapping: %w", err)
	}
	var mapping map[string]string
	if err := json.Unmarshal(top.Manager.Federation.Mapping, &mapping); err != nil {
		return fmt.Errorf("manager mapping: %w", err)
	}
	for _, name := range names {
		m.Federation.Mapping = append(m.Federation.Mapping, Attribute{name, mapping[name]})
	}
	m.Impersonation.Roles = map[string][]string{}
	for account := range m.ServiceAccounts {
		if raw, ok := top.Manager.Impersonation[account]; ok {
			var roles []string
			if err := json.Unmarshal(raw, &roles); err != nil {
				return fmt.Errorf("manager impersonation %s: %w", account, err)
			}
			m.Impersonation.Roles[account] = roles
		}
	}
	return nil
}

// keys are the keys of a JSON object in file order.
func keys(b json.RawMessage) ([]string, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("not an object")
	}
	var out []string
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		out = append(out, t.(string))
		var skip json.RawMessage
		if err := d.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Commands are the operator's commands in the manifest's order.
func Commands() []string { return slices.Clone(Load().commands) }

// Steps are a command's steps in the manifest's order (nil for an unknown command).
func Steps(command string) []string { return slices.Clone(Load().steps[command]) }

// Needs are the permissions of all of a command's steps, sorted, without duplicates (nil for an unknown command).
func Needs(command string) []string {
	steps, ok := Load().perms[command]
	if !ok {
		return nil
	}
	var out []string
	for _, ps := range steps {
		out = append(out, ps...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// StepNeeds are the permissions of the named steps of a command, sorted, without duplicates (nil for an unknown command):
// connect and disconnect test each resource for the steps that act on it only.
func StepNeeds(command string, steps ...string) []string {
	all, ok := Load().perms[command]
	if !ok {
		return nil
	}
	out := []string{}
	for _, step := range steps {
		out = append(out, all[step]...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// FeatureNames are the optional features, sorted.
func FeatureNames() []string {
	names := make([]string, 0, len(Load().Features))
	for name := range Load().Features {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// CheckFeatures is nil when every name is a feature, else an error naming the first that is not and the features there are.
func CheckFeatures(names []string) error {
	for _, name := range names {
		if _, ok := Load().Features[name]; !ok {
			return fmt.Errorf("%q is not a feature: the features are %s", name, strings.Join(FeatureNames(), ", "))
		}
	}
	return nil
}

// NeedsWith are Needs(command) and, for each named feature, the permissions its operator steps need for the command (a
// name that is not a feature adds nothing).
func NeedsWith(command string, features []string) []string { return NeedsWithout(command, features) }

// NeedsWithout are NeedsWith(command, features) without the permissions of the command's steps named in skip: a
// permission another step needs too stays.
func NeedsWithout(command string, features []string, skip ...string) []string {
	steps, ok := Load().perms[command]
	if !ok {
		return nil
	}
	var out []string
	for step, ps := range steps {
		if !slices.Contains(skip, step) {
			out = append(out, ps...)
		}
	}
	for _, name := range features {
		for _, ps := range Load().Features[name].Operator[command] {
			out = append(out, ps...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
