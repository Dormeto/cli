package gcptest

// The controller's workloads (GET, POST, DELETE /v1/workloads…, POST /v1/workloads/<id>/nap and /wake), the tag bindings
// of Resource Manager (on <location>-cloudresourcemanager.googleapis.com). Every handler here runs under the server's lock.

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
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
	// GKE is what the NEGs say of a cluster the request names; nil: nothing, as a controller before GKE.
	GKE *DryRunGKE
	// GKEUnasked answers GKE with no cluster in the request; MoreDatabases names instances the request does not.
	GKEUnasked    bool
	MoreDatabases []string
}

// DryRunGKE is what a dry run reads of a GKE workload.
type DryRunGKE struct {
	Namespace, Service string
	Autopilot          bool
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

// databaseParent is the full resource name of a Cloud SQL instance: the project's ID and the instance's name.
func (s *Server) databaseParent(name string) string {
	return "//sqladmin.googleapis.com/projects/" + s.ProjectID + "/instances/" + name
}

// cloudSQL serves a Cloud SQL instance's read: what its region says of where its tag bindings are.
func (s *Server) cloudSQL(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutPrefix(r.URL.Path, "/v1/projects/"+s.ProjectID+"/instances/")
	region, there := s.SQLInstances[name]
	switch {
	case !ok || r.Method != http.MethodGet:
		noRoute(w, r)
	case !there:
		Fail(w, 404, "NOT_FOUND", "The Cloud SQL instance does not exist.")
	default:
		Reply(w, map[string]any{"name": name, "region": region, "selfLink": "https://sqladmin.googleapis.com/v1/projects/" + s.ProjectID + "/instances/" + name})
	}
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
	if f := s.OnFallback[id]; len(f) > 0 {
		out["fallbacks"] = fallbacksOf(f)
	}
	return out
}

// fallbacksOf is OnFallback's VMs in the router's shape (AWS's): [{instance, from, to}], by VM.
func fallbacksOf(on map[string]map[string]string) []map[string]string {
	var list []map[string]string
	for _, vm := range slices.Sorted(maps.Keys(on)) {
		list = append(list, map[string]string{"instance": vm, "from": on[vm]["from"], "to": on[vm]["to"]})
	}
	return list
}

type workloadIn struct {
	URLMap         string         `json:"urlMap"`
	BackendService string         `json:"backendService"`
	ID             string         `json:"id"`
	Settings       map[string]any `json:"settings"`
	LoadBalancer   string         `json:"loadBalancer"`
	CloudSQL       []string       `json:"cloudSql"`
	GKE            map[string]any `json:"gke"`
	RequestLogs    map[string]any `json:"requestLogs"`

	FallbackInstanceTypes []string `json:"fallbackInstanceTypes"`
}

// workloads serves /v1/workloads…, as the router's controller does.
func (s *Server) workloads(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/v1/workloads"), "/")
	id, verb, _ := strings.Cut(rest, "/")
	if (s.ControllerBelow035 && slices.Contains([]string{"schedule", "pause", "resume"}, verb)) || (s.ControllerBeforeSettings && verb == "settings") {
		controllerFail(w, 404, "not-found", "no route for "+r.Method+" "+r.URL.Path)
		return
	}
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
			onFallback := s.OnFallback[id]
			if force {
				answer := map[string]any{"id": id, "left": left}
				if len(onFallback) > 0 {
					answer["fallbacks"] = fallbacksOf(onFallback)
				}
				if notGivenBack := s.NotGivenBack[id]; len(notGivenBack) > 0 {
					answer["notGivenBack"] = notGivenBack
				}
				Reply(w, answer)
				return
			}
			if len(onFallback) > 0 {
				Reply(w, map[string]any{"fallbacks": fallbacksOf(onFallback)})
				return
			}
			Reply(w, map[string]any{})
		case verb == "schedule":
			s.schedule(w, r, id)
		case verb == "waiting-page":
			s.waitingPage(w, r, id)
		case verb == "settings" && r.Method == http.MethodPut:
			s.workloadSettings(w, r, id)
		case verb == "pause" && r.Method == http.MethodPost:
			s.pause(w, r, id)
		case verb == "resume" && r.Method == http.MethodPost:
			delete(s.WorkloadDocs[id], "paused")
			Reply(w, s.workloadBody(id))
		case verb == "nap" && r.Method == http.MethodPost && state == "AWAKE" && s.WorkloadDocs[id]["paused"] != nil:
			// Only on one that would act: an asleep one is there already, as onboarding.mjs's move and napUnlessPaused.
			paused := s.WorkloadDocs[id]["paused"].(map[string]any)
			controllerFail(w, 409, "paused", fmt.Sprintf("workload %s is paused by %v: %v", id, paused["by"], paused["reason"]))
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
		databases := []map[string]any{}
		for _, name := range in.CloudSQL {
			if _, there := s.SQLInstances[name]; !there {
				reasons = append(reasons, "Cloud SQL instance "+name+" not found: name an instance of this project")
				continue
			}
			database := map[string]any{"name": name, "selfLink": "https://sqladmin.googleapis.com/v1/projects/" + s.ProjectID + "/instances/" + name}
			if replicas := s.SQLReplicas[name]; len(replicas) > 0 {
				database["replicas"] = replicas
			}
			databases = append(databases, database)
		}
		if _, there := s.Workloads[id]; there {
			reasons = append(slices.Clone(reasons), "workload "+id+" is onboarded already")
		}
		answer := map[string]any{"eligible": len(reasons) == 0, "reasons": reasons, "vms": vms, "groups": groups}
		if !s.ControllerBelow035 {
			kind := in.LoadBalancer
			if kind == "" {
				kind = "global"
			}
			answer["lb"], answer["databases"] = map[string]any{"kind": kind}, databases
			for _, name := range s.DryRun.MoreDatabases {
				databases = append(databases, map[string]any{"name": name, "selfLink": "https://sqladmin.googleapis.com/v1/projects/" + s.ProjectID + "/instances/" + name})
			}
			answer["databases"] = databases
			if s.DryRun.GKE != nil && (in.GKE != nil || s.DryRun.GKEUnasked) {
				answer["gke"] = map[string]any{"cluster": in.GKE["cluster"], "namespace": s.DryRun.GKE.Namespace, "service": s.DryRun.GKE.Service, "autopilot": s.DryRun.GKE.Autopilot}
			}
		}
		Reply(w, answer)
		return
	}
	if s.OnboardingRefused != "" {
		controllerFail(w, 400, "not-eligible", s.OnboardingRefused)
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
	for _, name := range in.CloudSQL {
		if !s.tagged(s.databaseParent(name)) {
			controllerFail(w, 400, "not-tagged", "Cloud SQL instance "+name+" carries no dormeto tag")
			return
		}
		backends = append(backends, map[string]any{"kind": "cloudsql", "name": name})
	}
	if in.GKE != nil {
		backend := map[string]any{"kind": "k8s"}
		for key, v := range in.GKE {
			backend[key] = v
		}
		backends = append(backends, backend)
	}
	s.Workloads[id] = "AWAKE"
	s.WorkloadDocs[id] = map[string]any{"urlMap": in.URLMap, "backendService": in.BackendService, "backends": backends,
		"wakerBackendService": "dmtr-fake-" + id, "settings": in.Settings}
	if in.LoadBalancer != "" && in.LoadBalancer != "global" {
		s.WorkloadDocs[id]["lb"] = map[string]any{"kind": in.LoadBalancer}
	}
	// What the onboarding found of the backend service's request logs, kept for its offboarding (the design's §5).
	if in.RequestLogs != nil {
		s.WorkloadDocs[id]["requestLogs"] = in.RequestLogs
	}
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
		if name, ok := strings.CutPrefix(parent, "//sqladmin.googleapis.com/projects/"+s.ProjectID+"/instances/"); ok && s.SQLInstances[name] == location {
			return true
		}
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

// schedule serves /v1/workloads/<id>/schedule: GET, PUT (the schedule as the body; ScheduleProblem, when set, refuses it
// with a 400) and DELETE. The next events are the ones NextEvents holds while a schedule is set.
func (s *Server) schedule(w http.ResponseWriter, r *http.Request, id string) {
	shown := func() {
		events := []any{}
		if s.Schedules[id] != nil {
			events = s.NextEvents
		}
		answer := map[string]any{"schedule": s.Schedules[id], "nextEvents": events}
		maps.Copy(answer, s.ScheduleJoin)
		Reply(w, answer)
	}
	switch r.Method {
	case http.MethodGet:
		shown()
	case http.MethodPut:
		var in map[string]any
		body, _ := io.ReadAll(r.Body)
		if s.ScheduleProblem != "" || json.Unmarshal(body, &in) != nil {
			controllerFail(w, 400, "bad-request", s.ScheduleProblem)
			return
		}
		in["at"] = "2026-11-01T10:00:00.000Z"
		s.Schedules[id] = in
		shown()
	case http.MethodDelete:
		delete(s.Schedules, id)
		shown()
	default:
		noRoute(w, r)
	}
}

// pause serves POST /v1/workloads/<id>/pause: a reason is needed and keepAsleep, when sent, is a boolean (null is refused).
func (s *Server) pause(w http.ResponseWriter, r *http.Request, id string) {
	var in map[string]any
	body, _ := io.ReadAll(r.Body)
	if json.Unmarshal(body, &in) != nil {
		controllerFail(w, 400, "bad-request", "the body is not JSON")
		return
	}
	if reason, _ := in["reason"].(string); strings.TrimSpace(reason) == "" {
		controllerFail(w, 400, "bad-request", "reason required (1 to 256 characters)")
		return
	}
	keep, given := in["keepAsleep"]
	if _, isBool := keep.(bool); given && !isBool {
		controllerFail(w, 400, "bad-request", "keepAsleep must be true or false")
		return
	}
	record := map[string]any{"by": in["by"], "at": "2026-11-01T10:00:00.000Z", "reason": in["reason"], "until": in["until"], "keepAsleep": keep == true}
	if s.WorkloadDocs[id] == nil {
		s.WorkloadDocs[id] = map[string]any{}
	}
	s.WorkloadDocs[id]["paused"] = record
	Reply(w, s.workloadBody(id))
}

// workloadSettings serves PUT /v1/workloads/<id>/settings: the flat body is merged into the workload's settings (a null removes
// the key); SettingsProblem, when set, refuses it with a 400 saying it. The answer is the router's: the settings written, the
// hibernation and what resolves.
func (s *Server) workloadSettings(w http.ResponseWriter, r *http.Request, id string) {
	var in map[string]any
	body, _ := io.ReadAll(r.Body)
	if s.SettingsProblem != "" || json.Unmarshal(body, &in) != nil {
		controllerFail(w, 400, "bad-request", s.SettingsProblem)
		return
	}
	delete(in, "by")
	if s.WorkloadDocs[id] == nil {
		s.WorkloadDocs[id] = map[string]any{}
	}
	stored, _ := s.WorkloadDocs[id]["ownSettings"].(map[string]any)
	if stored == nil {
		stored = map[string]any{}
	}
	for k, v := range in {
		if v == nil {
			delete(stored, k)
		} else {
			stored[k] = v
		}
	}
	s.WorkloadDocs[id]["ownSettings"] = stored
	hibernation := map[string]any{"hibernate": false, "from": "default"}
	if h, ok := stored["hibernate"].(bool); ok {
		hibernation = map[string]any{"hibernate": h, "from": "workload"}
	}
	Reply(w, map[string]any{"level": "workload", "settings": in, "hibernation": hibernation,
		"resolved": map[string]any{"settings": stored, "settingsFrom": map[string]any{}, "type": map[string]any{"name": "instance", "from": "backends"}}})
}

// routerSettings serves GET and PUT /v1/settings: the router level, its flat body merged as the router merges it (a null removes a
// key, defaults merged key by key).
func (s *Server) routerSettings(w http.ResponseWriter, r *http.Request) {
	if s.ControllerBeforeSettings {
		controllerFail(w, 404, "not-found", "no route for "+r.Method+" "+r.URL.Path)
		return
	}
	if s.RouterLevel == nil {
		s.RouterLevel = map[string]any{"hibernate": nil, "joinMinutes": nil, "defaults": nil}
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPut:
		var in map[string]any
		body, _ := io.ReadAll(r.Body)
		if s.SettingsProblem != "" || json.Unmarshal(body, &in) != nil {
			controllerFail(w, 400, "bad-request", s.SettingsProblem)
			return
		}
		for k, v := range in {
			switch k {
			case "by":
				s.RouterLevel["by"], s.RouterLevel["at"] = v, "2026-11-01T10:00:00.000Z"
			case "defaults":
				held, _ := s.RouterLevel["defaults"].(map[string]any)
				if held == nil {
					held = map[string]any{}
				}
				for dk, dv := range v.(map[string]any) {
					if dv == nil {
						delete(held, dk)
					} else {
						held[dk] = dv
					}
				}
				s.RouterLevel["defaults"] = held
			default:
				s.RouterLevel[k] = v
			}
		}
	default:
		noRoute(w, r)
		return
	}
	Reply(w, map[string]any{"level": "router", "settings": s.RouterLevel})
}

// NotServed is the router's warning for a custom page stored while its custom pages are off.
const NotServed = "custom pages are off on this router: this page is not served"

// waitingPage serves GET, PUT and DELETE /v1/waiting-page and /v1/workloads/<id>/waiting-page, with AWS's answer: own, effective,
// from, warnings, by and at. CustomPagesOff refuses a custom page with the router's words and warns of one already stored;
// SettingsProblem-like refusals are PageProblem's.
func (s *Server) waitingPage(w http.ResponseWriter, r *http.Request, level string) {
	if s.ControllerBeforeWaitingPage {
		controllerFail(w, 404, "not-found", "no route for "+r.Method+" "+r.URL.Path)
		return
	}
	if s.WaitingPages == nil {
		s.WaitingPages = map[string]map[string]any{}
	}
	var in struct {
		Page map[string]any `json:"page"`
		By   string         `json:"by"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &in)
	switch r.Method {
	case http.MethodGet:
	case http.MethodPut:
		if in.Page["mode"] == "custom" && s.CustomPagesOff {
			controllerFail(w, 400, "bad-request", "custom pages are off on this router: an administrator turns them on with dormeto gcp install --custom-pages")
			return
		}
		doc := map[string]any{}
		for k, v := range in.Page {
			doc[k] = v
		}
		doc["by"], doc["at"] = in.By, "2026-11-08T10:00:00.000Z"
		s.WaitingPages[level] = doc
	case http.MethodDelete:
		delete(s.WaitingPages, level)
	default:
		noRoute(w, r)
		return
	}
	stored := s.WaitingPages[level]
	kind := "workload"
	if level == "router" {
		kind = "router"
	}
	effective := map[string]any{"mode": "template", "icon": "auto", "title": "", "description": ""}
	from := map[string]any{"mode": "default", "icon": "default"}
	effective["from"] = from
	answer := map[string]any{"own": nil, "effective": effective, "from": from, "favicon": nil, "warnings": []any{}, "terraform": false, "by": "", "at": ""}
	if stored != nil {
		own := map[string]any{}
		for k, v := range stored {
			if k != "by" && k != "at" {
				own[k] = v
			}
		}
		answer["own"], answer["by"], answer["at"] = own, stored["by"], stored["at"]
		switch {
		case own["mode"] == "custom" && s.CustomPagesOff:
			answer["warnings"] = []any{NotServed}
		case own["mode"] == "custom":
			from = map[string]any{"mode": kind, "html": kind}
			answer["effective"], answer["from"] = map[string]any{"mode": "custom", "html": own["html"], "from": from}, from
		default:
			for _, k := range []string{"icon", "title", "description"} {
				if v, ok := own[k]; ok {
					effective[k], from[k] = v, kind
				}
			}
			from["mode"] = kind
		}
	}
	answer["from"] = from
	Reply(w, answer)
}
