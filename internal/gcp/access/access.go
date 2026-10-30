// Package access is the manifest of what Dormeto needs in Google Cloud: the APIs, the custom roles, the predefined
// roles' permissions, the router's grants and, per `dormeto gcp` command, the permissions the operator needs by step.
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

// Manifest is access.json.
type Manifest struct {
	Version    int                 `json:"version"`
	APIs       []string            `json:"apis"`
	Roles      map[string]Role     `json:"roles"`
	Predefined map[string][]string `json:"predefined"`
	Router     []Grant             `json:"router"`
	Features   map[string]Feature  `json:"features"`

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
	return m, nil
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
func NeedsWith(command string, features []string) []string {
	out := Needs(command)
	if out == nil {
		return nil
	}
	for _, name := range features {
		for _, ps := range Load().Features[name].Operator[command] {
			out = append(out, ps...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
