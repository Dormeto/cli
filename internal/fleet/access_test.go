package fleet

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseGrant(t *testing.T) {
	ok := map[string]Grant{
		"team=a,env=dev:read,wake": {Tags: map[string]string{"team": "a", "env": "dev"}, Permissions: []string{"read", "wake"}},
		":read":                    {Tags: map[string]string{}, Permissions: []string{"read"}},
		"a:b=c:nap":                {Tags: map[string]string{"a:b": "c"}, Permissions: []string{"nap"}},
		"team=:read,wake,nap":      {Tags: map[string]string{"team": ""}, Permissions: []string{"read", "wake", "nap"}},
	}
	for in, want := range ok {
		if got, err := ParseGrant(in); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseGrant(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	bad := []string{"team=a:", "team=a:write", "team=a", "team:read", "=x:read", "a=1,a=2:read", "dormeto:x=1:read", "a=b c!:read", "team=a:read,,wake"}
	for _, in := range bad {
		if g, err := ParseGrant(in); err == nil {
			t.Errorf("ParseGrant(%q) = %+v, want an error", in, g)
		}
	}
}

func TestParseGrantsAtMostTwenty(t *testing.T) {
	var vs []string
	for range 20 {
		vs = append(vs, ":read")
	}
	if gs, err := ParseGrants(vs); err != nil || len(gs) != 20 {
		t.Fatalf("%d grants, %v", len(gs), err)
	}
	if _, err := ParseGrants(append(vs, ":read")); err == nil || !strings.Contains(err.Error(), "at most 20") {
		t.Errorf("err = %v", err)
	}
	if gs, err := ParseGrants(nil); err != nil || gs == nil || len(gs) != 0 {
		t.Errorf("ParseGrants(nil) = %#v, %v: want an empty, non-nil list", gs, err)
	}
}

func TestAccessRules(t *testing.T) {
	if e, err := NormalEmail("  Ana+x@Example.IO "); err != nil || e != "ana+x@example.io" {
		t.Errorf("email %q, %v", e, err)
	}
	for _, s := range []string{"", "a@b", "a b@c.d", "@c.d", "a@@c.d"} {
		if _, err := NormalEmail(s); err == nil {
			t.Errorf("NormalEmail(%q) accepted", s)
		}
	}
	for _, n := range []string{"dmt-x", "Bad", "", "-a", strings.Repeat("a", 49)} {
		if CheckGroupName(n) == nil {
			t.Errorf("CheckGroupName(%q) accepted", n)
		}
	}
	for _, n := range []string{"a", "team-1", "0x", strings.Repeat("a", 48), "dmt"} {
		if err := CheckGroupName(n); err != nil {
			t.Errorf("CheckGroupName(%q): %v", n, err)
		}
	}
	if CheckDescription(strings.Repeat("é", 200)) != nil || CheckDescription(strings.Repeat("a", 201)) == nil {
		t.Error("description limit is 200 characters")
	}
	if CheckRole("admin") != nil || CheckRole("root") == nil {
		t.Error("roles")
	}
}

func TestAccessText(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	yes := true
	us := []User{
		{Email: "a@x.io", Role: "admin", Groups: []string{"ops", "dev"}, Status: "CONFIRMED", Enabled: true, CreatedAt: "2026-10-17T12:00:00Z", MFA: &yes},
		{Email: "b@x.io", Role: "member", Status: "FORCE_CHANGE_PASSWORD", Enabled: false},
	}
	got := UsersText(us, now)
	for _, want := range []string{"EMAIL", "ops,dev", "CONFIRMED", "yes", "2d", "disabled"} {
		if !strings.Contains(got, want) {
			t.Errorf("users text lacks %q:\n%s", want, got)
		}
	}
	g := Group{Name: "pay", Description: "payments", UpdatedAt: "2026-10-19T09:00:00Z", UpdatedBy: "arn:aws:sts::1:assumed-role/Ops/ana",
		Grants: []Grant{{Tags: map[string]string{"team": "a", "env": "dev"}, Permissions: []string{"read", "wake"}}, {Tags: map[string]string{}, Permissions: []string{"read"}}}}
	got = GroupText(g, time.UTC)
	for _, want := range []string{"2026-10-19 09:00 UTC by ana", "env=dev,team=a: read,wake", "every workload: read"} {
		if !strings.Contains(got, want) {
			t.Errorf("group text lacks %q:\n%s", want, got)
		}
	}
	if UsersText(nil, now) != "no users\n" || GroupsText(nil, now) != "no groups\n" {
		t.Error("empty lists")
	}
}
