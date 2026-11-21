package gcponboard

// Google Cloud's standalone workloads (vision standalone workloads design §2, §5): `dormeto gcp workloads onboard --resource
// <path>`, a VM, a managed group or a Cloud SQL instance by its path, in place of a URL map and a backend service. The router's
// dry run checks the resource; the CLI checks, with the operator's own credentials, that no backend service uses it (a VM or a
// group behind one is an HTTP workload's: Dormeto would stop it when its metrics are idle), then binds the tag dormeto=managed
// to it and its companions, and onboards it.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/output"
)

// The router's resource paths (router/gcp/src/resources.mjs): a VM, a managed group (zonal or regional), a Cloud SQL instance.
var (
	vmPattern  = regexp.MustCompile(`^projects/([a-z][-a-z0-9.:]{0,62})/zones/([a-z]+-[a-z]+[0-9]+-[a-z])/instances/([a-z]([-a-z0-9]{0,61}[a-z0-9])?)$`)
	migPattern = regexp.MustCompile(`^projects/([a-z][-a-z0-9.:]{0,62})/(?:zones|regions)/([a-z]+-[a-z]+[0-9]+(?:-[a-z])?)/instanceGroupManagers/([a-z]([-a-z0-9]{0,61}[a-z0-9])?)$`)
	sqlPattern = regexp.MustCompile(`^projects/([a-z][-a-z0-9.:]{0,62})/instances/([a-z]([-a-z0-9]{0,96}[a-z0-9])?)$`)
	// A backend's group, as a backend service names it: an instance group or a zonal network endpoint group of this project.
	backendGroup = regexp.MustCompile(`^https://(?:www|compute)\.googleapis\.com/compute/v1/projects/([^/]+)/(zones|regions)/([a-z0-9-]+)/(instanceGroups|networkEndpointGroups)/([a-z]([-a-z0-9]{0,61}[a-z0-9])?)$`)
)

// resourcePaths is what --resource takes, as the router says it.
const resourcePaths = "the path of a Compute Engine VM (projects/<project>/zones/<zone>/instances/<name>), a managed instance group " +
	"(projects/<project>/zones|regions/<location>/instanceGroupManagers/<name>) or a Cloud SQL instance (projects/<project>/instances/<name>)"

// resourceOf is the resource a path names, as a backend (no ID: the dry run answers it); false for a path that is none.
func resourceOf(path string) (backend, bool) {
	if m := vmPattern.FindStringSubmatch(path); m != nil {
		return backend{Kind: "vm", Location: m[2], Name: m[3]}, true
	}
	if m := migPattern.FindStringSubmatch(path); m != nil {
		return backend{Kind: "mig", Location: m[2], Name: m[3]}, true
	}
	if m := sqlPattern.FindStringSubmatch(path); m != nil {
		return backend{Kind: kindDatabase, Name: m[2]}, true
	}
	return backend{}, false
}

// googleLabel names a VM or a group in Google's words: VM build-agent (europe-west1-b), managed group batch (europe-west1).
func googleLabel(b backend) string {
	what := "VM"
	if b.Kind == "mig" {
		what = "managed group"
	}
	return fmt.Sprintf("%s %s (%s)", what, b.Name, b.Location)
}

// oneForm checks that onboard is given one form: --resource, or --url-map and --backend-service; each form's own flags refused
// in the other. Exit 2, before any call.
func (x *run) oneForm() error {
	r := x.r
	switch {
	case r.Resource != "" && (r.URLMap != "" || r.BackendService != ""):
		return &stop{2, "--resource: give it or --url-map and --backend-service, not both"}
	case r.Resource == "" && r.URLMap == "" && r.BackendService == "":
		return &stop{2, "give --resource <path> (a standalone workload) or --url-map and --backend-service (an HTTP workload)"}
	case r.Resource == "" && len(r.Idle) > 0:
		return &stop{2, "--idle-cpu, --idle-network and --idle-metric: a standalone workload's (give --resource)"}
	case r.Resource == "" && r.Unchecked:
		return &stop{2, "--unchecked: a standalone workload's (give --resource)"}
	case r.Resource == "":
		return nil
	case r.LoadBalancer != "":
		return &stop{2, "--load-balancer: an HTTP workload's: a standalone workload has no load balancer"}
	case r.RequestLogs:
		return &stop{2, "--request-logs: an HTTP workload's: a standalone workload has no backend service"}
	case r.GKECluster != "":
		return &stop{2, "--gke-cluster: an HTTP workload's: a standalone workload is a VM, a managed group or a Cloud SQL instance"}
	}
	if _, ok := resourceOf(r.Resource); !ok {
		return &stop{2, fmt.Sprintf("--resource: %q is not %s", r.Resource, resourcePaths)}
	}
	return nil
}

// resourceIn is the onboarding by resource in the controller's request (router/gcp/src/onboarding.mjs requestedResource).
type resourceIn struct {
	Resource              string            `json:"resource"`
	ID                    string            `json:"id"`
	Name                  string            `json:"name,omitempty"`
	Tags                  map[string]string `json:"tags,omitempty"`
	Settings              map[string]any    `json:"settings,omitempty"`
	CloudSQL              []string          `json:"cloudSql,omitempty"`
	FallbackInstanceTypes []string          `json:"fallbackInstanceTypes,omitempty"`
}

// resourceDryRun is the controller's answer to a dry run by resource.
type resourceDryRun struct {
	Eligible  bool          `json:"eligible"`
	Reasons   []string      `json:"reasons"`
	Backend   *backend      `json:"backend"`
	Databases []databaseRef `json:"databases"`
	Warnings  []string      `json:"warnings"`
}

// resourceInput validates the flags of the onboarding by resource, before any call: exit 2 for a bad one. The router checks the
// settings' values, its refusal said as it comes.
func (x *run) resourceInput() (resourceIn, backend, error) {
	r := x.r
	if err := x.oneForm(); err != nil {
		return resourceIn{}, backend{}, err
	}
	own, _ := resourceOf(r.Resource)
	in := resourceIn{Resource: r.Resource, Name: r.Name, Tags: r.Tags, CloudSQL: r.CloudSQL, Settings: map[string]any{}}
	if err := CheckFallbackTypes(r.FallbackTypes); err != nil {
		return in, own, &stop{2, err.Error()}
	}
	in.FallbackInstanceTypes = r.FallbackTypes
	if err := checkDatabaseNames(r.CloudSQL); err != nil {
		return in, own, err
	}
	if v := r.WakeMode; v != "" {
		in.Settings["wakeMode"] = v // the router refuses it for a standalone workload, in its words
	}
	if v := r.WakeTimeout; v != nil {
		if *v < MinWakeTimeout || *v > MaxWakeTimeout {
			return in, own, &stop{2, fmt.Sprintf("--wake-timeout-seconds: %d is not from %d to %d", *v, MinWakeTimeout, MaxWakeTimeout)}
		}
		in.Settings["wakeTimeoutSeconds"] = *v
	}
	if v := r.IdleMinutes; v != nil {
		if *v < minIdleMinutes || *v > maxIdleMinutes {
			return in, own, &stop{2, fmt.Sprintf("--idle-minutes: %d is not from %d to %d", *v, minIdleMinutes, maxIdleMinutes)}
		}
		in.Settings["idleMinutes"] = *v
	}
	if r.Hibernate != nil {
		in.Settings["hibernate"] = *r.Hibernate
	}
	for k, v := range r.Idle {
		if v != nil { // inherit: not sent, the levels decide
			in.Settings[k] = v
		}
	}
	if len(in.Settings) == 0 {
		in.Settings = nil
	}
	id := r.ID
	if id == "" && !workloadIDPattern.MatchString(own.Name) {
		if !x.d.Terminal {
			return in, own, &stop{2, fmt.Sprintf("--id: needed, the resource's name %q is not a valid workload ID (%s)", own.Name, workloadIDRule)}
		}
		var err error
		if id, err = x.d.Prompter.Ask(fmt.Sprintf("Workload ID (the resource's name %q is not a valid one: %s)", own.Name, workloadIDRule), ""); err != nil {
			return in, own, &stop{2, "--id: needed: " + err.Error()}
		}
	}
	if id == "" {
		id = own.Name
	}
	if !workloadIDPattern.MatchString(id) {
		return in, own, &stop{2, fmt.Sprintf("--id: %q is not a valid workload ID: %s", id, workloadIDRule)}
	}
	in.ID = id
	return in, own, nil
}

// onboardResource is the onboarding by resource: the router's dry run, the backend-service check, the tag bound to the resource
// and its companions as the operator, then the onboarding; a refusal after the tags removes the ones it made.
func (x *run) onboardResource() error {
	in, own, err := x.resourceInput()
	if err != nil {
		return err
	}
	features := x.onboardFeatures()
	if own.Kind == kindDatabase && !containsString(features, "cloud-sql") {
		features = append(features, "cloud-sql")
	}
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
	var dry resourceDryRun
	if err := json.Unmarshal(raw, &dry); err != nil {
		return fmt.Errorf("the controller's dry run is not understood: %w", err)
	}
	if !dry.Eligible {
		return &stop{1, fmt.Sprintf("Resource %s is not eligible:\n  - %s\nNothing was changed.", in.Resource, strings.Join(dry.Reasons, "\n  - "))}
	}
	// An older router reads no resource: its dry run names none.
	if dry.Backend == nil || dry.Backend.Kind != own.Kind || dry.Backend.Name != own.Name || (own.Kind != kindDatabase && (dry.Backend.Location != own.Location || dry.Backend.ID == "")) {
		return x.ignoredFlag("--resource " + in.Resource)
	}
	for _, name := range x.r.CloudSQL {
		if !containsDatabase(dry.Databases, name) {
			return x.ignoredFlag("--database " + name)
		}
	}
	if err := x.readsNameAndTags(); err != nil {
		return err
	}
	if own.Kind != kindDatabase {
		if err := x.notServing(*dry.Backend); err != nil {
			return err
		}
	}
	x.warnReplicas(dryRun{Databases: dry.Databases})
	labels := []string{dry.Backend.String()}
	for _, name := range x.r.CloudSQL {
		labels = append(labels, backend{Kind: kindDatabase, Name: name}.String())
	}
	if x.r.DryRun {
		if x.r.JSON {
			return output.JSON(x.d.Out, map[string]any{"dryRun": true, "targets": []string{x.target(in.ID)}, "eligibility": raw})
		}
		x.say("Resource %s is eligible as standalone workload %s; onboarding would bind the tag dormeto=managed to:", in.Resource, in.ID)
		for _, label := range labels {
			x.say("  %s", label)
		}
		return nil
	}
	var targets []target
	names := x.r.CloudSQL
	if own.Kind == kindDatabase {
		names = append([]string{own.Name}, names...)
	} else {
		targets = append(targets, dry.Backend.target())
	}
	for _, name := range names {
		t, found, err := x.databaseTarget(name)
		if err == nil && !found {
			err = fmt.Errorf("it is not found")
		}
		if err != nil {
			return &stop{1, fmt.Sprintf("Cloud SQL instance %s is not read, and its region says where its tag goes: %s\nNothing was changed.", name, refusal(err))}
		}
		targets = append(targets, t)
	}
	value, found, err := x.tag("tagValues", x.n.project+"/dormeto/managed")
	if err != nil {
		return err
	}
	if !found {
		return &stop{1, fmt.Sprintf("The tag dormeto/managed does not exist in project %s: run dormeto gcp routers install first.\nNothing was changed.", x.n.project)}
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
			return x.ended(what + "\n" + x.undoOnboarding(nil, created, value, nil))
		}
	}
	if x.ctx.Err() != nil {
		return x.ended(notSent + "\n" + x.undoOnboarding(nil, created, value, nil))
	}
	if err := x.controller(http.MethodPost, "/v1/workloads", in, &raw); err != nil {
		if refused(err) {
			return x.ended(fmt.Sprintf("the controller refuses the onboarding: %s\n%s", refusal(err), x.undoOnboarding(nil, created, value, nil)))
		}
		return x.ended(fmt.Sprintf("The controller's answer to the onboarding is lost (%s): the workload may be onboarded. The tag bindings stay.\nCheck with: %s\nNot there, run the same command again: a tag bound already is not an error.", refusal(err), x.showCommand(in.ID)))
	}
	w, err := decodeWorkload(raw)
	if err != nil {
		return err
	}
	var b strings.Builder
	x.writeTo(&b, func() {
		x.say("%s: onboarded (%s)", x.target(w.ID), w.State)
		x.details(w)
		for _, t := range targets {
			x.say("  tagged: %s", t.label)
		}
	})
	return x.answeredWorkload(w.ID, raw, strings.TrimSuffix(b.String(), "\n"))
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsDatabase(list []databaseRef, name string) bool {
	for _, d := range list {
		if d.Name == name {
			return true
		}
	}
	return false
}

// notServing is the load-balancer check (the plan's ruling, as wide as the manager's), made as the operator: a VM or a group
// that serves a load balancer is refused by name. A backend service of any scheme, global or regional, that uses it through an
// instance group (managed or not) or a zonal network endpoint group holding it; for a group, its target pools; for a VM, a
// target pool listing it or a target instance naming it. A listing refused or answered partial (unreachables) is not checked:
// the onboarding stops, naming the listings not made, unless --unchecked, which says them and what it risks and goes on.
func (x *run) notServing(b backend) error {
	label := googleLabel(b)
	c := &lbCheck{x: x, b: b, base: "https://compute.googleapis.com/compute/v1/projects/" + esc(x.n.project) + "/", members: map[string][]string{}}
	use, err := c.use()
	switch {
	case err != nil:
		return err
	case use != "":
		return &stop{1, fmt.Sprintf("%s%s %s\nNothing was changed.", strings.ToUpper(label[:1]), label[1:], use)}
	case len(c.missing) > 0 && x.r.Unchecked:
		fmt.Fprintf(x.textOut(), "  ! Not checked (--unchecked): %s: if %s serves a load balancer, Dormeto stops it when its metrics are idle.\n", strings.Join(c.missing, "; "), label)
	case len(c.missing) > 0:
		return &stop{1, fmt.Sprintf("Whether %s serves a load balancer is not checked: these listings were not made:\n  - %s\n"+
			"Give --unchecked to onboard it unchecked: if it serves one, Dormeto stops it when its metrics are idle.\nNothing was changed.",
			label, strings.Join(c.missing, "\n  - "))}
	}
	return nil
}

// The endings of a use found: a backend service's, and a load balancer's that has none.
const (
	httpWorkloads    = ": it belongs to an HTTP workload: onboard its backend service."
	noBackendService = ": it serves a load balancer that uses no backend service, which Dormeto does not onboard."
)

// lbCheck is one run of the load-balancer check: the listings not made (missing) and the groups' VMs read (members).
type lbCheck struct {
	x       *run
	b       backend
	base    string
	missing []string
	members map[string][]string
}

// notMade keeps a listing refused as not made, and passes any other error.
func (c *lbCheck) notMade(what, permissions string, err error) error {
	if gcp.IsDenied(err) && c.x.ctx.Err() == nil {
		c.missing = append(c.missing, fmt.Sprintf("%s (%s: %s)", what, permissions, refusal(err)))
		return nil
	}
	return err
}

// use is what uses the VM or the group, ending with what it is ("" for none).
func (c *lbCheck) use() (string, error) {
	if use, err := c.backendServices(); use != "" || err != nil {
		return use, err
	}
	if c.b.Kind == "mig" {
		return c.groupTargetPools()
	}
	if use, err := c.targetPools(); use != "" || err != nil {
		return use, err
	}
	return c.targetInstances()
}

// aggregated reads an aggregated listing, every page: each scope's entries of field to each. A scope Google could not reach is
// kept as not made.
func (c *lbCheck) aggregated(listing, what, permissions, field string, each func(scope string, entries json.RawMessage) (string, error)) (string, error) {
	token := ""
	for {
		var page struct {
			Items         map[string]map[string]json.RawMessage `json:"items"`
			NextPageToken string                                `json:"nextPageToken"`
			Unreachables  []string                              `json:"unreachables"`
		}
		u := c.base + "aggregated/" + listing
		if token != "" {
			u += "?pageToken=" + url.QueryEscape(token)
		}
		if err := c.x.d.Google.Do(c.x.ctx, http.MethodGet, u, nil, &page); err != nil {
			return "", c.notMade(what, permissions, err)
		}
		if len(page.Unreachables) > 0 {
			c.missing = append(c.missing, fmt.Sprintf("%s (unreachable: %s)", what, strings.Join(page.Unreachables, ", ")))
		}
		for scope, item := range page.Items {
			if entries, ok := item[field]; ok {
				if use, err := each(scope, entries); use != "" || err != nil {
					return use, err
				}
			}
		}
		if token = page.NextPageToken; token == "" {
			return "", nil
		}
	}
}

// isTheVM tells that a VM's URL names the checked VM.
func (c *lbCheck) isTheVM(u string) bool {
	return strings.HasSuffix(u, "/projects/"+c.x.n.project+"/zones/"+c.b.Location+"/instances/"+c.b.Name)
}

func (c *lbCheck) backendServices() (string, error) {
	return c.aggregated("backendServices", "backend services", "compute.backendServices.list, compute.regionBackendServices.list", "backendServices",
		func(scope string, entries json.RawMessage) (string, error) {
			var services []struct {
				Name     string `json:"name"`
				Backends []struct {
					Group string `json:"group"`
				} `json:"backends"`
			}
			if err := json.Unmarshal(entries, &services); err != nil {
				return "", fmt.Errorf("the backend services' list is not understood: %w", err)
			}
			where := strings.TrimPrefix(scope, "regions/")
			for _, s := range services {
				service := fmt.Sprintf("backs backend service %s (%s)", s.Name, where)
				for _, be := range s.Backends {
					through, used, err := c.groupUse(be.Group)
					if err != nil {
						return "", err
					}
					if used {
						return service + through + httpWorkloads, nil
					}
				}
			}
			return "", nil
		})
}

// groupUse tells whether a backend's group holds the VM (through names it) or is the group.
func (c *lbCheck) groupUse(group string) (through string, used bool, err error) {
	m := backendGroup.FindStringSubmatch(group)
	if m == nil || m[1] != c.x.n.project {
		return "", false, nil
	}
	place, location, kind, name := m[2], m[3], m[4], m[5]
	if c.b.Kind == "mig" {
		return "", kind == "instanceGroups" && location == c.b.Location && name == c.b.Name, nil
	}
	// A VM: a zonal group or NEG of its zone, or a regional group of its region.
	region := c.b.Location[:strings.LastIndex(c.b.Location, "-")]
	if !(place == "zones" && location == c.b.Location) && !(place == "regions" && location == region && kind == "instanceGroups") {
		return "", false, nil
	}
	key := place + "/" + location + "/" + kind + "/" + name
	vms, read := c.members[key]
	if !read {
		if vms, err = c.x.groupVMs(c.base+key, kind); err != nil {
			what, permission := fmt.Sprintf("instance group %s (%s/%s)", name, place, location), "compute.instanceGroups.list"
			if kind == "networkEndpointGroups" {
				what, permission = fmt.Sprintf("network endpoint group %s (%s/%s)", name, place, location), "compute.networkEndpointGroups.list"
			}
			return "", false, c.notMade(what, permission, err)
		}
		c.members[key] = vms
	}
	if !containsString(vms, c.b.Name) {
		return "", false, nil
	}
	if kind == "networkEndpointGroups" {
		return ", through network endpoint group " + name, true, nil
	}
	return ", through instance group " + name, true, nil
}

// groupTargetPools: a managed group that feeds target pools serves a target-pool load balancer.
func (c *lbCheck) groupTargetPools() (string, error) {
	place := "zones/"
	if regionPattern.MatchString(c.b.Location) {
		place = "regions/"
	}
	var group struct {
		TargetPools []string `json:"targetPools"`
	}
	if err := c.x.d.Google.Do(c.x.ctx, http.MethodGet, c.base+place+esc(c.b.Location)+"/instanceGroupManagers/"+esc(c.b.Name), nil, &group); err != nil {
		return "", c.notMade("managed group "+c.b.Name, "compute.instanceGroupManagers.get", err)
	}
	if len(group.TargetPools) == 0 {
		return "", nil
	}
	var names []string
	for _, p := range group.TargetPools {
		names = append(names, p[strings.LastIndex(p, "/")+1:])
	}
	return "feeds target pool " + strings.Join(names, ", ") + noBackendService, nil
}

func (c *lbCheck) targetPools() (string, error) {
	return c.aggregated("targetPools", "target pools", "compute.targetPools.list", "targetPools", func(scope string, entries json.RawMessage) (string, error) {
		var pools []struct {
			Name      string   `json:"name"`
			Instances []string `json:"instances"`
		}
		if err := json.Unmarshal(entries, &pools); err != nil {
			return "", fmt.Errorf("the target pools' list is not understood: %w", err)
		}
		for _, p := range pools {
			for _, vm := range p.Instances {
				if c.isTheVM(vm) {
					return fmt.Sprintf("is in target pool %s (%s)%s", p.Name, strings.TrimPrefix(scope, "regions/"), noBackendService), nil
				}
			}
		}
		return "", nil
	})
}

func (c *lbCheck) targetInstances() (string, error) {
	return c.aggregated("targetInstances", "target instances", "compute.targetInstances.list", "targetInstances", func(scope string, entries json.RawMessage) (string, error) {
		var targets []struct {
			Name     string `json:"name"`
			Instance string `json:"instance"`
		}
		if err := json.Unmarshal(entries, &targets); err != nil {
			return "", fmt.Errorf("the target instances' list is not understood: %w", err)
		}
		for _, t := range targets {
			if c.isTheVM(t.Instance) {
				return fmt.Sprintf("is the VM of target instance %s (%s)%s", t.Name, strings.TrimPrefix(scope, "zones/"), noBackendService), nil
			}
		}
		return "", nil
	})
}

// groupVMs are the names of the VMs of an instance group (listInstances) or of a zonal network endpoint group
// (listNetworkEndpoints), every page.
func (x *run) groupVMs(groupURL, kind string) ([]string, error) {
	var out []string
	token := ""
	for {
		var page struct {
			Items []struct {
				Instance        string `json:"instance"`
				NetworkEndpoint struct {
					Instance string `json:"instance"`
				} `json:"networkEndpoint"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		verb, body := "/listInstances", map[string]any{"instanceState": "ALL"}
		if kind == "networkEndpointGroups" {
			verb, body = "/listNetworkEndpoints", map[string]any{}
		}
		u := groupURL + verb
		if token != "" {
			u += "?pageToken=" + url.QueryEscape(token)
		}
		if err := x.d.Google.Do(x.ctx, http.MethodPost, u, body, &page); err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			name := it.Instance
			if name == "" {
				name = it.NetworkEndpoint.Instance
			}
			if i := strings.LastIndex(name, "/"); i >= 0 {
				name = name[i+1:]
			}
			out = append(out, name)
		}
		if token = page.NextPageToken; token == "" {
			return out, nil
		}
	}
}
