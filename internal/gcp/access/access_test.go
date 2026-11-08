package access

import (
	"reflect"
	"slices"
	"testing"
)

func TestLoadEmbeddedFileIsValid(t *testing.T) {
	m := Load()
	if m.Version != 1 || len(m.APIs) != 10 || len(m.Roles) != 2 {
		t.Fatalf("manifest: %+v", m)
	}
}

func TestCommandsInManifestOrder(t *testing.T) {
	want := []string{"check", "install", "install-router", "uninstall", "workloads", "onboard", "offboard"}
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
