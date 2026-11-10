package gcponboard

// What the writing commands share with AWS's (the AWS and Google Cloud review's §4.2): a workload named as AWS's targets are,
// <router>/<workload> with the router's name in the manager; --dry-run saying the request and sending nothing; the
// offboarding's confirmation; wake --wait; and list's filters, applied to the controller's list.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/install"
)

// followEvery is how often wake --wait reads the workload, as AWS's.
const followEvery = 5 * time.Second

// target is a workload as AWS's commands name one: <router>/<workload>, the router by its name in the manager.
func (x *run) target(id string) string { return x.n.managed + "/" + id }

// workloadDryRun says the request a write of the run's workload would send, and stops: no call (sayDry).
func (x *run) workloadDryRun(method, path string, in any) error {
	target := x.target(x.r.ID)
	return x.sayDry([]string{target}, []dryRequest{dryRequestOf(target, method, path, in)})
}

// routerDryRunOf is workloadDryRun of a write of the router, which it addresses by its name in the manager.
func (x *run) routerDryRunOf(method, path string, in any) error {
	return x.sayDry([]string{x.n.managed}, []dryRequest{dryRequestOf(x.n.managed, method, path, in)})
}

// confirmWorkload asks question on a terminal, the workload listed first, as AWS's commands ask before a destructive write:
// --yes skips it and is needed without a terminal (exit 2). done is true when it is declined: "not confirmed: nothing sent",
// and under --json {"confirmed":false,"targets":[…]} on stdout.
func (x *run) confirmWorkload(question string) (done bool, err error) {
	if x.r.Yes {
		return false, nil
	}
	if !x.d.Terminal {
		return false, &stop{2, "confirmation needed: pass --yes to act on 1 workload without a terminal"}
	}
	target := x.target(x.r.ID)
	out := x.textOut()
	fmt.Fprintf(out, "  %s\n", target)
	ok, err := install.Prompted(x.ctx, out, func() (bool, error) { return x.d.Prompter.Confirm(question) })
	if err != nil || ok {
		return false, err
	}
	fmt.Fprintln(out, "not confirmed: nothing sent")
	return true, x.printStop("confirmed", false, []string{target})
}

// waitAwake reads the workload every 5 s until AWAKE (a line on stdout, stderr under --json), FAILED (its failure on
// stderr) or --timeout, as AWS's wake --wait: a failure, a timeout or a workload that cannot be read any more is exit 1.
func (x *run) waitAwake(path string) error {
	target := x.target(x.r.ID)
	deadline := x.d.Now().Add(x.r.Timeout)
	errs := 0
	for {
		var w workload
		err := x.controller(http.MethodGet, path, nil, &w)
		switch {
		case x.ctx.Err() != nil:
			fmt.Fprintf(x.d.Err, "interrupted; not awake: %s\n", target)
			return &stop{exitInterrupted, ""}
		case err != nil:
			fmt.Fprintf(x.d.Err, "%s: %s\n", target, refusal(err))
			if errs++; errs >= 3 || lastWord(err) {
				fmt.Fprintf(x.d.Err, "%s: not followed any more\n", target)
				return &stop{1, ""}
			}
		case w.State == "AWAKE":
			fmt.Fprintf(x.textOut(), "%s: awake\n", target)
			return nil
		case w.State == "FAILED":
			fmt.Fprintf(x.d.Err, "%s: failed to wake: %s\n", target, failureText(w.Failure))
			return &stop{1, ""}
		default:
			errs = 0
		}
		if !x.d.Now().Before(deadline) {
			fmt.Fprintf(x.d.Err, "timeout of %s reached: not awake: %s\n", x.r.Timeout, target)
			return &stop{1, ""}
		}
		if err := x.d.Sleep(x.ctx, min(followEvery, deadline.Sub(x.d.Now()))); err != nil {
			fmt.Fprintf(x.d.Err, "interrupted; not awake: %s\n", target)
			return &stop{exitInterrupted, ""}
		}
	}
}

// lastWord tells that the controller refused the read (a 4xx but 429): reading again says no more.
func lastWord(err error) bool {
	var e *gcp.Error
	return errors.As(err, &e) && e.Code >= 400 && e.Code < 500 && e.Code != http.StatusTooManyRequests
}

// failureText is a workload's failure as one line: its message, whatever shape the router gave it, as AWS's.
func failureText(raw json.RawMessage) string {
	var f struct{ Message string }
	if json.Unmarshal(raw, &f) == nil && f.Message != "" {
		return f.Message
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	return "no reason given"
}

// filtered is listing the list's filters keep: the rows as the controller answered them, and each decoded; every is all the
// rows decoded. A router before 0.40.0 answers no tags: --tag is refused there rather than match nothing.
func (x *run) filtered(raw json.RawMessage) (keptRows []json.RawMessage, kept, every []workload, err error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, nil, nil, fmt.Errorf("the controller's answer is not a list of workloads: %w", err)
	}
	keptRows, kept = []json.RawMessage{}, []workload{}
	for _, row := range rows {
		var fields map[string]json.RawMessage
		w, err := decodeWorkload(row)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("the controller's answer is not a list of workloads: %w", err)
		}
		_ = json.Unmarshal(row, &fields)
		if _, ok := fields["tags"]; !ok && len(x.r.HasTags) > 0 {
			return nil, nil, nil, &stop{1, fmt.Sprintf("router %s does not answer its workloads' tags: --tag needs router %s or later: dormeto gcp routers install --version %s",
				x.n.router, nameAndTagsRouterVersion, embeddedRouterVersion())}
		}
		every = append(every, w)
		if x.matches(w) {
			keptRows, kept = append(keptRows, row), append(kept, w)
		}
	}
	return keptRows, kept, every, nil
}

// matches tells whether a workload passes the list's filters: one of the states, the text in its ID or name, every tag.
func (x *run) matches(w workload) bool {
	if len(x.r.States) > 0 && !slices.Contains(x.r.States, w.State) {
		return false
	}
	if text := strings.ToLower(x.r.Search); text != "" && !strings.Contains(strings.ToLower(w.ID), text) && !strings.Contains(strings.ToLower(w.Name), text) {
		return false
	}
	for k, v := range x.r.HasTags {
		if got, ok := w.Tags[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// filtering tells whether the list has a filter.
func (x *run) filtering() bool {
	return len(x.r.States) > 0 || x.r.Search != "" || len(x.r.HasTags) > 0
}

// offboardDryRun says what the offboarding would send and stops: the controller's delete (none for a workload it no longer
// holds: a rerun), then the tags it would unbind, the Kubernetes access it would remove and the request logs it would set back.
func (x *run) offboardDryRun(id string, p pendingOffboarding, there bool) error {
	target := x.target(id)
	var requests []dryRequest
	var after []string
	if there {
		requests = append(requests, dryRequestOf(target, http.MethodDelete, "/v1/workloads/"+esc(id), nil))
		if x.r.Force {
			after = append(after, "  again with ?force=true if its wake fails")
		}
	} else {
		after = append(after, "dry run: "+target+": the controller no longer holds it, nothing sent")
	}
	var untag []string
	pods := false
	for _, b := range p.Backends {
		if b.Kind == kindPods {
			pods = true
			continue
		}
		untag = append(untag, b.String())
	}
	if len(untag) > 0 {
		after = append(after, "  then untag: "+strings.Join(untag, ", "))
	}
	if pods || (x.rec != nil && len(x.recordedAccess(id)) > 0) {
		after = append(after, "  then remove its Kubernetes access: the Role and RoleBinding dormeto-"+id)
	}
	if p.RequestLogs != nil {
		after = append(after, "  then set its backend service's request logs back")
	}
	return x.sayDry([]string{target}, requests, after...)
}
