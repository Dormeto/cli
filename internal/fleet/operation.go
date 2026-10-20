package fleet

// The manager's operations: their records, and how one is started, read and followed until it ends. Router installs,
// upgrades and uninstalls, onboarding, offboarding, exclusions and the manager's update all run as operations.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Operation is a record of the manager's operations: what the CLI reads of it. The record as the API sent it is kept, so
// --json prints every field (input, context, holds), not only these.
type Operation struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Status     string          `json:"status"` // running, succeeded or failed
	Target     OperationTarget `json:"target"`
	Steps      []OperationStep `json:"steps"`
	Error      string          `json:"error,omitempty"`
	By         string          `json:"by,omitempty"`
	CreatedAt  string          `json:"createdAt,omitempty"`
	UpdatedAt  string          `json:"updatedAt,omitempty"`
	FinishedAt string          `json:"finishedAt,omitempty"`

	raw json.RawMessage
}

// OperationTarget is what an operation acts on, as the manager records it: an account and region (a router's install,
// upgrade or uninstall; the manager's update, with its version), a workload (offboard: its router and id too), a
// candidate's target group and load balancer (onboard), an exclusion (exclude, lift-exclusion), an account alone (its
// connection).
type OperationTarget struct {
	Account         string `json:"account,omitempty"`
	Region          string `json:"region,omitempty"`
	Router          string `json:"router,omitempty"`
	WorkloadID      string `json:"workloadId,omitempty"`
	TargetGroupARN  string `json:"targetGroupArn,omitempty"`
	LoadBalancerARN string `json:"loadBalancerArn,omitempty"`
	ExclusionID     string `json:"exclusionId,omitempty"`
	Version         string `json:"version,omitempty"`
}

// OperationStep is one step of an operation: pending, running, done, skipped or failed.
type OperationStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

type operationFields Operation // without the methods below, so that decoding does not recurse

func (o *Operation) UnmarshalJSON(data []byte) error {
	var f operationFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*o = Operation(f)
	o.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON is the record as the API sent it, or the fields known when it was built here.
func (o Operation) MarshalJSON() ([]byte, error) {
	if o.raw != nil {
		return o.raw, nil
	}
	return json.Marshal(operationFields(o))
}

// Current is the step the operation is at: the running one, else the failed one, else the last one done; nil when no
// step has begun.
func (o Operation) Current() *OperationStep {
	var failed, done *OperationStep
	for i := range o.Steps {
		switch o.Steps[i].Status {
		case "running":
			return &o.Steps[i]
		case "failed":
			if failed == nil {
				failed = &o.Steps[i]
			}
		case "done":
			done = &o.Steps[i]
		}
	}
	if failed != nil {
		return failed
	}
	return done
}

// TargetText is the target as the CLI names it: <router>/<workload>, the router, the candidate's target group name,
// <account>/<region>, the exclusion, or the account.
func (o Operation) TargetText() string {
	t := o.Target
	switch {
	case t.Router != "" && t.WorkloadID != "":
		return t.Router + "/" + t.WorkloadID
	case t.Router != "":
		return t.Router
	case t.TargetGroupARN != "":
		return TargetGroupName(t.TargetGroupARN)
	case t.ExclusionID != "":
		return "exclusion " + t.ExclusionID
	case t.Account != "" && t.Region != "":
		return t.Account + "/" + t.Region
	}
	return t.Account
}

// StartOperation starts an operation: POST /operations with body ({type, …}), the record it answers.
func StartOperation(ctx context.Context, c *Client, body map[string]any) (Operation, error) {
	var out struct {
		Operation Operation `json:"operation"`
	}
	err := c.Post(ctx, "/operations", body, &out)
	return out.Operation, err
}

// GetOperation reads an operation by its id: GET /operations/{id}.
func GetOperation(c *Client) func(context.Context, string) (Operation, error) {
	return func(ctx context.Context, id string) (Operation, error) {
		var op Operation
		err := c.Get(ctx, "/operations/"+url.PathEscape(id), nil, &op)
		return op, err
	}
}

// ListedOperation reads an operation in GET /operations: the only route that brings the manager's updates up to date.
func ListedOperation(c *Client) func(context.Context, string) (Operation, error) {
	return func(ctx context.Context, id string) (Operation, error) {
		var out struct {
			Operations []Operation `json:"operations"`
		}
		if err := c.Get(ctx, "/operations", nil, &out); err != nil {
			return Operation{}, err
		}
		for _, op := range out.Operations {
			if op.ID == id {
				return op, nil
			}
		}
		return Operation{}, fmt.Errorf("the manager lists no operation %s", id)
	}
}

// FollowOptions are how Follow reads; Sleep is injected so that tests need no real time.
type FollowOptions struct {
	Poll     time.Duration
	Sleep    func(context.Context, time.Duration) error // ends with the context's error when it is done
	Progress func(Operation)                            // called with the record each time a step's status changes
}

// refusedAfter is how many reads of an operation in a row may fail for a reason that will not pass (a 403 or 404, the
// credentials, an unreadable answer) before it is given up. A network error, 429 or 5xx is waited out: the operation
// runs in the manager, and the context bounds the wait.
const refusedAfter = 3

// Follow reads the operation id with get, at once then every Poll, until it is no longer running, and gives its last
// record. Progress is called when a step's status changes (a step not seen yet counts as pending). It ends with the
// error of the reads given up on, or with the context's error when it is done, the last record read returned with it.
func Follow(ctx context.Context, get func(context.Context, string) (Operation, error), id string, o FollowOptions) (Operation, error) {
	var last Operation
	seen := map[int]string{} // the steps' statuses, by index
	for failures := 0; ; {
		op, err := get(ctx, id)
		switch {
		case err == nil:
			failures, last = 0, op
			if stepsChanged(seen, op) && o.Progress != nil {
				o.Progress(op)
			}
			if op.Status != "running" {
				return op, nil
			}
		case ctx.Err() != nil:
			return last, ctx.Err()
		case !transient(err):
			if failures++; failures >= refusedAfter {
				return last, err
			}
		}
		if err := o.Sleep(ctx, o.Poll); err != nil {
			return last, err
		}
	}
}

// stepsChanged records op's step statuses in seen and tells whether one differs from what it held.
func stepsChanged(seen map[int]string, op Operation) bool {
	changed := false
	for i, s := range op.Steps {
		before, ok := seen[i]
		if !ok {
			before = "pending"
		}
		if s.Status != before {
			changed = true
		}
		seen[i] = s.Status
	}
	return changed
}

// transient tells whether err is worth waiting out: a network error, 429 or 5xx. Any other error (the credentials, an
// answer that cannot be read, a 4xx refusal) will not pass by itself.
func transient(err error) bool {
	var ne net.Error
	var ue *url.Error
	var ae *APIError
	switch {
	case errors.As(err, &ae):
		return ae.Status == http.StatusTooManyRequests || ae.Status >= 500
	case errors.As(err, &ue), errors.As(err, &ne):
		return true
	}
	return false
}

// MayHaveStarted tells whether a failed start leaves the operation possibly created in the manager: a 5xx, a transport
// error, or a 2xx answer that cannot be read (a 4xx refusal, or an error before the request is sent, did not start it).
func MayHaveStarted(err error) bool {
	var unreadable *unreadableError
	if errors.Is(err, ErrNotSent) {
		return false
	}
	if errors.As(err, &unreadable) {
		return true
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status >= 500
	}
	var ne net.Error
	var ue *url.Error
	return errors.As(err, &ue) || errors.As(err, &ne)
}
