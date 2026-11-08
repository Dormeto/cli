package gcponboard

// `dormeto gcp workloads list|show|onboard|offboard|nap|wake` (spec §5): the router's controller called at its Cloud Run
// URL (https://dmtr-<region>-controller-<project number>.<region>.run.app) with the operator's identity token, audience
// that URL. Onboarding asks the controller for a dry run first (the eligibility, the VMs, groups and databases it would
// manage), binds the tag dormeto=managed to each of them as the operator (the router holds no tag permission, else it could
// tag any VM and then stop it), then onboards; with --gke-cluster, between the two, it reads the owner in the cluster and
// applies its Kubernetes RBAC (the coverage design's §8); with --request-logs, last before the onboarding, it turns the
// backend service's request logs on (requestlogs.go). Offboarding removes the controller's workload, then the tag bindings
// and the RBAC, and sets the request logs back. Exit codes: 0; 1 when a permission is missing, the workload is not eligible,
// Google, Kubernetes or the controller refuses, or --force leaves something it names; 2 for a bad flag or a missing answer;
// 130 when interrupted.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/kubernetes"
)

const (
	// The idle window's range is the router's: AWS's onboarding (router/lambda/src/lib/onboarding.mjs).
	minIdleMinutes, maxIdleMinutes = 5, 1440

	// The wake's fits the controller's request: its range is the router's too (router/gcp/src/onboarding.mjs).
	minWakeTimeout, maxWakeTimeout = 60, 1700

	// maxBackendServiceName is what Compute Engine allows of a backend service's name; the workload's waker backend service
	// is dmtr-<region>-<workload>.
	maxBackendServiceName = 63

	// undoTimeout bounds what a stopped onboarding removes, as it no longer stops at an interrupt.
	undoTimeout = 2 * time.Minute

	notSent = "Interrupted before the onboarding was sent."

	// A workload's backend kinds beyond a VM's and a group's.
	kindDatabase, kindPods = "cloudsql", "k8s"
)

var (
	workloadIDPattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)
	// databaseNamePattern is the name of a Cloud SQL instance, the router's INSTANCE_NAME (router/gcp/src/cloudsql.mjs).
	databaseNamePattern = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,96}[a-z0-9])?$`)
)

const workloadIDRule = "lowercase letters, digits and dashes, starting with a letter and ending with a letter or a digit, at most 40 characters"

// backend is a VM, a managed group, a Cloud SQL instance or a GKE owner of a workload, as the controller names it.
type backend struct {
	Kind     string `json:"kind"` // vm, mig, cloudsql or k8s
	Location string `json:"location,omitempty"`
	Name     string `json:"name,omitempty"`
	ID       string `json:"id,omitempty"` // numeric: a tag binding's parent names it

	// A k8s backend's: where its owner is. It takes no tag.
	Cluster   *gkeCluster       `json:"cluster,omitempty"`
	Namespace string            `json:"namespace,omitempty"`
	Service   string            `json:"service,omitempty"`
	Owner     *kubernetes.Owner `json:"owner,omitempty"`
}

func (b backend) String() string {
	switch b.Kind {
	case kindDatabase:
		return "Cloud SQL instance " + b.Name
	case kindPods:
		owner := "Service " + b.Service
		if b.Owner != nil {
			owner = b.Owner.Kind + " " + b.Owner.Name
		}
		cluster := ""
		if b.Cluster != nil {
			cluster = ", cluster " + b.Cluster.String()
		}
		return fmt.Sprintf("%s (namespace %s%s)", owner, b.Namespace, cluster)
	}
	kind := "group"
	if b.Kind == "vm" {
		kind = "vm"
	}
	return fmt.Sprintf("%s %s (%s)", kind, b.Name, b.Location)
}

// workload is the controller's Workload.
type workload struct {
	ID                  string         `json:"id"`
	State               string         `json:"state"`
	URLMap              string         `json:"urlMap"`
	BackendService      string         `json:"backendService"`
	WakerBackendService string         `json:"wakerBackendService"`
	Hosts               []string       `json:"hosts"`
	Settings            map[string]any `json:"settings"`
	Backends            []backend      `json:"backends"`
	AwakeSince          string         `json:"awakeSince"`
	LastWake            *struct {
		DurationMs int `json:"durationMs"`
	} `json:"lastWake"`
	Failure json.RawMessage `json:"failure"`
	Paused  *pausedOf       `json:"paused"`
	LB      *lbRef          `json:"lb"`
	// The Cloud SQL instances named at its onboarding that no longer exist, as the router last read them.
	CompanionsGone []string `json:"companionsGone"`
	// What its onboarding found of the backend service's request logs, when it turned them on; and what the idle check read at
	// its last check that counted a request: "request logs" or "request count".
	RequestLogs *requestLogsIn `json:"requestLogs"`
	IdleReads   string         `json:"idleReads"`
}

// dryRun is the controller's answer to an onboarding dry run.
type dryRun struct {
	Eligible bool       `json:"eligible"`
	Reasons  []string   `json:"reasons"`
	VMs      []vmRef    `json:"vms"`
	Groups   []groupRef `json:"groups"`
	LB       *lbRef     `json:"lb"`
	// Databases are the Cloud SQL instances the request named, as the controller found them.
	Databases []databaseRef `json:"databases"`
	// GKE is what the load balancer's NEGs say of the cluster the request named. The request's flag names the cluster.
	GKE *gkeRef `json:"gke"`
}

type gkeRef struct {
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	Autopilot bool   `json:"autopilot"`
}

type databaseRef struct {
	Name string `json:"name"`
	// Replicas are its read replicas: Google stops only the primary (M8).
	Replicas []string `json:"replicas,omitempty"`
}

// lbRef is a workload's load balancer as the controller names it: global, regional in a region, or classic.
type lbRef struct {
	Kind   string `json:"kind"`
	Region string `json:"region"`
}

func (l lbRef) String() string {
	if l.Region != "" {
		return l.Kind + " (" + l.Region + ")"
	}
	return l.Kind
}

type vmRef struct {
	Zone, Name, ID string
}

type groupRef struct {
	Kind, Location, Name, ID string
}

// target is a VM, a group or a database that carries the tag: its place and what a tag binding's parent names it by.
type target struct {
	label    string // "vm web-1 (europe-west1-b)"
	location string // the zone or region: the tag binding endpoint's prefix
	path     string // zones/<zone>/instances/<id>, zones/<zone>/instanceGroupManagers/<id> or regions/<region>/instanceGroupManagers/<id>
	resource string // the whole parent of what is not Compute Engine's: then no path
	read     bool   // this run has just read it: a 403 on its bindings does not mean it is gone
}

func (t target) parent(number string) string {
	if t.resource != "" {
		return t.resource
	}
	return "//compute.googleapis.com/projects/" + number + "/" + t.path
}

func (t target) base() string {
	return "https://" + t.location + "-cloudresourcemanager.googleapis.com/v3/"
}

func (d dryRun) targets() []target {
	var out []target
	for _, v := range d.VMs {
		out = append(out, target{label: backend{Kind: "vm", Location: v.Zone, Name: v.Name}.String(), location: v.Zone, path: "zones/" + v.Zone + "/instances/" + v.ID})
	}
	for _, g := range d.Groups {
		place := "zones/"
		if g.Kind == "regional" {
			place = "regions/"
		}
		out = append(out, target{label: backend{Kind: "mig", Location: g.Location, Name: g.Name}.String(), location: g.Location, path: place + g.Location + "/instanceGroupManagers/" + g.ID})
	}
	return out
}

// labels name what the onboarding would tag: the dry run's VMs and groups, and the databases named.
func (d dryRun) labels(databases []string) []string {
	var out []string
	for _, t := range d.targets() {
		out = append(out, t.label)
	}
	for _, name := range databases {
		out = append(out, backend{Kind: kindDatabase, Name: name}.String())
	}
	return out
}

// WorkloadsList is `dormeto gcp workloads list`.
func WorkloadsList(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.list())
}

// WorkloadsShow is `dormeto gcp workloads show <id>`.
func WorkloadsShow(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.show())
}

// WorkloadsNap is `dormeto gcp workloads nap <id>`.
func WorkloadsNap(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.moves("nap"))
}

// WorkloadsWake is `dormeto gcp workloads wake <id>`.
func WorkloadsWake(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.moves("wake"))
}

// WorkloadsOnboard is `dormeto gcp workloads onboard`.
func WorkloadsOnboard(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.onboard())
}

// WorkloadsOffboard is `dormeto gcp workloads offboard <id>`.
func WorkloadsOffboard(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.offboard())
}

// workloadsEnd is the exit code of a workloads run: its stop's, 130 when interrupted, else 1 with the error, a refusal
// reading the same in every command. Unlike the install's, it never says nothing was changed: an onboarding that failed
// after the tags says what it did.
func (x *run) workloadsEnd(err error) int {
	var s *stop
	switch {
	case err == nil:
		return 0
	case errors.As(err, &s):
		if s.msg != "" {
			fmt.Fprintln(x.d.Err, s.msg)
		}
		return s.code
	case x.ctx.Err() != nil:
		fmt.Fprintln(x.d.Err, "Interrupted.")
		return exitInterrupted
	}
	fmt.Fprintln(x.d.Err, refusal(err))
	return 1
}

// require validates the project and region and asks the permissions of one command, with those of the features named, the
// missing ones named and nothing done.
func (x *run) require(command string, features ...string) error {
	if err := x.validate(); err != nil {
		return err
	}
	r := x.r
	r.With = featureSet(r.With, features)
	rep, err := check(x.ctx, x.d, r, []string{command})
	if err != nil {
		return err
	}
	var refused []string
	for _, c := range rep.Commands {
		if !c.OK() {
			refused = append(refused, fmt.Sprintf("  ✗ %s: missing %s", c.Command, strings.Join(c.Missing, ", ")))
		}
	}
	if len(refused) > 0 {
		msg := ""
		if !rep.Visible {
			msg = fmt.Sprintf("Project %s is not visible to %s: no role in it\n", x.n.project, rep.Account)
		}
		return &stop{1, msg + strings.Join(refused, "\n") + "\nNothing was changed."}
	}
	x.number = rep.Project.Number
	return nil
}

// refusal is the error as one line: the controller's message, else Google's.
func refusal(err error) string {
	var e *gcp.Error
	if errors.As(err, &e) && e.Message != "" && e.Reason != "" && !strings.HasPrefix(e.Reason, "IAM_") {
		return e.Message
	}
	return err.Error()
}

// controller calls the router's controller as the operator.
func (x *run) controller(method, path string, in, out any) error {
	u := x.controllerURL()
	return x.d.Google.Invoke(x.ctx, method, u+path, u, in, out)
}

func (x *run) workloadPath() (string, error) {
	if x.r.ID == "" {
		return "", &stop{2, "the workload's ID is needed"}
	}
	return "/v1/workloads/" + esc(x.r.ID), nil
}

func (x *run) print(raw json.RawMessage, text func(w workload)) error {
	if x.r.JSON {
		fmt.Fprintf(x.d.Out, "%s\n", raw)
		return nil
	}
	var w workload
	if err := json.Unmarshal(raw, &w); err != nil {
		return fmt.Errorf("the controller's answer is not a workload: %w", err)
	}
	text(w)
	return nil
}

func (x *run) list() error {
	if err := x.require("workloads"); err != nil {
		return err
	}
	var raw json.RawMessage
	if err := x.controller(http.MethodGet, "/v1/workloads", nil, &raw); err != nil {
		return err
	}
	if x.r.JSON {
		fmt.Fprintf(x.d.Out, "%s\n", raw)
		return nil
	}
	var all []workload
	if err := json.Unmarshal(raw, &all); err != nil {
		return fmt.Errorf("the controller's answer is not a list of workloads: %w", err)
	}
	if len(all) == 0 {
		x.say("no workload is onboarded in router %s", x.n.router)
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	for _, w := range all {
		mark := ""
		if w.Paused != nil {
			mark = " [paused]"
		}
		x.say("%-30s %-8s %s%s", w.ID, w.State, w.BackendService, mark)
	}
	return nil
}

func (x *run) show() error {
	if err := x.require("workloads"); err != nil {
		return err
	}
	path, err := x.workloadPath()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := x.controller(http.MethodGet, path, nil, &raw); err != nil {
		return err
	}
	return x.print(raw, func(w workload) {
		x.say("Workload %s: %s", w.ID, w.State)
		x.details(w)
		if w.AwakeSince != "" {
			x.say("  awake since: %s", w.AwakeSince)
		}
		if w.LastWake != nil {
			x.say("  last wake: %.1f s", float64(w.LastWake.DurationMs)/1000)
		}
		if w.IdleReads != "" {
			x.say("  idle check: %s", w.IdleReads)
		}
		if len(w.Failure) > 0 && string(w.Failure) != "null" {
			x.say("  failure: %s", w.Failure)
		}
		if w.Paused != nil {
			x.say("  %s (since %s)", w.Paused, w.Paused.At)
		}
	})
}

// details prints a workload's configuration.
func (x *run) details(w workload) {
	line := func(name, value string) {
		if value != "" {
			x.say("  %s: %s", name, value)
		}
	}
	if w.LB != nil {
		line("load balancer", w.LB.String())
	}
	line("URL map", w.URLMap)
	line("backend service", w.BackendService)
	line("waker backend service", w.WakerBackendService)
	line("hosts", strings.Join(w.Hosts, ", "))
	var settings []string
	for k, v := range w.Settings {
		settings = append(settings, fmt.Sprintf("%s %v", k, v))
	}
	sort.Strings(settings)
	line("settings", strings.Join(settings, ", "))
	for _, b := range w.Backends {
		x.say("  backend: %s", b)
	}
	for _, name := range w.CompanionsGone {
		x.say("  ! Cloud SQL instance %s no longer exists: its workload no longer naps; offboard and onboard it again", name)
	}
}

// moves is nap or wake: the controller's POST /v1/workloads/<id>/<verb>.
func (x *run) moves(verb string) error {
	if err := x.require("workloads"); err != nil {
		return err
	}
	path, err := x.workloadPath()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := x.controller(http.MethodPost, path+"/"+verb, nil, &raw); err != nil {
		return err
	}
	return x.print(raw, func(w workload) {
		// The controller acts only from another state, and answers at once: a workload there already was asked nothing.
		if there := map[string]string{"nap": "ASLEEP", "wake": "AWAKE"}[verb]; w.State == there {
			x.say("Workload %s is already %s: nothing was asked.", w.ID, strings.ToLower(there))
			return
		}
		switch {
		case verb == "wake" && w.State == "NAPPING":
			x.say("Wake of workload %s asked while it is napping: the wake is kept and runs once the nap ends; current state: %s.", w.ID, w.State)
		default:
			x.say("%s of workload %s asked: the controller runs it through Cloud Tasks; current state: %s.", strings.ToUpper(verb[:1])+verb[1:], w.ID, w.State)
		}
		x.say("Follow it with: %s", x.showCommand(w.ID))
	})
}

// showCommand is the command that shows a workload.
func (x *run) showCommand(id string) string {
	show := fmt.Sprintf("dormeto gcp workloads show %s --project %s --region %s", id, x.n.project, x.n.region)
	if x.r.Configuration != "" {
		show += " --gcloud-configuration " + x.r.Configuration
	}
	return show
}

// onboardingIn is what the controller takes to onboard a workload, and to dry run it.
type onboardingIn struct {
	URLMap         string         `json:"urlMap"`
	BackendService string         `json:"backendService"`
	ID             string         `json:"id"`
	Settings       map[string]any `json:"settings,omitempty"`
	LoadBalancer   string         `json:"loadBalancer,omitempty"`
	CloudSQL       []string       `json:"cloudSql,omitempty"`
	GKE            *onboardingGKE `json:"gke,omitempty"`
	RequestLogs    *requestLogsIn `json:"requestLogs,omitempty"`
}

// onboardingGKE is a GKE workload in the controller's request: the dry run names the cluster alone, and answers the namespace
// and the Service; the onboarding names them all, with the owner the CLI read.
type onboardingGKE struct {
	Cluster   gkeCluster        `json:"cluster"`
	Namespace string            `json:"namespace,omitempty"`
	Service   string            `json:"service,omitempty"`
	Owner     *kubernetes.Owner `json:"owner,omitempty"`
}

// gkeCluster is a cluster as the controller names it.
type gkeCluster struct {
	Location string `json:"location"`
	Name     string `json:"name"`
}

func (c gkeCluster) String() string { return c.Location + "/" + c.Name }

// onboardingInput validates the flags, before any call: exit 2 for a bad one.
func (x *run) onboardingInput() (onboardingIn, error) {
	r := x.r
	switch {
	case r.URLMap == "":
		return onboardingIn{}, &stop{2, "--url-map: needed"}
	case r.BackendService == "":
		return onboardingIn{}, &stop{2, "--backend-service: needed"}
	}
	in := onboardingIn{URLMap: r.URLMap, BackendService: r.BackendService, LoadBalancer: r.LoadBalancer, CloudSQL: r.CloudSQL, Settings: map[string]any{}}
	if v := r.LoadBalancer; v != "" && !slices.Contains([]string{"global", "regional", "classic"}, v) {
		return in, &stop{2, fmt.Sprintf("--load-balancer: %q is not global, regional or classic", v)}
	}
	if err := checkDatabaseNames(r.CloudSQL); err != nil {
		return in, err
	}
	if r.GKECluster != "" {
		location, name, err := clusterOf(r.GKECluster)
		if err != nil {
			return in, err
		}
		if r.JSON && !r.Yes && !r.DryRun {
			return in, &stop{2, "--yes: needed with --json"}
		}
		in.GKE = &onboardingGKE{Cluster: gkeCluster{location, name}}
	}
	if v := r.WakeTimeout; v != nil {
		if *v < minWakeTimeout || *v > maxWakeTimeout {
			return in, &stop{2, fmt.Sprintf("--wake-timeout: %d is not from %d to %d", *v, minWakeTimeout, maxWakeTimeout)}
		}
		in.Settings["wakeTimeoutSeconds"] = *v
	}
	if v := r.IdleMinutes; v != nil {
		if *v < minIdleMinutes || *v > maxIdleMinutes {
			return in, &stop{2, fmt.Sprintf("--idle-minutes: %d is not from %d to %d", *v, minIdleMinutes, maxIdleMinutes)}
		}
		in.Settings["idleMinutes"] = *v
	}
	if r.Hibernate != nil {
		in.Settings["hibernate"] = *r.Hibernate
	}
	if len(in.Settings) == 0 {
		in.Settings = nil
	}
	id, err := x.workloadID()
	in.ID = id
	return in, err
}

// checkDatabaseNames validates --cloud-sql: exit 2 for a name that is no Cloud SQL instance's, or one named twice.
func checkDatabaseNames(names []string) error {
	for i, name := range names {
		if !databaseNamePattern.MatchString(name) {
			return &stop{2, fmt.Sprintf("--cloud-sql: %q is not the name of a Cloud SQL instance", name)}
		}
		if slices.Contains(names[:i], name) {
			return &stop{2, fmt.Sprintf("--cloud-sql: %s is named twice", name)}
		}
	}
	return nil
}

// workloadID is --id, else the backend service's name when it is a valid ID, else asked on a terminal.
func (x *run) workloadID() (string, error) {
	id := x.r.ID
	if id == "" {
		id = x.r.BackendService
		if !workloadIDPattern.MatchString(id) {
			if !x.d.Terminal {
				return "", &stop{2, fmt.Sprintf("--id: needed, the backend service %q is not a valid workload ID (%s)", id, workloadIDRule)}
			}
			var err error
			if id, err = x.d.Prompter.Ask(fmt.Sprintf("Workload ID (the backend service %q is not a valid one: %s)", id, workloadIDRule), ""); err != nil {
				return "", &stop{2, "--id: needed: " + err.Error()}
			}
		}
	}
	if !workloadIDPattern.MatchString(id) {
		return "", &stop{2, fmt.Sprintf("--id: %q is not a valid workload ID: %s", id, workloadIDRule)}
	}
	if name := "dmtr-" + x.r.Region + "-" + id; len(name) > maxBackendServiceName {
		return "", &stop{2, fmt.Sprintf("--id: %s would name the waker backend service %s, longer than the %d characters of a Compute Engine name: use a shorter --id", id, name, maxBackendServiceName)}
	}
	return id, nil
}

func (x *run) onboard() error {
	if err := x.validate(); err != nil {
		return err
	}
	in, err := x.onboardingInput()
	if err != nil {
		return err
	}
	features := x.onboardFeatures()
	if err := x.require("onboard", features...); err != nil {
		return err
	}
	if err := x.needFeatures(features); err != nil {
		return err
	}
	var raw json.RawMessage
	if err := x.controller(http.MethodPost, "/v1/workloads?dryRun=true", in, &raw); err != nil {
		return err
	}
	var dry dryRun
	if err := json.Unmarshal(raw, &dry); err != nil {
		return fmt.Errorf("the controller's dry run is not understood: %w", err)
	}
	if in.GKE == nil {
		dry.GKE = nil // not asked: what the flags name decides
	}
	if !dry.Eligible {
		return &stop{1, fmt.Sprintf("Backend service %s is not eligible:\n  - %s\nNothing was changed.", in.BackendService, strings.Join(dry.Reasons, "\n  - "))}
	}
	if err := x.covers(dry); err != nil {
		return err
	}
	x.warnStandard(dry)
	x.warnReplicas(dry)
	// Read before any change: what --request-logs sets back, which stops on a failed read; without it, whether the idle check
	// reads the logs, which decides the warning alone.
	logs := requestLogs{BackendService: in.BackendService, Region: x.regionOf(dry.LB)}
	service, unread := x.readLogs(logs.BackendService, logs.Region)
	if unread != nil && x.r.RequestLogs {
		return &stop{1, fmt.Sprintf("The request logs of backend service %s are not read: %s\nNothing was changed.", in.BackendService, refusal(unread))}
	}
	logs.Before = service.setting()
	turnOn := x.r.RequestLogs && !logs.Before.everyRequest()
	if x.r.DryRun {
		if x.r.JSON {
			fmt.Fprintf(x.d.Out, "%s\n", raw)
			x.warnCount(logs, unread)
			return nil
		}
		if labels := dry.labels(x.r.CloudSQL); len(labels) > 0 || dry.GKE == nil {
			x.say("Backend service %s is eligible as workload %s; onboarding would bind the tag dormeto=managed to:", in.BackendService, in.ID)
			for _, label := range labels {
				x.say("  %s", label)
			}
		} else {
			x.say("Backend service %s is eligible as workload %s.", in.BackendService, in.ID)
		}
		if dry.GKE != nil {
			rb := x.onboardingRbac(in.ID, dry.GKE)
			x.say("Onboarding would read the owner of Service %s in cluster %s, then ask and apply the Role and RoleBinding %s of namespace %s; read them with: %s", dry.GKE.Service, rb.Cluster, rb.name(), rb.Namespace, x.accessCommand("show", rb.entry()))
		}
		if turnOn {
			x.say("Onboarding would turn on the request logs of backend service %s: sample rate 1.0 (now: %s)", in.BackendService, logs.Before)
		}
		x.warnCount(logs, unread)
		return nil
	}
	targets := dry.targets()
	// What the flags name, which covers has found in the dry run: nothing else gets the tag.
	for _, name := range x.r.CloudSQL {
		t, found, err := x.databaseTarget(name)
		if err == nil && !found {
			err = fmt.Errorf("it is not found")
		}
		if err != nil {
			return &stop{1, fmt.Sprintf("Cloud SQL instance %s is not read, and its region says where its tag goes: %s\nNothing was changed.", name, refusal(err))}
		}
		targets = append(targets, t)
	}
	// A GKE workload alone needs no tag.
	value := ""
	if len(targets) > 0 {
		var found bool
		if value, found, err = x.tag("tagValues", x.n.project+"/dormeto/managed"); err != nil {
			return err
		}
		if !found {
			return &stop{1, fmt.Sprintf("The tag dormeto/managed does not exist in project %s: run dormeto gcp install first.\nNothing was changed.", x.n.project)}
		}
	}
	var grant *rbacGrant
	if dry.GKE != nil {
		rb := x.onboardingRbac(in.ID, dry.GKE)
		owner, err := x.readOwner(rb, dry.GKE.Service)
		if err != nil {
			return err
		}
		in.GKE.Namespace, in.GKE.Service, in.GKE.Owner = rb.Namespace, dry.GKE.Service, &owner
		g, err := x.grantRbac(rb)
		if err != nil {
			return err
		}
		grant = &g
		if x.r.JSON {
			// The answer stays JSON, and what was applied is said all the same.
			for _, d := range g.done {
				fmt.Fprintf(x.d.Err, "  ✓ %s, namespace %s, cluster %s\n", d, rb.Namespace, rb.Cluster)
			}
		}
	}
	var created []target
	for _, t := range targets {
		made, err := x.tagBind(t, value)
		if made {
			created = append(created, t)
		}
		if err != nil {
			what := fmt.Sprintf("the tag is not bound to %s: %s", t.label, refusal(err))
			if x.ctx.Err() != nil {
				what = notSent
				if !made {
					what += fmt.Sprintf(" The tag of %s may be bound: it was being asked for, and no answer came.", t.label)
				}
			}
			return x.ended(what + "\n" + x.undoOnboarding(grant, created, value, nil))
		}
	}
	if x.ctx.Err() != nil {
		return x.ended(notSent + "\n" + x.undoOnboarding(grant, created, value, nil))
	}
	// Last before the onboarding, which a refusal undoes: the request logs, every request.
	var turned *requestLogs
	if turnOn {
		changed, err := x.turnOnLogs(logs, service)
		if changed {
			turned = &logs
		}
		if err != nil {
			what := fmt.Sprintf("the request logs of backend service %s are not turned on: %s", in.BackendService, x.why(err))
			return x.ended(what + "\n" + x.undoOnboarding(grant, created, value, turned))
		}
		in.RequestLogs = &requestLogsIn{Before: &logs.Before}
	}
	if err := x.controller(http.MethodPost, "/v1/workloads", in, &raw); err != nil {
		if refused(err) {
			return x.ended(fmt.Sprintf("the controller refuses the onboarding: %s\n%s", refusal(err), x.undoOnboarding(grant, created, value, turned)))
		}
		// A lost answer says nothing of what the controller did: untagged, an onboarded workload could not be woken.
		stays, again := "The tag bindings stay.", "a tag bound already is not an error"
		switch {
		case grant != nil && len(targets) > 0:
			stays, again = stays+" The Kubernetes access stays.", "a tag bound or an access there already is not an error"
		case grant != nil:
			stays, again = "The Kubernetes access stays.", "an access there already is not an error"
		}
		if turned != nil {
			stays += fmt.Sprintf(" The request logs of backend service %s stay on (before: %s).", in.BackendService, logs.Before)
		}
		giveUp := ""
		if grant != nil {
			giveUp = "\nNot there and given up, remove the access with: " + x.revokeCommand(grant.plan.entry())
		}
		return x.ended(fmt.Sprintf("The controller's answer to the onboarding is lost (%s): the workload may be onboarded. %s\nCheck with: %s\nNot there, run the same command again: %s.%s", refusal(err), stays, x.showCommand(in.ID), again, giveUp))
	}
	err = x.print(raw, func(w workload) {
		x.say("Onboarded workload %s: %s", w.ID, w.State)
		x.details(w)
		for _, t := range targets {
			x.say("  tagged: %s", t.label)
		}
		if turned != nil {
			x.say("  request logs: on, sample rate 1.0 (before: %s)", logs.Before)
		}
	})
	x.warnCount(logs, unread)
	return err
}

// warnCount says, after the onboarding's output, that the idle check counts every request of a backend service that does
// not log each one, without --request-logs; a backend service that was not read (unread) is said instead. Under --json, on the
// error stream, as warnStandard.
func (x *run) warnCount(logs requestLogs, unread error) {
	if x.r.RequestLogs || (unread == nil && logs.Before.everyRequest()) {
		return
	}
	w := x.d.Out
	if x.r.JSON {
		w = x.d.Err
	}
	if unread != nil {
		fmt.Fprintf(w, "  ! the request logs of backend service %s are not read: %s\n", logs.BackendService, refusal(unread))
		return
	}
	fmt.Fprintf(w, "  %s\n", countWarning)
}

// covers stops an onboarding whose dry run does not answer what the flags ask: a router below 0.35.0 ignores those flags, and
// would onboard something else.
func (x *run) covers(dry dryRun) error {
	if asked := x.r.LoadBalancer; asked != "" && (dry.LB == nil || dry.LB.Kind != asked) {
		return x.ignoredFlag("--load-balancer " + asked)
	}
	for _, name := range x.r.CloudSQL {
		if !slices.ContainsFunc(dry.Databases, func(d databaseRef) bool { return d.Name == name }) {
			return x.ignoredFlag("--cloud-sql " + name)
		}
	}
	if x.r.GKECluster != "" {
		if dry.GKE == nil {
			return x.ignoredFlag("--gke-cluster " + x.r.GKECluster)
		}
		// They name what the operator's token reads and writes in the cluster.
		if !namespacePattern.MatchString(dry.GKE.Namespace) || !namespacePattern.MatchString(dry.GKE.Service) {
			return &stop{1, fmt.Sprintf("The dry run of router %s names namespace %q and Service %q for cluster %s, which are not Kubernetes names.\nNothing was changed.", x.n.router, dry.GKE.Namespace, dry.GKE.Service, x.r.GKECluster)}
		}
	}
	return nil
}

// ignoredFlag is the end of a run whose router did not read a flag.
func (x *run) ignoredFlag(flag string) error {
	return &stop{1, fmt.Sprintf("The dry run of router %s does not answer %s: it is older than %s, which reads it. Install a newer router first: %s.\nNothing was changed.",
		x.n.router, flag, minRouterVersion, x.installCommand("--project "+x.n.project+" --region "+x.n.region, "<the router's image, "+minRouterVersion+" or later>"))}
}

// refused tells that the controller's call was answered with a refusal (4xx): nothing was done. A 5xx or no answer at all
// says nothing of what was.
func refused(err error) bool {
	var e *gcp.Error
	return errors.As(err, &e) && e.Code >= 400 && e.Code < 500
}

// tagBind binds the tag value to a VM or a group, as the operator, in the resource's location. A binding there already
// (409 ALREADY_EXISTS) is not this run's: made says whether this call created it. Another 409 (ABORTED) bound nothing.
func (x *run) tagBind(t target, value string) (made bool, err error) {
	var op longOperation
	err = x.call(http.MethodPost, t.base()+"tagBindings", map[string]string{"parent": t.parent(x.number), "tagValue": value}, &op)
	if gcp.IsAlreadyExists(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, x.wait(t.base(), op, tagTimeout)
}

// undoOnboarding removes what an onboarding refused or interrupted before its call added, the request logs it turned on (nil:
// none), its Kubernetes access and its tag bindings, and says what it did and what to do. It finishes whatever the interrupt,
// up to undoTimeout: the onboarding is not there, so what it added serves nothing. Once interrupted, a second interrupt is the
// shell's and stops the process.
func (x *run) undoOnboarding(grant *rbacGrant, created []target, value string, logs *requestLogs) string {
	next := "To go on, fix the refusal, then run the same command again."
	if x.ctx.Err() != nil {
		next = "To go on, run the same command again."
	}
	asked := x.ctx
	ctx, cancel := context.WithTimeout(context.WithoutCancel(asked), undoTimeout)
	x.ctx = ctx
	defer func() {
		cancel()
		x.ctx = asked
	}()
	msg := x.undoLogs(logs)
	if grant != nil {
		msg = append(msg, x.undoGrant(*grant)...)
	}
	msg = append(msg, x.undoBindings(created, value)...)
	return strings.Join(append(msg, next), "\n")
}

// ended is the stop of a run past its first change: its words say what is left, an interrupt or not.
func (x *run) ended(msg string) error {
	if x.ctx.Err() != nil {
		return &stop{exitInterrupted, msg}
	}
	return &stop{1, msg}
}

// why is why a call failed, as a line.
func (x *run) why(err error) string {
	if x.ctx.Err() != nil {
		return "interrupted"
	}
	return refusal(err)
}

// undoBindings removes the bindings a refused onboarding made, and says what it did.
func (x *run) undoBindings(created []target, value string) []string {
	var msg, removed []string
	for _, t := range created {
		if _, err := x.unbind(t, value); err != nil {
			msg = append(msg, fmt.Sprintf("The tag binding of %s is not removed: %s", t.label, refusal(err)))
		} else {
			removed = append(removed, t.label)
		}
	}
	if len(removed) > 0 {
		msg = append(msg, "The tag bindings this run made are removed: "+strings.Join(removed, ", ")+".")
	}
	return msg
}

func (x *run) offboard() error {
	if err := x.require("offboard"); err != nil {
		return err
	}
	if _, err := x.workloadPath(); err != nil {
		return err
	}
	id := x.r.ID
	p, there, err := x.offboardee(id)
	if err != nil {
		return x.controllerFailure(id, nil, err, false)
	}
	// What the workload holds says which features' permissions it asks, and the request logs' when its onboarding turned them
	// on: nothing has changed yet.
	x.r.RequestLogs = p.RequestLogs != nil
	if features := x.offboardFeatures(id, p.Backends); len(features) > 0 || x.r.RequestLogs {
		if err := x.require("offboard", features...); err != nil {
			return err
		}
	}
	var forced forcedOff
	if there {
		if forced, err = x.deleteAtController(id, p, x.r.Force); err != nil {
			if x.ctx.Err() != nil {
				return x.ended(fmt.Sprintf("Interrupted during the controller's delete: workload %s may be offboarded. Its tags and its Kubernetes access stay: run the same command again to finish.", id))
			}
			return x.controllerFailure(id, p.Backends, err, true)
		}
	}
	out, err := x.afterController(id, p, x.r.Force)
	if err != nil {
		return err
	}
	left := slices.Concat(out.tagsLeft, out.accessLeft, out.logsLeft)
	if x.r.JSON {
		answer := map[string]any{"id": id, "offboarded": true, "untagged": out.untagged, "left": leftDown(forced.Left)}
		for key, value := range map[string][]string{"removed": out.removed, "restored": out.restored, "kept": out.kept, "notRemoved": left, "notGivenBack": forced.NotGivenBack} {
			if len(value) > 0 {
				answer[key] = value
			}
		}
		b, _ := json.Marshal(answer)
		fmt.Fprintf(x.d.Out, "%s\n", b)
	} else {
		x.say("Offboarded workload %s", id)
		if len(forced.Left) > 0 {
			x.say("  left down: %s", strings.Join(leftDown(forced.Left), ", "))
		}
		for _, l := range forced.NotGivenBack {
			x.say("  ! %s", l)
		}
		for _, u := range out.untagged {
			x.say("  untagged: %s", u)
		}
		for _, r := range out.removed {
			x.say("  removed: %s", r)
		}
		for _, r := range out.restored {
			x.say("  restored: %s", r)
		}
		for _, k := range out.kept {
			x.say("  %s", k)
		}
		for _, l := range left {
			x.say("  ! %s", l)
		}
	}
	if len(left) > 0 {
		return x.ended("Run the same command again to finish what is not removed.")
	}
	if len(forced.NotGivenBack) > 0 {
		return x.ended(fmt.Sprintf("What the router could not give back is yours to change: workload %s is offboarded, and no rerun reaches it.", id))
	}
	return nil
}

// leftDown names the backends an offboarding left down.
func leftDown(left []backend) []string {
	names := []string{}
	for _, b := range left {
		names = append(names, b.String())
	}
	return names
}

// forcedOff is what the controller answers of a forced offboarding: the backends it left down, and in its own words what it could
// not give back. A router below this version answers no such field: none.
type forcedOff struct {
	Left         []backend `json:"left"`
	NotGivenBack []string  `json:"notGivenBack"`
}

// deleteWorkload offboards a workload at the controller, which wakes it first. One that cannot wake is refused
// (wake-failed); with force it is offboarded anyway, its backends left down, and what it left is returned.
func (x *run) deleteWorkload(path string, force bool) (forcedOff, error) {
	err := x.controller(http.MethodDelete, path, nil, nil)
	var e *gcp.Error
	if !errors.As(err, &e) || e.Reason != "wake-failed" {
		if workloadNotThere(err) {
			return forcedOff{}, nil
		}
		return forcedOff{}, err
	}
	if !force {
		return forcedOff{}, &stop{1, fmt.Sprintf("%s\nOffboard it anyway with --force: its backends are then left down.", e.Message)}
	}
	var res forcedOff
	if err := x.controller(http.MethodDelete, path+"?force=true", nil, &res); err != nil && !workloadNotThere(err) {
		return forcedOff{}, err
	}
	return res, nil
}

// pendingPath is where the backends of a workload being offboarded wait until their tag bindings are removed ("" without
// a cache directory).
func (x *run) pendingPath(id string) string {
	if x.d.Cache == "" {
		return ""
	}
	return filepath.Join(x.d.Cache, "dormeto", "gcp", x.n.project+"-"+x.n.region+"-offboard-"+id+".json")
}

// pendingOffboarding is what an offboarding has to go through after the controller's delete, kept in the cache for a rerun:
// the workload's backends, and what its onboarding turned on of the request logs (nil: nothing). An older CLI's file is the
// list of backends alone.
type pendingOffboarding struct {
	Backends    []backend    `json:"backends"`
	RequestLogs *requestLogs `json:"requestLogs,omitempty"`
}

// pending reads what is kept for a workload: false when there is nothing.
func (x *run) pending(id string) (pendingOffboarding, bool) {
	path := x.pendingPath(id)
	if path == "" {
		return pendingOffboarding{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return pendingOffboarding{}, false
	}
	var p pendingOffboarding
	if json.Unmarshal(data, &p) == nil {
		return p, true
	}
	if json.Unmarshal(data, &p.Backends) != nil {
		return pendingOffboarding{}, false
	}
	return p, true
}

// keepPending writes the backends and the request logs before the controller's workload goes: the tag must stay until then
// (a wake needs it), and once the workload is gone nothing else names the bindings to remove nor the logConfig to set back.
func (x *run) keepPending(id string, p pendingOffboarding) error {
	path := x.pendingPath(id)
	if path == "" {
		return nil
	}
	data, err := json.Marshal(p)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			err = os.WriteFile(path, data, 0o600)
		}
	}
	return err
}

// offboarding is what an offboarding did past the controller, and what it left. With --force, what it cannot remove does not
// stop the rest: it is named here.
type offboarding struct {
	untagged   []string // what lost its tag
	removed    []string // the Kubernetes objects and subjects removed
	kept       []string // what stays in a cluster and is no failure: someone else's, or held by another subject
	restored   []string // the request logs set back
	tagsLeft   []string // the tags not removed
	accessLeft []string // the Kubernetes access not removed
	logsLeft   []string // the request logs not set back
}

// offboardWorkload offboards a workload at the controller (which wakes it and restores every reference), then removes the tag
// bindings of its VMs, groups and databases and its Kubernetes access, sets its request logs back, and says which. Its backends
// and request logs are kept in the cache before the controller deletes the workload: a rerun that finds the workload gone
// finishes from them. A workload the controller does not know, with nothing kept and no access in the record, is an error that
// gcp.IsNotFound tells.
func (x *run) offboardWorkload(id string, force bool) (forced forcedOff, out offboarding, err error) {
	p, there, err := x.offboardee(id)
	if err != nil {
		return forcedOff{}, out, err
	}
	if there {
		if forced, err = x.deleteAtController(id, p, force); err != nil {
			return forcedOff{}, out, err
		}
	}
	out, err = x.afterController(id, p, force)
	return forced, out, err
}

// offboardee reads what an offboarding has to go through: the workload's backends and request logs, and whether the controller
// still holds it. Only the controller's own answer that it holds no such workload lets what the cache or the record keeps be
// finished: anything else says nothing of a workload that may still need its tags and its access to wake.
func (x *run) offboardee(id string) (p pendingOffboarding, there bool, err error) {
	var w workload
	gotten := x.controller(http.MethodGet, "/v1/workloads/"+esc(id), nil, &w)
	switch {
	case gotten == nil:
		return pendingOffboarding{Backends: w.Backends, RequestLogs: x.requestLogsOf(w)}, true, nil
	case !workloadNotThere(gotten):
		return p, false, gotten
	}
	if p, ok := x.pending(id); ok {
		return p, false, nil
	}
	// Nothing kept in the cache: the record alone may name its access. One the operator cannot read names nothing.
	if _, err := x.rbacRecord(); err == nil && len(x.recordedAccess(id)) > 0 {
		return p, false, nil
	}
	return p, false, gotten
}

// offboardFeatures are the features an offboarding of these backends needs.
func (x *run) offboardFeatures(id string, backends []backend) []string {
	var features []string
	if slices.ContainsFunc(backends, func(b backend) bool { return b.Kind == kindDatabase }) {
		features = append(features, "cloud-sql")
	}
	if slices.ContainsFunc(backends, func(b backend) bool { return b.Kind == kindPods }) || (x.rec != nil && len(x.recordedAccess(id)) > 0) {
		features = append(features, "gke")
	}
	return features
}

// deleteAtController keeps the backends and the request logs for a rerun, then deletes the workload at the controller. It
// returns what it left, with force.
func (x *run) deleteAtController(id string, p pendingOffboarding, force bool) (forcedOff, error) {
	if err := x.keepPending(id, p); err != nil {
		return forcedOff{}, fmt.Errorf("the backends of workload %s are not kept for a rerun: %w", id, err)
	}
	forced, err := x.deleteWorkload("/v1/workloads/"+esc(id), force)
	// Refused, nothing was done, and the workload is there: the file would read as an offboarding that stopped.
	var s *stop
	if p := x.pendingPath(id); p != "" && (refused(err) || errors.As(err, &s)) {
		os.Remove(p)
	}
	return forced, err
}

// afterController removes what the controller's workload no longer needs: its tags (a wake needs them, so they went on until
// now), then its Kubernetes access, then sets its request logs back. What it kept for a rerun goes once nothing is left: the
// record may not name an access left.
func (x *run) afterController(id string, p pendingOffboarding, force bool) (offboarding, error) {
	var out offboarding
	var err error
	if out.untagged, out.tagsLeft, err = x.untag(id, p.Backends, force); err != nil {
		return out, err
	}
	if err := x.removeAccess(id, p.Backends, force, &out); err != nil {
		return out, err
	}
	if err := x.logsAfterController(id, p.RequestLogs, force, &out); err != nil {
		return out, err
	}
	if path := x.pendingPath(id); path != "" && len(out.tagsLeft) == 0 && len(out.accessLeft) == 0 && len(out.logsLeft) == 0 {
		os.Remove(path)
	}
	return out, nil
}

// workloadNotThere tells that the controller itself answers that it holds no such workload.
func workloadNotThere(err error) bool { return gcp.IsNotFound(err) && !controllerGone(err) }

// controllerGone tells that a controller's call says nothing of the workload: no answer, a 5xx, or a 404 that is not the
// controller's own.
func controllerGone(err error) bool {
	var e *gcp.Error
	if !errors.As(err, &e) {
		return true
	}
	return e.Code >= 500 || (e.Code == http.StatusNotFound && e.Reason != "not-found")
}

// controllerFailure is the end of an offboarding the controller did not answer. With force it names what is left: nothing is
// removed, as the workload may still be running, and a tag or an access gone would keep it from waking. backends are what is
// known of it (nil: the cache's). deleting says the call was the delete: with no answer, it may have gone through.
func (x *run) controllerFailure(id string, backends []backend, err error, deleting bool) error {
	if !x.r.Force || !controllerGone(err) || x.ctx.Err() != nil {
		return err
	}
	var logs *requestLogs
	if backends == nil {
		p, _ := x.pending(id)
		backends, logs = p.Backends, p.RequestLogs
	}
	left := []string{fmt.Sprintf("the workload at the router: its configuration, its waker backend service dmtr-%s-%s and its routes in the URL map", x.n.region, id)}
	tagged := 0
	for _, b := range backends {
		if b.Kind != kindPods {
			left = append(left, "the tag dormeto=managed of "+b.String())
			tagged++
		}
	}
	if tagged == 0 {
		left = append(left, "the tag dormeto=managed of any VM, group or database of it: the controller alone names them")
	}
	if logs != nil {
		left = append(left, fmt.Sprintf("the request logs of backend service %s, on since its onboarding (before: %s)", logs.BackendService, logs.Before))
	}
	if _, readErr := x.rbacRecord(); readErr != nil {
		left = append(left, "any Kubernetes access of it: the install record is not read ("+refusal(readErr)+")")
	} else {
		for _, e := range x.accessOf(id, backends) {
			left = append(left, x.rbacLeftLine(e))
		}
	}
	flags := "--project " + x.n.project + " --region " + x.n.region
	if x.r.Configuration != "" {
		flags += " --gcloud-configuration " + x.r.Configuration
	}
	state := "is not offboarded"
	if deleting && !refused(err) {
		state = "may not be offboarded"
	}
	return &stop{1, fmt.Sprintf("The controller %s does not answer (%s): workload %s %s, and nothing was removed, as it may still be running.\nLeft:\n  - %s\nOnce it answers, run the same command again; installing the router again brings it back: %s",
		x.n.controller, refusal(err), id, state, strings.Join(left, "\n  - "), x.installCommand(flags, "<a good image>"))}
}

// untag removes the tag bindings of the backends of a workload that is offboarded: none for pods, which carry no tag, and then
// the tag is not read. With force, those it cannot remove are named in left, and the others go on.
func (x *run) untag(id string, backends []backend, force bool) (untagged, left []string, err error) {
	tagged := slices.DeleteFunc(slices.Clone(backends), func(b backend) bool { return b.Kind == kindPods })
	if len(tagged) == 0 {
		return nil, nil, nil
	}
	value, found, err := x.tag("tagValues", x.n.project+"/dormeto/managed")
	switch {
	case err != nil && force:
		for _, b := range tagged {
			left = append(left, fmt.Sprintf("the tag of %s is not removed: %s", b, x.why(err)))
		}
		return nil, left, nil
	case err != nil:
		return nil, nil, x.ended(fmt.Sprintf("workload %s is offboarded, but its tags are not removed: %s\n(run the same command again to finish)", id, x.why(err)))
	case !found:
		return nil, nil, nil
	}
	for _, b := range tagged {
		removed, err := x.unbindBackend(b, value)
		if err != nil {
			if force {
				left = append(left, fmt.Sprintf("the tag of %s is not removed: %s", b, x.why(err)))
				continue
			}
			return untagged, nil, x.ended(fmt.Sprintf("workload %s is offboarded, but the tag of %s is not removed: %s\n(run the same command again to finish)", id, b, x.why(err)))
		}
		if removed {
			untagged = append(untagged, b.String())
		}
	}
	return untagged, left, nil
}

// unbindBackend removes the tag of a backend; false when it held none.
func (x *run) unbindBackend(b backend, value string) (bool, error) {
	if b.Kind != kindDatabase {
		return x.unbind(b.target(), value)
	}
	t, found, err := x.databaseTarget(b.Name)
	if err != nil || !found {
		return false, err
	}
	return x.unbind(t, value)
}

// target is a VM or a group as a tag binding names it: a database's is databaseTarget, as only Cloud SQL says its region, and
// pods carry no tag.
func (b backend) target() target {
	path := "zones/" + b.Location + "/instances/"
	switch {
	case b.Kind == "vm":
	case regionPattern.MatchString(b.Location):
		path = "regions/" + b.Location + "/instanceGroupManagers/"
	default:
		path = "zones/" + b.Location + "/instanceGroupManagers/"
	}
	return target{label: b.String(), location: b.Location, path: path + b.ID}
}

// pendingOffboards lists the workloads of this router whose offboarding stopped after the controller deleted them (their
// backends wait in the cache).
func (x *run) pendingOffboards() []string {
	if x.d.Cache == "" {
		return nil
	}
	prefix := x.n.project + "-" + x.n.region + "-offboard-"
	files, _ := filepath.Glob(filepath.Join(x.d.Cache, "dormeto", "gcp", prefix+"*.json"))
	var ids []string
	for _, f := range files {
		ids = append(ids, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), prefix), ".json"))
	}
	return ids
}

// planPendingOffboards finishes them, but the ones the controller still lists (the offboard step does those).
func (x *run) planPendingOffboards(listed []workloadRef) *step {
	var lines []string
	for _, id := range x.pendingOffboards() {
		if !slices.ContainsFunc(listed, func(w workloadRef) bool { return w.ID == id }) {
			lines = append(lines, "- remove the tag bindings of workload "+id+" (its offboarding stopped)")
			lines = append(lines, x.accessLines(id)...)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return &step{name: "offboard", lines: lines, apply: func() (string, error) {
		n := 0
		for _, id := range x.pendingOffboards() {
			p, _ := x.pending(id)
			out, err := x.afterController(id, p, true)
			if err == nil {
				err = x.endOffboarding(id, out)
			}
			if err != nil {
				return "", err
			}
			n++
		}
		return fmt.Sprintf("finished the offboarding of %d workloads", n), nil
	}}
}

// unbind removes the binding of the tag value from a VM or a group; false when there was none. Resource Manager answers
// 403 for a resource that does not exist (the permission being held, as the checks proved; see tag): a VM deleted by hand
// is warned of, on the error stream (the answer may be JSON), and the run goes on. For a target this run has just read, that
// reason is false: the 403 is an error.
func (x *run) unbind(t target, value string) (bool, error) {
	var list struct {
		TagBindings []struct{ Name, TagValue string } `json:"tagBindings"`
	}
	if err := x.call(http.MethodGet, t.base()+"tagBindings?parent="+url.QueryEscape(t.parent(x.number)), nil, &list); err != nil {
		if gcp.IsNotFound(err) {
			return false, nil
		}
		if gcp.IsDenied(err) && !serviceDisabled(err) && !t.read {
			fmt.Fprintf(x.d.Err, "  ! the tag bindings of %s are not read (%s): it may be gone, going on\n", t.label, refusal(err))
			return false, nil
		}
		return false, err
	}
	removed := false
	for _, b := range list.TagBindings {
		if b.TagValue != value {
			continue
		}
		var op longOperation
		err := x.call(http.MethodDelete, t.base()+b.Name, nil, &op)
		if gcp.IsNotFound(err) {
			continue
		}
		if err != nil {
			return removed, err
		}
		if err := x.wait(t.base(), op, tagTimeout); err != nil {
			return removed, err
		}
		removed = true
	}
	return removed, nil
}
