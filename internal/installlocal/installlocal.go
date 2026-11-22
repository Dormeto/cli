// Package installlocal is `dormeto aws install local [directory]`: a Terraform configuration of the manager's source (its
// org or platform stack, or any configuration laid out the same way) checked, planned and applied from this machine, in
// place of `cd <dir>` and the three Terraform commands. Read-only checks first, each a ✓/✗ line: the directory, the
// environment's files under env/, the local files the configuration reads with file(), filebase64() or templatefile(), and
// Terraform itself (tfenv exec when tfenv is on PATH). Then, in the directory, with Terraform's own output: init, plan
// saved to <env>.tfplan, and that saved plan applied unless --dry-run; running the command is the confirmation. Exit 0; 1
// when a check or a Terraform step fails, or the run is interrupted (the command's caller gives a signal its code).
package installlocal

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Deps are the run's dependencies: where it writes, how it finds a command on PATH and how it runs one in a directory
// (its exit code; an error when it could not run or was stopped).
type Deps struct {
	Out, Err io.Writer
	LookPath func(string) (string, error)
	Run      func(ctx context.Context, dir, name string, args []string, stdout, stderr io.Writer) (int, error)
}

// Request is the command's: the configuration's directory, the environment, --dry-run and --verbose (accepted, as every
// install command takes it: Terraform's lines are always printed here).
type Request struct {
	Dir, Env        string
	DryRun, Verbose bool
}

var (
	// localFile is a file the configuration reads from its own directory: file, filebase64 or templatefile's first argument.
	localFile       = regexp.MustCompile(`\b(?:file|filebase64|templatefile)\(\s*"\$\{path\.module\}/([^"]+)"`)
	requiredVersion = regexp.MustCompile(`required_version\s*=\s*"([^"]+)"`)
)

// terraform is the command that runs Terraform: tfenv exec, or terraform.
type terraform struct {
	name   string
	prefix []string
}

func (t terraform) String() string { return strings.Join(append([]string{t.name}, t.prefix...), " ") }

func (t terraform) args(args ...string) []string { return append(slices.Clone(t.prefix), args...) }

// run is one run of the command.
type run struct {
	ctx context.Context
	d   Deps
	r   Request
	tf  terraform
	// The environment's files, relative to the directory ("" when absent).
	tfvars, backend, localVars string
}

// Run is `dormeto aws install local`.
func Run(ctx context.Context, d Deps, r Request) int {
	x := &run{ctx: ctx, d: d, r: r}
	fmt.Fprintln(d.Out, "Checks")
	for _, check := range []func() bool{x.checkDirectory, x.checkEnvironment, x.checkLocalFiles, x.checkTerraform} {
		if !check() {
			fmt.Fprintln(d.Out, "\nNothing was run: fix what is marked ✗, then run the same command again.")
			return 1
		}
	}
	return x.terraformSteps()
}

func (x *run) ok(format string, a ...any)   { fmt.Fprintf(x.d.Out, "  ✓ "+format+"\n", a...) }
func (x *run) fail(format string, a ...any) { fmt.Fprintf(x.d.Out, "  ✗ "+format+"\n", a...) }
func (x *run) skip(format string, a ...any) { fmt.Fprintf(x.d.Out, "  - "+format+"\n", a...) }

// tfFiles are the directory's own .tf files, sorted.
func (x *run) tfFiles() []string {
	files, _ := filepath.Glob(filepath.Join(x.r.Dir, "*.tf"))
	slices.Sort(files)
	return files
}

func (x *run) checkDirectory() bool {
	info, err := os.Stat(x.r.Dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		x.fail("directory: %s: not found", x.r.Dir)
		return false
	case err != nil:
		x.fail("directory: %s: %v", x.r.Dir, err)
		return false
	case !info.IsDir():
		x.fail("directory: %s: not a directory", x.r.Dir)
		return false
	}
	n := len(x.tfFiles())
	if n == 0 {
		x.fail("directory: %s: no .tf file: not a Terraform configuration", x.r.Dir)
		return false
	}
	files := "files"
	if n == 1 {
		files = "file"
	}
	x.ok("directory: %s (%d .tf %s)", x.r.Dir, n, files)
	return true
}

// present tells whether a file of the directory exists.
func (x *run) present(rel string) bool {
	info, err := os.Stat(filepath.Join(x.r.Dir, rel))
	return err == nil && !info.IsDir()
}

func (x *run) checkEnvironment() bool {
	env := x.r.Env
	if info, err := os.Stat(filepath.Join(x.r.Dir, "env")); err != nil || !info.IsDir() {
		x.skip("environment %s: no env/ folder: no var file, no backend configuration", env)
		return true
	}
	tfvars := "env/" + env + ".tfvars"
	if !x.present(tfvars) {
		here := "none"
		if found := x.environments(); len(found) > 0 {
			here = strings.Join(found, ", ")
		}
		x.fail("environment %s: %s not found; the environments here: %s", env, tfvars, here)
		return false
	}
	x.tfvars = tfvars
	used := []string{tfvars}
	if b := "env/" + env + ".backend.hcl"; x.present(b) {
		x.backend = b
		used = append(used, b)
	}
	if l := "env/" + env + ".local.tfvars"; x.present(l) {
		x.localVars = l
		used = append(used, l)
	}
	x.ok("environment %s: %s", env, strings.Join(used, ", "))
	return true
}

// environments are the names of env/'s .tfvars files, a .local one's without it, sorted and once each.
func (x *run) environments() []string {
	files, _ := filepath.Glob(filepath.Join(x.r.Dir, "env", "*.tfvars"))
	var out []string
	for _, f := range files {
		name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(f), ".tfvars"), ".local")
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// checkLocalFiles checks that each file the configuration reads from ${path.module} exists: a missing one, which a build
// usually makes, is named with the hint to build it.
func (x *run) checkLocalFiles() bool {
	var paths []string
	for _, f := range x.tfFiles() {
		b, err := os.ReadFile(f)
		if err != nil {
			x.fail("local files: %s is not read: %v", filepath.Base(f), err)
			return false
		}
		for _, m := range localFile.FindAllStringSubmatch(string(b), -1) {
			// A path built from a variable is not one this check can name.
			if !strings.Contains(m[1], "${") && !slices.Contains(paths, m[1]) {
				paths = append(paths, m[1])
			}
		}
	}
	if len(paths) == 0 {
		x.skip("local files: none read")
		return true
	}
	var found []string
	missing := false
	for _, p := range paths {
		if x.present(p) {
			found = append(found, p)
			continue
		}
		missing = true
		x.fail("local file %s: not found: build it first", p)
	}
	if len(found) > 0 {
		x.ok("local files: %s", strings.Join(found, ", "))
	}
	return !missing
}

// checkTerraform finds Terraform, tfenv exec first, and asks its version; the configuration's required_version is said
// beside it, for Terraform to enforce.
func (x *run) checkTerraform() bool {
	switch {
	case x.found("tfenv"):
		x.tf = terraform{"tfenv", []string{"exec"}}
	case x.found("terraform"):
		x.tf = terraform{name: "terraform"}
	default:
		x.fail("terraform: neither tfenv nor terraform is on PATH")
		return false
	}
	var out, errs bytes.Buffer
	code, err := x.d.Run(x.ctx, x.r.Dir, x.tf.name, x.tf.args("version"), &out, &errs)
	switch {
	case err != nil:
		x.fail("terraform: %s version did not run: %v", x.tf, err)
		return false
	case code != 0:
		x.fail("terraform: %s version failed (exit %d)", x.tf, code)
		if s := strings.TrimSpace(errs.String()); s != "" {
			fmt.Fprintln(x.d.Err, s)
		}
		return false
	}
	version, _, _ := strings.Cut(strings.TrimSpace(out.String()), "\n")
	if req := x.requiredVersion(); req != "" {
		version += "; required_version " + req
	}
	x.ok("terraform: %s (%s)", x.tf, version)
	return true
}

func (x *run) found(name string) bool {
	_, err := x.d.LookPath(name)
	return err == nil
}

// requiredVersion is the first required_version of the directory's .tf files ("" for none).
func (x *run) requiredVersion() string {
	for _, f := range x.tfFiles() {
		file, err := os.Open(f)
		if err != nil {
			continue
		}
		s := bufio.NewScanner(file)
		for s.Scan() {
			if m := requiredVersion.FindStringSubmatch(s.Text()); m != nil {
				file.Close()
				return m[1]
			}
		}
		file.Close()
	}
	return ""
}

// step runs one Terraform command in the directory, its output streamed: its exit code, or -1 when it could not run or was
// stopped, and the words of its failure.
func (x *run) step(name string, args ...string) (int, string) {
	fmt.Fprintf(x.d.Out, "\n%s: %s %s\n", name, x.tf, strings.Join(args, " "))
	code, err := x.d.Run(x.ctx, x.r.Dir, x.tf.name, x.tf.args(args...), x.d.Out, x.d.Err)
	switch {
	case x.ctx.Err() != nil:
		return -1, "stopped: interrupted;"
	case err != nil:
		return -1, "did not run: " + err.Error() + ":"
	}
	return code, fmt.Sprintf("failed (exit %d):", code)
}

func (x *run) terraformSteps() int {
	init := []string{"init", "-input=false"}
	if x.backend != "" {
		init = append(init, "-backend-config="+x.backend)
	}
	if code, why := x.step("init", init...); code != 0 {
		fmt.Fprintf(x.d.Out, "\nterraform init %s nothing was planned or applied.\n", why)
		return 1
	}
	planFile := x.r.Env + ".tfplan"
	plan := []string{"plan", "-input=false", "-detailed-exitcode"}
	for _, f := range []string{x.tfvars, x.localVars} {
		if f != "" {
			plan = append(plan, "-var-file="+f)
		}
	}
	plan = append(plan, "-out="+planFile)
	code, why := x.step("plan", plan...)
	if code != 0 && code != 2 {
		fmt.Fprintf(x.d.Out, "\nterraform plan %s nothing was applied.\n", why)
		return 1
	}
	if x.r.DryRun {
		fmt.Fprintf(x.d.Out, "\nNothing was applied (--dry-run). The plan is saved in %s; without --dry-run, the command plans again and applies it.\n",
			filepath.Join(x.r.Dir, planFile))
		return 0
	}
	if code == 0 {
		fmt.Fprintln(x.d.Out, "\nNo change: nothing to apply.")
		return 0
	}
	if code, why := x.step("apply", "apply", "-input=false", planFile); code != 0 {
		fmt.Fprintf(x.d.Out, "\nterraform apply %s read Terraform's output above; the state holds what it applied.\n", why)
		return 1
	}
	fmt.Fprintf(x.d.Out, "\nApplied %s in %s.\n", planFile, x.r.Dir)
	return 0
}
