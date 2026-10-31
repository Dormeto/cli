package gcponboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/gcp/gcptest"
	"github.com/Dormeto/cli/internal/install"
)

// atPrompt is what a person answers: yes, after the rest of the world did something while the prompt waited.
type atPrompt struct {
	meanwhile func()
	asked     bool
}

func (a *atPrompt) Read(p []byte) (int, error) {
	if a.asked {
		return 0, io.EOF
	}
	a.asked = true
	a.meanwhile()
	return copy(p, "y\n"), nil
}

// waitingAtItsPrompt makes the rig's next command wait at its prompt while meanwhile runs, and asks.
func (g *rbacRig) waitingAtItsPrompt(meanwhile func()) {
	g.d.Terminal = true
	g.d.Prompter = install.NewPrompter(&atPrompt{meanwhile: meanwhile}, g.out, true)
}

// another runs a command of another person's: its own output, the same project. It runs on the prompt's goroutine: a
// failure is not fatal there.
func (g *rbacRig) another(t *testing.T, f func(context.Context, Deps, Request) int, r Request) {
	t.Helper()
	var out bytes.Buffer
	d := g.d
	d.Out, d.Err, d.Terminal = &out, &out, false
	if code := f(context.Background(), d, r); code != 0 {
		t.Errorf("exit %d:\n%s", code, out.String())
	}
}

var (
	blogAccess = Request{Project: "dormeto-dev", Region: "europe-west1", GKECluster: "europe-west1-b/shop-cluster", Namespace: "shop", ID: "blog", Yes: true}
	blogEntry  = RecordRbac{Cluster: "europe-west1-b/shop-cluster", Namespace: "shop", Workload: "blog"}
)

func asking(r Request) Request {
	r.Yes = false
	return r
}

// A grant reads the record, waits at its prompt, then writes: another's write meanwhile is not lost, nor its own.
func TestRecordAGrantAtItsPromptLosesNoWriteMadeMeanwhile(t *testing.T) {
	g := newRbacRig(t)
	g.waitingAtItsPrompt(func() { g.another(t, WorkloadsAccessGrant, blogAccess) })
	code, out := g.call(t, WorkloadsAccessGrant, asking(shopAccess))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.rbacOf(t); !slices.Contains(got, shopEntry) || !slices.Contains(got, blogEntry) || len(got) != 2 {
		t.Fatalf("record: %v", got)
	}
}

// The record's other fields are not put back by a grant that read them before.
func TestRecordAGrantKeepsWhatAnInstallWroteMeanwhile(t *testing.T) {
	g := newRbacRig(t)
	g.waitingAtItsPrompt(func() {
		r := withImage
		r.Image = nextImage
		g.another(t, Install, r)
	})
	code, out := g.call(t, WorkloadsAccessGrant, asking(shopAccess))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	rec := g.record(t)
	if rec.Image != nextImage || rec.RouterVersion != "0.35.0" || !slices.Equal(rec.Rbac, []RecordRbac{shopEntry}) {
		t.Fatalf("record: %+v", rec)
	}
}

// An install is the other side: it read the record at its start, its writes keep an entry added since.
func TestRecordAnInstallAtItsPromptKeepsAnAccessGrantedMeanwhile(t *testing.T) {
	g := newRbacRig(t)
	g.waitingAtItsPrompt(func() { g.another(t, WorkloadsAccessGrant, shopAccess) })
	r := withImage
	r.Image, r.Yes = nextImage, false
	code, out := g.run(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	rec := g.record(t)
	if rec.Image != nextImage || !slices.Equal(rec.Rbac, []RecordRbac{shopEntry}) {
		t.Fatalf("record: %+v", rec)
	}
}

// A revoke meanwhile stays one: the grant does not write back the entry it read.
func TestRecordAGrantKeepsARevokeMadeMeanwhile(t *testing.T) {
	g := newRbacRig(t)
	g.another(t, WorkloadsAccessGrant, blogAccess)
	g.waitingAtItsPrompt(func() { g.another(t, WorkloadsAccessRevoke, blogAccess) })
	code, out := g.call(t, WorkloadsAccessGrant, asking(shopAccess))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.rbacOf(t); !slices.Equal(got, []RecordRbac{shopEntry}) {
		t.Fatalf("record: %v", got)
	}
}

// The same workload revoked at the prompt, then granted: its entry is listed again, as its access is about to exist.
func TestRecordAGrantListsAgainAnEntryRevokedMeanwhile(t *testing.T) {
	g := newRbacRig(t)
	g.another(t, WorkloadsAccessGrant, shopAccess)
	g.k.mu.Lock()
	delete(g.k.objects, shopRole)
	g.k.mu.Unlock()
	g.waitingAtItsPrompt(func() { g.another(t, WorkloadsAccessRevoke, shopAccess) })
	code, out := g.call(t, WorkloadsAccessGrant, asking(shopAccess))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.rbacOf(t); !slices.Equal(got, []RecordRbac{shopEntry}) {
		t.Fatalf("record: %v", got)
	}
}

// A first record meets one written meanwhile: neither is lost.
func TestRecordAFirstWriteKeepsARecordWrittenMeanwhile(t *testing.T) {
	g := newRbacRig(t)
	var rec Record
	if err := json.Unmarshal([]byte(g.s.Field(recordDB, "install/record", "data")), &rec); err != nil {
		t.Fatal(err)
	}
	rec.Rbac = []RecordRbac{shopEntry}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	delete(g.s.Documents, recordDB+"/install/record")
	g.waitingAtItsPrompt(func() {
		g.s.Lock()
		defer g.s.Unlock()
		g.s.Documents[recordDB+"/install/record"] = map[string]any{"data": map[string]any{"stringValue": string(b)}}
	})
	r := withImage
	r.Yes = false
	code, out := g.run(t, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := g.record(t); !slices.Equal(got.Rbac, []RecordRbac{shopEntry}) || got.Image != routerImage {
		t.Fatalf("record: %+v", got)
	}
}

func recordPatches(calls []gcptest.Call) []gcptest.Call {
	var out []gcptest.Call
	for _, c := range callsOf(calls, http.MethodPatch) {
		if strings.HasSuffix(c.Path, "/documents/install/record") {
			out = append(out, c)
		}
	}
	return out
}

// Each write of the record names the version it read; the first, that there is none.
func TestRecordEachWriteNamesTheVersionItRead(t *testing.T) {
	g := newRouterRig(t)
	if code, out := g.run(t, withImage); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	patches := recordPatches(g.s.Calls())
	if len(patches) < 2 || !strings.HasSuffix(patches[0].URL, "?currentDocument.exists=false") {
		t.Fatalf("%+v", patches)
	}
	for _, p := range patches[1:] {
		if !strings.Contains(p.URL, "?currentDocument.updateTime=") {
			t.Errorf("%s", p.URL)
		}
	}
}

// Conflicts have an end: the run says so, and does not go round for ever.
func TestRecordAConflictThatDoesNotEndIsSaid(t *testing.T) {
	g := newRbacRig(t)
	mark := len(g.s.Calls())
	g.s.Route("firestore.googleapis.com", "/v1/projects/dormeto-dev/databases/dmtr-europe-west1/documents/install/record", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			gcptest.Fail(w, 400, "FAILED_PRECONDITION", "the stored version does not match the required base version")
			return
		}
		g.s.Lock()
		fields := g.s.Documents[recordDB+"/install/record"]
		g.s.Unlock()
		gcptest.Reply(w, map[string]any{"fields": fields, "updateTime": "2026-11-01T10:00:00.000001Z"})
	})
	code, out := g.call(t, WorkloadsAccessGrant, shopAccess)
	if code != 1 || !strings.Contains(out, "does not match the required base version") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := recordPatches(g.s.Calls()[mark:]); len(got) != recordTries {
		t.Fatalf("%d writes", len(got))
	}
	if w := g.k.writes(0); len(w) != 0 {
		t.Errorf("applied with no entry in the record: %v", w)
	}
}

// Another refusal is no conflict: it is not tried again.
func TestRecordARefusalIsNotTriedAgain(t *testing.T) {
	g := newRbacRig(t)
	mark := len(g.s.Calls())
	g.s.Route("firestore.googleapis.com", "/v1/projects/dormeto-dev/databases/dmtr-europe-west1/documents/install/record", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			gcptest.Fail(w, 403, "PERMISSION_DENIED", "Missing or insufficient permissions.")
			return
		}
		g.s.Lock()
		fields := g.s.Documents[recordDB+"/install/record"]
		g.s.Unlock()
		gcptest.Reply(w, map[string]any{"fields": fields, "updateTime": "2026-11-01T10:00:00.000001Z"})
	})
	if code, out := g.call(t, WorkloadsAccessGrant, shopAccess); code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := recordPatches(g.s.Calls()[mark:]); len(got) != 1 {
		t.Fatalf("%d writes", len(got))
	}
}

func TestMergedRecordAppliesThisRunsChangesOnTheirs(t *testing.T) {
	a, b, c := RecordRbac{Workload: "a"}, RecordRbac{Workload: "b"}, RecordRbac{Workload: "c"}
	base := Record{Image: "old", Running: "", Rbac: []RecordRbac{a, b}, Grants: []RecordGrant{{Role: "r1", Condition: &Condition{Title: "t"}}}}
	ours := Record{Image: "new", Running: "", Rbac: []RecordRbac{b}, Grants: []RecordGrant{{Role: "r1", Condition: &Condition{Title: "t"}}, {Role: "r2"}}}
	theirs := Record{Image: "old", Running: "tag", Rbac: []RecordRbac{a, b, c}, Grants: []RecordGrant{{Role: "r1", Condition: &Condition{Title: "t"}}, {Role: "r3"}}}
	got := mergedRecord(base, ours, theirs)
	want := Record{Image: "new", Running: "tag", Rbac: []RecordRbac{b, c},
		Grants: []RecordGrant{{Role: "r1", Condition: &Condition{Title: "t"}}, {Role: "r3"}, {Role: "r2"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestMergedRecordAddsNothingTwiceAndKeepsItsFeaturesSorted(t *testing.T) {
	entry := RecordRbac{Workload: "a"}
	got := mergedRecord(Record{}, Record{Rbac: []RecordRbac{entry}, Features: []string{"gke"}}, Record{Rbac: []RecordRbac{entry}, Features: []string{"swap"}})
	if !slices.Equal(got.Rbac, []RecordRbac{entry}) || !slices.Equal(got.Features, []string{"gke", "swap"}) {
		t.Fatalf("%+v", got)
	}
}
