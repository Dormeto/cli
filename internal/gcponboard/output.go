package gcponboard

// What the writes print, as AWS's (the AWS and Google Cloud review's §4.4): each its line "<router>/<workload>: <what happened>",
// the router by its name in the manager; under --json the line on the standard error and, on the standard output, AWS's map
// {"<router>/<workload>": answer} ({} when the write is refused), and the list AWS's object. A workload the controller answers
// is printed with the shared names (§5.2), as the manager's rows name theirs: router (its name in the manager), account (the
// project), region, cloud, workloadId, pauses each with its scope, and the backends' shared kinds, from a router of any version
// (before 0.40.0 it answers id and paused alone; the REST values stay on the wire). A dry run prints, under --json, the released
// shape of both subtrees, {"dryRun": true, "targets": [...]}.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/install"
	"github.com/Dormeto/cli/internal/output"
)

// needsYes is AWS's refusal of a question without a terminal (exit 2): --yes acts without asking.
func needsYes(noun string) *stop { return &stop{2, output.NeedsYes(1, noun).Error()} }

// textOut is where a command's words go: stdout, and stderr under --json, whose stdout is JSON alone.
func (x *run) textOut() io.Writer { return output.Words(x.r.JSON, x.d.Out, x.d.Err) }

// ask asks q as AWS's commands ask before a write (output.Asker): on a terminal, --yes skips it and is needed without one
// (exit 2). done is true when it is declined: "not confirmed: nothing sent", and under --json {"confirmed":false,
// "targets":[…]} on stdout.
func (x *run) ask(q output.Question) (done bool, err error) {
	done, err = output.Asker{JSON: x.r.JSON, Yes: x.r.Yes, Stdout: x.d.Out, Stderr: x.d.Err,
		Terminal: func() bool { return x.d.Terminal },
		Confirm: func(words io.Writer, question string) (bool, error) {
			return install.Prompted(x.ctx, words, func() (bool, error) { return x.d.Prompter.Confirm(question) })
		},
	}.Ask(q)
	if errors.Is(err, output.ErrNeedsYes) {
		return false, &stop{2, err.Error()}
	}
	return done, err
}

// sharedKinds are the shared names of the controller's backend kinds.
var sharedKinds = map[string]string{"vm": "instance", "mig": "asg", kindPods: "k8s", kindDatabase: "cloudsql-instance"}

// sharedKind is a backend kind of the controller by its shared name (a kind it does not know, as it is).
func sharedKind(kind string) string {
	if shared, ok := sharedKinds[kind]; ok {
		return shared
	}
	return kind
}

// sharedWorkload is a workload of the controller as the manager's rows are (fleet.Workload): its router by its name in the
// manager, its account (the project), its region and its cloud; workloadId (else id), pauses (else [paused]), each pause with
// its scope (the workload's when it names none), and the backends' shared kinds; id and paused are left out.
func (x *run) sharedWorkload(raw json.RawMessage) (json.RawMessage, error) {
	var w map[string]json.RawMessage
	if err := json.Unmarshal(raw, &w); err != nil || w == nil {
		return nil, fmt.Errorf("the controller's answer is not a workload: %s", raw)
	}
	for k, v := range map[string]string{"router": x.n.managed, "account": x.n.project, "region": x.n.region, "cloud": "gcp"} {
		w[k], _ = json.Marshal(v)
	}
	if _, ok := w["workloadId"]; !ok && w["id"] != nil {
		w["workloadId"] = w["id"]
	}
	delete(w, "id")
	var pauses []map[string]any
	if err := json.Unmarshal(w["pauses"], &pauses); err != nil || pauses == nil {
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
	shared, err := x.sharedWorkload(raw)
	if err != nil {
		return err
	}
	return x.answered(id, shared, lines...)
}

// printAnswers prints AWS's map of answers, by target, on the standard output.
func (x *run) printAnswers(answers map[string]json.RawMessage) error {
	return output.JSON(x.d.Out, answers)
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

// refusedWorkloadWrite is refusedWrite for a nap, a wake, a pause, a resume or a schedule's set or clear: the refusal printed
// as AWS's applyAll prints a workload's, "<router>/<workload>: <refusal>", exit 1. An interrupt stays one.
func (x *run) refusedWorkloadWrite(err error) error {
	if x.ctx.Err() == nil {
		err = &stop{1, x.target(x.r.ID) + ": " + refusal(err)}
	}
	return x.refusedWrite(err)
}

// answeringRefusal runs a write that, under --json, prints AWS's map of no answer, {}, when it is refused (exit 1) before it
// printed anything on the standard output, as refusedWrite does: not for a bad flag, a decline or an interrupt.
func (x *run) answeringRefusal(write func() error) error {
	if !x.r.JSON {
		return write()
	}
	out := &writtenTo{w: x.d.Out}
	x.d.Out = out
	err := write()
	x.d.Out = out.w
	var s *stop
	if err == nil || out.any || x.ctx.Err() != nil || (errors.As(err, &s) && s.code != 1) {
		return err
	}
	return x.refusedWrite(err)
}

// writtenTo is a writer that tells whether anything was written to it.
type writtenTo struct {
	w   io.Writer
	any bool
}

func (o *writtenTo) Write(p []byte) (int, error) {
	o.any = o.any || len(p) > 0
	return o.w.Write(p)
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
		shared, err := x.sharedWorkload(row)
		if err != nil {
			return err
		}
		list.Workloads = append(list.Workloads, shared)
	}
	return output.JSON(x.d.Out, list)
}

// dryRequest is a request a dry run says, the shape of both subtrees' (fleet.DryRequest): what it addresses (the
// <router>/<workload>, or the router), its method, its path and its body (none: absent).
type dryRequest = fleet.DryRequest

// dryRequestOf is the request a dry run says of a call: in its body (nil for none) as it would go, without `by`.
func dryRequestOf(target, method, path string, in any) dryRequest {
	r := dryRequest{Targets: []string{target}, Method: method, Path: path}
	if in != nil {
		if b, err := json.Marshal(in); err == nil && string(b) != "null" {
			r.Body = b
		}
	}
	return r
}

// sayDry says the requests of a dry run on targets, each "dry run: <target>: would <method> <path>, nothing sent" and its body on
// the next line (fleet.DryRunText), then the lines after them, on the words' stream; under --json, the released
// {"dryRun":true,"targets":[…]} on the standard output, what each request sends in the words alone.
func (x *run) sayDry(targets []string, requests []dryRequest, after ...string) error {
	out := x.textOut()
	for _, r := range requests {
		fmt.Fprint(out, fleet.DryRunText(strings.Join(r.Targets, ", "), r.Method, r.Path, r.Body))
	}
	for _, l := range after {
		fmt.Fprintln(out, l)
	}
	return output.Stop(x.d.Out, x.r.JSON, "dryRun", true, targets)
}
