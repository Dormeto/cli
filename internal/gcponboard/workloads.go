package gcponboard

// `dormeto gcp workloads list|show|onboard|offboard|nap|wake` (spec §5): the router's controller called at its Cloud Run
// URL (https://dmtr-<region>-controller-<project number>.<region>.run.app) with the operator's identity token, audience
// that URL. Onboarding asks the controller for a dry run first (the eligibility, the VMs and groups it would manage), binds
// the tag dormeto=managed to each of them as the operator (the router holds no tag permission, else it could tag any VM
// and then stop it), then onboards; offboarding removes the controller's workload, then the tag bindings. Exit codes: 0;
// 1 when a permission is missing, the workload is not eligible or Google or the controller refuses; 2 for a bad flag or
// a missing answer; 130 when interrupted.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/Dormeto/cli/internal/gcp"
)

const (
	// The settings' ranges are the router's: AWS's onboarding (router/lambda/src/lib/onboarding.mjs).
	minIdleMinutes, maxIdleMinutes = 5, 1440
	minWaitSeconds, maxWaitSeconds = 5, 120

	// maxBackendServiceName is what Compute Engine allows of a backend service's name; the workload's waker backend service
	// is dmtr-<region>-<workload>.
	maxBackendServiceName = 63
)

var workloadIDPattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

const workloadIDRule = "lowercase letters, digits and dashes, starting with a letter and ending with a letter or a digit, at most 40 characters"

// backend is a VM or a managed group of a workload, as the controller names it.
type backend struct {
	Kind     string `json:"kind"` // vm or mig
	Location string `json:"location"`
	Name     string `json:"name"`
}

func (b backend) String() string {
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
}

// dryRun is the controller's answer to an onboarding dry run.
type dryRun struct {
	Eligible bool       `json:"eligible"`
	Reasons  []string   `json:"reasons"`
	VMs      []vmRef    `json:"vms"`
	Groups   []groupRef `json:"groups"`
}

type vmRef struct {
	Zone, Name, ID string
}

type groupRef struct {
	Kind, Location, Name, ID string
}

// target is a VM or a group that carries the tag: its place and its numeric ID, which a tag binding's parent names.
type target struct {
	label    string // "vm web-1 (europe-west1-b)"
	location string // the zone or region: the tag binding endpoint's prefix
	path     string // zones/<zone>/instances/<id>, zones/<zone>/instanceGroupManagers/<id> or regions/<region>/instanceGroupManagers/<id>
}

func (t target) parent(number string) string {
	return "//compute.googleapis.com/projects/" + number + "/" + t.path
}

func (t target) base() string {
	return "https://" + t.location + "-cloudresourcemanager.googleapis.com/v3/"
}

func (d dryRun) targets() []target {
	var out []target
	for _, v := range d.VMs {
		out = append(out, target{backend{"vm", v.Zone, v.Name}.String(), v.Zone, "zones/" + v.Zone + "/instances/" + v.ID})
	}
	for _, g := range d.Groups {
		place := "zones/"
		if g.Kind == "regional" {
			place = "regions/"
		}
		out = append(out, target{backend{"mig", g.Location, g.Name}.String(), g.Location, place + g.Location + "/instanceGroupManagers/" + g.ID})
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

// workloadsEnd is the exit code of a workloads run: its stop's, 130 when interrupted, else 1 with the error. Unlike the
// install's, it never says nothing was changed: an onboarding that failed after the tags says what it did.
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
	fmt.Fprintln(x.d.Err, err)
	return 1
}

// require validates the project and region and asks the permissions of one command, the missing ones named and nothing
// done.
func (x *run) require(command string) error {
	if err := x.validate(); err != nil {
		return err
	}
	rep, err := check(x.ctx, x.d, x.r, []string{command})
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

// controllerError is the controller's {"error", "reason"} as the error's message.
func controllerError(err error) error {
	var e *gcp.Error
	if !errors.As(err, &e) {
		return err
	}
	var body struct{ Error, Reason string }
	if json.Unmarshal([]byte(e.Message), &body) != nil || body.Error == "" {
		return err
	}
	return &gcp.Error{Code: e.Code, Status: e.Status, Reason: body.Reason, Message: body.Error}
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
	return controllerError(x.d.Google.Invoke(x.ctx, method, u+path, u, in, out))
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
		x.say("%-30s %-8s %s", w.ID, w.State, w.BackendService)
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
		if len(w.Failure) > 0 && string(w.Failure) != "null" {
			x.say("  failure: %s", w.Failure)
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
	return x.print(raw, func(w workload) { x.say("Workload %s: %s", w.ID, w.State) })
}

// onboardingIn is what the controller takes to onboard a workload, and to dry run it.
type onboardingIn struct {
	URLMap         string         `json:"urlMap"`
	BackendService string         `json:"backendService"`
	ID             string         `json:"id"`
	Settings       map[string]any `json:"settings,omitempty"`
}

// onboardingInput validates the flags, before any call: exit 2 for a bad one.
func (x *run) onboardingInput() (onboardingIn, error) {
	r := x.r
	switch {
	case r.URLMap == "":
		return onboardingIn{}, &stop{2, "--url-map: needed"}
	case r.BackendService == "":
		return onboardingIn{}, &stop{2, "--backend-service: needed"}
	}
	in := onboardingIn{URLMap: r.URLMap, BackendService: r.BackendService, Settings: map[string]any{}}
	if v := r.IdleMinutes; v != nil {
		if *v < minIdleMinutes || *v > maxIdleMinutes {
			return in, &stop{2, fmt.Sprintf("--idle-minutes: %d is not from %d to %d", *v, minIdleMinutes, maxIdleMinutes)}
		}
		in.Settings["idleMinutes"] = *v
	}
	if v := r.WaitSeconds; v != nil {
		if *v < minWaitSeconds || *v > maxWaitSeconds {
			return in, &stop{2, fmt.Sprintf("--wait-seconds: %d is not from %d to %d", *v, minWaitSeconds, maxWaitSeconds)}
		}
		in.Settings["waitSeconds"] = *v
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
	if err := x.require("onboard"); err != nil {
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
	if !dry.Eligible {
		return &stop{1, fmt.Sprintf("Backend service %s is not eligible:\n  - %s\nNothing was changed.", in.BackendService, strings.Join(dry.Reasons, "\n  - "))}
	}
	targets := dry.targets()
	if x.r.DryRun {
		if x.r.JSON {
			fmt.Fprintf(x.d.Out, "%s\n", raw)
			return nil
		}
		x.say("Backend service %s is eligible as workload %s; onboarding would bind the tag dormeto=managed to:", in.BackendService, in.ID)
		for _, t := range targets {
			x.say("  %s", t.label)
		}
		return nil
	}
	value, found, err := x.tag("tagValues", x.n.project+"/dormeto/managed")
	if err != nil {
		return err
	}
	if !found {
		return &stop{1, fmt.Sprintf("The tag dormeto/managed does not exist in project %s: run dormeto gcp install first.\nNothing was changed.", x.n.project)}
	}
	for _, t := range targets {
		if err := x.tagBind(t, value); err != nil {
			return fmt.Errorf("the tag is not bound to %s: %s\n(the bindings made stay: run the same command again, or offboard)", t.label, refusal(err))
		}
	}
	if err := x.controller(http.MethodPost, "/v1/workloads", in, &raw); err != nil {
		return fmt.Errorf("the controller refuses the onboarding: %s\n(the tag bindings made stay: run the same command again, or offboard)", refusal(err))
	}
	return x.print(raw, func(w workload) {
		x.say("Onboarded workload %s: %s", w.ID, w.State)
		x.details(w)
		for _, t := range targets {
			x.say("  tagged: %s", t.label)
		}
	})
}

// tagBind binds the tag value to a VM or a group, as the operator, in the resource's location; a binding there already is
// fine.
func (x *run) tagBind(t target, value string) error {
	var op longOperation
	err := x.call(http.MethodPost, t.base()+"tagBindings", map[string]string{"parent": t.parent(x.number), "tagValue": value}, &op)
	if gcp.IsConflict(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return x.wait(t.base(), op, tagTimeout)
}

func (x *run) offboard() error {
	if err := x.require("offboard"); err != nil {
		return err
	}
	if _, err := x.workloadPath(); err != nil {
		return err
	}
	untagged, err := x.offboardWorkload(x.r.ID)
	if err != nil {
		return err
	}
	if x.r.JSON {
		b, _ := json.Marshal(map[string]any{"id": x.r.ID, "offboarded": true, "untagged": untagged})
		fmt.Fprintf(x.d.Out, "%s\n", b)
		return nil
	}
	x.say("Offboarded workload %s", x.r.ID)
	for _, u := range untagged {
		x.say("  untagged: %s", u)
	}
	return nil
}

// offboardWorkload offboards a workload at the controller (which wakes it and restores every reference), then removes
// the tag bindings of its VMs and groups, and says which. A workload the controller does not know is an error that
// gcp.IsNotFound tells.
func (x *run) offboardWorkload(id string) ([]string, error) {
	path := "/v1/workloads/" + esc(id)
	var w workload
	if err := x.controller(http.MethodGet, path, nil, &w); err != nil {
		return nil, err
	}
	if err := x.controller(http.MethodDelete, path, nil, nil); err != nil && !gcp.IsNotFound(err) {
		return nil, err
	}
	value, found, err := x.tag("tagValues", x.n.project+"/dormeto/managed")
	if err != nil || !found {
		return nil, err
	}
	var untagged []string
	for _, b := range w.Backends {
		t, ok, err := x.backendTarget(b)
		if err != nil {
			return untagged, fmt.Errorf("workload %s is offboarded, but the tag of %s is not removed: %s", id, b, refusal(err))
		}
		if !ok {
			continue
		}
		removed, err := x.unbind(t, value)
		if err != nil {
			return untagged, fmt.Errorf("workload %s is offboarded, but the tag of %s is not removed: %s", id, b, refusal(err))
		}
		if removed {
			untagged = append(untagged, b.String())
		}
	}
	return untagged, nil
}

// backendTarget reads the ID of a VM or a managed group of a workload (a tag binding's parent names it); false when it
// is gone, and its bindings with it.
func (x *run) backendTarget(b backend) (target, bool, error) {
	var path string
	switch {
	case b.Kind == "vm":
		path = "zones/" + b.Location + "/instances/"
	case regionPattern.MatchString(b.Location):
		path = "regions/" + b.Location + "/instanceGroupManagers/"
	default:
		path = "zones/" + b.Location + "/instanceGroupManagers/"
	}
	var got struct {
		ID string `json:"id"`
	}
	err := x.call(http.MethodGet, "https://compute.googleapis.com/compute/v1/projects/"+esc(x.n.project)+"/"+path+esc(b.Name), nil, &got)
	switch {
	case gcp.IsNotFound(err):
		return target{}, false, nil
	case err != nil:
		return target{}, false, err
	}
	return target{label: b.String(), location: b.Location, path: path + got.ID}, true, nil
}

// unbind removes the binding of the tag value from a VM or a group; false when there was none.
func (x *run) unbind(t target, value string) (bool, error) {
	var list struct {
		TagBindings []struct{ Name, TagValue string } `json:"tagBindings"`
	}
	if err := x.call(http.MethodGet, t.base()+"tagBindings?parent="+url.QueryEscape(t.parent(x.number)), nil, &list); err != nil {
		if gcp.IsNotFound(err) {
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
