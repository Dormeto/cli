// Package gcptest is a fake Google Cloud for tests: an httptest.Server and an http.Client whose transport sends every
// request for a *.googleapis.com, *.pkg.dev or *.run.app host to it, so production code keeps its real URLs. It routes by
// host and path, holds state the tests set and inspect, and records the calls made. Tasks that need more resources add
// routes with Route.
//
// Besides the project (its IAM policy, its permissions, its services, its billing), it holds what `dormeto gcp install`
// creates: custom roles, service accounts, tag keys and values, Firestore databases and documents, Cloud Tasks queues,
// Cloud Run services, serverless network endpoint groups, Cloud Scheduler jobs, the policies of service accounts, queues
// and services, and long-running operations, done after OperationPolls polls; and the images of Artifact Registry and the
// controller's health. A resource of an API that is not enabled answers 403 SERVICE_DISABLED, as Google does. Google's
// side effects are played: enabling run.googleapis.com grants roles/run.serviceAgent to Cloud Run's service agent on the
// project, creating a Firestore database grants roles/firestore.serviceAgent to Firestore's, DefaultEditor grants Editor
// to the Compute Engine default service account when compute.googleapis.com is enabled, GrantOnCreate grants what a test
// says when a router's resource is created, and AccountLag refuses the first bindings naming a service account just
// created ("does not exist", IAM's eventual consistency). Refuse makes any call fail before the handlers.
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

// Mutating tells whether the call may change something: anything but a GET or a HEAD, except the read-only POSTs
// (testIamPermissions, getIamPolicy).
func (c Call) Mutating() bool {
	if c.Method == http.MethodGet || c.Method == http.MethodHead {
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
	// Invalid are permissions Google does not know: a testIamPermissions naming one answers 400 INVALID_ARGUMENT.
	Invalid map[string]bool

	// Policy is the project's IAM policy; PolicyDenied makes reading it a 403.
	Policy       map[string]any
	PolicyDenied bool

	// NoBilling makes the project one without an open billing account; BillingDenied makes reading its billing a 403.
	NoBilling, BillingDenied bool

	// Enabled are the enabled services (names such as compute.googleapis.com). ServicePageSize splits the list in pages.
	Enabled         []string
	ServicePageSize int
	// Propagating is, per API, how many more calls it answers SERVICE_DISABLED although enabled (Google propagating it).
	Propagating map[string]int

	// Roles are the project's custom roles, by ID.
	Roles map[string]*Role
	// ServiceAccounts are the project's service accounts, by email.
	ServiceAccounts map[string]*ServiceAccount
	// TagKeys and TagValues are the tags, by name (tagKeys/<n>, tagValues/<n>).
	TagKeys, TagValues map[string]*Tag
	// Databases are the Firestore databases, by ID; Documents their documents' fields, by <database>/<document path>.
	Databases map[string]*Database
	Documents map[string]map[string]any
	// DatabaseOperationGone makes a database's deletion answer an operation that is 404 once polled: it lives under the
	// database's own path, which is gone.
	DatabaseOperationGone bool
	// Queues are the Cloud Tasks queues, by <region>/<queue>. BlockedQueues are the names Google still holds after a
	// deletion, by <region>/<queue>: a create answers as if it succeeded, and no queue exists.
	Queues        map[string]*Queue
	BlockedQueues map[string]bool
	// RunServices are the Cloud Run services, by <region>/<service>; NEGs the network endpoint groups, by <region>/<name>;
	// Jobs the Cloud Scheduler jobs, by <region>/<job>.
	RunServices map[string]*RunService
	NEGs        map[string]*NEG
	Jobs        map[string]*Job
	// FailingRevisions are the services whose first revision does not start, by service, with Google's message: the
	// creation's operation fails and the service stays in Google, not ready.
	FailingRevisions map[string]string
	// Workloads are the workloads the controller lists, by ID, with their state (DELETE /v1/workloads/<id> offboards one);
	// WorkloadDocs the other fields of a workload as GET /v1/workloads/<id> answers it (urlMap, backends…).
	Workloads    map[string]string
	WorkloadDocs map[string]map[string]any
	// Schedules are the workloads' schedules, by ID; NextEvents the events GET .../schedule answers while one is set;
	// ScheduleProblem, when set, makes PUT .../schedule refuse with a 400 saying it.
	Schedules       map[string]map[string]any
	NextEvents      []any
	ScheduleProblem string
	// ControllerBelow035 plays a controller older than 0.35.0: no route for a workload's schedule, pause and resume.
	ControllerBelow035 bool
	// WakeFails are the workloads whose wake fails: DELETE /v1/workloads/<id> answers 409 wake-failed, and only
	// ?force=true offboards them (answering the backends left down).
	WakeFails map[string]bool
	// DryRun is what the controller's onboarding dry run answers: the reasons it refuses (none: eligible), the VMs and
	// the groups it would tag. The onboarding itself is refused (400) while one of them carries no tag binding.
	DryRun DryRun
	// AnswerOnboarding, when set, answers an onboarding the controller did: the answer lost, or a gateway's error. It runs
	// under the server's lock.
	AnswerOnboarding func(w http.ResponseWriter)
	// TagBindings are the tag values bound to resources.
	TagBindings []TagBinding
	// Images are the manifests of Artifact Registry, by <host>/<path>:<tag or digest>.
	Images map[string]bool
	// HealthVersion is the version the controller's health answers instead of its service's DMT_VERSION.
	HealthVersion string
	// GrantOnCreate plays a binding Google adds to the project (role, member) when a resource of a kind (service, neg, job)
	// is created.
	GrantOnCreate map[string][2]string
	// GrantOnEnable plays a binding Google adds to the project (role, member) when an API is turned on, by API
	// (sqladmin.googleapis.com…), beside the ones the fake knows.
	GrantOnEnable map[string][2]string
	// Policies are the IAM policies of service accounts, queues and Cloud Run services, by resource name
	// (projects/<id>/serviceAccounts/<email>, projects/<id>/locations/<region>/queues/<queue>,
	// projects/<id>/locations/<region>/services/<service>).
	Policies map[string]map[string]any
	// OperationPolls is the number of polls after which an operation is done (0: done when created). New sets 1.
	OperationPolls int
	// AccountLag is how many setIamPolicy calls naming a service account created here are refused, per account, with
	// "Service account … does not exist." (IAM's eventual consistency).
	AccountLag int
	// DefaultEditor makes enabling compute.googleapis.com grant roles/editor to the Compute Engine default service
	// account, as Google does in a project without the organization policy iam.automaticIamGrantsForDefaultServiceAccounts.
	DefaultEditor bool

	routes   []route
	calls    []Call
	refusals []*Refusal
	ops      map[string]*operation
	lag      map[string]int
	serial   int
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
		Roles:  map[string]*Role{}, ServiceAccounts: map[string]*ServiceAccount{}, TagKeys: map[string]*Tag{},
		TagValues: map[string]*Tag{}, Databases: map[string]*Database{}, Documents: map[string]map[string]any{},
		Queues: map[string]*Queue{}, Policies: map[string]map[string]any{}, OperationPolls: 1, Propagating: map[string]int{},
		RunServices: map[string]*RunService{}, NEGs: map[string]*NEG{}, Jobs: map[string]*Job{}, Images: map[string]bool{}, Workloads: map[string]string{},
		WorkloadDocs: map[string]map[string]any{}, Schedules: map[string]map[string]any{}, WakeFails: map[string]bool{},
		ops: map[string]*operation{}, lag: map[string]int{},
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
	if !strings.HasSuffix(host, ".googleapis.com") && !strings.HasSuffix(host, ".pkg.dev") && !strings.HasSuffix(host, ".run.app") {
		return nil, fmt.Errorf("gcptest: %s is not a googleapis.com, pkg.dev or run.app host", host)
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
	refusal := s.refusal(r)
	routes := slices.Clone(s.routes)
	s.mu.Unlock()
	if refusal != nil {
		var metadata map[string]string
		if refusal.Reason == "SERVICE_DISABLED" {
			metadata = map[string]string{"service": r.Host}
		}
		failWith(w, refusal.Code, refusal.Status, refusal.Message, refusal.Reason, metadata)
		return
	}
	for _, rt := range routes {
		if rt.host == r.Host && strings.HasPrefix(r.URL.Path, rt.prefix) {
			rt.h(w, r)
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := r.URL.Path
	switch {
	case r.Host == "cloudresourcemanager.googleapis.com" && strings.HasPrefix(path, "/v3/projects/"):
		s.resourceManager(w, r, strings.TrimPrefix(path, "/v3/projects/"), body)
	case r.Host == "cloudresourcemanager.googleapis.com" && (strings.HasPrefix(path, "/v3/tagKeys") || strings.HasPrefix(path, "/v3/tagValues")):
		s.tags(w, r, body)
	case strings.HasSuffix(r.Host, "-cloudresourcemanager.googleapis.com") && strings.HasPrefix(path, "/v3/tagBindings"):
		s.tagBindings(w, r, body)
	case strings.HasSuffix(r.Host, "cloudresourcemanager.googleapis.com") && strings.HasPrefix(path, "/v3/operations/"):
		s.poll(w, strings.TrimPrefix(path, "/v3/"))
	case r.Host == "serviceusage.googleapis.com" && strings.HasPrefix(path, "/v1/projects/"):
		s.serviceUsage(w, r, strings.TrimPrefix(path, "/v1/projects/"), body)
	case r.Host == "serviceusage.googleapis.com" && strings.HasPrefix(path, "/v1/operations/"):
		s.poll(w, strings.TrimPrefix(path, "/v1/"))
	case r.Host == "cloudbilling.googleapis.com" && path == "/v1/projects/"+s.ProjectID+"/billingInfo" && r.Method == http.MethodGet:
		s.billing(w)
	case r.Host == "iam.googleapis.com":
		s.iam(w, r, body)
	case r.Host == "firestore.googleapis.com":
		s.firestore(w, r, body)
	case r.Host == "cloudtasks.googleapis.com":
		s.cloudTasks(w, r, body)
	case r.Host == "run.googleapis.com":
		s.cloudRun(w, r, body)
	case r.Host == "compute.googleapis.com" || r.Host == "www.googleapis.com":
		s.compute(w, r, body)
	case r.Host == "cloudscheduler.googleapis.com":
		s.cloudScheduler(w, r, body)
	case strings.HasSuffix(r.Host, "-docker.pkg.dev"):
		s.manifest(w, r)
	case strings.HasSuffix(r.Host, ".run.app"):
		s.controller(w, r)
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
		for _, p := range in.Permissions {
			if s.Invalid[p] {
				Fail(w, 400, "INVALID_ARGUMENT", "Permission "+p+" is not valid.")
				return
			}
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
		if s.lagging(w, in.Policy) {
			return
		}
		s.Policy = in.Policy
		Reply(w, s.Policy)
	default:
		Fail(w, 404, "NOT_FOUND", "gcptest: no route for "+r.Method+" "+r.URL.Path)
	}
}

func (s *Server) serviceUsage(w http.ResponseWriter, r *http.Request, rest string, body []byte) {
	number, tail, _ := strings.Cut(rest, "/")
	if tail == "services:batchEnable" && r.Method == http.MethodPost {
		s.batchEnable(w, number, body)
		return
	}
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

// billing answers the project's billing info: an open billing account linked, unless NoBilling.
func (s *Server) billing(w http.ResponseWriter) {
	if s.NoRole || s.BillingDenied {
		Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
		return
	}
	info := map[string]any{"name": "projects/" + s.ProjectID + "/billingInfo", "projectId": s.ProjectID, "billingEnabled": !s.NoBilling}
	if !s.NoBilling {
		info["billingAccountName"] = "billingAccounts/012345-6789AB-CDEF01"
	}
	Reply(w, info)
}

// Tokens is a gcp.TokenSource that needs no gcloud. Audiences, when set, collects the audience of each identity token
// asked for.
type Tokens struct {
	AccountName string
	Audiences   *[]string
}

func (Tokens) AccessToken(context.Context) (string, error) { return "test-token", nil }
func (t Tokens) IdentityToken(_ context.Context, audience string) (string, error) {
	if t.Audiences != nil {
		*t.Audiences = append(*t.Audiences, audience)
	}
	return "test-identity", nil
}
func (t Tokens) Account(context.Context) (string, error) {
	if t.AccountName == "" {
		return "me@example.com", nil
	}
	return t.AccountName, nil
}
