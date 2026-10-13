package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/options"
)

func readme(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// section is the text of the "## <title>" section, up to the next "## ".
func section(t *testing.T, text, title string) string {
	t.Helper()
	_, rest, ok := strings.Cut(text, "\n## "+title+"\n")
	if !ok {
		t.Fatalf("README.md has no '## %s' section", title)
	}
	body, _, _ := strings.Cut(rest, "\n## ")
	return body
}

func names(re *regexp.Regexp, text string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	slices.Sort(out)
	return out
}

// The README's "Options" section explains each option as a bullet starting with "- `--name`": exactly the options
// of the usage, none missing and none extra.
func TestReadmeOptionsAreTheUsageOptions(t *testing.T) {
	want := names(regexp.MustCompile(`--([a-z][a-z0-9-]*)`), options.Usage)
	if len(want) == 0 {
		t.Fatal("no option found in the usage")
	}
	text := section(t, readme(t), "Options")
	got := names(regexp.MustCompile("(?m)^- `--([a-z][a-z0-9-]*)"), text)
	if !slices.Equal(want, got) {
		t.Errorf("README options %v, usage options %v", got, want)
	}
	if !strings.Contains(text, options.Usage) {
		t.Errorf("the Options section does not contain the usage text verbatim")
	}
}

// The relative links of the README name files that exist; the website's links are https://dormeto.com/... ones,
// which are answered 200 at review (no network in the tests).
func TestReadmeRelativeLinksResolve(t *testing.T) {
	text := readme(t)
	links := regexp.MustCompile(`\]\(([^)\s]+)\)`).FindAllStringSubmatch(text, -1)
	if len(links) == 0 {
		t.Fatal("no link in README.md")
	}
	for _, m := range links {
		target := m[1]
		if strings.Contains(target, "://") || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
			continue
		}
		path, _, _ := strings.Cut(target, "#")
		if _, err := os.Stat(filepath.FromSlash(path)); err != nil {
			t.Errorf("link %q: %v", target, err)
		}
	}
}

func TestReadmeSections(t *testing.T) {
	text := readme(t)
	for _, title := range []string{"Install", "First run", "Options", "Permissions", "Report", "Cache", "Privacy", "Releasing", "License"} {
		section(t, text, title)
	}
}
