package gcponboard

// The optional features (`--with cloud-sql,gke`): each adds its APIs, custom roles and the router's grants to the base
// ones, through the same steps. The install record holds the choice; a run with fewer features than the record keeps the
// record's (only the uninstall removes a feature).

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"

	"github.com/Dormeto/cli/internal/gcp/access"
)

// featureSet is the names, sorted and without duplicates.
func featureSet(names ...[]string) []string {
	var out []string
	for _, l := range names {
		out = append(out, l...)
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// useFeatures sets the run's features: the request's and the record's. It follows loadRecord. A feature the record lacks
// makes the record differ; the APIs of the record's features that --with does not name are asked of Service Usage, as
// the checks asked only for the request's.
func (x *run) useFeatures() error {
	x.features = featureSet(x.rec.Features, x.r.With)
	if !slices.Equal(x.features, x.rec.Features) {
		x.rec.Features = slices.Clone(x.features)
		x.recChanged = true
	}
	var recorded []string
	for _, name := range x.rec.Features {
		if !slices.Contains(x.r.With, name) {
			recorded = append(recorded, name)
		}
	}
	if len(recorded) == 0 || x.number == "" {
		return nil
	}
	on, err := enabledServices(x.ctx, x.d.Google, x.number)
	if err != nil {
		return err
	}
	for _, name := range recorded {
		for _, api := range access.Load().Features[name].APIs {
			if !slices.Contains(on, api) && !slices.Contains(x.turningOn, api) {
				x.turningOn = append(x.turningOn, api)
			}
		}
	}
	return nil
}

// everyAPI are the APIs of the manifest, the features' too.
func everyAPI() []string {
	out := slices.Clone(access.Load().APIs)
	for _, name := range access.FeatureNames() {
		out = append(out, access.Load().Features[name].APIs...)
	}
	return out
}

// apisWith are the manifest's APIs and those of the named features.
func apisWith(features []string) []string {
	out := slices.Clone(access.Load().APIs)
	for _, name := range featureSet(features) {
		out = append(out, access.Load().Features[name].APIs...)
	}
	return out
}

// roleDef is a custom role of the manifest or of one of its features.
func roleDef(id string) access.Role {
	if r, ok := access.Load().Roles[id]; ok {
		return r
	}
	for _, f := range access.Load().Features {
		if r, ok := f.Roles[id]; ok {
			return r
		}
	}
	return access.Role{}
}

// roleIDsWith are the manifest's custom roles and those of the named features, sorted.
func roleIDsWith(features []string) []string {
	var ids []string
	for id := range access.Load().Roles {
		ids = append(ids, id)
	}
	for _, name := range features {
		for id := range access.Load().Features[name].Roles {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// allRoleIDs are the custom roles of the manifest and of every feature: the uninstall removes those the record lists.
func allRoleIDs() []string { return roleIDsWith(access.FeatureNames()) }

// roleIDs are the custom roles of this run: the base ones and those of its features.
func (x *run) roleIDs() []string { return roleIDsWith(x.features) }

// recordedFeatures are the features of the router's install record in the database, none when the region is not given or
// there is no record: what check tests besides --with.
func recordedFeatures(x *run) []string {
	var doc recordDocument
	// Not through get: an API that is off is no reason to wait for it, a report only tests.
	if err := x.d.Google.Do(x.ctx, http.MethodGet, x.recordURL(), nil, &doc); err != nil {
		return nil
	}
	var rec Record
	if json.Unmarshal([]byte(doc.Fields.Data.StringValue), &rec) != nil {
		return nil
	}
	return rec.Features
}
