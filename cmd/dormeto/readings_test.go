package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const overviewRouters = `{"routers":[{"name":"prod-eu","region":"eu-west-1","account":"1","source":"manager","version":"0.29.0"},{"name":"prod-us","region":"us-east-1","account":"1","source":"manager","version":"0.30.0"}],"available":{"version":"0.30.0"}}`

func overviewAPI() *readAPI {
	return &readAPI{routes: map[string]string{
		"/workloads":        `{"workloads":[{"workloadId":"w-1","router":"prod-eu","state":"ASLEEP"}],"total":5,"byState":{"ASLEEP":3,"AWAKE":2},"cursor":"x"}`,
		"/workloads/states": `{"workloads":5,"computedAt":"2026-10-19T11:00:00Z","cacheMinutes":60,"states7d":{"from":"a","to":"b","totals":{"ASLEEP":3000,"AWAKE":1000}}}`,
		"/savings":          `{"summary":{"computedAt":"2026-10-19T00:30:00Z","day":"2026-10-18","baseline":{"kind":"24x7"},"last30":{"usd":1234.5,"asleepHours":900,"days":[]},"month":{"month":"2026-10","usd":400},"cumulative":{"usd":2000,"since":"2026-09-01"},"top":[],"unpriced":{"workloads":0,"hours":0},"workloads":5,"dormeto":{"usdMonth":30}},"settings":{"compute":10,"databases":0}}`,
		"/alerts":           `{"alarms":[{"id":"a1","kind":"router","name":"prod-eu","level":"error","since":"2026-10-19T09:00:00Z","problems":[{"code":"x","text":"the router is silent"}]}],"notifications":null}`,
		"/operations":       operationsList,
		"/routers":          overviewRouters,
	}}
}

func TestOverviewWithAnOutdatedRouterAndAnAlert(t *testing.T) {
	a := overviewAPI()
	r := runReading(t, a, nil, "overview")
	t.Logf("\n%s", r.stdout)
	if r.exit != 0 || r.stderr != "" {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	for _, want := range []string{"5 (3 ASLEEP, 2 AWAKE)", "ASLEEP 75%, AWAKE 25%", "$1,234.50", "1 open", "2, 1 outdated (available 0.30.0)", "2 running", "op-3"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	if len(a.requests) != 6 || !strings.HasPrefix(a.requests[0], "GET /iam/workloads?limit=1") {
		t.Errorf("requests %q", a.requests)
	}
}

func TestOverviewJSONIsTheComposite(t *testing.T) {
	r := runReading(t, overviewAPI(), nil, "overview", "--json")
	var got struct {
		Workloads struct {
			Total   int
			ByState map[string]int
		}
		States     struct{ Workloads int }
		Savings    struct{ Summary struct{ Day string } }
		Alerts     struct{ Open int }
		Operations struct{ Running []struct{ ID string } }
		Routers    struct {
			Total, Outdated int
			Available       string
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || r.exit != 0 {
		t.Fatalf("exit %d, %v: %q", r.exit, err, r.stdout)
	}
	if got.Workloads.Total != 5 || got.Workloads.ByState["ASLEEP"] != 3 || got.States.Workloads != 5 || got.Savings.Summary.Day != "2026-10-18" ||
		got.Alerts.Open != 1 || len(got.Operations.Running) != 2 || got.Routers.Total != 2 || got.Routers.Outdated != 1 || got.Routers.Available != "0.30.0" {
		t.Errorf("got %+v", got)
	}
}

func TestOverviewReportsARouteThatFailsAndPrintsTheOthers(t *testing.T) {
	a := overviewAPI()
	a.routes["status:/savings"] = "403"
	r := runReading(t, a, nil, "overview")
	if r.exit != 1 || !strings.Contains(r.stderr, "savings: the route failed") || strings.Contains(r.stderr, "alerts") {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
	if !strings.Contains(r.stdout, "1 open") || strings.Contains(r.stdout, "savings") {
		t.Errorf("stdout %q", r.stdout)
	}
	r = runReading(t, a, nil, "overview", "--json")
	if r.exit != 1 || strings.Contains(r.stdout, `"savings"`) || !strings.Contains(r.stdout, `"alerts"`) {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
}

func TestAlertsListWithNoneConfigured(t *testing.T) {
	a := &readAPI{routes: map[string]string{"/alerts": `{"alarms":[],"notifications":null}`}}
	r := runReading(t, a, nil, "alerts", "list")
	if r.exit != 0 || r.stdout != "no alerts configured\n" {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
}

func TestAlertsListShowsTheAlarmsAndTheSubscriptions(t *testing.T) {
	a := overviewAPI()
	a.routes["/alerts"] = `{"alarms":[{"id":"a1","kind":"workload","name":"api","router":"prod-eu","workloadId":"w-1","level":"warning","since":"2026-10-19T09:00:00Z","problems":[{"code":"x","text":"a wake failed"},{"code":"y","text":"slow"}]}],
	 "notifications":{"topicArn":"arn:aws:sns:eu-west-1:1:alerts","confirmed":2,"pending":1,"endpoints":[{"protocol":"email","endpoint":"a***@example.com","pending":false},{"protocol":"https","endpoint":"hooks.example.com","pending":true}]}}`
	r := runReading(t, a, nil, "alerts", "list")
	for _, want := range []string{"LEVEL", "warning", "workload", "api", "3h", "a wake failed; slow", "2 confirmed, 1 pending", "a***@example.com", "pending"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q: %q", want, r.stdout)
		}
	}
	r = runReading(t, a, nil, "alerts", "list", "--json")
	if !strings.Contains(r.stdout, `"notifications"`) || !strings.Contains(r.stdout, `"a wake failed"`) {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestIPListsList(t *testing.T) {
	a := &readAPI{routes: map[string]string{"/ip-lists": `{"catalog":"https://example.com/catalog.json","lists":[
	 {"name":"scanners","url":"u","action":"defer","refreshHours":24,"version":"v1","entries":12345,"fetchedAt":"2026-10-19T09:00:00Z"},
	 {"name":"abusers","url":"u","action":"ignore","refreshHours":24,"error":"timeout"},
	 {"name":"clouds","url":"u","action":"defer","refreshHours":24,"version":"v1","entries":9,"fetchedAt":"2026-10-17T09:00:00Z","error":"http 500"}]}`}}
	r := runReading(t, a, nil, "ip-lists", "list")
	for _, want := range []string{"scanners", "12,345", "3h", "ok", "never downloaded: timeout", "last download failed: http 500", "catalog https://example.com/catalog.json"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q: %q", want, r.stdout)
		}
	}
	a = &readAPI{routes: map[string]string{"/ip-lists": `{"catalog":null,"lists":[]}`}}
	if r := runReading(t, a, nil, "ip-lists", "list"); r.exit != 0 || r.stdout != "no IP lists configured\n" {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestSavingsShow(t *testing.T) {
	r := runReading(t, overviewAPI(), nil, "savings", "show")
	t.Logf("\n%s", r.stdout)
	for _, want := range []string{"$1,234.50, 900 hours asleep", "this month", "$400.00", "since 2026-09-01", "about $30.00 a month", "compute 10%, databases 0% off the list prices; applies from the next day computed"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	a := &readAPI{routes: map[string]string{"/savings": `{"summary":null,"settings":{"compute":0,"databases":0}}`}}
	if r := runReading(t, a, nil, "savings", "show"); !strings.Contains(r.stdout, "no estimate yet") || !strings.Contains(r.stdout, "no discount set") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestSavingsDiscounts(t *testing.T) {
	a := &readAPI{routes: map[string]string{"PUT /savings/settings": `{"settings":{"compute":20,"databases":5,"updatedAt":"2026-10-19T12:00:00Z","updatedBy":"arn:aws:sts::1:assumed-role/Ci/run-42"}}`}}
	r := runReading(t, a, nil, "savings", "discounts", "--compute", "20", "--databases", "5")
	if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != `PUT /iam/savings/settings {"compute":20,"databases":5}` {
		t.Fatalf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	if !strings.Contains(r.stdout, "compute 20%, databases 5%") {
		t.Errorf("stdout %q", r.stdout)
	}
	for _, args := range [][]string{
		{"--compute", "91", "--databases", "5"}, {"--compute", "20"}, {"--databases", "5"}, {}, {"--compute", "-1", "--databases", "5"},
		{"--compute", "1.5", "--databases", "5"}, {"--compute", "20", "--databases", "5", "extra"},
	} {
		b := &readAPI{}
		r := runReading(t, b, nil, append([]string{"savings", "discounts"}, args...)...)
		if r.exit != 2 || len(b.requests) != 0 || r.stderr == "" {
			t.Errorf("%v: exit %d, %d requests, stderr %q", args, r.exit, len(b.requests), r.stderr)
		}
	}
	b := &readAPI{}
	if r := runReading(t, b, nil, "savings", "discounts", "--compute", "91", "--databases", "1"); !strings.Contains(r.stderr, "--compute: 91 is not a whole percentage from 0 to 90") {
		t.Errorf("stderr %q", r.stderr)
	}
}

const auditAnswer = `{"entries":[
 {"id":"e1","at":"2026-10-19T10:00:00Z","who":"arn:aws:sts::1:assumed-role/Ci/run-42","role":"admin","action":"pause","target":"prod-eu/w-1","result":"ok"},
 {"id":"e2","at":"2026-10-19T11:00:00Z","who":"arn:aws:sts::1:assumed-role/CI/run-43","role":"admin","action":"pause","target":"prod-eu/w-2","result":"error: busy"},
 {"id":"e3","at":"2026-10-19T09:00:00Z","who":"arn:aws:sts::1:assumed-role/Ops/ana","role":"admin","action":"pause","target":"prod-eu/w-3","result":"ok"},
 {"id":"e4","at":"2026-10-19T11:30:00Z","who":"arn:aws:sts::1:assumed-role/Ci/run-44","role":"admin","action":"wake","target":"prod-eu/w-1","result":"ok"},
 {"id":"e5","at":"2026-10-10T11:30:00Z","who":"arn:aws:sts::1:assumed-role/Ci/run-1","role":"admin","action":"pause","target":"prod-eu/w-1","result":"ok"}
]}`

func TestAuditFiltersSinceActionAndWhoNewestFirst(t *testing.T) {
	a := &readAPI{routes: map[string]string{"/audit": auditAnswer}}
	r := runReading(t, a, nil, "audit", "--since", "2d", "--action", "pause", "--who", "ci")
	t.Logf("\n%s", r.stdout)
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	i2, i1 := strings.Index(r.stdout, "CI/run-43"), strings.Index(r.stdout, "Ci/run-42")
	if i2 < 0 || i1 < 0 || i2 > i1 {
		t.Errorf("order: stdout %q", r.stdout)
	}
	for _, not := range []string{"ana", "run-44", "run-1 ", "arn:aws"} {
		if strings.Contains(r.stdout, not) {
			t.Errorf("stdout has %q: %q", not, r.stdout)
		}
	}
	if !strings.Contains(r.stdout, "2026-10-19 11:00 UTC") || !strings.Contains(r.stdout, "error: busy") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestAuditTargetLimitAndJSON(t *testing.T) {
	a := &readAPI{routes: map[string]string{"/audit": auditAnswer}}
	r := runReading(t, a, nil, "audit", "--target", "W-1", "--limit", "2", "--json")
	var got struct{ Entries []struct{ ID string } }
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || len(got.Entries) != 2 || got.Entries[0].ID != "e4" || got.Entries[1].ID != "e1" {
		t.Errorf("%v: %q", err, r.stdout)
	}
	r = runReading(t, a, nil, "audit", "--action", "pause", "--action", "wake", "--limit", "0")
	if n := strings.Count(r.stdout, "\n"); n < 6 { // the header and five entries
		t.Errorf("lines %d: %q", n, r.stdout)
	}
	r = runReading(t, a, nil, "audit", "--action", "pause,wake", "--since", "2026-10-19")
	if strings.Contains(r.stdout, "run-1 ") || !strings.Contains(r.stdout, "run-44") {
		t.Errorf("since a date: %q", r.stdout)
	}
	r = runReading(t, a, nil, "audit", "--since", "2026-10-19T10:30:00Z")
	if strings.Contains(r.stdout, "run-42") || !strings.Contains(r.stdout, "run-44") {
		t.Errorf("since a time: %q", r.stdout)
	}
}

func TestAuditRefusesABadSinceOrLimit(t *testing.T) {
	for _, args := range [][]string{{"--since", "yesterday"}, {"--since", "2026-13-40"}, {"--limit", "-1"}, {"extra"}} {
		a := &readAPI{routes: map[string]string{"/audit": auditAnswer}}
		r := runReading(t, a, nil, append([]string{"audit"}, args...)...)
		if r.exit != 2 || len(a.requests) != 0 || r.stderr == "" {
			t.Errorf("%v: exit %d, %d requests, stderr %q", args, r.exit, len(a.requests), r.stderr)
		}
	}
}
