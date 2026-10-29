package gcponboard

// The tag step: the secure tag dormeto=managed of the project, on which the router's dormetoRouterBackends grant is
// conditioned: the key dormeto, then its value managed, each created by an operation and then read by its namespaced
// name (<project ID>/dormeto, <project ID>/dormeto/managed) for its ID. The workloads' onboarding binds the value to
// their VMs and groups as the operator; the router itself holds no tag permission.

import (
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
// that does not exist (the permission to read it being held, as the checks proved): it is missing then.
func (x *run) tag(kind, namespaced string) (string, bool, error) {
	var t googleTag
	err := x.d.Google.Do(x.ctx, http.MethodGet, crmV3+kind+"/namespaced?name="+url.QueryEscape(namespaced), nil, &t)
	switch {
	case err == nil:
		return t.Name, true, nil
	case gcp.IsNotFound(err), gcp.IsDenied(err):
		return "", false, nil
	}
	return "", false, err
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
	x.found("tagKey", key)
	value, found, err := x.tag("tagValues", x.n.project+"/dormeto/managed")
	if err != nil {
		return nil, err
	}
	if found {
		x.found("tagValue", value)
		return nil, nil
	}
	return &step{name: "tag", lines: []string{"+ tag value dormeto/managed"}, apply: func() (string, error) {
		if err := x.createTag("tagValues", x.tagKey, "managed", tagValueDescription); err != nil {
			return "", err
		}
		return "created the value dormeto/managed", nil
	}}, nil
}

// createTag creates a tag key (under the project) or value (under the key), waits for it and records its ID.
func (x *run) createTag(kind, parent, short, description string) error {
	var op longOperation
	in := map[string]any{"parent": parent, "shortName": short, "description": description}
	if err := x.call(http.MethodPost, crmV3+kind, in, &op); err != nil {
		return err
	}
	if err := x.wait(crmV3, op, tagTimeout); err != nil {
		return err
	}
	namespaced := x.n.project + "/dormeto"
	if kind == "tagValues" {
		namespaced += "/managed"
	}
	name, found, err := x.tag(kind, namespaced)
	switch {
	case err != nil:
		return err
	case !found:
		return fmt.Errorf("%s not found after its creation", namespaced)
	}
	if kind == "tagKeys" {
		x.tagKey = name
		x.rec.addResource("tagKey", name)
	} else {
		x.rec.addResource("tagValue", name)
	}
	return nil
}
