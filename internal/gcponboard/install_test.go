package gcponboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
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
	routerSA       = "dmtr-europe-west1@dormeto-dev.iam.gserviceaccount.com"
	routerMember   = "serviceAccount:" + routerSA
	runAgent       = "serviceAccount:service-1034397096264@serverless-robot-prod.iam.gserviceaccount.com"
	firestoreAgent = "serviceAccount:service-1034397096264@gcp-sa-firestore.iam.gserviceaccount.com"
	computeDefault = "serviceAccount:1034397096264-compute@developer.gserviceaccount.com"
	schedulerAgent = "serviceAccount:service-1034397096264@gcp-sa-cloudscheduler.iam.gserviceaccount.com"
	tasksAgent     = "serviceAccount:service-1034397096264@gcp-sa-cloudtasks.iam.gserviceaccount.com"
	registryAgent  = "serviceAccount:service-1034397096264@containerregistry.iam.gserviceaccount.com"
	pubsubAgent    = "serviceAccount:service-1034397096264@gcp-sa-pubsub.iam.gserviceaccount.com"
	rulesSystem    = "serviceAccount:service-1034397096264@firebase-rules.iam.gserviceaccount.com"
	recordDB       = "dmtr-europe-west1"
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
		Google:   &gcp.Client{HTTP: s.HTTP, Tokens: gcptest.Tokens{AccountName: "me@example.com"}, Sleep: noWait},
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
  + role dormetoRouter "Dormeto router" (43 permissions)
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
Turning APIs on and creating the database can make Google add bindings of its own: they are shown after those steps. Expected:
  roles/editor to serviceAccount:1034397096264-compute@developer.gserviceaccount.com on the project, after apis (Compute Engine's default service account, in a project without an organization)
  roles/cloudscheduler.serviceAgent to serviceAccount:service-1034397096264@gcp-sa-cloudscheduler.iam.gserviceaccount.com on the project, after apis (Cloud Scheduler's service agent)
  roles/cloudtasks.serviceAgent to serviceAccount:service-1034397096264@gcp-sa-cloudtasks.iam.gserviceaccount.com on the project, after apis (Cloud Tasks' service agent)
  roles/containerregistry.ServiceAgent to serviceAccount:service-1034397096264@containerregistry.iam.gserviceaccount.com on the project, after apis (Container Registry's service agent)
  roles/pubsub.serviceAgent to serviceAccount:service-1034397096264@gcp-sa-pubsub.iam.gserviceaccount.com on the project, after apis (Pub/Sub's service agent)
  roles/run.serviceAgent to serviceAccount:service-1034397096264@serverless-robot-prod.iam.gserviceaccount.com on the project, after apis (Cloud Run's service agent)
  roles/firestore.serviceAgent to serviceAccount:service-1034397096264@gcp-sa-firestore.iam.gserviceaccount.com on the project, after database (Firestore's service agent)
  roles/firebaserules.system to serviceAccount:service-1034397096264@firebase-rules.iam.gserviceaccount.com on the project, after projectBindings (late from the database: Firebase Rules' system account)
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
			"  ✓ projectBindings: granted 3 roles on the project\n"+
			"      IAM + roles/firestore.serviceAgent to "+firestoreAgent+" on the project (Google, after database)\n",
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
	if len(rec.Google) != 2 || rec.Google[0] != (GoogleGrant{Role: "roles/run.serviceAgent", Member: runAgent, Step: "apis"}) ||
		rec.Google[1] != (GoogleGrant{Role: "roles/firestore.serviceAgent", Member: firestoreAgent, Step: "database"}) {
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
	// Once the database holds the record, each step starts by writing it, with the step it is at.
	const record = "PATCH databases/dmtr-europe-west1/documents/install/record"
	want := []string{
		"POST services:batchEnable",
		"POST roles", "POST roles",
		"POST serviceAccounts",
		"POST tagKeys", "POST tagValues",
		"POST databases",
		record, // the database's first write
		record, "POST projects/dormeto-dev:setIamPolicy",
		record, record, "POST queues", "POST queues/dmtr-europe-west1:setIamPolicy", // the step's start, the queue listed first
		record, "POST serviceAccounts/" + routerSA + ":setIamPolicy",
		record, record, // the record step's start, and its end: finished
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
			u, _, _ := strings.Cut(c.URL, "?currentDocument.")
			got = append(got, c.Method+" "+u)
		}
	}
	record := "PATCH https://firestore.googleapis.com/v1/projects/dormeto-dev/databases/dmtr-europe-west1/documents/install/record"
	want := []string{
		record,
		"POST https://iam.googleapis.com/v1/projects/dormeto-dev/roles/dormetoRouter:undelete",
		"PATCH https://iam.googleapis.com/v1/projects/dormeto-dev/roles/dormetoRouter?updateMask=title,description,includedPermissions,stage",
		record, record,
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
	if len(patches) != 1 || !strings.HasSuffix(patches[0].URL, "?updateMask=title,description,includedPermissions,stage") {
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
	// batchEnable, the tag key and value, the database: 3 polls each; then each tag's operation once more for its
	// response (gcp.WaitOperation does not return it).
	if polls != 14 {
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
	if len(rec.Google) != 2 || rec.Google[0].Member != runAgent || rec.Google[0].Step != "apis" || rec.Google[1].Step != "database" {
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
	if len(rec.Google) != 3 || rec.Google[0].Role != "roles/x.serviceAgent" || rec.Google[1].Member != runAgent {
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
	rec := g.record(t)
	p := g.projectPolicy(t)
	x.rec, x.start = &rec, &p
	warnings, err := x.verify()
	if len(warnings) != 0 {
		t.Fatalf("warnings %v", warnings)
	}
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

func TestServiceAccountPolicyIsReadWithAnEmptyBody(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// Planning reads the existing account's policy: IAM wants the version in the query and an empty body.
	if code, out := g.run(t, europe); code != 0 || !strings.Contains(out, "is up to date.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	reads := g.s.CallsTo("/serviceAccounts/" + routerSA + ":getIamPolicy")
	if len(reads) < 2 {
		t.Fatalf("%d reads", len(reads))
	}
	for _, c := range reads {
		if c.Method != "POST" || c.Body != "" || !strings.HasSuffix(c.URL, ":getIamPolicy?options.requestedPolicyVersion=3") {
			t.Fatalf("%+v", c)
		}
	}
	// The fake refuses a body there, as Google does.
	in := map[string]any{"options": map[string]int{"requestedPolicyVersion": 3}}
	err := g.d.Google.Do(context.Background(), "POST", "https://iam.googleapis.com/v1/projects/dormeto-dev/serviceAccounts/"+routerSA+":getIamPolicy", in, nil)
	var e *gcp.Error
	if !errors.As(err, &e) || e.Code != 400 || e.Status != "INVALID_ARGUMENT" {
		t.Fatalf("%v", err)
	}
}

// grantInFake gives member role on the fake's project, as someone else would.
func grantInFake(t *testing.T, s *gcptest.Server, role, member string) {
	t.Helper()
	s.Lock()
	defer s.Unlock()
	b, _ := json.Marshal(s.Policy)
	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	Add(&p, Grant{Role: role, Member: member})
	b, _ = json.Marshal(p)
	var m map[string]any
	json.Unmarshal(b, &m)
	s.Policy = m
}

func TestVerifyWarnsOfAnIAMChangeNotMadeByThisInstall(t *testing.T) {
	g := newInstallRig(t)
	injected := false
	g.s.Route("cloudresourcemanager.googleapis.com", "/v3/projects/dormeto-dev:getIamPolicy", func(w http.ResponseWriter, r *http.Request) {
		// Someone grants a role after the install's last read, before its verification.
		if !injected && len(callsOf(g.s.CallsTo("/documents/install/record"), "PATCH")) == 7 {
			injected = true
			grantInFake(t, g.s, "roles/viewer", "user:someone@example.com")
		}
		g.s.Lock()
		p := g.s.Policy
		g.s.Unlock()
		gcptest.Reply(w, p)
	})
	code, out := g.run(t, europe)
	if code != 0 || !injected {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✓ verified: the planned grants, the roles' permissions, the service account, the database and the queue\n"+
		"  ! IAM change not made by this install: + roles/viewer to user:someone@example.com on the project\n"+
		"Installed the access of router dmtr-europe-west1 in project dormeto-dev.\n")
	// Dormeto's grants, Google's recorded ones and the before are not warned of.
	if n := strings.Count(out, "! IAM change not made by this install:"); n != 1 {
		t.Fatalf("%d warnings:\n%s", n, out)
	}
}

func TestInstallStopsOnADisabledServiceAccount(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.s.Lock()
	g.s.ServiceAccounts[routerSA].Disabled = true
	g.s.Unlock()
	before := len(g.s.Mutations())
	r := europe
	r.Configuration = "dormeto-dev"
	code, out := g.run(t, r)
	if code != 1 || strings.Contains(out, "up to date") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "The service account "+routerSA+" is disabled: enable it (gcloud iam service-accounts enable "+routerSA+
		" --configuration dormeto-dev), then run the same command again.\n")
	if after := len(g.s.Mutations()); after != before {
		t.Fatalf("%d mutating calls", after-before)
	}
}

func TestInstallPatchesARoleOfAnotherStage(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.s.Lock()
	g.s.Roles["dormetoRouter"].Stage = "BETA"
	g.s.Unlock()
	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ~ role dormetoRouter: stage GA\n", "  ✓ roles: updated dormetoRouter\n", "  ✓ verified:")
	if g.s.Roles["dormetoRouter"].Stage != "GA" {
		t.Fatal(g.s.Roles["dormetoRouter"].Stage)
	}
}

func TestInstallTakesTheTagIDsFromTheOperations(t *testing.T) {
	old := operationEvery
	operationEvery = time.Millisecond
	t.Cleanup(func() { operationEvery = old })
	for _, polls := range []int{0, 1, 3} {
		g := newInstallRig(t)
		g.s.OperationPolls = polls
		if code, out := g.run(t, europe); code != 0 {
			t.Fatalf("polls %d: exit %d:\n%s", polls, code, out)
		}
		// The key is read once while planning (missing, so its value is not); never again after the creations.
		if k, v := len(g.s.CallsTo("/v3/tagKeys/namespaced")), len(g.s.CallsTo("/v3/tagValues/namespaced")); k != 1 || v != 0 {
			t.Fatalf("polls %d: %d key reads, %d value reads", polls, k, v)
		}
		rec := g.record(t)
		for _, kind := range []string{"tagKey", "tagValue"} {
			found := false
			for _, r := range rec.Resources {
				store := g.s.TagKeys
				if kind == "tagValue" {
					store = g.s.TagValues
				}
				if r.Kind == kind && store[r.Name] != nil {
					found = true
				}
			}
			if !found {
				t.Errorf("polls %d: no %s in %+v", polls, kind, rec.Resources)
			}
		}
	}
}

func TestInstallDoesNotWarnOfWhatWasThereBeforeTheRun(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// Another router of the project, installed since, and a change the next run applies.
	west4 := "serviceAccount:dmtr-europe-west4@dormeto-dev.iam.gserviceaccount.com"
	grantInFake(t, g.s, "projects/dormeto-dev/roles/dormetoRouter", west4)
	grantInFake(t, g.s, "roles/viewer", "user:earlier@example.com")
	g.s.Lock()
	g.s.Roles["dormetoRouter"].Stage = "BETA"
	g.s.Unlock()
	code, out := g.run(t, europe)
	if code != 0 || !strings.Contains(out, "  ✓ verified:") || strings.Contains(out, "IAM change not made by this install") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestInstallTestsOnlyItsOwnPermissions(t *testing.T) {
	g := newInstallRig(t)
	// A permission of another command that Google does not know does not stop install.
	g.s.Invalid = map[string]bool{"run.services.create": true}
	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if perms, _ := asked(t, g.s); !slices.Equal(perms, access.Needs("install")) {
		t.Fatalf("asked %v\nwant %v", perms, access.Needs("install"))
	}
}

func TestInstallNamesAPermissionGoogleRejects(t *testing.T) {
	g := newInstallRig(t)
	g.s.Invalid = map[string]bool{"iam.roles.undelete": true}
	code, out := g.run(t, europe)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "testIamPermissions rejects iam.roles.undelete (google: 400 INVALID_ARGUMENT: Permission iam.roles.undelete is not valid.)\n",
		"Nothing was changed.\n")
	if strings.Contains(out, "Plan for") {
		t.Fatal(out)
	}
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
}

func TestInstallStopsWithoutBilling(t *testing.T) {
	g := newInstallRig(t)
	g.s.NoBilling = true
	r := europe
	r.Configuration = "dormeto-dev"
	code, out := g.run(t, r)
	if code != 1 || strings.Contains(out, "Plan for") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ billing: not enabled on project dormeto-dev: link a billing account (gcloud billing projects link dormeto-dev --billing-account <id> --configuration dormeto-dev), then run again\n"+
		"Nothing was changed.\n")
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
}

func TestInstallWarnsWhenBillingIsNotReadable(t *testing.T) {
	g := newInstallRig(t)
	g.s.BillingDenied = true
	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Checked: me@example.com holds every permission dormeto gcp install needs in project dormeto-dev.\n"+
		"  ! billing not checked: google: 403 PERMISSION_DENIED: The caller does not have permission\n")
}

func TestInstallPlansOnceAnAPIAnswersAfterAPropagationFailure(t *testing.T) {
	g := newInstallRig(t)
	// IAM's API, once on, answers SERVICE_DISABLED for longer than the first run waits (5 minutes, every 5 s: 61 calls),
	// then for the next run's first 2 calls.
	g.s.Propagating["iam.googleapis.com"] = 61 + 2
	code, out := g.run(t, europe)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✓ apis: ", "  ✗ roles: google: 403 PERMISSION_DENIED (SERVICE_DISABLED): iam.googleapis.com has not been used",
		"Done: apis. Not done: roles, ")
	if len(*g.slept) != 60 || (*g.slept)[0] != 5*time.Second {
		t.Fatalf("slept %d times: %v", len(*g.slept), *g.slept)
	}

	// The next run: the API is on (Service Usage says so) but not answering yet while planning; it waits, then plans.
	code, out = g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Checked: me@example.com holds every permission dormeto gcp install needs in project dormeto-dev.\n"+
		"  … iam.googleapis.com is on but does not answer yet: waiting for Google to propagate it (up to 5 minutes)\n"+
		"Plan for project dormeto-dev, router dmtr-europe-west1:\n"+
		"  + role dormetoRouter \"Dormeto router\" (43 permissions)\n", "  ✓ roles: created dormetoRouter, created dormetoRouterBackends\n",
		"Installed the access of router dmtr-europe-west1 in project dormeto-dev.\n")
	if !slices.Equal(*g.slept, []time.Duration{5 * time.Second, 5 * time.Second}) {
		t.Fatalf("slept %v", *g.slept)
	}
}

// cached is the record waiting in the cache.
func (g *installRig) cached(t *testing.T) Record {
	t.Helper()
	b, err := os.ReadFile(g.cacheFile())
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func apis(r Record) int {
	n := 0
	for _, res := range r.Resources {
		if res.Kind == "api" {
			n++
		}
	}
	return n
}

// stopDuringTheAPIsWait stops the run (Ctrl-C) at its first poll of the APIs' operation; finish says whether Google
// finishes turning them on right then (before the CLI reads again) or later.
func stopDuringTheAPIsWait(t *testing.T, g *installRig, finish bool) context.Context {
	old := operationEvery
	operationEvery = time.Millisecond
	t.Cleanup(func() { operationEvery = old })
	ctx, cancel := context.WithCancel(context.Background())
	g.s.OperationPolls = 5
	g.s.Route("serviceusage.googleapis.com", "/v1/operations/", func(w http.ResponseWriter, r *http.Request) {
		if finish {
			g.s.FinishOperations()
		}
		cancel()
		gcptest.Reply(w, map[string]any{"name": strings.TrimPrefix(r.URL.Path, "/v1/"), "done": false})
	})
	return ctx
}

func TestInstallStoppedDuringAStepSaysGooglesChanges(t *testing.T) {
	g := newInstallRig(t)
	ctx := stopDuringTheAPIsWait(t, g, true)
	g.out.Reset()
	code := Install(ctx, g.d, europe)
	out := g.out.String()
	if code != 130 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "context canceled\n      IAM + roles/run.serviceAgent to "+runAgent+" on the project (Google, after apis)\n"+
		"Done: none. Not done: apis, roles, ")
	rec := g.cached(t)
	if len(rec.Google) != 1 || rec.Google[0] != (GoogleGrant{Role: "roles/run.serviceAgent", Member: runAgent, Step: "apis"}) {
		t.Errorf("google %+v", rec.Google)
	}
	if apis(rec) != 10 || len(rec.Enabling) != 0 || rec.Running != "apis" {
		t.Errorf("resources %+v, enabling %v, running %q", rec.Resources, rec.Enabling, rec.Running)
	}
}

func TestInstallSaysGooglesChangesWhenTheReadAfterAStepFails(t *testing.T) {
	g := newInstallRig(t)
	failed := false
	g.s.Route("cloudresourcemanager.googleapis.com", "/v3/projects/dormeto-dev:getIamPolicy", func(w http.ResponseWriter, r *http.Request) {
		if !failed && len(g.s.CallsTo("services:batchEnable")) == 1 {
			failed = true
			gcptest.Fail(w, 403, "PERMISSION_DENIED", "denied once")
			return
		}
		g.s.Lock()
		p := g.s.Policy
		g.s.Unlock()
		gcptest.Reply(w, p)
	})
	code, out := g.run(t, europe)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ apis: google: 403 PERMISSION_DENIED: denied once\n"+
		"      IAM + roles/run.serviceAgent to "+runAgent+" on the project (Google, after apis)\n"+
		"Done: none. Not done: apis, ")
	if rec := g.cached(t); len(rec.Google) != 1 || rec.Google[0].Member != runAgent || apis(rec) != 10 {
		t.Fatalf("%+v", rec)
	}
	code, out = g.run(t, europe)
	if code != 0 || strings.Contains(out, "since the last run") || strings.Contains(out, "IAM change not made") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if rec := g.record(t); len(rec.Google) != 2 || rec.Google[0].Step != "apis" || rec.Google[1].Step != "database" || apis(rec) != 10 || rec.Running != "" {
		t.Fatalf("%+v", rec)
	}
}

func TestInstallAfterAStoppedRunRecordsGooglesChangesSeenLater(t *testing.T) {
	g := newInstallRig(t)
	g.s.DefaultEditor = true
	ctx := stopDuringTheAPIsWait(t, g, false)
	g.out.Reset()
	if code := Install(ctx, g.d, europe); code != 130 || strings.Contains(g.out.String(), "IAM +") {
		t.Fatalf("exit %d:\n%s", code, g.out)
	}
	if rec := g.cached(t); apis(rec) != 0 || len(rec.Enabling) != 10 {
		t.Fatalf("%+v", rec)
	}
	// Google finishes turning the APIs on after the CLI stopped; someone grants a role, another router is installed.
	g.s.FinishOperations()
	grantInFake(t, g.s, "roles/viewer", "user:someone@example.com")
	grantInFake(t, g.s, "projects/dormeto-dev/roles/dormetoRouter", "serviceAccount:dmtr-europe-west4@dormeto-dev.iam.gserviceaccount.com")

	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "IAM changes since the last run, not Dormeto's:\n"+
		"  + roles/editor to "+computeDefault+" on the project (Google, after apis (seen later))\n"+
		"  + roles/run.serviceAgent to "+runAgent+" on the project (Google, after apis (seen later))\n"+
		"  + roles/viewer to user:someone@example.com on the project (not a Google service agent: not recorded)\n")
	if strings.Contains(out, "dmtr-europe-west4@dormeto-dev.iam.gserviceaccount.com on the project (") || strings.Contains(out, "\n  + API ") ||
		strings.Contains(out, "IAM change not made by this install") {
		t.Fatalf("%s", out)
	}
	rec := g.record(t)
	want := []GoogleGrant{{Role: "roles/editor", Member: computeDefault, Step: "apis (seen later)"},
		{Role: "roles/run.serviceAgent", Member: runAgent, Step: "apis (seen later)"},
		{Role: "roles/firestore.serviceAgent", Member: firestoreAgent, Step: "database"}}
	if !slices.Equal(rec.Google, want) {
		t.Errorf("google %+v", rec.Google)
	}
	if apis(rec) != 10 || len(rec.Enabling) != 0 || rec.Running != "" || len(rec.Resources) != 17 {
		t.Errorf("resources %+v, enabling %v, running %q", rec.Resources, rec.Enabling, rec.Running)
	}
	// Recorded: the next run plans nothing (only the binding it does not record is shown again).
	if code, out := g.run(t, europe); code != 0 || !strings.Contains(out, "is up to date.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestInstallResumesTheUnfinishedRecordOfTheDatabase(t *testing.T) {
	g := newInstallRig(t)
	// The run is killed (no failure path) after the database step wrote the record: Google's grant comes later.
	g.s.Refuse(gcptest.Refusal{Method: "POST", Host: "cloudresourcemanager.googleapis.com", Path: "/v3/projects/dormeto-dev:setIamPolicy",
		Times: 1, Code: 403, Status: "PERMISSION_DENIED", Message: "denied"})
	if code, out := g.run(t, europe); code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	rec := g.record(t)
	rec.Running = "database"
	b, _ := json.Marshal(rec)
	g.s.Lock()
	g.s.Documents[recordDB+"/install/record"] = map[string]any{"data": map[string]any{"stringValue": string(b)}}
	g.s.Unlock()
	agent := "serviceAccount:service-1034397096264@gcp-sa-cloudtasks.iam.gserviceaccount.com"
	grantInFake(t, g.s, "roles/cloudtasks.serviceAgent", agent)
	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "IAM changes since the last run, not Dormeto's:\n"+
		"  + roles/cloudtasks.serviceAgent to "+agent+" on the project (Google, after database (seen later))\n")
	if rec := g.record(t); rec.Running != "" || !slices.Contains(rec.Google, GoogleGrant{Role: "roles/cloudtasks.serviceAgent", Member: agent, Step: "database (seen later)"}) {
		t.Fatalf("%+v", rec)
	}
}

func TestInstallPlanSaysWhichBindingsGoogleIsExpectedToAdd(t *testing.T) {
	dry := Request{Project: "dormeto-dev", Region: "europe-west1", DryRun: true}
	// In an organization (its policy decides the default service account's Editor): every other one.
	g := newInstallRig(t)
	g.s.Parent = "organizations/42"
	code, out := g.run(t, dry)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  + record install/record in database dmtr-europe-west1\n"+
		"Turning APIs on and creating the database can make Google add bindings of its own: they are shown after those steps. Expected:\n"+
		"  roles/cloudscheduler.serviceAgent to "+schedulerAgent+" on the project, after apis (Cloud Scheduler's service agent)\n"+
		"  roles/cloudtasks.serviceAgent to "+tasksAgent+" on the project, after apis (Cloud Tasks' service agent)\n"+
		"  roles/containerregistry.ServiceAgent to "+registryAgent+" on the project, after apis (Container Registry's service agent)\n"+
		"  roles/pubsub.serviceAgent to "+pubsubAgent+" on the project, after apis (Pub/Sub's service agent)\n"+
		"  roles/run.serviceAgent to "+runAgent+" on the project, after apis (Cloud Run's service agent)\n"+
		"  roles/firestore.serviceAgent to "+firestoreAgent+" on the project, after database (Firestore's service agent)\n"+
		"  roles/firebaserules.system to "+rulesSystem+" on the project, after projectBindings (late from the database: Firebase Rules' system account)\n")
	if strings.Contains(out, "roles/editor") {
		t.Fatal(out)
	}

	// The APIs on: the database alone.
	g = newInstallRig(t)
	g.s.Enabled = access.Load().APIs
	code, out = g.run(t, dry)
	if code != 0 || !strings.HasSuffix(out, "  + record install/record in database dmtr-europe-west1\n"+
		"Creating the database can make Google add bindings of its own: they are shown after that step. Expected:\n"+
		"  roles/firestore.serviceAgent to "+firestoreAgent+" on the project, after database (Firestore's service agent)\n"+
		"  roles/firebaserules.system to "+rulesSystem+" on the project, after projectBindings (late from the database: Firebase Rules' system account)\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	// Firestore's service agent and Firebase Rules' account holding their roles already (another database): nothing expected.
	grantInFake(t, g.s, "roles/firestore.serviceAgent", firestoreAgent)
	grantInFake(t, g.s, "roles/firebaserules.system", rulesSystem)
	code, out = g.run(t, dry)
	if code != 0 || !strings.HasSuffix(out, "  + record install/record in database dmtr-europe-west1\n"+
		"Creating the database can make Google add bindings of its own: they are shown after that step.\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestInstallWarnsOfTheDefaultServiceAccountsEditorAfterTheAPIs(t *testing.T) {
	g := newInstallRig(t)
	g.s.DefaultEditor = true
	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	warning := "  ! the Compute Engine default service account 1034397096264-compute@developer.gserviceaccount.com has Editor on project dormeto-dev: Dormeto does not use it; remove the grant when nothing else needs it\n"
	contains(t, out, "      IAM + roles/editor to "+computeDefault+" on the project (Google, after apis)\n"+
		"      IAM + roles/run.serviceAgent to "+runAgent+" on the project (Google, after apis)\n"+warning+
		"  ✓ roles: ")
	if n := strings.Count(out, warning); n != 1 {
		t.Fatalf("%d warnings:\n%s", n, out)
	}
	// There before the run, the check's warning is the only one.
	g = newInstallRig(t)
	g.s.DefaultEditor = true
	grantInFake(t, g.s, "roles/editor", computeDefault)
	code, out = g.run(t, europe)
	if code != 0 || strings.Count(out, warning) != 1 || !strings.Contains(out, "dormeto-dev.\n"+warning+"Plan for") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

const (
	routerImage    = "europe-west1-docker.pkg.dev/dormeto-dev/dormeto/router:0.34.0"
	nextImage      = "europe-west1-docker.pkg.dev/dormeto-dev/dormeto/router:0.35.0"
	controllerName = "dmtr-europe-west1-controller"
	wakerName      = "dmtr-europe-west1-waker"
	jobName        = "dmtr-europe-west1-check"
	controllerURL  = "https://dmtr-europe-west1-controller-1034397096264.europe-west1.run.app"
	servicesPath   = "/v2/projects/dormeto-dev/locations/europe-west1/services/"
)

var withImage = Request{Project: "dormeto-dev", Region: "europe-west1", Image: routerImage, Yes: true}

const routerPlan = "  + service " + controllerName + " (image " + routerImage + ", ingress all, invoker check on, 0 to 10 instances, 10 concurrent requests, timeout 1800 s, 512 MiB, CPU during requests only, as " + routerSA + ")\n" +
	"  + grant roles/run.invoker to " + routerMember + " on controller " + controllerName + "\n" +
	"  + service " + wakerName + " (image " + routerImage + ", ingress internal and Cloud Load Balancing, invoker check off, 0 to 10 instances, 80 concurrent requests, timeout 120 s, 256 MiB, CPU during requests only, as " + routerSA + ")\n" +
	"  + network endpoint group " + wakerName + " (serverless, Cloud Run service " + wakerName + ")\n" +
	"  + job " + jobName + " (*/5 * * * * Etc/UTC, attempt deadline 300 s, POST " + controllerURL + "/v1/check, OIDC of " + routerSA + ", audience " + controllerURL + ")\n" +
	"  + record install/record in database dmtr-europe-west1\n"

// newRouterRig is a rig whose Artifact Registry holds the router's images.
func newRouterRig(t *testing.T) *installRig {
	t.Helper()
	g := newInstallRig(t)
	g.s.Images[routerImage] = true
	g.s.Images[nextImage] = true
	return g
}

// routerInstalled is a rig with the router installed from routerImage.
func routerInstalled(t *testing.T) *installRig {
	t.Helper()
	g := newRouterRig(t)
	if code, out := g.run(t, withImage); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	return g
}

// changes are the mutating calls since mark, as "<method> <path after the project's and the region's prefix>[?query]".
func (g *installRig) changes(mark int) []string {
	var got []string
	for _, c := range g.s.Calls()[mark:] {
		if c.Mutating() {
			u, _, _ := strings.Cut(c.URL, "?currentDocument.") // TestRecordEachWriteNamesTheVersionItRead has it
			got = append(got, c.Method+" "+strings.TrimPrefix(u, "https://"))
		}
	}
	return got
}

func TestInstallImageDryRunPlansTheRouterAfterTheAccess(t *testing.T) {
	g := newRouterRig(t)
	r := withImage
	r.Yes, r.DryRun = false, true
	code, out := g.run(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  + grant roles/iam.serviceAccountUser to "+routerMember+" on service account "+routerSA+"\n"+routerPlan)
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
}

func TestInstallWithoutImageDoesNotPlanTheRouter(t *testing.T) {
	g := newRouterRig(t)
	code, out := g.run(t, Request{Project: "dormeto-dev", Region: "europe-west1", DryRun: true})
	if code != 0 || strings.Contains(out, "service dmtr") || strings.Contains(out, "network endpoint group") || strings.Contains(out, "+ job") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestInstallImageCreatesTheRouter(t *testing.T) {
	g := newRouterRig(t)
	code, out := g.run(t, withImage)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Checked: image "+routerImage+" is in Artifact Registry.\n",
		"  ✓ cloudRun: created "+controllerName+", granted roles/run.invoker on it, created "+wakerName+" (invoker check off)\n"+
			"      IAM + roles/run.invoker to "+routerMember+" on controller "+controllerName+" (Dormeto)\n"+
			"  ✓ neg: created "+wakerName+"\n"+
			"  ✓ job: created "+jobName+"\n"+
			"  ✓ record: wrote install/record in database dmtr-europe-west1\n"+
			"  ✓ verified: the planned grants, the roles' permissions, the service account, the database and the queue\n"+
			"  ✓ verified: both Cloud Run services ready, the controller's URL among its URLs, the network endpoint group, the job, /v1/health answering version 0.34.0\n"+
			"Installed router dmtr-europe-west1 in project dormeto-dev, version 0.34.0.\n")

	c, w := g.s.RunServices["europe-west1/"+controllerName], g.s.RunServices["europe-west1/"+wakerName]
	if c == nil || w == nil {
		t.Fatalf("services: %v", g.s.RunServices)
	}
	if c.Ingress != "INGRESS_TRAFFIC_ALL" || c.InvokerIamDisabled {
		t.Errorf("controller %+v", c)
	}
	if w.Ingress != "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER" || !w.InvokerIamDisabled {
		t.Errorf("waker %+v", w)
	}
	for _, want := range []struct {
		svc              *gcptest.RunService
		maxInst, concurr int
		timeout, memory  string
		role             string
	}{{c, 10, 10, "1800s", "512Mi", "controller"}, {w, 10, 80, "120s", "256Mi", "waker"}} {
		tmpl := want.svc.Template
		ct := tmpl.Containers[0]
		if tmpl.ServiceAccount != routerSA || tmpl.Scaling.MinInstanceCount != 0 || tmpl.Scaling.MaxInstanceCount != want.maxInst ||
			tmpl.MaxInstanceRequestConcurrency != want.concurr || tmpl.Timeout != want.timeout || ct.Image != routerImage ||
			ct.Resources.Limits["memory"] != want.memory || !ct.Resources.CPUIdle || ct.Ports[0].ContainerPort != 8080 {
			t.Errorf("%s template %+v", want.role, tmpl)
		}
		env := map[string]string{}
		for _, e := range ct.Env {
			env[e.Name] = e.Value
		}
		wantEnv := map[string]string{"DMT_ROLE": want.role, "DMT_PROJECT": "dormeto-dev", "DMT_PROJECT_NUMBER": "1034397096264",
			"DMT_REGION": "europe-west1", "DMT_DATABASE": "dmtr-europe-west1", "DMT_QUEUE": "dmtr-europe-west1", "DMT_CONTROLLER_URL": controllerURL,
			"DMT_SERVICE_ACCOUNT": routerSA, "DMT_WAKER_NEG": wakerName, "DMT_VERSION": "0.34.0"}
		if len(env) != len(wantEnv) {
			t.Errorf("%s env %v", want.role, env)
		}
		for k, v := range wantEnv {
			if env[k] != v {
				t.Errorf("%s env %s = %q, want %q", want.role, k, env[k], v)
			}
		}
	}
	if p := g.policyOf(t, "projects/dormeto-dev/locations/europe-west1/services/"+controllerName); !has(p, Grant{Role: "roles/run.invoker", Member: routerMember}) {
		t.Errorf("controller's policy %+v", p)
	}
	if p := g.policyOf(t, "projects/dormeto-dev/locations/europe-west1/services/"+wakerName); len(p.Bindings) != 0 {
		t.Errorf("waker's policy %+v", p)
	}
	neg := g.s.NEGs["europe-west1/"+wakerName]
	if neg == nil || neg.NetworkEndpointType != "SERVERLESS" || neg.CloudRun.Service != wakerName {
		t.Errorf("NEG %+v", neg)
	}
	job := g.s.Jobs["europe-west1/"+jobName]
	if job == nil || job.Name != "projects/dormeto-dev/locations/europe-west1/jobs/"+jobName || job.Schedule != "*/5 * * * *" || job.TimeZone != "Etc/UTC" ||
		job.AttemptDeadline != "300s" || job.HTTPTarget.URI != controllerURL+"/v1/check" || job.HTTPTarget.HTTPMethod != "POST" ||
		job.HTTPTarget.OIDCToken.ServiceAccountEmail != routerSA || job.HTTPTarget.OIDCToken.Audience != controllerURL {
		t.Errorf("job %+v", job)
	}

	rec := g.record(t)
	if rec.Image != routerImage || rec.RouterVersion != "0.34.0" || rec.Running != "" {
		t.Errorf("record %+v", rec)
	}
	for _, want := range []Resource{
		{Kind: "service", Name: "projects/dormeto-dev/locations/europe-west1/services/" + controllerName},
		{Kind: "service", Name: "projects/dormeto-dev/locations/europe-west1/services/" + wakerName},
		{Kind: "neg", Name: "projects/dormeto-dev/regions/europe-west1/networkEndpointGroups/" + wakerName},
		{Kind: "job", Name: "projects/dormeto-dev/locations/europe-west1/jobs/" + jobName},
	} {
		if !slices.Contains(rec.Resources, want) {
			t.Errorf("record lacks %+v", want)
		}
	}
	if !slices.Contains(rec.Grants, RecordGrant{Role: "roles/run.invoker", Member: routerMember, On: "controller"}) || len(rec.Grants) != 6 {
		t.Errorf("grants %+v", rec.Grants)
	}
}

func TestInstallImageCreatesInOrderControllerBindingWakerNEGJob(t *testing.T) {
	g := routerInstalled(t)
	var got []string
	for _, c := range g.s.Mutations() {
		if c.Host == "run.googleapis.com" || c.Host == "compute.googleapis.com" || c.Host == "cloudscheduler.googleapis.com" {
			u, _, _ := strings.Cut(c.URL, "?currentDocument.") // TestRecordEachWriteNamesTheVersionItRead has it
			got = append(got, c.Method+" "+strings.TrimPrefix(u, "https://"))
		}
	}
	want := []string{
		"POST run.googleapis.com/v2/projects/dormeto-dev/locations/europe-west1/services?serviceId=" + controllerName,
		"POST run.googleapis.com" + servicesPath + controllerName + ":setIamPolicy",
		"POST run.googleapis.com/v2/projects/dormeto-dev/locations/europe-west1/services?serviceId=" + wakerName,
		"POST compute.googleapis.com/compute/v1/projects/dormeto-dev/regions/europe-west1/networkEndpointGroups",
		"POST cloudscheduler.googleapis.com/v1/projects/dormeto-dev/locations/europe-west1/jobs",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestInstallImageAgainIsUpToDate(t *testing.T) {
	g := routerInstalled(t)
	mark := len(g.s.Calls())
	code, out := g.run(t, withImage)
	if code != 0 || !strings.HasSuffix(out, "Router dmtr-europe-west1 in project dormeto-dev is up to date: version 0.34.0.\n") || strings.Contains(out, "Plan for") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if m := g.changes(mark); len(m) != 0 {
		t.Fatalf("changes: %v", m)
	}
}

func TestInstallAnotherTagUpdatesOnlyTheTwoServices(t *testing.T) {
	g := routerInstalled(t)
	mark := len(g.s.Calls())
	r := withImage
	r.Image = nextImage
	code, out := g.run(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Plan for project dormeto-dev, router dmtr-europe-west1:\n"+
		"  ~ service "+controllerName+": image "+nextImage+", environment DMT_VERSION\n"+
		"  ~ service "+wakerName+": image "+nextImage+", environment DMT_VERSION\n"+
		"  ~ record install/record in database dmtr-europe-west1\n",
		"  ✓ cloudRun: updated "+controllerName+", updated "+wakerName+"\n",
		"/v1/health answering version 0.35.0\n", "Installed router dmtr-europe-west1 in project dormeto-dev, version 0.35.0.\n")
	mask := "?updateMask=ingress,invokerIamDisabled,template"
	record := "PATCH firestore.googleapis.com/v1/projects/dormeto-dev/databases/dmtr-europe-west1/documents/install/record"
	want := []string{
		record,
		"PATCH run.googleapis.com" + servicesPath + controllerName + mask,
		"PATCH run.googleapis.com" + servicesPath + wakerName + mask,
		record, record,
	}
	if got := g.changes(mark); !slices.Equal(got, want) {
		t.Fatalf("%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, name := range []string{controllerName, wakerName} {
		svc := g.s.RunServices["europe-west1/"+name]
		if svc.Template.Containers[0].Image != nextImage || svc.Env("DMT_VERSION") != "0.35.0" {
			t.Errorf("%s %+v", name, svc.Template)
		}
	}
	if rec := g.record(t); rec.Image != nextImage || rec.RouterVersion != "0.35.0" {
		t.Errorf("record %+v", rec)
	}
}

func TestInstallWithoutImageLeavesAnInstalledRouterAlone(t *testing.T) {
	g := routerInstalled(t)
	mark := len(g.s.Calls())
	code, out := g.run(t, europe)
	if code != 0 || !strings.Contains(out, "The access of router dmtr-europe-west1 in project dormeto-dev is up to date.\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if m := g.changes(mark); len(m) != 0 {
		t.Fatalf("changes: %v", m)
	}
	if rec := g.record(t); rec.Image != routerImage {
		t.Fatalf("record %+v", rec)
	}
}

func TestInstallMissingImageStopsBeforeAnyChange(t *testing.T) {
	g := newInstallRig(t)
	code, out := g.run(t, withImage)
	if code != 1 || strings.Contains(out, "Plan for") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ image: "+routerImage+" is not in Artifact Registry\nNothing was changed.\n")
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
	heads := callsOf(g.s.CallsTo("/manifests/0.34.0"), "HEAD")
	if len(heads) != 1 || heads[0].URL != "https://europe-west1-docker.pkg.dev/v2/dormeto-dev/dormeto/router/manifests/0.34.0" {
		t.Fatalf("%+v", heads)
	}
}

func TestInstallImageNameBeyondArtifactRegistryIsAWarning(t *testing.T) {
	g := newInstallRig(t)
	r := withImage
	r.Image = "docker.io/dormeto/router:0.34.0"
	code, out := g.run(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ! image docker.io/dormeto/router:0.34.0 is not on *-docker.pkg.dev: not checked\n",
		"Installed router dmtr-europe-west1 in project dormeto-dev, version 0.34.0.\n")
}

func TestInstallImageManifestNotReadableIsAWarning(t *testing.T) {
	for _, status := range []int{403, 401} {
		g := newRouterRig(t)
		g.s.Refuse(gcptest.Refusal{Method: "HEAD", Host: "europe-west1-docker.pkg.dev", Path: "/v2/", Code: status})
		r := withImage
		r.DryRun = true
		code, out := g.run(t, r)
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		contains(t, out, fmt.Sprintf("  ! image %s not checked: google: %d\n", routerImage, status), "Plan for")
	}
}

func TestInstallImageFlagIsChecked(t *testing.T) {
	g := newRouterRig(t)
	for _, image := range []string{"europe-west1-docker.pkg.dev/dormeto-dev/dormeto/router", "europe-west1-docker.pkg.dev/dormeto-dev/dormeto/router@sha256:abc", "localhost:5000/router"} {
		r := withImage
		r.Image = image
		code, out := g.run(t, r)
		if code != 2 || !strings.Contains(out, "--image: "+image+" has no tag: its tag is the router's version\n") {
			t.Errorf("%q: exit %d:\n%s", image, code, out)
		}
	}
	if c := g.s.Calls(); len(c) != 0 {
		t.Fatalf("calls: %+v", c)
	}
}

func TestInstallImageWithADigestChecksTheDigest(t *testing.T) {
	g := newInstallRig(t)
	image := "europe-west1-docker.pkg.dev/dormeto-dev/dormeto/router:0.34.0@sha256:abc"
	g.s.Images["europe-west1-docker.pkg.dev/dormeto-dev/dormeto/router:sha256:abc"] = true
	r := withImage
	r.Image = image
	code, out := g.run(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if svc := g.s.RunServices["europe-west1/"+wakerName]; svc.Template.Containers[0].Image != image || svc.Env("DMT_VERSION") != "0.34.0" {
		t.Fatalf("%+v", svc.Template)
	}
}

func TestInstallImageTestsTheRoutersPermissionsToo(t *testing.T) {
	g := newRouterRig(t)
	g.s.HoldsAll = false
	g.s.Held = map[string]bool{}
	for _, p := range append(access.Needs("install"), access.Needs("install-router")...) {
		g.s.Held[p] = p != "run.services.create" && p != "cloudscheduler.jobs.create"
	}
	code, out := g.run(t, withImage)
	if code != 1 || strings.Contains(out, "Plan for") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ install-router: missing cloudscheduler.jobs.create, run.services.create\nNothing was changed.\n")
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
}

func TestInstallHealthVersionThatDiffersFailsTheVerification(t *testing.T) {
	g := newRouterRig(t)
	g.s.HealthVersion = "0.33.0"
	code, out := g.run(t, withImage)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ verification: "+controllerURL+"/v1/health answers version 0.33.0, not the image's 0.34.0\n")
	if strings.Contains(out, "Installed router") {
		t.Fatal(out)
	}
	// Run again, nothing differs to change: not "up to date" for all that.
	code, out = g.run(t, withImage)
	if code != 1 || strings.Contains(out, "up to date") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ verification: "+controllerURL+"/v1/health answers version 0.33.0, not the image's 0.34.0\n")
}

func TestVerificationNamesWhatIsWrongWithTheRouter(t *testing.T) {
	g := routerInstalled(t)
	g.s.Lock()
	c := g.s.RunServices["europe-west1/"+controllerName]
	c.URLs = []string{c.URI, "https://other-1034397096264.europe-west1.run.app"}
	delete(g.s.Policies, "projects/dormeto-dev/locations/europe-west1/services/"+controllerName)
	g.s.NEGs["europe-west1/"+wakerName].CloudRun.Service = "other"
	g.s.Jobs["europe-west1/"+jobName].Schedule = "0 * * * *"
	g.s.RunServices["europe-west1/"+wakerName].TerminalCondition = gcptest.RunCondition{Type: "Ready", State: "CONDITION_FAILED", Message: "the revision crashed"}
	g.s.Unlock()
	x := newRun(context.Background(), g.d, withImage)
	x.number = "1034397096264"
	rec := g.record(t)
	p := g.projectPolicy(t)
	x.rec, x.start = &rec, &p
	_, err := x.verify()
	if err == nil {
		t.Fatal("verified")
	}
	contains(t, err.Error(), "service "+wakerName+" is not ready (the revision crashed)",
		"the URL "+controllerURL+" is not among the controller's",
		"controller "+controllerName+" lacks the grant of roles/run.invoker to "+routerMember,
		"network endpoint group "+wakerName+" serves other, not "+wakerName,
		"job "+jobName+" has other settings than */5 * * * * Etc/UTC")
}

func TestInstallUpdatesAServiceThatIsNotReady(t *testing.T) {
	g := routerInstalled(t)
	g.s.Lock()
	g.s.RunServices["europe-west1/"+wakerName].TerminalCondition = gcptest.RunCondition{Type: "Ready", State: "CONDITION_FAILED", Message: "the revision crashed"}
	g.s.Unlock()
	code, out := g.run(t, withImage)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ~ service "+wakerName+": not ready (the revision crashed)\n", "  ✓ cloudRun: updated "+wakerName+"\n")
}

func TestInstallPutsBackTheServicesSettings(t *testing.T) {
	g := routerInstalled(t)
	g.s.Lock()
	w := g.s.RunServices["europe-west1/"+wakerName]
	w.Ingress, w.InvokerIamDisabled = "INGRESS_TRAFFIC_ALL", false
	w.Template.Scaling.MaxInstanceCount = 100
	w.Template.MaxInstanceRequestConcurrency = 1
	w.Template.Timeout = "60s"
	w.Template.ServiceAccount = "other@example.com"
	w.Template.Containers[0].Resources.Limits["memory"] = "1Gi"
	w.Template.Containers[0].Resources.CPUIdle = false
	g.s.Unlock()
	code, out := g.run(t, withImage)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ~ service "+wakerName+": ingress internal and Cloud Load Balancing, invoker check off, service account "+routerSA+", 0 to 10 instances, 80 concurrent requests, timeout 120 s, 256 MiB, CPU during requests only\n")
	if w.Ingress != "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER" || !w.InvokerIamDisabled || w.Template.Scaling.MaxInstanceCount != 10 || !w.Template.Containers[0].Resources.CPUIdle {
		t.Fatalf("%+v", w)
	}
}

func TestInstallPutsBackTheJobsSettingsAndTheInvokerBinding(t *testing.T) {
	g := routerInstalled(t)
	g.s.Lock()
	g.s.Jobs["europe-west1/"+jobName].HTTPTarget.OIDCToken.Audience = "https://other"
	delete(g.s.Policies, "projects/dormeto-dev/locations/europe-west1/services/"+controllerName)
	g.s.Unlock()
	mark := len(g.s.Calls())
	code, out := g.run(t, withImage)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  + grant roles/run.invoker to "+routerMember+" on controller "+controllerName+"\n",
		"  ~ job "+jobName+": */5 * * * * Etc/UTC, attempt deadline 300 s, POST "+controllerURL+"/v1/check, OIDC of "+routerSA+", audience "+controllerURL+"\n",
		"  ✓ cloudRun: granted roles/run.invoker on "+controllerName+"\n",
		"  ✓ job: updated "+jobName+"\n")
	got := g.changes(mark)
	if !slices.Contains(got, "PATCH cloudscheduler.googleapis.com/v1/projects/dormeto-dev/locations/europe-west1/jobs/"+jobName+"?updateMask=schedule,timeZone,attemptDeadline,httpTarget") || len(got) != 6 {
		t.Fatalf("%v", got)
	}
	if g.s.Jobs["europe-west1/"+jobName].HTTPTarget.OIDCToken.Audience != controllerURL {
		t.Fatal("not updated")
	}
}

func TestInstallStopsOnANetworkEndpointGroupOfAnotherService(t *testing.T) {
	g := newRouterRig(t)
	g.s.Enabled = access.Load().APIs
	neg := &gcptest.NEG{Name: wakerName, NetworkEndpointType: "SERVERLESS"}
	neg.CloudRun.Service = "other"
	g.s.NEGs["europe-west1/"+wakerName] = neg
	code, out := g.run(t, withImage)
	if code != 1 || strings.Contains(out, "Plan for") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "The network endpoint group "+wakerName+" exists and serves other, not "+wakerName+": delete it, or choose another region, then run the same command again.\nNothing was changed.\n")
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
}

func TestInstallShowsGooglesBindingsAfterTheRoutersSteps(t *testing.T) {
	g := newRouterRig(t)
	g.s.GrantOnCreate = map[string][2]string{"service": {"roles/a.serviceAgent", "serviceAccount:service-1034397096264@gcp-sa-a.iam.gserviceaccount.com"},
		"neg": {"roles/b.serviceAgent", "serviceAccount:service-1034397096264@gcp-sa-b.iam.gserviceaccount.com"},
		"job": {"roles/c.serviceAgent", "serviceAccount:service-1034397096264@gcp-sa-c.iam.gserviceaccount.com"}}
	code, out := g.run(t, withImage)
	if code != 0 || strings.Contains(out, "IAM change not made") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out,
		"      IAM + roles/a.serviceAgent to serviceAccount:service-1034397096264@gcp-sa-a.iam.gserviceaccount.com on the project (Google, after cloudRun)\n",
		"  ✓ neg: created "+wakerName+"\n      IAM + roles/b.serviceAgent to serviceAccount:service-1034397096264@gcp-sa-b.iam.gserviceaccount.com on the project (Google, after neg)\n",
		"  ✓ job: created "+jobName+"\n      IAM + roles/c.serviceAgent to serviceAccount:service-1034397096264@gcp-sa-c.iam.gserviceaccount.com on the project (Google, after job)\n")
	rec := g.record(t)
	for _, want := range []GoogleGrant{{Role: "roles/a.serviceAgent", Member: "serviceAccount:service-1034397096264@gcp-sa-a.iam.gserviceaccount.com", Step: "cloudRun"},
		{Role: "roles/c.serviceAgent", Member: "serviceAccount:service-1034397096264@gcp-sa-c.iam.gserviceaccount.com", Step: "job"}} {
		if !slices.Contains(rec.Google, want) {
			t.Errorf("record lacks %+v: %+v", want, rec.Google)
		}
	}
}

func TestInstallNamesTheOrganizationPolicyThatRefusesTheWaker(t *testing.T) {
	refusal := gcptest.Refusal{Host: "run.googleapis.com", Code: 400, Status: "FAILED_PRECONDITION",
		Message: "Disabling the invoker IAM check violates the organization policy constraints/run.managed.requireInvokerIam."}
	exception := "the organization policy run.managed.requireInvokerIam refuses the waker's invoker check off: ask your organization's policy administrator for an exception on project dormeto-dev (constraints/run.managed.requireInvokerIam not enforced there), the waker's ingress still admits only the load balancer"
	cases := map[string]func(g *installRig) (Request, gcptest.Refusal){
		"update": func(*installRig) (Request, gcptest.Refusal) {
			r, f := withImage, refusal
			r.Image = nextImage
			f.Method, f.Path = "PATCH", servicesPath+wakerName
			return r, f
		},
		"create": func(g *installRig) (Request, gcptest.Refusal) {
			delete(g.s.RunServices, "europe-west1/"+wakerName)
			f := refusal
			f.Method, f.Path = "POST", "/v2/projects/dormeto-dev/locations/europe-west1/services"
			return withImage, f
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			g := routerInstalled(t)
			r, f := prepare(g)
			f.Times = 1
			g.s.Refuse(f)
			code, out := g.run(t, r)
			if code != 1 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			contains(t, out, "  ✗ cloudRun: "+exception+" (google: 400 FAILED_PRECONDITION: ", "Done: none. Not done: cloudRun, record. Run the same command again to continue.\n")
			// The exception given, the same command continues.
			code, out = g.run(t, r)
			if code != 0 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if rec := g.record(t); rec.Image != r.Image || rec.Running != "" {
				t.Fatalf("%+v", rec)
			}
		})
	}
}

func TestInstallKeepsThePreviousImageRecordedUntilBothServicesHaveTheNewOne(t *testing.T) {
	g := routerInstalled(t)
	r := withImage
	r.Image = nextImage
	g.s.Refuse(gcptest.Refusal{Method: "PATCH", Host: "run.googleapis.com", Path: servicesPath + wakerName, Times: 1, Code: 500, Status: "INTERNAL", Message: "boom"})
	code, out := g.run(t, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if rec := g.record(t); rec.Image != routerImage || rec.Running != "cloudRun" {
		t.Fatalf("%+v", rec)
	}
	mark := len(g.s.Calls())
	code, out = g.run(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.changes(mark); len(got) != 4 || !strings.HasPrefix(got[1], "PATCH run.googleapis.com"+servicesPath+wakerName) {
		t.Fatalf("%v", got)
	}
	if rec := g.record(t); rec.Image != nextImage || rec.RouterVersion != "0.35.0" {
		t.Fatalf("%+v", rec)
	}
}

func TestInstallRecordsAnImageAlreadyDeployedByARunStoppedBeforeItsRecord(t *testing.T) {
	g := routerInstalled(t)
	rec := g.record(t)
	rec.Image, rec.RouterVersion = "", ""
	b, _ := json.Marshal(rec)
	g.s.Lock()
	g.s.Documents[recordDB+"/install/record"] = map[string]any{"data": map[string]any{"stringValue": string(b)}}
	g.s.Unlock()
	code, out := g.run(t, withImage)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ~ record install/record in database dmtr-europe-west1\n")
	if rec := g.record(t); rec.Image != routerImage || rec.RouterVersion != "0.34.0" {
		t.Fatalf("%+v", rec)
	}
}

// recordBefore is the record as last written before the first call of method whose URL contains part.
func (g *installRig) recordBefore(t *testing.T, method, part string) Record {
	t.Helper()
	var rec Record
	written := false
	for _, c := range g.s.Calls() {
		if c.Method == method && strings.Contains(c.URL, part) {
			if !written {
				t.Fatalf("no record was written before %s %s", method, part)
			}
			return rec
		}
		if c.Method == http.MethodPatch && strings.HasSuffix(c.Path, "/documents/install/record") {
			var doc recordDocument
			if err := json.Unmarshal([]byte(c.Body), &doc); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(doc.Fields.Data.StringValue), &rec); err != nil {
				t.Fatal(err)
			}
			written = true
		}
	}
	t.Fatalf("no %s %s", method, part)
	return rec
}

func listsResource(rec Record, kind, name string) bool {
	return slices.Contains(rec.Resources, Resource{Kind: kind, Name: name})
}

func TestInstallRecordsAServiceWhoseFirstRevisionDoesNotStartSoTheUninstallRemovesIt(t *testing.T) {
	g := newRouterRig(t)
	g.s.FailingRevisions = map[string]string{wakerName: "the container did not start"}
	code, out := g.run(t, withImage)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ cloudRun: ", "the container did not start")
	if rec := g.record(t); !listsResource(rec, "service", "projects/dormeto-dev/locations/europe-west1/services/"+wakerName) {
		t.Fatalf("the service is not recorded: %+v", rec.Resources)
	}
	code, out = g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("uninstall: exit %d:\n%s", code, out)
	}
	contains(t, out, "  - service "+wakerName+"\n")
	g.nothingLeft(t, "europe-west1")
}

func TestInstallRecordsANetworkEndpointGroupWhoseOperationIsNotSeenToEnd(t *testing.T) {
	g := newRouterRig(t)
	// Every try of the poll fails: the group exists in Google, the run stops waiting.
	g.s.Refuse(gcptest.Refusal{Method: "GET", Host: "www.googleapis.com", Path: "/compute/v1/projects/dormeto-dev/regions/europe-west1/operations/",
		Times: 4, Code: 500, Status: "INTERNAL", Message: "boom"})
	code, out := g.run(t, withImage)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ neg: ")
	if rec := g.record(t); !listsResource(rec, "neg", "projects/dormeto-dev/regions/europe-west1/networkEndpointGroups/"+wakerName) {
		t.Fatalf("the group is not recorded: %+v", rec.Resources)
	}
	code, out = g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("uninstall: exit %d:\n%s", code, out)
	}
	contains(t, out, "  - network endpoint group "+wakerName+"\n")
	g.nothingLeft(t, "europe-west1")
}

// A run killed at any point (no failure path to write what it did) has written the step it was at, and the resource it
// was creating, before the call that creates it.
func TestInstallWritesTheRecordBeforeCreatingWhatAKilledRunWouldLeave(t *testing.T) {
	g := newRouterRig(t)
	if code, out := g.run(t, withImage); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	cases := []struct {
		method, part, running, kind, name string
	}{
		{"POST", "/services?serviceId=" + controllerName, "cloudRun", "service", "projects/dormeto-dev/locations/europe-west1/services/" + controllerName},
		{"POST", "/services?serviceId=" + wakerName, "cloudRun", "service", "projects/dormeto-dev/locations/europe-west1/services/" + wakerName},
		{"POST", "/regions/europe-west1/networkEndpointGroups", "neg", "neg", "projects/dormeto-dev/regions/europe-west1/networkEndpointGroups/" + wakerName},
	}
	for _, c := range cases {
		rec := g.recordBefore(t, c.method, c.part)
		if rec.Running != c.running || !listsResource(rec, c.kind, c.name) {
			t.Errorf("before %s %s: running %q, resources %+v", c.method, c.part, rec.Running, rec.Resources)
		}
	}
	if rec := g.recordBefore(t, "POST", "cloudscheduler.googleapis.com/v1/projects/dormeto-dev/locations/europe-west1/jobs"); rec.Running != "job" {
		t.Errorf("before the job: running %q", rec.Running)
	}
	if rec := g.recordBefore(t, "POST", "/queues"); rec.Running != "queue" {
		t.Errorf("before the queue: running %q", rec.Running)
	}
}

const registryAgentRole = "roles/artifactregistry.serviceAgent"

var artifactAgent = "serviceAccount:service-1034397096264@gcp-sa-artifactregistry.iam.gserviceaccount.com"

func TestInstallShowsAndRecordsAServiceAgentsBindingThatAppearedAfterAFinishedInstall(t *testing.T) {
	g := routerInstalled(t)
	grantInFake(t, g.s, registryAgentRole, artifactAgent)
	code, out := g.run(t, withImage)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "IAM changes since the last run, not Dormeto's:\n"+
		"  + "+registryAgentRole+" to "+artifactAgent+" on the project (Google, after install (seen later))\n")
	want := GoogleGrant{Role: registryAgentRole, Member: artifactAgent, Step: "install (seen later)"}
	if rec := g.record(t); !slices.Contains(rec.Google, want) || rec.Running != "" {
		t.Fatalf("%+v", rec)
	}
	// Recorded: the next run has nothing to say.
	code, out = g.run(t, withImage)
	if code != 0 || !strings.Contains(out, "is up to date") || strings.Contains(out, "since the last run") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestInstallDryRunShowsAServiceAgentsBindingThatAppearedAndRecordsNothing(t *testing.T) {
	g := routerInstalled(t)
	grantInFake(t, g.s, registryAgentRole, artifactAgent)
	mark := len(g.s.Calls())
	r := withImage
	r.Yes, r.DryRun = false, true
	code, out := g.run(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  + "+registryAgentRole+" to "+artifactAgent+" on the project (Google, after install (seen later))\n")
	if m := g.changes(mark); len(m) != 0 {
		t.Fatalf("changes: %v", m)
	}
}

func TestInstallUpToDateStillShowsABindingNotDormetosAndRecordsNothing(t *testing.T) {
	g := routerInstalled(t)
	grantInFake(t, g.s, "roles/viewer", "user:someone@example.com")
	mark := len(g.s.Calls())
	code, out := g.run(t, withImage)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "IAM changes since the last run, not Dormeto's:\n"+
		"  + roles/viewer to user:someone@example.com on the project (not a Google service agent: not recorded)\n", "is up to date")
	if m := g.changes(mark); len(m) != 0 {
		t.Fatalf("changes: %v", m)
	}
}

// The router's IP lists (--ip-list, --no-ip-lists).

var (
	blockList = IPList{Name: "scanners", URL: "https://example.com/scanners.txt", RefreshHours: 24}
	botsList  = IPList{Name: "bots", URL: "http://example.com/bots.txt?token=abc", RefreshHours: 6}
)

const (
	blockListEnv = `[{"name":"scanners","url":"https://example.com/scanners.txt","action":"ignore","refreshHours":24}]`
	twoListsEnv  = `[{"name":"scanners","url":"https://example.com/scanners.txt","action":"ignore","refreshHours":24},` +
		`{"name":"bots","url":"http://example.com/bots.txt?token=abc","action":"ignore","refreshHours":6}]`
)

const laterImage = "europe-west1-docker.pkg.dev/dormeto-dev/dormeto/router:0.36.0"

// listsRouter is a rig with the router installed from nextImage: 0.35.0, the first that reads IP lists.
func listsRouter(t *testing.T) *installRig {
	t.Helper()
	g := newRouterRig(t)
	g.s.Images[laterImage] = true
	r := withImage
	r.Image = nextImage
	if code, out := g.run(t, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	return g
}

func withLists(r Request, lists ...IPList) Request {
	r.IPLists = lists
	return r
}

func TestInstallIPListWithoutImageNorRecordedImageStopsBeforeAnyChange(t *testing.T) {
	g := newRouterRig(t)
	code, out := g.run(t, withLists(europe, blockList))
	if code != 2 || !strings.Contains(out, "--image") || !strings.Contains(out, "--ip-list") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
}

func TestInstallIPListReusesTheRecordedImageAndUpdatesBothServices(t *testing.T) {
	g := listsRouter(t)
	mark := len(g.s.Calls())
	code, out := g.run(t, withLists(europe, blockList, botsList))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Plan for project dormeto-dev, router dmtr-europe-west1:\n"+
		"  ~ service "+controllerName+": environment DMT_IP_LISTS\n"+
		"  ~ service "+wakerName+": environment DMT_IP_LISTS, 512 MiB\n",
		"  ~ record install/record in database dmtr-europe-west1\n",
		"  IP list scanners: https://example.com/scanners.txt, every 24 hours, action ignore\n",
		"  IP list bots: http://example.com/bots.txt?token=abc, every 6 hours, action ignore\n")
	mask := "?updateMask=ingress,invokerIamDisabled,template"
	var patches []string
	for _, c := range g.changes(mark) {
		if !strings.Contains(c, "install/record") {
			patches = append(patches, c)
		}
	}
	want := []string{"PATCH run.googleapis.com" + servicesPath + controllerName + mask, "PATCH run.googleapis.com" + servicesPath + wakerName + mask}
	if !slices.Equal(patches, want) {
		t.Fatalf("%v", patches)
	}
	for _, name := range []string{controllerName, wakerName} {
		svc := g.s.RunServices["europe-west1/"+name]
		if svc.Template.Containers[0].Image != nextImage || svc.Env("DMT_IP_LISTS") != twoListsEnv || svc.Env("DMT_VERSION") != "0.35.0" {
			t.Errorf("%s: %+v", name, svc.Template.Containers[0])
		}
	}
	if m := g.s.RunServices["europe-west1/"+wakerName].Template.Containers[0].Resources.Limits["memory"]; m != "512Mi" {
		t.Errorf("waker memory %q", m)
	}
	if m := g.s.RunServices["europe-west1/"+controllerName].Template.Containers[0].Resources.Limits["memory"]; m != "512Mi" {
		t.Errorf("controller memory %q", m)
	}
	if rec := g.record(t); !reflect.DeepEqual(rec.IPLists, []RecordIPList{
		{Name: "scanners", URL: blockList.URL, Action: "ignore", RefreshHours: 24},
		{Name: "bots", URL: botsList.URL, Action: "ignore", RefreshHours: 6}}) || rec.Image != nextImage {
		t.Errorf("record %+v", rec)
	}
}

func TestInstallIPListsAreVerifiedAndAgainIsUpToDate(t *testing.T) {
	g := listsRouter(t)
	if code, out := g.run(t, withLists(europe, blockList)); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	mark := len(g.s.Calls())
	code, out := g.run(t, withLists(europe, blockList))
	if code != 0 || !strings.Contains(out, "is up to date") || len(g.changes(mark)) != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestInstallIPListsOfAnotherSetReplaceTheRecordedOnes(t *testing.T) {
	g := listsRouter(t)
	g.run(t, withLists(europe, blockList))
	if code, out := g.run(t, withLists(europe, botsList)); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.s.RunServices["europe-west1/"+wakerName].Env("DMT_IP_LISTS"); strings.Contains(got, "scanners") || !strings.Contains(got, "bots") {
		t.Errorf("env %s", got)
	}
	if rec := g.record(t); len(rec.IPLists) != 1 || rec.IPLists[0].Name != "bots" {
		t.Errorf("record %+v", rec.IPLists)
	}
}

func TestInstallNoIPListsClearsThemAndGivesBackTheWakersMemory(t *testing.T) {
	g := listsRouter(t)
	g.run(t, withLists(europe, blockList))
	r := europe
	r.NoIPLists = true
	code, out := g.run(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ~ service "+controllerName+": environment DMT_IP_LISTS\n", "  ~ service "+wakerName+": environment DMT_IP_LISTS, 256 MiB\n")
	for _, name := range []string{controllerName, wakerName} {
		svc := g.s.RunServices["europe-west1/"+name]
		if svc.Env("DMT_IP_LISTS") != "" {
			t.Errorf("%s keeps %s", name, svc.Env("DMT_IP_LISTS"))
		}
	}
	if m := g.s.RunServices["europe-west1/"+wakerName].Template.Containers[0].Resources.Limits["memory"]; m != "256Mi" {
		t.Errorf("waker memory %q", m)
	}
	if rec := g.record(t); len(rec.IPLists) != 0 {
		t.Errorf("record %+v", rec.IPLists)
	}
}

func TestInstallNoIPListsWithoutRecordedImageStopsBeforeAnyChange(t *testing.T) {
	g := newRouterRig(t)
	r := europe
	r.NoIPLists = true
	if code, out := g.run(t, r); code != 2 || !strings.Contains(out, "--no-ip-lists") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
}

func TestInstallALaterImageKeepsTheRecordedIPLists(t *testing.T) {
	g := listsRouter(t)
	g.run(t, withLists(europe, blockList))
	r := withImage
	r.Image = laterImage
	code, out := g.run(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ~ service "+controllerName+": image "+laterImage+", environment DMT_VERSION\n",
		"  ~ service "+wakerName+": image "+laterImage+", environment DMT_VERSION\n")
	for _, name := range []string{controllerName, wakerName} {
		svc := g.s.RunServices["europe-west1/"+name]
		if svc.Env("DMT_IP_LISTS") != blockListEnv || svc.Template.Containers[0].Image != laterImage {
			t.Errorf("%s: %+v", name, svc.Template.Containers[0])
		}
	}
	if m := g.s.RunServices["europe-west1/"+wakerName].Template.Containers[0].Resources.Limits["memory"]; m != "512Mi" {
		t.Errorf("waker memory %q", m)
	}
	if rec := g.record(t); len(rec.IPLists) != 1 || rec.Image != laterImage {
		t.Errorf("record %+v", rec)
	}
}

func TestInstallWithoutEitherFlagChangesNothingOfTheLists(t *testing.T) {
	g := listsRouter(t)
	g.run(t, withLists(europe, blockList))
	mark := len(g.s.Calls())
	code, out := g.run(t, europe)
	if code != 0 || len(g.changes(mark)) != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.s.RunServices["europe-west1/"+wakerName].Env("DMT_IP_LISTS"); got != blockListEnv {
		t.Errorf("env %s", got)
	}
}

func TestInstallNoIPListsOnARouterWithNoneChangesNothing(t *testing.T) {
	g := listsRouter(t)
	mark := len(g.s.Calls())
	r := europe
	r.NoIPLists = true
	code, out := g.run(t, r)
	if code != 0 || !strings.Contains(out, "is up to date") || len(g.changes(mark)) != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// A router below 0.35.0 does not read DMT_IP_LISTS: on the recorded version, the flags are refused and name --image.
func TestInstallIPListFlagsOnARecordedRouterBelowTheirVersionAreRefused(t *testing.T) {
	noLists := europe
	noLists.NoIPLists = true
	for name, r := range map[string]Request{"--ip-list": withLists(europe, blockList), "--no-ip-lists": noLists} {
		t.Run(name, func(t *testing.T) {
			g := routerInstalled(t)
			mark := len(g.s.Calls())
			code, out := g.run(t, r)
			if code != 2 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			contains(t, out, name+": ", "--image", "0.34.0", "0.35.0", "Nothing was changed.")
			if c := g.changes(mark); len(c) != 0 {
				t.Fatalf("changes: %v", c)
			}
		})
	}
}

func TestInstallIPListFlagsWithAnImageBelowTheirVersionAreRefusedBeforeAnyCall(t *testing.T) {
	noLists := withImage
	noLists.NoIPLists = true
	for name, r := range map[string]Request{"--ip-list": withLists(withImage, blockList), "--no-ip-lists": noLists} {
		t.Run(name, func(t *testing.T) {
			g := newRouterRig(t)
			code, out := g.run(t, r)
			if code != 2 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			contains(t, out, name+": ", "--image", "0.34.0", "0.35.0", "Nothing was changed.")
			if c := g.s.Calls(); len(c) != 0 {
				t.Fatalf("calls: %v", c)
			}
		})
	}
}

func TestInstallRecordedIPListsWithAnImageBelowTheirVersionAreRefused(t *testing.T) {
	g := listsRouter(t)
	if code, out := g.run(t, withLists(europe, blockList)); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	mark := len(g.s.Calls())
	code, out := g.run(t, withImage)
	if code != 2 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "--image: ", "0.34.0", "0.35.0", "scanners", "--no-ip-lists", "Nothing was changed.")
	if c := g.changes(mark); len(c) != 0 {
		t.Fatalf("changes: %v", c)
	}
}

// A version that does not parse is not refused: what was read is said.
func TestInstallIPListsOnATagThatIsNoVersionSayWhatWasRead(t *testing.T) {
	const devImage = "europe-west1-docker.pkg.dev/dormeto-dev/dormeto/router:dev-4ca90e1"
	g := newRouterRig(t)
	g.s.Images[devImage] = true
	r := withLists(withImage, blockList)
	r.Image = devImage
	code, out := g.run(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, `  ! the router's version "dev-4ca90e1" is no release's: whether it reads IP lists (0.35.0 or later) is not checked`+"\n")
	code, out = g.run(t, withLists(europe, botsList))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, `  ! the router's version "dev-4ca90e1" is no release's`)
	if rec := g.record(t); len(rec.IPLists) != 1 || rec.IPLists[0].Name != "bots" {
		t.Errorf("record %+v", rec.IPLists)
	}
}

// A pre-release of 0.35.0 reads them: only a release's number below it is refused.
func TestInstallIPListsOnAPreReleaseOfTheirVersionAreNotRefused(t *testing.T) {
	const rcImage = "europe-west1-docker.pkg.dev/dormeto-dev/dormeto/router:0.35.0-rc.1"
	g := newRouterRig(t)
	g.s.Images[rcImage] = true
	r := withLists(withImage, blockList)
	r.Image = rcImage
	if code, out := g.run(t, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// The recorded image is checked as --image is: gone, the run stops before the APIs, the roles and the bindings.
func TestInstallIPListWithARecordedImageGoneStopsBeforeAnyChange(t *testing.T) {
	g := listsRouter(t)
	delete(g.s.Images, nextImage)
	mark := len(g.s.Calls())
	code, out := g.run(t, withLists(europe, blockList))
	if code != 1 || strings.Contains(out, "Plan for") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ image: "+nextImage+" is not in Artifact Registry\nNothing was changed.\n")
	if c := g.changes(mark); len(c) != 0 {
		t.Fatalf("changes: %v", c)
	}
}

func TestInstallIPListChecksTheRecordedImageOnce(t *testing.T) {
	g := listsRouter(t)
	mark := len(g.s.Calls())
	code, out := g.run(t, withLists(europe, blockList))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Checked: image "+nextImage+" is in Artifact Registry.\n")
	if heads := callsOf(g.s.Calls()[mark:], "HEAD"); len(heads) != 1 {
		t.Fatalf("%+v", heads)
	}
}

// The C2 line: the flags need install-router's permissions as --image does, on the recorded image too.
func TestInstallIPListWithoutInstallRouterPermissionsStopsBeforeAnyChange(t *testing.T) {
	g := listsRouter(t)
	g.s.HoldsAll = false
	g.s.Held = map[string]bool{}
	for _, p := range append(access.Needs("install"), access.Needs("install-router")...) {
		g.s.Held[p] = p != "run.services.update"
	}
	mark := len(g.s.Calls())
	code, out := g.run(t, withLists(europe, blockList))
	if code != 1 || strings.Contains(out, "Plan for") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Using the recorded image "+nextImage+".\n", "  ✗ install-router: missing run.services.update\nNothing was changed.\n")
	if c := g.changes(mark); len(c) != 0 {
		t.Fatalf("changes: %v", c)
	}
}

func TestInstallRefusesBadIPLists(t *testing.T) {
	for name, tc := range map[string]struct {
		lists []IPList
		no    bool
		want  string
	}{
		"s3":           {lists: []IPList{{Name: "a", URL: "s3://x/y", RefreshHours: 24}}, want: "http"},
		"upper scheme": {lists: []IPList{{Name: "a", URL: "HTTPS://example.com/l", RefreshHours: 24}}, want: "http"},
		"no host":      {lists: []IPList{{Name: "a", URL: "https://", RefreshHours: 24}}, want: "URL"},
		"credentials":  {lists: []IPList{{Name: "a", URL: "https://user:pass@example.com/l", RefreshHours: 24}}, want: "credentials"},
		"slash":        {lists: []IPList{{Name: "a/b", URL: "https://example.com", RefreshHours: 24}}, want: "name"},
		"percent":      {lists: []IPList{{Name: "a%2f", URL: "https://example.com", RefreshHours: 24}}, want: "name"},
		"upper":        {lists: []IPList{{Name: "A", URL: "https://example.com", RefreshHours: 24}}, want: "name"},
		"too long":     {lists: []IPList{{Name: strings.Repeat("a", 41), URL: "https://example.com", RefreshHours: 24}}, want: "name"},
		"refresh":      {lists: []IPList{{Name: "a", URL: "https://example.com", RefreshHours: 0}}, want: "refresh"},
		"duplicate":    {lists: []IPList{blockList, blockList}, want: "twice"},
		"both":         {lists: []IPList{blockList}, no: true, want: "--no-ip-lists"},
	} {
		t.Run(name, func(t *testing.T) {
			g := routerInstalled(t)
			mark := len(g.s.Calls())
			r := withLists(europe, tc.lists...)
			r.NoIPLists = tc.no
			code, out := g.run(t, r)
			if code != 2 || !strings.Contains(out, tc.want) || len(g.s.Calls()) != mark {
				t.Fatalf("exit %d (%d calls):\n%s", code, len(g.s.Calls())-mark, out)
			}
		})
	}
}

func TestParseIPList(t *testing.T) {
	l, err := ParseIPList("name=scanners,url=https://example.com/l.txt?a=1")
	if err != nil || l != (IPList{Name: "scanners", URL: "https://example.com/l.txt?a=1", RefreshHours: 24}) {
		t.Fatalf("%+v %v", l, err)
	}
	if l, err = ParseIPList("url=http://example.com/l,name=x,refresh=6"); err != nil || l.RefreshHours != 6 || l.Name != "x" {
		t.Fatalf("%+v %v", l, err)
	}
	for in, want := range map[string]string{
		"name=a,url=HTTP://example.com":               "http",
		"name=a,url=https://example.com,action=defer": "only ignore",
		"name=a":                  "url",
		"url=https://example.com": "name",
		"name=a,url=https://example.com,refresh=0":  "refresh",
		"name=a,url=https://example.com,refresh=x":  "refresh",
		"name=a,url=https://example.com,refresh=-3": "refresh",
		"name=a,url=https://example.com,color=red":  "color",
		"name=a,url=https://example.com,name=b":     "twice",
		"name=a,url=s3://x/y":                       "http",
		"name=a,url=https://u:p@example.com":        "credentials",
		"nonsense":                                  "name=",
	} {
		if _, err := ParseIPList(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", in, err, want)
		}
	}
}
