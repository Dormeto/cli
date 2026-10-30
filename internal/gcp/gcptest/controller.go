package gcptest

// The controller's workloads (GET, POST, DELETE /v1/workloads…, POST /v1/workloads/<id>/nap and /wake), the tag bindings
// of Resource Manager (on <location>-cloudresourcemanager.googleapis.com). Every handler here runs under the server's lock.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Resource is a VM (Location: its zone) or a managed group (Kind zonal or regional, Location its zone or region) that
// the onboarding's dry run names.
type Resource struct {
	Kind, Location, Name, ID string
}

// DryRun is the answer of the controller's onboarding dry run.
type DryRun struct {
	Reasons []string
	VMs     []Resource
	Groups  []Resource
}

// TagBinding is a tag value bound to a resource.
type TagBinding struct {
	Parent, TagValue string
}

func (s *Server) selfLink(path string) string {
	return "https://www.googleapis.com/compute/v1/projects/" + s.ProjectID + "/" + path
}

// parentOf is the full resource name of a VM or a group, with the project's number and the resource's ID.
func (s *Server) parentOf(r Resource, vm bool) string {
	base := "//compute.googleapis.com/projects/" + s.ProjectNumber
	switch {
	case vm:
		return base + "/zones/" + r.Location + "/instances/" + r.ID
	case r.Kind == "regional":
		return base + "/regions/" + r.Location + "/instanceGroupManagers/" + r.ID
	}
	return base + "/zones/" + r.Location + "/instanceGroupManagers/" + r.ID
}

func (s *Server) tagged(parent string) bool {
	return slices.ContainsFunc(s.TagBindings, func(b TagBinding) bool { return b.Parent == parent })
}

// bound tells whether a tag value is bound to a resource.
func (s *Server) bound(value string) bool {
	return slices.ContainsFunc(s.TagBindings, func(b TagBinding) bool { return b.TagValue == value })
}

// Cut closes the connection without an answer: the client's call fails, whatever the server did.
func Cut(w http.ResponseWriter) {
	if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
		conn.Close()
	}
}

func controllerFail(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message, "reason": reason})
}

func (s *Server) workloadBody(id string) map[string]any {
	out := map[string]any{}
	for k, v := range s.WorkloadDocs[id] {
		out[k] = v
	}
	out["id"], out["state"] = id, s.Workloads[id]
	return out
}

type workloadIn struct {
	URLMap         string         `json:"urlMap"`
	BackendService string         `json:"backendService"`
	ID             string         `json:"id"`
	Settings       map[string]any `json:"settings"`
}

// workloads serves /v1/workloads…, as the router's controller does.
func (s *Server) workloads(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/v1/workloads"), "/")
	id, verb, _ := strings.Cut(rest, "/")
	switch {
	case id == "" && r.Method == http.MethodGet:
		ids := make([]string, 0, len(s.Workloads))
		for id := range s.Workloads {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		list := []map[string]any{}
		for _, id := range ids {
			list = append(list, s.workloadBody(id))
		}
		Reply(w, list)
	case id == "" && r.Method == http.MethodPost:
		s.onboard(w, r)
	case id != "":
		state, ok := s.Workloads[id]
		if !ok {
			controllerFail(w, 404, "not-found", "no workload "+id)
			return
		}
		switch {
		case verb == "" && r.Method == http.MethodGet:
			Reply(w, s.workloadBody(id))
		case verb == "" && r.Method == http.MethodDelete:
			force := r.URL.Query().Get("force") == "true"
			if s.WakeFails[id] && !force {
				controllerFail(w, 409, "wake-failed", "cannot offboard "+id+": its wake failed; fix that and try again, or offboard with --force, which leaves its backends down")
				return
			}
			left := []any{}
			if force {
				if backends, ok := s.WorkloadDocs[id]["backends"].([]any); ok {
					left = backends
				}
			}
			delete(s.Workloads, id)
			delete(s.WorkloadDocs, id)
			if force {
				Reply(w, map[string]any{"id": id, "left": left})
				return
			}
			Reply(w, map[string]any{})
		case verb == "wake" && r.Method == http.MethodPost && state == "NAPPING":
			// The wake is kept pending until the nap ends.
			Reply(w, s.workloadBody(id))
		case verb == "nap" && r.Method == http.MethodPost, verb == "wake" && r.Method == http.MethodPost:
			if state != "AWAKE" && state != "ASLEEP" {
				controllerFail(w, 409, "busy", "workload "+id+" is "+state)
				return
			}
			// The answer is at once, with the state as it is; the action runs through Cloud Tasks (here: before the next call).
			answer := s.workloadBody(id)
			s.Workloads[id] = map[string]string{"nap": "ASLEEP", "wake": "AWAKE"}[verb]
			Reply(w, answer)
		default:
			noRoute(w, r)
		}
	default:
		noRoute(w, r)
	}
}

func (s *Server) onboard(w http.ResponseWriter, r *http.Request) {
	var in workloadIn
	body, _ := io.ReadAll(r.Body)
	if json.Unmarshal(body, &in) != nil || in.URLMap == "" || in.BackendService == "" {
		controllerFail(w, 400, "bad-request", "urlMap and backendService are needed")
		return
	}
	id := in.ID
	if id == "" {
		id = in.BackendService
	}
	if r.URL.Query().Get("dryRun") == "true" {
		vms, groups := []map[string]any{}, []map[string]any{}
		for _, v := range s.DryRun.VMs {
			vms = append(vms, map[string]any{"zone": v.Location, "name": v.Name, "id": v.ID,
				"selfLink": s.selfLink("zones/" + v.Location + "/instances/" + v.Name)})
		}
		for _, g := range s.DryRun.Groups {
			place := "zones/"
			if g.Kind == "regional" {
				place = "regions/"
			}
			groups = append(groups, map[string]any{"kind": g.Kind, "location": g.Location, "name": g.Name, "id": g.ID,
				"selfLink": s.selfLink(place + g.Location + "/instanceGroupManagers/" + g.Name)})
		}
		reasons := s.DryRun.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		Reply(w, map[string]any{"eligible": len(reasons) == 0, "reasons": reasons, "vms": vms, "groups": groups})
		return
	}
	if _, ok := s.Workloads[id]; ok {
		controllerFail(w, 409, "exists", "workload "+id+" is onboarded already")
		return
	}
	backends := []map[string]any{}
	for _, v := range s.DryRun.VMs {
		if !s.tagged(s.parentOf(v, true)) {
			controllerFail(w, 400, "not-tagged", "VM "+v.Name+" carries no dormeto tag")
			return
		}
		backends = append(backends, map[string]any{"kind": "vm", "location": v.Location, "name": v.Name, "id": v.ID})
	}
	for _, g := range s.DryRun.Groups {
		if !s.tagged(s.parentOf(g, false)) {
			controllerFail(w, 400, "not-tagged", "group "+g.Name+" carries no dormeto tag")
			return
		}
		backends = append(backends, map[string]any{"kind": "mig", "location": g.Location, "name": g.Name, "id": g.ID})
	}
	s.Workloads[id] = "AWAKE"
	s.WorkloadDocs[id] = map[string]any{"urlMap": in.URLMap, "backendService": in.BackendService, "backends": backends,
		"wakerBackendService": "dmtr-fake-" + id, "settings": in.Settings}
	if s.AnswerOnboarding != nil {
		s.AnswerOnboarding(w)
		return
	}
	Reply(w, s.workloadBody(id))
}

var parentPattern = regexp.MustCompile(`^//compute\.googleapis\.com/projects/(\d+)/(zones|regions)/([a-z0-9-]+)/(instances|instanceGroupManagers)/(\d+)$`)

// tagBindings serves the tag bindings of a location: the parent must name a resource by the project's number and its
// numeric ID, the location is the host's prefix, and a binding is a long-running operation.
func (s *Server) tagBindings(w http.ResponseWriter, r *http.Request, body []byte) {
	if s.disabled(w, "cloudresourcemanager.googleapis.com") {
		return
	}
	location := strings.TrimSuffix(r.Host, "-cloudresourcemanager.googleapis.com")
	valid := func(parent string) bool {
		m := parentPattern.FindStringSubmatch(parent)
		if m == nil || m[1] != s.ProjectNumber || m[3] != location {
			Fail(w, 400, "INVALID_ARGUMENT", "invalid parent "+parent+" for location "+location)
			return false
		}
		return true
	}
	rest := strings.TrimPrefix(r.URL.EscapedPath(), "/v3/tagBindings")
	switch {
	case rest == "" && r.Method == http.MethodPost:
		var in struct{ Parent, TagValue string }
		if json.Unmarshal(body, &in) != nil || !valid(in.Parent) {
			return
		}
		if s.TagValues[in.TagValue] == nil {
			Fail(w, 400, "INVALID_ARGUMENT", "no tag value "+in.TagValue)
			return
		}
		b := TagBinding{in.Parent, in.TagValue}
		if slices.Contains(s.TagBindings, b) {
			Fail(w, 409, "ALREADY_EXISTS", "the binding exists")
			return
		}
		s.operate(w, "operations/rcbind."+s.next(), bindingBody(b), func() { s.TagBindings = append(s.TagBindings, b) })
	case rest == "" && r.Method == http.MethodGet:
		parent := r.URL.Query().Get("parent")
		if !valid(parent) {
			return
		}
		list := []map[string]any{}
		for _, b := range s.TagBindings {
			if b.Parent == parent {
				list = append(list, bindingBody(b))
			}
		}
		Reply(w, map[string]any{"tagBindings": list})
	case strings.HasPrefix(rest, "/") && r.Method == http.MethodDelete:
		escaped, value, ok := strings.Cut(strings.TrimPrefix(rest, "/"), "/tagValues/")
		parent, err := url.PathUnescape(escaped)
		if !ok || err != nil {
			Fail(w, 400, "INVALID_ARGUMENT", "bad binding name")
			return
		}
		if !valid(parent) {
			return
		}
		b := TagBinding{parent, "tagValues/" + value}
		if !slices.Contains(s.TagBindings, b) {
			Fail(w, 404, "NOT_FOUND", "the binding does not exist")
			return
		}
		s.operate(w, "operations/rcunbind."+s.next(), map[string]any{}, func() {
			s.TagBindings = slices.DeleteFunc(s.TagBindings, func(o TagBinding) bool { return o == b })
		})
	default:
		noRoute(w, r)
	}
}

func bindingBody(b TagBinding) map[string]any {
	return map[string]any{"name": "tagBindings/" + url.PathEscape(b.Parent) + "/" + b.TagValue, "parent": b.Parent, "tagValue": b.TagValue}
}
