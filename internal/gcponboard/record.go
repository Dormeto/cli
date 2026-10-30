package gcponboard

// The install record: the document install/record of the router's database, its field data holding the record as JSON.
// It lists the resources of the router's access (and the APIs the install turned on), its grants, the grants Google
// added (with the step after which they appeared), and the project's bindings before the first
// install, never removed. Until the database exists, the record waits in <Cache>/dormeto/gcp/<project>-<region>-
// before.json: a later run starts from that file (its before is the true one of an earlier run that stopped), and the
// file is deleted once the database holds the record.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const recordPath = "install/record"

// Record is the install record. Its rule: Grants and Resources are Dormeto's that are present after the install, added
// by it or found by it (a run adds what it finds of the router's, so a record rebuilt after a loss is whole again);
// Google lists the grants Google added, during an install or seen by a later run, never removed; Before is the project's
// bindings before the first install, for the uninstall only (which removes only the grants that are not in it; an
// install's verification compares with its own run's first read). Image and RouterVersion are the router part's (empty
// for the access alone); times are RFC 3339 UTC; By is the gcloud account that ran the install.
type Record struct {
	CLI            string          `json:"cli"`
	Image          string          `json:"image"`
	RouterVersion  string          `json:"routerVersion"`
	Region         string          `json:"region"`
	ServiceAccount string          `json:"serviceAccount"`
	Resources      []Resource      `json:"resources"`
	Grants         []RecordGrant   `json:"grants"`
	Google         []GoogleGrant   `json:"google"`
	Before         []RecordBinding `json:"before"`
	InstalledAt    string          `json:"installedAt"`
	UpdatedAt      string          `json:"updatedAt"`
	By             string          `json:"by"`
	// Running is the step an install was at when it wrote the record without having finished ("" once one has): the
	// next run shows the project's grants that appeared since and that the record does not explain, and records those of
	// Google's service agents as Google's, after "<Running> (seen later)".
	Running string `json:"running,omitempty"`
	// Enabling are the APIs an apis step asked Google to turn on and did not see on before it stopped: the next run
	// records those it finds on.
	Enabling []string `json:"enabling,omitempty"`
	// Features are the optional features installed (--with), sorted; a run without --with keeps them.
	Features []string `json:"features,omitempty"`
}

// Resource is one resource of the router's access. Kinds: api (an API the install turned on: never turned off), role
// (projects/<id>/roles/<role>), serviceAccount (projects/<id>/serviceAccounts/<email>), tagKey (tagKeys/<n>, or
// <id>/dormeto until its creation has named it), tagValue (tagValues/<n>, or <id>/dormeto/managed), database (projects/<id>/databases/<db>), queue (projects/<id>/locations/<region>/queues/<queue>), and
// of the router part: service (projects/<id>/locations/<region>/services/<service>), neg
// (projects/<id>/regions/<region>/networkEndpointGroups/<neg>), job (projects/<id>/locations/<region>/jobs/<job>).
type Resource struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// RecordGrant is one of the router's grants; On is project, queue, serviceAccount (the router's own ones) or controller.
type RecordGrant struct {
	Role      string     `json:"role"`
	Member    string     `json:"member"`
	On        string     `json:"on"`
	Condition *Condition `json:"condition,omitempty"`
}

// GoogleGrant is a grant Google added on its own during the install, after Step; "<step> (seen later)" when the run
// after a stopped one found it (Google acting after the stop), "install (seen later)" when a run after a finished install
// did.
type GoogleGrant struct {
	Role   string `json:"role"`
	Member string `json:"member"`
	Step   string `json:"step"`
}

// RecordBinding is a binding of the project's policy before the first install.
type RecordBinding struct {
	Role      string     `json:"role"`
	Members   []string   `json:"members"`
	Condition *Condition `json:"condition,omitempty"`
}

func recordGrant(g accessGrant) RecordGrant {
	return RecordGrant{Role: g.Role, Member: g.Member, On: g.On, Condition: g.Condition}
}

// addResource adds a resource the record lacks, and tells whether it did.
func (r *Record) addResource(kind, name string) bool {
	for _, have := range r.Resources {
		if have.Kind == kind && have.Name == name {
			return false
		}
	}
	r.Resources = append(r.Resources, Resource{Kind: kind, Name: name})
	return true
}

// nameResource replaces the provisional name of a resource by its own, if it is listed under it, and tells whether it was.
func (r *Record) nameResource(kind, provisional, name string) bool {
	i := slices.Index(r.Resources, Resource{Kind: kind, Name: provisional})
	if i < 0 {
		return false
	}
	if slices.Contains(r.Resources, Resource{Kind: kind, Name: name}) {
		r.Resources = slices.Delete(r.Resources, i, i+1)
	} else {
		r.Resources[i].Name = name
	}
	return true
}

// recordFirst lists a resource before the call that creates it, and writes the record where it is kept (the cache, until
// the database holds it): whatever happens to that call (a revision that does not start, a timeout, the run killed), the
// uninstall knows the resource, and tolerates its absence.
func (x *run) recordFirst(kind, name string) error {
	if !x.rec.addResource(kind, name) {
		return nil
	}
	if err := x.writeKept(); err != nil {
		return fmt.Errorf("the record is not written before %s is created: %w", name, err)
	}
	return nil
}

func (r *Record) addGrant(g RecordGrant) bool {
	for _, have := range r.Grants {
		if have.On == g.On && grantKey(Grant{Role: have.Role, Member: have.Member, Condition: have.Condition}) ==
			grantKey(Grant{Role: g.Role, Member: g.Member, Condition: g.Condition}) {
			return false
		}
	}
	r.Grants = append(r.Grants, g)
	return true
}

// addGoogle adds a grant of Google's the record lacks, and tells whether it did.
func (r *Record) addGoogle(g GoogleGrant) bool {
	for _, have := range r.Google {
		if have.Role == g.Role && have.Member == g.Member {
			return false
		}
	}
	r.Google = append(r.Google, g)
	return true
}

// beforePolicy is the project's policy before the first install, as far as its grants go.
func (r *Record) beforePolicy() *Policy {
	p := &Policy{}
	for _, b := range r.Before {
		p.Bindings = append(p.Bindings, Binding{Role: b.Role, Members: slices.Clone(b.Members), Condition: b.Condition})
	}
	return p
}

func bindingsOf(p *Policy) []RecordBinding {
	out := []RecordBinding{}
	for _, b := range p.Bindings {
		out = append(out, RecordBinding{Role: b.Role, Members: append([]string(nil), b.Members...), Condition: b.Condition})
	}
	return out
}

func (x *run) recordURL() string { return x.databaseURL() + "/documents/" + recordPath }

func (x *run) cachePath() string {
	if x.d.Cache == "" {
		return ""
	}
	return filepath.Join(x.d.Cache, "dormeto", "gcp", x.n.project+"-"+x.n.region+"-before.json")
}

type recordDocument struct {
	Fields struct {
		Data struct {
			StringValue string `json:"stringValue"`
		} `json:"data"`
	} `json:"fields"`
}

// loadRecord reads the database and its record; without a record, the one waiting in the cache, else a new one whose
// before is the project's policy as just read.
func (x *run) loadRecord() error {
	found, err := x.get(firestoreAPI, x.databaseURL(), nil)
	if err != nil {
		return err
	}
	x.database = found
	if found {
		var doc recordDocument
		found, err := x.get(firestoreAPI, x.recordURL(), &doc)
		if err != nil {
			return err
		}
		if found {
			var rec Record
			if err := json.Unmarshal([]byte(doc.Fields.Data.StringValue), &rec); err != nil {
				return fmt.Errorf("%s in database %s: %w", recordPath, x.n.database, err)
			}
			x.rec, x.recorded, x.resumed = &rec, true, rec.Running != ""
			return nil
		}
	}
	if path := x.cachePath(); path != "" {
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			var rec Record
			if err := json.Unmarshal(b, &rec); err != nil {
				return fmt.Errorf("%s: %w (the record of an earlier install that stopped)", path, err)
			}
			x.rec, x.resumed = &rec, true
			return nil
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	x.rec = &Record{Before: bindingsOf(x.policy)}
	return nil
}

// resume takes up the record of an install that did not finish: the APIs its apis step asked for and that are on now are
// recorded, and it is written again, finished. Then, as on every run with a record, sinceRecord.
func (x *run) resume() {
	if x.resumed {
		x.recChanged = true
		x.recordAPIs(x.enabled)
		x.rec.Enabling = nil // those still off are the apis step's again
	}
	x.sinceRecord()
}

// sinceRecord compares the project's grants, as this run first read them, with the record. Those it does not explain —
// neither in its before, nor the router's, nor Google's as it lists them, nor another Dormeto router's — appeared since:
// Google may have acted after a run stopped (an API's operation finishing) or between two runs (a late grant, a repository
// created by hand). They are shown in the plan; those of Google's service agents are recorded as Google's, after the step
// that install was at "(seen later)", or after "install" when it had finished; the others are only shown, so at every run.
func (x *run) sinceRecord() {
	explained := x.rec.beforePolicy()
	for _, g := range x.rec.Grants {
		if g.On == "project" {
			Add(explained, Grant{Role: g.Role, Member: g.Member, Condition: g.Condition})
		}
	}
	for _, g := range x.rec.Google {
		Add(explained, Grant{Role: g.Role, Member: g.Member})
	}
	step := x.rec.Running
	if step == "" {
		step = "install"
	}
	step += " (seen later)"
	added, _ := Diff(explained, x.start)
	for _, g := range added {
		switch {
		case x.ours(g), x.oursDeleted(g), x.otherRouter(g.Member):
		case x.serviceAgent(g.Member):
			if x.rec.addGoogle(GoogleGrant{Role: g.Role, Member: g.Member, Step: step}) {
				x.recChanged = true
				x.sinceLast = append(x.sinceLast, "+ "+x.describe(g, "project")+" (Google, after "+step+")")
			}
		default:
			x.sinceLast = append(x.sinceLast, "+ "+x.describe(g, "project")+" (not a Google service agent: not recorded)")
		}
	}
}

// oursDeleted tells that a grant is one of the router's, its service account deleted by hand: the member is then
// deleted:serviceAccount:<email>?uid=<n>.
func (x *run) oursDeleted(g Grant) bool {
	return sameMember(g.Member, x.n.member) && x.ours(Grant{Role: g.Role, Member: x.n.member, Condition: g.Condition})
}

// printSinceLast says what sinceRecord found.
func (x *run) printSinceLast() {
	if len(x.sinceLast) == 0 {
		return
	}
	x.say("IAM changes since the last run, not Dormeto's:")
	for _, l := range x.sinceLast {
		x.say("  %s", l)
	}
}

// serviceAgent tells that a member is one of Google's own accounts of the project: a service agent
// (service-<number>@<…>.gserviceaccount.com), the Compute Engine default service account
// (<number>-compute@developer.gserviceaccount.com) or the Google APIs service agent (<number>@cloudservices.…).
func (x *run) serviceAgent(member string) bool {
	email, ok := strings.CutPrefix(member, "serviceAccount:")
	if !ok || x.number == "" {
		return false
	}
	return email == x.number+"-compute@developer.gserviceaccount.com" || email == x.number+"@cloudservices.gserviceaccount.com" ||
		(strings.HasPrefix(email, "service-"+x.number+"@") && strings.HasSuffix(email, ".gserviceaccount.com"))
}

// otherRouter tells that a member is another Dormeto router's service account in the project.
func (x *run) otherRouter(member string) bool {
	email, ok := strings.CutPrefix(member, "serviceAccount:")
	return ok && email != x.n.email && strings.HasPrefix(email, "dmtr-") && strings.HasSuffix(email, "@"+x.n.project+".iam.gserviceaccount.com")
}

// complete fills the record's fields of this run.
func (x *run) complete() {
	r := x.rec
	r.CLI, r.Region, r.ServiceAccount, r.By = x.d.Version, x.n.region, x.n.email, x.account
	r.UpdatedAt = x.now()
	if r.InstalledAt == "" {
		r.InstalledAt = r.UpdatedAt
	}
	if r.Resources == nil {
		r.Resources = []Resource{}
	}
	if r.Grants == nil {
		r.Grants = []RecordGrant{}
	}
	if r.Google == nil {
		r.Google = []GoogleGrant{}
	}
	if r.Before == nil {
		r.Before = []RecordBinding{}
	}
}

// writeRecord writes the record in the database, then deletes the cache's copy.
func (x *run) writeRecord() error {
	x.complete()
	b, err := json.Marshal(x.rec)
	if err != nil {
		return err
	}
	in := map[string]any{"fields": map[string]any{"data": map[string]any{"stringValue": string(b)}}}
	if err := x.call(http.MethodPatch, x.recordURL(), in, nil); err != nil {
		return err
	}
	x.recorded = true
	if path := x.cachePath(); path != "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			x.say("  ! %s is not removed: %s", path, err)
		}
	}
	return nil
}

// writeCache keeps the record in the cache until the database holds it.
func (x *run) writeCache() error {
	path := x.cachePath()
	if path == "" {
		return nil
	}
	x.complete()
	b, err := json.MarshalIndent(x.rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// planRecord plans the record's write: a new record, or the record rewritten when anything else changes or it lacks
// something found.
func (x *run) planRecord() *step {
	sign := "~"
	switch {
	case !x.recorded:
		sign = "+"
	case len(x.steps) == 0 && !x.recChanged:
		return nil
	}
	where := recordPath + " in database " + x.n.database
	return &step{name: "record", lines: []string{sign + " record " + where}, apply: func() (string, error) {
		if err := x.catchUp(); err != nil {
			return "", err
		}
		x.rec.Running = "" // the install's last write: finished
		if err := x.writeRecord(); err != nil {
			return "", err
		}
		return "wrote " + where, nil
	}}
}
