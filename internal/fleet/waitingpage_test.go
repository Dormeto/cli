package fleet

// The waiting page's setting as the commands write and print it: the icon and the HTML checked before any call, the
// view that never prints an icon or a page.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

var (
	pngBytes  = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}
	jpegBytes = []byte{0xff, 0xd8, 0xff, 0xe0, 1}
	webpBytes = append(append([]byte("RIFF\x04\x00\x00\x00"), "WEBP"...), 1)
	icoBytes  = []byte{0, 0, 1, 0, 1, 0}
)

func TestIconTypeFromTheBytesSignature(t *testing.T) {
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"png", pngBytes, "png"},
		{"jpeg", jpegBytes, "jpeg"},
		{"webp", webpBytes, "webp"},
		{"ico", icoBytes, "x-icon"},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "svg+xml"},
		{"svg after a BOM, a declaration, a comment and a doctype", []byte("\ufeff <?xml version=\"1.0\"?>\n<!-- c -->\n<!DOCTYPE svg PUBLIC \"a\" \"b\" [<!ENTITY x \"y\">]>\n<svg>"), "svg+xml"},
		{"svg closing at once", []byte("<svg/>"), "svg+xml"},
		{"html is no svg", []byte("<html><svg></svg></html>"), ""},
		{"svgx is no svg", []byte("<svgx>"), ""},
		{"gif", []byte("GIF89a"), ""},
		{"riff that is not webp", []byte("RIFF\x00\x00\x00\x00WAVE"), ""},
		{"empty", nil, ""},
		{"doctype's quoted > does not end it", []byte(`<!DOCTYPE svg PUBLIC "a>b"><svg/>`), "svg+xml"},
		{"a quoted > then no svg", []byte(`<!DOCTYPE svg PUBLIC "a>b"><p/>`), ""},
		{"xml-foo is no declaration", []byte(`<?xml-foo?><svg/>`), ""},
		{"unclosed comment", []byte(`<!-- <svg/>`), ""},
		{"unclosed declaration", []byte(`<?xml version="1.0" <svg/>`), ""},
		{"unclosed doctype", []byte(`<!DOCTYPE svg <svg/>`), ""},
	} {
		if got := IconType(c.data); got != c.want {
			t.Errorf("%s: type %q, want %q", c.name, got, c.want)
		}
	}
}

func TestIconDataURI(t *testing.T) {
	got, err := IconDataURI(pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	if want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// 64 KiB exactly is allowed; one more byte is not.
	edge := append(append([]byte{}, pngBytes...), make([]byte, MaxIconBytes-len(pngBytes))...)
	if _, err := IconDataURI(edge); err != nil {
		t.Errorf("64 KiB: %v", err)
	}
	if _, err := IconDataURI(append(edge, 0)); err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Errorf("65 KiB: %v", err)
	}
	if _, err := IconDataURI(nil); err == nil {
		t.Error("an empty file is refused")
	}
	if _, err := IconDataURI([]byte("GIF89a")); err == nil || !strings.Contains(err.Error(), "PNG, JPEG, WebP, SVG or ICO") {
		t.Errorf("a GIF: %v", err)
	}
}

func TestCheckHTML(t *testing.T) {
	if s, err := CheckHTML([]byte("<!doctype html><p>hi</p>")); err != nil || s != "<!doctype html><p>hi</p>" {
		t.Errorf("a page: %q, %v", s, err)
	}
	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"blank":          {[]byte(" \n\t "), "blank"},
		"only a BOM":     {[]byte("\ufeff"), "blank"},
		"empty":          {nil, "blank"},
		"BOM and spaces": {[]byte("\n\ufeff \ufeff"), "blank"},
		"BOM twice":      {[]byte("\ufeff\ufeff"), "blank"},
		"not UTF-8":      {[]byte{0xff, 0xfe, 'a'}, "UTF-8"},
		"too large":      {bytes.Repeat([]byte("a"), MaxHTMLBytes+1), "256 KiB"},
	} {
		if _, err := CheckHTML(c.data); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want a sentence with %q", name, err, c.want)
		}
	}
	// U+0085 is not white space to the router (JavaScript's trim), so it is a page; a BOM before a page is kept.
	if _, err := CheckHTML([]byte("\u0085")); err != nil {
		t.Errorf("U+0085 alone is a page for the router: %v", err)
	}
	if s, err := CheckHTML([]byte("\ufeff<p>")); err != nil || s != "\ufeff<p>" {
		t.Errorf("a BOM is kept: %q, %v", s, err)
	}
	if _, err := CheckHTML(bytes.Repeat([]byte("a"), MaxHTMLBytes)); err != nil {
		t.Errorf("256 KiB exactly: %v", err)
	}
	// The limit counts bytes, not characters: 3 bytes each.
	if _, err := CheckHTML([]byte(strings.Repeat("€", MaxHTMLBytes/3+1))); err == nil {
		t.Error("the limit is in bytes")
	}
}

func TestPageDocMarshalsOnlyWhatIsSet(t *testing.T) {
	title := "Acme"
	data, _ := json.Marshal(PageDoc{Mode: "template", Title: &title})
	if string(data) != `{"mode":"template","title":"Acme"}` {
		t.Errorf("got %s", data)
	}
}

func fixture(t *testing.T, name string) PageView {
	t.Helper()
	data, err := os.ReadFile("../../testdata/waitingpage/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var v PageView
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

var utc = time.UTC

// An answer of the manager's API, as it sends it (testdata/waitingpage).
func TestPageViewOfATemplateWorkload(t *testing.T) {
	got := fixture(t, "wl-template").Text("prod-eu/w-1", "workload", utc)
	for _, want := range []string{
		"prod-eu/w-1: waiting page, template (from the workload)",
		"icon         PNG, ",
		"title        Acme (from the workload)",
		"description  Router words (from the router)",
		"favicon      ",
		"set on this workload by arn:aws:iam::121014831971:user/cli, 2023-11-14 22:13 UTC",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "base64") || strings.Contains(got, "iVBOR") || strings.Contains(got, "data:") {
		t.Errorf("an icon is printed:\n%s", got)
	}
}

func TestPageViewNeverPrintsAPageOrAnIconAtAnyLevel(t *testing.T) {
	icon := "data:image/png;base64,iVBORw0KGgo="
	for name, v := range map[string]PageView{
		"workload custom":     fixture(t, "wl-custom"),
		"organization custom": fixture(t, "org-custom"),
		"organization icon":   fixture(t, "org-template-put"),
		"router":              fixture(t, "rt-template"),
		"own only, icon":      {Own: &PageDoc{Mode: "template", Icon: &icon}},
	} {
		got := v.Text("x", "organization", utc) + v.Text("x", "workload", utc) + v.Text("x", "router", utc)
		for _, bad := range []string{"SECRET-HTML", "<p>", "base64", "iVBOR", "data:image"} {
			if strings.Contains(got, bad) {
				t.Errorf("%s: prints %q:\n%s", name, bad, got)
			}
		}
	}
	html := "<p>SECRET-HTML</p>"
	got := PageView{Page: &PageDoc{Mode: "custom", HTML: &html}}.Text("the organization", "organization", utc)
	if !strings.Contains(got, "custom page, 18 bytes") || strings.Contains(got, "SECRET") {
		t.Errorf("an organization's page:\n%s", got)
	}
}

func TestPageViewOfACustomWorkload(t *testing.T) {
	got := fixture(t, "wl-custom").Text("prod-eu/w-1", "workload", utc)
	for _, want := range []string{"waiting page, custom (from the workload)", "custom page, 18 bytes (from the workload)", "warning: the page has no element data-dormeto", "favicon  none: no host name (tried 2027-01-15 08:00 UTC)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestPageViewFavicon(t *testing.T) {
	if got := fixture(t, "wl-none").Text("w", "workload", utc); !strings.Contains(got, "favicon  not read yet") || !strings.Contains(got, "no document at this level") {
		t.Errorf("never read:\n%s", got)
	}
	if got := fixture(t, "wl-favicon-failed").Text("w", "workload", utc); !strings.Contains(got, "favicon  none: no host name (tried 2027-01-15 08:00 UTC)") {
		t.Errorf("failed:\n%s", got)
	}
	v := fixture(t, "wl-none")
	icon := "data:image/png;base64,iVBORw0KGgo="
	err := "timeout"
	v.Favicon = &struct {
		Icon      *string `json:"icon"`
		FetchedAt string  `json:"fetchedAt"`
		Error     *string `json:"error"`
	}{&icon, "2026-11-01T08:00:00Z", &err}
	if got := v.Text("w", "workload", utc); !strings.Contains(got, "favicon  PNG, 8 bytes, read 2026-11-01 08:00 UTC; last read failed: timeout") {
		t.Errorf("kept and failed since:\n%s", got)
	}
	// A router has no favicon line.
	if got := fixture(t, "rt-template").Text("prod-eu", "router", utc); strings.Contains(got, "favicon") {
		t.Errorf("a router has no favicon:\n%s", got)
	}
}

func TestPageViewTerraformOnlyAtAWorkload(t *testing.T) {
	v := fixture(t, "wl-none")
	v.Terraform = true
	if got := v.Text("w", "workload", utc); !strings.Contains(got, "written by Terraform: set waiting_page in its workload module") {
		t.Errorf("no word of Terraform:\n%s", got)
	}
	if got := v.Text("r", "router", utc); strings.Contains(got, "Terraform") {
		t.Errorf("Terraform at a router:\n%s", got)
	}
}

func TestPageViewOfTheOrganization(t *testing.T) {
	got := fixture(t, "org-template").Text("the organization", "organization", utc)
	for _, want := range []string{"the organization: waiting page, template (from the organization)", "icon   PNG, 108 bytes (from the organization)", "title  Org (from the organization)",
		"set on the organization by arn:aws:iam::121014831971:user/cli, 2027-01-15 08:00 UTC"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	title := "Org"
	got = PageView{Page: &PageDoc{Mode: "template", Title: &title}}.Text("the organization", "organization", utc)
	if !strings.Contains(got, "icon   auto (from built-in default)") || !strings.Contains(got, "title  Org (from the organization)") {
		t.Errorf("no icon of its own: the effective one is auto:\n%s", got)
	}
	got = fixture(t, "org-custom").Text("the organization", "organization", utc)
	for _, want := range []string{"waiting page, custom", "custom page, 18 bytes", "warning: "} {
		if !strings.Contains(got, want) {
			t.Errorf("custom: missing %q in\n%s", want, got)
		}
	}
	got = fixture(t, "org-none").Text("the organization", "organization", utc)
	if !strings.HasPrefix(got, "the organization: no waiting page set\n") || !strings.Contains(got, "auto (from built-in default)") || strings.Contains(got, "set on") {
		t.Errorf("none set:\n%s", got)
	}
}

func TestPageViewTellsUnreadableFromNoneSet(t *testing.T) {
	got := fixture(t, "org-unreadable").Text("the organization", "organization", utc)
	if !strings.Contains(got, "settings.json is unreadable") || strings.Contains(got, "no waiting page set") || strings.Contains(got, "keep") {
		t.Errorf("unreadable:\n%s", got)
	}
}

func TestIconTextNeverPrintsWhatItDoesNotKnow(t *testing.T) {
	for in, want := range map[string]string{
		"auto": "auto", "none": "none", "": "",
		"data:image/svg+xml,%3Csvg%3E%3C/svg%3E":   "other, 38 bytes",
		"https://x.io/" + strings.Repeat("a", 500): "other, 513 bytes",
		"data:image/png;base64,iVBORw0KGgo=":       "PNG, 8 bytes",
	} {
		if got := IconText(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
