package gcponboard

// What the writes print, as AWS's (the AWS and Google Cloud review's §4.4): each its line "<router>/<workload>: <what happened>",
// the router by its name in the manager; under --json the line on the standard error and, on the standard output, AWS's map
// {"<router>/<workload>": answer} ({} when the write is refused), and the list AWS's object. A workload the controller answers
// is printed with the shared names (§5.2): workloadId, pauses each with its scope, and the backends' shared kinds, from a router
// of any version (before 0.40.0 it answers id and paused alone; the REST values stay on the wire).

import (
	"encoding/json"
	"fmt"

	"github.com/Dormeto/cli/internal/fleet"
)

// sharedKinds are the shared names of the controller's backend kinds.
var sharedKinds = map[string]string{"vm": "instance", "mig": "asg", kindPods: "k8s", kindDatabase: "cloudsql-instance"}

// sharedKind is a backend kind of the controller by its shared name (a kind it does not know, as it is).
func sharedKind(kind string) string {
	if shared, ok := sharedKinds[kind]; ok {
		return shared
	}
	return kind
}

// sharedWorkload is a workload of the controller with the shared names: workloadId (else id), pauses (else [paused]), each pause
// with its scope (the workload's when it names none), and the backends' shared kinds; id and paused are left out.
func sharedWorkload(raw json.RawMessage) (json.RawMessage, error) {
	var w map[string]json.RawMessage
	if err := json.Unmarshal(raw, &w); err != nil || w == nil {
		return nil, fmt.Errorf("the controller's answer is not a workload: %s", raw)
	}
	if _, ok := w["workloadId"]; !ok && w["id"] != nil {
		w["workloadId"] = w["id"]
	}
	delete(w, "id")
	var pauses []map[string]any
	if err := json.Unmarshal(w["pauses"], &pauses); err != nil || w["pauses"] == nil {
		pauses = []map[string]any{}
		var paused map[string]any
		if json.Unmarshal(w["paused"], &paused) == nil && paused != nil {
			pauses = append(pauses, paused)
		}
	}
	delete(w, "paused")
	for _, p := range pauses {
		if _, ok := p["scope"]; !ok {
			p["scope"] = "workload"
		}
	}
	var err error
	if w["pauses"], err = json.Marshal(pauses); err != nil {
		return nil, err
	}
	var backends []map[string]any
	if json.Unmarshal(w["backends"], &backends) == nil && backends != nil {
		for _, b := range backends {
			if kind, ok := b["kind"].(string); ok {
				b["kind"] = sharedKind(kind)
			}
		}
		if w["backends"], err = json.Marshal(backends); err != nil {
			return nil, err
		}
	}
	return json.Marshal(w)
}

// decodeWorkload reads a workload of the controller, by its new names when the router answers them (workloadId, pauses), else
// its released ones.
func decodeWorkload(raw json.RawMessage) (workload, error) {
	var w workload
	if err := json.Unmarshal(raw, &w); err != nil {
		return w, fmt.Errorf("the controller's answer is not a workload: %w", err)
	}
	w.shared()
	return w, nil
}

// shared fills the released names from the new ones a router may answer alone.
func (w *workload) shared() {
	if w.WorkloadID != "" {
		w.ID = w.WorkloadID
	}
	if len(w.Pauses) > 0 {
		w.Paused = &w.Pauses[0]
	}
}

// answered says what a write did to the workload id, as AWS's: its lines on the standard output, or under --json on the standard
// error, the answer then printed on the standard output by <router>/<workload>.
func (x *run) answered(id string, answer json.RawMessage, lines ...string) error {
	out := x.textOut()
	for _, l := range lines {
		fmt.Fprintln(out, l)
	}
	if !x.r.JSON {
		return nil
	}
	if len(answer) == 0 {
		answer = json.RawMessage("null")
	}
	return x.printAnswers(map[string]json.RawMessage{x.target(id): answer})
}

// answeredWorkload is answered with the controller's workload, by the shared names.
func (x *run) answeredWorkload(id string, raw json.RawMessage, lines ...string) error {
	if !x.r.JSON {
		return x.answered(id, nil, lines...)
	}
	shared, err := sharedWorkload(raw)
	if err != nil {
		return err
	}
	return x.answered(id, shared, lines...)
}

// printAnswers prints AWS's map of answers, by target, on the standard output.
func (x *run) printAnswers(answers map[string]json.RawMessage) error {
	b, err := json.Marshal(answers)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(x.d.Out, "%s\n", b)
	return err
}

// refusedWrite is a write the controller refused: under --json AWS's map of no answer, {}, then the refusal.
func (x *run) refusedWrite(err error) error {
	if x.r.JSON {
		if perr := x.printAnswers(map[string]json.RawMessage{}); perr != nil {
			return perr
		}
	}
	return err
}

// workloadList is AWS's list object: the rows, their total, the router's counts by state, no cursor (one page) and no error.
type workloadList struct {
	Workloads []json.RawMessage `json:"workloads"`
	Total     int               `json:"total"`
	ByState   map[string]int    `json:"byState"`
	Cursor    *string           `json:"cursor"`
	Errors    []any             `json:"errors"`
}

// printList prints the rows kept as AWS's list object, every one counted by state, as the manager counts all the caller sees.
func (x *run) printList(rows []json.RawMessage, all []workload) error {
	list := workloadList{Workloads: []json.RawMessage{}, Total: len(rows), ByState: map[string]int{}, Errors: []any{}}
	for _, w := range all {
		list.ByState[w.State]++
	}
	for _, row := range rows {
		shared, err := sharedWorkload(row)
		if err != nil {
			return err
		}
		list.Workloads = append(list.Workloads, shared)
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(x.d.Out, "%s\n", b)
	return err
}

// dryRequest is a request a dry run says, AWS's: what it addresses (the <router>/<workload>, or the router), its method, its path
// and its body (none: absent).
type dryRequest struct {
	Target string          `json:"target"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// dryRequestOf is the request a dry run says of a call: in its body (nil for none) as it would go, without `by`.
func dryRequestOf(target, method, path string, in any) dryRequest {
	r := dryRequest{Target: target, Method: method, Path: path}
	if in != nil {
		if b, err := json.Marshal(in); err == nil && string(b) != "null" {
			r.Body = b
		}
	}
	return r
}

// sayDry says the requests of a dry run, each "dry run: <target>: would <method> <path>, nothing sent" and its body on the next
// line (fleet.DryRunText), then the lines after them, on the words' stream; under --json, {"dryRun":true,"requests":[…]} on the
// standard output.
func (x *run) sayDry(requests []dryRequest, after ...string) error {
	out := x.textOut()
	for _, r := range requests {
		fmt.Fprint(out, fleet.DryRunText(r.Target, r.Method, r.Path, r.Body))
	}
	for _, l := range after {
		fmt.Fprintln(out, l)
	}
	if !x.r.JSON {
		return nil
	}
	b, err := json.Marshal(map[string]any{"dryRun": true, "requests": emptyIfNone(requests)})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(x.d.Out, "%s\n", b)
	return err
}

func emptyIfNone[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
