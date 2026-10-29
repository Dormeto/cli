package gcponboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/gcp/access"
	"github.com/Dormeto/cli/internal/gcp/gcptest"
	"github.com/Dormeto/cli/internal/install"
)

const (
	routerSA     = "dmtr-europe-west1@dormeto-dev.iam.gserviceaccount.com"
	routerMember = "serviceAccount:" + routerSA
	runAgent     = "serviceAccount:service-1034397096264@serverless-robot-prod.iam.gserviceaccount.com"
	recordDB     = "dmtr-europe-west1"
)

var installNow = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

// installRig is a fake Google with the project's owner, and the dependencies of an install against it.
type installRig struct {
	s     *gcptest.Server
	d     Deps
	out   *bytes.Buffer
	slept *[]time.Duration
	cache string
}

func newInstallRig(t *testing.T) *installRig {
	t.Helper()
	s := gcptest.New(t)
	s.Policy = map[string]any{"version": 1, "etag": "BwAAAA==", "bindings": []any{
		map[string]any{"role": "roles/owner", "members": []any{"user:owner@example.com"}}}}
	var out bytes.Buffer
	var slept []time.Duration
	cache := t.TempDir()
	d := Deps{
		Google:   &gcp.Client{HTTP: s.HTTP, Tokens: gcptest.Tokens{AccountName: "me@example.com"}},
		Prompter: install.NewPrompter(strings.NewReader(""), &out, false),
		Out:      &out, Err: &out,
		Now:     func() time.Time { return installNow },
		Sleep:   func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil },
		Cache:   cache,
		Version: "0.8.0",
	}
	return &installRig{s: s, d: d, out: &out, slept: &slept, cache: cache}
}

var europe = Request{Project: "dormeto-dev", Region: "europe-west1", Yes: true}

func (g *installRig) run(t *testing.T, r Request) (int, string) {
	t.Helper()
	g.out.Reset()
	*g.slept = nil
	code := Install(context.Background(), g.d, r)
	return code, g.out.String()
}

func (g *installRig) record(t *testing.T) Record {
	t.Helper()
	data := g.s.Field(recordDB, "install/record", "data")
	if data == "" {
		t.Fatal("no record")
	}
	var r Record
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func (g *installRig) projectPolicy(t *testing.T) Policy {
	t.Helper()
	g.s.Lock()
	b, _ := json.Marshal(g.s.Policy)
	g.s.Unlock()
	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func (g *installRig) policyOf(t *testing.T, name string) Policy {
	t.Helper()
	g.s.Lock()
	b, _ := json.Marshal(g.s.Policies[name])
	g.s.Unlock()
	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func (g *installRig) cacheFile() string {
	return filepath.Join(g.cache, "dormeto", "gcp", "dormeto-dev-europe-west1-before.json")
}

func has(p Policy, g Grant) bool {
	for _, x := range grantsOf(&p) {
		if grantKey(x) == grantKey(g) {
			return true
		}
	}
	return false
}

func callsOf(calls []gcptest.Call, method string) []gcptest.Call {
	var out []gcptest.Call
	for _, c := range calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func contains(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

const freshPlan = `Plan for project dormeto-dev, router dmtr-europe-west1:
  + API compute.googleapis.com
  + API run.googleapis.com
  + API firestore.googleapis.com
  + API cloudtasks.googleapis.com
  + API cloudscheduler.googleapis.com
  + API monitoring.googleapis.com
  + API artifactregistry.googleapis.com
  + API iam.googleapis.com
  + API cloudresourcemanager.googleapis.com
  + API logging.googleapis.com
  + role dormetoRouter "Dormeto router" (27 permissions)
  + role dormetoRouterBackends "Dormeto router backends" (6 permissions)
  + service account dmtr-europe-west1@dormeto-dev.iam.gserviceaccount.com
  + tag dormeto/managed
  + database dmtr-europe-west1 (Firestore Native, europe-west1)
  + grant projects/dormeto-dev/roles/dormetoRouter to serviceAccount:dmtr-europe-west1@dormeto-dev.iam.gserviceaccount.com on the project
  + grant projects/dormeto-dev/roles/dormetoRouterBackends to serviceAccount:dmtr-europe-west1@dormeto-dev.iam.gserviceaccount.com on the project, if "Dormeto-managed resources": resource.matchTag('dormeto-dev/dormeto', 'managed')
  + grant roles/datastore.user to serviceAccount:dmtr-europe-west1@dormeto-dev.iam.gserviceaccount.com on the project, if "The router's database": resource.name == "projects/dormeto-dev/databases/dmtr-europe-west1"
  + queue dmtr-europe-west1 (3 attempts, backoff 5 to 60 s)
  + grant roles/cloudtasks.enqueuer to serviceAccount:dmtr-europe-west1@dormeto-dev.iam.gserviceaccount.com on queue dmtr-europe-west1
  + grant roles/iam.serviceAccountUser to serviceAccount:dmtr-europe-west1@dormeto-dev.iam.gserviceaccount.com on service account dmtr-europe-west1@dormeto-dev.iam.gserviceaccount.com
  + record install/record in database dmtr-europe-west1
Turning APIs on can make Google add bindings of its own: they are shown after that step.
`

func TestInstallDryRunPrintsThePlanOfAFreshProject(t *testing.T) {
	g := newInstallRig(t)
	code, out := g.run(t, Request{Project: "dormeto-dev", Region: "europe-west1", DryRun: true})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, freshPlan) {
		t.Fatalf("plan:\n%s\nwant:\n%s", out, freshPlan)
	}
	if strings.Contains(out, "✓ apis") || strings.Contains(out, "Apply these changes") {
		t.Fatal(out)
	}
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
	if _, err := os.Stat(g.cacheFile()); !os.IsNotExist(err) {
		t.Fatalf("cache file: %v", err)
	}
}

func TestInstallAppliesTheAccessOfAFreshProject(t *testing.T) {
	g := newInstallRig(t)
	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, freshPlan,
		"  ✓ apis: turned on compute.googleapis.com, run.googleapis.com, firestore.googleapis.com, cloudtasks.googleapis.com, cloudscheduler.googleapis.com, monitoring.googleapis.com, artifactregistry.googleapis.com, iam.googleapis.com, cloudresourcemanager.googleapis.com, logging.googleapis.com\n"+
			"      IAM + roles/run.serviceAgent to "+runAgent+" on the project (Google, after apis)\n"+
			"  ✓ roles: created dormetoRouter, created dormetoRouterBackends\n"+
			"  ✓ serviceAccount: created "+routerSA+"\n"+
			"  ✓ tag: created dormeto/managed\n"+
			"  ✓ database: created dmtr-europe-west1 (Firestore Native, europe-west1), wrote install/record\n"+
			"  ✓ projectBindings: granted 3 roles on the project\n",
		"      IAM + projects/dormeto-dev/roles/dormetoRouter to "+routerMember+" on the project (Dormeto)\n",
		"      IAM + roles/datastore.user to "+routerMember+` on the project, if "The router's database": resource.name == "projects/dormeto-dev/databases/dmtr-europe-west1" (Dormeto)`+"\n",
		"  ✓ queue: created dmtr-europe-west1 (3 attempts, backoff 5 to 60 s), granted roles/cloudtasks.enqueuer on it\n"+
			"      IAM + roles/cloudtasks.enqueuer to "+routerMember+" on queue dmtr-europe-west1 (Dormeto)\n"+
			"  ✓ serviceAccountBinding: granted roles/iam.serviceAccountUser on "+routerSA+"\n"+
			"      IAM + roles/iam.serviceAccountUser to "+routerMember+" on service account "+routerSA+" (Dormeto)\n"+
			"  ✓ record: wrote install/record in database dmtr-europe-west1\n"+
			"  ✓ verified: the planned grants, the roles' permissions, the service account, the database and the queue\n"+
			"Installed the access of router dmtr-europe-west1 in project dormeto-dev.\n")

	s := g.s
	for _, api := range access.Load().APIs {
		if !slices.Contains(s.Enabled, api) {
			t.Errorf("%s not enabled", api)
		}
	}
	m := access.Load()
	for id, want := range m.Roles {
		role := s.Roles[id]
		if role == nil || role.Title != want.Title || role.Description != "Dormeto 0.8.0: "+want.Description || role.Stage != "GA" ||
			!slices.Equal(role.Permissions, want.Permissions) {
			t.Errorf("role %s: %+v", id, role)
		}
	}
	a := s.ServiceAccounts[routerSA]
	if a == nil || a.DisplayName != "Dormeto router europe-west1" || a.Description != "Dormeto router dmtr-europe-west1, created by dormeto gcp install" {
		t.Errorf("service account %+v", a)
	}
	var key, value *gcptest.Tag
	for _, k := range s.TagKeys {
		key = k
	}
	for _, v := range s.TagValues {
		value = v
	}
	if key == nil || key.NamespacedName != "dormeto-dev/dormeto" || key.Parent != "projects/1034397096264" ||
		key.Description != "Dormeto: resources a Dormeto router may start, stop, suspend and resume" {
		t.Errorf("tag key %+v", key)
	}
	if value == nil || value.Parent != key.Name || value.ShortName != "managed" || value.Description != "Managed by Dormeto" {
		t.Errorf("tag value %+v", value)
	}
	db := s.Databases[recordDB]
	want := gcptest.Database{Type: "FIRESTORE_NATIVE", LocationID: "europe-west1", DeleteProtectionState: "DELETE_PROTECTION_DISABLED",
		PointInTimeRecoveryEnablement: "POINT_IN_TIME_RECOVERY_DISABLED"}
	if db == nil || *db != want {
		t.Errorf("database %+v", db)
	}
	q := s.Queues["europe-west1/dmtr-europe-west1"]
	if q == nil || *q != (gcptest.Queue{MaxAttempts: 3, MinBackoff: "5s", MaxBackoff: "60s", MaxDispatchesPerSecond: 50, MaxConcurrentDispatches: 100}) {
		t.Errorf("queue %+v", q)
	}

	p := g.projectPolicy(t)
	backends := Grant{Role: "projects/dormeto-dev/roles/dormetoRouterBackends", Member: routerMember,
		Condition: &Condition{Title: "Dormeto-managed resources", Expression: "resource.matchTag('dormeto-dev/dormeto', 'managed')"}}
	datastore := Grant{Role: "roles/datastore.user", Member: routerMember,
		Condition: &Condition{Title: "The router's database", Expression: `resource.name == "projects/dormeto-dev/databases/dmtr-europe-west1"`}}
	for _, want := range []Grant{
		{Role: "roles/owner", Member: "user:owner@example.com"},
		{Role: "roles/run.serviceAgent", Member: runAgent},
		{Role: "projects/dormeto-dev/roles/dormetoRouter", Member: routerMember},
		backends, datastore,
	} {
		if !has(p, want) {
			t.Errorf("project policy lacks %+v: %+v", want, p)
		}
	}
	if p.Version != 3 {
		t.Errorf("version %d", p.Version)
	}
	if qp := g.policyOf(t, "projects/dormeto-dev/locations/europe-west1/queues/dmtr-europe-west1"); !has(qp, Grant{Role: "roles/cloudtasks.enqueuer", Member: routerMember}) {
		t.Errorf("queue policy %+v", qp)
	}
	if sp := g.policyOf(t, "projects/dormeto-dev/serviceAccounts/"+routerSA); !has(sp, Grant{Role: "roles/iam.serviceAccountUser", Member: routerMember}) {
		t.Errorf("service account policy %+v", sp)
	}

	rec := g.record(t)
	if rec.CLI != "0.8.0" || rec.Image != "" || rec.RouterVersion != "" || rec.Region != "europe-west1" || rec.ServiceAccount != routerSA ||
		rec.InstalledAt != "2026-10-29T10:00:00Z" || rec.UpdatedAt != "2026-10-29T10:00:00Z" || rec.By != "me@example.com" {
		t.Errorf("record %+v", rec)
	}
	if len(rec.Google) != 1 || rec.Google[0] != (GoogleGrant{Role: "roles/run.serviceAgent", Member: runAgent, Step: "apis"}) {
		t.Errorf("google %+v", rec.Google)
	}
	if len(rec.Before) != 1 || rec.Before[0].Role != "roles/owner" || !slices.Equal(rec.Before[0].Members, []string{"user:owner@example.com"}) {
		t.Errorf("before %+v", rec.Before)
	}
	kinds := map[string]int{}
	for _, r := range rec.Resources {
		kinds[r.Kind]++
	}
	if kinds["api"] != 10 || kinds["role"] != 2 || kinds["serviceAccount"] != 1 || kinds["tagKey"] != 1 || kinds["tagValue"] != 1 ||
		kinds["database"] != 1 || kinds["queue"] != 1 || len(rec.Resources) != 17 {
		t.Errorf("resources %+v", rec.Resources)
	}
	for _, want := range []Resource{
		{Kind: "role", Name: "projects/dormeto-dev/roles/dormetoRouter"},
		{Kind: "serviceAccount", Name: "projects/dormeto-dev/serviceAccounts/" + routerSA},
		{Kind: "tagKey", Name: key.Name},
		{Kind: "tagValue", Name: value.Name},
		{Kind: "database", Name: "projects/dormeto-dev/databases/dmtr-europe-west1"},
		{Kind: "queue", Name: "projects/dormeto-dev/locations/europe-west1/queues/dmtr-europe-west1"},
	} {
		if !slices.Contains(rec.Resources, want) {
			t.Errorf("record lacks %+v", want)
		}
	}
	grants := map[string]RecordGrant{}
	for _, gr := range rec.Grants {
		grants[gr.Role+" "+gr.On] = gr
	}
	if len(rec.Grants) != 5 || grants["roles/cloudtasks.enqueuer queue"].Member != routerMember ||
		grants["roles/iam.serviceAccountUser serviceAccount"].Member != routerMember ||
		grants["projects/dormeto-dev/roles/dormetoRouter project"].Condition != nil ||
		grants["roles/datastore.user project"].Condition == nil ||
		grants["roles/datastore.user project"].Condition.Expression != datastore.Condition.Expression {
		t.Errorf("grants %+v", rec.Grants)
	}
	if _, err := os.Stat(g.cacheFile()); !os.IsNotExist(err) {
		t.Errorf("cache file left: %v", err)
	}
}

func TestInstallChangesInTheStepsOrder(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var got []string
	for _, c := range g.s.Mutations() {
		path := c.Path
		for _, cut := range []string{"/v1/projects/dormeto-dev/", "/v2/projects/dormeto-dev/locations/europe-west1/", "/v3/", "/v1/projects/1034397096264/"} {
			path = strings.TrimPrefix(path, cut)
		}
		got = append(got, c.Method+" "+path)
	}
	want := []string{
		"POST services:batchEnable",
		"POST roles", "POST roles",
		"POST serviceAccounts",
		"POST tagKeys", "POST tagValues",
		"POST databases",
		"PATCH databases/dmtr-europe-west1/documents/install/record",
		"POST projects/dormeto-dev:setIamPolicy",
		"POST queues", "POST queues/dmtr-europe-west1:setIamPolicy",
		"POST serviceAccounts/" + routerSA + ":setIamPolicy",
		"PATCH databases/dmtr-europe-west1/documents/install/record",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("mutations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	enable := g.s.CallsTo("services:batchEnable")[0]
	var body struct{ ServiceIDs []string }
	if json.Unmarshal([]byte(enable.Body), &body) != nil || !slices.Equal(body.ServiceIDs, access.Load().APIs) ||
		enable.URL != "https://serviceusage.googleapis.com/v1/projects/1034397096264/services:batchEnable" {
		t.Fatalf("%+v", enable)
	}
}

func TestInstallAgainIsUpToDate(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	before := len(g.s.Mutations())
	code, out := g.run(t, europe)
	if code != 0 || !strings.Contains(out, "The access of router dmtr-europe-west1 in project dormeto-dev is up to date.\n") ||
		strings.Contains(out, "Plan for") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if after := len(g.s.Mutations()); after != before {
		t.Fatalf("%d mutating calls", after-before)
	}
}

func TestInstallUndeletesThenUpdatesADeletedRole(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.s.Lock()
	g.s.Roles["dormetoRouter"].Deleted = true
	g.s.Unlock()
	mark := len(g.s.Calls())
	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Plan for project dormeto-dev, router dmtr-europe-west1:\n"+
		"  ↺ role dormetoRouter: undelete, then update\n"+
		"  ~ record install/record in database dmtr-europe-west1\n"+
		"  ✓ roles: undeleted and updated dormetoRouter\n", "  ✓ verified:")
	var got []string
	for _, c := range g.s.Calls()[mark:] {
		if c.Mutating() {
			got = append(got, c.Method+" "+c.URL)
		}
	}
	want := []string{
		"POST https://iam.googleapis.com/v1/projects/dormeto-dev/roles/dormetoRouter:undelete",
		"PATCH https://iam.googleapis.com/v1/projects/dormeto-dev/roles/dormetoRouter?updateMask=title,description,includedPermissions",
		"PATCH https://firestore.googleapis.com/v1/projects/dormeto-dev/databases/dmtr-europe-west1/documents/install/record",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
	if g.s.Roles["dormetoRouter"].Deleted {
		t.Fatal("still deleted")
	}
}

func TestInstallPatchesARoleThatDiffers(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.s.Lock()
	role := g.s.Roles["dormetoRouter"]
	role.Permissions = slices.DeleteFunc(slices.Clone(role.Permissions), func(p string) bool { return p == "compute.backendServices.create" })
	role.Permissions = append(role.Permissions, "compute.example.permission")
	g.s.Unlock()
	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ~ role dormetoRouter: + compute.backendServices.create, - compute.example.permission\n",
		"  ✓ roles: updated dormetoRouter\n", "  ✓ verified:")
	patches := callsOf(g.s.CallsTo("/roles/dormetoRouter"), "PATCH")
	if len(patches) != 1 || !strings.HasSuffix(patches[0].URL, "?updateMask=title,description,includedPermissions") {
		t.Fatalf("%+v", patches)
	}
	if !slices.Equal(g.s.Roles["dormetoRouter"].Permissions, access.Load().Roles["dormetoRouter"].Permissions) {
		t.Fatal(g.s.Roles["dormetoRouter"].Permissions)
	}
}

func TestInstallUpdatesADescriptionOfAnotherVersion(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.d.Version = "0.8.1"
	code, out := g.run(t, Request{Project: "dormeto-dev", Region: "europe-west1", DryRun: true})
	want := access.Load().Roles["dormetoRouterBackends"].Description
	if code != 0 || !strings.Contains(out, `  ~ role dormetoRouterBackends: description "Dormeto 0.8.1: `+want+`"`+"\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestInstallRetriesABindingOfTheNewServiceAccount(t *testing.T) {
	g := newInstallRig(t)
	g.s.AccountLag = 1
	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !slices.Equal(*g.slept, []time.Duration{5 * time.Second}) {
		t.Fatalf("slept %v", *g.slept)
	}
	if n := len(g.s.CallsTo("projects/dormeto-dev:setIamPolicy")); n != 2 {
		t.Fatalf("%d project writes", n)
	}
}

func TestInstallGivesUpOnAServiceAccountIAMNeverKnows(t *testing.T) {
	g := newInstallRig(t)
	g.s.AccountLag = 100
	code, out := g.run(t, europe)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if len(*g.slept) != 12 {
		t.Fatalf("slept %v", *g.slept)
	}
	contains(t, out, "  ✗ projectBindings: google: 400 INVALID_ARGUMENT: Service account "+routerSA+" does not exist.\n",
		"Done: apis, roles, serviceAccount, tag, database. Not done: projectBindings, queue, serviceAccountBinding, record. Run the same command again to continue.\n")
}

func TestInstallRetriesAnAPIJustTurnedOn(t *testing.T) {
	g := newInstallRig(t)
	g.s.Refuse(gcptest.Refusal{Method: "POST", Host: "iam.googleapis.com", Path: "/v1/projects/dormeto-dev/roles", Times: 2, Code: 403,
		Status: "PERMISSION_DENIED", Message: "Identity and Access Management (IAM) API has not been used in project 1034397096264 before or it is disabled.",
		Reason: "SERVICE_DISABLED"})
	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !slices.Equal(*g.slept, []time.Duration{5 * time.Second, 5 * time.Second}) {
		t.Fatalf("slept %v", *g.slept)
	}
}

func TestInstallWaitsForOperations(t *testing.T) {
	old := operationEvery
	operationEvery = time.Millisecond
	t.Cleanup(func() { operationEvery = old })
	g := newInstallRig(t)
	g.s.OperationPolls = 3
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	polls := 0
	for _, c := range g.s.Calls() {
		if c.Method == "GET" && strings.Contains(c.Path, "/operations/") {
			polls++
		}
	}
	// batchEnable, the tag key and value, the database: 3 polls each.
	if polls != 12 {
		t.Fatalf("%d polls", polls)
	}
}

func TestInstallStopsAtARefusedStepAndContinuesAfter(t *testing.T) {
	g := newInstallRig(t)
	g.s.Refuse(gcptest.Refusal{Method: "POST", Host: "cloudresourcemanager.googleapis.com", Path: "/v3/tagKeys", Times: 1, Code: 403,
		Status: "PERMISSION_DENIED", Message: "Permission 'resourcemanager.tagKeys.create' denied"})
	code, out := g.run(t, europe)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✓ serviceAccount: created "+routerSA+"\n"+
		"  ✗ tag: google: 403 PERMISSION_DENIED: Permission 'resourcemanager.tagKeys.create' denied\n"+
		"Done: apis, roles, serviceAccount. Not done: tag, database, projectBindings, queue, serviceAccountBinding, record. Run the same command again to continue.\n")
	if _, err := os.Stat(g.cacheFile()); err != nil {
		t.Fatalf("the before snapshot waits in the cache: %v", err)
	}

	code, out = g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Plan for project dormeto-dev, router dmtr-europe-west1:\n"+
		"  + tag dormeto/managed\n"+
		"  + database dmtr-europe-west1 (Firestore Native, europe-west1)\n")
	for _, not := range []string{"\n  + API ", "\n  + role ", "\n  + service account "} {
		if strings.Contains(out, not) {
			t.Errorf("%q planned again:\n%s", not, out)
		}
	}
	rec := g.record(t)
	if len(rec.Before) != 1 || rec.Before[0].Role != "roles/owner" {
		t.Errorf("before %+v", rec.Before)
	}
	if len(rec.Google) != 1 || rec.Google[0].Member != runAgent || rec.Google[0].Step != "apis" {
		t.Errorf("google %+v", rec.Google)
	}
	if len(rec.Resources) != 17 {
		t.Errorf("resources %+v", rec.Resources)
	}
	if _, err := os.Stat(g.cacheFile()); !os.IsNotExist(err) {
		t.Errorf("cache file left: %v", err)
	}
}

func TestInstallKeepsTheBeforeOfAnEarlierRun(t *testing.T) {
	g := newInstallRig(t)
	earlier := Record{Before: []RecordBinding{{Role: "roles/viewer", Members: []string{"user:earlier@example.com"}}},
		Google: []GoogleGrant{{Role: "roles/x.serviceAgent", Member: "serviceAccount:x@example.com", Step: "apis"}}, InstalledAt: "2026-10-28T09:00:00Z"}
	b, _ := json.Marshal(earlier)
	os.MkdirAll(filepath.Dir(g.cacheFile()), 0o700)
	if err := os.WriteFile(g.cacheFile(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	rec := g.record(t)
	if len(rec.Before) != 1 || rec.Before[0].Role != "roles/viewer" || rec.InstalledAt != "2026-10-28T09:00:00Z" {
		t.Fatalf("%+v", rec)
	}
	if len(rec.Google) != 2 || rec.Google[0].Role != "roles/x.serviceAgent" || rec.Google[1].Member != runAgent {
		t.Fatalf("%+v", rec.Google)
	}
	if _, err := os.Stat(g.cacheFile()); !os.IsNotExist(err) {
		t.Errorf("cache file left: %v", err)
	}
}

func TestInstallUpdatesTheQueueSettings(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.s.Lock()
	g.s.Queues["europe-west1/dmtr-europe-west1"].MaxAttempts = 100
	g.s.Unlock()
	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ~ queue dmtr-europe-west1: 3 attempts, backoff 5 to 60 s, 50 dispatches a second, 100 at once\n",
		"  ✓ queue: updated dmtr-europe-west1 (3 attempts, backoff 5 to 60 s, 50 dispatches a second, 100 at once)\n")
	patches := callsOf(g.s.CallsTo("/queues/dmtr-europe-west1"), "PATCH")
	if len(patches) != 1 || !strings.HasSuffix(patches[0].URL, "?updateMask=retryConfig,rateLimits") {
		t.Fatalf("%+v", patches)
	}
	if g.s.Queues["europe-west1/dmtr-europe-west1"].MaxAttempts != 3 {
		t.Fatal("not updated")
	}
}

func TestInstallMissingPermissionChangesNothing(t *testing.T) {
	g := newInstallRig(t)
	g.s.HoldsAll = false
	g.s.Held = map[string]bool{}
	for _, p := range access.Needs("install") {
		g.s.Held[p] = p != "iam.roles.create" && p != "resourcemanager.tagKeys.create"
	}
	code, out := g.run(t, europe)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ install: missing iam.roles.create, resourcemanager.tagKeys.create\n", "Nothing was changed.\n")
	if strings.Contains(out, "Plan for") {
		t.Fatal(out)
	}
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
}

func TestInstallWithoutTerminalNeedsYes(t *testing.T) {
	g := newInstallRig(t)
	code, out := g.run(t, Request{Project: "dormeto-dev", Region: "europe-west1"})
	if code != 2 || !strings.Contains(out, freshPlan) || !strings.HasSuffix(out, "--yes: needed\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
}

func TestInstallAsksForConfirmation(t *testing.T) {
	g := newInstallRig(t)
	g.d.Terminal = true
	g.d.Prompter = install.NewPrompter(strings.NewReader("n\n"), g.out, true)
	code, out := g.run(t, Request{Project: "dormeto-dev", Region: "europe-west1"})
	if code != 0 || !strings.Contains(out, "Apply these changes to project dormeto-dev? [y/N]: ") || !strings.HasSuffix(out, "Nothing was changed.\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
	if _, err := os.Stat(g.cacheFile()); !os.IsNotExist(err) {
		t.Fatalf("cache file: %v", err)
	}
	g.d.Prompter = install.NewPrompter(strings.NewReader("y\n"), g.out, true)
	code, out = g.run(t, Request{Project: "dormeto-dev", Region: "europe-west1"})
	if code != 0 || !strings.Contains(out, "Installed the access of router dmtr-europe-west1 in project dormeto-dev.\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestInstallRefusesABadRegion(t *testing.T) {
	g := newInstallRig(t)
	for _, region := range []string{"", "europe", "Europe-west1", "europe-west", "northamerica-northeastsouth12"} {
		code, out := g.run(t, Request{Project: "dormeto-dev", Region: region, Yes: true})
		if code != 2 || !strings.Contains(out, "--region") {
			t.Errorf("%q: exit %d:\n%s", region, code, out)
		}
	}
	if c := g.s.Calls(); len(c) != 0 {
		t.Fatalf("calls: %+v", c)
	}
}

func TestVerifyNamesWhatDiffers(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.s.Lock()
	g.s.Roles["dormetoRouterBackends"].Permissions = []string{"compute.instances.start"}
	delete(g.s.Policies, "projects/dormeto-dev/locations/europe-west1/queues/dmtr-europe-west1")
	g.s.Unlock()
	x := newRun(context.Background(), g.d, europe)
	x.number = "1034397096264"
	err := x.verify()
	if err == nil || !strings.Contains(err.Error(), "role dormetoRouterBackends lacks compute.instances.stop") ||
		!strings.Contains(err.Error(), "queue dmtr-europe-west1 lacks the grant of roles/cloudtasks.enqueuer") {
		t.Fatal(err)
	}
}

func TestInstallInterruptedDuringAStepSaysWhatWasDone(t *testing.T) {
	g := newInstallRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.s.Route("cloudresourcemanager.googleapis.com", "/v3/tagKeys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			cancel()
		}
		gcptest.Fail(w, 403, "PERMISSION_DENIED", "Permission denied on resource (or it may not exist).")
	})
	g.out.Reset()
	code := Install(ctx, g.d, europe)
	out := g.out.String()
	if code != 130 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ tag: ", "Done: apis, roles, serviceAccount. Not done: tag, ")
	if _, err := os.Stat(g.cacheFile()); err != nil {
		t.Fatalf("the done part waits in the cache: %v", err)
	}
}
