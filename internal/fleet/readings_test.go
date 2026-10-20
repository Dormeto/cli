package fleet

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestShortARN(t *testing.T) {
	for in, want := range map[string]string{
		"arn:aws:sts::1:assumed-role/Ci/run-42":            "Ci/run-42",
		"arn:aws:sts::1:assumed-role/Path/To/Role/session": "Role/session",
		"arn:aws:iam::1:user/bob":                          "bob",
		"arn:aws:iam::1:root":                              "arn:aws:iam::1:root",
		"ana@example.com":                                  "ana@example.com",
		"":                                                 "",
	} {
		if got := ShortARN(in); got != want {
			t.Errorf("ShortARN(%q) = %q, want %q", in, got, want)
		}
	}
}

func parse[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

var now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func TestOperationsTableAndShowText(t *testing.T) {
	ops := parse[[]Operation](t, `[
	 {"id":"a","type":"offboard","status":"failed","target":{"router":"r","workloadId":"w"},"by":"arn:aws:sts::1:assumed-role/Ci/run-42","createdAt":"2026-10-19T10:00:00Z","error":"boom",
	  "steps":[{"name":"locate","status":"done","startedAt":"2026-10-19T10:00:00Z","finishedAt":"2026-10-19T10:00:04Z"},{"name":"pause","status":"failed","detail":"no answer"},{"name":"candidate","status":"pending"}]},
	 {"id":"b","type":"install-router","status":"running","target":{},"steps":[]}]`)
	table := OperationsTable(ops, now)
	for _, want := range []string{"r/w", "failed", "pause", "Ci/run-42", "2h"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q: %q", want, table)
		}
	}
	if strings.Count(table, "—") < 3 { // b has no target, step, who
		t.Errorf("table %q", table)
	}
	if OperationsTable(nil, now) != "no operations\n" {
		t.Error("empty table")
	}
	text := OperationText(ops[0], time.UTC)
	for _, want := range []string{"status   failed", "error    boom", "2026-10-19 10:00 UTC", "locate     done     4s", "pause      failed   —  no answer", "candidate  pending"} {
		if !strings.Contains(text, want) {
			t.Errorf("show lacks %q: %q", want, text)
		}
	}
	if text := OperationText(ops[1], time.UTC); strings.Contains(text, "STEP") || strings.Contains(text, "error") {
		t.Errorf("unfinished one without steps: %q", text)
	}
}

func TestAlertsText(t *testing.T) {
	if got := parse[AlertsAnswer](t, `{"alarms":[],"notifications":null}`).Text(now); got != "no alerts configured\n" {
		t.Errorf("got %q", got)
	}
	if got := parse[AlertsAnswer](t, `{"alarms":[],"notifications":{"topicArn":"t","confirmed":1,"pending":0}}`).Text(now); !strings.HasPrefix(got, "no open alarm\n") || !strings.Contains(got, "t: 1 confirmed, 0 pending") || strings.Contains(got, "PROTOCOL") {
		t.Errorf("got %q", got)
	}
	got := parse[AlertsAnswer](t, `{"alarms":[{"id":"i","kind":"router","name":"","level":"error","since":"2026-10-19T11:00:00Z","problems":[]}],
	 "notifications":{"topicArn":"t","confirmed":1,"pending":1,"endpoints":[{"protocol":"email","endpoint":"a***@x.com","pending":false},{"protocol":"https","endpoint":"h","pending":true}]}}`).Text(now)
	for _, want := range []string{"error  router  i     1h     —", "email     a***@x.com  confirmed", "https     h           pending"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q: %q", want, got)
		}
	}
}

func TestSavingsText(t *testing.T) {
	none := parse[SavingsAnswer](t, `{"summary":null,"settings":null}`).Text(time.UTC)
	if !strings.Contains(none, "no estimate yet") || !strings.Contains(none, "no discount set") || strings.Contains(none, "applies") {
		t.Errorf("got %q", none)
	}
	a := parse[SavingsAnswer](t, `{"summary":{"computedAt":"2026-10-19T00:30:00Z","day":"2026-10-18","baseline":{"kind":"business-hours","timeZone":"Europe/Paris"},"last30":{"usd":100,"asleepHours":1234.4},"month":{"usd":10},
	 "cumulative":{"usd":55,"since":"2026-10-14"},"top":[{"router":"r","workloadId":"w","usd":5}],"unpriced":{"workloads":2,"hours":3},"workloads":7,"dormeto":{"usdMonth":30},"discounts":{"compute":10,"databases":0}},
	 "settings":{"compute":10,"databases":0,"updatedAt":"2026-10-19T09:00:00Z","updatedBy":"arn:aws:sts::1:assumed-role/Ci/run-42"}}`)
	got := a.Text(time.UTC)
	for _, want := range []string{"$100.00, 1,234 hours asleep", "since 2026-10-14", "net               about $95.00 over the 5 days computed", "business hours (weekdays 08:00 to 20:00, Europe/Paris)",
		"2 workloads (a router before", "r/w", "$5.00", "compute 10%, databases 0% off the list prices (set by Ci/run-42 2026-10-19 09:00 UTC)"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q: %q", want, got)
		}
	}
	if strings.Contains(got, "applies from") {
		t.Errorf("discounts in use equal the settings: %q", got)
	}
}

func TestFilterAuditSortsAndFilters(t *testing.T) {
	es := parse[[]AuditEntry](t, `[
	 {"id":"1","at":"2026-10-19T10:00:00Z","who":"Ci","action":"Pause","target":"r/w-1"},
	 {"id":"2","at":"2026-10-19T11:00:00Z","who":"ci","action":"pause","target":"r/w-2"},
	 {"id":"3","at":"2026-10-19T12:00:00Z","who":"ana","action":"wake","target":"r/w-1"},
	 {"id":"4","at":"not a time","who":"ci","action":"pause","target":"r/w-1"}]`)
	ids := func(out []AuditEntry) (s string) {
		for _, e := range out {
			s += e.ID
		}
		return s
	}
	out, total := FilterAudit(es, AuditFilter{}, 0)
	if ids(out) != "4321" || total != 4 { // an unreadable time sorts by its text
		t.Errorf("all: %s %d", ids(out), total)
	}
	out, total = FilterAudit(es, AuditFilter{Since: time.Date(2026, 10, 19, 10, 30, 0, 0, time.UTC), Actions: []string{"PAUSE"}, Who: "CI"}, 0)
	if ids(out) != "2" || total != 1 { // 1 is before, 4 has no readable time
		t.Errorf("since+action+who: %s %d", ids(out), total)
	}
	out, total = FilterAudit(es, AuditFilter{Target: "W-1"}, 1)
	if len(out) != 1 || total != 3 || out[0].ID != "4" {
		t.Errorf("target+limit: %s %d", ids(out), total)
	}
}

func TestOverviewTextWithABlockMissing(t *testing.T) {
	routers := parse[Routers](t, `{"routers":[{"name":"a","version":"0.1.0"}],"available":{"version":"0.2.0"}}`)
	running := []Operation{}
	o := Overview{Routers: &routers, Running: &running, Missing: map[string]bool{"savings": true}}
	got := o.Text(now)
	for _, want := range []string{"savings     not available", "routers     1, 1 outdated (available 0.2.0)", "operations  0 running"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q: %q", want, got)
		}
	}
	if strings.Contains(got, "workloads") || strings.Contains(got, "alerts") || strings.Contains(got, "ID") {
		t.Errorf("blocks that were not read: %q", got)
	}
	j := o.JSON()
	if v, ok := j["savings"]; !ok || v != nil {
		t.Errorf("json %v", j)
	}
	if _, ok := j["alerts"]; ok {
		t.Errorf("a failed block is absent: %v", j)
	}
}
