package installlocal

// The samples of `dormeto aws install local` (cmd/dormeto/samples_test.go lists it among the external ones): the run and its
// --dry-run, against the fake Terraform of these tests, in a copy of the manager's organization stack's shape, written to
// docs/samples/aws-install-local.json and aws-install-local-dry-run.json. Rewrite them with
// go test ./internal/installlocal -run Samples -update.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite docs/samples")

// sampleFile is cmd/dormeto's: the command, its variant, the line, what it prints, null for no --json.
type sampleFile struct {
	Command string  `json:"command"`
	Variant string  `json:"variant,omitempty"`
	Line    string  `json:"line"`
	Table   string  `json:"table"`
	JSON    *string `json:"json"`
}

func TestSamples(t *testing.T) {
	samples := docs(t)
	for _, s := range []struct {
		variant, line string
		dryRun        bool
	}{
		{"", "dormeto aws install local infra/terraform/org --env dev", false},
		{"dry-run", "dormeto aws install local infra/terraform/org --env dev --dry-run", true},
	} {
		dir := orgStack(t)
		t.Chdir(filepath.Join(dir, "..", "..", ".."))
		f := &fake{tools: []string{"tfenv", "terraform"}, codes: map[string]int{"plan": 2}}
		var out bytes.Buffer
		if code := Run(context.Background(), Deps{Out: &out, Err: &out, LookPath: f.lookPath, Run: f.run},
			Request{Dir: "infra/terraform/org", Env: "dev", DryRun: s.dryRun}); code != 0 {
			t.Fatalf("%s: exit %d\n%s", s.line, code, out.String())
		}
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(sampleFile{Command: "aws install local", Variant: s.variant, Line: s.line, Table: out.String()}); err != nil {
			t.Fatal(err)
		}
		name := "aws-install-local.json"
		if s.variant != "" {
			name = "aws-install-local-" + s.variant + ".json"
		}
		path := filepath.Join(samples, name)
		if *update {
			if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if was, err := os.ReadFile(path); err != nil || !bytes.Equal(was, b.Bytes()) {
			t.Errorf("docs/samples/%s differs from what %q prints now; update it: go test ./internal/installlocal -run Samples -update\nnow:\n%s", name, s.line, b.String())
		}
	}
}

// docs is docs/samples, absolute: the samples run in directories of their own.
func docs(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "docs", "samples"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
