package cli

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/Dormeto/cli"

// reaches follows the imports of the files, then those of each package of the module they reach (its non-test files),
// and returns the import chain to the first forbidden package, or nil.
func reaches(root string, files []string, forbidden ...string) []string {
	seen := map[string]bool{}
	var walk func(chain []string, imports []string) []string
	walk = func(chain []string, imports []string) []string {
		for _, imp := range imports {
			if imp != modulePath && !strings.HasPrefix(imp, modulePath+"/") {
				continue
			}
			next := append(append([]string{}, chain...), imp)
			for _, f := range forbidden {
				if imp == f {
					return next
				}
			}
			if seen[imp] {
				continue
			}
			seen[imp] = true
			dir := filepath.Join(root, strings.TrimPrefix(imp, modulePath))
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
					continue
				}
				if found := walk(next, importsOf(filepath.Join(dir, name))); found != nil {
					return found
				}
			}
		}
		return nil
	}
	for _, f := range files {
		if found := walk([]string{f}, importsOf(f)); found != nil {
			return found
		}
	}
	return nil
}

func importsOf(file string) []string {
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range f.Imports {
		out = append(out, strings.Trim(s.Path.Value, `"`))
	}
	return out
}

func TestTheEstimateNeverImportsTheInstallNorTheFleet(t *testing.T) {
	files := []string{"cmd/dormeto/estimate.go"}
	entries, err := os.ReadDir("internal/cli")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			files = append(files, filepath.Join("internal/cli", n))
		}
	}
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			t.Fatal(err)
		}
	}
	if chain := reaches(".", files, modulePath+"/internal/install", modulePath+"/internal/installaws", modulePath+"/internal/fleet"); chain != nil {
		t.Errorf("the estimate reaches the install or the fleet: %s", strings.Join(chain, " -> "))
	}
}

func TestTheGuardFindsAPlantedImport(t *testing.T) {
	root := t.TempDir()
	write := func(path, src string) {
		t.Helper()
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("start.go", "package main\nimport _ \""+modulePath+"/internal/a\"\n")
	write("internal/a/a.go", "package a\nimport _ \""+modulePath+"/internal/b\"\nimport _ \"fmt\"\n")
	write("internal/b/b.go", "package b\nimport _ \""+modulePath+"/internal/install\"\n")
	write("internal/install/i.go", "package install\n")
	start := filepath.Join(root, "start.go")
	got := strings.Join(reaches(root, []string{start}, modulePath+"/internal/install"), " -> ")
	want := start + " -> " + modulePath + "/internal/a -> " + modulePath + "/internal/b -> " + modulePath + "/internal/install"
	if got != want {
		t.Errorf("chain %q, want %q", got, want)
	}
	write("internal/b/b.go", "package b\n")
	if chain := reaches(root, []string{start}, modulePath+"/internal/install"); chain != nil {
		t.Errorf("unexpected chain %v", chain)
	}
	// A test file's import is not followed.
	write("internal/b/b_test.go", "package b\nimport _ \""+modulePath+"/internal/install\"\n")
	if chain := reaches(root, []string{start}, modulePath+"/internal/install"); chain != nil {
		t.Errorf("a test file was followed: %v", chain)
	}
}
