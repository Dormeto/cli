package main

import (
	"context"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/gcp/gcptest"
	"github.com/Dormeto/cli/internal/gcponboard"
)

// The samples' lines (samples_test.go), by method, in the CLI's help order; the manager's answers they share are in
// testdata/samples/manager.json, a line's own here.

var samples = []sample{
	// whoami, check
	{line: "dormeto aws whoami"},
	{line: "dormeto aws check"},
	{line: "dormeto gcp whoami --project my-project"},
	{line: "dormeto gcp check --project my-project --region europe-west1"},

	// alerts, api, audit, overview, ip-lists
	{line: "dormeto aws alerts list"},
	{line: "dormeto aws api GET /health"},
	{line: "dormeto aws audit --limit 3"},
	{line: "dormeto aws overview"},
	{line: "dormeto aws ip-lists list"},

	// routers
	{line: "dormeto aws routers list"},
	{line: "dormeto aws routers show prod-eu"},
	{line: "dormeto aws routers install --account 111122223333 --router-region eu-west-1", stderr: true},
	{line: "dormeto aws routers upgrade prod-us --yes", stderr: true},
	{line: "dormeto aws routers uninstall prod-us --yes", stderr: true},
	{line: "dormeto aws routers pause prod-eu --reason \"database migration\" --for 2h", stderr: true},
	{line: "dormeto aws routers resume prod-eu", stderr: true},
	{line: "dormeto aws routers settings prod-eu --idle-minutes 30", stderr: true},
	{line: "dormeto aws routers description prod-eu --set \"Production, Ireland\"", stderr: true},
	{line: "dormeto aws routers waiting-page show prod-eu"},
	{line: "dormeto aws routers waiting-page set prod-eu --title \"Back in a moment\"", stderr: true},
	{line: "dormeto aws routers waiting-page clear prod-eu", stderr: true},

	// workloads
	{line: "dormeto aws workloads list"},
	{line: "dormeto aws workloads show prod-eu/web"},
	{line: "dormeto aws workloads show prod-eu/e1b7eeff74af033b", variant: "standalone"},
	{line: "dormeto aws workloads history prod-eu/web --days 7"},
	{line: "dormeto aws workloads idle-window prod-eu/web"},
	{line: "dormeto aws workloads wake prod-eu/web --wait", stderr: true},
	{line: "dormeto aws workloads nap prod-eu/web", stderr: true},
	{line: "dormeto aws workloads keep-awake prod-eu/web --for 2h", stderr: true},
	{line: "dormeto aws workloads pause prod-eu/web --reason \"load test\" --for 4h", stderr: true},
	{line: "dormeto aws workloads resume prod-eu/web", stderr: true},
	{line: "dormeto aws workloads exclude prod-eu/web --reason \"always on\" --for 30d --yes", stderr: true},
	{line: "dormeto aws workloads onboard api-tg", stderr: true},
	{line: "dormeto aws workloads onboard reports", variant: "standalone", stderr: true},
	{line: "dormeto aws workloads offboard prod-eu/web --yes", stderr: true},
	{line: "dormeto aws workloads rename prod-eu/web \"Web shop\"", stderr: true},
	{line: "dormeto aws workloads tags prod-eu/web --set owner=web-team", stderr: true},
	{line: "dormeto aws workloads settings prod-eu/web --idle-minutes 20", stderr: true},
	{line: "dormeto aws workloads description prod-eu/web --set \"The public site\"", stderr: true},
	{line: "dormeto aws workloads waiting-page show prod-eu/web"},
	{line: "dormeto aws workloads waiting-page set prod-eu/web --title \"The shop wakes up\"", stderr: true},
	{line: "dormeto aws workloads waiting-page clear prod-eu/web", stderr: true},

	// schedule
	{line: "dormeto aws schedule list"},
	{line: "dormeto aws schedule show prod-eu/web"},
	{line: "dormeto aws schedule set prod-eu/web --file schedule.json", stderr: true, files: map[string]string{"schedule.json": sampleSchedule}},
	{line: "dormeto aws schedule clear prod-eu/web", stderr: true},
	{line: "dormeto aws schedule add prod-eu/web --kind nap --days sat,sun --start 00:00 --end 00:00 --wake header", stderr: true},
	{line: "dormeto aws schedule remove prod-eu/web --window 2", stderr: true},
	{line: "dormeto aws schedule except prod-eu/web --date 2026-12-24", stderr: true},
	{line: "dormeto aws schedule edit prod-eu/web --max-nap-days 14", stderr: true},

	// discovery, exclusions
	{line: "dormeto aws discovery status"},
	{line: "dormeto aws discovery run --wait", stderr: true},
	{line: "dormeto aws discovery candidates"},
	{line: "dormeto aws discovery dismiss batch-tg --note \"runs at night\"", stderr: true},
	{line: "dormeto aws discovery restore batch-tg", stderr: true},
	{line: "dormeto aws discovery access"},
	{line: "dormeto aws discovery config show"},
	{line: "dormeto aws discovery config set --file discovery.json --yes", stderr: true, files: map[string]string{"discovery.json": sampleDiscoveryConfig}},
	{line: "dormeto aws exclusions list"},
	{line: "dormeto aws exclusions add --account 444455556666 --reason \"payments, never napped\" --yes", stderr: true},
	{line: "dormeto aws exclusions lift 0d6f3a92-71c4-4e5b-8a2f-9b3c1d7e5a40", stderr: true},

	// users, groups
	{line: "dormeto aws users list"},
	{line: "dormeto aws users invite dev@example.com --role operator --group payments", stderr: true},
	{line: "dormeto aws users role dev@example.com viewer --yes", stderr: true},
	{line: "dormeto aws users groups dev@example.com payments", stderr: true},
	{line: "dormeto aws users disable dev@example.com --yes", stderr: true},
	{line: "dormeto aws users enable dev@example.com", stderr: true},
	{line: "dormeto aws users resend dev@example.com", stderr: true},
	{line: "dormeto aws users reset-mfa dev@example.com", stderr: true},
	{line: "dormeto aws groups list"},
	{line: "dormeto aws groups show payments"},
	{line: "dormeto aws groups create payments --description \"The payments team\" --grant team=payments:read,wake", stderr: true},
	{line: "dormeto aws groups update payments --grant team=payments:read,wake,nap", stderr: true},
	{line: "dormeto aws groups delete payments --yes", stderr: true},

	// operations, manager
	{line: "dormeto aws operations list"},
	{line: "dormeto aws operations show 9c2d4f61-3b8a-4c7e-9f15-2a6e0d8b4c73"},
	{line: "dormeto aws operations watch 9c2d4f61-3b8a-4c7e-9f15-2a6e0d8b4c73"},
	{line: "dormeto aws manager status"},
	{line: "dormeto aws manager changes --to 0.24.0"},
	{line: "dormeto aws manager update --yes", stderr: true},
	{line: "dormeto aws manager check --enable", stderr: true},

	// savings, settings, types
	{line: "dormeto aws savings show"},
	{line: "dormeto aws savings discounts --compute 20 --databases 10", stderr: true},
	{line: "dormeto aws settings show"},
	{line: "dormeto aws settings set --idle-minutes 30", stderr: true},
	{line: "dormeto aws settings waiting-page show"},
	{line: "dormeto aws settings waiting-page set --title \"Waking up\"", stderr: true},
	{line: "dormeto aws settings waiting-page clear", stderr: true},
	{line: "dormeto aws types list"},
	{line: "dormeto aws types show batch"},
	{line: "dormeto aws types set batch --tag team=batch --idle-minutes 15", stderr: true},
	{line: "dormeto aws types delete batch", stderr: true},

	// gcp connect, routers
	{line: "dormeto gcp connect --project my-project --yes", stderr: true},
	{line: "dormeto gcp disconnect --project my-project --yes", stderr: true, google: connected},
	{line: "dormeto gcp routers install --project my-project --region europe-west1 --yes", stderr: true},
	{line: "dormeto gcp routers show --project my-project --region europe-west1", google: routerReporting},
	{line: "dormeto gcp routers settings --project my-project --region europe-west1 --idle-minutes 30", stderr: true},
	{line: "dormeto gcp routers pause --project my-project --region europe-west1 --reason \"database migration\" --for 2h", stderr: true},
	{line: "dormeto gcp routers resume --project my-project --region europe-west1", stderr: true, google: routerPaused},
	{line: "dormeto gcp routers waiting-page show --project my-project --region europe-west1", google: pageOf("router", "Back in a moment")},
	{line: "dormeto gcp routers waiting-page set --project my-project --region europe-west1 --title \"Back in a moment\"", stderr: true},
	{line: "dormeto gcp routers waiting-page clear --project my-project --region europe-west1", stderr: true},
	{line: "dormeto gcp routers uninstall --project my-project --region europe-west1 --yes", stderr: true, google: noWorkloads},

	// gcp workloads
	{line: "dormeto gcp workloads list --project my-project --region europe-west1"},
	{line: "dormeto gcp workloads show shop --project my-project --region europe-west1"},
	{line: "dormeto gcp workloads onboard --project my-project --region europe-west1 --url-map checkout-lb --backend-service checkout --yes", stderr: true},
	{line: "dormeto gcp workloads offboard shop --project my-project --region europe-west1 --yes", stderr: true},
	{line: "dormeto gcp workloads wake shop --project my-project --region europe-west1 --wait", stderr: true, google: shopWaking},
	{line: "dormeto gcp workloads nap blog --project my-project --region europe-west1", stderr: true},
	{line: "dormeto gcp workloads pause shop --project my-project --region europe-west1 --reason \"load test\" --for 4h", stderr: true},
	{line: "dormeto gcp workloads resume shop --project my-project --region europe-west1", stderr: true},
	{line: "dormeto gcp workloads settings shop --project my-project --region europe-west1 --idle-minutes 20", stderr: true},
	{line: "dormeto gcp workloads waiting-page show shop --project my-project --region europe-west1", google: pageOf("shop", "The shop wakes up")},
	{line: "dormeto gcp workloads waiting-page set shop --project my-project --region europe-west1 --title \"The shop wakes up\"", stderr: true},
	{line: "dormeto gcp workloads waiting-page clear shop --project my-project --region europe-west1", stderr: true},
	{line: "dormeto gcp workloads access show --project my-project --region europe-west1 --gke-cluster europe-west1-b/shop-cluster --namespace shop --workload shop", google: shopAccessGranted},
	{line: "dormeto gcp workloads access grant --project my-project --region europe-west1 --gke-cluster europe-west1-b/shop-cluster --namespace shop --workload shop --yes", stderr: true},
	{line: "dormeto gcp workloads access revoke --project my-project --region europe-west1 --gke-cluster europe-west1-b/shop-cluster --namespace shop --workload shop --yes", stderr: true, google: shopAccessGranted},

	// gcp schedule
	{line: "dormeto gcp schedule list --project my-project --region europe-west1", google: shopScheduled},
	{line: "dormeto gcp schedule show shop --project my-project --region europe-west1", google: shopScheduled},
	{line: "dormeto gcp schedule set shop --project my-project --region europe-west1 --file schedule.json", stderr: true, files: map[string]string{"schedule.json": sampleSchedule}},
	{line: "dormeto gcp schedule clear shop --project my-project --region europe-west1", stderr: true},
	{line: "dormeto gcp schedule add shop --project my-project --region europe-west1 --kind nap --days sat,sun --start 00:00 --end 00:00 --wake header", stderr: true, google: shopScheduled},
	{line: "dormeto gcp schedule remove shop --project my-project --region europe-west1 --window 2", stderr: true, google: shopWeekend},
	{line: "dormeto gcp schedule except shop --project my-project --region europe-west1 --date 2026-12-24", stderr: true, google: shopScheduled},
	{line: "dormeto gcp schedule edit shop --project my-project --region europe-west1 --max-nap-days 14", stderr: true, google: shopScheduled},
}

// sampleSchedule is the schedule file of the schedule set samples: awake on weekdays' office hours.
const sampleSchedule = `{"timeZone": "Europe/Paris", "windows": [{"kind": "awake", "days": ["mon", "tue", "wed", "thu", "fri"], "start": "08:00", "end": "19:00"}]}
`

// sampleDiscoveryConfig is the discovery configuration of discovery config set.
const sampleDiscoveryConfig = `{"regions": ["eu-west-1", "us-east-1"]}
`

// shopAccessGranted grants the router its access to the GKE workload shop, as `dormeto gcp workloads access grant` does.
func shopAccessGranted(t *testing.T, _ *gcptest.Server, d gcponboard.Deps) {
	t.Helper()
	r := gcponboard.Request{Project: "my-project", Region: "europe-west1", GKECluster: "europe-west1-b/shop-cluster", Namespace: "shop", ID: "shop", Yes: true}
	if code := gcponboard.WorkloadsAccessGrant(context.Background(), d, r); code != 0 {
		t.Fatalf("the access grant: exit %d", code)
	}
}

// noWorkloads offboards the router's workloads, as an uninstall without --force needs.
func noWorkloads(t *testing.T, g *gcptest.Server, _ gcponboard.Deps) {
	t.Helper()
	g.Lock()
	defer g.Unlock()
	clear(g.Workloads)
}

// connected connects the manager through my-project, as `dormeto gcp connect` does, its record kept in memory.
func connected(t *testing.T, _ *gcptest.Server, d gcponboard.Deps) {
	t.Helper()
	r := gcponboard.ConnectRequest{Project: "my-project", Env: "prod", Yes: true, Manager: &memoryManager{}}
	if code := gcponboard.Connect(context.Background(), d, r); code != 0 {
		t.Fatalf("the connect: exit %d", code)
	}
}

// memoryManager is the manager prod of account 111122223333 as connect sees it, its record in memory.
type memoryManager struct{ rec *fleet.GoogleConnection }

func (m *memoryManager) About(context.Context) (gcponboard.ManagerInfo, error) {
	return gcponboard.ManagerInfo{Version: "0.24.0", Account: "111122223333", Env: "prod", URL: sampleAPI}, nil
}
func (m *memoryManager) GoogleConnection(context.Context) (*fleet.GoogleConnection, error) {
	return m.rec, nil
}
func (m *memoryManager) PutGoogleConnection(_ context.Context, rec fleet.GoogleConnection) (*fleet.GoogleConnection, error) {
	m.rec = &rec
	return &rec, nil
}
func (m *memoryManager) DeleteGoogleConnection(context.Context) error { m.rec = nil; return nil }
func (m *memoryManager) Disconnected(context.Context, string) (time.Time, error) {
	return time.Time{}, nil
}

// routerReporting makes the controller's health answer where it reports to the manager (a router of 0.41.0 and above does),
// five minutes before the samples' clock.
func routerReporting(t *testing.T, g *gcptest.Server, _ gcponboard.Deps) {
	t.Helper()
	g.Lock()
	defer g.Unlock()
	g.HealthFields = map[string]any{"report": map[string]any{"host": "abc123.execute-api.eu-west-1.amazonaws.com",
		"lastSentAt": "2026-11-14T09:55:00Z", "sentThrough": "j-1042"}}
}

// routerPaused pauses the router, as `dormeto gcp routers pause` does.
func routerPaused(t *testing.T, g *gcptest.Server, _ gcponboard.Deps) {
	t.Helper()
	g.Lock()
	defer g.Unlock()
	g.RouterPause = map[string]any{"by": "ops@example.com", "at": "2026-11-14T08:00:00.000Z", "reason": "database migration", "until": "2026-11-14T12:00:00Z", "keepAsleep": false}
}

// shopWaking makes shop's wake go through WAKING to AWAKE.
func shopWaking(t *testing.T, g *gcptest.Server, _ gcponboard.Deps) {
	t.Helper()
	g.Lock()
	defer g.Unlock()
	g.Waking = map[string][]string{"shop": {"WAKING", "AWAKE"}}
}

// shopScheduled gives shop the schedule of the schedule set sample, and its next events.
func shopScheduled(t *testing.T, g *gcptest.Server, _ gcponboard.Deps) {
	t.Helper()
	g.Lock()
	defer g.Unlock()
	g.Schedules["shop"] = map[string]any{"timeZone": "Europe/Paris", "windows": []any{map[string]any{"kind": "awake", "days": []any{"mon", "tue", "wed", "thu", "fri"}, "start": "08:00", "end": "19:00"}},
		"by": "ops@example.com", "at": "2026-11-02T14:00:00.000Z"}
	g.NextEvents = []any{map[string]any{"at": "2026-11-16T07:00:00.000Z", "kind": "awake-start", "window": 0}}
}

// shopWeekend is shopScheduled with the weekend's nap window that schedule add adds.
func shopWeekend(t *testing.T, g *gcptest.Server, d gcponboard.Deps) {
	t.Helper()
	shopScheduled(t, g, d)
	g.Lock()
	defer g.Unlock()
	s := g.Schedules["shop"]
	s["windows"] = append(s["windows"].([]any), map[string]any{"kind": "nap", "days": []any{"sat", "sun"}, "start": "00:00", "end": "00:00", "wake": "header"})
}

// pageOf gives a level (router, or a workload's ID) a waiting page of its own with that title.
func pageOf(level, title string) func(*testing.T, *gcptest.Server, gcponboard.Deps) {
	return func(t *testing.T, g *gcptest.Server, _ gcponboard.Deps) {
		t.Helper()
		g.Lock()
		defer g.Unlock()
		g.WaitingPages = map[string]map[string]any{level: {"mode": "template", "title": title, "by": "ops@example.com", "at": "2026-11-02T14:00:00.000Z"}}
	}
}
