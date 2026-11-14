package fleet

import (
	"slices"
	"strings"
)

// Route is a route of the manager's API under /iam, as an IAM policy names it: Method is GET, POST, PUT or DELETE (ANY
// for every method), and each variable part of Path ({router}, {id}, {email}…) is *.
type Route struct {
	Method, Path string
}

// String is the route as the CLI says it: "POST /iam/workloads/*/*/wake".
func (r Route) String() string { return r.Method + " /iam" + r.Path }

// ARN is the route's execute-api resource on the API apiID of account in region, in its $default stage (the manager's
// only stage): arn:<partition>:execute-api:<region>:<account>:<api id>/$default/<method>/iam<path>, ANY being *.
func (r Route) ARN(partition, region, account, apiID string) string {
	method := r.Method
	if method == "ANY" {
		method = "*"
	}
	return "arn:" + partition + ":execute-api:" + region + ":" + account + ":" + apiID + "/$default/" + method + "/iam" + r.Path
}

// Matches tells whether a request (its method and its escaped path under /iam) is one of the route's: each * of the path
// stands for one part, a trailing /* of ANY for any.
func (r Route) Matches(method, path string) bool {
	if r.Method != "ANY" && r.Method != method {
		return false
	}
	if r.Method == "ANY" && r.Path == "/*" {
		return true
	}
	want, got := strings.Split(r.Path, "/"), strings.Split(path, "/")
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i] != "*" && want[i] != got[i] {
			return false
		}
	}
	return true
}

// Routes are the routes each method of `dormeto aws` that calls the manager calls, by the method's name (the third word
// of `dormeto aws <method>`): every route any of its commands may call, reads and writes. `dormeto aws check` simulates
// execute-api:Invoke on each; the README's "Permission to call the manager" is held to it by a test.
var Routes = map[string][]Route{
	"alerts": {{"GET", "/alerts"}},
	// api calls any route it is given.
	"api":   {{"ANY", "/*"}},
	"audit": {{"GET", "/audit"}},
	"discovery": {
		{"GET", "/discovery"}, {"POST", "/discovery/run"}, {"GET", "/discovery/candidates"},
		{"POST", "/discovery/dismiss"}, {"POST", "/discovery/restore"}, {"GET", "/discovery/access"},
		{"GET", "/discovery/config"}, {"PUT", "/discovery/config"},
	},
	"exclusions": {
		{"GET", "/exclusions"}, {"POST", "/exclusions"}, {"DELETE", "/exclusions/*"}, {"GET", "/operations/*"},
	},
	"groups": {
		{"GET", "/groups"}, {"POST", "/groups"}, {"PUT", "/groups/*"}, {"DELETE", "/groups/*"},
	},
	"ip-lists": {{"GET", "/ip-lists"}},
	"manager": {
		{"GET", "/health"}, {"GET", "/update"}, {"GET", "/update/check"}, {"PUT", "/update/check"},
		{"GET", "/update/changes"}, {"POST", "/update/plan"}, {"POST", "/update/apply"}, {"GET", "/operations"},
	},
	"operations": {{"GET", "/operations"}, {"GET", "/operations/*"}},
	"overview": {
		{"GET", "/workloads"}, {"GET", "/workloads/states"}, {"GET", "/savings"}, {"GET", "/alerts"},
		{"GET", "/operations"}, {"GET", "/routers"},
	},
	"routers": {
		{"GET", "/routers"}, {"GET", "/routers/*"}, {"GET", "/workloads"},
		{"POST", "/operations"}, {"GET", "/operations/*"},
		{"POST", "/routers/*/pause"}, {"POST", "/routers/*/resume"}, {"PUT", "/routers/*/settings"},
		{"GET", "/routers/*/description"}, {"PUT", "/routers/*/description"}, {"DELETE", "/routers/*/description"},
		{"GET", "/routers/*/waiting-page"}, {"PUT", "/routers/*/waiting-page"}, {"DELETE", "/routers/*/waiting-page"},
	},
	"savings": {{"GET", "/savings"}, {"PUT", "/savings/settings"}},
	"settings": {
		{"GET", "/settings/workloads"}, {"PUT", "/settings/workloads"},
		{"GET", "/settings/waiting-page"}, {"PUT", "/settings/waiting-page"}, {"DELETE", "/settings/waiting-page"},
	},
	"types": {{"GET", "/settings/types"}, {"PUT", "/settings/types"}},
	"users": {
		{"GET", "/users"}, {"POST", "/users"}, {"PUT", "/users/*/role"}, {"PUT", "/users/*/groups"},
		{"POST", "/users/*/disable"}, {"POST", "/users/*/enable"}, {"POST", "/users/*/resend"}, {"POST", "/users/*/mfa/reset"},
	},
	"whoami": {{"GET", "/me"}, {"GET", "/health"}},
	"workloads": {
		{"GET", "/workloads"}, {"GET", "/workloads/*/*"}, {"GET", "/workloads/*/*/history"}, {"GET", "/workloads/*/*/idle-window"},
		{"GET", "/routers"}, {"GET", "/discovery/candidates"},
		{"POST", "/operations"}, {"GET", "/operations/*"}, {"POST", "/exclusions"},
		{"POST", "/workloads/*/*/wake"}, {"POST", "/workloads/*/*/nap"}, {"POST", "/workloads/*/*/pause"},
		{"POST", "/workloads/*/*/resume"}, {"POST", "/workloads/*/*/rename"}, {"PUT", "/workloads/*/*/tags"},
		{"PUT", "/workloads/*/*/settings"},
		{"GET", "/workloads/*/*/description"}, {"PUT", "/workloads/*/*/description"}, {"DELETE", "/workloads/*/*/description"},
		{"GET", "/workloads/*/*/waiting-page"}, {"PUT", "/workloads/*/*/waiting-page"}, {"DELETE", "/workloads/*/*/waiting-page"},
		{"PUT", "/workloads/*/*/schedule"}, {"DELETE", "/workloads/*/*/schedule"},
	},
}

// RouteMethods are the names of Routes, sorted.
func RouteMethods() []string {
	var out []string
	for m := range Routes {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}
