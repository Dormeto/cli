package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ReleasesURL is where the manager's releases are published.
const ReleasesURL = "https://releases.dormeto.com/manager"

// MinVersion is the first manager release `dormeto install` can install.
const MinVersion = "0.11.0"

// SelfUpdateVersion is the first manager release with the module's install input (the dashboard's updates).
const SelfUpdateVersion = "0.12.0"

// Release is the manager release to install, resolved and checked against its SHA256SUMS.
type Release struct {
	Version string            // 0.11.0, or 0.11.0-dev for a local build
	Base    string            // ReleasesURL/<version>; empty for a local release (the bucket's copy is used)
	Sums    map[string]string // file name → sha256 (hex), from SHA256SUMS
	Local   string            // the local directory, or empty
}

var (
	managerVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	sumLineRe        = regexp.MustCompile(`^([0-9a-fA-F]{64}) [ *](\S.*)$`)
	zipRe            = regexp.MustCompile(`^terraform-aws-dormeto-(.+)\.zip$`)
)

// get reads one URL; any status but 200 is an error naming the URL and the status (notFound: true for a 404).
func get(ctx context.Context, h HTTP, url string) (body []byte, notFound bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := h.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// releases.dormeto.com is a private bucket behind CloudFront: a file it does not have answers 403, not 404.
		missing := resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden
		return nil, missing, fmt.Errorf("GET %s: %d %s", url, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("GET %s: %w", url, err)
	}
	return body, false, nil
}

// Latest is the version latest.json names.
func Latest(ctx context.Context, h HTTP) (string, error) {
	body, _, err := get(ctx, h, ReleasesURL+"/latest.json")
	if err != nil {
		return "", err
	}
	var l struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &l); err != nil {
		return "", fmt.Errorf("%s/latest.json: %w", ReleasesURL, err)
	}
	if l.Version == "" {
		return "", fmt.Errorf("%s/latest.json has no version", ReleasesURL)
	}
	return l.Version, nil
}

// Resolve reads and checks the release to install: version on releases.dormeto.com, or the local directory when local
// is set (version, when given, must then be the directory's).
func Resolve(ctx context.Context, h HTTP, version, local string) (Release, error) {
	var r Release
	var err error
	if local == "" {
		r, err = resolvePublished(ctx, h, version)
	} else {
		r, err = resolveLocal(version, local)
	}
	if err != nil {
		return Release{}, err
	}
	if !AtLeast(r.Version, MinVersion) {
		return Release{}, fmt.Errorf("dormeto install needs manager %s or later (asked: %s)", MinVersion, r.Version)
	}
	if missing := r.missing(); len(missing) > 0 {
		return Release{}, fmt.Errorf("manager %s: SHA256SUMS does not list %s", r.Version, strings.Join(missing, ", "))
	}
	return r, nil
}

// NotPublishedError is a version releases.dormeto.com does not have; its message says how to install a release built
// locally instead.
type NotPublishedError struct{ Version string }

// Short is the fact alone, for a caller that gives its own advice.
func (e *NotPublishedError) Short() string {
	return fmt.Sprintf("manager %s is not published on releases.dormeto.com", e.Version)
}

func (e *NotPublishedError) Error() string {
	return fmt.Sprintf("%s: for a release built locally, --release <dir> (in the manager: node scripts/release/release.mjs --version %s --out <dir>)", e.Short(), e.Version)
}

func resolvePublished(ctx context.Context, h HTTP, version string) (Release, error) {
	if !managerVersionRe.MatchString(version) {
		return Release{}, fmt.Errorf("%q is not a manager version (like 0.11.0)", version)
	}
	base := ReleasesURL + "/" + version
	body, notFound, err := get(ctx, h, base+"/SHA256SUMS")
	if notFound {
		return Release{}, &NotPublishedError{Version: version}
	}
	if err != nil {
		return Release{}, err
	}
	sums, err := parseSums(string(body))
	if err != nil {
		return Release{}, fmt.Errorf("%s/SHA256SUMS: %w", base, err)
	}
	return Release{Version: version, Base: base, Sums: sums}, nil
}

func resolveLocal(version, dir string) (Release, error) {
	body, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		return Release{}, fmt.Errorf("local release %s: %w", dir, err)
	}
	sums, err := parseSums(string(body))
	if err != nil {
		return Release{}, fmt.Errorf("%s/SHA256SUMS: %w", dir, err)
	}
	var versions []string
	for name := range sums {
		if m := zipRe.FindStringSubmatch(name); m != nil {
			versions = append(versions, m[1])
		}
	}
	if len(versions) != 1 {
		return Release{}, fmt.Errorf("local release %s: expected one terraform-aws-dormeto-<version>.zip in SHA256SUMS, found %d", dir, len(versions))
	}
	if version != "" && version != versions[0] {
		return Release{}, fmt.Errorf("--version %s does not match the local release %s (%s)", version, dir, versions[0])
	}
	r := Release{Version: versions[0], Sums: sums, Local: dir}
	for _, name := range r.Files() {
		if name == "SHA256SUMS" {
			continue
		}
		got, err := fileSum(filepath.Join(dir, name))
		if err != nil {
			return Release{}, fmt.Errorf("local release %s: %s: %w", dir, name, err)
		}
		if got != sums[name] {
			return Release{}, fmt.Errorf("local release %s: %s does not match its SHA256SUMS line", dir, name)
		}
	}
	return r, nil
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// parseSums reads `<64 hex>  <name>` lines (also `<hex> *<name>`); sums are lower-cased.
func parseSums(s string) (map[string]string, error) {
	sums := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := sumLineRe.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("unexpected line %q", line)
		}
		sums[m[2]] = strings.ToLower(m[1])
	}
	return sums, nil
}

// Required is the files the install cannot go without.
func (r Release) Required() []string {
	return []string{
		"terraform-aws-dormeto-" + r.Version + ".zip",
		"dormeto-access-commands-" + r.Version + ".sh",
		"dormeto-organization-policy.json",
	}
}

func (r Release) missing() []string {
	var m []string
	for _, f := range r.Required() {
		if _, ok := r.Sums[f]; !ok {
			m = append(m, f)
		}
	}
	return m
}

// Files is the files SHA256SUMS lists, sorted, then SHA256SUMS itself (what a local release uploads).
func (r Release) Files() []string {
	files := make([]string, 0, len(r.Sums)+1)
	for f := range r.Sums {
		files = append(files, f)
	}
	sort.Strings(files)
	return append(files, "SHA256SUMS")
}

// AtLeast reports whether version's major.minor.patch reaches floor's; a pre-release suffix is ignored.
func AtLeast(version, floor string) bool {
	a, _ := splitVersion(version)
	b, _ := splitVersion(floor)
	return compareCore(a, b) >= 0
}

// CompareVersions is semver precedence: major.minor.patch numerically, then a pre-release sorts before its release,
// pre-releases compared as strings.
func CompareVersions(a, b string) int {
	ac, ap := splitVersion(a)
	bc, bp := splitVersion(b)
	if c := compareCore(ac, bc); c != 0 {
		return c
	}
	switch {
	case ap == bp:
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	}
	return strings.Compare(ap, bp)
}

func splitVersion(v string) (core [3]int, pre string) {
	v, pre, _ = strings.Cut(v, "-")
	for i, p := range strings.SplitN(v, ".", 3) {
		core[i], _ = strconv.Atoi(p)
	}
	return core, pre
}

func compareCore(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
