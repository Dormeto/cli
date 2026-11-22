package installlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// call is a Terraform run the fake saw: its directory and its command line.
type call struct{ dir, line string }

// fake is the runner: each command's exit code by its subcommand (init, plan, apply, version), what it prints, and the calls.
type fake struct {
	codes map[string]int
	calls []call
	tools []string // the commands on PATH
}

func (f *fake) lookPath(name string) (string, error) {
	if slices.Contains(f.tools, name) {
		return "/usr/local/bin/" + name, nil
	}
	return "", errors.New("not found")
}

func (f *fake) run(_ context.Context, dir, name string, args []string, stdout, _ io.Writer) (int, error) {
	f.calls = append(f.calls, call{dir, strings.Join(append([]string{name}, args...), " ")})
	sub := args[0]
	if sub == "exec" {
		sub = args[1]
	}
	switch sub {
	case "version":
		fmt.Fprintln(stdout, "Terraform v1.16.5\non darwin_arm64")
	case "init":
		fmt.Fprintln(stdout, "Initializing the backend...\n\nTerraform has been successfully initialized!")
	case "plan":
		fmt.Fprintln(stdout, "Terraform will perform the following actions:\n\n  # module.org.aws_iam_policy.boundary will be created\n\nPlan: 1 to add, 0 to change, 0 to destroy.\n\nSaved the plan to: dev.tfplan")
	case "apply":
		fmt.Fprintln(stdout, "Apply complete! Resources: 1 added, 0 changed, 0 destroyed.")
	}
	return f.codes[sub], nil
}

// stack writes a Terraform configuration: its .tf files and its env/ files, under a temporary directory, and returns its path.
func stack(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "infra", "terraform", "org")
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// orgStack is the manager's organization stack's shape: a file it reads outside the directory, a required version, the
// dev environment's three files.
func orgStack(t *testing.T) string {
	t.Helper()
	dir := stack(t, map[string]string{
		"main.tf":                  `module "org" { boundary_policy = file("${path.module}/../../../router/dist/router-boundary.json") }`,
		"versions.tf":              "terraform {\n  required_version = \"~> 1.16.5\"\n}\n",
		"env/dev.tfvars":           "x = 1\n",
		"env/dev.backend.hcl":      "bucket = \"b\"\n",
		"env/dev.local.tfvars":     "secret = \"never printed\"\n",
		"env/prod.tfvars":          "x = 2\n",
		"env/staging.local.tfvars": "y = 1\n",
	})
	boundary := filepath.Join(dir, "../../../router/dist/router-boundary.json")
	if err := os.MkdirAll(filepath.Dir(boundary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(boundary, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runIt(t *testing.T, f *fake, r Request) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := Run(context.Background(), Deps{Out: &out, Err: &out, LookPath: f.lookPath, Run: f.run}, r)
	return code, out.String()
}

func lines(f *fake) []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.line)
	}
	return out
}

// The checks pass, then init, plan and, with changes, the saved plan applied, all in the directory, with tfenv when it is
// there; the environment's three files used.
func TestRunChecksThenInitPlanApply(t *testing.T) {
	dir := orgStack(t)
	f := &fake{tools: []string{"tfenv", "terraform"}, codes: map[string]int{"plan": 2}}
	code, out := runIt(t, f, Request{Dir: dir, Env: "dev"})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	want := []string{"tfenv exec version", "tfenv exec init -input=false -backend-config=env/dev.backend.hcl",
		"tfenv exec plan -input=false -detailed-exitcode -var-file=env/dev.tfvars -var-file=env/dev.local.tfvars -out=dev.tfplan",
		"tfenv exec apply -input=false dev.tfplan"}
	if got := lines(f); !slices.Equal(got, want) {
		t.Fatalf("calls %q", got)
	}
	for _, c := range f.calls {
		if c.dir != dir {
			t.Fatalf("ran in %s", c.dir)
		}
	}
	for _, w := range []string{
		"Checks\n",
		"  ✓ directory: " + dir + " (2 .tf files)\n",
		"  ✓ environment dev: env/dev.tfvars, env/dev.backend.hcl, env/dev.local.tfvars\n",
		"  ✓ local files: ../../../router/dist/router-boundary.json\n",
		"  ✓ terraform: tfenv exec (Terraform v1.16.5; required_version ~> 1.16.5)\n",
		"\ninit: tfenv exec init -input=false -backend-config=env/dev.backend.hcl\n",
		"\nplan: tfenv exec plan -input=false -detailed-exitcode -var-file=env/dev.tfvars -var-file=env/dev.local.tfvars -out=dev.tfplan\n",
		"Plan: 1 to add, 0 to change, 0 to destroy.\n",
		"\napply: tfenv exec apply -input=false dev.tfplan\n",
		"Apply complete!",
		"Applied dev.tfplan in " + dir + ".\n",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
	if strings.Contains(out, "never printed") {
		t.Fatal("a local tfvars value printed")
	}
}

// --dry-run stops after the plan, changes or not; without it, a plan without changes applies nothing.
func TestRunDryRunAndNoChange(t *testing.T) {
	for _, planCode := range []int{0, 2} {
		f := &fake{tools: []string{"terraform"}, codes: map[string]int{"plan": planCode}}
		dir := orgStack(t)
		code, out := runIt(t, f, Request{Dir: dir, Env: "dev", DryRun: true})
		if code != 0 || slices.ContainsFunc(lines(f), func(l string) bool { return strings.Contains(l, " apply") }) ||
			!strings.Contains(out, "Nothing was applied (--dry-run). The plan is saved in "+filepath.Join(dir, "dev.tfplan")+"; without --dry-run, the command plans again and applies it.\n") {
			t.Errorf("dry run, plan %d: exit %d %q:\n%s", planCode, code, lines(f), out)
		}
		if lines(f)[1] != "terraform init -input=false -backend-config=env/dev.backend.hcl" {
			t.Errorf("without tfenv: %q", lines(f))
		}
	}
	f := &fake{tools: []string{"terraform"}, codes: map[string]int{"plan": 0}}
	code, out := runIt(t, f, Request{Dir: orgStack(t), Env: "dev"})
	if code != 0 || len(f.calls) != 3 || !strings.Contains(out, "No change: nothing to apply.\n") {
		t.Fatalf("no change: exit %d %q:\n%s", code, lines(f), out)
	}
}

// A Terraform failure: exit 1, the step named, nothing after it.
func TestRunSaysWhichStepFailed(t *testing.T) {
	for step, want := range map[string]string{
		"init":  "terraform init failed (exit 1): nothing was planned or applied.",
		"plan":  "terraform plan failed (exit 1): nothing was applied.",
		"apply": "terraform apply failed (exit 1): read Terraform's output above; the state holds what it applied.",
	} {
		codes := map[string]int{step: 1}
		if step == "apply" {
			codes["plan"] = 2
		}
		f := &fake{tools: []string{"terraform"}, codes: codes}
		code, out := runIt(t, f, Request{Dir: orgStack(t), Env: "dev"})
		if code != 1 || !strings.Contains(out, want) {
			t.Errorf("%s: exit %d %q:\n%s", step, code, lines(f), out)
		}
		if last := lines(f)[len(f.calls)-1]; !strings.HasPrefix(last, "terraform "+step) {
			t.Errorf("%s: ran on to %q", step, last)
		}
	}
}

// Each batch of checks stops the run at its first failure, before any Terraform call but the version's.
func TestRunChecksStopAtTheirBatch(t *testing.T) {
	cases := map[string]struct {
		dir  func(t *testing.T) string
		env  string
		want string
	}{
		"no directory": {func(t *testing.T) string { return filepath.Join(t.TempDir(), "none") }, "dev", "  ✗ directory: "},
		"a file": {func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "main.tf")
			os.WriteFile(p, []byte(""), 0o644)
			return p
		}, "dev", ": not a directory\n"},
		"no .tf":      {func(t *testing.T) string { return t.TempDir() }, "dev", ": no .tf file: not a Terraform configuration\n"},
		"no env file": {orgStack, "qa", "  ✗ environment qa: env/qa.tfvars not found; the environments here: dev, prod, staging\n"},
		"a local file missing": {func(t *testing.T) string {
			return stack(t, map[string]string{"main.tf": `x = templatefile("${path.module}/tpl/a.tpl", {}) y = filebase64("${path.module}/b.zip")`})
		}, "dev", "  ✗ local file tpl/a.tpl: not found: build it first\n  ✗ local file b.zip: not found: build it first\n"},
	}
	for name, c := range cases {
		f := &fake{tools: []string{"terraform"}}
		code, out := runIt(t, f, Request{Dir: c.dir(t), Env: c.env})
		if code != 1 || !strings.Contains(out, c.want) || len(f.calls) != 0 {
			t.Errorf("%s: exit %d %q:\n%s", name, code, lines(f), out)
		}
	}
	// No Terraform at all.
	f := &fake{}
	code, out := runIt(t, f, Request{Dir: orgStack(t), Env: "dev"})
	if code != 1 || !strings.Contains(out, "  ✗ terraform: neither tfenv nor terraform is on PATH\n") || len(f.calls) != 0 {
		t.Errorf("no terraform: exit %d:\n%s", code, out)
	}
	// Its version refused.
	f = &fake{tools: []string{"terraform"}, codes: map[string]int{"version": 1}}
	code, out = runIt(t, f, Request{Dir: orgStack(t), Env: "dev"})
	if code != 1 || !strings.Contains(out, "  ✗ terraform: terraform version failed (exit 1)\n") || len(f.calls) != 1 {
		t.Errorf("version refused: exit %d:\n%s", code, out)
	}
}

// Without an env/ folder: no var file, no backend configuration; no local file read is said.
func TestRunWithoutAnEnvFolder(t *testing.T) {
	dir := stack(t, map[string]string{"main.tf": "resource \"x\" \"y\" {}\n"})
	f := &fake{tools: []string{"terraform"}, codes: map[string]int{"plan": 2}}
	code, out := runIt(t, f, Request{Dir: dir, Env: "prod"})
	want := []string{"terraform version", "terraform init -input=false", "terraform plan -input=false -detailed-exitcode -out=prod.tfplan", "terraform apply -input=false prod.tfplan"}
	if code != 0 || !slices.Equal(lines(f), want) {
		t.Fatalf("exit %d %q:\n%s", code, lines(f), out)
	}
	for _, w := range []string{"  - environment prod: no env/ folder: no var file, no backend configuration\n", "  - local files: none read\n",
		"  ✓ terraform: terraform (Terraform v1.16.5)\n"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

// A run cancelled (Ctrl-C) stops at once, the step said, nothing after it.
func TestRunStopsWhenCancelled(t *testing.T) {
	f := &fake{tools: []string{"terraform"}, codes: map[string]int{"plan": 2}}
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	run := func(c context.Context, dir, name string, args []string, stdout, stderr io.Writer) (int, error) {
		code, err := f.run(c, dir, name, args, stdout, stderr)
		if args[0] == "plan" {
			cancel()
			return -1, context.Canceled
		}
		return code, err
	}
	code := Run(ctx, Deps{Out: &out, Err: &out, LookPath: f.lookPath, Run: run}, Request{Dir: orgStack(t), Env: "dev"})
	if code != 1 || slices.ContainsFunc(lines(f), func(l string) bool { return strings.Contains(l, " apply") }) || !strings.Contains(out.String(), "terraform plan stopped: interrupted; nothing was applied.") {
		t.Fatalf("exit %d %q:\n%s", code, lines(f), out.String())
	}
}
