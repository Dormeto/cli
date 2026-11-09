package gcponboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/gcp/access"
	"github.com/Dormeto/cli/internal/gcp/gcptest"
)

// fakeManager is the manager's /iam API as connect and disconnect use it: what it says of itself and its record.
type fakeManager struct {
	info         ManagerInfo
	rec          *fleet.GoogleConnection
	puts         []fleet.GoogleConnection
	deletes      int
	disconnected time.Time
	reads        int
}

func (m *fakeManager) About(context.Context) (ManagerInfo, error) { m.reads++; return m.info, nil }
func (m *fakeManager) GoogleConnection(context.Context) (*fleet.GoogleConnection, error) {
	if m.rec == nil {
		return nil, nil
	}
	c := *m.rec
	return &c, nil
}
func (m *fakeManager) PutGoogleConnection(_ context.Context, rec fleet.GoogleConnection) (*fleet.GoogleConnection, error) {
	m.puts = append(m.puts, rec)
	m.rec = &rec
	return &rec, nil
}
func (m *fakeManager) DeleteGoogleConnection(context.Context) error {
	m.deletes++
	m.rec = nil
	return nil
}
func (m *fakeManager) Disconnected(context.Context, string) (time.Time, error) {
	return m.disconnected, nil
}

func newFakeManager() *fakeManager {
	return &fakeManager{info: ManagerInfo{Version: "0.24.0", Account: "121014831971", Env: "dev", URL: "https://abc123.execute-api.eu-west-1.amazonaws.com"}}
}

const (
	discoveryEmail = "dmt-dev-discovery@dormeto-dev.iam.gserviceaccount.com"
	commandEmail   = "dmt-dev-command@dormeto-dev.iam.gserviceaccount.com"
)

// connectServer is dormeto-dev with a router in europe-west1 (its controller at version 0.40.0, the tag value
// dormeto-dev/dormeto/managed) and the manager's APIs off but Resource Manager's.
func connectServer(t *testing.T) *gcptest.Server {
	t.Helper()
	s := gcptest.New(t)
	s.Enabled = []string{"run.googleapis.com", "cloudresourcemanager.googleapis.com"}
	routerAt(s, "europe-west1")
	s.TagKeys["tagKeys/8"] = &gcptest.Tag{Name: "tagKeys/8", Parent: "projects/1034397096264", ShortName: "dormeto", NamespacedName: "dormeto-dev/dormeto"}
	s.TagValues["tagValues/9"] = &gcptest.Tag{Name: "tagValues/9", Parent: "tagKeys/8", ShortName: "managed", NamespacedName: "dormeto-dev/dormeto/managed"}
	return s
}

func routerAt(s *gcptest.Server, region string) {
	s.Lock()
	defer s.Unlock()
	s.RunServices[region+"/dmtr-"+region+"-controller"] = &gcptest.RunService{URI: "https://dmtr-" + region + "-controller-abcdefghij-ew.a.run.app",
		Template: gcptest.RunTemplate{Containers: []gcptest.RunContainer{{Env: []gcptest.RunEnv{{Name: "DMT_VERSION", Value: "0.40.0"}}}}}}
}

func connectDeps(s *gcptest.Server) (Deps, *bytes.Buffer, *bytes.Buffer) {
	var out, errs bytes.Buffer
	return Deps{Google: &gcp.Client{HTTP: s.HTTP, Tokens: gcptest.Tokens{AccountName: "me@example.com"}, Sleep: noWait}, Out: &out, Err: &errs,
		Now: func() time.Time { return time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC) }, Sleep: noWait, Version: "0.14.0"}, &out, &errs
}

func connectRequest(m Manager) ConnectRequest {
	return ConnectRequest{Project: "dormeto-dev", Profile: "kloudy", Env: "dev", Yes: true, Manager: m}
}

func runConnect(t *testing.T, s *gcptest.Server, r ConnectRequest) (int, string) {
	t.Helper()
	d, out, _ := connectDeps(s)
	code := Connect(context.Background(), d, r)
	return code, out.String()
}

func runDisconnect(t *testing.T, s *gcptest.Server, r ConnectRequest) (int, string) {
	t.Helper()
	d, out, _ := connectDeps(s)
	code := Disconnect(context.Background(), d, r)
	return code, out.String()
}

func principal(role string) string {
	return "principalSet://iam.googleapis.com/projects/1034397096264/locations/global/workloadIdentityPools/dmt-dev/attribute.aws_role/" + role
}

// bindingsOf are the roles a policy gives member, sorted.
func rolesOf(policy map[string]any, member string) []string {
	var out []string
	bindings, _ := policy["bindings"].([]any)
	for _, b := range bindings {
		m, _ := b.(map[string]any)
		members, _ := m["members"].([]any)
		for _, x := range members {
			if s, _ := x.(string); strings.TrimPrefix(strings.Split(s, "?")[0], "deleted:") == member {
				out = append(out, m["role"].(string))
			}
		}
	}
	slices.Sort(out)
	return out
}

func lines(out string) []string { return strings.Split(strings.TrimRight(out, "\n"), "\n") }

// The design's §4.3 and §4.2: the plan names the pool, the provider with its condition verbatim, the two accounts, the
// impersonation of each of the manager's roles, dormetoDiscovery at the host's project scope, and for the router the
// command account's dormetoCommand and its grants on the controller and on the tag value; the apply sets them up as
// planned, verifies, and writes the manager's record with the router's controller and version.
func TestConnectPlansAndSetsUpTheFederationTheAccountsTheRolesAndTheGrants(t *testing.T) {
	s := connectServer(t)
	m := newFakeManager()
	code, out := runConnect(t, s, connectRequest(m))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	t.Logf("\n%s", out)
	want := []string{
		"Manager 0.24.0, environment dev, AWS account 121014831971 (https://abc123.execute-api.eu-west-1.amazonaws.com).",
		"Checked: me@example.com holds every permission dormeto gcp connect needs in project dormeto-dev (host).",
		"Plan for manager dev's connection through project dormeto-dev:",
		"  + API iam.googleapis.com",
		"  + API iamcredentials.googleapis.com",
		"  + API sts.googleapis.com",
		"  + API cloudbilling.googleapis.com",
		`  + role dormetoDiscovery "Dormeto manager discovery" (32 permissions) in project dormeto-dev`,
		`  + role dormetoCommand "Dormeto manager command" (10 permissions) in project dormeto-dev`,
		"  + workload identity pool dmt-dev",
		"  + provider aws of pool dmt-dev: AWS account 121014831971, condition assertion.arn.startsWith('arn:aws:sts::121014831971:assumed-role/dmt-dev-manager-'), mapping google.subject=assertion.arn, attribute.aws_role=assertion.arn.extract('assumed-role/{role}/')",
		"  + service account " + discoveryEmail,
		"  + service account " + commandEmail,
		"  + grant roles/iam.workloadIdentityUser to " + principal("dmt-dev-manager-discovery") + " on service account " + discoveryEmail,
		"  + grant roles/iam.workloadIdentityUser to " + principal("dmt-dev-manager-api") + " on service account " + discoveryEmail,
		"  + grant roles/iam.workloadIdentityUser to " + principal("dmt-dev-manager-savings") + " on service account " + discoveryEmail,
		"  + grant roles/iam.workloadIdentityUser to " + principal("dmt-dev-manager-operations") + " on service account " + commandEmail,
		"  + grant roles/iam.workloadIdentityUser to " + principal("dmt-dev-manager-api") + " on service account " + commandEmail,
		"  + grant roles/iam.workloadIdentityUser to " + principal("dmt-dev-manager-fleet") + " on service account " + commandEmail,
		"  + grant projects/dormeto-dev/roles/dormetoDiscovery to serviceAccount:" + discoveryEmail + " on project dormeto-dev",
		"  + grant projects/dormeto-dev/roles/dormetoCommand to serviceAccount:" + commandEmail + " on project dormeto-dev",
		"  + grant roles/run.invoker to serviceAccount:" + commandEmail + " on controller dmtr-europe-west1-controller of project dormeto-dev",
		"  + grant roles/resourcemanager.tagUser to serviceAccount:" + commandEmail + " on tag value dormeto-dev/dormeto/managed",
		"  + record of the connection in manager dev: 1 router (dormeto-dev/europe-west1), 1 scope (projects/dormeto-dev)",
	}
	got := lines(out)
	for i, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("no line %d %q", i, w)
		}
	}
	for _, w := range []string{"  ✓ apis: turned on iam.googleapis.com, iamcredentials.googleapis.com, sts.googleapis.com, cloudbilling.googleapis.com",
		"  ✓ verified: the pool and its provider, the two service accounts, the roles' permissions and every planned grant",
		"  ✓ record: wrote the connection in manager dev: 1 router registered",
		"Connected manager dev to Google Cloud through project dormeto-dev: 1 router, 1 scope."} {
		if !slices.Contains(got, w) {
			t.Errorf("no line %q", w)
		}
	}

	pool, provider := s.Pools["dmt-dev"], s.Providers["dmt-dev/aws"]
	if pool == nil || pool.State != "ACTIVE" || provider == nil || provider.AccountID != "121014831971" ||
		provider.Condition != "assertion.arn.startsWith('arn:aws:sts::121014831971:assumed-role/dmt-dev-manager-')" ||
		!reflect.DeepEqual(provider.Mapping, map[string]string{"google.subject": "assertion.arn", "attribute.aws_role": "assertion.arn.extract('assumed-role/{role}/')"}) {
		t.Fatalf("pool %+v provider %+v", pool, provider)
	}
	if s.ServiceAccounts[discoveryEmail] == nil || s.ServiceAccounts[commandEmail] == nil {
		t.Fatalf("accounts %v", s.ServiceAccounts)
	}
	if r := s.Roles["dormetoDiscovery"]; r == nil || r.Description != "Dormeto 0.14.0: "+access.Load().Manager.Roles["dormetoDiscovery"].Description ||
		!slices.Equal(r.Permissions, access.Load().Manager.Roles["dormetoDiscovery"].Permissions) || r.Stage != "GA" {
		t.Fatalf("dormetoDiscovery %+v", r)
	}
	if got := rolesOf(s.Policy, "serviceAccount:"+discoveryEmail); !slices.Equal(got, []string{"projects/dormeto-dev/roles/dormetoDiscovery"}) {
		t.Errorf("discovery on the project: %v", got)
	}
	if got := rolesOf(s.Policy, "serviceAccount:"+commandEmail); !slices.Equal(got, []string{"projects/dormeto-dev/roles/dormetoCommand"}) {
		t.Errorf("command on the project: %v", got)
	}
	if got := rolesOf(s.Policies["projects/dormeto-dev/locations/europe-west1/services/dmtr-europe-west1-controller"], "serviceAccount:"+commandEmail); !slices.Equal(got, []string{"roles/run.invoker"}) {
		t.Errorf("command on the controller: %v", got)
	}
	if got := rolesOf(s.Policies["tagValues/9"], "serviceAccount:"+commandEmail); !slices.Equal(got, []string{"roles/resourcemanager.tagUser"}) {
		t.Errorf("command on the tag value: %v", got)
	}
	for account, roles := range map[string][]string{discoveryEmail: {"discovery", "api", "savings"}, commandEmail: {"operations", "api", "fleet"}} {
		for _, role := range roles {
			if got := rolesOf(s.Policies["projects/dormeto-dev/serviceAccounts/"+account], principal("dmt-dev-manager-"+role)); !slices.Equal(got, []string{"roles/iam.workloadIdentityUser"}) {
				t.Errorf("%s impersonated by %s: %v", account, role, got)
			}
		}
	}

	if len(m.puts) != 1 {
		t.Fatalf("puts %+v", m.puts)
	}
	rec := m.puts[0]
	if rec.Project != "dormeto-dev" || rec.ProjectNumber != "1034397096264" || rec.Pool != "dmt-dev" || rec.Provider != "aws" ||
		!slices.Equal(rec.Scopes, []string{"projects/dormeto-dev"}) ||
		rec.ServiceAccounts != (fleet.GoogleServiceAccounts{Discovery: discoveryEmail, Command: commandEmail}) ||
		!reflect.DeepEqual(rec.Routers, []fleet.GoogleRouter{{Project: "dormeto-dev", Region: "europe-west1", Controller: "https://dmtr-europe-west1-controller-abcdefghij-ew.a.run.app", Version: "0.40.0"}}) ||
		rec.GoogleAdded == nil || rec.ConnectedAt != "2026-11-09T10:00:00Z" || rec.ConnectedBy != "me@example.com" || rec.CLI != "0.14.0" {
		t.Fatalf("record %+v", rec)
	}
	for _, api := range access.Load().Manager.APIs {
		if !slices.Contains(s.Enabled, api) {
			t.Errorf("%s off", api)
		}
	}
}

// A second run finds everything as planned: "up to date", no change in Google, no record written.
func TestConnectAgainIsUpToDate(t *testing.T) {
	s := connectServer(t)
	m := newFakeManager()
	if code, out := runConnect(t, s, connectRequest(m)); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	before := len(s.Mutations())
	code, out := runConnect(t, s, connectRequest(m))
	if code != 0 || !strings.Contains(out, "The connection of manager dev to Google Cloud through project dormeto-dev is up to date: 1 router, 1 scope.\n") ||
		strings.Contains(out, "Plan for") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if after := s.Mutations(); len(after) != before || len(m.puts) != 1 {
		t.Fatalf("changes: %+v, puts %d", after[before:], len(m.puts))
	}
}

// A router installed since: its grant on its controller (its project's grants are there) and the record, nothing else.
func TestConnectAgainPlansANewRoutersGrantsOnly(t *testing.T) {
	s := connectServer(t)
	m := newFakeManager()
	if code, out := runConnect(t, s, connectRequest(m)); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	routerAt(s, "europe-west2")
	code, out := runConnect(t, s, connectRequest(m))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var plan []string
	for _, l := range lines(out) {
		if strings.HasPrefix(l, "  + ") || strings.HasPrefix(l, "  ~ ") || strings.HasPrefix(l, "  ↺ ") {
			plan = append(plan, l)
		}
	}
	want := []string{
		"  + grant roles/run.invoker to serviceAccount:" + commandEmail + " on controller dmtr-europe-west2-controller of project dormeto-dev",
		"  ~ record of the connection in manager dev: + router dormeto-dev/europe-west2",
	}
	if !slices.Equal(plan, want) {
		t.Fatalf("plan %q\n%s", plan, out)
	}
	if got := m.rec.Routers; len(got) != 2 || got[1].Region != "europe-west2" || got[1].Version != "0.40.0" {
		t.Fatalf("routers %+v", got)
	}
}

// A permission missing at the host: exit 1 with it named, nothing changed in Google or in the manager.
func TestConnectWithAPermissionMissingChangesNothing(t *testing.T) {
	s := connectServer(t)
	s.HoldsAll = false
	s.Held = map[string]bool{}
	for _, p := range access.Needs("connect") {
		s.Held[p] = p != "iam.workloadIdentityPools.create"
	}
	m := newFakeManager()
	code, out := runConnect(t, s, connectRequest(m))
	if code != 1 || !strings.Contains(out, "  ✗ connect in project dormeto-dev: missing iam.workloadIdentityPools.create\nNothing was changed.\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if mut := s.Mutations(); len(mut) != 0 || len(m.puts) != 0 {
		t.Fatalf("mutations %+v puts %d", mut, len(m.puts))
	}
}

// --dry-run prints the plan and writes nothing; under --json, stdout holds {"dryRun": true, "plan": [...]} and the words go
// to stderr.
func TestConnectDryRunWritesNothing(t *testing.T) {
	s := connectServer(t)
	m := newFakeManager()
	r := connectRequest(m)
	r.DryRun, r.Yes, r.JSON = true, false, true
	d, out, errs := connectDeps(s)
	if code := Connect(context.Background(), d, r); code != 0 {
		t.Fatalf("exit %d:\n%s%s", code, out, errs)
	}
	if mut := s.Mutations(); len(mut) != 0 || len(m.puts) != 0 {
		t.Fatalf("mutations %+v puts %d", mut, len(m.puts))
	}
	var got struct {
		DryRun bool     `json:"dryRun"`
		Plan   []string `json:"plan"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !got.DryRun || !slices.Contains(got.Plan, "+ workload identity pool dmt-dev") {
		t.Fatalf("%v %s", err, out)
	}
	if !strings.Contains(errs.String(), "  + workload identity pool dmt-dev\n") {
		t.Fatalf("stderr %s", errs)
	}
}

// Under --json, stdout holds the manager's record, {"connection": …}, alone.
func TestConnectJSONPrintsTheRecord(t *testing.T) {
	s := connectServer(t)
	m := newFakeManager()
	r := connectRequest(m)
	r.JSON = true
	d, out, errs := connectDeps(s)
	if code := Connect(context.Background(), d, r); code != 0 {
		t.Fatalf("exit %d:\n%s%s", code, out, errs)
	}
	var got struct {
		Connection fleet.GoogleConnection `json:"connection"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Connection.Pool != "dmt-dev" || len(got.Connection.Routers) != 1 {
		t.Fatalf("%v %s", err, out)
	}
	if !strings.Contains(errs.String(), "Connected manager dev") {
		t.Fatalf("stderr %s", errs)
	}
}

// A manager older than 0.24.0 has no connection's record: refused before any Google call; a dev build of 0.24.0 counts.
func TestConnectRefusesAManagerBefore0240(t *testing.T) {
	s := connectServer(t)
	m := newFakeManager()
	m.info.Version = "0.23.4"
	code, out := runConnect(t, s, connectRequest(m))
	if code != 1 || !strings.Contains(out, "manager 0.23.4 can't connect to Google Cloud: it needs 0.24.0 or later (dormeto aws manager update)\nNothing was changed.\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if calls := s.Calls(); len(calls) != 0 {
		t.Fatalf("calls %+v", calls)
	}
	m.info.Version = "0.24.0-dev.3"
	r := connectRequest(m)
	r.DryRun = true
	if code, out := runConnect(t, s, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// A bad --project or --scope exits 2 before any call.
func TestConnectRefusesABadProjectOrScope(t *testing.T) {
	s := connectServer(t)
	for _, r := range []ConnectRequest{{Project: "Dormeto"}, {Project: "dormeto-dev", Scopes: []string{"folder/12"}}, {Project: "dormeto-dev", Scopes: []string{"projects/X"}}} {
		r.Manager = newFakeManager()
		if code, out := runConnect(t, s, r); code != 2 || !strings.Contains(out, "--") {
			t.Errorf("%+v: exit %d:\n%s", r, code, out)
		}
	}
	if calls := s.Calls(); len(calls) != 0 {
		t.Fatalf("calls %+v", calls)
	}
}

// A manager connected through another host project: refused, with the disconnect to run first.
func TestConnectRefusesAnotherHostProject(t *testing.T) {
	s := connectServer(t)
	m := newFakeManager()
	m.rec = &fleet.GoogleConnection{Project: "other-host", ProjectNumber: "1", Pool: "dmt-dev", Provider: "aws"}
	code, out := runConnect(t, s, connectRequest(m))
	if code != 1 || !strings.Contains(out, "manager dev is connected to Google Cloud through project other-host: disconnect it first (dormeto gcp disconnect --project other-host --profile kloudy --env dev)") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if mut := s.Mutations(); len(mut) != 0 {
		t.Fatalf("mutations %+v", mut)
	}
}

// disconnect removes every binding of the two accounts at the recorded scopes and router projects, the two custom roles,
// the accounts, the provider and the pool, and clears the record; the APIs stay on and are listed, with Google's own
// bindings.
func TestDisconnectRemovesEveryBindingOfTheTwoAccountsAndLeavesTheAPIs(t *testing.T) {
	s := connectServer(t)
	m := newFakeManager()
	if code, out := runConnect(t, s, connectRequest(m)); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// A grant of the command account's not made by connect, and someone else's on the controller: the former goes, the
	// latter stays.
	s.Lock()
	s.Policy["bindings"] = append(s.Policy["bindings"].([]any), map[string]any{"role": "roles/viewer", "members": []any{"serviceAccount:" + commandEmail, "user:ann@example.com"}})
	s.Unlock()
	code, out := runDisconnect(t, s, connectRequest(m))
	t.Logf("\n%s", out)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, w := range []string{
		"Plan for disconnecting manager dev from Google Cloud (project dormeto-dev):",
		"  - grant roles/run.invoker to serviceAccount:" + commandEmail + " on controller dmtr-europe-west1-controller of project dormeto-dev",
		"  - grant roles/resourcemanager.tagUser to serviceAccount:" + commandEmail + " on tag value dormeto-dev/dormeto/managed",
		"  - grant projects/dormeto-dev/roles/dormetoCommand to serviceAccount:" + commandEmail + " on project dormeto-dev",
		"  - grant roles/viewer to serviceAccount:" + commandEmail + " on project dormeto-dev",
		"  - grant projects/dormeto-dev/roles/dormetoDiscovery to serviceAccount:" + discoveryEmail + " on project dormeto-dev",
		"  - role dormetoDiscovery in project dormeto-dev",
		"  - role dormetoCommand in project dormeto-dev",
		"  - service account " + discoveryEmail,
		"  - grant roles/iam.workloadIdentityUser to " + principal("dmt-dev-manager-discovery") + " on service account " + discoveryEmail + " (goes with it)",
		"  - service account " + commandEmail,
		"  - provider aws of pool dmt-dev",
		"  - workload identity pool dmt-dev",
		"  - record of the connection in manager dev (its 1 router unregistered)",
		"  ✓ verified: no binding of " + discoveryEmail + " or " + commandEmail + " is left at the scopes and router projects",
		"Left in place:",
		"  API iam.googleapis.com",
		"  API iamcredentials.googleapis.com",
		"  API sts.googleapis.com",
		"  API cloudresourcemanager.googleapis.com",
		"  API cloudbilling.googleapis.com",
		"Disconnected manager dev from Google Cloud (project dormeto-dev).",
	} {
		if !slices.Contains(lines(out), w) {
			t.Errorf("no line %q", w)
		}
	}
	for _, p := range []map[string]any{s.Policy, s.Policies["projects/dormeto-dev/locations/europe-west1/services/dmtr-europe-west1-controller"], s.Policies["tagValues/9"]} {
		if got := append(rolesOf(p, "serviceAccount:"+discoveryEmail), rolesOf(p, "serviceAccount:"+commandEmail)...); len(got) != 0 {
			t.Errorf("left: %v", got)
		}
	}
	if got := rolesOf(s.Policy, "user:ann@example.com"); !slices.Equal(got, []string{"roles/viewer"}) {
		t.Errorf("someone else's grant: %v", got)
	}
	if !s.Roles["dormetoDiscovery"].Deleted || !s.Roles["dormetoCommand"].Deleted || len(s.ServiceAccounts) != 0 ||
		s.Pools["dmt-dev"].State != "DELETED" || s.Providers["dmt-dev/aws"].State != "DELETED" || m.deletes != 1 || m.rec != nil {
		t.Fatalf("roles %+v accounts %v pool %+v provider %+v deletes %d", s.Roles, s.ServiceAccounts, s.Pools["dmt-dev"], s.Providers["dmt-dev/aws"], m.deletes)
	}
	for _, api := range access.Load().Manager.APIs {
		if !slices.Contains(s.Enabled, api) {
			t.Errorf("%s turned off", api)
		}
	}
}

// Without a record there is nothing to disconnect: exit 1, nothing changed.
func TestDisconnectWithoutARecordChangesNothing(t *testing.T) {
	s := connectServer(t)
	m := newFakeManager()
	code, out := runDisconnect(t, s, connectRequest(m))
	if code != 1 || !strings.Contains(out, "manager dev has no Google Cloud connection: nothing to disconnect\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if mut := s.Mutations(); len(mut) != 0 || m.deletes != 0 {
		t.Fatalf("mutations %+v", mut)
	}
}

// A connect after a disconnect undeletes what Google keeps: the pool and its provider (30 days), the roles (7), as the
// install's roles step does for its roles.
func TestConnectAfterADisconnectUndeletesThePoolTheProviderAndTheRoles(t *testing.T) {
	s := connectServer(t)
	m := newFakeManager()
	if code, out := runConnect(t, s, connectRequest(m)); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if code, out := runDisconnect(t, s, connectRequest(m)); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	creates := len(s.CallsTo("/workloadIdentityPools")) + len(s.CallsTo("/providers")) + len(s.CallsTo("/roles"))
	code, out := runConnect(t, s, connectRequest(m))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, w := range []string{
		"  ↺ workload identity pool dmt-dev: undelete",
		"  ↺ provider aws of pool dmt-dev: undelete",
		"  ↺ role dormetoDiscovery in project dormeto-dev: undelete, then update",
		"  ↺ role dormetoCommand in project dormeto-dev: undelete, then update",
	} {
		if !slices.Contains(lines(out), w) {
			t.Errorf("no line %q in\n%s", w, out)
		}
	}
	if s.Pools["dmt-dev"].State != "ACTIVE" || s.Providers["dmt-dev/aws"].State != "ACTIVE" || s.Roles["dormetoDiscovery"].Deleted {
		t.Fatalf("pool %+v provider %+v", s.Pools["dmt-dev"], s.Providers["dmt-dev/aws"])
	}
	if again := len(s.CallsTo("/workloadIdentityPools")) + len(s.CallsTo("/providers")) + len(s.CallsTo("/roles")); again != creates {
		t.Fatalf("%d creations: they were undeleted", again-creates)
	}
}

// A role ID Google blocks without letting it be undeleted (deleted more than 7 days ago) stops the connect with the date it
// frees: 44 days after the manager's last disconnect of the host.
func TestConnectStopsAtABlockedRoleIDWithTheDateItFrees(t *testing.T) {
	s := connectServer(t)
	m := newFakeManager()
	m.disconnected = time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	s.Refuse(gcptest.Refusal{Method: http.MethodPost, Host: "iam.googleapis.com", Path: "/v1/projects/dormeto-dev/roles", Code: 400,
		Status: "FAILED_PRECONDITION", Message: "You can't create a role with role_id (dormetoDiscovery) where there is an existing role with that role_id in a deleted state."})
	code, out := runConnect(t, s, connectRequest(m))
	if code != 1 || !strings.Contains(out, "  ✗ roles: Google blocks the ID of role dormetoDiscovery in project dormeto-dev: it was deleted more than 7 days ago and can't be undeleted; Google frees it by 2026-12-13 (44 days after the manager's disconnect of 2026-10-30)\n") ||
		!strings.Contains(out, "Done: apis. Not done: roles, federation, serviceAccounts, impersonation, projectBindings, controller, tag. Run the same command again to continue.\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if len(m.puts) != 0 {
		t.Fatal("record written")
	}
}

// A folder scope: dormetoDiscovery is defined in the folder's organization (found by the ancestry of a project under it)
// with the organization's permissions too, and granted on the folder; the folder is tested for its bindings only, the
// organization for the roles.
func TestConnectAtAFolderDefinesTheRoleInItsOrganization(t *testing.T) {
	s := connectServer(t)
	s.Parent, s.Ancestors = "folders/7", []string{"folders/7", "organizations/42"}
	s.Scopes = map[string]bool{"folders/7": true, "organizations/42": true}
	m := newFakeManager()
	r := connectRequest(m)
	r.Scopes = []string{"folders/7"}
	code, out := runConnect(t, s, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, w := range []string{
		"Checked: me@example.com holds every permission dormeto gcp connect needs in project dormeto-dev (host), folder 7, organization 42.",
		`  + role dormetoDiscovery "Dormeto manager discovery" (35 permissions) in organization 42`,
		"  + grant organizations/42/roles/dormetoDiscovery to serviceAccount:" + discoveryEmail + " on folder 7",
		"  + record of the connection in manager dev: 1 router (dormeto-dev/europe-west1), 1 scope (folders/7)",
	} {
		if !slices.Contains(lines(out), w) {
			t.Errorf("no line %q in\n%s", w, out)
		}
	}
	if r := s.OrgRoles["42/dormetoDiscovery"]; r == nil || len(r.Permissions) != 35 || !slices.Contains(r.Permissions, "resourcemanager.folders.list") {
		t.Fatalf("organization role %+v", r)
	}
	if got := rolesOf(s.Policies["folders/7"], "serviceAccount:"+discoveryEmail); !slices.Equal(got, []string{"organizations/42/roles/dormetoDiscovery"}) {
		t.Fatalf("folder grant %v", got)
	}
	tested := map[string]string{}
	for _, c := range s.CallsTo(":testIamPermissions") {
		tested[c.Path] += c.Body
	}
	if f := tested["/v3/folders/7:testIamPermissions"]; !strings.Contains(f, "resourcemanager.folders.setIamPolicy") || strings.Contains(f, "organizations.") || strings.Contains(f, "iam.roles.") {
		t.Errorf("folder tested for %s", f)
	}
	if o := tested["/v3/organizations/42:testIamPermissions"]; !strings.Contains(o, "iam.roles.create") || strings.Contains(o, "setIamPolicy") {
		t.Errorf("organization tested for %s", o)
	}
	if p := tested["/v3/projects/dormeto-dev:testIamPermissions"]; strings.Contains(p, "folders.") || strings.Contains(p, "organizations.") {
		t.Errorf("project tested for %s", p)
	}
	if rec := m.puts[0]; !slices.Equal(rec.Scopes, []string{"folders/7"}) || len(rec.Routers) != 1 {
		t.Fatalf("record %+v", rec)
	}
}

// A router of the record no longer found (uninstalled since): the record drops it, and when its project holds no router any
// more the command account's grants there go (the project's and the tag value's); the role dormetoCommand stays, granting
// nothing (connect's permissions don't delete roles).
func TestConnectAgainRemovesTheGrantsOfAProjectWhoseRouterIsGone(t *testing.T) {
	s := connectServer(t)
	m := newFakeManager()
	if code, out := runConnect(t, s, connectRequest(m)); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	s.Lock()
	delete(s.RunServices, "europe-west1/dmtr-europe-west1-controller")
	s.Unlock()
	code, out := runConnect(t, s, connectRequest(m))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, w := range []string{
		"  - grant projects/dormeto-dev/roles/dormetoCommand to serviceAccount:" + commandEmail + " on project dormeto-dev (no router left)",
		"  - grant roles/resourcemanager.tagUser to serviceAccount:" + commandEmail + " on tag value dormeto-dev/dormeto/managed (no router left)",
		"  = role dormetoCommand in project dormeto-dev: left in place, granted to no one",
		"  ~ record of the connection in manager dev: - router dormeto-dev/europe-west1 (not found at the scopes)",
	} {
		if !slices.Contains(lines(out), w) {
			t.Errorf("no line %q in\n%s", w, out)
		}
	}
	if got := rolesOf(s.Policy, "serviceAccount:"+commandEmail); len(got) != 0 {
		t.Errorf("command on the project: %v", got)
	}
	if got := rolesOf(s.Policies["tagValues/9"], "serviceAccount:"+commandEmail); len(got) != 0 {
		t.Errorf("command on the tag value: %v", got)
	}
	if got := rolesOf(s.Policy, "serviceAccount:"+discoveryEmail); !slices.Equal(got, []string{"projects/dormeto-dev/roles/dormetoDiscovery"}) {
		t.Errorf("discovery on the project: %v", got)
	}
	if len(m.rec.Routers) != 0 || s.Roles["dormetoCommand"].Deleted {
		t.Fatalf("routers %+v role %+v", m.rec.Routers, s.Roles["dormetoCommand"])
	}
}
