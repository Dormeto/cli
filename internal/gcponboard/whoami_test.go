package gcponboard

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Dormeto/cli/internal/gcp/gcptest"
)

func TestWhoamiLabelsTheAccountTheConfigurationAndTheProject(t *testing.T) {
	s := gcptest.New(t)
	s.Parent = "organizations/42"
	d, out := deps(s)
	code := RunWhoami(context.Background(), d, Request{Project: "dormeto-dev", Configuration: "dormeto-dev"})
	want := "account        me@example.com\n" +
		"configuration  dormeto-dev\n" +
		"project        dormeto-dev\n" +
		"number         1034397096264\n" +
		"parent         organization 42\n"
	if code != 0 || out.String() != want {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if m := s.Mutations(); len(m) != 0 {
		t.Fatalf("mutating calls: %+v", m)
	}
}

func TestWhoamiSaysActiveWithoutAConfigurationAndNoneWithoutAParent(t *testing.T) {
	s := gcptest.New(t)
	d, out := deps(s)
	if code := RunWhoami(context.Background(), d, Request{Project: "dormeto-dev"}); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"configuration  (active)\n", "parent         none\n"} {
		contains(t, out.String(), want)
	}
	s.Parent = "folders/7"
	d, out = deps(s)
	RunWhoami(context.Background(), d, Request{Project: "dormeto-dev"})
	contains(t, out.String(), "parent         folder 7\n")
}

func TestWhoamiJSON(t *testing.T) {
	s := gcptest.New(t)
	s.Parent = "organizations/42"
	d, out := deps(s)
	if code := RunWhoami(context.Background(), d, Request{Project: "dormeto-dev", JSON: true}); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !indentedAsAWS(out.String()) {
		t.Fatalf("not indented:\n%s", out)
	}
	want := `{"account":"me@example.com","configuration":"(active)","project":{"id":"dormeto-dev","number":"1034397096264","parent":"organizations/42"}}`
	if got := compacted(out.String()); got != want {
		t.Fatalf("%s", got)
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
}

func TestWhoamiOfAProjectNotVisible(t *testing.T) {
	s := gcptest.New(t)
	s.NoRole = true
	d, out := deps(s)
	code := RunWhoami(context.Background(), d, Request{Project: "dormeto-dev"})
	if code != 1 || out.String() != "Project dormeto-dev is not visible to me@example.com: no role in it\n" {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
