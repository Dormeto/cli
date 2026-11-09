package access

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestLoadEmbeddedFileIsValid(t *testing.T) {
	m := Load()
	if m.Version != 1 || len(m.APIs) != 10 || len(m.Roles) != 2 {
		t.Fatalf("manifest: %+v", m)
	}
}

func TestCommandsInManifestOrder(t *testing.T) {
	want := []string{"check", "install", "install-router", "uninstall", "workloads", "onboard", "offboard", "connect", "disconnect"}
	if got := Commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestStepsFollowRuling1(t *testing.T) {
	want := map[string][]string{
		"check":          {"read"},
		"install":        {"read", "apis", "roles", "serviceAccount", "projectBindings", "tag", "database", "queue", "logView"},
		"install-router": {"image", "cloudRun", "neg", "job"},
		"uninstall":      {"read", "job", "neg", "cloudRun", "logView", "queue", "database", "projectBindings", "tag", "serviceAccount", "roles"},
		"workloads":      {"invoke"},
		"onboard":        {"invoke", "tag", "requestLogs"},
		"offboard":       {"invoke", "tag", "requestLogs"},
		"connect": {"read", "apis", "federation", "serviceAccounts", "roles", "projectBindings", "folderBindings", "organizationBindings",
			"controller", "tag"},
		"disconnect": {"read", "controller", "tag", "projectBindings", "folderBindings", "organizationBindings", "roles", "serviceAccounts",
			"federation"},
	}
	for c, steps := range want {
		if got := Steps(c); !reflect.DeepEqual(got, steps) {
			t.Errorf("%s: got %v want %v", c, got, steps)
		}
	}
	if Steps("nope") != nil || Needs("nope") != nil {
		t.Error("unknown command")
	}
}

func TestNeedsIsSortedUniqueUnion(t *testing.T) {
	n := Needs("install")
	if !slices.IsSorted(n) || len(slices.Compact(slices.Clone(n))) != len(n) {
		t.Fatalf("not sorted/unique: %v", n)
	}
	for _, p := range []string{"resourcemanager.projects.setIamPolicy", "iam.roles.create", "cloudtasks.queues.create"} {
		if !slices.Contains(n, p) {
			t.Errorf("missing %s", p)
		}
	}
	if slices.Contains(Needs("check"), "resourcemanager.projects.setIamPolicy") {
		t.Error("check needs setIamPolicy")
	}
	if len(Needs("check")) != 4 {
		t.Errorf("check: %v", Needs("check"))
	}
}

func TestFeaturesAreCheckedAndAddTheirOperatorPermissions(t *testing.T) {
	if got := FeatureNames(); !reflect.DeepEqual(got, []string{"cloud-sql", "gke"}) {
		t.Fatalf("got %v", got)
	}
	if CheckFeatures([]string{"gke"}) != nil || CheckFeatures([]string{"gke", "nope"}) == nil {
		t.Error("CheckFeatures")
	}
	if slices.Contains(Needs("onboard"), "cloudsql.instances.createTagBinding") {
		t.Error("the base onboard needs a feature's permission")
	}
	if !slices.Contains(NeedsWith("onboard", []string{"cloud-sql"}), "cloudsql.instances.createTagBinding") ||
		slices.Contains(NeedsWith("onboard", []string{"cloud-sql"}), "container.roles.escalate") {
		t.Error("NeedsWith")
	}
	if !reflect.DeepEqual(NeedsWith("install", []string{"cloud-sql", "gke"}), Needs("install")) {
		t.Error("the features add nothing to install")
	}
}

// install-router's image step is for a router image of the customer's own only: its permissions can be left out, and a
// permission another step needs too stays.
func TestNeedsWithoutLeavesOutTheStepsSkipped(t *testing.T) {
	image := Load().perms["install-router"]["image"]
	without := NeedsWithout("install-router", nil, "image")
	if len(image) == 0 || len(without) != len(NeedsWith("install-router", nil))-len(image) ||
		slices.ContainsFunc(without, func(p string) bool { return slices.Contains(image, p) }) {
		t.Fatalf("%v without %v", without, image)
	}
	if !slices.Contains(NeedsWithout("install-router", nil, "job"), "iam.serviceAccounts.actAs") {
		t.Fatal("cloudRun's iam.serviceAccounts.actAs left out with job's")
	}
	if !slices.Equal(NeedsWithout("install", []string{"gke"}, "image"), NeedsWith("install", []string{"gke"})) {
		t.Fatal("a step the command does not have changed its permissions")
	}
	if NeedsWithout("nope", nil) != nil {
		t.Fatal("an unknown command needs something")
	}
}

// The manager's section (Google Cloud manager design §4.4): the host's APIs, the federation, the two accounts, the two roles,
// their grants and the impersonation, as `dormeto gcp connect` reads them.
func TestManagerSectionIsLoaded(t *testing.T) {
	m := Load().Manager
	if !reflect.DeepEqual(m.APIs, []string{"iam.googleapis.com", "iamcredentials.googleapis.com", "sts.googleapis.com",
		"cloudresourcemanager.googleapis.com", "cloudbilling.googleapis.com"}) {
		t.Fatalf("apis %v", m.APIs)
	}
	f := m.Federation
	if f.Pool != "dmt-{env}" || f.Provider != "aws" ||
		f.Condition != "assertion.arn.startsWith('arn:aws:sts::{account}:assumed-role/dmt-{env}-manager-')" {
		t.Fatalf("federation %+v", f)
	}
	// The mapping in the manifest's order: google.subject first.
	if !reflect.DeepEqual(f.Mapping, []Attribute{{"google.subject", "assertion.arn"},
		{"attribute.aws_role", "assertion.arn.extract('assumed-role/{role}/')"}}) {
		t.Fatalf("mapping %+v", f.Mapping)
	}
	if m.ServiceAccounts["discovery"] != "dmt-{env}-discovery" || m.ServiceAccounts["command"] != "dmt-{env}-command" {
		t.Fatalf("accounts %v", m.ServiceAccounts)
	}
	d, c := m.Roles["dormetoDiscovery"], m.Roles["dormetoCommand"]
	if d.Title != "Dormeto manager discovery" || len(d.Permissions) != 32 ||
		!reflect.DeepEqual(d.OrganizationPermissions, []string{"resourcemanager.folders.get", "resourcemanager.folders.list", "resourcemanager.organizations.get"}) {
		t.Fatalf("dormetoDiscovery %+v", d)
	}
	if c.Title != "Dormeto manager command" || len(c.Permissions) != 10 || len(c.OrganizationPermissions) != 0 {
		t.Fatalf("dormetoCommand %+v", c)
	}
	if !reflect.DeepEqual(m.Grants.Scopes, []ManagerGrant{{"discovery", "dormetoDiscovery", "scope"}}) ||
		!reflect.DeepEqual(m.Grants.RouterProjects, []ManagerGrant{{"command", "roles/run.invoker", "controller"},
			{"command", "roles/resourcemanager.tagUser", "tagValue"}, {"command", "dormetoCommand", "project"}}) {
		t.Fatalf("grants %+v", m.Grants)
	}
	i := m.Impersonation
	if i.Role != "roles/iam.workloadIdentityUser" ||
		i.PrincipalSet != "principalSet://iam.googleapis.com/projects/{hostNumber}/locations/global/workloadIdentityPools/dmt-{env}/attribute.aws_role/{awsRole}" ||
		!reflect.DeepEqual(i.Roles["discovery"], []string{"dmt-{env}-manager-discovery", "dmt-{env}-manager-api", "dmt-{env}-manager-savings"}) ||
		!reflect.DeepEqual(i.Roles["command"], []string{"dmt-{env}-manager-operations", "dmt-{env}-manager-api", "dmt-{env}-manager-fleet"}) {
		t.Fatalf("impersonation %+v", i)
	}
}

// connect and disconnect test each scope for its kind's steps only: StepNeeds are the permissions of the steps named.
func TestStepNeedsAreTheNamedStepsPermissions(t *testing.T) {
	if got := StepNeeds("connect", "folderBindings"); !reflect.DeepEqual(got, []string{"resourcemanager.folders.getIamPolicy", "resourcemanager.folders.setIamPolicy"}) {
		t.Fatalf("folderBindings %v", got)
	}
	got := StepNeeds("connect", "read", "projectBindings")
	if !slices.IsSorted(got) || !slices.Contains(got, "run.services.list") || !slices.Contains(got, "resourcemanager.projects.setIamPolicy") ||
		len(slices.Compact(slices.Clone(got))) != len(got) || slices.ContainsFunc(got, func(p string) bool { return strings.HasPrefix(p, "resourcemanager.folders.") }) {
		t.Fatalf("read and projectBindings %v", got)
	}
	if StepNeeds("nope", "read") != nil || len(StepNeeds("connect", "nope")) != 0 {
		t.Fatal("an unknown command or step needs something")
	}
}
