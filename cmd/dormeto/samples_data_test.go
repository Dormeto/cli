package main

// The samples' lines (samples_test.go), by method, in the CLI's help order; the manager's answers they share are in
// testdata/samples/manager.json, a line's own here.

var samples = []sample{
	// whoami, check
	{line: "dormeto aws whoami"},
	{line: "dormeto aws check"},

	// alerts, api, audit, overview, ip-lists
	{line: "dormeto aws alerts list"},
	{line: "dormeto aws api GET /health"},
	{line: "dormeto aws audit --limit 3"},
	{line: "dormeto aws overview"},
	{line: "dormeto aws ip-lists list"},

	// routers
	{line: "dormeto aws routers list"},
	{line: "dormeto aws routers show prod-eu"},
	{line: "dormeto aws routers waiting-page show prod-eu"},

	// workloads
	{line: "dormeto aws workloads list"},
	{line: "dormeto aws workloads show prod-eu/web"},
	{line: "dormeto aws workloads history prod-eu/web --days 7"},
	{line: "dormeto aws workloads idle-window prod-eu/web"},
	{line: "dormeto aws workloads schedule show prod-eu/web"},
	{line: "dormeto aws workloads waiting-page show prod-eu/web"},

	// discovery, exclusions
	{line: "dormeto aws discovery status"},
	{line: "dormeto aws discovery candidates"},
	{line: "dormeto aws discovery access"},
	{line: "dormeto aws discovery config show"},
	{line: "dormeto aws exclusions list"},

	// users, groups
	{line: "dormeto aws users list"},
	{line: "dormeto aws groups list"},
	{line: "dormeto aws groups show payments"},

	// operations, manager
	{line: "dormeto aws operations list"},
	{line: "dormeto aws operations show 9c2d4f61-3b8a-4c7e-9f15-2a6e0d8b4c73"},
	{line: "dormeto aws operations watch 9c2d4f61-3b8a-4c7e-9f15-2a6e0d8b4c73"},
	{line: "dormeto aws manager status"},
	{line: "dormeto aws manager changes --to 0.24.0"},

	// savings, settings, types
	{line: "dormeto aws savings show"},
	{line: "dormeto aws settings show"},
	{line: "dormeto aws settings waiting-page show"},
	{line: "dormeto aws types list"},
	{line: "dormeto aws types show batch"},
}

// sampleSchedule is the schedule file of the schedule set samples: awake on weekdays' office hours.
const sampleSchedule = `{"timeZone": "Europe/Paris", "windows": [{"kind": "awake", "days": ["mon", "tue", "wed", "thu", "fri"], "start": "08:00", "end": "19:00"}]}
`

// sampleDiscoveryConfig is the discovery configuration of discovery config set.
const sampleDiscoveryConfig = `{"regions": ["eu-west-1", "us-east-1"]}
`
