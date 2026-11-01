package fleet

// The waiting page's setting as the commands write and print it: the icon and the HTML checked before any call, the
// view that never prints an icon or a page.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
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
		"blank":      {[]byte(" \n\t "), "blank"},
		"only a BOM": {[]byte("\ufeff"), "blank"},
		"empty":      {nil, "blank"},
		"not UTF-8":  {[]byte{0xff, 0xfe, 'a'}, "UTF-8"},
		"too large":  {bytes.Repeat([]byte("a"), MaxHTMLBytes+1), "256 KiB"},
	} {
		if _, err := CheckHTML(c.data); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want a sentence with %q", name, err, c.want)
		}
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

func view(t *testing.T, raw string) PageView {
	t.Helper()
	var v PageView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

var utc = time.UTC

func TestPageViewOfATemplateWorkload(t *testing.T) {
	icon := "data:image/png;base64," + base64.StdEncoding.EncodeToString(append(pngBytes, make([]byte, 2000)...))
	v := view(t, `{"own":{"mode":"template","title":"Acme portal"},"inherited":{"mode":"template","icon":"auto","from":{"mode":"organization","icon":"default"}},
"effective":{"mode":"template","icon":"`+icon+`","title":"Acme portal","description":"Sleeps at night.\nBack at 8.","from":{"mode":"workload","icon":"router","title":"workload","description":"organization"}},
"favicon":{"icon":"`+icon+`","fetchedAt":"2026-11-01T08:00:00.000Z","error":null},"warnings":[],"terraform":false,"by":"ann@x.io","at":"2026-11-01T09:30:00.000Z"}`)
	got := v.Text("prod-eu/w-1", "workload", utc)
	for _, want := range []string{
		"prod-eu/w-1: waiting page, template (from the workload)",
		"icon         PNG, 2.0 KiB (from the router)",
		"title        Acme portal (from the workload)",
		"description  Sleeps at night.\n", "Back at 8. (from the organization)",
		"favicon      PNG, 2.0 KiB, read 2026-11-01 08:00 UTC",
		"set on this workload by ann@x.io, 2026-11-01 09:30 UTC",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "base64") || strings.Contains(got, "iVBOR") {
		t.Errorf("an icon is printed:\n%s", got)
	}
}

func TestPageViewOfACustomPageNeverPrintsIt(t *testing.T) {
	html := "<!doctype html><h1>SECRET-MARKER</h1>" + strings.Repeat("x", 2000)
	raw, _ := json.Marshal(map[string]any{
		"own": map[string]any{"mode": "custom", "html": html}, "effective": map[string]any{"mode": "custom", "html": html, "from": map[string]string{"mode": "router", "html": "router"}},
		"warnings": []string{"the page has no element with data-dormeto=\"status\""}, "terraform": false, "by": "root", "at": "2026-11-01T09:30:00Z"})
	got := view(t, string(raw)).Text("prod-eu", "router", utc)
	for _, want := range []string{"prod-eu: waiting page, custom (from the router)", "html", "custom page, 2.0 KiB (from the router)", "warning", "data-dormeto", "set on this router by root"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "SECRET-MARKER") {
		t.Errorf("the page is printed:\n%s", got)
	}
}

func TestPageViewFaviconStates(t *testing.T) {
	base := `{"own":null,"effective":{"mode":"template","icon":"auto","from":{"mode":"default","icon":"default"}},"warnings":[],"terraform":false,"by":null,"at":null,"favicon":`
	for name, c := range map[string]struct{ favicon, want string }{
		"never read":   {`null`, "favicon  not read yet"},
		"failed":       {`{"icon":null,"fetchedAt":"2026-11-01T08:00:00Z","error":"no host name"}`, "favicon  none: no host name (tried 2026-11-01 08:00 UTC)"},
		"kept and old": {`{"icon":"data:image/png;base64,iVBORw0KGgo=","fetchedAt":"2026-11-01T08:00:00Z","error":"timeout"}`, "last read failed: timeout"},
	} {
		got := view(t, base+c.favicon+`}`).Text("prod-eu/w-1", "workload", utc)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: missing %q in\n%s", name, c.want, got)
		}
		if !strings.Contains(got, "no document at this level") {
			t.Errorf("%s: says nothing of the level:\n%s", name, got)
		}
	}
	// A router has no favicon line.
	if got := view(t, base+`null}`).Text("prod-eu", "router", utc); strings.Contains(got, "favicon") {
		t.Errorf("a router has no favicon:\n%s", got)
	}
}

func TestPageViewTerraformAndOrganization(t *testing.T) {
	got := view(t, `{"own":{"mode":"template","title":"T"},"effective":{"mode":"template","icon":"auto","title":"T","from":{"mode":"workload","icon":"default","title":"workload"}},"terraform":true,"warnings":[]}`).Text("prod-eu/w-1", "workload", utc)
	if !strings.Contains(got, "written by Terraform: set waiting_page in its workload module") {
		t.Errorf("no word of Terraform:\n%s", got)
	}
	// The organization's answer carries its document as page, and no effective page.
	got = view(t, `{"page":{"mode":"template","icon":"none","description":"Hi"},"by":"ann@x.io","at":"2026-11-01T09:30:00Z","warnings":[]}`).Text("the organization", "organization", utc)
	for _, want := range []string{"the organization: waiting page, template", "icon         none", "description  Hi", "set on the organization by ann@x.io"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if got := view(t, `{"page":null}`).Text("the organization", "organization", utc); !strings.Contains(got, "no waiting page set") {
		t.Errorf("nothing set:\n%s", got)
	}
}
