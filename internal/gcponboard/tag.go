package gcponboard

// The tag step: the secure tag dormeto=managed of the project, on which the router's dormetoRouterBackends grant is
// conditioned: the key dormeto, then its value managed, each created by an operation whose response names it
// (tagKeys/<n>, tagValues/<n>); planning reads them by their namespaced names (<project ID>/dormeto,
// <project ID>/dormeto/managed). The workloads' onboarding binds the value to
// their VMs and groups as the operator; the router itself holds no tag permission.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Dormeto/cli/internal/gcp"
)

const (
	crmAPI     = "cloudresourcemanager.googleapis.com"
	crmV3      = "https://cloudresourcemanager.googleapis.com/v3/"
	tagTimeout = 2 * time.Minute

	tagKeyDescription   = "Dormeto: resources a Dormeto router may start, stop, suspend and resume"
	tagValueDescription = "Managed by Dormeto"
)

type googleTag struct {
	Name string `json:"name"`
}

// tag reads a tag key or value (kind tagKeys or tagValues) by its namespaced name. Resource Manager answers 403 for one
// that does not exist (the permission to read it being held, as the checks proved): it is missing then. Its API on but
// not answering yet (SERVICE_DISABLED, also a 403) is waited for.
func (x *run) tag(kind, namespaced string) (string, bool, error) {
	var t googleTag
	found := false
	err := x.settle(func() error {
		err := x.d.Google.Do(x.ctx, http.MethodGet, crmV3+kind+"/namespaced?name="+url.QueryEscape(namespaced), nil, &t)
		switch {
		case err == nil:
			found = true
			return nil
		case x.off(err, crmAPI):
			return nil
		case serviceDisabled(err):
			return err
		case gcp.IsNotFound(err), gcp.IsDenied(err):
			return nil
		}
		return err
	})
	return t.Name, found, err
}

func (x *run) planTag() (*step, error) {
	key, found, err := x.tag("tagKeys", x.n.project+"/dormeto")
	if err != nil {
		return nil, err
	}
	if !found {
		return &step{name: "tag", lines: []string{"+ tag dormeto/managed"}, apply: func() (string, error) {
			if err := x.createTag("tagKeys", "projects/"+x.number, "dormeto", tagKeyDescription); err != nil {
				return "", err
			}
			if err := x.createTag("tagValues", x.tagKey, "managed", tagValueDescription); err != nil {
				return "", err
			}
			return "created dormeto/managed", nil
		}}, nil
	}
	x.tagKey = key
	x.foundTag("tagKey", x.n.project+"/dormeto", key)
	value, found, err := x.tag("tagValues", x.n.project+"/dormeto/managed")
	if err != nil {
		return nil, err
	}
	if found {
		x.foundTag("tagValue", x.n.project+"/dormeto/managed", value)
		return nil, nil
	}
	return &step{name: "tag", lines: []string{"+ tag value dormeto/managed"}, apply: func() (string, error) {
		if err := x.createTag("tagValues", x.tagKey, "managed", tagValueDescription); err != nil {
			return "", err
		}
		return "created the value dormeto/managed", nil
	}}, nil
}

// foundTag notes, while planning, a tag that is already there: its ID replaces the namespaced name a stopped run recorded.
func (x *run) foundTag(kind, namespaced, name string) {
	if x.rec.nameResource(kind, namespaced, name) {
		x.recChanged = true
	}
	x.found(kind, name)
}

// createTag creates a tag key (under the project) or value (under the key), waits for it and records its ID, taken
// from the operation's response (a read by namespaced name right after the creation could miss it). Google gives the ID
// only then: until then the record lists the tag by its namespaced name, for an uninstall after a stop.
func (x *run) createTag(kind, parent, short, description string) error {
	resource, namespaced := "tagKey", x.n.project+"/"+short
	if kind == "tagValues" {
		resource, namespaced = "tagValue", x.n.project+"/dormeto/"+short
	}
	if err := x.recordFirst(resource, namespaced); err != nil {
		return err
	}
	var op longOperation
	in := map[string]any{"parent": parent, "shortName": short, "description": description}
	if err := x.call(http.MethodPost, crmV3+kind, in, &op); err != nil {
		return err
	}
	if !op.Done || op.Error != nil {
		if err := x.wait(crmV3, op, tagTimeout); err != nil {
			return err
		}
		// Done now: read once more for its response.
		if err := x.call(http.MethodGet, crmV3+op.Name, nil, &op); err != nil {
			return err
		}
	}
	var t googleTag
	if len(op.Response) > 0 {
		if err := json.Unmarshal(op.Response, &t); err != nil {
			return fmt.Errorf("the creation of %s answered %s: %w", short, op.Response, err)
		}
	}
	if t.Name == "" {
		return fmt.Errorf("the creation of %s answered no name (operation %s)", short, op.Name)
	}
	if kind == "tagKeys" {
		x.tagKey = t.Name
	}
	x.rec.nameResource(resource, namespaced, t.Name)
	return nil
}
