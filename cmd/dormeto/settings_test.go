package main

// The hibernation setting from the command line against the fake manager API: `settings show|set`,
// `routers settings`, `workloads settings`, `workloads onboard --hibernate`, and the lines of `routers show` and
// `workloads show`.

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// globalAPI is a fake manager's /settings/workloads route.
type globalAPI struct {
	mu     sync.Mutex
	writes []string
}

func (a *globalAPI) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/iam/settings/workloads":
		io.WriteString(w, `{"hibernate":true,"updatedAt":"2026-10-19T10:00:00.000Z","updatedBy":"ann@x.io"}`)
	case r.Method == http.MethodPut && r.URL.Path == "/iam/settings/workloads":
		a.writes = append(a.writes, string(body))
		io.WriteString(w, `{"hibernate":false,"updatedAt":"2026-10-19T12:00:00.000Z","updatedBy":"ann@x.io","note":"routers apply it at their next check, within 5 minutes"}`)
	default:
		w.WriteHeader(404)
	}
}

func runSettings(t *testing.T, a *globalAPI, args ...string) fleetRan {
	t.Helper()
	return runFleetWith(t, &fakeAPI{custom: a.handler}, "eu-west-1", nil, append([]string{"settings"}, args...)...)
}

func TestSettingsShowPrintsTheGlobalHibernation(t *testing.T) {
	r := runSettings(t, &globalAPI{}, "show")
	if r.exit != 0 || !strings.Contains(r.stdout, "Hibernate: on (changed by ann@x.io at 2026-10-19 10:00 UTC)") {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
	r = runSettings(t, &globalAPI{}, "show", "--json")
	if r.exit != 0 || !strings.Contains(r.stdout, `"updatedBy": "ann@x.io"`) {
		t.Errorf("--json: exit %d, stdout %q", r.exit, r.stdout)
	}
}

func TestSettingsSetSendsTheBooleanAndPrintsTheNote(t *testing.T) {
	a := &globalAPI{}
	r := runSettings(t, a, "set", "--hibernate", "false")
	if r.exit != 0 || !reflect.DeepEqual(a.writes, []string{`{"hibernate":false}`}) {
		t.Fatalf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	if !strings.Contains(r.stdout, "Hibernate: off") || !strings.Contains(r.stdout, "routers apply it at their next check, within 5 minutes") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestSettingsSetRefusesABadOrMissingValueBeforeSending(t *testing.T) {
	for _, args := range [][]string{
		{"set", "--hibernate", "maybe"}, {"set", "--hibernate", "inherit"}, {"set"}, {"set", "--hibernate", ""},
	} {
		a := &globalAPI{}
		r := runSettings(t, a, args...)
		if r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("%q: exit %d, writes %q, stderr %q", args, r.exit, a.writes, r.stderr)
		}
	}
}

func TestParseHibernate(t *testing.T) {
	for in, want := range map[string]string{"true": "true", "false": "false", "inherit": "nil"} {
		got, err := parseHibernate(in, true)
		if err != nil || (want == "nil") != (got == nil) || (got != nil && *got != (want == "true")) {
			t.Errorf("%q: %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"maybe", "", "TRUE", "on"} {
		if _, err := parseHibernate(in, true); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
	if _, err := parseHibernate("inherit", false); err == nil {
		t.Error("inherit accepted where it is not allowed")
	}
}

func TestRoutersSettingsInheritSendsNull(t *testing.T) {
	a := &wrAPI{}
	r := runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", nil, "routers", "settings", "r1", "--hibernate", "inherit")
	if want := []string{`PUT /iam/routers/r1/settings {"hibernate":null}`}; r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestRoutersSettingsSeveralWithoutTerminalOrYesSendNothing(t *testing.T) {
	a := &wrAPI{}
	r := runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", nil, "routers", "settings", "r1", "r2", "--hibernate", "true")
	if r.exit != 2 || len(a.writes) != 0 {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	r = runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", nil, "routers", "settings", "r1", "r2", "--hibernate", "true", "--dry-run")
	if r.exit != 0 || len(a.writes) != 0 || !strings.Contains(r.stdout, "r2") {
		t.Errorf("--dry-run: exit %d, writes %q, stdout %q", r.exit, a.writes, r.stdout)
	}
	r = runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", nil, "routers", "settings", "r1", "r2", "--hibernate", "true", "--yes")
	want := []string{`PUT /iam/routers/r1/settings {"hibernate":true}`, `PUT /iam/routers/r2/settings {"hibernate":true}`}
	if r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Errorf("--yes: exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestRoutersSettingsRefusesABadValue(t *testing.T) {
	a := &wrAPI{}
	for _, args := range [][]string{{"r1", "--hibernate", "maybe"}, {"r1"}, {"--hibernate", "true"}} {
		r := runFleetWith(t, &fakeAPI{custom: a.handler()}, "eu-west-1", nil, append([]string{"routers", "settings"}, args...)...)
		if r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("%q: exit %d, writes %q", args, r.exit, a.writes)
		}
	}
}

func TestWorkloadsSettingsByTagPutsEachMatch(t *testing.T) {
	a := &wrAPI{}
	r := runWR(t, a, nil, "settings", "--tag", "team=a", "--hibernate", "true", "--yes")
	want := []string{
		`PUT /iam/workloads/prod-eu/w-1/settings {"hibernate":true}`,
		`PUT /iam/workloads/prod-eu/w-2/settings {"hibernate":true}`,
	}
	if r.exit != 0 || !reflect.DeepEqual(a.writes, want) {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestWorkloadsSettingsPrintsTheRefusalOfATerraformWorkload(t *testing.T) {
	msg := "workload w-1 was written by Terraform: set hibernate in its workload module"
	a := &wrAPI{refuse: map[string]string{"/iam/workloads/prod-eu/w-1/settings": msg}}
	r := runWR(t, a, nil, "settings", "prod-eu/w-1", "--hibernate", "inherit")
	if r.exit != 1 || !strings.Contains(r.stderr, "prod-eu/w-1: "+msg) {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
	if want := []string{`PUT /iam/workloads/prod-eu/w-1/settings {"hibernate":null}`}; !reflect.DeepEqual(a.writes, want) {
		t.Errorf("writes %q", a.writes)
	}
}

func TestWorkloadsSettingsRefusesABadValueOrNoTarget(t *testing.T) {
	for _, args := range [][]string{{"prod-eu/w-1", "--hibernate", "maybe"}, {"prod-eu/w-1"}, {"--hibernate", "true"}} {
		a := &wrAPI{}
		r := runWR(t, a, nil, append([]string{"settings"}, args...)...)
		if r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("%q: exit %d, writes %q", args, r.exit, a.writes)
		}
	}
}

func TestOnboardRefusesInheritAndABadHibernate(t *testing.T) {
	for _, v := range []string{"inherit", "maybe"} {
		a := &routersAPI{}
		r := runFleetWith(t, &fakeAPI{custom: a.handler}, "eu-west-1", nil, "workloads", "onboard", "tg", "--hibernate", v)
		if r.exit != 2 || len(a.posted) != 0 {
			t.Errorf("%s: exit %d, posted %q, stderr %q", v, r.exit, a.posted, r.stderr)
		}
	}
}

func TestRoutersShowPrintsTheHibernation(t *testing.T) {
	show := func(extra string) string {
		a := &routersAPI{}
		h := func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/iam/routers/prod-eu" {
				io.WriteString(w, strings.Replace(routerDetailsAnswer, `"pauses":`, extra+`"pauses":`, 1))
				return
			}
			a.handler(w, r)
		}
		r := runFleetWith(t, &fakeAPI{custom: h}, "eu-west-1", nil, "routers", "show", "prod-eu")
		if r.exit != 0 {
			t.Fatalf("exit %d: %s", r.exit, r.stderr)
		}
		return r.stdout
	}
	for name, c := range map[string]struct{ extra, want string }{
		"inherit": {`"settings":{"hibernate":null,"by":null,"at":null},"managerSettings":{"url":"s3://b/settings.json","hibernate":true,"readAt":"2026-10-19T11:58:00.000Z","error":null},`, "inherit (manager: on, read 2m ago)"},
		"error":   {`"settings":{"hibernate":null},"managerSettings":{"url":"s3://b/settings.json","hibernate":true,"readAt":"2026-10-19T11:58:00.000Z","error":"AccessDenied"},`, "inherit (manager: on, read 2m ago), AccessDenied"},
		"default": {`"settings":{"hibernate":null},"managerSettings":null,`, "built-in default: on"},
		"no url":  {`"settings":{"hibernate":null},"managerSettings":{"url":null},`, "built-in default: on"},
		"router":  {`"settings":{"hibernate":false,"by":"ann@x.io","at":"2026-10-19T10:00:00.000Z"},"managerSettings":null,`, "off (set on this router by ann@x.io, 2026-10-19 10:00 UTC)"},
	} {
		if out := show(c.extra); !strings.Contains(out, "hibernate") || !strings.Contains(out, c.want) {
			t.Errorf("%s: no %q in\n%s", name, c.want, out)
		}
	}
	if out := show(""); strings.Contains(out, "hibernate") {
		t.Errorf("an old router prints a hibernate line:\n%s", out)
	}
}

func TestWorkloadsShowPrintsTheHibernationAndReadiness(t *testing.T) {
	show := func(h string) string {
		a := &showAPI{status: 200, body: `{"workloadId":"w-1","state":"ASLEEP","settings":{"idleMinutes":15}` + h + `}`}
		r := runFleetWith(t, &fakeAPI{custom: a.handler}, "eu-west-1", nil, "workloads", "show", "prod-eu/w-1")
		if r.exit != 0 {
			t.Fatalf("exit %d: %s", r.exit, r.stderr)
		}
		return r.stdout
	}
	for name, c := range map[string]struct{ h, want string }{
		"router": {`,"hibernation":{"hibernate":true,"from":"router","instances":[{"id":"i-0a","configured":true,"hibernated":false},{"id":"i-0abc","configured":false,"hibernated":false},{"id":"i-0c","configured":true,"hibernated":false}]}`,
			"on, from the router"},
		"readiness": {`,"hibernation":{"hibernate":true,"from":"router","instances":[{"id":"i-0a","configured":true},{"id":"i-0abc","configured":false},{"id":"i-0c","configured":true}]}`,
			"2 of 3 instances were launched with hibernation; i-0abc is stopped instead"},
		"every":    {`,"hibernation":{"hibernate":true,"from":"workload","instances":[{"id":"i-0a","configured":true}]}`, "every instance was launched with hibernation"},
		"workload": {`,"hibernation":{"hibernate":true,"from":"workload"}`, "on, set on this workload"},
		"fallback": {`,"hibernation":{"hibernate":false,"from":"fallback-types"}`, "off: the workload has fallback instance types"},
		"manager":  {`,"hibernation":{"hibernate":false,"from":"manager"}`, "off, from the manager's default"},
		"default":  {`,"hibernation":{"hibernate":true,"from":"default"}`, "on, built-in default"},
	} {
		if out := show(c.h); !strings.Contains(out, c.want) {
			t.Errorf("%s: no %q in\n%s", name, c.want, out)
		}
	}
	if out := show(`,"hibernation":{"hibernate":true,"from":"router","instances":null}`); strings.Contains(out, "instance") {
		t.Errorf("null instances print a line:\n%s", out)
	}
	if out := show(""); strings.Contains(out, "hibernat") {
		t.Errorf("an old router prints a hibernation line:\n%s", out)
	}
}
