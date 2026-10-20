package fleet

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Selection is which routers a command acts on: the names given (upgrade) narrowed by the filters. Accounts and
// RouterRegions are ORed within each, every filter ANDed with the others; Tags keeps the routers that serve a workload
// carrying all of them (the caller asks the manager for those workloads and passes their routers to Select).
type Selection struct {
	Names, Accounts, RouterRegions []string
	NameGlob                       string // *, ? and […] on the router's name
	Tags                           map[string]string
	All                            bool
}

// filtered tells whether any filter (not a name) is set.
func (s Selection) filtered() bool {
	return len(s.Accounts) > 0 || len(s.RouterRegions) > 0 || s.NameGlob != "" || len(s.Tags) > 0
}

// Validate refuses what cannot be selected. An upgrade must name routers, filter them or take --all, and --all takes
// neither: an upgrade of everything is never the default of an empty command line.
func (s Selection) Validate(forUpgrade bool) error {
	if _, err := path.Match(s.NameGlob, ""); err != nil {
		return fmt.Errorf("--name: bad pattern %q", s.NameGlob)
	}
	if !forUpgrade {
		return nil
	}
	switch named := len(s.Names) > 0 || s.filtered(); {
	case s.All && named:
		return errors.New("--all takes no router names nor filters")
	case !s.All && !named:
		return errors.New("name routers, filter them or use --all")
	}
	return nil
}

// Select is the routers of rs the selection keeps, in rs's order, each once (a name given twice, or named and matched
// by a filter, counts once). tagged is the set of routers serving workloads that carry Tags. A name that is no
// router's is an error.
func (s Selection) Select(rs []Router, tagged map[string]bool) ([]Router, error) {
	for _, n := range s.Names {
		if !slices.ContainsFunc(rs, func(r Router) bool { return r.Name == n }) {
			return nil, fmt.Errorf("unknown router %q", n)
		}
	}
	var out []Router
	for _, r := range rs {
		switch {
		case len(s.Names) > 0 && !slices.Contains(s.Names, r.Name):
		case len(s.Accounts) > 0 && !slices.Contains(s.Accounts, r.Account):
		case len(s.RouterRegions) > 0 && !slices.Contains(s.RouterRegions, r.Region):
		case s.NameGlob != "" && !globs(s.NameGlob, r.Name):
		case len(s.Tags) > 0 && !tagged[r.Name]:
		default:
			out = append(out, r)
		}
	}
	return out, nil
}

func globs(pattern, name string) bool {
	ok, _ := path.Match(pattern, name) // the pattern was validated
	return ok
}

// ParseTags reads --tag values, key=value: the value may hold "=" but no comma (API Gateway joins repeated parameters
// with commas and the manager splits them there), and a key is given once.
func ParseTags(values []string) (map[string]string, error) {
	tags := map[string]string{}
	for _, v := range values {
		k, val, ok := strings.Cut(v, "=")
		switch {
		case !ok || strings.TrimSpace(k) == "":
			return nil, fmt.Errorf("--tag: %q is not key=value", v)
		case strings.Contains(v, ","):
			return nil, fmt.Errorf("--tag: %q holds a comma, which a tag cannot", v)
		}
		if _, dup := tags[k]; dup {
			return nil, fmt.Errorf("--tag: key %q given twice", k)
		}
		tags[k] = val
	}
	return tags, nil
}

// TagsProblem says why the manager refuses these tags as a workload's (at most 50; key 1 to 128 and value up to 256
// characters; letters, digits, spaces and _ . : / = + - @; no dormeto: key prefix), or "" when it accepts them.
func TagsProblem(tags map[string]string) string {
	if len(tags) > 50 {
		return "tags: at most 50"
	}
	for _, k := range slices.Sorted(maps.Keys(tags)) {
		v := tags[k]
		switch {
		case utf8.RuneCountInString(k) < 1 || utf8.RuneCountInString(k) > 128:
			return fmt.Sprintf("tag %q: key of 1 to 128 characters", k)
		case utf8.RuneCountInString(v) > 256:
			return fmt.Sprintf("tag %q: value of at most 256 characters", k)
		case !tagChars.MatchString(k):
			return fmt.Sprintf("tag %q: key characters: letters, digits, spaces and _ . : / = + - @", k)
		case !tagChars.MatchString(v):
			return fmt.Sprintf("tag %q: value characters: letters, digits, spaces and _ . : / = + - @", k)
		case strings.HasPrefix(strings.ToLower(k), "dormeto:"):
			return fmt.Sprintf("tag %q: the dormeto: prefix is reserved", k)
		}
	}
	return ""
}

var tagChars = regexp.MustCompile(`^[\p{L}\p{Z}\p{N}_.:/=+\-@]*$`)
