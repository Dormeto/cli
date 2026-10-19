package fleet

import (
	"fmt"
	"net/url"
	"strings"
)

// Target is a workload as a command names it: <router>/<id>.
type Target struct{ Router, ID string }

// ParseTarget reads "<router>/<id>": the router's name, then the workload's id (everything after the first slash).
func ParseTarget(s string) (Target, error) {
	router, id, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok || router == "" || id == "" {
		return Target{}, fmt.Errorf("%q is not a target: write <router>/<workload id>", s)
	}
	return Target{Router: router, ID: id}, nil
}

func (t Target) String() string { return t.Router + "/" + t.ID }

// Path is the target's route under /iam, with an optional suffix such as "/pause".
func (t Target) Path(suffix string) string {
	return "/workloads/" + url.PathEscape(t.Router) + "/" + url.PathEscape(t.ID) + suffix
}
