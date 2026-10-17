// The install's settings: the checks, the merge of flags over install.json, and its JSON.
package install

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const certARN = "arn:aws:acm:us-east-1:123456789012:certificate/abc-123"

// valid is the smallest settings that pass every check.
func valid() Settings {
	return Settings{Env: "prd", Region: "eu-west-1", Mode: "account", AdminEmails: []string{"a@example.com"}}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name   string
		change func(s *Settings)
		flag   string // empty: passes
	}{
		{"minimal", func(s *Settings) {}, ""},
		{"env single char", func(s *Settings) { s.Env = "a" }, ""},
		{"env inner hyphen", func(s *Settings) { s.Env = "my-env1" }, ""},
		{"env empty", func(s *Settings) { s.Env = "" }, "--env"},
		{"env upper", func(s *Settings) { s.Env = "Prd" }, "--env"},
		{"env trailing hyphen", func(s *Settings) { s.Env = "prd-" }, "--env"},
		{"env too long", func(s *Settings) { s.Env = "abcdefghijk" }, "--env"},
		{"env ten", func(s *Settings) { s.Env = "abcdefghij" }, ""},
		{"region empty", func(s *Settings) { s.Region = "" }, "--region"},
		{"region bad", func(s *Settings) { s.Region = "europe" }, "--region"},
		{"region gov", func(s *Settings) { s.Region = "us-gov-west-1" }, ""},
		{"version empty", func(s *Settings) { s.Version = "" }, ""},
		{"version", func(s *Settings) { s.Version = "0.11.0" }, ""},
		{"version pre", func(s *Settings) { s.Version = "0.11.0-rc.1" }, ""},
		{"version bad", func(s *Settings) { s.Version = "v0.11" }, "--version"},
		{"mode org", func(s *Settings) { s.Mode = "organization" }, ""},
		{"mode bad", func(s *Settings) { s.Mode = "both" }, "--mode"},
		{"mode empty", func(s *Settings) { s.Mode = "" }, "--mode"},
		{"emails none", func(s *Settings) { s.AdminEmails = nil }, "--admin-emails"},
		{"emails bad", func(s *Settings) { s.AdminEmails = []string{"a@example.com", "nope"} }, "--admin-emails"},
		{"domain nil", func(s *Settings) { s.Domain = nil }, ""},
		{"domain ok", func(s *Settings) { s.Domain = &Domain{Dashboard: "d.example.com", CertificateARN: certARN, ZoneID: "Z1"} }, ""},
		{"domain no dashboard", func(s *Settings) { s.Domain = &Domain{CertificateARN: certARN, ZoneID: "Z1"} }, "--domain"},
		{"domain no certificate", func(s *Settings) { s.Domain = &Domain{Dashboard: "d.example.com", ZoneID: "Z1"} }, "--certificate-arn"},
		{"domain no zone", func(s *Settings) { s.Domain = &Domain{Dashboard: "d.example.com", CertificateARN: certARN} }, "--zone-id"},
		{"domain bad region cert", func(s *Settings) {
			s.Domain = &Domain{Dashboard: "d.example.com", CertificateARN: "arn:aws:acm:eu-west-1:123456789012:certificate/x", ZoneID: "Z1"}
		}, "--certificate-arn"},
		{"email-from alone", func(s *Settings) { s.EmailFrom = "x@example.com" }, "--email-from"},
		{"email-from with identity", func(s *Settings) { s.SESIdentity = "example.com"; s.EmailFrom = "x@example.com" }, ""},
		{"var ok", func(s *Settings) { s.Vars = map[string]json.RawMessage{"log_level": json.RawMessage(`"debug"`)} }, ""},
		{"var reserved", func(s *Settings) { s.Vars = map[string]json.RawMessage{"env": json.RawMessage(`"x"`)} }, "--var"},
		{"var reserved domain", func(s *Settings) { s.Vars = map[string]json.RawMessage{"domain": json.RawMessage(`{}`)} }, "--var"},
		{"var bad name", func(s *Settings) { s.Vars = map[string]json.RawMessage{"Log-Level": json.RawMessage(`1`)} }, "--var"},
		{"var bad json", func(s *Settings) { s.Vars = map[string]json.RawMessage{"x": json.RawMessage(`{oops`)} }, "--var"},
		{"member role", func(s *Settings) { s.MemberRole = "OrganizationAccountAccessRole" }, ""},
		{"member role path", func(s *Settings) { s.MemberRole = "svc/team/Role" }, ""},
		{"member role bad", func(s *Settings) { s.MemberRole = "bad role" }, "--member-role"},
		{"accounts ok", func(s *Settings) { s.Accounts = []string{"123456789012"} }, ""},
		{"accounts bad", func(s *Settings) { s.Accounts = []string{"1234"} }, "--accounts"},
		{"exclude bad", func(s *Settings) { s.ExcludeAccounts = []string{"12345678901x"} }, "--exclude-accounts"},
		{"discovery bad", func(s *Settings) { s.DiscoveryOnly = []string{"x"} }, "--discovery-only"},
		{"build role ok", func(s *Settings) { s.BuildRole = "arn:aws:iam::123456789012:role/path/R" }, ""},
		{"build role bad", func(s *Settings) { s.BuildRole = "R" }, "--build-role"},
		{"first failing wins", func(s *Settings) { s.Env = "X"; s.Region = "" }, "--env"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := valid()
			c.change(&s)
			err := s.Validate()
			if c.flag == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), c.flag+": ") {
				t.Fatalf("error %v, want prefix %q", err, c.flag+": ")
			}
		})
	}
}

func TestAuthHost(t *testing.T) {
	if got := (Domain{Dashboard: "d.example.com"}).AuthHost(); got != "auth.d.example.com" {
		t.Fatalf("default: %q", got)
	}
	if got := (Domain{Dashboard: "d.example.com", Auth: "login.example.com"}).AuthHost(); got != "login.example.com" {
		t.Fatalf("explicit: %q", got)
	}
}

func TestMerge(t *testing.T) {
	saved := Settings{
		Env: "prd", Region: "eu-west-1", Version: "0.10.0", Mode: "organization", AdminEmails: []string{"old@example.com"},
		Domain:     &Domain{Dashboard: "old.example.com", CertificateARN: certARN, ZoneID: "Z1"},
		Vars:       map[string]json.RawMessage{"a": json.RawMessage(`1`), "b": json.RawMessage(`2`)},
		MemberRole: "OldRole", Accounts: []string{"111111111111"}, BuildRole: "arn:aws:iam::111111111111:role/B",
		Installed: "0.10.0", LastPlan: "plan-1", LastApply: "apply-1",
	}
	flags := Settings{
		Env: "dev", Region: "us-east-1", Version: "0.11.0", Mode: "account", AdminEmails: []string{"new@example.com"},
		Domain: &Domain{Dashboard: "new.example.com", CertificateARN: certARN, ZoneID: "Z2"}, SESIdentity: "ses", EmailFrom: "f@x.io",
		Vars:       map[string]json.RawMessage{"b": json.RawMessage(`3`), "c": json.RawMessage(`4`)},
		MemberRole: "NewRole", Accounts: []string{"222222222222"}, ExcludeAccounts: []string{"333333333333"},
		DiscoveryOnly: []string{"444444444444"}, BuildRole: "arn:aws:iam::222222222222:role/B",
		Installed: "9.9.9", LastPlan: "x", LastApply: "y",
	}

	t.Run("nothing set keeps saved", func(t *testing.T) {
		if got := Merge(flags, saved, nil); !reflect.DeepEqual(got, saved) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("all set takes flags, state from saved", func(t *testing.T) {
		set := map[string]bool{}
		for _, k := range []string{"env", "region", "version", "mode", "admin-emails", "domain", "ses-identity", "email-from",
			"var", "member-role", "accounts", "exclude-accounts", "discovery-only", "build-role"} {
			set[k] = true
		}
		got := Merge(flags, saved, set)
		want := flags
		want.Vars = map[string]json.RawMessage{"a": json.RawMessage(`1`), "b": json.RawMessage(`3`), "c": json.RawMessage(`4`)}
		want.Installed, want.LastPlan, want.LastApply = "0.10.0", "plan-1", "apply-1"
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v\nwant %+v", got, want)
		}
	})
	t.Run("one set", func(t *testing.T) {
		got := Merge(flags, saved, map[string]bool{"region": true})
		want := saved
		want.Region = "us-east-1"
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("zero saved", func(t *testing.T) {
		got := Merge(flags, Settings{}, map[string]bool{"env": true, "var": true})
		if got.Env != "dev" || got.Region != "" || len(got.Vars) != 2 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("saved vars not modified", func(t *testing.T) {
		Merge(flags, saved, map[string]bool{"var": true})
		if len(saved.Vars) != 2 || string(saved.Vars["b"]) != "2" {
			t.Fatalf("saved changed: %v", saved.Vars)
		}
	})
}

func TestJSONRoundTrip(t *testing.T) {
	s := valid()
	s.Version = "0.11.0"
	s.Domain = &Domain{Dashboard: "d.example.com", Auth: "auth.d.example.com", CertificateARN: certARN, ZoneID: "Z1"}
	s.Vars = map[string]json.RawMessage{"x": json.RawMessage(`{"a":1}`)}
	s.Accounts = []string{"123456789012"}
	s.Installed = "0.11.0"
	b, err := s.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "}\n") || !strings.Contains(string(b), "\n  \"env\": \"prd\"") {
		t.Fatalf("format: %q", b)
	}
	got, err := ParseSettings(b)
	if err != nil {
		t.Fatal(err)
	}
	// RawMessage keeps its indentation after a round trip: compare values.
	if string(got.Vars["x"]) == "" {
		t.Fatalf("vars lost: %+v", got)
	}
	var a, w any
	_ = json.Unmarshal(got.Vars["x"], &a)
	_ = json.Unmarshal(s.Vars["x"], &w)
	if !reflect.DeepEqual(a, w) {
		t.Fatalf("vars %v != %v", a, w)
	}
	got.Vars = s.Vars
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("got %+v\nwant %+v", got, s)
	}
}

func TestJSONOmitsEmpty(t *testing.T) {
	b, err := valid().JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"organizationId", "domain", "sesIdentity", "emailFrom", "vars", "memberRole", "accounts",
		"excludeAccounts", "discoveryOnly", "buildRole", "installed", "lastPlan", "lastApply"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("%s not omitted: %s", k, b)
		}
	}
	for _, k := range []string{"env", "region", "version", "mode", "adminEmails"} {
		if !strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("%s missing: %s", k, b)
		}
	}
}

func TestParseSettingsInvalid(t *testing.T) {
	if _, err := ParseSettings([]byte("{nope")); err == nil {
		t.Fatal("want an error")
	}
}
