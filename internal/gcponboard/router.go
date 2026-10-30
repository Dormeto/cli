package gcponboard

// The router's steps of `dormeto gcp install --image`, after the access: cloudRun (the controller, its invoker binding to
// the router, the waker with its invoker check off), neg (the waker's serverless network endpoint group) and job (the
// check every 5 minutes). The image is checked while planning, before any change. Its tag is the router's version: set in
// DMT_VERSION, recorded, and what the controller's health must answer. A new image changes the two services only.

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/gcp"
)

const (
	runAPI       = "run.googleapis.com"
	computeAPI   = "compute.googleapis.com"
	schedulerAPI = "cloudscheduler.googleapis.com"
	runV2        = "https://run.googleapis.com/v2/"

	ingressAll = "INGRESS_TRAFFIC_ALL"
	ingressLB  = "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER"
	readyState = "CONDITION_SUCCEEDED"

	// containerPort is the port Cloud Run gives the container in PORT, which it reserves: no service can set it.
	containerPort       = 8080
	serviceTimeout      = 10 * time.Minute
	negTimeout          = 5 * time.Minute
	serviceUpdateFields = "ingress,invokerIamDisabled,template"

	jobSchedule     = "*/5 * * * *"
	jobTimeZone     = "Etc/UTC"
	jobDeadline     = "300s"
	jobUpdateFields = "schedule,timeZone,attemptDeadline,httpTarget"

	invokerPolicy = "run.managed.requireInvokerIam"
)

// manifestTypes are the media types of an image's manifest: Podman's OCI ones, and Docker's.
var manifestTypes = []string{
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
}

// splitImage cuts an image's name (host/path[:tag][@digest]) into the name before the tag, its tag and its digest.
func splitImage(image string) (name, tag, digest string) {
	name, digest, _ = strings.Cut(image, "@")
	if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
		name, tag = name[:colon], name[colon+1:]
	}
	return name, tag, digest
}

// checkImage checks the image while planning: HEAD of its manifest on *-docker.pkg.dev, with the caller's access token;
// elsewhere, a warning. Missing, the install stops before any change. Refused to the caller, a warning: Cloud Run pulls
// with its own service agent, and says so if it cannot.
func (x *run) checkImage() error {
	name, tag, digest := splitImage(x.r.Image)
	host, path, _ := strings.Cut(name, "/")
	if !strings.HasSuffix(host, "-docker.pkg.dev") {
		x.say("  ! image %s is not on *-docker.pkg.dev: not checked", x.r.Image)
		return nil
	}
	ref := tag
	if digest != "" {
		ref = digest
	}
	err := x.d.Google.Head(x.ctx, "https://"+host+"/v2/"+path+"/manifests/"+ref, manifestTypes...)
	switch {
	case err == nil:
		x.say("Checked: image %s is in Artifact Registry.", x.r.Image)
	case gcp.IsNotFound(err):
		return &stop{1, "  ✗ image: " + x.r.Image + " is not in Artifact Registry\nNothing was changed."}
	case gcp.IsDenied(err), isUnauthorized(err):
		x.say("  ! image %s not checked: %s", x.r.Image, err)
	default:
		return err
	}
	return nil
}

func isUnauthorized(err error) bool {
	var e *gcp.Error
	return errors.As(err, &e) && e.Code == http.StatusUnauthorized
}

// runService is a Cloud Run service as v2 of its API writes it, and answers with its URLs and readiness.
type runService struct {
	Ingress            string        `json:"ingress"`
	InvokerIamDisabled bool          `json:"invokerIamDisabled"`
	Template           runTemplate   `json:"template"`
	URLs               []string      `json:"urls,omitempty"`
	TerminalCondition  *runCondition `json:"terminalCondition,omitempty"`
}

type runTemplate struct {
	ServiceAccount                string         `json:"serviceAccount"`
	Containers                    []runContainer `json:"containers"`
	Scaling                       runScaling     `json:"scaling"`
	MaxInstanceRequestConcurrency int            `json:"maxInstanceRequestConcurrency"`
	Timeout                       string         `json:"timeout"`
}

type runContainer struct {
	Image     string    `json:"image"`
	Env       []runEnv  `json:"env"`
	Ports     []runPort `json:"ports"`
	Resources struct {
		Limits map[string]string `json:"limits"`
	} `json:"resources"`
}

type runEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type runPort struct {
	ContainerPort int `json:"containerPort"`
}

type runScaling struct {
	MinInstanceCount int `json:"minInstanceCount"`
	MaxInstanceCount int `json:"maxInstanceCount"`
}

type runCondition struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

// notReady says why a service is not ready ("" when it is).
func (s runService) notReady() string {
	switch {
	case s.TerminalCondition == nil:
		return "not ready"
	case s.TerminalCondition.State == readyState:
		return ""
	case s.TerminalCondition.Message == "":
		return "not ready"
	}
	return "not ready (" + s.TerminalCondition.Message + ")"
}

func (s runService) image() string {
	if len(s.Template.Containers) == 0 {
		return ""
	}
	return s.Template.Containers[0].Image
}

// serviceSettings are the settings of one of the router's two services.
type serviceSettings struct {
	role, name             string
	ingress, ingressWords  string
	invokerOff             bool
	maxInstances, requests int
	timeoutSeconds         int
	memory                 string // in MiB, as Cloud Run writes it: 512Mi
}

func (x *run) controllerSettings() serviceSettings {
	return serviceSettings{role: "controller", name: x.n.controller, ingress: ingressAll, ingressWords: "ingress all",
		maxInstances: 3, requests: 10, timeoutSeconds: 900, memory: "512Mi"}
}

func (x *run) wakerSettings() serviceSettings {
	return serviceSettings{role: "waker", name: x.n.waker, ingress: ingressLB, ingressWords: "ingress internal and Cloud Load Balancing",
		invokerOff: true, maxInstances: 10, requests: 80, timeoutSeconds: 120, memory: "256Mi"}
}

func (s serviceSettings) invokerWords() string {
	if s.invokerOff {
		return "invoker check off"
	}
	return "invoker check on"
}

func (s serviceSettings) instanceWords() string {
	return fmt.Sprintf("0 to %d instances", s.maxInstances)
}
func (s serviceSettings) requestWords() string {
	return fmt.Sprintf("%d concurrent requests", s.requests)
}
func (s serviceSettings) timeoutWords() string { return fmt.Sprintf("timeout %d s", s.timeoutSeconds) }
func (s serviceSettings) memoryWords() string  { return strings.TrimSuffix(s.memory, "Mi") + " MiB" }

// controllerURL is Cloud Run's deterministic URL of the controller; the verification checks it among the service's URLs.
func (x *run) controllerURL() string {
	return "https://" + x.n.controller + "-" + x.number + "." + x.n.region + ".run.app"
}

func (x *run) servicesURL() string {
	return runV2 + "projects/" + esc(x.n.project) + "/locations/" + esc(x.n.region) + "/services"
}

func (x *run) serviceName(service string) string {
	return "projects/" + x.n.project + "/locations/" + x.n.region + "/services/" + service
}

// environment is what the router reads, for one of its roles. PORT is Cloud Run's own (containerPort).
func (x *run) environment(role string) []runEnv {
	return []runEnv{
		{"DMT_ROLE", role},
		{"DMT_PROJECT", x.n.project},
		{"DMT_PROJECT_NUMBER", x.number},
		{"DMT_REGION", x.n.region},
		{"DMT_DATABASE", x.n.database},
		{"DMT_QUEUE", x.n.queue},
		{"DMT_CONTROLLER_URL", x.controllerURL()},
		{"DMT_SERVICE_ACCOUNT", x.n.email},
		{"DMT_WAKER_NEG", x.n.neg},
		{"DMT_VERSION", x.version},
	}
}

// wantService is the service as the install writes it.
func (x *run) wantService(s serviceSettings) runService {
	c := runContainer{Image: x.r.Image, Env: x.environment(s.role), Ports: []runPort{{containerPort}}}
	c.Resources.Limits = map[string]string{"memory": s.memory}
	return runService{Ingress: s.ingress, InvokerIamDisabled: s.invokerOff, Template: runTemplate{
		ServiceAccount: x.n.email, Containers: []runContainer{c}, Scaling: runScaling{MaxInstanceCount: s.maxInstances},
		MaxInstanceRequestConcurrency: s.requests, Timeout: strconv.Itoa(s.timeoutSeconds) + "s"}}
}

// serviceDifferences say what of the service's settings an update would change, as the plan does.
func (x *run) serviceDifferences(s serviceSettings, got runService) []string {
	want := x.wantService(s)
	var out []string
	if got.image() != want.image() {
		out = append(out, "image "+want.image())
	}
	if names := envDifferences(got, want); len(names) > 0 {
		out = append(out, "environment "+strings.Join(names, ", "))
	}
	if got.Ingress != want.Ingress {
		out = append(out, s.ingressWords)
	}
	if got.InvokerIamDisabled != want.InvokerIamDisabled {
		out = append(out, s.invokerWords())
	}
	t := got.Template
	if t.ServiceAccount != want.Template.ServiceAccount {
		out = append(out, "service account "+x.n.email)
	}
	if t.Scaling != want.Template.Scaling {
		out = append(out, s.instanceWords())
	}
	if t.MaxInstanceRequestConcurrency != s.requests {
		out = append(out, s.requestWords())
	}
	if !sameDuration(t.Timeout, want.Template.Timeout) {
		out = append(out, s.timeoutWords())
	}
	if len(t.Containers) == 0 || t.Containers[0].Resources.Limits["memory"] != s.memory {
		out = append(out, s.memoryWords())
	}
	return out
}

// envDifferences are the names of the variables that differ: those of want first, in its order, then those only got has.
func envDifferences(got, want runService) []string {
	values := map[string]string{}
	if len(got.Template.Containers) > 0 {
		for _, e := range got.Template.Containers[0].Env {
			values[e.Name] = e.Value
		}
	}
	var names []string
	for _, e := range want.Template.Containers[0].Env {
		if v, ok := values[e.Name]; !ok || v != e.Value {
			names = append(names, e.Name)
		}
		delete(values, e.Name)
	}
	var extra []string
	for name := range values {
		extra = append(extra, name)
	}
	slices.Sort(extra)
	return append(names, extra...)
}

// planService reads a service and plans what the cloudRun step does to it: its lines, its write (nil for none) and
// whether the service exists.
func (x *run) planService(s serviceSettings) (lines []string, write func() (string, error), exists bool, err error) {
	var got runService
	found, err := x.get(runAPI, x.servicesURL()+"/"+esc(s.name), &got)
	if err != nil {
		return nil, nil, false, err
	}
	if !found {
		line := fmt.Sprintf("+ service %s (image %s, %s, %s, %s, %s, %s, %s, as %s)", s.name, x.r.Image, s.ingressWords, s.invokerWords(),
			s.instanceWords(), s.requestWords(), s.timeoutWords(), s.memoryWords(), x.n.email)
		return []string{line}, func() (string, error) { return x.writeService(s, true) }, false, nil
	}
	differences := x.serviceDifferences(s, got)
	if why := got.notReady(); why != "" {
		differences = append(differences, why)
	}
	if len(differences) == 0 {
		x.found("service", x.serviceName(s.name))
		return nil, nil, true, nil
	}
	line := "~ service " + s.name + ": " + strings.Join(differences, ", ")
	return []string{line}, func() (string, error) { return x.writeService(s, false) }, true, nil
}

// writeService creates or updates a service and waits for it to serve its new revision.
func (x *run) writeService(s serviceSettings, create bool) (string, error) {
	method, u, done := http.MethodPatch, x.servicesURL()+"/"+esc(s.name)+"?updateMask="+serviceUpdateFields, "updated "+s.name
	if create {
		method, u, done = http.MethodPost, x.servicesURL()+"?serviceId="+url.QueryEscape(s.name), "created "+s.name
		if s.invokerOff {
			done += " (invoker check off)"
		}
	}
	if err := x.recordFirst("service", x.serviceName(s.name)); err != nil {
		return "", err
	}
	var op longOperation
	err := x.call(method, u, x.wantService(s), &op)
	if err == nil {
		err = x.wait(runV2, op, serviceTimeout)
	}
	if err != nil {
		if s.invokerOff && strings.Contains(err.Error(), invokerPolicy) {
			return "", fmt.Errorf("the organization policy %s refuses the waker's invoker check off: ask your organization's policy administrator for an exception on project %s (constraints/%s not enforced there), the waker's ingress still admits only the load balancer (%w)",
				invokerPolicy, x.n.project, invokerPolicy, err)
		}
		return "", err
	}
	return done, nil
}

// noteImage records the image the two services run.
func (x *run) noteImage() {
	if x.rec.Image != x.r.Image || x.rec.RouterVersion != x.version {
		x.rec.Image, x.rec.RouterVersion = x.r.Image, x.version
		x.recChanged = true
	}
}

// planCloudRun plans the cloudRun step: the controller, its invoker binding, the waker.
func (x *run) planCloudRun() (*step, error) {
	controller, waker := x.controllerSettings(), x.wakerSettings()
	lines, writeController, exists, err := x.planService(controller)
	if err != nil {
		return nil, err
	}
	g := x.grantOn("controller")
	rp := RunPolicy(x.n.project, x.n.region, x.n.controller)
	grant := true
	if exists {
		p, err := x.readPolicy(rp)
		if err != nil {
			return nil, err
		}
		if holds(p, g.Grant) {
			grant = false
			x.foundGrant(g)
		}
	}
	if grant {
		lines = append(lines, "+ grant "+x.describe(g.Grant, g.On))
	}
	wakerLines, writeWaker, _, err := x.planService(waker)
	if err != nil {
		return nil, err
	}
	lines = append(lines, wakerLines...)
	if len(lines) == 0 {
		x.noteImage()
		return nil, nil
	}
	return &step{name: "cloudRun", lines: lines, apply: func() (string, error) {
		if err := x.catchUp(); err != nil {
			return "", err
		}
		var done []string
		if writeController != nil {
			d, err := writeController()
			if err != nil {
				return "", err
			}
			done = append(done, d)
		}
		if grant {
			if err := x.bind(rp, g); err != nil {
				return "", err
			}
			if writeController != nil {
				done = append(done, "granted "+g.Role+" on it")
			} else {
				done = append(done, "granted "+g.Role+" on "+x.n.controller)
			}
		}
		if writeWaker != nil {
			d, err := writeWaker()
			if err != nil {
				return "", err
			}
			done = append(done, d)
		}
		x.noteImage()
		if err := x.policyChanged("cloudRun"); err != nil {
			return "", err
		}
		return strings.Join(done, ", "), nil
	}}, nil
}

// googleNEG is a serverless network endpoint group.
type googleNEG struct {
	Name                string `json:"name"`
	NetworkEndpointType string `json:"networkEndpointType"`
	CloudRun            struct {
		Service string `json:"service"`
	} `json:"cloudRun"`
}

func (x *run) negsURL() string {
	return "https://compute.googleapis.com/compute/v1/projects/" + esc(x.n.project) + "/regions/" + esc(x.n.region) + "/networkEndpointGroups"
}

func (x *run) negName() string {
	return "projects/" + x.n.project + "/regions/" + x.n.region + "/networkEndpointGroups/" + x.n.neg
}

// planNEG plans the neg step. A group cannot be updated: one of that name serving something else is the owner's to
// delete.
func (x *run) planNEG() (*step, error) {
	var got googleNEG
	found, err := x.get(computeAPI, x.negsURL()+"/"+esc(x.n.neg), &got)
	if err != nil {
		return nil, err
	}
	if found && got.CloudRun.Service != x.n.waker {
		return nil, &stop{1, fmt.Sprintf("The network endpoint group %s exists and serves %s, not %s: delete it, or choose another region, then run the same command again.\nNothing was changed.",
			x.n.neg, got.CloudRun.Service, x.n.waker)}
	}
	if found {
		x.found("neg", x.negName())
		return nil, nil
	}
	line := "+ network endpoint group " + x.n.neg + " (serverless, Cloud Run service " + x.n.waker + ")"
	return &step{name: "neg", lines: []string{line}, apply: func() (string, error) {
		if err := x.catchUp(); err != nil {
			return "", err
		}
		in := googleNEG{Name: x.n.neg, NetworkEndpointType: "SERVERLESS"}
		in.CloudRun.Service = x.n.waker
		if err := x.recordFirst("neg", x.negName()); err != nil {
			return "", err
		}
		var op struct {
			SelfLink string `json:"selfLink"`
		}
		if err := x.call(http.MethodPost, x.negsURL(), in, &op); err != nil {
			return "", err
		}
		if err := gcp.WaitCompute(x.ctx, x.d.Google, op.SelfLink, negTimeout); err != nil {
			return "", err
		}
		if err := x.policyChanged("neg"); err != nil {
			return "", err
		}
		return "created " + x.n.neg, nil
	}}, nil
}

// googleJob is a Cloud Scheduler job.
type googleJob struct {
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

func (x *run) jobsURL() string {
	return "https://cloudscheduler.googleapis.com/v1/projects/" + esc(x.n.project) + "/locations/" + esc(x.n.region) + "/jobs"
}

func (x *run) jobName() string {
	return "projects/" + x.n.project + "/locations/" + x.n.region + "/jobs/" + x.n.job
}

// wantJob is the job as the install writes it.
func (x *run) wantJob() googleJob {
	j := googleJob{Name: x.jobName(), Schedule: jobSchedule, TimeZone: jobTimeZone, AttemptDeadline: jobDeadline}
	j.HTTPTarget.URI, j.HTTPTarget.HTTPMethod = x.controllerURL()+"/v1/check", http.MethodPost
	j.HTTPTarget.OIDCToken.ServiceAccountEmail, j.HTTPTarget.OIDCToken.Audience = x.n.email, x.controllerURL()
	return j
}

// jobAsWanted tells whether a job has the install's settings.
func (x *run) jobAsWanted(got googleJob) bool {
	want := x.wantJob()
	return got.Schedule == want.Schedule && got.TimeZone == want.TimeZone && sameDuration(got.AttemptDeadline, want.AttemptDeadline) &&
		got.HTTPTarget == want.HTTPTarget
}

func (x *run) jobWords() string {
	return fmt.Sprintf("%s %s, attempt deadline 300 s, POST %s/v1/check, OIDC of %s, audience %s", jobSchedule, jobTimeZone, x.controllerURL(), x.n.email, x.controllerURL())
}

func (x *run) planJob() (*step, error) {
	var got googleJob
	found, err := x.get(schedulerAPI, x.jobsURL()+"/"+esc(x.n.job), &got)
	if err != nil {
		return nil, err
	}
	if found && x.jobAsWanted(got) {
		x.found("job", x.jobName())
		return nil, nil
	}
	method, u, line, done := http.MethodPost, x.jobsURL(), "+ job "+x.n.job+" ("+x.jobWords()+")", "created "+x.n.job
	if found {
		method, u = http.MethodPatch, x.jobsURL()+"/"+esc(x.n.job)+"?updateMask="+jobUpdateFields
		line, done = "~ job "+x.n.job+": "+x.jobWords(), "updated "+x.n.job
	}
	return &step{name: "job", lines: []string{line}, apply: func() (string, error) {
		if err := x.catchUp(); err != nil {
			return "", err
		}
		if err := x.recordFirst("job", x.jobName()); err != nil {
			return "", err
		}
		if err := x.call(method, u, x.wantJob(), nil); err != nil {
			return "", err
		}
		if err := x.policyChanged("job"); err != nil {
			return "", err
		}
		return done, nil
	}}, nil
}

// verifyRouter is the read-only verification of the router part: both services ready and as written, the controller's
// URL among its URLs, the network endpoint group, the job, and the controller's health answering the image's version to
// the caller's identity token. The waker admits only the load balancer: it is checked through a workload, at its first
// onboarding. It returns the problems found.
func (x *run) verifyRouter() []string {
	if x.r.Image == "" {
		return nil
	}
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	for _, s := range []serviceSettings{x.controllerSettings(), x.wakerSettings()} {
		var got runService
		if err := x.d.Google.Do(x.ctx, http.MethodGet, x.servicesURL()+"/"+esc(s.name), nil, &got); err != nil {
			add("service %s: %s", s.name, err)
			continue
		}
		if why := got.notReady(); why != "" {
			add("service %s is %s", s.name, why)
		}
		if differences := x.serviceDifferences(s, got); len(differences) > 0 {
			add("service %s differs: %s", s.name, strings.Join(differences, ", "))
		}
		if s.role == "controller" && !slices.Contains(got.URLs, x.controllerURL()) {
			add("the URL %s is not among the controller's: %s", x.controllerURL(), strings.Join(got.URLs, ", "))
		}
	}
	var neg googleNEG
	if err := x.d.Google.Do(x.ctx, http.MethodGet, x.negsURL()+"/"+esc(x.n.neg), nil, &neg); err != nil {
		add("network endpoint group %s: %s", x.n.neg, err)
	} else if neg.CloudRun.Service != x.n.waker {
		add("network endpoint group %s serves %s, not %s", x.n.neg, neg.CloudRun.Service, x.n.waker)
	}
	var job googleJob
	if err := x.d.Google.Do(x.ctx, http.MethodGet, x.jobsURL()+"/"+esc(x.n.job), nil, &job); err != nil {
		add("job %s: %s", x.n.job, err)
	} else if !x.jobAsWanted(job) {
		add("job %s has other settings than %s", x.n.job, x.jobWords())
	}
	if version, err := x.health(); err != nil {
		add("%s/v1/health: %s", x.controllerURL(), err)
	} else if version != x.version {
		add("%s/v1/health answers version %s, not the image's %s", x.controllerURL(), version, x.version)
	}
	return problems
}

// health is the version the controller answers to the caller's identity token.
func (x *run) health() (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	err := x.d.Google.Invoke(x.ctx, http.MethodGet, x.controllerURL()+"/v1/health", x.controllerURL(), nil, &out)
	return out.Version, err
}
