package main

// `dormeto workloads onboard|offboard|exclude` and `dormeto exclusions` against the fake manager API: the bodies they
// send, what they print, the refusals that send nothing.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// obAPI is a fake manager for onboarding and exclusions: the candidates pending onboarding, the operations started (each
// reads running once, then succeeded), the conflicts, and every write it received.
type obAPI struct {
	mu         sync.Mutex
	pending    []string          // the candidates' JSON rows
	conflicts  map[string]string // workload id (or exclusion id) -> the conflicting operation's id
	noTG       map[string]bool   // workloads without a target group
	writes     []string          // "METHOD path body"
	types      map[string]string // operation id -> type
	reads      map[string]int
	active     int // operations started and not yet read finished
	maxActive  int
	exclusions string // the answer of GET /iam/exclusions
}

const (
	tgAPI = "api"
	tgWeb = "web"
)

func (a *obAPI) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.types == nil {
		a.types, a.reads = map[string]string{}, map[string]int{}
	}
	path := r.URL.EscapedPath()
	if r.Method != http.MethodGet {
		a.writes = append(a.writes, r.Method+" "+path+" "+string(body))
	}
	start := func(typ, key string) {
		if id := a.conflicts[key]; id != "" {
			w.WriteHeader(409)
			fmt.Fprintf(w, `{"error":"conflict","message":"another operation holds it","operationId":%q}`, id)
			return
		}
		id := fmt.Sprintf("op-%d", len(a.types)+1)
		a.types[id] = typ
		if a.active++; a.active > a.maxActive {
			a.maxActive = a.active
		}
		w.WriteHeader(202)
		fmt.Fprintf(w, `{"operation":{"id":%q,"type":%q,"status":"running","target":{},"steps":[]},"candidates":1}`, id, typ)
	}
	switch {
	case r.Method == http.MethodGet && path == "/iam/discovery/candidates":
		io.WriteString(w, `{"candidates":[`+strings.Join(a.pending, ",")+`],"cursor":null}`)
	case r.Method == http.MethodGet && path == "/iam/routers":
		io.WriteString(w, `{"routers":[{"name":"prod-eu","account":"111111111111","region":"eu-west-1","source":"manager"}]}`)
	case r.Method == http.MethodPost && path == "/iam/operations":
		var req struct{ Type, WorkloadID, Router, CandidateID string }
		_ = json.Unmarshal(body, &req)
		key := req.WorkloadID
		if req.Type == "onboard" {
			key = req.CandidateID
		}
		start(req.Type, key)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/iam/operations/"):
		id := strings.TrimPrefix(path, "/iam/operations/")
		a.reads[id]++
		if a.reads[id] == 1 {
			fmt.Fprintf(w, `{"id":%q,"type":%q,"status":"running","target":{},"steps":[{"name":"preview","status":"running"}]}`, id, a.types[id])
			return
		}
		if a.reads[id] == 2 {
			a.active--
		}
		extra := ""
		if a.types[id] == "onboard" {
			extra = `,"context":{"workloadId":"w-new","router":{"name":"prod-eu"}}`
		}
		fmt.Fprintf(w, `{"id":%q,"type":%q,"status":"succeeded","target":{"account":"111111111111","region":"eu-west-1"},"steps":[]%s}`, id, a.types[id], extra)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/iam/workloads/"):
		id := path[strings.LastIndex(path, "/")+1:]
		if a.noTG[id] {
			io.WriteString(w, `{"workloadId":"`+id+`","state":"AWAKE"}`)
			return
		}
		fmt.Fprintf(w, `{"workloadId":%q,"state":"AWAKE","targetGroupArn":%q}`, id, tgARN("111111111111", "tg-"+id))
	case r.Method == http.MethodPost && path == "/iam/exclusions":
		var req struct{ Workload struct{ ID string } }
		_ = json.Unmarshal(body, &req)
		start("exclude", req.Workload.ID)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/iam/exclusions/"):
		id, _ := url.PathUnescape(strings.TrimPrefix(path, "/iam/exclusions/"))
		start("lift-exclusion", id)
	case r.Method == http.MethodGet && path == "/iam/exclusions":
		io.WriteString(w, a.exclusions)
	default:
		w.WriteHeader(404)
	}
}

func runOB(t *testing.T, a *obAPI, tune func(*fleetDeps), args ...string) fleetRan {
	t.Helper()
	return runFleetWith(t, &fakeAPI{custom: a.handler}, "eu-west-1", tune, args...)
}

func pendingAPI() *obAPI {
	return &obAPI{pending: []string{row("111111111111", tgAPI, "pending-onboarding"), row("222222222222", tgWeb, "pending-onboarding"), row("333333333333", tgWeb, "pending-onboarding")}}
}

func TestOnboardPostsTheDashboardsBodyAndNamesTheWorkload(t *testing.T) {
	a := pendingAPI()
	r := runOB(t, a, nil, "workloads", "onboard", tgAPI, "--name", " My API ", "--idle-minutes", "30", "--wake-mode", "browser",
		"--database", "cluster:aurora-1", "--database", "db-1", "--fallback-type", "t3a.medium", "--fallback-type", "m6i.large", "--set-tag", "team=pay", "--set-tag", "env=a=b")
	want := `POST /iam/operations {"candidateId":"` + tgARN("111111111111", tgAPI) + `","databases":[{"id":"aurora-1","kind":"rds-cluster"},{"id":"db-1","kind":"rds-instance"}],"name":"My API","settings":{"fallbackInstanceTypes":["t3a.medium","m6i.large"],"idleMinutes":30,"wakeMode":"browser"},"tags":{"env":"a=b","team":"pay"},"type":"onboard"}`
	if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != want {
		t.Fatalf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	if !strings.Contains(r.stdout, "onboarded prod-eu/w-new") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestOnboardDefaultsAreTheDialogs(t *testing.T) {
	a := pendingAPI()
	r := runOB(t, a, nil, "workloads", "onboard", tgAPI, "--no-wait")
	want := `POST /iam/operations {"candidateId":"` + tgARN("111111111111", tgAPI) + `","databases":[],"settings":{"idleMinutes":15,"wakeMode":"any"},"type":"onboard"}`
	if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != want || strings.Contains(r.stdout, "onboarded") {
		t.Fatalf("exit %d, writes %q, stdout %q", r.exit, a.writes, r.stdout)
	}
}

func TestOnboardRefusalsSendNothing(t *testing.T) {
	k8s := `{"id":"arn:aws:elasticloadbalancing:eu-west-1:111111111111:targetgroup/kube/0123456789abcdef","account":"111111111111","region":"eu-west-1","status":"pending-onboarding","backend":{"kind":"k8s","id":"x"}}`
	for _, args := range [][]string{
		{tgAPI, "--idle-minutes", "4"},
		{tgAPI, "--idle-minutes", "1441"},
		{tgAPI, "--wake-mode", "sometimes"},
		{tgAPI, "--fallback-type", "a.b", "--fallback-type", "t3a.medium", "--fallback-type", "t3a.large", "--fallback-type", "m6i.large", "--fallback-type", "m6i.xlarge", "--fallback-type", "c6i.large"},
		{tgAPI, "--fallback-type", "medium"},
		{tgAPI, "--set-tag", "novalue"},
		{tgAPI, "--set-tag", "dormeto:x=1"},
		{tgAPI, "--set-tag", "a=1", "--set-tag", "a=2"},
		{tgAPI, "--name", strings.Repeat("n", 65)},
		{tgAPI, "--timeout", "0s"},
		{tgWeb},    // two accounts
		{"nobody"}, // unknown
		{},
		{tgAPI, tgWeb},
	} {
		a := pendingAPI()
		a.pending = append(a.pending, k8s)
		r := runOB(t, a, nil, append([]string{"workloads", "onboard"}, args...)...)
		if r.exit != 2 || len(a.writes) != 0 {
			t.Errorf("%v: exit %d, writes %q, stderr %q", args, r.exit, a.writes, r.stderr)
		}
	}
	a := pendingAPI()
	r := runOB(t, a, nil, "workloads", "onboard", tgWeb)
	if !strings.Contains(r.stderr, tgARN("222222222222", tgWeb)) || !strings.Contains(r.stderr, tgARN("333333333333", tgWeb)) {
		t.Errorf("both ARNs are named: %q", r.stderr)
	}
}

// The router has no rule against fallback types for a Kubernetes workload: they are sent as for any other.
func TestOnboardKubernetesCandidateSendsItsFallbackTypes(t *testing.T) {
	k8s := `{"id":"arn:aws:elasticloadbalancing:eu-west-1:111111111111:targetgroup/kube/0123456789abcdef","account":"111111111111","region":"eu-west-1","status":"pending-onboarding","backend":{"kind":"k8s","id":"x"}}`
	a := &obAPI{pending: []string{k8s}}
	r := runOB(t, a, nil, "workloads", "onboard", "kube", "--fallback-type", "t3a.medium", "--no-wait")
	if r.exit != 0 || len(a.writes) != 1 || !strings.Contains(a.writes[0], `"fallbackInstanceTypes":["t3a.medium"]`) {
		t.Fatalf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

// An empty or blank --name is no name, as the manager takes it: the target group's name is the default.
func TestOnboardEmptyNameIsTheDefaultName(t *testing.T) {
	for _, name := range []string{"", "   "} {
		a := pendingAPI()
		r := runOB(t, a, nil, "workloads", "onboard", tgAPI, "--name", name, "--no-wait")
		if r.exit != 0 || len(a.writes) != 1 || strings.Contains(a.writes[0], `"name"`) {
			t.Errorf("--name %q: exit %d, writes %q, stderr %q", name, r.exit, a.writes, r.stderr)
		}
	}
}

func TestOnboardConflictNamesTheOtherOperation(t *testing.T) {
	a := pendingAPI()
	a.conflicts = map[string]string{tgARN("111111111111", tgAPI): "op-77"}
	r := runOB(t, a, nil, "workloads", "onboard", tgAPI)
	if r.exit != 1 || !strings.Contains(r.stderr, "op-77") || !strings.Contains(r.stderr, "dormeto operations watch op-77") {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestOnboardJSONPrintsTheOperation(t *testing.T) {
	a := pendingAPI()
	r := runOB(t, a, nil, "workloads", "onboard", tgAPI, "--json")
	if r.exit != 0 || !strings.HasPrefix(strings.TrimSpace(r.stdout), "{") || strings.Contains(r.stdout, "onboarded") || !strings.Contains(r.stderr, "onboarded prod-eu/w-new") {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
}

func TestOffboardIsAlwaysConfirmed(t *testing.T) {
	a := &obAPI{}
	r := runOB(t, a, nil, "workloads", "offboard", "prod-eu/w-1")
	if r.exit != 2 || len(a.writes) != 0 || !strings.Contains(r.stderr, "--yes") {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	var asked []string
	r = runOB(t, a, withTerminal(false, &asked), "workloads", "offboard", "prod-eu/w-1")
	if r.exit != 0 || len(asked) != 1 || len(a.writes) != 0 || !strings.Contains(r.stdout, "not confirmed: nothing sent") {
		t.Errorf("exit %d, asked %q, writes %q, stdout %q", r.exit, asked, a.writes, r.stdout)
	}
	r = runOB(t, a, nil, "workloads", "offboard", "prod-eu/w-1", "--dry-run")
	if r.exit != 0 || len(a.writes) != 0 || !strings.Contains(r.stdout, "dry run") {
		t.Errorf("exit %d, writes %q, stdout %q", r.exit, a.writes, r.stdout)
	}
	r = runOB(t, a, withTerminal(true, &asked), "workloads", "offboard", "prod-eu/w-1")
	if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != `POST /iam/operations {"router":"prod-eu","type":"offboard","workloadId":"w-1"}` {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestOffboardOfThreeRunsTwoAtATime(t *testing.T) {
	a := &obAPI{}
	r := runOB(t, a, nil, "workloads", "offboard", "prod-eu/w-1", "prod-eu/w-2", "prod-eu/w-3", "--yes", "--concurrency", "2")
	if r.exit != 0 || len(a.writes) != 3 || a.maxActive != 2 {
		t.Errorf("exit %d, writes %q, most at once %d, stderr %q", r.exit, a.writes, a.maxActive, r.stderr)
	}
}

func TestOffboardOneConflictTheOtherGoesOn(t *testing.T) {
	a := &obAPI{conflicts: map[string]string{"w-1": "op-99"}}
	r := runOB(t, a, nil, "workloads", "offboard", "prod-eu/w-1", "prod-eu/w-2", "--yes")
	if r.exit != 1 || len(a.writes) != 2 || !strings.Contains(r.stderr, "op-99") || !strings.Contains(r.stdout, "prod-eu/w-2: offboard succeeded") {
		t.Errorf("exit %d, writes %q, stdout %q, stderr %q", r.exit, a.writes, r.stdout, r.stderr)
	}
}

func TestOffboardBadConcurrencyOrTargetsSendNothing(t *testing.T) {
	for _, args := range [][]string{
		{"prod-eu/w-1", "--concurrency", "0"},
		{"prod-eu/w-1", "--concurrency", "21"},
		{"prod-eu"},
		{},
		{"prod-eu/w-1", "--tag", "a=b"},
	} {
		a := &obAPI{}
		r := runOB(t, a, nil, append([]string{"workloads", "offboard", "--yes"}, args...)...)
		if r.exit != 2 || len(r.api.requests) != 0 {
			t.Errorf("%v: exit %d, requests %d, stderr %q", args, r.exit, len(r.api.requests), r.stderr)
		}
	}
}

func TestExcludeSendsTheWorkloadsTargetGroupAndTheHint(t *testing.T) {
	a := &obAPI{}
	r := runOB(t, a, nil, "workloads", "exclude", "prod-eu/w-1", "--reason", " migrating ", "--for", "2d", "--yes")
	want := `POST /iam/exclusions {"expiresAt":"2026-10-21T12:00:00Z","reason":"migrating","target":{"arn":"` + tgARN("111111111111", "tg-w-1") + `","kind":"target-group"},"workload":{"id":"w-1","router":"prod-eu"}}`
	if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != want {
		t.Fatalf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	if !strings.Contains(r.stdout, "exclude succeeded") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestExcludeWithoutEndSendsNoExpiry(t *testing.T) {
	a := &obAPI{}
	r := runOB(t, a, nil, "workloads", "exclude", "prod-eu/w-1", "--reason", "gone", "--yes", "--no-wait")
	if r.exit != 0 || strings.Contains(a.writes[0], "expiresAt") {
		t.Errorf("exit %d, writes %q", r.exit, a.writes)
	}
}

func TestExcludeIsAlwaysConfirmedAndRefusalsSendNothing(t *testing.T) {
	a := &obAPI{}
	r := runOB(t, a, nil, "workloads", "exclude", "prod-eu/w-1", "--reason", "x")
	if r.exit != 2 || len(a.writes) != 0 {
		t.Errorf("exit %d, writes %q", r.exit, a.writes)
	}
	var asked []string
	r = runOB(t, a, withTerminal(false, &asked), "workloads", "exclude", "prod-eu/w-1", "--reason", "x")
	if r.exit != 0 || len(asked) != 1 || len(a.writes) != 0 {
		t.Errorf("exit %d, asked %q, writes %q", r.exit, asked, a.writes)
	}
	for _, args := range [][]string{
		{"prod-eu/w-1"},
		{"prod-eu/w-1", "--reason", ""},
		{"prod-eu/w-1", "--reason", strings.Repeat("x", 247)},
		{"prod-eu/w-1", "--reason", "x", "--for", "367d"},
		{"prod-eu/w-1", "--reason", "x", "--for", "1h", "--until", "23:00"},
		{"--reason", "x"},
	} {
		a := &obAPI{}
		r := runOB(t, a, nil, append([]string{"workloads", "exclude", "--yes"}, args...)...)
		if r.exit != 2 || len(r.api.requests) != 0 {
			t.Errorf("%v: exit %d, requests %d, stderr %q", args, r.exit, len(r.api.requests), r.stderr)
		}
	}
	r = runOB(t, &obAPI{}, nil, "workloads", "exclude", "prod-eu/w-1", "--reason", strings.Repeat("é", 246), "--yes", "--no-wait")
	if r.exit != 0 {
		t.Errorf("246 characters: exit %d, stderr %q", r.exit, r.stderr)
	}
}

func TestExcludeWithoutTargetGroupFailsThatTargetTheOthersGoOn(t *testing.T) {
	a := &obAPI{noTG: map[string]bool{"w-1": true}}
	r := runOB(t, a, nil, "workloads", "exclude", "prod-eu/w-1", "prod-eu/w-2", "--reason", "x", "--yes")
	if r.exit != 1 || len(a.writes) != 1 || !strings.Contains(r.stderr, "prod-eu/w-1") || !strings.Contains(r.stderr, "no target group") {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestExclusionsAddBodies(t *testing.T) {
	tg := tgARN("111111111111", "tg-a")
	lb := "arn:aws:elasticloadbalancing:eu-west-1:111111111111:loadbalancer/app/my-lb/0123456789abcdef"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--target-group", tg}, `{"reason":"r","target":{"arn":"` + tg + `","kind":"target-group"}}`},
		{[]string{"--load-balancer", lb}, `{"reason":"r","target":{"arn":"` + lb + `","kind":"load-balancer"}}`},
		{[]string{"--account", "111111111111", "--for", "1h"}, `{"expiresAt":"2026-10-19T13:00:00Z","reason":"r","target":{"account":"111111111111","kind":"account"}}`},
	} {
		a := &obAPI{}
		r := runOB(t, a, nil, append([]string{"exclusions", "add", "--reason", "r", "--yes"}, c.args...)...)
		if r.exit != 0 || len(a.writes) != 1 || a.writes[0] != "POST /iam/exclusions "+c.want {
			t.Errorf("%v: exit %d, writes %q, stderr %q", c.args, r.exit, a.writes, r.stderr)
		}
	}
}

func TestExclusionsAddRefusalsSendNothing(t *testing.T) {
	tg := tgARN("111111111111", "tg-a")
	for _, args := range [][]string{
		{"--reason", "r"},
		{"--reason", "r", "--target-group", tg, "--account", "111111111111"},
		{"--reason", "r", "--target-group", tg, "--load-balancer", tg},
		{"--reason", "r", "--target-group", "arn:nope"},
		{"--reason", "r", "--load-balancer", tg},
		{"--reason", "r", "--account", "1234"},
		{"--target-group", tg},
		{"--reason", strings.Repeat("x", 247), "--target-group", tg},
		{"--reason", "r", "--target-group", tg, "--for", "400d"},
	} {
		a := &obAPI{}
		r := runOB(t, a, nil, append([]string{"exclusions", "add", "--yes"}, args...)...)
		if r.exit != 2 || len(r.api.requests) != 0 {
			t.Errorf("%v: exit %d, requests %d, stderr %q", args, r.exit, len(r.api.requests), r.stderr)
		}
	}
	a := &obAPI{}
	r := runOB(t, a, nil, "exclusions", "add", "--reason", "r", "--account", "111111111111")
	if r.exit != 2 || len(a.writes) != 0 || !strings.Contains(r.stderr, "--yes") {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
}

func TestExclusionsLiftFollowsTheLiftOperation(t *testing.T) {
	a := &obAPI{}
	r := runOB(t, a, nil, "exclusions", "lift", "ex/1", "ex-2")
	if r.exit != 0 || !sameSet(a.writes, "DELETE /iam/exclusions/ex%2F1 ", "DELETE /iam/exclusions/ex-2 ") {
		t.Fatalf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	if !strings.Contains(r.stdout, "lift-exclusion op-1 for ex") || !strings.Contains(r.stdout, "lift-exclusion succeeded") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestExclusionsLiftConflictNamesTheOtherOperation(t *testing.T) {
	a := &obAPI{conflicts: map[string]string{"ex-1": "op-5"}}
	r := runOB(t, a, nil, "exclusions", "lift", "ex-1", "ex-2")
	if r.exit != 1 || len(a.writes) != 2 || !strings.Contains(r.stderr, "operation op-5") {
		t.Errorf("exit %d, writes %q, stderr %q", r.exit, a.writes, r.stderr)
	}
	if r = runOB(t, a, nil, "exclusions", "lift"); r.exit != 2 {
		t.Errorf("no id: exit %d", r.exit)
	}
}

const exclusionsAnswer = `{"exclusions":[
 {"id":"e1","target":{"kind":"target-group","arn":"x","account":"111111111111","region":"eu-west-1","name":"tg-a"},"reason":"migrating","by":"arn:aws:sts::1:assumed-role/R/ann","at":"2026-10-18T08:00:00.000Z","expiresAt":"2026-10-25T08:00:00.000Z","paused":[{"router":"prod-eu","workloadId":"w-1","name":"api"},{"router":"prod-eu","workloadId":"w-2"}],"expired":false,"lifting":false},
 {"id":"e2","target":{"kind":"account","account":"222222222222","name":"sandbox"},"reason":"freeze","by":"bob","at":"2026-10-01T08:00:00.000Z","expired":true,"lifting":false},
 {"id":"e3","target":{"kind":"load-balancer","arn":"y","account":"111111111111","region":"eu-west-1","name":"lb"},"reason":"r","by":"bob","at":"2026-10-19T08:00:00.000Z","liftedAt":"2026-10-19T09:00:00.000Z","expired":false,"lifting":true}
]}`

func TestExclusionsListText(t *testing.T) {
	a := &obAPI{exclusions: exclusionsAnswer}
	r := runOB(t, a, nil, "exclusions", "list")
	if r.exit != 0 {
		t.Fatalf("exit %d, stderr %q", r.exit, r.stderr)
	}
	for _, want := range []string{"target group tg-a", "migrating", "R/ann", "2026-10-25 08:00 UTC", "api, w-2", "expired", "lifting", "account sandbox (222222222222)", "load balancer lb"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q: %s", want, r.stdout)
		}
	}
}

func TestExclusionsListJSONKeepsTheRows(t *testing.T) {
	a := &obAPI{exclusions: exclusionsAnswer}
	r := runOB(t, a, nil, "exclusions", "list", "--json")
	if r.exit != 0 || !strings.Contains(r.stdout, `"expired": true`) || !strings.Contains(r.stdout, `"liftedAt"`) {
		t.Errorf("exit %d, stdout %q", r.exit, r.stdout)
	}
	r = runOB(t, &obAPI{exclusions: `{"exclusions":[]}`}, nil, "exclusions", "list")
	if r.exit != 0 || !strings.Contains(r.stdout, "no exclusions") {
		t.Errorf("empty: exit %d, stdout %q", r.exit, r.stdout)
	}
}
