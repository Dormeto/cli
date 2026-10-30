package gcptest

// The resources of `dormeto gcp install`: custom roles, service accounts, tags, Firestore, Cloud Tasks, their policies
// and the long-running operations. Every handler here runs under the server's lock.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// Role is a custom role.
type Role struct {
	Title, Description, Stage string
	Permissions               []string
	Deleted                   bool
}

// ServiceAccount is a service account.
type ServiceAccount struct {
	DisplayName, Description string
	Disabled                 bool
}

// Tag is a tag key or a tag value.
type Tag struct {
	Name, Parent, ShortName, Description, NamespacedName string
}

// Database is a Firestore database.
type Database struct {
	Type, LocationID, DeleteProtectionState, PointInTimeRecoveryEnablement string
}

// Queue is a Cloud Tasks queue's settings.
type Queue struct {
	MaxAttempts             int
	MinBackoff, MaxBackoff  string
	MaxDispatchesPerSecond  float64
	MaxConcurrentDispatches int
}

// Refusal makes the fake answer an error before any handler, Times times (0: always), to the calls of Method (any when
// empty) to Host whose path starts with Path. Reason, when set, is the error's google.rpc.ErrorInfo reason.
type Refusal struct {
	Method, Host, Path string
	Times              int
	Code               int
	Status, Message    string
	Reason             string
}

// Refuse adds a refusal.
func (s *Server) Refuse(r Refusal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refusals = append(s.refusals, &r)
}

// refusal is the refusal of the call, if any; it counts it. Under the lock.
func (s *Server) refusal(r *http.Request) *Refusal {
	for _, f := range s.refusals {
		if f.Times < 0 || (f.Method != "" && f.Method != r.Method) || f.Host != r.Host || !strings.HasPrefix(r.URL.Path, f.Path) {
			continue
		}
		if f.Times > 0 {
			if f.Times--; f.Times == 0 {
				f.Times = -1
			}
		}
		return f
	}
	return nil
}

// Field is the string value of a field of a Firestore document ("" when there is none).
func (s *Server) Field(database, path, field string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, _ := s.Documents[database+"/"+path][field].(map[string]any)
	str, _ := v["stringValue"].(string)
	return str
}

func failWith(w http.ResponseWriter, code int, status, message, reason string, metadata map[string]string) {
	e := map[string]any{"code": code, "status": status, "message": message}
	if reason != "" {
		e["details"] = []any{map[string]any{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": reason,
			"domain": "googleapis.com", "metadata": metadata}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": e})
}

func noRoute(w http.ResponseWriter, r *http.Request) {
	Fail(w, 404, "NOT_FOUND", "gcptest: no route for "+r.Method+" "+r.Host+r.URL.Path)
}

// disabled answers SERVICE_DISABLED when api is not enabled, or still propagating.
func (s *Server) disabled(w http.ResponseWriter, api string) bool {
	if slices.Contains(s.Enabled, api) {
		if s.Propagating[api] <= 0 {
			return false
		}
		s.Propagating[api]--
	}
	failWith(w, 403, "PERMISSION_DENIED", fmt.Sprintf("%s has not been used in project %s before or it is disabled.", api, s.ProjectNumber),
		"SERVICE_DISABLED", map[string]string{"service": api, "consumer": "projects/" + s.ProjectNumber})
	return true
}

func (s *Server) next() string {
	s.serial++
	return strconv.Itoa(s.serial)
}

// operation is a long-running operation: done after polls more polls, its effect then applied once.
type operation struct {
	polls    int
	done     bool
	effect   func()
	response any
}

// operate starts an operation named name and answers it: done at once when OperationPolls is 0.
func (s *Server) operate(w http.ResponseWriter, name string, response any, effect func()) {
	op := &operation{polls: s.OperationPolls, effect: effect, response: response}
	s.ops[name] = op
	if op.polls <= 0 {
		s.finish(op)
	}
	Reply(w, s.operationBody(name, op))
}

func (s *Server) finish(op *operation) {
	if !op.done {
		op.done = true
		if op.effect != nil {
			op.effect()
		}
	}
}

func (s *Server) operationBody(name string, op *operation) map[string]any {
	out := map[string]any{"name": name, "done": op.done}
	if op.done && op.response != nil {
		out["response"] = op.response
	}
	return out
}

// FinishOperations finishes every operation still running, as Google does whether anyone polls it or not (a CLI that
// stopped waiting).
func (s *Server) FinishOperations() {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.ops))
	for name := range s.ops {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		s.finish(s.ops[name])
	}
}

func (s *Server) poll(w http.ResponseWriter, name string) {
	op, ok := s.ops[name]
	if !ok {
		Fail(w, 404, "NOT_FOUND", "operation "+name+" not found")
		return
	}
	if op.polls--; op.polls <= 0 {
		s.finish(op)
	}
	Reply(w, s.operationBody(name, op))
}

// batchEnable enables services; turning run.googleapis.com on grants Cloud Run's service agent its role on the project,
// and compute.googleapis.com, with DefaultEditor, Editor to the default service account.
func (s *Server) batchEnable(w http.ResponseWriter, number string, body []byte) {
	var in struct {
		ServiceIDs []string `json:"serviceIds"`
	}
	if s.NoRole || number != s.ProjectNumber {
		Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
		return
	}
	if json.Unmarshal(body, &in) != nil || len(in.ServiceIDs) == 0 || len(in.ServiceIDs) > 20 {
		Fail(w, 400, "INVALID_ARGUMENT", "between 1 and 20 services")
		return
	}
	s.operate(w, "operations/acf."+s.next(), nil, func() {
		for _, api := range in.ServiceIDs {
			if slices.Contains(s.Enabled, api) {
				continue
			}
			s.Enabled = append(s.Enabled, api)
			if api == "run.googleapis.com" {
				s.Policy = addBinding(s.Policy, "roles/run.serviceAgent",
					"serviceAccount:service-"+s.ProjectNumber+"@serverless-robot-prod.iam.gserviceaccount.com")
			}
			if api == "compute.googleapis.com" && s.DefaultEditor {
				s.Policy = addBinding(s.Policy, "roles/editor", "serviceAccount:"+s.ProjectNumber+"-compute@developer.gserviceaccount.com")
			}
		}
	})
}

// generic is v as decoded JSON (maps and slices of any).
func generic(v any) map[string]any {
	b, _ := json.Marshal(v)
	var out map[string]any
	json.Unmarshal(b, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

// addBinding gives member role (no condition) in policy, and changes its etag.
func addBinding(policy map[string]any, role, member string) map[string]any {
	p := generic(policy)
	bindings, _ := p["bindings"].([]any)
	found := false
	for _, b := range bindings {
		m, _ := b.(map[string]any)
		if m["role"] != role || m["condition"] != nil {
			continue
		}
		members, _ := m["members"].([]any)
		if !slices.Contains(members, any(member)) {
			m["members"] = append(members, member)
		}
		found = true
		break
	}
	if !found {
		bindings = append(bindings, map[string]any{"role": role, "members": []any{member}})
	}
	p["bindings"] = bindings
	etag, _ := p["etag"].(string)
	p["etag"] = etag + "x"
	return p
}

// lagging refuses a policy naming a service account created here that IAM does not know yet.
func (s *Server) lagging(w http.ResponseWriter, policy map[string]any) bool {
	var p struct {
		Bindings []struct {
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	b, _ := json.Marshal(policy)
	json.Unmarshal(b, &p)
	for _, binding := range p.Bindings {
		for _, m := range binding.Members {
			email, ok := strings.CutPrefix(m, "serviceAccount:")
			if ok && s.lag[email] > 0 {
				s.lag[email]--
				Fail(w, 400, "INVALID_ARGUMENT", "Service account "+email+" does not exist.")
				return true
			}
		}
	}
	return false
}

// resourcePolicy answers getIamPolicy and setIamPolicy of a resource of Policies.
func (s *Server) resourcePolicy(w http.ResponseWriter, name, verb string, body []byte) {
	switch verb {
	case "getIamPolicy":
		p, ok := s.Policies[name]
		if !ok {
			p = map[string]any{"version": 1, "etag": "ACAB"}
		}
		Reply(w, p)
	case "setIamPolicy":
		var in struct {
			Policy map[string]any `json:"policy"`
		}
		if json.Unmarshal(body, &in) != nil || in.Policy == nil {
			Fail(w, 400, "INVALID_ARGUMENT", "no policy")
			return
		}
		if s.lagging(w, in.Policy) {
			return
		}
		etag, _ := in.Policy["etag"].(string)
		in.Policy["etag"] = etag + "x"
		s.Policies[name] = in.Policy
		Reply(w, in.Policy)
	default:
		Fail(w, 404, "NOT_FOUND", "no method "+verb)
	}
}

func (s *Server) roleBody(id string, role *Role) map[string]any {
	out := map[string]any{"name": "projects/" + s.ProjectID + "/roles/" + id, "title": role.Title, "description": role.Description,
		"includedPermissions": role.Permissions, "stage": role.Stage, "etag": "BwR" + id}
	if role.Deleted {
		out["deleted"] = true
	}
	return out
}

func (s *Server) accountBody(email string, a *ServiceAccount) map[string]any {
	return map[string]any{"name": "projects/" + s.ProjectID + "/serviceAccounts/" + email, "projectId": s.ProjectID,
		"email": email, "uniqueId": "1" + strconv.Itoa(len(email)), "displayName": a.DisplayName, "description": a.Description,
		"disabled": a.Disabled}
}

// iam serves custom roles, service accounts and their policies.
func (s *Server) iam(w http.ResponseWriter, r *http.Request, body []byte) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/v1/projects/"+s.ProjectID+"/")
	if !ok {
		Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
		return
	}
	if s.disabled(w, "iam.googleapis.com") {
		return
	}
	kind, item, _ := strings.Cut(rest, "/")
	item, verb, _ := strings.Cut(item, ":")
	switch {
	case kind == "roles" && item == "" && r.Method == http.MethodPost:
		var in struct {
			RoleID string `json:"roleId"`
			Role   struct {
				Title               string   `json:"title"`
				Description         string   `json:"description"`
				IncludedPermissions []string `json:"includedPermissions"`
				Stage               string   `json:"stage"`
			} `json:"role"`
		}
		if json.Unmarshal(body, &in) != nil || in.RoleID == "" {
			Fail(w, 400, "INVALID_ARGUMENT", "no roleId")
			return
		}
		if _, ok := s.Roles[in.RoleID]; ok {
			Fail(w, 409, "ALREADY_EXISTS", "A role named "+in.RoleID+" in this project already exists.")
			return
		}
		role := &Role{Title: in.Role.Title, Description: in.Role.Description, Permissions: in.Role.IncludedPermissions, Stage: in.Role.Stage}
		s.Roles[in.RoleID] = role
		Reply(w, s.roleBody(in.RoleID, role))
	case kind == "roles" && item != "":
		role, ok := s.Roles[item]
		if !ok {
			Fail(w, 404, "NOT_FOUND", "The role named projects/"+s.ProjectID+"/roles/"+item+" was not found.")
			return
		}
		switch {
		case verb == "" && r.Method == http.MethodGet:
		case verb == "undelete" && r.Method == http.MethodPost:
			if !role.Deleted {
				Fail(w, 400, "FAILED_PRECONDITION", "The role is not deleted.")
				return
			}
			role.Deleted = false
		case verb == "" && r.Method == http.MethodPatch:
			if role.Deleted {
				Fail(w, 400, "FAILED_PRECONDITION", "You can't update a deleted role.")
				return
			}
			var in struct {
				Title               string   `json:"title"`
				Description         string   `json:"description"`
				IncludedPermissions []string `json:"includedPermissions"`
				Stage               string   `json:"stage"`
			}
			json.Unmarshal(body, &in)
			for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
				switch field {
				case "title":
					role.Title = in.Title
				case "description":
					role.Description = in.Description
				case "includedPermissions":
					role.Permissions = in.IncludedPermissions
				case "stage":
					role.Stage = in.Stage
				}
			}
		default:
			noRoute(w, r)
			return
		}
		Reply(w, s.roleBody(item, role))
	case kind == "serviceAccounts" && item == "" && r.Method == http.MethodPost:
		var in struct {
			AccountID      string `json:"accountId"`
			ServiceAccount struct {
				DisplayName string `json:"displayName"`
				Description string `json:"description"`
			} `json:"serviceAccount"`
		}
		if json.Unmarshal(body, &in) != nil || in.AccountID == "" {
			Fail(w, 400, "INVALID_ARGUMENT", "no accountId")
			return
		}
		email := in.AccountID + "@" + s.ProjectID + ".iam.gserviceaccount.com"
		if _, ok := s.ServiceAccounts[email]; ok {
			Fail(w, 409, "ALREADY_EXISTS", "Service account "+in.AccountID+" already exists within project projects/"+s.ProjectID+".")
			return
		}
		a := &ServiceAccount{DisplayName: in.ServiceAccount.DisplayName, Description: in.ServiceAccount.Description}
		s.ServiceAccounts[email] = a
		s.lag[email] = s.AccountLag
		Reply(w, s.accountBody(email, a))
	case kind == "serviceAccounts" && item != "":
		a, ok := s.ServiceAccounts[item]
		if !ok {
			Fail(w, 404, "NOT_FOUND", "Unknown service account")
			return
		}
		switch {
		case verb == "" && r.Method == http.MethodGet:
			Reply(w, s.accountBody(item, a))
		case verb == "getIamPolicy" && r.Method == http.MethodPost && len(strings.TrimSpace(string(body))) > 0:
			// IAM's service accounts take the version in the query: "The request body must be empty."
			Fail(w, 400, "INVALID_ARGUMENT", `Invalid JSON payload received. Unknown name "options": Cannot find field.`)
		case (verb == "getIamPolicy" || verb == "setIamPolicy") && r.Method == http.MethodPost:
			s.resourcePolicy(w, "projects/"+s.ProjectID+"/serviceAccounts/"+item, verb, body)
		default:
			noRoute(w, r)
		}
	default:
		noRoute(w, r)
	}
}

func tagBody(t *Tag) map[string]any {
	return map[string]any{"name": t.Name, "parent": t.Parent, "shortName": t.ShortName, "description": t.Description,
		"namespacedName": t.NamespacedName}
}

// tags serves tag keys and values: created by operations, read by namespaced name (403 when missing, as Google).
func (s *Server) tags(w http.ResponseWriter, r *http.Request, body []byte) {
	if s.disabled(w, "cloudresourcemanager.googleapis.com") {
		return
	}
	values := strings.HasPrefix(r.URL.Path, "/v3/tagValues")
	store := s.TagKeys
	if values {
		store = s.TagValues
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/namespaced") && r.Method == http.MethodGet:
		name := r.URL.Query().Get("name")
		for _, t := range store {
			if t.NamespacedName == name {
				Reply(w, tagBody(t))
				return
			}
		}
		Fail(w, 403, "PERMISSION_DENIED", "Permission denied on resource '"+name+"' (or it may not exist).")
	case (r.URL.Path == "/v3/tagKeys" || r.URL.Path == "/v3/tagValues") && r.Method == http.MethodPost:
		var in struct {
			Parent, ShortName, Description string
		}
		if json.Unmarshal(body, &in) != nil || in.ShortName == "" {
			Fail(w, 400, "INVALID_ARGUMENT", "no shortName")
			return
		}
		namespace := ""
		switch {
		case !values && in.Parent == "projects/"+s.ProjectNumber:
			namespace = s.ProjectID
		case values && s.TagKeys[in.Parent] != nil:
			namespace = s.TagKeys[in.Parent].NamespacedName
		default:
			Fail(w, 400, "INVALID_ARGUMENT", "unknown parent "+in.Parent)
			return
		}
		t := &Tag{Parent: in.Parent, ShortName: in.ShortName, Description: in.Description, NamespacedName: namespace + "/" + in.ShortName}
		for _, other := range store {
			if other.NamespacedName == t.NamespacedName {
				Fail(w, 409, "ALREADY_EXISTS", t.NamespacedName+" already exists")
				return
			}
		}
		if values {
			t.Name = "tagValues/" + s.next()
		} else {
			t.Name = "tagKeys/" + s.next()
		}
		s.operate(w, "operations/rctag."+s.next(), tagBody(t), func() { store[t.Name] = t })
	default:
		noRoute(w, r)
	}
}

func (s *Server) databaseBody(id string, d *Database) map[string]any {
	return map[string]any{"name": "projects/" + s.ProjectID + "/databases/" + id, "type": d.Type, "locationId": d.LocationID,
		"deleteProtectionState": d.DeleteProtectionState, "pointInTimeRecoveryEnablement": d.PointInTimeRecoveryEnablement}
}

// firestore serves databases (created by operations) and documents.
func (s *Server) firestore(w http.ResponseWriter, r *http.Request, body []byte) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/v1/projects/"+s.ProjectID+"/databases")
	if !ok {
		Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
		return
	}
	if s.disabled(w, "firestore.googleapis.com") {
		return
	}
	if rest == "" && r.Method == http.MethodPost {
		id := r.URL.Query().Get("databaseId")
		var in Database
		if json.Unmarshal(body, &in) != nil || id == "" {
			Fail(w, 400, "INVALID_ARGUMENT", "no databaseId")
			return
		}
		if _, ok := s.Databases[id]; ok {
			Fail(w, 409, "ALREADY_EXISTS", "Database already exists.")
			return
		}
		d := &in
		s.operate(w, "projects/"+s.ProjectID+"/databases/"+id+"/operations/"+s.next(), s.databaseBody(id, d), func() {
			s.Databases[id] = d
			// Google's side effect: Firestore's service agent and its role, at the first database.
			s.Policy = addBinding(s.Policy, "roles/firestore.serviceAgent",
				"serviceAccount:service-"+s.ProjectNumber+"@gcp-sa-firestore.iam.gserviceaccount.com")
		})
		return
	}
	if rest == "" && r.Method == http.MethodGet {
		ids := make([]string, 0, len(s.Databases))
		for id := range s.Databases {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		databases := []map[string]any{}
		for _, id := range ids {
			databases = append(databases, s.databaseBody(id, s.Databases[id]))
		}
		Reply(w, map[string]any{"databases": databases})
		return
	}
	id, tail, _ := strings.Cut(strings.TrimPrefix(rest, "/"), "/")
	if strings.HasPrefix(tail, "operations/") && r.Method == http.MethodGet {
		s.poll(w, "projects/"+s.ProjectID+"/databases/"+id+"/"+tail)
		return
	}
	d, ok := s.Databases[id]
	if !ok {
		Fail(w, 404, "NOT_FOUND", "The database "+id+" does not exist for project "+s.ProjectID)
		return
	}
	path, isDocument := strings.CutPrefix(tail, "documents/")
	switch {
	case tail == "" && r.Method == http.MethodGet:
		Reply(w, s.databaseBody(id, d))
	case isDocument && r.Method == http.MethodGet:
		fields, ok := s.Documents[id+"/"+path]
		if !ok {
			Fail(w, 404, "NOT_FOUND", "Document \"projects/"+s.ProjectID+"/databases/"+id+"/documents/"+path+"\" not found.")
			return
		}
		Reply(w, map[string]any{"name": "projects/" + s.ProjectID + "/databases/" + id + "/documents/" + path, "fields": fields})
	case isDocument && r.Method == http.MethodPatch:
		var in struct {
			Fields map[string]any `json:"fields"`
		}
		if json.Unmarshal(body, &in) != nil {
			Fail(w, 400, "INVALID_ARGUMENT", "bad document")
			return
		}
		s.Documents[id+"/"+path] = in.Fields
		Reply(w, map[string]any{"name": "projects/" + s.ProjectID + "/databases/" + id + "/documents/" + path, "fields": in.Fields})
	default:
		noRoute(w, r)
	}
}

func (s *Server) queueBody(region, id string, q *Queue) map[string]any {
	return map[string]any{
		"name":  "projects/" + s.ProjectID + "/locations/" + region + "/queues/" + id,
		"state": "RUNNING",
		"retryConfig": map[string]any{"maxAttempts": q.MaxAttempts, "minBackoff": q.MinBackoff, "maxBackoff": q.MaxBackoff,
			"maxDoublings": 16, "maxRetryDuration": "0s"},
		"rateLimits": map[string]any{"maxDispatchesPerSecond": q.MaxDispatchesPerSecond, "maxBurstSize": 100,
			"maxConcurrentDispatches": q.MaxConcurrentDispatches},
	}
}

type queueIn struct {
	Name        string `json:"name"`
	RetryConfig struct {
		MaxAttempts int    `json:"maxAttempts"`
		MinBackoff  string `json:"minBackoff"`
		MaxBackoff  string `json:"maxBackoff"`
	} `json:"retryConfig"`
	RateLimits struct {
		MaxDispatchesPerSecond  float64 `json:"maxDispatchesPerSecond"`
		MaxConcurrentDispatches int     `json:"maxConcurrentDispatches"`
	} `json:"rateLimits"`
}

// cloudTasks serves queues and their policies.
func (s *Server) cloudTasks(w http.ResponseWriter, r *http.Request, body []byte) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/v2/projects/"+s.ProjectID+"/locations/")
	if !ok {
		Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
		return
	}
	if s.disabled(w, "cloudtasks.googleapis.com") {
		return
	}
	region, tail, _ := strings.Cut(rest, "/")
	id, isQueue := strings.CutPrefix(tail, "queues/")
	id, verb, _ := strings.Cut(id, ":")
	switch {
	case tail == "queues" && r.Method == http.MethodPost:
		var in queueIn
		if json.Unmarshal(body, &in) != nil {
			Fail(w, 400, "INVALID_ARGUMENT", "bad queue")
			return
		}
		prefix := "projects/" + s.ProjectID + "/locations/" + region + "/queues/"
		qid, ok := strings.CutPrefix(in.Name, prefix)
		if !ok || qid == "" {
			Fail(w, 400, "INVALID_ARGUMENT", "bad queue name "+in.Name)
			return
		}
		if _, ok := s.Queues[region+"/"+qid]; ok {
			Fail(w, 409, "ALREADY_EXISTS", "Queue already exists")
			return
		}
		q := &Queue{MaxAttempts: in.RetryConfig.MaxAttempts, MinBackoff: in.RetryConfig.MinBackoff, MaxBackoff: in.RetryConfig.MaxBackoff,
			MaxDispatchesPerSecond: in.RateLimits.MaxDispatchesPerSecond, MaxConcurrentDispatches: in.RateLimits.MaxConcurrentDispatches}
		s.Queues[region+"/"+qid] = q
		Reply(w, s.queueBody(region, qid, q))
	case isQueue && id != "":
		q, ok := s.Queues[region+"/"+id]
		if !ok {
			Fail(w, 404, "NOT_FOUND", "Requested entity was not found.")
			return
		}
		switch {
		case verb == "" && r.Method == http.MethodGet:
		case verb == "" && r.Method == http.MethodPatch:
			var in queueIn
			json.Unmarshal(body, &in)
			for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
				switch field {
				case "retryConfig":
					q.MaxAttempts, q.MinBackoff, q.MaxBackoff = in.RetryConfig.MaxAttempts, in.RetryConfig.MinBackoff, in.RetryConfig.MaxBackoff
				case "rateLimits":
					q.MaxDispatchesPerSecond, q.MaxConcurrentDispatches = in.RateLimits.MaxDispatchesPerSecond, in.RateLimits.MaxConcurrentDispatches
				}
			}
		case (verb == "getIamPolicy" || verb == "setIamPolicy") && r.Method == http.MethodPost:
			s.resourcePolicy(w, "projects/"+s.ProjectID+"/locations/"+region+"/queues/"+id, verb, body)
			return
		default:
			noRoute(w, r)
			return
		}
		Reply(w, s.queueBody(region, id, q))
	default:
		noRoute(w, r)
	}
}
