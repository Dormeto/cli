package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Config is main.tf.json, the Terraform configuration CodeBuild runs: the manager module from the release unpacked
// beside it, with the settings as its inputs. Keys are sorted and the bytes are stable.
func Config(s Settings, w Where, p Project) ([]byte, error) {
	module := map[string]any{
		"source":         "./release/modules/manager",
		"env":            esc(s.Env),
		"discovery_mode": esc(s.Mode),
		"admin_emails":   escAll(s.AdminEmails),
	}
	switch {
	case w.Organization != nil:
		module["organization_id"] = esc(w.Organization.ID)
	case s.Mode == "organization" && s.OrganizationID != "":
		module["organization_id"] = esc(s.OrganizationID)
	}
	if d := s.Domain; d != nil {
		domain := map[string]any{
			"dashboard":       esc(d.Dashboard),
			"auth":            esc(d.AuthHost()),
			"certificate_arn": esc(d.CertificateARN),
		}
		if d.ZoneID != "" { // the module tests zone_id == null: never an empty string
			domain["zone_id"] = esc(d.ZoneID)
		}
		module["domain"] = domain
	}
	if AtLeast(s.Version, SelfUpdateVersion) && s.BuildRole == "" { // the dashboard's updates; a custom build role has no rule
		module["install"] = map[string]any{"bucket": p.Bucket, "project": p.Name, "log_group": p.LogGroup}
	}
	if s.SESIdentity != "" {
		module["ses_identity"] = esc(s.SESIdentity)
	}
	if s.EmailFrom != "" {
		module["email_from"] = esc(s.EmailFrom)
	}
	for name, raw := range s.Vars {
		if slices.Contains(metaArguments, name) {
			return nil, fmt.Errorf("var %q is a Terraform module argument, not a variable of the manager", name)
		}
		v, err := escRaw(raw)
		if err != nil {
			return nil, fmt.Errorf("var %q: %w", name, err)
		}
		module[name] = v
	}

	tags := map[string]string{} // the install's tags; the fixed ones win, and Project.Tags stays the fixed set
	maps.Copy(tags, s.Tags)
	maps.Copy(tags, p.Tags())

	expr := func(name string) map[string]any {
		return map[string]any{"value": "${module.dormeto." + name + "}"}
	}
	cfg := map[string]any{
		"output": map[string]any{
			"dashboard_url": expr("dashboard_url"),
			"user_pool_id":  expr("user_pool_id"),
			"version":       expr("version"),
		},
		"module": map[string]any{"dormeto": module},
		"provider": map[string]any{"aws": map[string]any{
			"region":       p.Region,
			"default_tags": map[string]any{"tags": tags},
		}},
		"terraform": map[string]any{"backend": map[string]any{"s3": map[string]any{
			"bucket": p.Bucket, "key": p.StateKey(), "region": p.Region, "use_lockfile": true, "encrypt": true,
		}}},
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil // Encode ends with a newline
}

// metaArguments are the module block's own arguments: a variable cannot take their names.
var metaArguments = []string{"source", "version", "providers", "count", "for_each", "depends_on"}

// esc keeps Terraform from interpolating a literal ${ or running a literal %{ directive of a user's value.
func esc(s string) string { return strings.NewReplacer("${", "$${", "%{", "%%{").Replace(s) }

func escAll(l []string) []string {
	out := make([]string, len(l))
	for i, s := range l {
		out[i] = esc(s)
	}
	return out
}

// escRaw is a raw JSON value, validated and with the ${ of its strings escaped; numbers keep their digits.
func escRaw(raw json.RawMessage) (any, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	dec := json.NewDecoder(&compact)
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return escValue(v), nil
}

func escValue(v any) any {
	switch t := v.(type) {
	case string:
		return esc(t)
	case []any:
		for i := range t {
			t[i] = escValue(t[i])
		}
	case map[string]any:
		for k := range t {
			t[k] = escValue(t[k])
		}
	}
	return v
}
