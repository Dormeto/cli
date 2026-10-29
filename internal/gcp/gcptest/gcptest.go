// Package gcptest is a fake Google Cloud for tests: an httptest.Server and an http.Client whose transport sends every
// request for a *.googleapis.com host to it, so production code keeps its real URLs. It routes by host and path, holds
// state the tests set and inspect, and records the calls made. Tasks that need more resources add routes with Route.
package gcptest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Call is one request the server received.
type Call struct {
	Method string
	Host   string
	URL    string // the full URL as the client wrote it: https://<host><path>?<query>
	Path   string
	Body   string
}

// Mutating tells whether the call may change something: anything but a GET, except the read-only POSTs
// (testIamPermissions, getIamPolicy).
func (c Call) Mutating() bool {
	if c.Method == http.MethodGet {
		return false
	}
	return !strings.HasSuffix(c.Path, ":testIamPermissions") && !strings.HasSuffix(c.Path, ":getIamPolicy")
}

// Server is the fake. Set its exported fields before the requests (or under Lock while they run).
type Server struct {
	*httptest.Server
	// HTTP is a client for production code: any *.googleapis.com request reaches the server.
	HTTP *http.Client

	mu sync.Mutex

	// The project.
	ProjectID     string
	ProjectNumber string
	Parent        string // "organizations/<n>", "folders/<n>" or ""
	State         string // ACTIVE by default

	// NoRole makes the caller one with no role in the project: the project is a 403 and no permission is held.
	NoRole bool
	// Held are the permissions the caller holds; HoldsAll (the default of New) holds every permission asked.
	Held     map[string]bool
	HoldsAll bool

	// Policy is the project's IAM policy; PolicyDenied makes reading it a 403.
	Policy       map[string]any
	PolicyDenied bool

	// Enabled are the enabled services (names such as compute.googleapis.com). ServicePageSize splits the list in pages.
	Enabled         []string
	ServicePageSize int

	routes []route
	calls  []Call
}

type route struct {
	host, prefix string
	h            http.HandlerFunc
}

// New starts a fake with the project dormeto-dev (1034397096264, no parent, ACTIVE), an owner as caller, an empty
// policy and no service enabled. It closes with the test.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		ProjectID: "dormeto-dev", ProjectNumber: "1034397096264", State: "ACTIVE", HoldsAll: true,
		Policy: map[string]any{"version": 1, "etag": "BwAAAA=="},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	target, _ := url.Parse(s.URL)
	s.HTTP = &http.Client{Transport: redirect{target: target}}
	return s
}

type redirect struct{ target *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if !strings.HasSuffix(host, ".googleapis.com") {
		return nil, fmt.Errorf("gcptest: %s is not a googleapis.com host", host)
	}
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host = r.target.Scheme, r.target.Host
	out.Host = host
	return http.DefaultTransport.RoundTrip(out)
}

// Lock and Unlock guard the state against the running handlers.
func (s *Server) Lock()   { s.mu.Lock() }
func (s *Server) Unlock() { s.mu.Unlock() }

// Route adds a handler for a host and a path prefix, tried before the built-in ones. The handler runs without the lock.
func (s *Server) Route(host, prefix string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes = append(s.routes, route{host, prefix, h})
}

// Calls is a copy of the recorded calls, in order.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// CallsTo are the recorded calls whose path ends with suffix.
func (s *Server) CallsTo(suffix string) []Call {
	var out []Call
	for _, c := range s.Calls() {
		if strings.HasSuffix(c.Path, suffix) {
			out = append(out, c)
		}
	}
	return out
}

// Mutations are the recorded calls that may change something.
func (s *Server) Mutations() []Call {
	var out []Call
	for _, c := range s.Calls() {
		if c.Mutating() {
			out = append(out, c)
		}
	}
	return out
}

// Fail writes a Google error body.
func Fail(w http.ResponseWriter, code int, status, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "status": status, "message": message}})
}

// Reply writes a 200 JSON body.
func Reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	full := "https://" + r.Host + r.URL.Path
	if r.URL.RawQuery != "" {
		full += "?" + r.URL.RawQuery
	}
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: r.Method, Host: r.Host, URL: full, Path: r.URL.Path, Body: string(body)})
	routes := slices.Clone(s.routes)
	s.mu.Unlock()
	for _, rt := range routes {
		if rt.host == r.Host && strings.HasPrefix(r.URL.Path, rt.prefix) {
			rt.h(w, r)
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Host == "cloudresourcemanager.googleapis.com" && strings.HasPrefix(r.URL.Path, "/v3/projects/"):
		s.resourceManager(w, r, strings.TrimPrefix(r.URL.Path, "/v3/projects/"), body)
	case r.Host == "serviceusage.googleapis.com" && strings.HasPrefix(r.URL.Path, "/v1/projects/"):
		s.serviceUsage(w, r, strings.TrimPrefix(r.URL.Path, "/v1/projects/"))
	default:
		Fail(w, 404, "NOT_FOUND", "gcptest: no route for "+r.Method+" "+full)
	}
}

func (s *Server) resourceManager(w http.ResponseWriter, r *http.Request, rest string, body []byte) {
	id, verb, _ := strings.Cut(rest, ":")
	if id != s.ProjectID {
		Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
		return
	}
	switch {
	case verb == "" && r.Method == http.MethodGet:
		if s.NoRole {
			Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
			return
		}
		p := map[string]any{"name": "projects/" + s.ProjectNumber, "projectId": s.ProjectID, "state": s.State}
		if s.Parent != "" {
			p["parent"] = s.Parent
		}
		Reply(w, p)
	case verb == "testIamPermissions" && r.Method == http.MethodPost:
		var in struct {
			Permissions []string `json:"permissions"`
		}
		if json.Unmarshal(body, &in) != nil || len(in.Permissions) == 0 || len(in.Permissions) > 100 {
			Fail(w, 400, "INVALID_ARGUMENT", "between 1 and 100 permissions")
			return
		}
		held := []string{}
		for _, p := range in.Permissions {
			if !s.NoRole && (s.HoldsAll || s.Held[p]) {
				held = append(held, p)
			}
		}
		Reply(w, map[string]any{"permissions": held})
	case verb == "getIamPolicy" && r.Method == http.MethodPost:
		if s.NoRole || s.PolicyDenied {
			Fail(w, 403, "PERMISSION_DENIED", "Permission 'resourcemanager.projects.getIamPolicy' denied")
			return
		}
		Reply(w, s.Policy)
	case verb == "setIamPolicy" && r.Method == http.MethodPost:
		var in struct {
			Policy map[string]any `json:"policy"`
		}
		if s.NoRole || s.PolicyDenied || json.Unmarshal(body, &in) != nil {
			Fail(w, 403, "PERMISSION_DENIED", "Permission 'resourcemanager.projects.setIamPolicy' denied")
			return
		}
		s.Policy = in.Policy
		Reply(w, s.Policy)
	default:
		Fail(w, 404, "NOT_FOUND", "gcptest: no route for "+r.Method+" "+r.URL.Path)
	}
}

func (s *Server) serviceUsage(w http.ResponseWriter, r *http.Request, rest string) {
	number, tail, _ := strings.Cut(rest, "/")
	if tail != "services" || r.Method != http.MethodGet {
		Fail(w, 404, "NOT_FOUND", "gcptest: no route for "+r.Method+" "+r.URL.Path)
		return
	}
	if s.NoRole || number != s.ProjectNumber {
		Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
		return
	}
	start := 0
	if tok := r.URL.Query().Get("pageToken"); tok != "" {
		start, _ = strconv.Atoi(tok)
	}
	size := s.ServicePageSize
	if size <= 0 || size > 200 {
		size = 200
	}
	end := min(start+size, len(s.Enabled))
	services := []map[string]string{}
	for _, name := range s.Enabled[min(start, end):end] {
		services = append(services, map[string]string{"name": "projects/" + number + "/services/" + name, "state": "ENABLED"})
	}
	out := map[string]any{"services": services}
	if end < len(s.Enabled) {
		out["nextPageToken"] = strconv.Itoa(end)
	}
	Reply(w, out)
}

// Tokens is a gcp.TokenSource that needs no gcloud.
type Tokens struct{ AccountName string }

func (Tokens) AccessToken(context.Context) (string, error) { return "test-token", nil }
func (Tokens) IdentityToken(context.Context, string) (string, error) {
	return "test-identity", nil
}
func (t Tokens) Account(context.Context) (string, error) {
	if t.AccountName == "" {
		return "me@example.com", nil
	}
	return t.AccountName, nil
}
