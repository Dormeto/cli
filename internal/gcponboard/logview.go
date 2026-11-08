package gcponboard

// The logView step (the G4 design's §5): the router's log view dmtr-<region>-lb on the project's _Default bucket, filtered to
// the load balancers' resource types, through which the router's idle check reads their request logs (router/gcp/src/logs.mjs).
// The router holds roles/logging.viewAccessor on that view alone, by the manifest's conditional grant, which the
// projectBindings step gives after this one: never the project's logs, the applications' among them. Created when missing,
// its filter updated when it is another; the uninstall removes the grant, then the view. The controller reads its name in
// DMT_LOG_VIEW.

import (
	"fmt"
	"net/http"
	"net/url"
)

const (
	loggingAPI = "logging.googleapis.com"
	loggingV2  = "https://logging.googleapis.com/v2/"

	// logViewFilter names the resource types of the load balancers' request logs, as LOG_RESOURCES of the router's logs.mjs:
	// global and classic ones, then regional ones (a test compares them).
	logViewFilter = `resource.type="http_load_balancer" OR resource.type="http_external_regional_lb_rule"`
)

type googleLogView struct {
	Filter string `json:"filter"`
}

// logViewName is the view's resource name, which the router's grant names in its condition.
func (x *run) logViewName() string {
	return "projects/" + x.n.project + "/locations/global/buckets/_Default/views/" + x.n.logView
}

func (x *run) logViewsURL() string {
	return loggingV2 + "projects/" + esc(x.n.project) + "/locations/global/buckets/_Default/views"
}

func (x *run) logViewURL() string { return x.logViewsURL() + "/" + esc(x.n.logView) }

// logViewWords say what the view holds, in the plan and the step's line.
func (x *run) logViewWords() string {
	return x.n.logView + " (bucket _Default, the load balancers' request logs)"
}

// planLogView follows planDatabase: the record is in the database before the view is listed in it.
func (x *run) planLogView() (*step, error) {
	var got googleLogView
	found, err := x.get(loggingAPI, x.logViewURL(), &got)
	if err != nil {
		return nil, err
	}
	switch {
	case !found:
		return &step{name: "logView", lines: []string{"+ log view " + x.logViewWords()}, apply: func() (string, error) {
			if err := x.recordFirst("logView", x.logViewName()); err != nil {
				return "", err
			}
			in := map[string]any{"filter": logViewFilter, "description": "Dormeto: the load balancers' request logs, which router " + x.n.router + " reads"}
			if err := x.call(http.MethodPost, x.logViewsURL()+"?viewId="+url.QueryEscape(x.n.logView), in, nil); err != nil {
				return "", err
			}
			return "created " + x.logViewWords(), nil
		}}, nil
	case got.Filter != logViewFilter:
		x.found("logView", x.logViewName())
		return &step{name: "logView", lines: []string{"~ log view " + x.n.logView + ": filter " + logViewFilter}, apply: func() (string, error) {
			if err := x.call(http.MethodPatch, x.logViewURL()+"?updateMask=filter", map[string]any{"filter": logViewFilter}, nil); err != nil {
				return "", err
			}
			return "updated " + x.n.logView + " (filter " + logViewFilter + ")", nil
		}}, nil
	}
	x.found("logView", x.logViewName())
	return nil, nil
}

// verifyLogView is the verification's problem with the view, "" for none.
func (x *run) verifyLogView() string {
	var got googleLogView
	if err := x.d.Google.Do(x.ctx, http.MethodGet, x.logViewURL(), nil, &got); err != nil {
		return fmt.Sprintf("log view %s: %s", x.n.logView, err)
	}
	if got.Filter != logViewFilter {
		return fmt.Sprintf("log view %s filters %s, not %s", x.n.logView, got.Filter, logViewFilter)
	}
	return ""
}

// planLogViewRemoval follows planUnbind: the grant that names the view is gone first.
func (x *run) planLogViewRemoval() (*step, error) {
	return x.removal("logView", "logView", x.logViewName(), loggingAPI, x.logViewURL(), "log view "+x.n.logView, func() error {
		return x.call(http.MethodDelete, x.logViewURL(), nil, nil)
	})
}
