package gcponboard

import (
	"slices"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/gcp/access"
)

const (
	sqlAgent  = "serviceAccount:service-1034397096264@gcp-sa-cloud-sql.iam.gserviceaccount.com"
	gkeAgent  = "serviceAccount:service-1034397096264@container-engine-robot.iam.gserviceaccount.com"
	sqlTagIf  = `if "Dormeto-managed resources": resource.matchTag('dormeto-dev/dormeto', 'managed')`
	sqlPlan   = "projects/dormeto-dev/roles/dormetoRouterDatabases to " + routerMember + " on the project, " + sqlTagIf
	withSQL   = "sqladmin.googleapis.com"
	withKube  = "container.googleapis.com"
	sqlReadID = "dormetoRouterDatabasesRead"
)

func withFeatures(names ...string) Request {
	r := europe
	r.With = names
	return r
}

func TestInstallWithoutFeaturesNamesNeitherAPI(t *testing.T) {
	g := newInstallRig(t)
	r := withFeatures()
	r.DryRun = true
	code, out := g.run(t, r)
	if code != 0 || strings.Contains(out, withSQL) || strings.Contains(out, withKube) || strings.Contains(out, "dormetoRouterDatabases") ||
		strings.Contains(out, "dormetoRouterKubernetes") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestInstallWithCloudSQLPlansItsAPIRolesAndGrants(t *testing.T) {
	g := newInstallRig(t)
	r := withFeatures("cloud-sql")
	r.DryRun = true
	code, out := g.run(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  + API sqladmin.googleapis.com\n",
		`  + role dormetoRouterDatabasesRead "Dormeto router databases (read)" (1 permissions)`+"\n",
		`  + role dormetoRouterDatabases "Dormeto router databases" (1 permissions)`+"\n",
		"  + grant projects/dormeto-dev/roles/dormetoRouterDatabasesRead to "+routerMember+" on the project\n",
		"  + grant "+sqlPlan+"\n")
	if strings.Contains(out, withKube) || strings.Contains(out, "dormetoRouterKubernetes") {
		t.Fatal(out)
	}
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
}

func TestInstallWithCloudSQLAppliesAndRecordsGooglesBinding(t *testing.T) {
	g := newInstallRig(t)
	g.s.GrantOnEnable = map[string][2]string{withSQL: {"roles/cloudsql.serviceAgent", sqlAgent}}
	code, out := g.run(t, withFeatures("cloud-sql"))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "      IAM + roles/cloudsql.serviceAgent to "+sqlAgent+" on the project (Google, after apis)\n",
		"      IAM + "+sqlPlan+" (Dormeto)\n", "created dormetoRouterDatabasesRead")
	rec := g.record(t)
	if !slices.Equal(rec.Features, []string{"cloud-sql"}) || !listsResource(rec, "api", withSQL) ||
		!listsResource(rec, "role", "projects/dormeto-dev/roles/"+sqlReadID) {
		t.Fatalf("record %+v", rec)
	}
	if !slices.Contains(rec.Google, GoogleGrant{Role: "roles/cloudsql.serviceAgent", Member: sqlAgent, Step: "apis"}) {
		t.Fatalf("google: %+v", rec.Google)
	}
	role := g.s.Roles["dormetoRouterDatabases"]
	if role == nil || !slices.Equal(role.Permissions, []string{"cloudsql.instances.update"}) {
		t.Fatalf("role %+v", role)
	}
	p := g.projectPolicy(t)
	if !has(p, Grant{Role: "projects/dormeto-dev/roles/dormetoRouterDatabases", Member: routerMember,
		Condition: &Condition{Title: "Dormeto-managed resources", Expression: "resource.matchTag('dormeto-dev/dormeto', 'managed')"}}) {
		t.Fatalf("policy %+v", p)
	}
}

func TestInstallWithoutFeaturesAfterwardsIsUpToDateAndKeepsTheRecords(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, withFeatures("cloud-sql", "gke")); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	before := len(g.s.Mutations())
	code, out := g.run(t, europe)
	if code != 0 || !strings.Contains(out, "is up to date.\n") || strings.Contains(out, "Plan for") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if len(g.s.Mutations()) != before {
		t.Fatal("mutating calls")
	}
	if rec := g.record(t); !slices.Equal(rec.Features, []string{"cloud-sql", "gke"}) {
		t.Fatalf("features %v", rec.Features)
	}
}

func TestInstallAFeatureAddedLaterAddsOnlyIt(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, withFeatures("cloud-sql")); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	r := withFeatures("gke")
	r.DryRun = true
	code, out := g.run(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  + API container.googleapis.com\n", "  + role dormetoRouterKubernetes")
	if strings.Contains(out, "sqladmin") || strings.Contains(out, "dormetoRouterDatabases") {
		t.Fatal(out)
	}
	r.DryRun = false
	if code, out := g.run(t, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if rec := g.record(t); !slices.Equal(rec.Features, []string{"cloud-sql", "gke"}) {
		t.Fatalf("features %v", rec.Features)
	}
}

func TestInstallRecordedFeatureWhoseAPIIsOffComesBack(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, withFeatures("gke")); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.s.Lock()
	g.s.Enabled = slices.DeleteFunc(g.s.Enabled, func(a string) bool { return a == withKube })
	g.s.Unlock()
	code, out := g.run(t, europe)
	if code != 0 || !strings.Contains(out, "  + API container.googleapis.com\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestInstallAnUnknownFeatureExits2(t *testing.T) {
	g := newInstallRig(t)
	code, out := g.run(t, withFeatures("cloud-sql", "nope"))
	if code != 2 || !strings.Contains(out, `--with: "nope" is not a feature`) || !strings.Contains(out, "cloud-sql, gke") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if len(g.s.Calls()) != 0 {
		t.Fatalf("calls: %+v", g.s.Calls())
	}
}

func TestInstallWithAFeatureMissingAPermissionChangesNothing(t *testing.T) {
	g := newInstallRig(t)
	g.s.HoldsAll = false
	g.s.Held = map[string]bool{}
	for _, p := range access.Needs("install") {
		g.s.Held[p] = p != "serviceusage.services.enable"
	}
	code, out := g.run(t, withFeatures("cloud-sql"))
	if code != 1 || !strings.Contains(out, "  ✗ install: missing serviceusage.services.enable\n") || strings.Contains(out, "Plan for") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if m := g.s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
}

func TestUninstallRemovesTheFeaturesRolesAndGrantsAndListsTheirAPIsAsLeft(t *testing.T) {
	g := newInstallRig(t)
	g.s.GrantOnEnable = map[string][2]string{withSQL: {"roles/cloudsql.serviceAgent", sqlAgent}, withKube: {"roles/container.serviceAgent", gkeAgent}}
	before := g.policyGrants(t)
	if code, out := g.run(t, withFeatures("cloud-sql", "gke")); code != 0 {
		t.Fatalf("install: exit %d:\n%s", code, out)
	}
	rec := g.record(t)
	code, out := g.uninstall(t, uninstallEurope)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "- grant "+sqlPlan+"\n", "- role dormetoRouterDatabases\n", "- role dormetoRouterKubernetes\n",
		"Left in place:\n", "  API sqladmin.googleapis.com\n", "  API container.googleapis.com\n",
		"roles/cloudsql.serviceAgent to "+sqlAgent+" on the project (added by Google)\n",
		"roles/container.serviceAgent to "+gkeAgent+" on the project (added by Google)\n")
	for _, id := range []string{"dormetoRouterDatabasesRead", "dormetoRouterDatabases", "dormetoRouterKubernetes"} {
		if r := g.s.Roles[id]; r != nil && !r.Deleted {
			t.Errorf("role %s left", id)
		}
	}
	want := slices.Clone(before)
	for _, x := range rec.Google {
		want = append(want, grantKey(Grant{Role: x.Role, Member: x.Member}))
	}
	for _, k := range g.policyGrants(t) {
		if !slices.Contains(want, k) {
			t.Errorf("grant left: %s", k)
		}
	}
}

func TestUninstallAnUnknownFeatureExits2(t *testing.T) {
	g := newInstallRig(t)
	r := uninstallEurope
	r.With = []string{"nope"}
	if code, out := g.uninstall(t, r); code != 2 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestCheckWithFeaturesTestsTheirPermissionsAndAPIs(t *testing.T) {
	s := newInstallRig(t).s
	s.Enabled = access.Load().APIs
	s.HoldsAll = false
	s.Held = map[string]bool{}
	for _, c := range access.Commands() {
		for _, p := range access.Needs(c) {
			s.Held[p] = true
		}
	}
	d, _ := deps(s)
	rep, err := Check(t.Context(), d, Request{Project: "dormeto-dev"})
	if err != nil || !rep.OK() || len(rep.APIs.Missing) != 0 {
		t.Fatalf("%v %+v", err, rep)
	}
	rep, err = Check(t.Context(), d, Request{Project: "dormeto-dev", With: []string{"cloud-sql", "gke"}})
	if err != nil {
		t.Fatal(err)
	}
	if on := command(t, rep, "onboard"); on.OK() || !slices.Contains(on.Missing, "cloudsql.instances.createTagBinding") ||
		!slices.Contains(on.Missing, "container.roles.escalate") {
		t.Fatalf("onboard %+v", on)
	}
	if !slices.Equal(rep.APIs.Missing, []string{withSQL, withKube}) {
		t.Fatalf("apis %v", rep.APIs.Missing)
	}
	if _, err := Check(t.Context(), d, Request{Project: "dormeto-dev", With: []string{"nope"}}); err == nil {
		t.Fatal("unknown feature")
	}
}

func TestCheckTestsTheRecordsFeatures(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, withFeatures("gke")); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.s.Lock()
	g.s.Enabled = slices.DeleteFunc(g.s.Enabled, func(a string) bool { return a == withKube })
	g.s.Unlock()
	rep, err := Check(t.Context(), g.d, Request{Project: "dormeto-dev", Region: "europe-west1"})
	if err != nil || !slices.Equal(rep.APIs.Missing, []string{withKube}) {
		t.Fatalf("%v %+v", err, rep.APIs)
	}
	rep, err = Check(t.Context(), g.d, Request{Project: "dormeto-dev"})
	if err != nil || len(rep.APIs.Missing) != 0 {
		t.Fatalf("without a region: %v %+v", err, rep.APIs)
	}
}

func TestInstallPutsBackTheControllersOldSettings(t *testing.T) {
	g := routerInstalled(t)
	g.s.Lock()
	c := g.s.RunServices["europe-west1/"+controllerName]
	if c.Template.Scaling.MaxInstanceCount != 10 || c.Template.Timeout != "1800s" {
		t.Fatalf("%+v", c.Template)
	}
	c.Template.Scaling.MaxInstanceCount, c.Template.Timeout = 3, "900s"
	g.s.Unlock()
	code, out := g.run(t, withImage)
	if code != 0 || !strings.Contains(out, "0 to 10 instances") || !strings.Contains(out, "timeout 1800 s") || !strings.Contains(out, "  ~ service "+controllerName) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
