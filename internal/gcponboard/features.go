package gcponboard

// The optional features (`--with cloud-sql,gke`): each adds its APIs, custom roles and the router's grants to the base
// ones, through the same steps. The install record holds the choice; a run with fewer features than the record keeps the
// record's (only the uninstall removes a feature).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"

	"github.com/Dormeto/cli/internal/gcp"
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

// useFeatures sets the run's features: the request's and the record's. It follows loadRecord. The APIs on are read when
// the record holds a feature --with does not name, or APIs an earlier run asked for and was stopped before seeing them on
// (they are recorded as this install's, whatever feature asked), and those of the record's features that are off are
// turned on again. A feature joins the record only once its APIs are on (the apis step, or none to turn on): one Google
// refuses must not block the runs after it.
func (x *run) useFeatures() error {
	x.features = featureSet(x.rec.Features, x.r.With)
	x.recordedFeatures = slices.Clone(x.rec.Features)
	var recorded []string
	for _, name := range x.rec.Features {
		if !slices.Contains(x.r.With, name) {
			recorded = append(recorded, name)
		}
	}
	if (len(recorded) > 0 || len(x.rec.Enabling) > 0) && x.number != "" {
		on, err := enabledServices(x.ctx, x.d.Google, x.number)
		if err != nil {
			return err
		}
		for _, api := range everyAPI() {
			if slices.Contains(on, api) && !slices.Contains(x.enabled, api) {
				x.enabled = append(x.enabled, api)
			}
		}
		for _, name := range recorded {
			for _, api := range access.Load().Features[name].APIs {
				if !slices.Contains(on, api) && !slices.Contains(x.turningOn, api) {
					x.turningOn = append(x.turningOn, api)
				}
			}
		}
	}
	for _, name := range featureSet(x.r.With) {
		if slices.Contains(x.rec.Features, name) {
			continue
		}
		if slices.ContainsFunc(access.Load().Features[name].APIs, func(api string) bool { return slices.Contains(x.turningOn, api) }) {
			x.joining = append(x.joining, name)
			continue
		}
		x.rec.Features = featureSet(x.rec.Features, []string{name})
		x.recChanged = true
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

// recordedFeatures are the features of the router's install record in the database: none when the region has no record
// (no database, or its API off); any other failure to read it is an error, as a feature not tested must not pass for
// tested.
func recordedFeatures(x *run) ([]string, error) {
	var doc recordDocument
	// Not through get: an API that is off is no reason to wait for it, a report only tests.
	switch err := x.d.Google.Do(x.ctx, http.MethodGet, x.recordURL(), nil, &doc); {
	case err == nil:
	case gcp.IsNotFound(err), serviceDisabled(err):
		return nil, nil
	default:
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal([]byte(doc.Fields.Data.StringValue), &rec); err != nil {
		return nil, fmt.Errorf("%s in database %s: %w", recordPath, x.n.database, err)
	}
	return rec.Features, nil
}
