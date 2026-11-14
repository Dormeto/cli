package main

// The routes table of `dormeto aws check` (fleet.Routes) is true to the commands: every request a method's commands send
// is one of its routes, every route of the table is sent by one of them, and every method of the tree that calls the
// manager has its row.

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

// routesAPI is a fake manager that answers every read with enough for the commands to go on to their writes, accepts
// every write, and records each request as "<method> <escaped path under /iam>".
type routesAPI struct {
	mu    sync.Mutex
	sent  []string
	ops   fakeAPI
	inner http.HandlerFunc // answers instead when set (the manager's update)
}

func (a *routesAPI) handler(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.sent = append(a.sent, r.Method+" "+strings.TrimPrefix(r.URL.EscapedPath(), "/iam"))
	a.mu.Unlock()
	if a.inner != nil {
		a.inner(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case path == "/iam/operations" && r.Method == http.MethodGet:
		io.WriteString(w, `{"operations":[{"id":"op-1","type":"upgrade-manager","status":"succeeded","steps":[]}]}`)
	case strings.HasPrefix(path, "/iam/operations"):
		a.ops.operations(w, r)
	case r.Method != http.MethodGet:
		switch path {
		case "/iam/update/plan", "/iam/update/apply":
			w.WriteHeader(202)
			io.WriteString(w, `{"operation":"op-1"}`)
		case "/iam/exclusions":
			w.WriteHeader(202)
			io.WriteString(w, `{"operation":{"id":"op-x","type":"exclude","status":"running","steps":[]}}`)
		default:
			io.WriteString(w, `{}`)
		}
	case path == "/iam/routers":
		io.WriteString(w, routersAnswer)
	case path == "/iam/workloads":
		io.WriteString(w, workloadsAnswer)
	case path == "/iam/discovery/candidates":
		io.WriteString(w, `{"candidates":[{"id":"`+dismissedARN+`","cloud":"aws","account":"111111111111","region":"eu-west-1","router":"prod-eu","status":"pending-onboarding","eligible":true}],"cursor":null}`)
	case path == "/iam/settings/types":
		io.WriteString(w, `{"types":[{"name":"api","tags":{"kind":"api"},"settings":{"idleMinutes":30}}],"builtIn":["instance","asg","ecs","k8s","databases"]}`)
	case path == "/iam/update":
		io.WriteString(w, `{"version":"0.24.0","available":{"version":"0.25.0"},"state":"idle"}`)
	case path == "/iam/health":
		io.WriteString(w, `{"status":"ok","version":"0.24.0"}`)
	case strings.HasPrefix(path, "/iam/workloads/") && path != "/iam/workloads/states":
		io.WriteString(w, `{"workloadId":"w-1","router":"prod-eu","cloud":"aws","account":"111111111111","region":"eu-west-1","state":"AWAKE","source":"manager","tags":{},"targetGroupArn":"arn:aws:elasticloadbalancing:eu-west-1:111111111111:targetgroup/web/0123456789abcdef"}`)
	default:
		io.WriteString(w, `{}`)
	}
}

// routeRuns are command lines of each method, run against routesAPI: together they send every route of the table.
var routeRuns = map[string][][]string{
	"alerts":    {{"list"}},
	"api":       {{"GET", "/iam/me"}, {"POST", "/iam/discovery/run"}},
	"audit":     {{}},
	"discovery": {{"status"}, {"run"}, {"candidates"}, {"dismiss", dismissedARN}, {"restore", dismissedARN}, {"access"}, {"config", "show"}, {"config", "set", "--file", "-", "--yes"}},
	"exclusions": {{"list"}, {"add", "--account", "111111111111", "--reason", "audit", "--yes"}, {"lift", "ex-1", "--yes"},
		{"lift", "ex-1", "--yes", "--no-wait"}},
	"groups":     {{"list"}, {"show", "payments"}, {"create", "payments"}, {"update", "payments", "--description", "the team"}, {"delete", "payments", "--yes"}},
	"ip-lists":   {{"list"}},
	"manager":    {{"status"}, {"check", "--enable"}, {"changes"}, {"update", "--yes"}},
	"operations": {{"list"}, {"show", "op-1"}, {"watch", "op-1"}},
	"overview":   {{}},
	"routers": {{"list", "--tag", "team=payments"}, {"show", "prod-eu"}, {"install", "--account", "111111111111", "--router-region", "eu-west-1"},
		{"upgrade", "prod-eu", "--yes"}, {"uninstall", "prod-eu", "--yes"}, {"pause", "prod-eu", "--reason", "x", "--yes"},
		{"resume", "prod-eu", "--yes"}, {"settings", "prod-eu"}, {"settings", "prod-eu", "--hibernate", "true", "--yes"},
		{"description", "prod-eu"}, {"description", "prod-eu", "--set", "x"}, {"description", "prod-eu", "--clear"},
		{"waiting-page", "show", "prod-eu"}, {"waiting-page", "set", "prod-eu", "--title", "Back"}, {"waiting-page", "clear", "prod-eu"}},
	"savings":  {{"show"}, {"discounts", "--compute", "20", "--databases", "10"}},
	"settings": {{"show"}, {"set", "--hibernate", "true"}, {"waiting-page", "show"}, {"waiting-page", "set", "--title", "Back"}, {"waiting-page", "clear"}},
	"types":    {{"list"}, {"set", "api", "--idle-minutes", "20"}},
	"users": {{"list"}, {"invite", "ann@x.io", "--role", "viewer"}, {"role", "ann@x.io", "admin", "--yes"}, {"groups", "ann@x.io", "payments"},
		{"disable", "ann@x.io", "--yes"}, {"enable", "ann@x.io"}, {"resend", "ann@x.io"}, {"reset-mfa", "ann@x.io"}},
	"whoami": {{}},
	"workloads": {{"list"}, {"show", "prod-eu/w-1"}, {"history", "prod-eu/w-1"}, {"idle-window", "prod-eu/w-1"},
		{"wake", "--tag", "team=payments", "--yes", "--wait"}, {"nap", "prod-eu/w-1", "--yes"}, {"pause", "prod-eu/w-1", "--reason", "x", "--yes"},
		{"resume", "prod-eu/w-1", "--yes"}, {"rename", "prod-eu/w-1", "api", "--yes"}, {"tags", "prod-eu/w-1", "--set", "team=a", "--yes"},
		{"settings", "prod-eu/w-1", "--hibernate", "true", "--yes"},
		{"description", "prod-eu/w-1"}, {"description", "prod-eu/w-1", "--set", "x"}, {"description", "prod-eu/w-1", "--clear"},
		{"waiting-page", "show", "prod-eu/w-1"}, {"waiting-page", "set", "prod-eu/w-1", "--title", "Back"}, {"waiting-page", "clear", "prod-eu/w-1"},
		{"schedule", "show", "prod-eu/w-1"}, {"schedule", "set", "prod-eu/w-1", "--file", "-", "--yes"}, {"schedule", "clear", "prod-eu/w-1", "--yes"},
		{"onboard", dismissedARN, "--yes"}, {"offboard", "prod-eu/w-1", "--yes"}, {"exclude", "prod-eu/w-1", "--reason", "x", "--yes"}},
}

// The requests the commands of each method send are routes of its row, and each route of a row is sent by one of them.
func TestEveryRouteACommandSendsIsInItsMethodsRowAndEveryRowIsSent(t *testing.T) {
	for _, method := range fleet.RouteMethods() {
		runs, ok := routeRuns[method]
		if !ok {
			t.Errorf("%s: no command line runs its routes", method)
			continue
		}
		sent := map[fleet.Route]bool{}
		for _, args := range runs {
			a := &routesAPI{}
			if method == "manager" {
				m := updating()
				m.check, m.changes = `{"enabled":false,"alert":false}`, `[]`
				a.inner = m.handler()
			}
			stdin := `{"rules":[]}`
			if slices.Contains(args, "schedule") {
				stdin = `{"windows":[{"days":["mon"],"from":"08:00","to":"18:00"}]}`
			}
			r := runCommands(t, &fakeAPI{custom: a.handler, stdin: stdin}, "eu-west-1", nil, withTypes, append([]string{method}, args...)...)
			if len(a.sent) == 0 {
				t.Errorf("%s %q sent nothing: exit %d, stderr %q", method, args, r.exit, r.stderr)
			}
			for _, s := range a.sent {
				verb, path, _ := strings.Cut(s, " ")
				i := slices.IndexFunc(fleet.Routes[method], func(rt fleet.Route) bool { return rt.Matches(verb, path) })
				if i < 0 {
					t.Errorf("%s %q sends %s, not in the method's routes", method, args, s)
					continue
				}
				sent[fleet.Routes[method][i]] = true
			}
		}
		for _, rt := range fleet.Routes[method] {
			if !sent[rt] {
				t.Errorf("%s: no command line sends %s", method, rt)
			}
		}
	}
}

// withTypes are the manager's commands and types, as the tree has them.
func withTypes(d fleetDeps) []*cobra.Command { return append(managerCommands(d), newTypes(d)) }

// Every method of `dormeto aws` that calls the manager has its row, and every row is such a method's.
func TestEveryManagerMethodHasItsRoutes(t *testing.T) {
	var names []string
	for _, c := range withTypes(fleetDeps{}) {
		names = append(names, c.Name())
	}
	slices.Sort(names)
	if !slices.Equal(names, fleet.RouteMethods()) {
		t.Errorf("the manager's methods %v, the routes' rows %v", names, fleet.RouteMethods())
	}
	if got := fmt.Sprint(fleet.Routes["api"]); got != "[ANY /iam/*]" {
		t.Errorf("api's row %s, want every route", got)
	}
}
