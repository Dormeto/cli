package gcponboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/gcp/access"
	"github.com/Dormeto/cli/internal/gcp/gcptest"
)

func deps(s *gcptest.Server) (Deps, *bytes.Buffer) {
	var out bytes.Buffer
	return Deps{Google: &gcp.Client{HTTP: s.HTTP, Tokens: gcptest.Tokens{AccountName: "me@example.com"}}, Out: &out, Err: &out}, &out
}

func command(t *testing.T, r Report, name string) CommandReport {
	t.Helper()
	for _, c := range r.Commands {
		if c.Command == name {
			return c
		}
	}
	t.Fatalf("no command %s in %+v", name, r.Commands)
	return CommandReport{}
}

func TestOwnerHoldsEveryCommand(t *testing.T) {
	s := gcptest.New(t)
	s.Enabled = access.Load().APIs
	d, out := deps(s)
	r, err := Check(context.Background(), d, Request{Project: "dormeto-dev"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK() || len(r.Commands) != 7 {
		t.Fatalf("%+v", r)
	}
	if err := PrintText(out, r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Project dormeto-dev (1034397096264), no organization, checked as me@example.com\n",
		"  ✓ check: every permission held\n",
		"  ✓ offboard: every permission held\n",
		"  APIs on: compute.googleapis.com, run.googleapis.com,",
		"  APIs off: none\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if m := s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
}

func TestParentIsPrinted(t *testing.T) {
	s := gcptest.New(t)
	s.Parent = "organizations/42"
	d, out := deps(s)
	r, _ := Check(context.Background(), d, Request{Project: "dormeto-dev"})
	PrintText(out, r)
	if !strings.Contains(out.String(), "(1034397096264), in organization 42, checked as") {
		t.Fatal(out)
	}
	s.Parent = "folders/7"
	r, _ = Check(context.Background(), d, Request{Project: "dormeto-dev"})
	out.Reset()
	PrintText(out, r)
	if !strings.Contains(out.String(), "in folder 7, checked as") {
		t.Fatal(out)
	}
}

func TestMissingSetIamPolicyFailsInstallNotCheck(t *testing.T) {
	s := gcptest.New(t)
	s.HoldsAll = false
	s.Held = map[string]bool{}
	for _, p := range access.Needs("check") {
		s.Held[p] = true
	}
	d, out := deps(s)
	r, err := Check(context.Background(), d, Request{Project: "dormeto-dev"})
	if err != nil {
		t.Fatal(err)
	}
	if r.OK() || !command(t, r, "check").OK() {
		t.Fatalf("%+v", r)
	}
	inst := command(t, r, "install")
	if !slices.Contains(inst.Missing, "resourcemanager.projects.setIamPolicy") || slices.Contains(inst.Held, "resourcemanager.projects.setIamPolicy") {
		t.Fatalf("%+v", inst)
	}
	PrintText(out, r)
	if !strings.Contains(out.String(), "  ✓ check: every permission held\n") ||
		!strings.Contains(out.String(), "  ✗ install: missing ") ||
		!strings.Contains(out.String(), "resourcemanager.projects.setIamPolicy") {
		t.Fatal(out)
	}
}

func TestNoRoleInTheProject(t *testing.T) {
	s := gcptest.New(t)
	s.NoRole = true
	d, out := deps(s)
	r, err := Check(context.Background(), d, Request{Project: "dormeto-dev"})
	if err != nil {
		t.Fatal(err)
	}
	if r.OK() {
		t.Fatal("ok")
	}
	for _, c := range r.Commands {
		if !slices.Equal(c.Missing, access.Needs(c.Command)) || len(c.Held) != 0 {
			t.Fatalf("%+v", c)
		}
	}
	PrintText(out, r)
	if !strings.HasPrefix(out.String(), "Project dormeto-dev is not visible to me@example.com: no role in it\n") ||
		!strings.Contains(out.String(), "  APIs: not checked\n") || strings.Contains(out.String(), "APIs on") {
		t.Fatal(out)
	}
	if len(s.CallsTo("/services")) != 0 {
		t.Fatal("services listed")
	}
}

func TestTestIamPermissionsInBatchesOf100(t *testing.T) {
	s := gcptest.New(t)
	d, _ := deps(s)
	var perms []string
	for i := range 250 {
		perms = append(perms, fmt.Sprintf("x.y.p%03d", i))
	}
	held, err := testPermissions(context.Background(), d.Google, "dormeto-dev", perms)
	if err != nil || len(held) != 250 {
		t.Fatalf("%d %v", len(held), err)
	}
	calls := s.CallsTo(":testIamPermissions")
	if len(calls) != 3 {
		t.Fatalf("%d calls", len(calls))
	}
	for _, c := range calls {
		var in struct{ Permissions []string }
		json.Unmarshal([]byte(c.Body), &in)
		if len(in.Permissions) > 100 {
			t.Fatalf("%d permissions", len(in.Permissions))
		}
	}
}

const compute = "1034397096264-compute@developer.gserviceaccount.com"

func TestEditorOnTheDefaultServiceAccountWarns(t *testing.T) {
	s := gcptest.New(t)
	s.Policy = map[string]any{"etag": "x", "bindings": []map[string]any{
		{"role": "roles/editor", "members": []string{"user:a@b.c", "serviceAccount:" + compute}},
	}}
	d, out := deps(s)
	r, _ := Check(context.Background(), d, Request{Project: "dormeto-dev"})
	PrintText(out, r)
	want := "  ! the Compute Engine default service account " + compute + " has Editor on project dormeto-dev: Dormeto does not use it; remove the grant when nothing else needs it\n"
	if !strings.Contains(out.String(), want) || len(r.Warnings) != 1 {
		t.Fatalf("%v\n%s", r.Warnings, out)
	}
	if len(s.Mutations()) != 0 {
		t.Fatal("mutated")
	}
}

func TestNoWarningWithoutEditor(t *testing.T) {
	s := gcptest.New(t)
	s.Policy = map[string]any{"bindings": []map[string]any{{"role": "roles/viewer", "members": []string{"serviceAccount:" + compute}}}}
	d, _ := deps(s)
	r, _ := Check(context.Background(), d, Request{Project: "dormeto-dev"})
	if len(r.Warnings) != 0 {
		t.Fatal(r.Warnings)
	}
}

func TestUnreadablePolicyWarnsOnce(t *testing.T) {
	s := gcptest.New(t)
	s.PolicyDenied = true
	d, out := deps(s)
	r, err := Check(context.Background(), d, Request{Project: "dormeto-dev"})
	if err != nil {
		t.Fatal(err)
	}
	PrintText(out, r)
	if len(r.Warnings) != 1 || r.Warnings[0] != "the IAM policy is not readable: the default service account was not checked" ||
		!strings.Contains(out.String(), "  ! the IAM policy is not readable: the default service account was not checked\n") {
		t.Fatalf("%v", r.Warnings)
	}
}

func TestAPIsOnAndOffAcrossPages(t *testing.T) {
	s := gcptest.New(t)
	s.Enabled = []string{"compute.googleapis.com", "bigquery.googleapis.com", "run.googleapis.com"}
	s.ServicePageSize = 1
	d, out := deps(s)
	r, _ := Check(context.Background(), d, Request{Project: "dormeto-dev"})
	if !slices.Equal(r.APIs.Enabled, []string{"compute.googleapis.com", "run.googleapis.com"}) || len(r.APIs.Missing) != 8 {
		t.Fatalf("%+v", r.APIs)
	}
	PrintText(out, r)
	if !strings.Contains(out.String(), "  APIs on: compute.googleapis.com, run.googleapis.com\n") ||
		!strings.Contains(out.String(), "  APIs off: firestore.googleapis.com, ") || strings.Contains(out.String(), "bigquery") {
		t.Fatal(out)
	}
}

func TestJSONShape(t *testing.T) {
	s := gcptest.New(t)
	s.Parent = "folders/7"
	s.Enabled = []string{"compute.googleapis.com"}
	d, out := deps(s)
	r, _ := Check(context.Background(), d, Request{Project: "dormeto-dev", JSON: true})
	if err := PrintJSON(out, r); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Project  map[string]string
		Account  string
		Commands []struct {
			Command string
			Held    []string
			Missing []string
		}
		APIs     struct{ Enabled, Missing []string } `json:"apis"`
		Warnings []string
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err, out)
	}
	if got.Project["id"] != "dormeto-dev" || got.Project["number"] != "1034397096264" || got.Project["parent"] != "folders/7" ||
		got.Account != "me@example.com" || len(got.Commands) != 7 || got.Commands[0].Command != "check" ||
		len(got.APIs.Enabled) != 1 || len(got.APIs.Missing) != 9 {
		t.Fatalf("%s", out)
	}
	for _, k := range []string{`"missing":[]`, `"warnings":[]`} {
		if !strings.Contains(out.String(), k) {
			t.Errorf("no %s in %s", k, out)
		}
	}
}

func TestGoogleRefusalIsAnError(t *testing.T) {
	s := gcptest.New(t)
	s.Route("cloudresourcemanager.googleapis.com", "/v3/projects/", func(w http.ResponseWriter, r *http.Request) {
		gcptest.Fail(w, 500, "INTERNAL", "boom")
	})
	d, _ := deps(s)
	if _, err := Check(context.Background(), d, Request{Project: "dormeto-dev"}); err == nil {
		t.Fatal("no error")
	}
}

func TestRunCheckExitCodes(t *testing.T) {
	s := gcptest.New(t)
	d, out := deps(s)
	if code := RunCheck(context.Background(), d, Request{Project: "dormeto-dev"}); code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	s.NoRole = true
	if code := RunCheck(context.Background(), d, Request{Project: "dormeto-dev"}); code != 1 {
		t.Fatalf("%d", code)
	}
}
