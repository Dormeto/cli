package gcptest

// The router's log view on the project's _Default bucket (logging.googleapis.com), and the backend services whose request logs
// an onboarding turns on (compute.googleapis.com, global and regional, with their operations). Every handler here runs under the
// server's lock.

import (
	"encoding/json"
	"net/http"
	"strings"
)

// LogView is a log view of the _Default bucket.
type LogView struct {
	Filter, Description string
}

// BackendService is a backend service as the onboarding reads and patches it: its logConfig as Google answers it (nil: none),
// changed by a PATCH as JSON merge patch does, and its fingerprint, which a PATCH naming another one is refused for (412).
type BackendService struct {
	LogConfig   map[string]any
	Fingerprint string
}

// logging serves the views of the _Default bucket: create (?viewId=), get, patch (the fields of ?updateMask=) and delete.
func (s *Server) logging(w http.ResponseWriter, r *http.Request, body []byte) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/v2/projects/"+s.ProjectID+"/locations/global/buckets/_Default/views")
	if !ok {
		Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
		return
	}
	if s.disabled(w, "logging.googleapis.com") {
		return
	}
	if s.LogBucketElsewhere {
		Fail(w, 404, "NOT_FOUND", "Bucket projects/"+s.ProjectID+"/locations/global/buckets/_Default does not exist")
		return
	}
	name := func(id string) string {
		return "projects/" + s.ProjectID + "/locations/global/buckets/_Default/views/" + id
	}
	id := strings.TrimPrefix(rest, "/")
	switch {
	case rest == "" && r.Method == http.MethodPost:
		id := r.URL.Query().Get("viewId")
		var in LogView
		if json.Unmarshal(body, &in) != nil || id == "" {
			Fail(w, 400, "INVALID_ARGUMENT", "bad log view")
			return
		}
		if _, there := s.LogViews[id]; there {
			Fail(w, 409, "ALREADY_EXISTS", "View "+id+" already exists")
			return
		}
		s.LogViews[id] = &in
		Reply(w, map[string]any{"name": name(id), "filter": in.Filter, "description": in.Description})
	case id != "" && !strings.Contains(id, "/"):
		v, there := s.LogViews[id]
		switch {
		case !there:
			Fail(w, 404, "NOT_FOUND", "View "+id+" does not exist")
		case r.Method == http.MethodGet:
			Reply(w, map[string]any{"name": name(id), "filter": v.Filter, "description": v.Description})
		case r.Method == http.MethodPatch:
			var in LogView
			json.Unmarshal(body, &in)
			for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
				switch field {
				case "filter":
					v.Filter = in.Filter
				case "description":
					v.Description = in.Description
				}
			}
			Reply(w, map[string]any{"name": name(id), "filter": v.Filter, "description": v.Description})
		case r.Method == http.MethodDelete:
			delete(s.LogViews, id)
			Reply(w, map[string]any{})
		default:
			noRoute(w, r)
		}
	default:
		noRoute(w, r)
	}
}

// backendService serves a backend service of a location ("global" or a region): its read, and a PATCH, whose operation is done
// at its first poll. One the test did not set is a 404.
func (s *Server) backendService(w http.ResponseWriter, r *http.Request, location, name string, body []byte) {
	b, there := s.BackendServices[location+"/"+name]
	if !there {
		Fail(w, 404, "NOT_FOUND", "The resource '"+name+"' was not found")
		return
	}
	place := "global"
	if location != "global" {
		place = "regions/" + location
	}
	link := "https://www.googleapis.com/compute/v1/projects/" + s.ProjectID + "/" + place
	switch r.Method {
	case http.MethodGet:
		out := map[string]any{"name": name, "selfLink": link + "/backendServices/" + name, "fingerprint": b.Fingerprint}
		if b.LogConfig != nil {
			out["logConfig"] = b.LogConfig
		}
		Reply(w, out)
	case http.MethodPatch:
		var in struct {
			Fingerprint string         `json:"fingerprint"`
			LogConfig   map[string]any `json:"logConfig"`
		}
		if json.Unmarshal(body, &in) != nil {
			Fail(w, 400, "INVALID_ARGUMENT", "bad backend service")
			return
		}
		if in.Fingerprint != "" && in.Fingerprint != b.Fingerprint {
			Fail(w, 412, "FAILED_PRECONDITION", "Invalid fingerprint.")
			return
		}
		if in.LogConfig != nil {
			if b.LogConfig == nil {
				b.LogConfig = map[string]any{}
			}
			for k, v := range in.LogConfig {
				if v == nil {
					delete(b.LogConfig, k)
				} else {
					b.LogConfig[k] = v
				}
			}
		}
		b.Fingerprint = "fp-" + s.next()
		op := "operation-" + s.next()
		Reply(w, map[string]any{"name": op, "status": "RUNNING", "selfLink": link + "/operations/" + op})
	default:
		noRoute(w, r)
	}
}
