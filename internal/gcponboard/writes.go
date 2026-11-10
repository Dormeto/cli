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

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/install"
)

// followEvery is how often wake --wait reads the workload, as AWS's.
const followEvery = 5 * time.Second

// target is a workload as AWS's commands name one: <router>/<workload>, the router by its name in the manager.
func (x *run) target(id string) string { return x.n.managed + "/" + id }

// dryRun says what a write would send, and stops: no call. what words it as AWS's ("nap 1 workload", "set the waiting page of
// router"), on is the target, then the request: "<target>: POST /v1/… <body>". Under --json, stdout is {"dryRun":true,
// "targets":[target]}, as AWS's.
func (x *run) dryRun(what, on, method, path string, in any) error {
	line := fmt.Sprintf("  %s: %s %s", on, method, path)
	if in != nil {
		line += " " + fleet.DryRunBody(in)
	}
	fmt.Fprintf(x.textOut(), "dry run: would %s, nothing sent\n%s\n", what, line)
	return x.printStop("dryRun", true, []string{on})
}

// workloadDryRun is dryRun of the workload of the run.
func (x *run) workloadDryRun(verb, method, path string, in any) error {
	return x.dryRun(verb+" 1 workload", x.target(x.r.ID), method, path, in)
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

// filtered is listing the list's filters keep: the rows as the controller answered them, and each decoded. A router before
// 0.40.0 answers no tags: --tag is refused there rather than match nothing.
func (x *run) filtered(raw json.RawMessage) ([]json.RawMessage, []workload, error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, nil, fmt.Errorf("the controller's answer is not a list of workloads: %w", err)
	}
	keptRows, kept := []json.RawMessage{}, []workload{}
	for _, row := range rows {
		var w workload
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(row, &w); err != nil {
			return nil, nil, fmt.Errorf("the controller's answer is not a list of workloads: %w", err)
		}
		_ = json.Unmarshal(row, &fields)
		if _, ok := fields["tags"]; !ok && len(x.r.HasTags) > 0 {
			return nil, nil, &stop{1, fmt.Sprintf("router %s does not answer its workloads' tags: --tag needs router %s or later: dormeto gcp routers install --version %s",
				x.n.router, nameAndTagsRouterVersion, embeddedRouterVersion())}
		}
		if x.matches(w) {
			keptRows, kept = append(keptRows, row), append(kept, w)
		}
	}
	return keptRows, kept, nil
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
	out := x.textOut()
	fmt.Fprintln(out, "dry run: would offboard 1 workload, nothing sent")
	if there {
		line := fmt.Sprintf("  %s: DELETE /v1/workloads/%s", target, esc(id))
		if x.r.Force {
			line += " (again with ?force=true if its wake fails)"
		}
		fmt.Fprintln(out, line)
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
		fmt.Fprintf(out, "  then untag: %s\n", strings.Join(untag, ", "))
	}
	if pods || (x.rec != nil && len(x.recordedAccess(id)) > 0) {
		fmt.Fprintf(out, "  then remove its Kubernetes access: the Role and RoleBinding dormeto-%s\n", id)
	}
	if p.RequestLogs != nil {
		fmt.Fprintln(out, "  then set its backend service's request logs back")
	}
	return x.printStop("dryRun", true, []string{target})
}
