package fleet

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// permissionSection is the README's "Permission to call the manager", up to the next heading.
func permissionSection(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, s, ok := strings.Cut(string(b), "\n### Permission to call the manager\n")
	if !ok {
		t.Fatal("README.md has no Permission to call the manager")
	}
	s, _, _ = strings.Cut(s, "\n### ")
	return s
}

// readmeResource is a resource of the README's policies: any stage, the placeholders of the region, account and API, then
// the route's method and path.
var readmeResource = regexp.MustCompile(`arn:aws:execute-api:<region>:<account>:<api id>/\*/([A-Z*]+)/iam(/[^"\s]*)`)

// toRoute is the route of a readmeResource match.
func toRoute(m []string) Route {
	method := m[1]
	if method == "*" {
		method = "ANY"
	}
	return Route{method, m[2]}
}

func has(method string, r Route) bool { return slices.Contains(Routes[method], r) }

// covers tells whether a policy's route (its * standing for any characters, / included, as in IAM) allows a route of
// method's row.
func covers(policy Route, method string) bool {
	re := regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(policy.String()), `\*`, ".*") + "$")
	return slices.ContainsFunc(Routes[method], func(r Route) bool { return re.MatchString(r.String()) })
}

// The README's three policies are the routes of the table: read-only every GET, wake and nap the routes of whoami and of
// the workloads' reads, wakes and naps, every route the api's row.
func TestTheReadmesPoliciesAreTheRoutesTable(t *testing.T) {
	s := permissionSection(t)
	parts := strings.Split(s, "**Every route.**")
	if len(parts) != 2 {
		t.Fatal("no Every route policy")
	}
	readOnly, wake, _ := strings.Cut(parts[0], "**Wake and nap.**")
	if got := readmeResource.FindAllStringSubmatch(readOnly, -1); len(got) != 1 || toRoute(got[0]) != (Route{"GET", "/*"}) {
		t.Errorf("read-only policy %v, want every GET", got)
	}
	wakes := readmeResource.FindAllStringSubmatch(wake, -1)
	if len(wakes) != 6 {
		t.Errorf("wake and nap policy: %d resources", len(wakes))
	}
	for _, m := range wakes {
		if r := toRoute(m); !covers(r, "workloads") && !covers(r, "whoami") {
			t.Errorf("wake and nap policy: %s allows no route of whoami or workloads", r)
		}
	}
	for _, r := range Routes["whoami"] {
		if !slices.ContainsFunc(wakes, func(m []string) bool { return toRoute(m) == r }) {
			t.Errorf("wake and nap policy lacks whoami's %s", r)
		}
	}
	for _, added := range regexp.MustCompile("`\\.\\.\\./([A-Z]+)/iam(/[^`]*)`").FindAllStringSubmatch(wake, -1) {
		if r := (Route{added[1], added[2]}); !covers(r, "workloads") {
			t.Errorf("wake and nap policy adds %s, which allows no route of workloads", r)
		}
	}
	every := readmeResource.FindAllStringSubmatch(parts[1], -1)
	if len(every) != 1 || toRoute(every[0]) != Routes["api"][0] {
		t.Errorf("every-route policy %v, want api's %v", every, Routes["api"])
	}
}

// The README's table "Command | Route it writes" holds, for each method, exactly the routes of its row that write, and
// reads of the row only.
func TestTheReadmesTableIsTheRoutesTable(t *testing.T) {
	s := permissionSection(t)
	got := map[string][]Route{}
	rows := 0
	for _, line := range strings.Split(s, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) != 4 || !strings.HasPrefix(strings.TrimSpace(cells[1]), "`") {
			continue
		}
		rows++
		methods := readmeMethods(cells[1])
		routes := readmeRoutes(cells[2])
		if len(methods) == 0 || len(routes) == 0 {
			t.Errorf("row %q: methods %v, routes %v", line, methods, routes)
		}
		for _, m := range methods {
			for _, r := range routes {
				if !has(m, r) {
					t.Errorf("README: %s's %s is not in its row of the routes", m, r)
				}
				if !slices.Contains(got[m], r) {
					got[m] = append(got[m], r)
				}
			}
		}
	}
	if rows < 20 {
		t.Fatalf("only %d rows read", rows)
	}
	for method, routes := range Routes {
		if method == "api" {
			continue
		}
		for _, r := range routes {
			if r.Method != "GET" && !slices.Contains(got[method], r) {
				t.Errorf("the README's table lacks %s's %s", method, r)
			}
		}
	}
}

var (
	backticked = regexp.MustCompile("`([^`]+)`")
	braces     = regexp.MustCompile(`\{[a-z]+\}`)
)

// readmeMethods are the methods a command cell names: "`routers install`, `upgrade`, `workloads onboard`" is routers and
// workloads; a single word goes with the method before it.
func readmeMethods(cell string) []string {
	var out []string
	for _, m := range backticked.FindAllStringSubmatch(cell, -1) {
		words := strings.Fields(m[1])
		if len(words) > 1 && !slices.Contains(out, words[0]) {
			out = append(out, words[0])
		}
	}
	return out
}

// readmeRoutes are the routes a route cell names: "`POST /a/{x}/wake` (`nap`, `pause`)" is POST /a/*/wake and
// POST /a/*/nap and POST /a/*/pause; "`DELETE` on the same path" is DELETE on the path before it.
func readmeRoutes(cell string) []Route {
	var out []Route
	var last Route
	for _, loc := range backticked.FindAllStringSubmatchIndex(cell, -1) {
		token := cell[loc[2]:loc[3]]
		inParens := strings.Count(cell[:loc[0]], "(") > strings.Count(cell[:loc[0]], ")")
		method, path, isRoute := strings.Cut(token, " ")
		switch {
		case isRoute:
			last = Route{method, braces.ReplaceAllString(path, "*")}
			out = append(out, last)
		case inParens && last.Path != "":
			out = append(out, Route{last.Method, last.Path[:strings.LastIndex(last.Path, "/")] + "/" + token}) // the last part replaced
		case strings.ToUpper(token) == token && last.Path != "":
			out = append(out, Route{token, last.Path})
		}
	}
	return out
}
