package fleet

// The waiting page's setting at the organization, router and workload levels as the commands write and print it: the
// icon and the HTML checked before any call (the router's rules, spec 2026-11-01-branded-waiting-page §4), and the view
// that says the effective values and where each comes from, an icon or a page only as its type and size.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxIconBytes is the largest icon, decoded: 64 KiB.
	MaxIconBytes = 64 << 10
	// MaxHTMLBytes is the largest custom page, in UTF-8 bytes: 256 KiB.
	MaxHTMLBytes = 256 << 10
)

// PageDoc is a level's document as it is written: a template (icon, title, description) or a custom page (html). A nil
// field is left out of the JSON.
type PageDoc struct {
	Mode        string  `json:"mode"`
	Icon        *string `json:"icon,omitempty"`
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	HTML        *string `json:"html,omitempty"`
}

func startsWith(b []byte, sig ...byte) bool {
	return len(b) >= len(sig) && string(b[:len(sig)]) == string(sig)
}

// IconType is the image type of the bytes by their signature, as the data URI names it ("png", "jpeg", "webp",
// "x-icon", "svg+xml"); "" for none of the five. The same rules as the router's.
func IconType(b []byte) string {
	switch {
	case startsWith(b, 0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a):
		return "png"
	case startsWith(b, 0xff, 0xd8, 0xff):
		return "jpeg"
	case startsWith(b, 'R', 'I', 'F', 'F') && len(b) >= 12 && string(b[8:12]) == "WEBP":
		return "webp"
	case startsWith(b, 0, 0, 1, 0):
		return "x-icon"
	case startsWithSVG(string(b)):
		return "svg+xml"
	}
	return ""
}

func isBlank(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	switch s[i] {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	}
	return strings.HasPrefix(s[i:], "\ufeff")
}

func blankLen(s string, i int) int {
	if strings.HasPrefix(s[i:], "\ufeff") {
		return len("\ufeff")
	}
	return 1
}

// doctypeEnd is the index after the '>' of the DOCTYPE at i, skipping quoted strings and an internal subset; -1 if unclosed.
func doctypeEnd(s string, i int) int {
	var quote byte
	depth := 0
	for j := i + 9; j < len(s); j++ {
		c := s[j]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[':
			depth++
		case c == ']':
			depth = max(0, depth-1)
		case c == '>' && depth == 0:
			return j + 1
		}
	}
	return -1
}

// startsWithSVG tells whether the first element of an XML text is <svg, after any BOM, white space, XML declaration,
// comments and DOCTYPE.
func startsWithSVG(s string) bool {
	i := 0
	for {
		for i < len(s) && isBlank(s, i) {
			i += blankLen(s, i)
		}
		switch {
		case strings.HasPrefix(s[i:], "<!--"):
			end := strings.Index(s[i+4:], "-->")
			if end < 0 {
				return false
			}
			i += 4 + end + 3
		case strings.HasPrefix(s[i:], "<?xml") && isBlank(s, i+5):
			end := strings.Index(s[i+5:], "?>")
			if end < 0 {
				return false
			}
			i += 5 + end + 2
		case len(s) >= i+9 && strings.EqualFold(s[i:i+9], "<!doctype"):
			if i = doctypeEnd(s, i); i < 0 {
				return false
			}
		default:
			if !strings.HasPrefix(s[i:], "<svg") {
				return false
			}
			return isBlank(s, i+4) || (i+4 < len(s) && (s[i+4] == '/' || s[i+4] == '>'))
		}
	}
}

// IconDataURI is the data URI of an icon file's bytes, its type from the signature: at most 64 KiB, one of PNG, JPEG,
// WebP, SVG and ICO.
func IconDataURI(b []byte) (string, error) {
	switch typ := IconType(b); {
	case len(b) == 0:
		return "", errors.New("the icon file is empty")
	case len(b) > MaxIconBytes:
		return "", errors.New("the icon is larger than 64 KiB")
	case typ == "":
		return "", errors.New("the icon is not a PNG, JPEG, WebP, SVG or ICO image")
	default:
		return "data:image/" + typ + ";base64," + base64.StdEncoding.EncodeToString(b), nil
	}
}

// isJSSpace is what JavaScript's trim() removes, which is what the router's blank test removes: Unicode white space
// except U+0085, and the byte order mark.
func isJSSpace(r rune) bool { return r == '\ufeff' || (r != '\u0085' && unicode.IsSpace(r)) }

// CheckHTML is the custom page of the bytes read: UTF-8, not blank, at most 256 KiB.
func CheckHTML(b []byte) (string, error) {
	switch {
	case len(b) > MaxHTMLBytes:
		return "", errors.New("the page is larger than 256 KiB")
	case !utf8.Valid(b):
		return "", errors.New("the page is not UTF-8 text")
	case strings.TrimFunc(string(b), isJSSpace) == "":
		return "", errors.New("the page is blank")
	}
	return string(b), nil
}

// EffectivePage is the page a level would show, with the level each value comes from.
type EffectivePage struct {
	Mode        string            `json:"mode"`
	Icon        string            `json:"icon"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	HTML        string            `json:"html"`
	From        map[string]string `json:"from"`
}

// CustomPageOff is the warning of a Google Cloud router whose custom pages are off, for a custom page it holds: stored, not served.
const CustomPageOff = "custom pages are off on this router: this page is not served"

// PageView is the manager's answer for a level, as a router 0.35.0 reports it. The organization's carries its document as
// Page (and no effective page).
type PageView struct {
	Own       *PageDoc       `json:"own"`
	Page      *PageDoc       `json:"page"`
	Effective *EffectivePage `json:"effective"`
	Favicon   *struct {
		Icon      *string `json:"icon"`
		FetchedAt string  `json:"fetchedAt"`
		Error     *string `json:"error"`
	} `json:"favicon"`
	Warnings  []string `json:"warnings"`
	Terraform bool     `json:"terraform"`
	By        string   `json:"by"`
	At        string   `json:"at"`
	// Error is said when the organization's settings.json cannot be read; Note is what a write adds (when routers apply it).
	Error string `json:"error"`
	Note  string `json:"note"`
}

var iconURI = regexp.MustCompile(`^data:image/(png|jpeg|webp|svg\+xml|x-icon);base64,`)

var iconNames = map[string]string{"png": "PNG", "jpeg": "JPEG", "webp": "WebP", "svg+xml": "SVG", "x-icon": "ICO"}

// sizeText is a size as "37 bytes" or "2.0 KiB".
func sizeText(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d %s", n, Plural(n, "byte"))
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/1024)
}

// IconText is an icon value as it is printed: auto and none as they are, a data URI as its type and size.
func IconText(icon string) string {
	if icon == "auto" || icon == "none" || icon == "" {
		return icon
	}
	m := iconURI.FindStringSubmatch(icon)
	if m == nil {
		return "other, " + sizeText(len(icon))
	}
	n := len(icon) - len(m[0])
	if data, err := base64.StdEncoding.DecodeString(icon[len(m[0]):]); err == nil {
		n = len(data)
	}
	return iconNames[m[1]] + ", " + sizeText(n)
}

func levelText(level string) string {
	if level == "default" {
		return "built-in default"
	}
	return "the " + level
}

// ownDoc is the level's own document: own for a router or a workload, page for the organization.
func (v PageView) ownDoc() *PageDoc {
	if v.Own != nil {
		return v.Own
	}
	return v.Page
}

// effective is the page the level shows. The manager's answer carries it for a router and a workload; the organization's
// is made here from its page alone (it is the top level): the icon is auto unless it says, the rest is what it has.
func (v PageView) effective() *EffectivePage {
	if v.Effective != nil {
		return v.Effective
	}
	e := &EffectivePage{Mode: "template", Icon: "auto", From: map[string]string{"mode": "default", "icon": "default"}}
	own := v.ownDoc()
	if own == nil {
		return e
	}
	e.Mode, e.From = own.Mode, map[string]string{"mode": "organization", "icon": "default"}
	if own.Mode == "custom" {
		e.Icon, e.HTML = "", derefOr(own.HTML)
		e.From["html"] = "organization"
		return e
	}
	for _, f := range []struct {
		key string
		v   *string
		to  *string
	}{{"icon", own.Icon, &e.Icon}, {"title", own.Title, &e.Title}, {"description", own.Description, &e.Description}} {
		if f.v != nil {
			*f.to, e.From[f.key] = *f.v, "organization"
		}
	}
	return e
}

func derefOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Text is the view of a level ("workload", "router" or "organization") under label: the effective values and where each
// comes from, for a workload the cached favicon, then who set this level and the warnings. Times are in loc.
func (v PageView) Text(label, level string, loc *time.Location) string {
	own := v.ownDoc()
	if v.Error != "" { // the organization's settings.json could not be read: nothing is known of its page
		return fmt.Sprintf("%s: the waiting page is not known: %s\n", label, v.Error)
	}
	var b strings.Builder
	e := v.effective()
	from := func(key string) string {
		if e.From[key] == "" {
			return ""
		}
		return " (from " + levelText(e.From[key]) + ")"
	}
	if own == nil && level == "organization" {
		fmt.Fprintf(&b, "%s: no waiting page set\n", label)
	} else {
		fmt.Fprintf(&b, "%s: waiting page, %s%s\n", label, e.Mode, from("mode"))
	}
	var rows [][2]string
	if e.Mode == "custom" {
		rows = append(rows, [2]string{"html", "custom page, " + sizeText(len(e.HTML)) + from("html")})
	} else {
		rows = append(rows, [2]string{"icon", IconText(e.Icon) + from("icon")})
		if e.Title != "" {
			rows = append(rows, [2]string{"title", e.Title + from("title")})
		}
		if e.Description != "" {
			rows = append(rows, [2]string{"description", e.Description + from("description")})
		}
	}
	if own != nil && own.Mode == "custom" && slices.Contains(v.Warnings, CustomPageOff) {
		rows = append(rows, [2]string{"page", "off: custom page, " + sizeText(len(derefOr(own.HTML)))})
	}
	if level == "workload" {
		rows = append(rows, [2]string{"favicon", v.faviconText(loc)})
	}
	b.WriteString(Labelled(rows))
	if own == nil && level != "organization" {
		b.WriteString("no document at this level: it shows what it inherits\n")
	}
	if own != nil {
		where := "the " + level
		if level != "organization" {
			where = "this " + level
		}
		line := "set on " + where
		var by []string
		for _, p := range []string{v.By, When(v.At, loc)} {
			if p != "" {
				by = append(by, p)
			}
		}
		if len(by) > 0 {
			line += " by " + strings.Join(by, ", ")
		}
		b.WriteString(line + "\n")
	}
	if v.Terraform && level == "workload" {
		b.WriteString("written by Terraform: set waiting_page in its workload module (it cannot be changed here)\n")
	}
	for _, w := range v.Warnings {
		b.WriteString("warning: " + w + "\n")
	}
	return b.String()
}

// faviconText is the workload's cached favicon: its type, size and date, or why there is none.
func (v PageView) faviconText(loc *time.Location) string {
	f := v.Favicon
	if e := v.effective(); f == nil && (e.Mode == "custom" || e.Icon != "auto") {
		return "not used: this page doesn't show it"
	}
	if f == nil {
		return "not read yet (a workload's favicon is read before its next nap)"
	}
	when := When(f.FetchedAt, loc)
	var text string
	switch {
	case f.Icon != nil && *f.Icon != "":
		text = IconText(*f.Icon)
		if when != "" {
			text += ", read " + when
		}
		if f.Error != nil && *f.Error != "" {
			text += "; last read failed: " + *f.Error
		}
		return text
	case f.Error != nil && *f.Error != "":
		text = "none: " + *f.Error
	default:
		text = "none"
	}
	if when != "" {
		text += " (tried " + when + ")"
	}
	return text
}
