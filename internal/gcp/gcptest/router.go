package gcptest

// The resources of `dormeto gcp install --image`: Cloud Run services and their policies and operations, the serverless
// network endpoint group and its Compute Engine operations, the Cloud Scheduler job, the manifests of Artifact Registry
// (on *-docker.pkg.dev) and the controller's GET /v1/health and /v1/workloads… (on *.run.app). Every handler here runs under the server's
// lock.

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

// RunService is a Cloud Run service as v2 of its API writes and answers it.
type RunService struct {
	Ingress            string       `json:"ingress"`
	InvokerIamDisabled bool         `json:"invokerIamDisabled,omitempty"`
	Template           RunTemplate  `json:"template"`
	Name               string       `json:"name,omitempty"`
	URI                string       `json:"uri,omitempty"`
	URLs               []string     `json:"urls,omitempty"`
	TerminalCondition  RunCondition `json:"terminalCondition"`
}

// RunTemplate is a service's revision template.
type RunTemplate struct {
	ServiceAccount                string         `json:"serviceAccount"`
	Containers                    []RunContainer `json:"containers"`
	Scaling                       RunScaling     `json:"scaling"`
	MaxInstanceRequestConcurrency int            `json:"maxInstanceRequestConcurrency"`
	Timeout                       string         `json:"timeout"`
}

// RunContainer is a template's container.
type RunContainer struct {
	Image string   `json:"image"`
	Env   []RunEnv `json:"env"`
	Ports []struct {
		Name          string `json:"name,omitempty"`
		ContainerPort int    `json:"containerPort"`
	} `json:"ports"`
	Resources struct {
		Limits  map[string]string `json:"limits"`
		CPUIdle bool              `json:"cpuIdle"`
	} `json:"resources"`
}

// RunEnv is a container's environment variable.
type RunEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// RunScaling is a template's numbers of instances (Google leaves a 0 out).
type RunScaling struct {
	MinInstanceCount int `json:"minInstanceCount,omitempty"`
	MaxInstanceCount int `json:"maxInstanceCount"`
}

// RunCondition is a service's readiness.
type RunCondition struct {
	Type    string `json:"type"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// Env is the value of a variable of the service's container ("" when it has none).
func (r *RunService) Env(name string) string {
	for _, e := range r.Template.Containers[0].Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

// NEG is a serverless network endpoint group.
type NEG struct {
	Name                string `json:"name"`
	NetworkEndpointType string `json:"networkEndpointType"`
	CloudRun            struct {
		Service string `json:"service"`
	} `json:"cloudRun"`
}

// Job is a Cloud Scheduler job.
type Job struct {
	Name            string `json:"name"`
	Schedule        string `json:"schedule"`
	TimeZone        string `json:"timeZone"`
	AttemptDeadline string `json:"attemptDeadline"`
	State           string `json:"state,omitempty"`
	HTTPTarget      struct {
		URI        string `json:"uri"`
		HTTPMethod string `json:"httpMethod"`
		OIDCToken  struct {
			ServiceAccountEmail string `json:"serviceAccountEmail"`
			Audience            string `json:"audience"`
		} `json:"oidcToken"`
	} `json:"httpTarget"`
}

// created plays Google's side effect of a creation: GrantOnCreate's binding for its kind.
func (s *Server) created(kind string) {
	if g, ok := s.GrantOnCreate[kind]; ok {
		s.Policy = addBinding(s.Policy, g[0], g[1])
	}
}

// cloudRun serves services, their policies and the operations of their creations and updates.
func (s *Server) cloudRun(w http.ResponseWriter, r *http.Request, body []byte) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/v2/projects/"+s.ProjectID+"/locations/")
	if !ok {
		Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
		return
	}
	if s.disabled(w, "run.googleapis.com") {
		return
	}
	region, tail, _ := strings.Cut(rest, "/")
	if strings.HasPrefix(tail, "operations/") && r.Method == http.MethodGet {
		s.poll(w, strings.TrimPrefix(r.URL.Path, "/v2/"))
		return
	}
	id, isService := strings.CutPrefix(tail, "services/")
	id, verb, _ := strings.Cut(id, ":")
	key := region + "/" + id
	name := "projects/" + s.ProjectID + "/locations/" + region + "/services/" + id
	operation := "projects/" + s.ProjectID + "/locations/" + region + "/operations/"
	switch {
	case tail == "services" && r.Method == http.MethodPost:
		id = r.URL.Query().Get("serviceId")
		key = region + "/" + id
		var in RunService
		if json.Unmarshal(body, &in) != nil || id == "" {
			Fail(w, 400, "INVALID_ARGUMENT", "no serviceId")
			return
		}
		if !s.validRunTemplate(w, in.Template) {
			return
		}
		if _, ok := s.RunServices[key]; ok {
			Fail(w, 409, "ALREADY_EXISTS", "Resource '"+id+"' already exists.")
			return
		}
		in.Name = "projects/" + s.ProjectID + "/locations/" + region + "/services/" + id
		in.URI = "https://" + id + "-abcdefghij-ew.a.run.app"
		in.URLs = []string{"https://" + id + "-" + s.ProjectNumber + "." + region + ".run.app", in.URI}
		in.TerminalCondition = RunCondition{Type: "Ready", State: "CONDITION_SUCCEEDED"}
		s.withGoogleDefaults(&in.Template)
		if message, fails := s.FailingRevisions[id]; fails {
			in.TerminalCondition = RunCondition{Type: "Ready", State: "CONDITION_FAILED", Message: message}
			s.operateFailing(w, operation+s.next(), 9, message, func() { s.RunServices[key] = &in })
			return
		}
		s.operate(w, operation+s.next(), nil, func() {
			s.RunServices[key] = &in
			s.created("service")
		})
	case isService && id != "":
		svc, ok := s.RunServices[key]
		if !ok {
			Fail(w, 404, "NOT_FOUND", "Resource '"+id+"' of kind 'SERVICE' in region '"+region+"' in project '"+s.ProjectID+"' does not exist.")
			return
		}
		switch {
		case verb == "" && r.Method == http.MethodGet:
			Reply(w, svc)
		case verb == "" && r.Method == http.MethodDelete:
			s.operate(w, operation+s.next(), nil, func() {
				delete(s.RunServices, key)
				delete(s.Policies, name)
			})
		case verb == "" && r.Method == http.MethodPatch:
			var in RunService
			if json.Unmarshal(body, &in) != nil {
				Fail(w, 400, "INVALID_ARGUMENT", "bad service")
				return
			}
			if !s.validRunTemplate(w, in.Template) {
				return
			}
			s.withGoogleDefaults(&in.Template)
			mask := strings.Split(r.URL.Query().Get("updateMask"), ",")
			s.operate(w, operation+s.next(), nil, func() {
				if slices.Contains(mask, "ingress") {
					svc.Ingress = in.Ingress
				}
				if slices.Contains(mask, "invokerIamDisabled") {
					svc.InvokerIamDisabled = in.InvokerIamDisabled
				}
				if slices.Contains(mask, "template") {
					svc.Template = in.Template
				}
				svc.TerminalCondition = RunCondition{Type: "Ready", State: "CONDITION_SUCCEEDED"}
			})
		case verb == "getIamPolicy" && r.Method == http.MethodGet, verb == "setIamPolicy" && r.Method == http.MethodPost:
			s.resourcePolicy(w, name, verb, body)
		default:
			noRoute(w, r)
		}
	default:
		noRoute(w, r)
	}
}

// validRunTemplate refuses what Cloud Run refuses: PORT is reserved, Cloud Run sets it from the container's port.
func (s *Server) validRunTemplate(w http.ResponseWriter, t RunTemplate) bool {
	for _, c := range t.Containers {
		for _, e := range c.Env {
			if e.Name == "PORT" {
				Fail(w, 400, "INVALID_ARGUMENT", "The following reserved env names were provided: PORT. These values are automatically set by the system.")
				return false
			}
		}
		if !c.Resources.CPUIdle && c.Resources.Limits["memory"] == "256Mi" {
			Fail(w, 400, "INVALID_ARGUMENT", "template.containers.resources.limits.memory: Invalid value specified for memory. Total memory < 512 Mi is not supported with cpu always allocated (unthrottled).")
			return false
		}
	}
	return true
}

// withGoogleDefaults adds what Google answers beyond what was written: a CPU limit and the port's name.
func (s *Server) withGoogleDefaults(t *RunTemplate) {
	for i := range t.Containers {
		c := &t.Containers[i]
		if c.Resources.Limits == nil {
			c.Resources.Limits = map[string]string{}
		}
		c.Resources.Limits["cpu"] = "1000m"
		for j := range c.Ports {
			c.Ports[j].Name = "http1"
		}
	}
}

// compute serves the regional network endpoint groups and their operations, written and read on compute.googleapis.com,
// the operations' selfLinks on www.googleapis.com. A creation's operation is done at its first poll.
func (s *Server) compute(w http.ResponseWriter, r *http.Request, body []byte) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/compute/v1/projects/"+s.ProjectID+"/regions/")
	if !ok {
		Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
		return
	}
	if s.disabled(w, "compute.googleapis.com") {
		return
	}
	region, tail, _ := strings.Cut(rest, "/")
	id, isNEG := strings.CutPrefix(tail, "networkEndpointGroups/")
	switch {
	case strings.HasPrefix(tail, "operations/") && r.Method == http.MethodGet:
		Reply(w, map[string]any{"name": strings.TrimPrefix(tail, "operations/"), "status": "DONE"})
	case tail == "networkEndpointGroups" && r.Method == http.MethodPost:
		var in NEG
		if json.Unmarshal(body, &in) != nil || in.Name == "" {
			Fail(w, 400, "INVALID_ARGUMENT", "bad network endpoint group")
			return
		}
		if _, ok := s.NEGs[region+"/"+in.Name]; ok {
			Fail(w, 409, "ALREADY_EXISTS", "The resource already exists")
			return
		}
		s.NEGs[region+"/"+in.Name] = &in
		s.created("neg")
		op := "operation-" + s.next()
		Reply(w, map[string]any{"name": op, "status": "RUNNING",
			"selfLink": "https://www.googleapis.com/compute/v1/projects/" + s.ProjectID + "/regions/" + region + "/operations/" + op})
	case isNEG && r.Method == http.MethodDelete:
		if _, ok := s.NEGs[region+"/"+id]; !ok {
			Fail(w, 404, "NOT_FOUND", "The resource '"+id+"' was not found")
			return
		}
		delete(s.NEGs, region+"/"+id)
		op := "operation-" + s.next()
		Reply(w, map[string]any{"name": op, "status": "RUNNING",
			"selfLink": "https://www.googleapis.com/compute/v1/projects/" + s.ProjectID + "/regions/" + region + "/operations/" + op})
	case isNEG && r.Method == http.MethodGet:
		neg, ok := s.NEGs[region+"/"+id]
		if !ok {
			Fail(w, 404, "NOT_FOUND", "The resource '"+id+"' was not found")
			return
		}
		Reply(w, neg)
	default:
		noRoute(w, r)
	}
}

// cloudScheduler serves jobs.
func (s *Server) cloudScheduler(w http.ResponseWriter, r *http.Request, body []byte) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/v1/projects/"+s.ProjectID+"/locations/")
	if !ok {
		Fail(w, 403, "PERMISSION_DENIED", "The caller does not have permission")
		return
	}
	if s.disabled(w, "cloudscheduler.googleapis.com") {
		return
	}
	region, tail, _ := strings.Cut(rest, "/")
	id, isJob := strings.CutPrefix(tail, "jobs/")
	switch {
	case tail == "jobs" && r.Method == http.MethodPost:
		var in Job
		if json.Unmarshal(body, &in) != nil {
			Fail(w, 400, "INVALID_ARGUMENT", "bad job")
			return
		}
		prefix := "projects/" + s.ProjectID + "/locations/" + region + "/jobs/"
		jid, ok := strings.CutPrefix(in.Name, prefix)
		if !ok || jid == "" {
			Fail(w, 400, "INVALID_ARGUMENT", "bad job name "+in.Name)
			return
		}
		if _, ok := s.Jobs[region+"/"+jid]; ok {
			Fail(w, 409, "ALREADY_EXISTS", "Job already exists.")
			return
		}
		in.State = "ENABLED"
		s.Jobs[region+"/"+jid] = &in
		s.created("job")
		Reply(w, &in)
	case isJob && id != "":
		job, ok := s.Jobs[region+"/"+id]
		if !ok {
			Fail(w, 404, "NOT_FOUND", "Job not found.")
			return
		}
		switch r.Method {
		case http.MethodGet:
		case http.MethodDelete:
			delete(s.Jobs, region+"/"+id)
			Reply(w, map[string]any{})
			return
		case http.MethodPatch:
			var in Job
			json.Unmarshal(body, &in)
			for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
				switch field {
				case "schedule":
					job.Schedule = in.Schedule
				case "timeZone":
					job.TimeZone = in.TimeZone
				case "attemptDeadline":
					job.AttemptDeadline = in.AttemptDeadline
				case "httpTarget":
					job.HTTPTarget = in.HTTPTarget
				}
			}
		default:
			noRoute(w, r)
			return
		}
		Reply(w, job)
	default:
		noRoute(w, r)
	}
}

// manifest answers a HEAD of a manifest of Images. Without an Accept header naming OCI's image index it is not found, as
// a registry that holds only that format answers.
func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/v2/")
	path, ref, isManifest := strings.Cut(rest, "/manifests/")
	if !ok || !isManifest || r.Method != http.MethodHead {
		noRoute(w, r)
		return
	}
	if !s.Images[r.Host+"/"+path+":"+ref] || !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
		w.WriteHeader(http.StatusNotFound)
	}
}

// controller answers GET /v1/health on the URLs of a Cloud Run service, given the identity token of Tokens (else a 401,
// as Cloud Run's invoker check does): the service's DMT_VERSION, or HealthVersion when set.
func (s *Server) controller(w http.ResponseWriter, r *http.Request) {
	for _, svc := range s.RunServices {
		if !slices.Contains(svc.URLs, "https://"+r.Host) {
			continue
		}
		if r.Header.Get("Authorization") != "Bearer test-identity" {
			Fail(w, 401, "UNAUTHENTICATED", "The request was not authenticated.")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/workloads") {
			s.workloads(w, r)
			return
		}
		if r.URL.Path != "/v1/health" || r.Method != http.MethodGet {
			noRoute(w, r)
			return
		}
		version := s.HealthVersion
		if version == "" {
			version = svc.Env("DMT_VERSION")
		}
		Reply(w, map[string]any{"version": version, "project": s.ProjectID, "region": svc.Env("DMT_REGION")})
		return
	}
	noRoute(w, r)
}
