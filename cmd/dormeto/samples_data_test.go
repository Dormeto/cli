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
	{line: "dormeto aws workloads history prod-eu/web --days 7"},
	{line: "dormeto aws workloads idle-window prod-eu/web"},
	{line: "dormeto aws workloads wake prod-eu/web --wait", stderr: true},
	{line: "dormeto aws workloads nap prod-eu/web", stderr: true},
	{line: "dormeto aws workloads keep-awake prod-eu/web --for 2h", stderr: true},
	{line: "dormeto aws workloads pause prod-eu/web --reason \"load test\" --for 4h", stderr: true},
	{line: "dormeto aws workloads resume prod-eu/web", stderr: true},
	{line: "dormeto aws workloads exclude prod-eu/web --reason \"always on\" --for 30d --yes", stderr: true},
	{line: "dormeto aws workloads onboard api-tg", stderr: true},
	{line: "dormeto aws workloads offboard prod-eu/web --yes", stderr: true},
	{line: "dormeto aws workloads rename prod-eu/web \"Web shop\"", stderr: true},
	{line: "dormeto aws workloads tags prod-eu/web --set owner=web-team", stderr: true},
	{line: "dormeto aws workloads settings prod-eu/web --idle-minutes 20", stderr: true},
	{line: "dormeto aws workloads description prod-eu/web --set \"The public site\"", stderr: true},
	{line: "dormeto aws workloads schedule show prod-eu/web"},
	{line: "dormeto aws workloads schedule set prod-eu/web --file schedule.json", stderr: true, files: map[string]string{"schedule.json": sampleSchedule}},
	{line: "dormeto aws workloads schedule clear prod-eu/web", stderr: true},
	{line: "dormeto aws workloads waiting-page show prod-eu/web"},
	{line: "dormeto aws workloads waiting-page set prod-eu/web --title \"The shop wakes up\"", stderr: true},
	{line: "dormeto aws workloads waiting-page clear prod-eu/web", stderr: true},

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
}

// sampleSchedule is the schedule file of the schedule set samples: awake on weekdays' office hours.
const sampleSchedule = `{"timeZone": "Europe/Paris", "windows": [{"kind": "awake", "days": ["mon", "tue", "wed", "thu", "fri"], "start": "08:00", "end": "19:00"}]}
`

// sampleDiscoveryConfig is the discovery configuration of discovery config set.
const sampleDiscoveryConfig = `{"regions": ["eu-west-1", "us-east-1"]}
`
