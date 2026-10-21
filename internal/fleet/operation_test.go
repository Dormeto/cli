package fleet

// The manager's operation records, their start, reading and following: progress per step change, the errors waited out
// and those given up on, the context's end; the 409 and 400 shapes of the API's refusals.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// reads is a get that answers the records (or errors) in order, the last one repeated; it counts the reads.
type reads struct {
	answers []any // Operation or error
	n       int
	onRead  func(n int)
}

func (r *reads) get(ctx context.Context, id string) (Operation, error) {
	a := r.answers[min(r.n, len(r.answers)-1)]
	r.n++
	if r.onRead != nil {
		r.onRead(r.n)
	}
	if err, ok := a.(error); ok {
		return Operation{}, err
	}
	op := a.(Operation)
	op.ID = id
	return op, nil
}

func op(status string, steps ...string) Operation {
	o := Operation{Type: "install-router", Status: status}
	for i := 0; i+1 < len(steps); i += 2 {
		o.Steps = append(o.Steps, OperationStep{Name: steps[i], Status: steps[i+1]})
	}
	return o
}

func followOptions(progress *[]Operation) FollowOptions {
	return FollowOptions{Poll: 5 * time.Second, Sleep: (&clock{}).sleep, Progress: func(o Operation) { *progress = append(*progress, o) }}
}

func TestFollowCallsProgressOncePerStepChange(t *testing.T) {
	r := &reads{answers: []any{
		op("running", "find router", "pending", "install router", "pending"),
		op("running", "find router", "running", "install router", "pending"),
		op("running", "find router", "running", "install router", "pending"),
		op("running", "find router", "done", "install router", "running"),
		op("succeeded", "find router", "done", "install router", "done"),
	}}
	var progress []Operation
	got, err := Follow(t.Context(), r.get, "op-1", followOptions(&progress))
	if err != nil || got.Status != "succeeded" || got.ID != "op-1" || r.n != 5 {
		t.Fatalf("%+v, %v, %d reads", got, err, r.n)
	}
	if len(progress) != 3 {
		t.Fatalf("%d progress calls: %+v", len(progress), progress)
	}
	if c := progress[0].Current(); c == nil || c.Name != "find router" || c.Status != "running" {
		t.Errorf("first progress at %+v", c)
	}
	if c := progress[2].Current(); c == nil || c.Name != "install router" || c.Status != "done" {
		t.Errorf("last progress at %+v", c)
	}
}

func TestFollowWaitsOutTwo503s(t *testing.T) {
	unavailable := &APIError{Status: 503, Code: "unavailable", Message: "later"}
	r := &reads{answers: []any{unavailable, unavailable, op("succeeded")}}
	got, err := Follow(t.Context(), r.get, "op-1", followOptions(new([]Operation)))
	if err != nil || got.Status != "succeeded" || r.n != 3 {
		t.Errorf("%+v, %v, %d reads", got, err, r.n)
	}
}

func TestFollowGivesUpAfterThree403sWithTheLastRecord(t *testing.T) {
	forbidden := &APIError{Status: 403, Code: "forbidden", Message: "not yours"}
	r := &reads{answers: []any{op("running", "lock router", "running"), forbidden}}
	got, err := Follow(t.Context(), r.get, "op-1", followOptions(new([]Operation)))
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 403 || r.n != 1+refusedAfter {
		t.Fatalf("%v, %d reads", err, r.n)
	}
	if got.ID != "op-1" || got.Status != "running" || got.Current().Name != "lock router" {
		t.Errorf("last record %+v", got)
	}
}

func TestFollowReturnsTheLastRecordOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	r := &reads{answers: []any{op("running", "slot", "running"), op("running", "slot", "done", "onboard", "running")}}
	r.onRead = func(n int) {
		if n == 2 {
			cancel()
		}
	}
	got, err := Follow(ctx, r.get, "op-1", followOptions(new([]Operation)))
	if !errors.Is(err, context.Canceled) || r.n != 2 {
		t.Fatalf("%v, %d reads", err, r.n)
	}
	if got.ID != "op-1" || got.Current().Name != "onboard" {
		t.Errorf("last record %+v", got)
	}
}

const startedRecord = `{"operation":{"id":"op-7","type":"install-router","target":{"account":"111111111111","region":"eu-west-3"},` +
	`"input":{"account":"111111111111","region":"eu-west-3"},"status":"running",` +
	`"steps":[{"name":"find router","status":"running","startedAt":"2026-10-19T08:00:00.000Z"},{"name":"lock router","status":"pending"}],` +
	`"cursor":0,"holds":{"locks":[],"slots":[]},"by":"arn:aws:iam::1:role/ops","createdAt":"2026-10-19T08:00:00.000Z","updatedAt":"2026-10-19T08:00:01.000Z"}}`

func TestStartOperationPostsTheBodyAndDecodesTheRecord(t *testing.T) {
	var sent string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = r.Method + " " + r.URL.Path + " " + string(b)
		w.WriteHeader(202)
		io.WriteString(w, startedRecord)
	})
	got, err := StartOperation(t.Context(), c, map[string]any{"type": "install-router", "account": "111111111111", "region": "eu-west-3"})
	if err != nil {
		t.Fatal(err)
	}
	if sent != `POST /iam/operations {"account":"111111111111","region":"eu-west-3","type":"install-router"}` {
		t.Errorf("sent %s", sent)
	}
	if got.ID != "op-7" || got.Type != "install-router" || got.Status != "running" || got.By != "arn:aws:iam::1:role/ops" ||
		got.Target.Account != "111111111111" || got.Target.Region != "eu-west-3" || got.CreatedAt != "2026-10-19T08:00:00.000Z" ||
		len(got.Steps) != 2 || got.Steps[0].StartedAt != "2026-10-19T08:00:00.000Z" {
		t.Errorf("%+v", got)
	}
	if got.TargetText() != "111111111111/eu-west-3" {
		t.Errorf("target %q", got.TargetText())
	}
	data, _ := json.Marshal(got)
	if !strings.Contains(string(data), `"holds":{"locks":[],"slots":[]}`) || !strings.Contains(string(data), `"cursor":0`) {
		t.Errorf("the record is not kept as sent: %s", data)
	}
}

func TestConflictNamesTheOtherOperation(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		io.WriteString(w, `{"error":"conflict","message":"another operation runs on this target","operationId":"op-9"}`)
	})
	_, err := StartOperation(t.Context(), c, map[string]any{"type": "uninstall-router", "router": "r1"})
	var ae *APIError
	if !errors.As(err, &ae) || ae.OperationID != "op-9" || ae.Code != "conflict" {
		t.Fatalf("%#v", err)
	}
	if want := "another operation runs on this target (operation op-9: dormeto operations watch op-9)"; err.Error() != want {
		t.Errorf("%q, want %q", err.Error(), want)
	}
}

func TestBadRequestKeepsTheErrorsList(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"bad_request","message":"invalid configuration","errors":["regions: required","refreshHours: 1 to 168"]}`)
	})
	err := c.Put(t.Context(), "/discovery/config", map[string]any{}, nil)
	var ae *APIError
	if !errors.As(err, &ae) || len(ae.Errors) != 2 || ae.Errors[1] != "refreshHours: 1 to 168" || ae.OperationID != "" {
		t.Fatalf("%#v", err)
	}
	if err.Error() != "invalid configuration" {
		t.Errorf("%q", err.Error())
	}
}

func TestGetOperationEscapesTheID(t *testing.T) {
	var path string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		io.WriteString(w, `{"id":"a/b","status":"succeeded"}`)
	})
	got, err := GetOperation(c)(t.Context(), "a/b")
	if err != nil || got.Status != "succeeded" || path != "/iam/operations/a%2Fb" {
		t.Errorf("%+v, %v, path %q", got, err, path)
	}
}

func TestListedOperationIsTheRecordOfThatID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/iam/operations" {
			t.Errorf("path %s", r.URL.Path)
		}
		io.WriteString(w, `{"operations":[{"id":"u-1","type":"update-manager","status":"failed"},{"id":"u-2","type":"update-manager","status":"running"}]}`)
	})
	got, err := ListedOperation(c)(t.Context(), "u-2")
	if err != nil || got.ID != "u-2" || got.Status != "running" {
		t.Errorf("%+v, %v", got, err)
	}
	if _, err := ListedOperation(c)(t.Context(), "u-3"); err == nil || !strings.Contains(err.Error(), "u-3") {
		t.Errorf("unknown id: %v", err)
	}
}

func TestCurrentStep(t *testing.T) {
	for _, c := range []struct {
		op   Operation
		want string
	}{
		{op("running", "a", "done", "b", "running", "c", "pending"), "b"},
		{op("failed", "a", "done", "b", "failed", "c", "pending"), "b"},
		{op("succeeded", "a", "done", "b", "done", "c", "skipped"), "b"},
		{op("running", "a", "pending"), ""},
	} {
		got := ""
		if s := c.op.Current(); s != nil {
			got = s.Name
		}
		if got != c.want {
			t.Errorf("%+v: %q, want %q", c.op.Steps, got, c.want)
		}
	}
}

func TestTargetText(t *testing.T) {
	for _, c := range []struct {
		target OperationTarget
		want   string
	}{
		{OperationTarget{Account: "1", Region: "eu-west-1", Router: "prod-eu", WorkloadID: "w-1"}, "prod-eu/w-1"},
		{OperationTarget{Router: "prod-eu"}, "prod-eu"},
		{OperationTarget{Account: "1", Region: "eu-west-1", TargetGroupARN: "arn:aws:elasticloadbalancing:eu-west-1:1:targetgroup/api-tg/0123456789abcdef"}, "api-tg"},
		{OperationTarget{Account: "1", Region: "eu-west-1"}, "1/eu-west-1"},
		{OperationTarget{Account: "1", ExclusionID: "ex-1"}, "exclusion ex-1"},
		{OperationTarget{Account: "111111111111"}, "111111111111"},
	} {
		if got := (Operation{Target: c.target}).TargetText(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.target, got, c.want)
		}
	}
}

func TestFollowWithWaitOutWaitsOutEveryFailedReadAndSaysEachErrorOnce(t *testing.T) {
	notFound := &APIError{Status: 404, Gateway: true, Message: "Not Found"}
	forbidden := &APIError{Status: 403, Gateway: true, Message: "Forbidden"}
	r := &reads{answers: []any{op("running", "apply", "running"), notFound, notFound, notFound, forbidden, notFound, op("succeeded", "apply", "done")}}
	var told []string
	o := followOptions(new([]Operation))
	o.WaitOut = func(err error) { told = append(told, err.Error()) }
	got, err := Follow(t.Context(), r.get, "op-1", o)
	if err != nil || got.Status != "succeeded" || r.n != 7 {
		t.Fatalf("%+v, %v, %d reads", got, err, r.n)
	}
	if len(told) != 2 || told[0] != notFound.Error() || told[1] != forbidden.Error() {
		t.Errorf("told %q", told)
	}
}
