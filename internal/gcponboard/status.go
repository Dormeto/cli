package gcponboard

// `dormeto gcp status`: a project's routers, one for --region, else one for each database named dmtr-<region>: what the
// install record says, and what Google and the controller answer now. It reads; it never writes. Exit 0 when every
// router is as recorded; 1 when one is not, there is none, or Google refuses; 2 for a bad region.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Dormeto/cli/internal/gcp"
)

// StatusReport is a project's routers.
type StatusReport struct {
	Project ProjectInfo    `json:"project"`
	Routers []RouterStatus `json:"routers"`
}

// RouterStatus is one router: Record is nil without an install record; Version is what the controller answers; OK that
// every resource and grant is as it should be.
type RouterStatus struct {
	Router    string           `json:"router"`
	Region    string           `json:"region"`
	OK        bool             `json:"ok"`
	Record    *Record          `json:"record"`
	Version   string           `json:"version,omitempty"`
	Resources []ResourceStatus `json:"resources"`
	Grants    []GrantStatus    `json:"grants"`
}

// ResourceStatus is a resource's live state. Kinds: service, health (the controller's), neg, queue, job, database, record
// (an install record that cannot be read).
type ResourceStatus struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// GrantStatus is a recorded grant and whether its resource's policy holds it now; Error why that policy was not read.
type GrantStatus struct {
	RecordGrant
	Present bool   `json:"present"`
	Error   string `json:"error,omitempty"`
}

// Status reads the report of the project's routers.
func Status(ctx context.Context, d Deps, r Request) (StatusReport, error) {
	account, err := d.Google.Tokens.Account(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	var p projectBody
	if err := d.Google.Do(ctx, http.MethodGet, crm+url.PathEscape(r.Project), nil, &p); err != nil {
		if denied(err) {
			return StatusReport{}, fmt.Errorf("Project %s is not visible to %s: no role in it", r.Project, account)
		}
		return StatusReport{}, err
	}
	rep := StatusReport{Project: ProjectInfo{ID: r.Project, Number: strings.TrimPrefix(p.Name, "projects/"), Parent: p.Parent}, Routers: []RouterStatus{}}
	regions := []string{r.Region}
	if r.Region == "" {
		if regions, err = routerRegions(ctx, d, r.Project); err != nil {
			return StatusReport{}, err
		}
	}
	for _, region := range regions {
		x := newRun(ctx, d, Request{Project: r.Project, Region: region})
		x.number = rep.Project.Number
		rep.Routers = append(rep.Routers, x.routerStatus())
	}
	return rep, nil
}

// routerRegions are the regions of the project's databases named dmtr-<region>, sorted.
func routerRegions(ctx context.Context, d Deps, project string) ([]string, error) {
	var out struct {
		Databases []struct {
			Name string `json:"name"`
		} `json:"databases"`
	}
	err := d.Google.Do(ctx, http.MethodGet, firestoreV1+"projects/"+esc(project)+"/databases", nil, &out)
	if serviceDisabled(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var regions []string
	for _, db := range out.Databases {
		id := db.Name[strings.LastIndex(db.Name, "/")+1:]
		if region, ok := strings.CutPrefix(id, "dmtr-"); ok && regionPattern.MatchString(region) {
			regions = append(regions, region)
		}
	}
	slices.Sort(regions)
	return regions, nil
}

// look reads a resource for the status: found, or the state to say.
func (x *run) look(u string, out any) (found bool, state string) {
	err := x.d.Google.Do(x.ctx, http.MethodGet, u, nil, out)
	switch {
	case err == nil:
		return true, ""
	case gcp.IsNotFound(err):
		return false, stateMissing
	case serviceDisabled(err):
		return false, stateMissing + " (its API is off)"
	}
	return false, "unreadable: " + err.Error()
}

const stateMissing = "missing"

func (x *run) routerStatus() RouterStatus {
	st := RouterStatus{Router: x.n.router, Region: x.n.region, Resources: []ResourceStatus{}, Grants: []GrantStatus{}}
	add := func(kind, name string, ok bool, state, detail string) {
		st.Resources = append(st.Resources, ResourceStatus{Kind: kind, Name: name, OK: ok, State: state, Detail: detail})
	}
	var db struct {
		Type       string `json:"type"`
		LocationID string `json:"locationId"`
	}
	hasDatabase, databaseState := x.look(x.databaseURL(), &db)
	if hasDatabase {
		var doc recordDocument
		switch found, state := x.look(x.recordURL(), &doc); {
		case found:
			var rec Record
			if err := json.Unmarshal([]byte(doc.Fields.Data.StringValue), &rec); err != nil {
				add("record", recordPath, false, "unreadable: "+err.Error(), "")
			} else {
				st.Record = &rec
			}
		case state != stateMissing:
			add("record", recordPath, false, state, "")
		}
	}

	// The router part: expected once an image is recorded; else said only when something of it is there.
	expected := st.Record != nil && st.Record.Image != ""
	absent := func(kind, name, state string) {
		if expected || strings.HasPrefix(state, "unreadable") {
			add(kind, name, false, state, "")
		}
	}
	var controllerFound bool
	for _, s := range []serviceSettings{x.controllerSettings(), x.wakerSettings()} {
		var got runService
		found, state := x.look(x.servicesURL()+"/"+esc(s.name), &got)
		if !found {
			absent("service", s.name, state)
			continue
		}
		state, ok := "ready", true
		if why := got.notReady(); why != "" {
			state, ok = why, false
		}
		detail := "image " + got.image()
		if s.role == "controller" {
			controllerFound = true
			detail += ", " + x.controllerURL()
		}
		if expected && got.image() != st.Record.Image {
			detail += ": not the recorded image"
			ok = false
		}
		add("service", s.name, ok, state, detail)
	}
	if controllerFound {
		version, err := x.health()
		switch {
		case err != nil:
			add("health", x.controllerURL(), false, "unreadable: "+err.Error(), "")
		case expected && version != st.Record.RouterVersion:
			st.Version = version
			add("health", x.controllerURL(), false, "version "+version+", not the recorded "+st.Record.RouterVersion, "")
		default:
			st.Version = version
			add("health", x.controllerURL(), true, "version "+version, "")
		}
	}
	var neg googleNEG
	if found, state := x.look(x.negsURL()+"/"+esc(x.n.neg), &neg); found {
		add("neg", x.n.neg, neg.CloudRun.Service == x.n.waker, strings.ToLower(neg.NetworkEndpointType), "Cloud Run service "+neg.CloudRun.Service)
	} else {
		absent("neg", x.n.neg, state)
	}

	var q googleQueue
	if found, state := x.look(x.queuesURL()+"/"+esc(x.n.queue), &q); found {
		add("queue", x.n.queue, q.State == "RUNNING" && queueAsWanted(q), q.State, "")
	} else {
		add("queue", x.n.queue, false, state, "")
	}
	var job googleJob
	if found, state := x.look(x.jobsURL()+"/"+esc(x.n.job), &job); found {
		add("job", x.n.job, job.State == "ENABLED" && x.jobAsWanted(job), job.State, job.Schedule+" "+job.TimeZone)
	} else {
		absent("job", x.n.job, state)
	}
	if hasDatabase {
		add("database", x.n.database, db.Type == "FIRESTORE_NATIVE", db.Type, db.LocationID)
	} else {
		add("database", x.n.database, false, databaseState, "")
	}

	if st.Record != nil {
		st.Grants = x.grantStatuses(st.Record.Grants)
	}
	st.OK = st.Record != nil
	for _, res := range st.Resources {
		st.OK = st.OK && res.OK
	}
	for _, g := range st.Grants {
		st.OK = st.OK && g.Present
	}
	return st
}

// grantStatuses read each policy the grants are on, once.
func (x *run) grantStatuses(grants []RecordGrant) []GrantStatus {
	places := map[string]ResourcePolicy{
		"project":        ProjectPolicy(x.n.project),
		"queue":          QueuePolicy(x.n.project, x.n.region, x.n.queue),
		"serviceAccount": ServiceAccountPolicy(x.n.project, x.n.email),
		"controller":     RunPolicy(x.n.project, x.n.region, x.n.controller),
	}
	policies, failures := map[string]*Policy{}, map[string]string{}
	out := []GrantStatus{}
	for _, g := range grants {
		if policies[g.On] == nil && failures[g.On] == "" {
			if p, err := read(x.ctx, x.d.Google, places[g.On]); err != nil {
				failures[g.On] = err.Error()
			} else {
				policies[g.On] = p
			}
		}
		out = append(out, GrantStatus{RecordGrant: g, Error: failures[g.On],
			Present: policies[g.On] != nil && holds(policies[g.On], Grant{Role: g.Role, Member: g.Member, Condition: g.Condition})})
	}
	return out
}

var statusKinds = map[string]string{"neg": "network endpoint group"}

func checkMark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

// PrintStatus prints the report, one line each.
func PrintStatus(w io.Writer, rep StatusReport) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Project %s (%s)\n", rep.Project.ID, rep.Project.Number)
	if len(rep.Routers) == 0 {
		fmt.Fprintf(&b, "No Dormeto router in project %s.\n", rep.Project.ID)
	}
	for _, r := range rep.Routers {
		x := &run{n: newNames(rep.Project.ID, r.Region)}
		fmt.Fprintf(&b, "Router %s, region %s: ", r.Router, r.Region)
		switch {
		case r.Record == nil:
			b.WriteString("not installed (no install record)\n")
		case r.Record.Image == "":
			b.WriteString("the access only, no image\n")
		default:
			fmt.Fprintf(&b, "version %s, image %s\n", r.Record.RouterVersion, r.Record.Image)
		}
		if r.Record != nil {
			fmt.Fprintf(&b, "  installed %s, updated %s by %s with the CLI %s\n", r.Record.InstalledAt, r.Record.UpdatedAt, r.Record.By, r.Record.CLI)
			if len(r.Record.Features) > 0 {
				fmt.Fprintf(&b, "  features %s\n", strings.Join(r.Record.Features, ", "))
			}
		}
		for _, res := range r.Resources {
			kind := res.Kind
			if words, ok := statusKinds[kind]; ok {
				kind = words
			}
			fmt.Fprintf(&b, "  %s %s %s: %s", checkMark(res.OK), kind, res.Name, res.State)
			if res.Detail != "" {
				fmt.Fprintf(&b, ", %s", res.Detail)
			}
			b.WriteString("\n")
		}
		for _, g := range r.Grants {
			fmt.Fprintf(&b, "  %s grant %s", checkMark(g.Present), x.describe(Grant{Role: g.Role, Member: g.Member, Condition: g.Condition}, g.On))
			if g.Error != "" {
				fmt.Fprintf(&b, ": %s", g.Error)
			}
			b.WriteString("\n")
		}
		if r.Record != nil {
			for _, g := range r.Record.Google {
				fmt.Fprintf(&b, "  Google added: %s to %s (after %s)\n", g.Role, g.Member, g.Step)
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// RunStatus is `dormeto gcp status`: the report printed, and the exit code.
func RunStatus(ctx context.Context, d Deps, r Request) int {
	if r.Region != "" {
		x := newRun(ctx, d, r)
		if err := x.validate(); err != nil {
			return x.end(err)
		}
	}
	rep, err := Status(ctx, d, r)
	if err == nil {
		if r.JSON {
			var body []byte
			if body, err = json.Marshal(rep); err == nil {
				_, err = fmt.Fprintf(d.Out, "%s\n", body)
			}
		} else {
			err = PrintStatus(d.Out, rep)
		}
	}
	if err != nil {
		fmt.Fprintln(d.Err, err)
		return 1
	}
	code := 0
	if len(rep.Routers) == 0 {
		code = 1
	}
	for _, router := range rep.Routers {
		if !router.OK {
			code = 1
		}
	}
	return code
}
