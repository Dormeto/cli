// The install's project names and the generated Terraform configuration (golden files, updated with -update).
package install

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func TestNames(t *testing.T) {
	p := Names("532105290256", "eu-west-1", "prod")
	want := Project{
		Account: "532105290256", Region: "eu-west-1", Env: "prod",
		Bucket: "dmt-install-532105290256-eu-west-1", Name: "dmt-prod-install", Role: "dmt-prod-install",
		LogGroup: "/aws/codebuild/dmt-prod-install",
	}
	if p != want {
		t.Fatalf("Names = %+v, want %+v", p, want)
	}
	if got := p.Key("plans", "abc", "tfplan"); got != "prod/plans/abc/tfplan" {
		t.Errorf("Key = %q", got)
	}
	if got := p.Key("install.json"); got != "prod/install.json" {
		t.Errorf("Key = %q", got)
	}
	if got := p.StateKey(); got != "prod/manager.tfstate" {
		t.Errorf("StateKey = %q", got)
	}
	tags := map[string]string{"project": "dormeto", "dormeto:env": "prod", "managed-by": "dormeto-cli"}
	if got := p.Tags(); !reflect.DeepEqual(got, tags) {
		t.Errorf("Tags = %v", got)
	}
}

func TestConfig(t *testing.T) {
	org := &Organization{ID: "o-vvk0115ixn", Management: "111111111111", AllFeatures: true}
	where := func(o *Organization) Where {
		return Where{Account: "532105290256", Region: "eu-west-1", Organization: o}
	}
	base := func(mode string) Settings {
		return Settings{Env: "prod", Region: "eu-west-1", Mode: mode, AdminEmails: []string{"admin@example.com"}}
	}
	domain := base("account")
	domain.Domain = &Domain{Dashboard: "dormeto.example.com", CertificateARN: certARN, ZoneID: "Z123"}
	domain.SESIdentity = "example.com"
	domain.EmailFrom = "Dormeto <no-reply@example.com>"
	vars := base("account")
	vars.Vars = map[string]json.RawMessage{"history_days": json.RawMessage(` 90 `), "alert_emails": json.RawMessage(`[ "a@example.com" ]`)}
	esc := base("account")
	esc.AdminEmails = []string{`we"ird@example.com`}
	esc.EmailFrom = "x ${x}"
	esc.SESIdentity = "id"
	esc.Vars = map[string]json.RawMessage{"description": json.RawMessage(`{"a": ["${y}"]}`)}

	cases := []struct {
		name string
		s    Settings
		w    Where
	}{
		{"organization", base("organization"), where(org)},
		{"account-outside", base("account"), where(nil)},
		{"account-inside", base("account"), where(org)},
		{"domain", domain, where(nil)},
		{"vars", vars, where(nil)},
		{"escaping", esc, where(nil)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Names(c.w.Account, c.s.Region, c.s.Env)
			got, err := Config(c.s, c.w, p)
			if err != nil {
				t.Fatal(err)
			}
			again, err := Config(c.s, c.w, p)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, again) {
				t.Error("two runs give different bytes")
			}
			if !json.Valid(got) || !bytes.HasSuffix(got, []byte("}\n")) {
				t.Fatalf("not JSON ending with a newline:\n%s", got)
			}
			file := filepath.Join("testdata", "config", c.name+".tf.json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs (run with -update):\n%s", file, got)
			}
		})
	}

	t.Run("escaping content", func(t *testing.T) {
		got, err := Config(esc, where(nil), Names("532105290256", "eu-west-1", "prod"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), `x $${x}`) || !strings.Contains(string(got), `$${y}`) {
			t.Errorf("${ not escaped:\n%s", got)
		}
		if !strings.Contains(string(got), `${module.dormeto.dashboard_url}`) {
			t.Errorf("output expression lost:\n%s", got)
		}
		var v map[string]any
		if err := json.Unmarshal(got, &v); err != nil {
			t.Fatal(err)
		}
		m := v["module"].(map[string]any)["dormeto"].(map[string]any)
		if m["admin_emails"].([]any)[0] != `we"ird@example.com` {
			t.Errorf("admin_emails = %v", m["admin_emails"])
		}
	})

	t.Run("invalid var", func(t *testing.T) {
		s := base("account")
		s.Vars = map[string]json.RawMessage{"bad": json.RawMessage(`{`)}
		if _, err := Config(s, where(nil), Names("1", "eu-west-1", "prod")); err == nil {
			t.Error("want an error")
		}
	})

	t.Run("no zone", func(t *testing.T) {
		s := base("account")
		s.Domain = &Domain{Dashboard: "d.example.com", CertificateARN: certARN}
		got, err := Config(s, where(nil), Names("1", "eu-west-1", "prod"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(got), "zone_id") {
			t.Errorf("zone_id emitted without a zone:\n%s", got)
		}
	})

	t.Run("template directive", func(t *testing.T) {
		s := base("account")
		s.EmailFrom, s.SESIdentity = "%{if x}", "id"
		s.Vars = map[string]json.RawMessage{"note": json.RawMessage(`["%{for}"]`)}
		got, err := Config(s, where(nil), Names("1", "eu-west-1", "prod"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), `%%{if x}`) || !strings.Contains(string(got), `%%{for}`) {
			t.Errorf("%%{ not escaped:\n%s", got)
		}
	})

	t.Run("meta-arguments", func(t *testing.T) {
		for _, name := range []string{"source", "version", "providers", "count", "for_each", "depends_on"} {
			s := base("account")
			s.Vars = map[string]json.RawMessage{name: json.RawMessage(`1`)}
			_, err := Config(s, where(nil), Names("1", "eu-west-1", "prod"))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("%s: err = %v", name, err)
			}
		}
	})
}
