package fleet

import (
	"strings"
	"testing"
	"time"
)

var renderNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func sampleRouters() []Router {
	return []Router{
		{Name: "prod-eu", Account: "111111111111", Region: "eu-west-1", Source: "manager", Version: "0.29.0", Health: Health{"healthy"},
			Workloads: 4, WorkloadStates: map[string]int{"AWAKE": 1, "ASLEEP": 3}},
		{Name: "prod-us", Account: "222222222222", Region: "us-east-1", Source: "manager", Version: "0.30.0", Health: Health{"healthy"}, Workloads: 0},
		{Name: "busy", Account: "111111111111", Region: "eu-west-3", Source: "manager", Version: "0.29.0", Health: Health{"degraded"},
			Workloads: 1, WorkloadStates: map[string]int{"AWAKE": 1}, Operation: &OpRef{ID: "op1", Type: "upgrade-router"}},
		{Name: "lost", Account: "222222222222", Region: "eu-west-1", Source: "manager", Error: "timed out"},
		{Name: "own", Region: "eu-west-1", Source: "terraform", Version: "0.31.0-dev.1", Health: Health{"healthy"}, Workloads: 2},
	}
}

func TestRoutersTable(t *testing.T) {
	got := RoutersTable(sampleRouters(), "0.30.0")
	t.Logf("\n%s", got)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("%d lines:\n%s", len(lines), got)
	}
	for i, want := range []string{
		"NAME", "prod-eu", "prod-us", "busy", "lost", "own",
	} {
		if !strings.HasPrefix(lines[i], want) {
			t.Errorf("line %d = %q", i, lines[i])
		}
	}
	for _, want := range []string{
		"0.29.0 → 0.30.0", "4 (3 ASLEEP, 1 AWAKE)", "upgrading", "1 (1 AWAKE)", "terraform", "0.31.0-dev.1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(lines[2], "→") || !strings.Contains(lines[4], "?") {
		t.Errorf("up to date shows an arrow, or an unknown version has no ?:\n%s", got)
	}
	if strings.Contains(lines[5], "→") {
		t.Errorf("a newer pre-release is outdated: %q", lines[5])
	}
	// Columns align.
	col := strings.Index(lines[0], "ACCOUNT")
	for _, l := range lines[1:5] {
		if len(l) <= col || l[col-1] != ' ' {
			t.Errorf("misaligned: %q", l)
		}
	}
}

func TestOutdated(t *testing.T) {
	for _, c := range []struct {
		version, available string
		want               bool
	}{
		{"0.29.0", "0.30.0", true}, {"0.30.0", "0.30.0", false}, {"0.30.0-dev.1", "0.30.0", true},
		{"0.31.0-dev.1", "0.30.0", false}, {"", "0.30.0", false}, {"0.29.0", "", false},
	} {
		if got := (Router{Version: c.version}).Outdated(c.available); got != c.want {
			t.Errorf("%q vs %q = %v", c.version, c.available, got)
		}
	}
}

func TestRoutersSummary(t *testing.T) {
	rs := sampleRouters()
	if got, want := RoutersSummary(rs, "0.30.0"), "available 0.30.0 · 2 of 5 outdated"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := RoutersSummary(rs, ""), "no version available · 5 routers"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := RoutersSummary(nil, "0.30.0"), "available 0.30.0 · 0 of 0 outdated"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWorkloadsTable(t *testing.T) {
	ws := []Workload{
		{WorkloadID: "w-1", Name: "api", Router: "prod-eu", State: "ASLEEP", Tags: map[string]string{"team": "payments", "env": "dev"},
			StateAt: "2026-10-19T09:00:00Z"},
		{WorkloadID: "w-2", Router: "prod-eu", State: "AWAKE", StateAt: "2026-10-17T12:00:00Z"},
		{WorkloadID: "w-3", Name: "batch", Router: "prod-us", State: "AWAKE", UpdatedAt: "2026-10-19T11:59:30Z"},
		{WorkloadID: "w-4", Router: "prod-us", State: "AWAKE", UpdatedAt: "2026-10-19T11:15:00Z"},
		{WorkloadID: "w-5", Router: "prod-us", State: "AWAKE"},
	}
	got := WorkloadsTable(ws, renderNow)
	t.Logf("\n%s", got)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 6 || !strings.HasPrefix(lines[0], "ROUTER") {
		t.Fatalf("lines:\n%s", got)
	}
	for i, want := range []string{"env=dev,team=payments", "3h", "2d", "<1m", "45m", "—"} {
		_ = i
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, f := range strings.Fields(lines[2]) {
		if f == "—" {
			return
		}
	}
	t.Errorf("w-2 has no name or tags: %q", lines[2])
}

func TestWorkloadsSummary(t *testing.T) {
	cases := []struct {
		w    Workloads
		want string
	}{
		{Workloads{Rows: make([]Workload, 4), Total: 4, ByState: map[string]int{"AWAKE": 1, "ASLEEP": 3}}, "4 workloads (3 ASLEEP, 1 AWAKE)"},
		{Workloads{Rows: make([]Workload, 1), Total: 1}, "1 workload"},
		{Workloads{}, "0 workloads"},
		{Workloads{Rows: make([]Workload, 2), Total: 9}, "9 workloads (2 shown)"},
	}
	for _, c := range cases {
		if got := WorkloadsSummary(c.w); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
