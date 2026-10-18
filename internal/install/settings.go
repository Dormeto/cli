// Package install is `dormeto install`: the manager installed in an AWS account (or an organization) by CodeBuild, from
// the CLI. This part holds the install's settings, kept as install.json, and its terminal prompts, as the design spec
// describes them (vision docs/specs/2026-10-17-cli-install-design.md, sections 3 and 6).
package install

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Domain is the custom domain of the dashboard and its sign-in page.
type Domain struct {
	Dashboard string `json:"dashboard"`
	// The sign-in host; empty: AuthHost's default.
	Auth string `json:"auth"`
	// The ACM certificate, in us-east-1.
	CertificateARN string `json:"certificateArn"`
	ZoneID         string `json:"zoneId"`
}

// AuthHost is the sign-in host: Auth, or auth.<Dashboard> when empty.
func (d Domain) AuthHost() string {
	if d.Auth != "" {
		return d.Auth
	}
	return "auth." + d.Dashboard
}

// Settings is what the install asks and remembers (install.json): the flags' values, then the install's own state.
type Settings struct {
	Env             string                     `json:"env"`
	Region          string                     `json:"region"`
	Version         string                     `json:"version"`
	Mode            string                     `json:"mode"` // "organization" | "account"
	AdminEmails     []string                   `json:"adminEmails"`
	OrganizationID  string                     `json:"organizationId,omitempty"`
	Domain          *Domain                    `json:"domain,omitempty"`
	SESIdentity     string                     `json:"sesIdentity,omitempty"`
	EmailFrom       string                     `json:"emailFrom,omitempty"`
	Vars            map[string]json.RawMessage `json:"vars,omitempty"`
	Tags            map[string]string          `json:"tags,omitempty"` // the Terraform default tags, after the fixed ones
	MemberRole      string                     `json:"memberRole,omitempty"`
	Accounts        []string                   `json:"accounts,omitempty"`
	ExcludeAccounts []string                   `json:"excludeAccounts,omitempty"`
	DiscoveryOnly   []string                   `json:"discoveryOnly,omitempty"`
	BuildRole       string                     `json:"buildRole,omitempty"`
	ReleaseBase     string                     `json:"releaseBase,omitempty"`  // the base the plan used, for a later install apply
	Installed       string                     `json:"installed,omitempty"`    // the version applied
	LastPlan        string                     `json:"lastPlan,omitempty"`     // build ids
	LastPlanKind    string                     `json:"lastPlanKind,omitempty"` // planApply or planDestroy; "": planApply
	LastApply       string                     `json:"lastApply,omitempty"`
	AdoptedFrom     *AdoptedFrom               `json:"adoptedFrom,omitempty"`   // the state adopted (--adopt-state)
	AdoptedModule   string                     `json:"adoptedModule,omitempty"` // its module's name in that state
}

// AdoptedFrom is the state file an install adopted: its base name and its SHA-256.
type AdoptedFrom struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

// The kinds of the last plan (Settings.LastPlanKind): each explicit apply applies its own kind only (ruling R15).
const (
	planApply   = "apply"   // install's: dormeto install apply applies it
	planDestroy = "destroy" // uninstall's: dormeto uninstall apply applies it
)

var (
	envRe        = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,8}[a-z0-9])?$`)
	regionRe     = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d{1,2}$`)
	versionRe    = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	emailRe      = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	certRe       = regexp.MustCompile(`^arn:aws[a-z-]*:acm:us-east-1:\d{12}:certificate/.+$`)
	varNameRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	roleNameRe   = regexp.MustCompile(`^([A-Za-z0-9+=,.@_-]+/)*[A-Za-z0-9+=,.@_-]{1,64}$`)
	accountIDRe  = regexp.MustCompile(`^\d{12}$`)
	buildRoleRe  = regexp.MustCompile(`^arn:aws[a-z-]*:iam::\d{12}:role/.+$`)
	tagCharsRe   = regexp.MustCompile(`^[\p{L}\p{Z}\p{N}_.:/=+\-@]*$`)
	reservedVars = []string{"env", "organization_id", "discovery_mode", "admin_emails", "domain", "ses_identity", "email_from", "install"}
)

// Validate checks the settings and returns the first failure, its message beginning with the flag it is about.
func (s Settings) Validate() error {
	if err := validEnv(s.Env); err != nil {
		return err
	}
	if err := validRegion(s.Region); err != nil {
		return err
	}
	if s.Version != "" && !versionRe.MatchString(s.Version) {
		return fmt.Errorf("--version: %q is not a version (for example 0.11.0)", s.Version)
	}
	if s.Mode != "organization" && s.Mode != "account" {
		return fmt.Errorf("--mode: %q is neither organization nor account", s.Mode)
	}
	if len(s.AdminEmails) == 0 {
		return fmt.Errorf("--admin-emails: at least one email is required")
	}
	for _, e := range s.AdminEmails {
		if !emailRe.MatchString(e) {
			return fmt.Errorf("--admin-emails: %q is not an email address", e)
		}
	}
	if d := s.Domain; d != nil {
		switch {
		case d.Dashboard == "":
			return fmt.Errorf("--domain: the dashboard host is required with the other domain flags")
		case d.CertificateARN == "":
			return fmt.Errorf("--certificate-arn: required with --domain")
		case d.ZoneID == "":
			return fmt.Errorf("--zone-id: required with --domain")
		case !certRe.MatchString(d.CertificateARN):
			return fmt.Errorf("--certificate-arn: %q is not an ACM certificate ARN in us-east-1", d.CertificateARN)
		}
	}
	if s.EmailFrom != "" && s.SESIdentity == "" {
		return fmt.Errorf("--email-from: only with --ses-identity")
	}
	for _, name := range slices.Sorted(maps.Keys(s.Vars)) {
		if slices.Contains(reservedVars, name) {
			return fmt.Errorf("--var: %q is set by its own flag", name)
		}
		if !varNameRe.MatchString(name) {
			return fmt.Errorf("--var: %q is not a variable name (lowercase letters, digits and underscores, a letter first)", name)
		}
		if !json.Valid(s.Vars[name]) {
			return fmt.Errorf("--var: the value of %q is not valid JSON", name)
		}
	}
	if err := ValidateTags(s.Tags); err != nil {
		return err
	}
	if s.MemberRole != "" && !roleNameRe.MatchString(s.MemberRole) {
		return fmt.Errorf("--member-role: %q is not an IAM role name", s.MemberRole)
	}
	for _, l := range []struct {
		flag string
		ids  []string
	}{{"accounts", s.Accounts}, {"exclude-accounts", s.ExcludeAccounts}, {"discovery-only", s.DiscoveryOnly}} {
		for _, id := range l.ids {
			if !accountIDRe.MatchString(id) {
				return fmt.Errorf("--%s: %q is not a 12-digit account id", l.flag, id)
			}
		}
	}
	if s.BuildRole != "" && !buildRoleRe.MatchString(s.BuildRole) {
		return fmt.Errorf("--build-role: %q is not an IAM role ARN", s.BuildRole)
	}
	return nil
}

// maxTags is how many tags the install adds: AWS allows 50 per resource, the fixed three and room for tags set on a
// resource itself stay.
const maxTags = 40

// fixedTagKeys are the keys of Project.Tags, which the install's tags cannot take.
var fixedTagKeys = []string{"project", "managed-by"}

// ValidateTags checks the install's tags (--tag), its message beginning with --tag and naming the faulty tag.
func ValidateTags(tags map[string]string) error {
	if len(tags) > maxTags {
		return fmt.Errorf("--tag: %d tags given, at most %d", len(tags), maxTags)
	}
	for _, key := range slices.Sorted(maps.Keys(tags)) {
		lower := strings.ToLower(key)
		switch n := utf8.RuneCountInString(key); {
		case n < 1 || n > 128:
			return fmt.Errorf("--tag: the key %q is not 1 to 128 characters", key)
		case !tagCharsRe.MatchString(key):
			return fmt.Errorf("--tag: the key %q has a character AWS does not allow (letters, digits, spaces and _ . : / = + - @)", key)
		case strings.HasPrefix(lower, "aws:"):
			return fmt.Errorf("--tag: %q begins with aws:, which is AWS's", key)
		case strings.HasPrefix(lower, "dormeto:"):
			return fmt.Errorf("--tag: %q begins with dormeto:, which is Dormeto's", key)
		case slices.Contains(fixedTagKeys, key):
			return fmt.Errorf("--tag: %q is set by the install", key)
		}
		if v := tags[key]; utf8.RuneCountInString(v) > 256 {
			return fmt.Errorf("--tag: the value of %q is longer than 256 characters", key)
		} else if !tagCharsRe.MatchString(v) {
			return fmt.Errorf("--tag: the value of %q has a character AWS does not allow (letters, digits, spaces and _ . : / = + - @)", key)
		}
	}
	return nil
}

// ParseTags reads the --tag values (key=value, the value up to the end after the first =). Nothing given is nil; "" alone
// is an empty map, which removes every tag.
func ParseTags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	tags := map[string]string{}
	for _, v := range values {
		if v == "" {
			if len(values) > 1 {
				return nil, fmt.Errorf("--tag '' removes every tag: give it alone")
			}
			continue
		}
		key, value, ok := strings.Cut(v, "=")
		if !ok {
			return nil, fmt.Errorf("--tag: %q is not key=value", v)
		}
		if _, dup := tags[key]; dup {
			return nil, fmt.Errorf("--tag: the key %q is given twice", key)
		}
		tags[key] = value
	}
	return tags, nil
}

// validEnv checks an env (it names the install's keys and resources), its message beginning with --env.
func validEnv(env string) error {
	if !envRe.MatchString(env) {
		return fmt.Errorf("--env: %q is not 1 to 10 lowercase letters, digits or inner hyphens", env)
	}
	return nil
}

// validRegion checks a region, its message beginning with --region.
func validRegion(region string) error {
	if !regionRe.MatchString(region) {
		return fmt.Errorf("--region: %q is not an AWS region (for example eu-west-1)", region)
	}
	return nil
}

// Merge is the settings a run uses: saved (install.json, or zero on a first install), each field whose flag is in set
// (the flag names) replaced by the flag's value. Vars merge key by key, a flag's key winning; Tags are replaced whole.
// The install's own state (Installed, LastPlan, LastPlanKind, LastApply) is always saved's.
func Merge(flags, saved Settings, set map[string]bool) Settings {
	s := saved
	if set["env"] {
		s.Env = flags.Env
	}
	if set["region"] {
		s.Region = flags.Region
	}
	if set["version"] {
		s.Version = flags.Version
	}
	if set["mode"] {
		s.Mode = flags.Mode
	}
	if set["admin-emails"] {
		s.AdminEmails = flags.AdminEmails
	}
	if set["domain"] {
		s.Domain = flags.Domain
	}
	if set["ses-identity"] {
		s.SESIdentity = flags.SESIdentity
	}
	if set["email-from"] {
		s.EmailFrom = flags.EmailFrom
	}
	if set["var"] {
		vars := maps.Clone(saved.Vars)
		if vars == nil {
			vars = map[string]json.RawMessage{}
		}
		maps.Copy(vars, flags.Vars)
		s.Vars = vars
	}
	if set["tag"] {
		s.Tags = flags.Tags // the whole set, as the administrators: a tag is removed by leaving it out
	}
	if set["member-role"] {
		s.MemberRole = flags.MemberRole
	}
	if set["accounts"] {
		s.Accounts = flags.Accounts
	}
	if set["exclude-accounts"] {
		s.ExcludeAccounts = flags.ExcludeAccounts
	}
	if set["discovery-only"] {
		s.DiscoveryOnly = flags.DiscoveryOnly
	}
	if set["build-role"] {
		s.BuildRole = flags.BuildRole
	}
	return s
}

// JSON is install.json: indented by two spaces, ending with a newline.
func (s Settings) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParseSettings reads install.json.
func ParseSettings(b []byte) (Settings, error) {
	var s Settings
	if err := json.Unmarshal(b, &s); err != nil {
		return Settings{}, fmt.Errorf("install.json: %w", err)
	}
	return s, nil
}
