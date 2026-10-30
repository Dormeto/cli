package gcponboard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/gcp/gcptest"
)

func (g *installRig) status(t *testing.T, r Request) (int, string) {
	t.Helper()
	g.out.Reset()
	code := RunStatus(context.Background(), g.d, r)
	return code, g.out.String()
}

var europeStatus = Request{Project: "dormeto-dev", Region: "europe-west1"}

func TestStatusText(t *testing.T) {
	g := routerInstalled(t)
	code, out := g.status(t, europeStatus)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	want := "Project dormeto-dev (1034397096264)\n" +
		"Router dmtr-europe-west1, region europe-west1: version 0.34.0, image " + routerImage + "\n" +
		"  installed 2026-10-29T10:00:00Z, updated 2026-10-29T10:00:00Z by me@example.com with the CLI 0.8.0\n" +
		"  ✓ service " + controllerName + ": ready, image " + routerImage + ", " + controllerURL + "\n" +
		"  ✓ service " + wakerName + ": ready, image " + routerImage + "\n" +
		"  ✓ health " + controllerURL + ": version 0.34.0\n" +
		"  ✓ network endpoint group " + wakerName + ": serverless, Cloud Run service " + wakerName + "\n" +
		"  ✓ queue dmtr-europe-west1: RUNNING\n" +
		"  ✓ job " + jobName + ": ENABLED, */5 * * * * Etc/UTC\n" +
		"  ✓ database dmtr-europe-west1: FIRESTORE_NATIVE, europe-west1\n" +
		"  ✓ grant projects/dormeto-dev/roles/dormetoRouter to " + routerMember + " on the project\n" +
		"  ✓ grant projects/dormeto-dev/roles/dormetoRouterBackends to " + routerMember + " on the project, if \"Dormeto-managed resources\": resource.matchTag('dormeto-dev/dormeto', 'managed')\n" +
		"  ✓ grant roles/datastore.user to " + routerMember + " on the project, if \"The router's database\": resource.name == \"projects/dormeto-dev/databases/dmtr-europe-west1\"\n" +
		"  ✓ grant roles/cloudtasks.enqueuer to " + routerMember + " on queue dmtr-europe-west1\n" +
		"  ✓ grant roles/iam.serviceAccountUser to " + routerMember + " on service account " + routerSA + "\n" +
		"  ✓ grant roles/run.invoker to " + routerMember + " on controller " + controllerName + "\n" +
		"  Google added: roles/run.serviceAgent to " + runAgent + " (after apis)\n" +
		"  Google added: roles/firestore.serviceAgent to " + firestoreAgent + " (after database)\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestStatusChangesNothing(t *testing.T) {
	g := routerInstalled(t)
	before := len(g.s.Mutations())
	if code, out := g.status(t, europeStatus); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if after := len(g.s.Mutations()); after != before {
		t.Fatalf("%d mutating calls", after-before)
	}
}

func TestStatusJSON(t *testing.T) {
	g := routerInstalled(t)
	r := europeStatus
	r.JSON = true
	code, out := g.status(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var rep StatusReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if rep.Project.ID != "dormeto-dev" || rep.Project.Number != "1034397096264" || len(rep.Routers) != 1 {
		t.Fatalf("%+v", rep)
	}
	router := rep.Routers[0]
	if router.Router != "dmtr-europe-west1" || router.Region != "europe-west1" || !router.OK || router.Version != "0.34.0" ||
		router.Record == nil || router.Record.Image != routerImage || router.Record.RouterVersion != "0.34.0" {
		t.Fatalf("%+v", router)
	}
	kinds := map[string]int{}
	for _, res := range router.Resources {
		kinds[res.Kind]++
		if !res.OK {
			t.Errorf("%+v", res)
		}
	}
	if kinds["service"] != 2 || kinds["health"] != 1 || kinds["neg"] != 1 || kinds["queue"] != 1 || kinds["job"] != 1 || kinds["database"] != 1 {
		t.Errorf("%+v", router.Resources)
	}
	if router.Resources[0] != (ResourceStatus{Kind: "service", Name: controllerName, OK: true, State: "ready", Detail: "image " + routerImage + ", " + controllerURL}) {
		t.Errorf("%+v", router.Resources[0])
	}
	if len(router.Grants) != 6 {
		t.Fatalf("%+v", router.Grants)
	}
	for _, g := range router.Grants {
		if !g.Present {
			t.Errorf("%+v", g)
		}
	}
	// The JSON's own words.
	var raw struct {
		Routers []struct {
			Grants []map[string]any `json:"grants"`
		} `json:"routers"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if first := raw.Routers[0].Grants[0]; first["role"] == nil || first["member"] == nil || first["on"] != "project" || first["present"] != true {
		t.Fatalf("%+v", first)
	}
}

func TestStatusOfTheAccessAlone(t *testing.T) {
	g := newInstallRig(t)
	if code, out := g.run(t, europe); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	code, out := g.status(t, europeStatus)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Router dmtr-europe-west1, region europe-west1: the access only, no image\n", "  ✓ queue dmtr-europe-west1: RUNNING\n")
	for _, not := range []string{"service dmtr", "health", "network endpoint group", "job"} {
		if strings.Contains(out, not) {
			t.Errorf("%q in:\n%s", not, out)
		}
	}
}

func TestStatusSaysWhatIsWrongAndExits1(t *testing.T) {
	g := routerInstalled(t)
	g.s.Lock()
	g.s.RunServices["europe-west1/"+wakerName].TerminalCondition = gcptest.RunCondition{Type: "Ready", State: "CONDITION_FAILED", Message: "the revision crashed"}
	g.s.RunServices["europe-west1/"+controllerName].Template.Containers[0].Image = nextImage
	g.s.HealthVersion = "0.33.0"
	delete(g.s.NEGs, "europe-west1/"+wakerName)
	g.s.Jobs["europe-west1/"+jobName].State = "PAUSED"
	delete(g.s.Policies, "projects/dormeto-dev/locations/europe-west1/queues/dmtr-europe-west1")
	g.s.Unlock()
	code, out := g.status(t, europeStatus)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out,
		"  ✗ service "+controllerName+": ready, image "+nextImage+", "+controllerURL+": not the recorded image\n",
		"  ✗ service "+wakerName+": not ready (the revision crashed), image "+routerImage+"\n",
		"  ✗ health "+controllerURL+": version 0.33.0, not the recorded 0.34.0\n",
		"  ✗ network endpoint group "+wakerName+": missing\n",
		"  ✗ job "+jobName+": PAUSED, */5 * * * * Etc/UTC\n",
		"  ✗ grant roles/cloudtasks.enqueuer to "+routerMember+" on queue dmtr-europe-west1\n",
		"  ✓ grant roles/run.invoker to ")
}

func TestStatusSaysWhatItCouldNotRead(t *testing.T) {
	g := routerInstalled(t)
	refuse := func(host, path string) {
		g.s.Refuse(gcptest.Refusal{Host: host, Path: path, Code: 403, Status: "PERMISSION_DENIED", Message: "denied"})
	}
	refuse("cloudscheduler.googleapis.com", "/v1/projects/dormeto-dev/locations/europe-west1/jobs/"+jobName)
	refuse("cloudtasks.googleapis.com", "/v2/projects/dormeto-dev/locations/europe-west1/queues/dmtr-europe-west1:getIamPolicy")
	code, out := g.status(t, europeStatus)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ job "+jobName+": unreadable: google: 403 PERMISSION_DENIED: denied\n",
		"  ✗ grant roles/cloudtasks.enqueuer to "+routerMember+" on queue dmtr-europe-west1: google: 403 PERMISSION_DENIED: denied\n")
}

func TestStatusOfARecordThatCannotBeRead(t *testing.T) {
	g := routerInstalled(t)
	g.s.Lock()
	g.s.Documents[recordDB+"/install/record"] = map[string]any{"data": map[string]any{"stringValue": "not JSON"}}
	g.s.Unlock()
	code, out := g.status(t, europeStatus)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "not installed (no install record)\n", "  ✗ record install/record: unreadable: ")
}

func TestStatusOfARegionWithoutARouter(t *testing.T) {
	g := newInstallRig(t)
	code, out := g.status(t, europeStatus)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Router dmtr-europe-west1, region europe-west1: not installed (no install record)\n", "  ✗ database dmtr-europe-west1: missing (its API is off)\n")
	if strings.Contains(out, "service dmtr") {
		t.Fatal(out)
	}
}

func TestStatusWithoutARegionFindsTheRouters(t *testing.T) {
	g := routerInstalled(t)
	west4 := Request{Project: "dormeto-dev", Region: "europe-west4", Yes: true}
	if code, out := g.run(t, west4); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.s.Lock()
	g.s.Databases["(default)"] = &gcptest.Database{Type: "FIRESTORE_NATIVE", LocationID: "europe-west1"}
	g.s.Unlock()
	code, out := g.status(t, Request{Project: "dormeto-dev"})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Router dmtr-europe-west1, region europe-west1: version 0.34.0", "Router dmtr-europe-west4, region europe-west4: the access only, no image\n")
	if strings.Contains(out, "(default)") {
		t.Fatal(out)
	}
}

func TestStatusWithoutARouterInTheProject(t *testing.T) {
	g := newInstallRig(t)
	code, out := g.status(t, Request{Project: "dormeto-dev"})
	if code != 1 || out != "Project dormeto-dev (1034397096264)\nNo Dormeto router in project dormeto-dev.\n" {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	r := Request{Project: "dormeto-dev", JSON: true}
	code, out = g.status(t, r)
	if code != 1 || out != `{"project":{"id":"dormeto-dev","number":"1034397096264","parent":""},"routers":[]}`+"\n" {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestStatusOfAProjectNotVisibleOrABadRegion(t *testing.T) {
	g := newInstallRig(t)
	g.s.NoRole = true
	code, out := g.status(t, europeStatus)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Project dormeto-dev is not visible to me@example.com: no role in it\n")

	code, out = g.status(t, Request{Project: "dormeto-dev", Region: "europe"})
	if code != 2 || !strings.Contains(out, "--region") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
