package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeHTTP answers GETs by URL: a known URL gets its status and body, any other gets 404.
type fakeHTTP struct {
	bodies map[string]string
	status map[string]int
}

func (f fakeHTTP) Do(r *http.Request) (*http.Response, error) {
	code, body := http.StatusNotFound, ""
	if b, ok := f.bodies[r.URL.String()]; ok {
		code, body = http.StatusOK, b
	}
	if c, ok := f.status[r.URL.String()]; ok {
		code = c
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func sumsFor(v string, files ...string) string {
	var b strings.Builder
	for _, f := range files {
		b.WriteString(sum(f) + "  " + f + "\n")
	}
	return b.String()
}

func releaseFiles(v string) []string {
	return []string{
		"CHANGELOG.md",
		"dormeto-access-" + v + ".zip",
		"dormeto-access-commands-" + v + ".sh",
		"dormeto-kubernetes-" + v + ".yaml",
		"dormeto-organization-" + v + ".zip",
		"dormeto-organization-policy.json",
		"terraform-aws-dormeto-" + v + ".zip",
	}
}

func TestLatest(t *testing.T) {
	h := fakeHTTP{bodies: map[string]string{ReleasesURL + "/latest.json": `{"version":"0.12.0","router":"0.29.0","published":"x"}`}}
	v, err := Latest(context.Background(), h)
	if err != nil || v != "0.12.0" {
		t.Fatalf("got %q, %v", v, err)
	}
	h = fakeHTTP{bodies: map[string]string{ReleasesURL + "/latest.json": `{"router":"0.29.0"}`}}
	if _, err := Latest(context.Background(), h); err == nil {
		t.Fatal("a body without version must fail")
	}
	if _, err := Latest(context.Background(), fakeHTTP{}); err == nil || !strings.Contains(err.Error(), "latest.json") {
		t.Fatalf("a 404 must fail naming the URL: %v", err)
	}
}

func TestResolvePublished(t *testing.T) {
	url := ReleasesURL + "/0.11.0/SHA256SUMS"
	files := releaseFiles("0.11.0")
	h := fakeHTTP{bodies: map[string]string{url: sumsFor("0.11.0", files...)}}
	r, err := Resolve(context.Background(), h, "0.11.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "0.11.0" || r.Base != ReleasesURL+"/0.11.0" || r.Local != "" || len(r.Sums) != len(files) {
		t.Fatalf("got %+v", r)
	}
	if r.Sums["CHANGELOG.md"] != sum("CHANGELOG.md") {
		t.Fatal("sum not parsed")
	}

	// the "<hex> *<name>" form
	h.bodies[url] = strings.Replace(sumsFor("0.11.0", files...), "  CHANGELOG.md", " *CHANGELOG.md", 1)
	if r, err = Resolve(context.Background(), h, "0.11.0", ""); err != nil || r.Sums["CHANGELOG.md"] == "" {
		t.Fatalf("binary form: %+v %v", r, err)
	}

	// not published
	if _, err := Resolve(context.Background(), h, "0.13.0", ""); err == nil || !strings.Contains(err.Error(), "manager 0.13.0 is not published on releases.dormeto.com") {
		t.Fatalf("got %v", err)
	}
	// releases.dormeto.com is a private bucket behind CloudFront: a missing file answers 403, also "not published", with
	// how to install a release built locally
	h.status = map[string]int{ReleasesURL + "/0.14.0/SHA256SUMS": 403}
	_, err = Resolve(context.Background(), h, "0.14.0", "")
	if err == nil || !strings.Contains(err.Error(), "manager 0.14.0 is not published on releases.dormeto.com") || !strings.Contains(err.Error(), "--release <dir>") {
		t.Fatalf("403: got %v", err)
	}
	h.status = nil

	// another status names the URL and status
	h.status = map[string]int{url: 500}
	if _, err := Resolve(context.Background(), h, "0.11.0", ""); err == nil || !strings.Contains(err.Error(), url) || !strings.Contains(err.Error(), "500") {
		t.Fatalf("got %v", err)
	}
	h.status = nil

	// missing access script
	h.bodies[url] = sumsFor("0.11.0", files[:2]...)
	if _, err := Resolve(context.Background(), h, "0.11.0", ""); err == nil || !strings.Contains(err.Error(), "dormeto-access-commands-0.11.0.sh") {
		t.Fatalf("got %v", err)
	}

	// published but too old
	h.bodies[ReleasesURL+"/0.10.0/SHA256SUMS"] = sumsFor("0.10.0", releaseFiles("0.10.0")...)
	if _, err := Resolve(context.Background(), h, "0.10.0", ""); err == nil || !strings.Contains(err.Error(), "dormeto install needs manager 0.11.0 or later (asked: 0.10.0)") {
		t.Fatalf("got %v", err)
	}
}

func writeLocal(t *testing.T, v string, mutate func(map[string]string)) string {
	t.Helper()
	dir := t.TempDir()
	content := map[string]string{}
	for _, f := range releaseFiles(v) {
		content[f] = "content of " + f
	}
	var sums strings.Builder
	for _, f := range releaseFiles(v) {
		sums.WriteString(sum(content[f]) + "  " + f + "\n")
	}
	if mutate != nil {
		mutate(content)
	}
	for f, c := range content {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveLocal(t *testing.T) {
	dir := writeLocal(t, "0.11.0-dev", nil)
	r, err := Resolve(context.Background(), fakeHTTP{}, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "0.11.0-dev" || r.Local != dir || r.Base != "" || len(r.Sums) != 7 {
		t.Fatalf("got %+v", r)
	}
	want := append(releaseFiles("0.11.0-dev"), "SHA256SUMS")
	if got := r.Files(); !reflect.DeepEqual(got, want) {
		t.Fatalf("files %v, want %v", got, want)
	}
	if got := r.Required(); !reflect.DeepEqual(got, []string{"terraform-aws-dormeto-0.11.0-dev.zip", "dormeto-access-commands-0.11.0-dev.sh", "dormeto-organization-policy.json"}) {
		t.Fatalf("required %v", got)
	}
	if _, err := Resolve(context.Background(), fakeHTTP{}, "0.11.0-dev", dir); err != nil {
		t.Fatalf("matching --version: %v", err)
	}

	// wrong sum
	bad := writeLocal(t, "0.11.0-dev", func(c map[string]string) { c["dormeto-access-0.11.0-dev.zip"] = "tampered" })
	if _, err := Resolve(context.Background(), fakeHTTP{}, "", bad); err == nil || !strings.Contains(err.Error(), "dormeto-access-0.11.0-dev.zip") {
		t.Fatalf("got %v", err)
	}

	// a listed file missing
	gone := writeLocal(t, "0.11.0-dev", nil)
	os.Remove(filepath.Join(gone, "CHANGELOG.md"))
	if _, err := Resolve(context.Background(), fakeHTTP{}, "", gone); err == nil || !strings.Contains(err.Error(), "CHANGELOG.md") {
		t.Fatalf("got %v", err)
	}

	// another version asked
	if _, err := Resolve(context.Background(), fakeHTTP{}, "0.12.0", dir); err == nil || !strings.Contains(err.Error(), "0.12.0") {
		t.Fatalf("got %v", err)
	}

	// no terraform zip
	empty := t.TempDir()
	os.WriteFile(filepath.Join(empty, "SHA256SUMS"), nil, 0o644)
	if _, err := Resolve(context.Background(), fakeHTTP{}, "", empty); err == nil {
		t.Fatal("a directory without the terraform zip must fail")
	}

	// too old
	old := writeLocal(t, "0.10.0", nil)
	if _, err := Resolve(context.Background(), fakeHTTP{}, "", old); err == nil || !strings.Contains(err.Error(), "0.11.0 or later") {
		t.Fatalf("got %v", err)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.10.0", "0.11.0", -1},
		{"0.11.0", "0.10.0", 1},
		{"0.11.0-dev", "0.11.0", -1},
		{"0.9.9", "0.10.0", -1},
		{"0.11.0", "0.11.0", 0},
		{"0.11.0-a", "0.11.0-b", -1},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestAtLeast(t *testing.T) {
	if !AtLeast("0.11.0-dev", "0.11.0") || !AtLeast("0.11.0", "0.11.0") || AtLeast("0.10.9", "0.11.0") {
		t.Fatal("AtLeast")
	}
}
