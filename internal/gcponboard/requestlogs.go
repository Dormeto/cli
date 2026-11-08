package gcponboard

// A workload's request logs (the G4 design's §5): the router's idle check reads the load balancer's request logs of a backend
// service that logs every request (its logConfig on, sampleRate 1.0), and counts only the requests the waker would not ignore;
// else the raw count decides. The onboarding reads the backend service's logConfig as the operator. With --request-logs, one
// that does not log every request is set to log each one, after the dry run and before the onboarding, and the onboarding
// sends what it found (requestLogs.before), which the controller keeps; a refused onboarding sets it back. Without the flag,
// it warns. The offboarding sets it back after the controller's delete, if it is still what Dormeto set; changed since, it is
// left and said. The router never holds the right to change a backend service: these are the operator's calls.

import (
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"time"

	"github.com/Dormeto/cli/internal/gcp"
)

// logsTimeout bounds the wait for a backend service's update.
const logsTimeout = 2 * time.Minute

// countWarning is said after an onboarding without --request-logs of a backend service that does not log every request.
const countWarning = "! the idle check counts every request, scanners included: --request-logs reads the requests instead (Cloud Logging's price past its free tier)"

// logSetting is what a backend service's logConfig says of its request logs: on or off, and the share of the requests logged.
type logSetting struct {
	Enable     bool    `json:"enable"`
	SampleRate float64 `json:"sampleRate"`
}

// everyRequest is the router's test (controller-core.mjs): logging on at 1.0.
func (l logSetting) everyRequest() bool { return l.Enable && l.SampleRate == 1 }

// same tells whether two settings log the same requests: off is off, whatever its rate.
func (l logSetting) same(o logSetting) bool {
	return l.Enable == o.Enable && (!l.Enable || l.SampleRate == o.SampleRate)
}

func (l logSetting) String() string {
	if !l.Enable {
		return "off"
	}
	return "sample rate " + rate(l.SampleRate)
}

func rate(r float64) string {
	if r == float64(int64(r)) {
		return strconv.FormatFloat(r, 'f', 1, 64)
	}
	return strconv.FormatFloat(r, 'f', -1, 64)
}

// requestLogs is what an onboarding turned on of a backend service's request logs, which its offboarding sets back: the
// backend service, its region ("" for a global one) and its logConfig before.
type requestLogs struct {
	BackendService string     `json:"backendService"`
	Region         string     `json:"region,omitempty"`
	Before         logSetting `json:"before"`
}

// requestLogsIn is the onboarding's field, and the controller's Workload's.
type requestLogsIn struct {
	Before *logSetting `json:"before"`
}

// googleBackendService is what the onboarding reads of a backend service.
type googleBackendService struct {
	Fingerprint string         `json:"fingerprint"`
	LogConfig   map[string]any `json:"logConfig"`
}

// setting reads Google's defaults where the logConfig says nothing: off, and 1.0 (the router's reading too).
func (b googleBackendService) setting() logSetting {
	on, _ := b.LogConfig["enable"].(bool)
	r, ok := b.LogConfig["sampleRate"].(float64)
	if !ok {
		r = 1
	}
	return logSetting{Enable: on, SampleRate: r}
}

// regionOf is the region of a workload's backend service: "" for a global or classic load balancer (also when the dry run of a
// router below 0.35.0 says nothing of it), else the load balancer's region, the router's when it names none.
func (x *run) regionOf(lb *lbRef) string {
	if lb == nil || lb.Kind != "regional" {
		return ""
	}
	if lb.Region != "" {
		return lb.Region
	}
	return x.n.region
}

func (x *run) backendServiceURL(name, region string) string {
	place := "global"
	if region != "" {
		place = "regions/" + esc(region)
	}
	return "https://compute.googleapis.com/compute/v1/projects/" + esc(x.n.project) + "/" + place + "/backendServices/" + esc(name)
}

func (x *run) readLogs(name, region string) (googleBackendService, error) {
	var b googleBackendService
	err := x.call(http.MethodGet, x.backendServiceURL(name, region), nil, &b)
	return b, err
}

// patchLogs sets a backend service's logConfig to want, its other fields kept, under the fingerprint read, and waits for the
// update; answered says Google took the update, whatever the wait. Turned off, no sampleRate is sent: Google takes one only
// with logging on, and keeps the one it holds.
func (x *run) patchLogs(name, region string, got googleBackendService, want logSetting) (answered bool, err error) {
	logConfig := maps.Clone(got.LogConfig)
	if logConfig == nil {
		logConfig = map[string]any{}
	}
	logConfig["enable"] = want.Enable
	if want.Enable {
		logConfig["sampleRate"] = want.SampleRate
	} else {
		delete(logConfig, "sampleRate")
	}
	in := map[string]any{"logConfig": logConfig}
	if got.Fingerprint != "" {
		in["fingerprint"] = got.Fingerprint
	}
	var op struct {
		SelfLink string `json:"selfLink"`
	}
	if err := x.call(http.MethodPatch, x.backendServiceURL(name, region), in, &op); err != nil {
		return false, err
	}
	return true, gcp.WaitCompute(x.ctx, x.d.Google, op.SelfLink, logsTimeout)
}

// turnOnLogs sets the request logs of the backend service to every request. changed says the logConfig may be Dormeto's
// now: the update was answered (its operation's wait may have failed since), or its answer is lost; a refusal changed nothing.
func (x *run) turnOnLogs(l requestLogs, got googleBackendService) (changed bool, err error) {
	answered, err := x.patchLogs(l.BackendService, l.Region, got, logSetting{Enable: true, SampleRate: 1})
	return answered || (err != nil && !refused(err)), err
}

// setBack is what restoreLogs did.
type setBack int

const (
	logsRestored setBack = iota // set back to before
	logsAsBefore                // already as before: nothing to do
	logsChanged                 // changed since Dormeto set them: left
)

// restoreLogs sets the request logs of a backend service back to before, when they are still what Dormeto set (every
// request); already as before, there is nothing to do; changed since, they are left.
func (x *run) restoreLogs(l requestLogs) (setBack, error) {
	got, err := x.readLogs(l.BackendService, l.Region)
	if err != nil {
		return 0, err
	}
	now := got.setting()
	switch {
	case now.same(l.Before):
		return logsAsBefore, nil
	case !now.everyRequest():
		return logsChanged, nil
	}
	_, err = x.patchLogs(l.BackendService, l.Region, got, l.Before)
	return logsRestored, err
}

// undoLogs sets back the request logs a refused onboarding turned on, and says what it did.
func (x *run) undoLogs(l *requestLogs) []string {
	if l == nil {
		return nil
	}
	done, err := x.restoreLogs(*l)
	switch {
	case err != nil:
		return []string{fmt.Sprintf("The request logs of backend service %s are not set back (%s): %s", l.BackendService, l.Before, refusal(err))}
	case done == logsChanged:
		return []string{fmt.Sprintf("The request logs of backend service %s changed since this run turned them on: left as they are (before it: %s).", l.BackendService, l.Before)}
	}
	return []string{fmt.Sprintf("The request logs of backend service %s are set back (%s).", l.BackendService, l.Before)}
}

// logsAfterController sets back what the onboarding turned on of the request logs of an offboarded workload. With force, what
// it cannot set back is named in out and the rest goes on.
func (x *run) logsAfterController(id string, l *requestLogs, force bool, out *offboarding) error {
	if l == nil {
		return nil
	}
	done, err := x.restoreLogs(*l)
	what := fmt.Sprintf("the request logs of backend service %s (%s)", l.BackendService, l.Before)
	switch {
	case err != nil && force:
		out.logsLeft = append(out.logsLeft, fmt.Sprintf("%s are not set back: %s", what, x.why(err)))
	case err != nil:
		return x.ended(fmt.Sprintf("workload %s is offboarded, but the request logs of backend service %s are not set back: %s\n(run the same command again to finish)", id, l.BackendService, x.why(err)))
	case done == logsRestored:
		out.restored = append(out.restored, what)
	case done == logsChanged:
		out.kept = append(out.kept, fmt.Sprintf("! the request logs of backend service %s changed since the onboarding turned them on: left as they are (before it: %s)", l.BackendService, l.Before))
	}
	return nil
}

// requestLogsOf is what the controller's workload says its onboarding turned on of the request logs: nil for nothing.
func (x *run) requestLogsOf(w workload) *requestLogs {
	if w.RequestLogs == nil || w.RequestLogs.Before == nil {
		return nil
	}
	return &requestLogs{BackendService: w.BackendService, Region: x.regionOf(w.LB), Before: *w.RequestLogs.Before}
}
