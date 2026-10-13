// The estimate command's options (ported from manager packages/estimate/test/options.test.ts).
package options

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/prices"
)

func must(t *testing.T, args ...string) Options {
	t.Helper()
	o, err := Parse(args)
	if err != nil {
		t.Fatalf("Parse(%q): %v", args, err)
	}
	return o
}

func f(v float64) *float64 { return &v }
func s(v string) *string   { return &v }

func TestProfileAlone(t *testing.T) {
	cwd, _ := os.Getwd()
	want := Options{
		Profiles: []string{"prospect"}, ExcludeTags: []map[string][]string{}, Role: "OrganizationAccountAccessRole",
		Baseline: prices.Baseline{Kind: "always"}, Out: cwd, Window: 60, MaxWakes: f(3), MinSaving: f(1), Parallel: 5, MaxWakesRds: f(2),
	}
	if got := must(t, "--profile", "prospect"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestEveryOptionGiven(t *testing.T) {
	got := must(t, "--profile", "prospect", "--accounts", "222222222222, 333333333333,222222222222", "--regions", "eu-west-1,eu-west-3",
		"--exclude-tags", "env=dev|test, team=web", "--exclude-tags", "dormeto:ignore", "--ec2-uptime", "--role", "ReadOnlyAudit", "--baseline", "business-hours",
		"--timezone", "Europe/Paris", "--discount-compute", "20", "--discount-databases", "30", "--refresh", "metrics", "--out", "/tmp/reports",
		"--window", "30", "--max-wakes", "5", "--min-saving", "2.5", "--parallel", "2", "--max-wakes-rds", "1.5")
	want := Options{
		Profiles: []string{"prospect"}, Accounts: []string{"222222222222", "333333333333"}, Regions: []string{"eu-west-1", "eu-west-3"},
		ExcludeTags: []map[string][]string{{"env": {"dev", "test"}, "team": {"web"}}, {"dormeto:ignore": {}}},
		Ec2Uptime:   true, Role: "ReadOnlyAudit", Baseline: prices.Baseline{Kind: "business-hours", TimeZone: "Europe/Paris"},
		Discounts: prices.Discounts{Compute: 20, Databases: 30}, Refresh: s("metrics"), Out: "/tmp/reports", Window: 30,
		MaxWakes: f(5), MinSaving: f(2.5), Parallel: 2, MaxWakesRds: f(1.5),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if b := must(t, "--profile", "p", "--baseline", "business-hours").Baseline; b != (prices.Baseline{Kind: "business-hours", TimeZone: "UTC"}) {
		t.Fatalf("baseline %+v", b)
	}
	// A flag, anywhere: it takes no value.
	if o := must(t, "--ec2-uptime", "--profile", "p"); !reflect.DeepEqual(o.Profiles, []string{"p"}) || !o.Ec2Uptime {
		t.Fatalf("got %+v", o)
	}
}

func refuses(t *testing.T, args []string, message string) {
	t.Helper()
	_, err := Parse(args)
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("%q: want a *UsageError, got %v", args, err)
	}
	if !strings.Contains(err.Error(), message) {
		t.Fatalf("%q: message %q lacks %q", args, err.Error(), message)
	}
}

func TestMaxWakes(t *testing.T) {
	if got := must(t, "--profile", "p", "--max-wakes", "0.5").MaxWakes; got == nil || *got != 0.5 {
		t.Fatal(got)
	}
	if must(t, "--profile", "p", "--max-wakes", "none").MaxWakes != nil {
		t.Fatal("none must be nil")
	}
	for _, bad := range []string{"-1", "many", ""} {
		refuses(t, []string{"--profile", "p", "--max-wakes", bad}, "--max-wakes: a number of wakes a day, such as 3 or 0.5, or none")
	}
}

func TestMinSaving(t *testing.T) {
	if got := must(t, "--profile", "p", "--min-saving", "0.5").MinSaving; got == nil || *got != 0.5 {
		t.Fatal(got)
	}
	if must(t, "--profile", "p", "--min-saving", "none").MinSaving != nil {
		t.Fatal("none must be nil")
	}
	if got := must(t, "--profile", "p", "--min-saving", "0").MinSaving; got == nil || *got != 0 {
		t.Fatal(got)
	}
	for _, bad := range []string{"-1", "$1", ""} {
		refuses(t, []string{"--profile", "p", "--min-saving", bad}, "--min-saving: dollars a month, such as 1 or 0.5, or none")
	}
}

func TestMaxWakesRds(t *testing.T) {
	if got := must(t, "--profile", "p", "--max-wakes-rds", "0.5").MaxWakesRds; got == nil || *got != 0.5 {
		t.Fatal(got)
	}
	if must(t, "--profile", "p", "--max-wakes-rds", "none").MaxWakesRds != nil {
		t.Fatal("none must be nil")
	}
	for _, bad := range []string{"-1", "few"} {
		refuses(t, []string{"--profile", "p", "--max-wakes-rds", bad}, "--max-wakes-rds: a number of wakes a day, such as 2 or 0.5, or none")
	}
}

func TestParallel(t *testing.T) {
	for _, ok := range []int{1, 50} {
		if got := must(t, "--profile", "p", "--parallel", map[int]string{1: "1", 50: "50"}[ok]).Parallel; got != ok {
			t.Fatal(got)
		}
	}
	for _, bad := range []string{"0", "51", "-2", "2.5", "all"} {
		refuses(t, []string{"--profile", "p", "--parallel", bad}, "--parallel: accounts discovered at once, 1 to 50")
	}
}

func TestUptimeFlags(t *testing.T) {
	cases := []struct {
		args          []string
		ec2, ecs, rds bool
	}{
		{[]string{"--ecs-uptime"}, false, true, false},
		{[]string{"--uptime"}, true, true, true},
		{[]string{"--rds-uptime"}, false, false, true},
		{[]string{"--uptime", "--ec2-uptime"}, true, true, true},
	}
	for _, c := range cases {
		o := must(t, append([]string{"--profile", "p"}, c.args...)...)
		if o.Ec2Uptime != c.ec2 || o.EcsUptime != c.ecs || o.RdsUptime != c.rds {
			t.Errorf("%q: %+v", c.args, o)
		}
	}
}

func TestSeveralProfiles(t *testing.T) {
	if got := must(t, "--profile", "qal, prd,qal,pilot").Profiles; !reflect.DeepEqual(got, []string{"qal", "prd", "pilot"}) {
		t.Fatal(got)
	}
}

func TestRefuses(t *testing.T) {
	refused := []struct {
		args    []string
		message string
	}{
		{nil, "--profile is required"},
		{[]string{"--profile"}, "--profile needs a value"},
		{[]string{"--profile", " , "}, "--profile: one AWS profile or more, such as qal,prod"},
		{[]string{"--profile", "p", "--baseline", "nightly"}, "--baseline: always or business-hours"},
		{[]string{"--profile", "p", "--timezone", "Mars/Olympus"}, "--timezone: unknown time zone Mars/Olympus"},
		{[]string{"--profile", "p", "--timezone", ""}, "--timezone: unknown time zone "},
		{[]string{"--profile", "p", "--discount-compute", "91"}, "--discount-compute: a whole number from 0 to 90"},
		{[]string{"--profile", "p", "--discount-databases", "2.5"}, "--discount-databases: a whole number from 0 to 90"},
		{[]string{"--profile", "p", "--refresh", "all"}, "--refresh: discovery or metrics"},
		{[]string{"--profile", "p", "--regions", ""}, "--regions: one region or more, such as eu-west-1,us-east-1"},
		{[]string{"--profile", "p", "--regions", "eu-west-1,mars"}, "--regions: unknown region mars"},
		{[]string{"--profile", "p", "--accounts", ","}, "--accounts: one AWS account id or more, such as 111111111111,222222222222"},
		{[]string{"--profile", "p", "--accounts", "111111111111,12345"}, "--accounts: not an AWS account id: 12345"},
		{[]string{"--profile", "p", "--exclude-tags", " , "}, "--exclude-tags: one tag or more, such as env=dev|test,team=web"},
		{[]string{"--profile", "p", "--exclude-tags", "env=dev,=web"}, "--exclude-tags: a tag without its key: =web"},
		{[]string{"--profile", "p", "--exclude-tags", "env=dev,env=test"}, "--exclude-tags: env twice in one criterion: env=dev|test for either"},
		{[]string{"--profile", "p", "--ec2-uptime", "yes"}, "unknown option: yes"},
		{[]string{"--profile", "p", "--window", "4"}, "--window: whole minutes from 5 to 1440"},
		{[]string{"--profile", "p", "--window", "90.5"}, "--window: whole minutes from 5 to 1440"},
		{[]string{"--profile", "p", "--verbose"}, "unknown option: --verbose"},
	}
	for _, r := range refused {
		_, err := Parse(r.args)
		var ue *UsageError
		if !errors.As(err, &ue) || err.Error() != r.message {
			t.Errorf("%q: got %v, want exactly %q", r.args, err, r.message)
		}
	}
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"--profile", "p", "--help"}} {
		if _, err := Parse(args); !errors.Is(err, ErrHelp) {
			t.Errorf("%q: got %v", args, err)
		}
	}
	if !strings.HasPrefix(Usage, "Usage: dormeto estimate --profile <profile>") || strings.Contains(Usage, "npm run") {
		t.Fatal(Usage)
	}
}

func TestReportJSON(t *testing.T) {
	o := must(t, "--profile", "p")
	b, _ := json.Marshal(o)
	want := `{"accounts":null,"regions":null,"excludeTags":[],"ec2Uptime":false,"ecsUptime":false,"rdsUptime":false,"role":"OrganizationAccountAccessRole",` +
		`"baseline":{"kind":"always"},"discounts":{"compute":0,"databases":0},"refresh":null,"window":60,"maxWakes":3,"minSaving":1,"parallel":5,"maxWakesRds":2}`
	if string(b) != want {
		t.Fatalf("got %s", b)
	}
}
