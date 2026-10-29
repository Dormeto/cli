package gcponboard

// The install record: the document install/record of the router's database, its field data holding the record as JSON.
// It lists the resources of the router's access (and the APIs the install turned on), its grants, the grants Google
// added during the install (with the step after which they appeared), and the project's bindings before the first
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
)

const recordPath = "install/record"

// Record is the install record. Its rule: Grants and Resources are Dormeto's that are present after the install, added
// by it or found by it (a run adds what it finds of the router's, so a record rebuilt after a loss is whole again);
// Google lists the grants Google added during an install, never removed; Before is the project's bindings before the
// first install. The uninstall removes only the grants that are not in Before. Image and RouterVersion are the router
// part's (empty for the access alone); times are RFC 3339 UTC; By is the gcloud account that ran the install.
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
}

// Resource is one resource of the router's access. Kinds: api (an API the install turned on: never turned off), role
// (projects/<id>/roles/<role>), serviceAccount (projects/<id>/serviceAccounts/<email>), tagKey (tagKeys/<n>), tagValue
// (tagValues/<n>), database (projects/<id>/databases/<db>), queue (projects/<id>/locations/<region>/queues/<queue>).
type Resource struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// RecordGrant is one of the router's grants; On is project, queue or serviceAccount (the router's own ones).
type RecordGrant struct {
	Role      string     `json:"role"`
	Member    string     `json:"member"`
	On        string     `json:"on"`
	Condition *Condition `json:"condition,omitempty"`
}

// GoogleGrant is a grant Google added on its own during the install, after Step.
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

func (r *Record) addGoogle(g GoogleGrant) {
	for _, have := range r.Google {
		if have.Role == g.Role && have.Member == g.Member {
			return
		}
	}
	r.Google = append(r.Google, g)
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
			x.rec, x.recorded = &rec, true
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
			x.rec = &rec
			return nil
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	x.rec = &Record{Before: bindingsOf(x.policy)}
	return nil
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
		if err := x.writeRecord(); err != nil {
			return "", err
		}
		return "wrote " + where, nil
	}}
}
