package gcptest

// The resources of `dormeto gcp connect`: the workload identity pools and their providers (created, updated, deleted and
// undeleted by operations; a deleted one answers with state DELETED), the Cloud Run services listed (locations/- or a
// region), the policies of tag values, folders and organizations, the custom roles of organizations, the project search and
// the project's ancestry. Every handler here runs under the server's lock.

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

// Pool is a workload identity pool; State is ACTIVE or DELETED.
type Pool struct {
	DisplayName, Description, State string
	Disabled                        bool
}

// Provider is a workload identity pool's AWS provider; State is ACTIVE or DELETED.
type Provider struct {
	DisplayName, Description, State string
	Disabled                        bool
	AccountID                       string
	Mapping                         map[string]string
	Condition                       string
}

func (s *Server) poolName(id string) string {
	return "projects/" + s.ProjectNumber + "/locations/global/workloadIdentityPools/" + id
}

func (s *Server) poolBody(id string, p *Pool) map[string]any {
	return map[string]any{"name": s.poolName(id), "displayName": p.DisplayName, "description": p.Description, "state": p.State, "disabled": p.Disabled}
}

func (s *Server) providerBody(pool, id string, p *Provider) map[string]any {
	return map[string]any{"name": s.poolName(pool) + "/providers/" + id, "displayName": p.DisplayName, "description": p.Description,
		"state": p.State, "disabled": p.Disabled, "attributeMapping": p.Mapping, "attributeCondition": p.Condition,
		"aws": map[string]any{"accountId": p.AccountID}}
}

type poolIn struct {
	DisplayName        string            `json:"displayName"`
	Description        string            `json:"description"`
	Disabled           bool              `json:"disabled"`
	AttributeMapping   map[string]string `json:"attributeMapping"`
	AttributeCondition string            `json:"attributeCondition"`
	AWS                struct {
		AccountID string `json:"accountId"`
	} `json:"aws"`
}

// pools serves rest, the path after projects/<id or number>/locations/global/workloadIdentityPools.
func (s *Server) pools(w http.ResponseWriter, r *http.Request, rest string, body []byte) {
	rest = strings.TrimPrefix(rest, "/")
	if strings.Contains(rest, "/operations/") && r.Method == http.MethodGet {
		s.poll(w, s.poolName("")+rest)
		return
	}
	var in poolIn
	if len(body) > 0 && json.Unmarshal(body, &in) != nil {
		Fail(w, 400, "INVALID_ARGUMENT", "bad body")
		return
	}
	op := func(pool string, response any, effect func()) {
		s.operate(w, s.poolName(pool)+"/operations/"+s.next(), response, effect)
	}
	if rest == "" && r.Method == http.MethodPost {
		id := r.URL.Query().Get("workloadIdentityPoolId")
		if _, ok := s.Pools[id]; ok || id == "" {
			Fail(w, 409, "ALREADY_EXISTS", "Requested entity already exists")
			return
		}
		p := &Pool{DisplayName: in.DisplayName, Description: in.Description, Disabled: in.Disabled, State: "ACTIVE"}
		op(id, s.poolBody(id, p), func() { s.Pools[id] = p })
		return
	}
	path, verb, _ := strings.Cut(rest, ":")
	parts := strings.Split(path, "/")
	id := parts[0]
	pool, ok := s.Pools[id]
	if !ok {
		Fail(w, 404, "NOT_FOUND", "Requested entity was not found.")
		return
	}
	if len(parts) == 1 {
		switch {
		case verb == "" && r.Method == http.MethodGet:
			Reply(w, s.poolBody(id, pool))
		case verb == "" && r.Method == http.MethodPatch:
			if pool.State == "DELETED" {
				Fail(w, 400, "FAILED_PRECONDITION", "The pool is deleted.")
				return
			}
			mask := strings.Split(r.URL.Query().Get("updateMask"), ",")
			op(id, nil, func() {
				if slices.Contains(mask, "disabled") {
					pool.Disabled = in.Disabled
				}
				if slices.Contains(mask, "displayName") {
					pool.DisplayName = in.DisplayName
				}
				if slices.Contains(mask, "description") {
					pool.Description = in.Description
				}
			})
		case verb == "" && r.Method == http.MethodDelete:
			op(id, nil, func() { pool.State = "DELETED" })
		case verb == "undelete" && r.Method == http.MethodPost:
			if pool.State != "DELETED" {
				Fail(w, 400, "FAILED_PRECONDITION", "The pool is not deleted.")
				return
			}
			op(id, nil, func() { pool.State = "ACTIVE" })
		default:
			noRoute(w, r)
		}
		return
	}
	if parts[1] != "providers" || len(parts) > 3 {
		noRoute(w, r)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost && verb == "" {
		pid := r.URL.Query().Get("workloadIdentityPoolProviderId")
		if _, ok := s.Providers[id+"/"+pid]; ok || pid == "" {
			Fail(w, 409, "ALREADY_EXISTS", "Requested entity already exists")
			return
		}
		p := &Provider{DisplayName: in.DisplayName, Description: in.Description, Disabled: in.Disabled, State: "ACTIVE",
			AccountID: in.AWS.AccountID, Mapping: in.AttributeMapping, Condition: in.AttributeCondition}
		op(id, s.providerBody(id, pid, p), func() { s.Providers[id+"/"+pid] = p })
		return
	}
	if len(parts) != 3 {
		noRoute(w, r)
		return
	}
	providerID := parts[2]
	provider, ok := s.Providers[id+"/"+providerID]
	if !ok {
		Fail(w, 404, "NOT_FOUND", "Requested entity was not found.")
		return
	}
	switch {
	case verb == "" && r.Method == http.MethodGet:
		Reply(w, s.providerBody(id, providerID, provider))
	case verb == "" && r.Method == http.MethodPatch:
		if provider.State == "DELETED" {
			Fail(w, 400, "FAILED_PRECONDITION", "The provider is deleted.")
			return
		}
		mask := strings.Split(r.URL.Query().Get("updateMask"), ",")
		op(id, nil, func() {
			for _, field := range mask {
				switch field {
				case "disabled":
					provider.Disabled = in.Disabled
				case "attributeMapping":
					provider.Mapping = in.AttributeMapping
				case "attributeCondition":
					provider.Condition = in.AttributeCondition
				case "aws":
					provider.AccountID = in.AWS.AccountID
				case "displayName":
					provider.DisplayName = in.DisplayName
				case "description":
					provider.Description = in.Description
				}
			}
		})
	case verb == "" && r.Method == http.MethodDelete:
		op(id, nil, func() { provider.State = "DELETED" })
	case verb == "undelete" && r.Method == http.MethodPost:
		if provider.State != "DELETED" {
			Fail(w, 400, "FAILED_PRECONDITION", "The provider is not deleted.")
			return
		}
		op(id, nil, func() { provider.State = "ACTIVE" })
	default:
		noRoute(w, r)
	}
}

// runServices lists the Cloud Run services of a location, or of every one with locations/-.
func (s *Server) runServices(w http.ResponseWriter, location string) {
	keys := make([]string, 0, len(s.RunServices))
	for key := range s.RunServices {
		if region, _, _ := strings.Cut(key, "/"); location == "-" || region == location {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	services := []*RunService{}
	for _, key := range keys {
		svc := *s.RunServices[key]
		if svc.Name == "" {
			region, id, _ := strings.Cut(key, "/")
			svc.Name = "projects/" + s.ProjectID + "/locations/" + region + "/services/" + id
		}
		services = append(services, &svc)
	}
	out := map[string]any{"services": services}
	if len(s.RunUnreachable) > 0 {
		var un []string
		for _, region := range s.RunUnreachable {
			un = append(un, "projects/"+s.ProjectID+"/locations/"+region)
		}
		out["unreachable"] = un
	}
	Reply(w, out)
}

// scopePolicy serves testIamPermissions, getIamPolicy and setIamPolicy of a folder or an organization (resource:
// folders/<n> or organizations/<n>), whose policies are in Policies; the permissions held are the project's.
func (s *Server) scopePolicy(w http.ResponseWriter, r *http.Request, resource string, body []byte) {
	name, verb, _ := strings.Cut(resource, ":")
	if !s.Scopes[name] {
		Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
		return
	}
	switch {
	case verb == "testIamPermissions" && r.Method == http.MethodPost:
		var in struct {
			Permissions []string `json:"permissions"`
		}
		json.Unmarshal(body, &in)
		held := []string{}
		for _, p := range in.Permissions {
			if s.holds(name, p) {
				held = append(held, p)
			}
		}
		Reply(w, map[string]any{"permissions": held})
	case (verb == "getIamPolicy" || verb == "setIamPolicy") && r.Method == http.MethodPost:
		s.resourcePolicy(w, name, verb, body)
	default:
		noRoute(w, r)
	}
}

// holds tells whether the caller holds a permission at a resource: held (HoldsAll or Held) and not Withheld there.
func (s *Server) holds(resource, permission string) bool {
	return (s.HoldsAll || s.Held[permission]) && !s.Withheld[resource][permission]
}

// otherProject serves one of OtherProjects: its read, testIamPermissions and its policy (in Policies).
func (s *Server) otherProject(w http.ResponseWriter, r *http.Request, id, verb string, body []byte) {
	switch {
	case verb == "" && r.Method == http.MethodGet:
		p := s.OtherProjects[id]
		Reply(w, map[string]any{"name": "projects/" + p.Number, "projectId": id, "parent": firstOf(p.Ancestors), "state": "ACTIVE"})
	case verb == "testIamPermissions" && r.Method == http.MethodPost:
		var in struct {
			Permissions []string `json:"permissions"`
		}
		json.Unmarshal(body, &in)
		held := []string{}
		for _, p := range in.Permissions {
			if s.holds("projects/"+id, p) {
				held = append(held, p)
			}
		}
		Reply(w, map[string]any{"permissions": held})
	case (verb == "getIamPolicy" || verb == "setIamPolicy") && r.Method == http.MethodPost:
		s.resourcePolicy(w, "projects/"+id, verb, body)
	default:
		noRoute(w, r)
	}
}

func firstOf(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// search answers projects:search: the project, and the ones of OtherProjects.
func (s *Server) search(w http.ResponseWriter) {
	projects := []map[string]any{{"name": "projects/" + s.ProjectNumber, "projectId": s.ProjectID, "parent": s.Parent, "state": s.State}}
	for _, id := range sortedKeys(s.OtherProjects) {
		p := s.OtherProjects[id]
		projects = append(projects, map[string]any{"name": "projects/" + p.Number, "projectId": id, "parent": firstOf(p.Ancestors), "state": "ACTIVE"})
	}
	Reply(w, map[string]any{"projects": projects})
}

// ancestry answers v1's projects.getAncestry: the project, then its Ancestors (the project's own, or another's).
func (s *Server) ancestry(w http.ResponseWriter, id string) {
	ancestors := s.Ancestors
	if id != s.ProjectID {
		p, ok := s.OtherProjects[id]
		if !ok {
			Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
			return
		}
		ancestors = p.Ancestors
	}
	out := []any{map[string]any{"resourceId": map[string]string{"type": "project", "id": id}}}
	for _, a := range ancestors {
		kind, n, _ := strings.Cut(a, "s/")
		out = append(out, map[string]any{"resourceId": map[string]string{"type": kind, "id": n}})
	}
	Reply(w, map[string]any{"ancestor": out})
}

// OtherProject is a project the search finds beside the server's own: its number and ancestors (parent first).
type OtherProject struct {
	Number    string
	Ancestors []string
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
