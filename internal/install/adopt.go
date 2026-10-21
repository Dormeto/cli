package install

// --adopt-state: a first install takes over the Terraform state of a manager installed with the module directly (vision
// docs/specs/2026-10-18-cli-adopt-design.md). The manager module's part of that state becomes the install's state, the
// resources kept: the state is checked, moved to the install's module, kept as it was, then written before the first
// plan; the first apply says how to detach the resources from the source, and uninstall refuses until then.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/Dormeto/cli/internal/install/build"
)

const (
	installModule      = "dormeto" // the manager module's name in the install's configuration (Config)
	defaultAdoptModule = "manager" // --adopt-module's default: module "manager"
)

// moduleNameRe is a Terraform module name.
var moduleNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// Moved is the state an adopting install takes from a source state.
type Moved struct {
	State     []byte   // the install's state, as <env>/manager.tfstate holds it
	Resources int      // the resources moved
	Left      []string // the addresses of the resources left in the source, in its order
	Terraform string   // the Terraform version that wrote the source
	IAMNames  []string // the names of the IAM roles and policies moved, in the source's order
}

// Adoption is --adopt-state's: the source state's file (its base name and SHA-256), the module's name in it, the file as
// read, and the state moved from it; Err is why it cannot be adopted.
type Adoption struct {
	File, SHA256, Module string
	Source               []byte
	Moved
	Err error
}

// stateResource is what MoveState reads of a resource of a state: where it is, and its instances' attributes.
type stateResource struct {
	Module, Mode, Type, Name string
	Instances                []struct {
		Attributes json.RawMessage `json:"attributes"`
	} `json:"instances"`
}

// address is the resource's address: its module, data. for a data source, its type and name.
func (r stateResource) address() string {
	a := r.Type + "." + r.Name
	if r.Mode == "data" {
		a = "data." + a
	}
	if r.Module != "" {
		a = r.Module + "." + a
	}
	return a
}

// MoveState is the state an install of the project p takes from source, a Terraform state of a manager installed as
// module.<module>. The source must be of format version 4, written by a Terraform no newer than terraform (the build's),
// and hold module.<module>'s aws_lambda_function.api named dmt-<env>-manager-api in p's account and region. Every
// resource of module.<module> and of the modules nested in it moves to module.dormeto, their dependencies (and a
// provider of the module) renamed with them, the rest of their JSON kept as written; the resources outside are left (and
// listed); the serial goes up, the lineage and the Terraform version stay; the outputs and the check results go.
func MoveState(source []byte, module string, p Project, terraform string) (Moved, error) {
	var state map[string]json.RawMessage
	var head struct {
		Version          int             `json:"version"`
		TerraformVersion string          `json:"terraform_version"`
		Serial           json.Number     `json:"serial"`
		Resources        []stateResource `json:"resources"`
	}
	if err := json.Unmarshal(source, &state); err != nil {
		return Moved{}, fmt.Errorf("not a Terraform state: %w", err)
	}
	if err := json.Unmarshal(source, &head); err != nil {
		return Moved{}, fmt.Errorf("not a Terraform state: %w", err)
	}
	switch {
	case head.Version != 4:
		return Moved{}, fmt.Errorf("state format version %d, not 4", head.Version)
	case !IsVersion(head.TerraformVersion):
		return Moved{}, fmt.Errorf("terraform_version %q is not a version", head.TerraformVersion)
	case CompareVersions(head.TerraformVersion, terraform) > 0:
		return Moved{}, fmt.Errorf("written by Terraform %s, newer than the install's %s", head.TerraformVersion, terraform)
	}
	serial, err := strconv.ParseUint(head.Serial.String(), 10, 64)
	if err != nil {
		return Moved{}, fmt.Errorf("serial %q is not a number", head.Serial)
	}
	var resources []map[string]json.RawMessage
	if err := json.Unmarshal(state["resources"], &resources); err != nil {
		return Moved{}, fmt.Errorf("not a Terraform state: resources: %w", err)
	}

	from := "module." + module
	rename := func(addr string) (string, bool) {
		switch {
		case addr == from:
			return "module." + installModule, true
		case strings.HasPrefix(addr, from+"."):
			return "module." + installModule + addr[len(from):], true
		}
		return addr, false
	}
	m := Moved{Terraform: head.TerraformVersion}
	var moved []map[string]json.RawMessage
	var modules []string
	var api *stateResource
	for i, r := range head.Resources {
		if name, _, _ := strings.Cut(strings.TrimPrefix(r.Module, "module."), "."); r.Module != "" && !slices.Contains(modules, "module."+name) {
			modules = append(modules, "module."+name)
		}
		to, ok := rename(r.Module)
		if !ok {
			m.Left = append(m.Left, r.address())
			continue
		}
		if r.Module == from && r.Mode == "managed" && r.Type == "aws_lambda_function" && r.Name == "api" && len(r.Instances) > 0 {
			api = &head.Resources[i]
		}
		if r.Mode == "managed" && (r.Type == "aws_iam_role" || r.Type == "aws_iam_policy") {
			for _, in := range r.Instances {
				var a struct{ Name string }
				if json.Unmarshal(in.Attributes, &a) == nil && a.Name != "" {
					m.IAMNames = append(m.IAMNames, a.Name)
				}
			}
		}
		res, err := moveResource(resources[i], to, rename)
		if err != nil {
			return Moved{}, fmt.Errorf("%s: %w", r.address(), err)
		}
		moved = append(moved, res)
	}
	if len(moved) == 0 {
		return Moved{}, fmt.Errorf("no %s in the state (its modules: %s)", from, cmp.Or(strings.Join(modules, ", "), "none"))
	}
	if err := isManager(api, from, p); err != nil {
		return Moved{}, err
	}
	m.Resources = len(moved)

	out := map[string]any{}
	for k, v := range state {
		out[k] = v
	}
	out["serial"] = serial + 1
	out["outputs"] = map[string]any{}
	out["check_results"] = nil
	out["resources"] = moved
	if m.State, err = encodeState(out, "  "); err != nil {
		return Moved{}, err
	}
	return m, nil
}

// isManager checks that api, the module from's aws_lambda_function.api (nil: none), is the manager of p: named
// dmt-<env>-manager-api, in p's account and region.
func isManager(api *stateResource, from string, p Project) error {
	if api == nil {
		return fmt.Errorf("%s has no aws_lambda_function.api: not a manager's state", from)
	}
	var a struct {
		FunctionName string `json:"function_name"`
		ARN          string `json:"arn"`
	}
	if err := json.Unmarshal(api.Instances[0].Attributes, &a); err != nil {
		return fmt.Errorf("%s's aws_lambda_function.api: %w", from, err)
	}
	if want := "dmt-" + p.Env + "-manager-api"; a.FunctionName != want {
		return fmt.Errorf("%s's aws_lambda_function.api is %s, not env %s's %s", from, a.FunctionName, p.Env, want)
	}
	parts := strings.SplitN(a.ARN, ":", 7)
	if len(parts) != 7 || parts[0] != "arn" || parts[2] != "lambda" {
		return fmt.Errorf("%s's ARN %q is not a Lambda function's", a.FunctionName, a.ARN)
	}
	if region, account := parts[3], parts[4]; account != p.Account || region != p.Region {
		return fmt.Errorf("%s is in %s %s, not in the profile's %s %s", a.FunctionName, account, region, p.Account, p.Region)
	}
	return nil
}

// moveResource is a resource of a state (its JSON by key) in the module to: its provider and its instances'
// dependencies renamed by rename, everything else as written.
func moveResource(r map[string]json.RawMessage, to string, rename func(string) (string, bool)) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for k, v := range r {
		out[k] = v
	}
	var err error
	if out["module"], err = encodeState(to, ""); err != nil {
		return nil, err
	}
	var provider string
	if json.Unmarshal(r["provider"], &provider) == nil {
		if renamed, ok := rename(provider); ok {
			if out["provider"], err = encodeState(renamed, ""); err != nil {
				return nil, err
			}
		}
	}
	var instances []map[string]json.RawMessage
	if err := json.Unmarshal(r["instances"], &instances); err != nil {
		return nil, fmt.Errorf("instances: %w", err)
	}
	for _, in := range instances {
		raw, ok := in["dependencies"]
		if !ok {
			continue
		}
		var deps []string
		if err := json.Unmarshal(raw, &deps); err != nil {
			return nil, fmt.Errorf("dependencies: %w", err)
		}
		for j, d := range deps {
			deps[j], _ = rename(d)
		}
		if in["dependencies"], err = encodeState(deps, ""); err != nil {
			return nil, err
		}
	}
	if out["instances"], err = encodeState(instances, ""); err != nil {
		return nil, err
	}
	return out, nil
}

// encodeState is v as JSON, its strings as written (no HTML escaping): indented by indent and ending with a newline,
// or compact without one when indent is empty.
func encodeState(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if indent == "" {
		return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
	}
	return buf.Bytes(), nil
}

// adoptSource reads --adopt-state's file, nil when not adopting; its flags are checked first: --adopt-module only with
// it, a module name, not with --access.
func (x *run) adoptSource() ([]byte, error) {
	switch {
	case x.r.AdoptState == "" && x.r.AdoptModule != "":
		return nil, &stop{2, "--adopt-module: only with --adopt-state"}
	case x.r.AdoptState == "":
		return nil, nil
	case x.r.Access:
		return nil, &stop{2, "--adopt-state: not with --access"}
	case x.r.AdoptModule != "" && !moduleNameRe.MatchString(x.r.AdoptModule):
		return nil, &stop{2, fmt.Sprintf("--adopt-module: %q is not a module name", x.r.AdoptModule)}
	}
	b, err := os.ReadFile(x.r.AdoptState)
	if err != nil {
		return nil, &stop{2, "--adopt-state: " + err.Error()}
	}
	return b, nil
}

// adopting is the adoption of source, --adopt-state's file, by the run's install, with the build's Terraform. It is
// refused, before anything is written, for an env with install.json (saved) or applied.json (applied), and where another
// state is at <env>/manager.tfstate already.
func (x *run) adopting(source []byte, saved *Settings, applied *Applied) (*Adoption, error) {
	switch {
	case saved != nil:
		return nil, &stop{1, fmt.Sprintf("env %s is already installed with the CLI: --adopt-state is for a first install", x.env)}
	case applied != nil:
		return nil, &stop{1, fmt.Sprintf("env %s has an applied version already (applied.json): --adopt-state is for a first install", x.env)}
	}
	sum := sha256.Sum256(source)
	a := &Adoption{File: filepath.Base(x.r.AdoptState), SHA256: hex.EncodeToString(sum[:]), Module: cmp.Or(x.r.AdoptModule, defaultAdoptModule), Source: source}
	if a.Moved, a.Err = MoveState(source, a.Module, x.p, build.TerraformVersion); a.Err != nil {
		return a, nil // the checks say why
	}
	there, err := getObject(x.ctx, x.c, x.p, x.p.StateKey())
	switch {
	case err != nil:
		return nil, err
	case there != nil && !bytes.Equal(there, a.State): // the same state is an adoption that stopped once it wrote it
		return nil, stateThere(x.p)
	}
	return a, nil
}

// stateThere stops an adoption that finds another state where it would write its own.
func stateThere(p Project) error {
	return &stop{1, fmt.Sprintf("env %s has a Terraform state already (s3://%s/%s), maybe an earlier adoption's, interrupted: to start over, remove %s "+
		"and %s from the bucket %s, then run the same command again. Nothing was adopted.", p.Env, p.Bucket, p.StateKey(), p.Key("install.json"), p.StateKey(), p.Bucket)}
}

// adopt writes the run's adoption, the bootstrap there and before the first plan: the source state kept as
// <env>/adopted/<UTC time>.tfstate (never read again), the moved state as <env>/manager.tfstate where there is none,
// then install.json naming where it came from (s, from then on).
func (x *run) adopt(s *Settings) error {
	a := x.adoption
	backup := x.p.Key("adopted", x.d.Now().UTC().Format("20060102T150405Z")+".tfstate")
	if err := putObject(x.ctx, x.c, x.p, backup, a.Source); err != nil {
		return err
	}
	if err := putFirstState(x.ctx, x.c, x.p, a.State); err != nil {
		return err
	}
	s.AdoptedFrom, s.AdoptedModule = &AdoptedFrom{File: a.File, SHA256: a.SHA256}, a.Module
	// The state is the install's: install.json is written even when Ctrl-C comes now.
	if err := WriteSettings(context.WithoutCancel(x.ctx), x.c, x.p, *s); err != nil {
		return err
	}
	x.interrupted = "Interrupted before the plan started: the state is adopted; run dormeto install again without --adopt-state."
	x.say("Adopted: module.%s of %s is the install's state, s3://%s/%s; the original is kept as s3://%s/%s", a.Module, a.File, x.p.Bucket, x.p.StateKey(),
		x.p.Bucket, backup)
	return nil
}

// putFirstState writes state as the manager's Terraform state, <env>/manager.tfstate, only where there is none (a
// conditional put). A state there already (written since adopting looked) stops the adoption, unless it is that same
// state: an adoption that stopped once it wrote it.
func putFirstState(ctx context.Context, c Clients, p Project, state []byte) error {
	key := p.StateKey()
	_, err := c.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: &p.Bucket, Key: &key, Body: bytes.NewReader(state), IfNoneMatch: aws.String("*")})
	switch {
	case err == nil:
		return nil
	case apiCode(err) != "PreconditionFailed":
		return fmt.Errorf("write s3://%s/%s: %w", p.Bucket, key, err)
	}
	there, err := getObject(ctx, c, p, key)
	if err != nil || bytes.Equal(there, state) {
		return err
	}
	return stateThere(p)
}

// detach says, after an adopted manager's first apply (s its settings, applied the applied.json before it, nil: none),
// how to detach the resources from the configuration that installed it: while both states name them, applying that
// configuration again would fight the install.
func (x *run) detach(s Settings, applied *Applied) {
	if s.AdoptedFrom == nil || applied != nil {
		return
	}
	x.say("The manager is now this install's. Detach it from the configuration that installed it: terraform state rm module.%s there, "+
		"then remove the module from that configuration.", s.AdoptedModule)
}

// adoptionPending refuses to destroy an adopted manager that nothing applied yet (s its settings, applied its
// applied.json, nil: none, resources whether its state holds any): destroying it would delete the manager adopted.
func (x *run) adoptionPending(s Settings, applied *Applied, resources bool) error {
	if s.AdoptedFrom == nil || applied != nil || !resources {
		return nil
	}
	return &stop{1, fmt.Sprintf("env %s's manager is adopted (from %s) and nothing is applied yet: an uninstall would destroy the adopted manager. "+
		"To abandon the adoption instead, remove %s and %s from the bucket %s: the source state is untouched, and the bootstrap can stay or be removed by hand.",
		x.env, s.AdoptedFrom.File, x.p.Key("install.json"), x.p.StateKey(), x.p.Bucket)}
}
