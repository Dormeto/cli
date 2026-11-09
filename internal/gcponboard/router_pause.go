package gcponboard

// `dormeto gcp routers pause` and `resume`: the controller's POST /v1/pause and /v1/resume, the router-wide pause, with the
// bodies of a workload's (`{reason, until?, keepAsleep?, by}` and `{by}`) and AWS's flags and lines. The operator's gcloud
// account is the `by`. The router's checks (the 366 days) are the controller's, said as it says them; a router below 0.40.0 has
// no route for them, which is said with its version and the one to install.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// routerPauseWords is what a router below 0.40.0 lacks, in the words of its refusal.
const routerPauseWords = "the router's pause"

// RouterPause is `dormeto gcp routers pause --reason <text> [--until <time>] [--keep-asleep]`.
func RouterPause(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.pauseRouter())
}

// RouterResume is `dormeto gcp routers resume`.
func RouterResume(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.resumeRouter())
}

func (x *run) pauseRouter() error {
	in, ends, err := x.pauseIn()
	if err != nil {
		return err
	}
	if err := x.require("workloads"); err != nil {
		return err
	}
	if in["by"], err = x.d.Google.Tokens.Account(x.ctx); err != nil {
		return err
	}
	var raw json.RawMessage
	if err := x.withRoutes(routerPauseWords, http.MethodPost, "/v1/pause", in, &raw); err != nil {
		return err
	}
	if x.r.JSON {
		fmt.Fprintf(x.d.Out, "%s\n", raw)
		return nil
	}
	var a struct {
		Woken []string `json:"woken"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("the controller's answer is not a pause: %w", err)
	}
	x.say("Router %s is paused (%s): none of its workloads is napped automatically %s.", x.n.router, in["reason"], ends)
	switch {
	case x.r.KeepAsleep:
		x.say("The workloads asleep stay as they are, keeping asleep: a visitor still wakes one.")
	case len(a.Woken) > 0:
		x.say("Asleep, now woke %s.", strings.Join(a.Woken, ", "))
	}
	x.say("End it with: %s", x.routersCommand("resume"))
	return nil
}

func (x *run) resumeRouter() error {
	if err := x.require("workloads"); err != nil {
		return err
	}
	by, err := x.d.Google.Tokens.Account(x.ctx)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := x.withRoutes(routerPauseWords, http.MethodPost, "/v1/resume", map[string]any{"by": by}, &raw); err != nil {
		return err
	}
	if x.r.JSON {
		fmt.Fprintf(x.d.Out, "%s\n", raw)
		return nil
	}
	var a struct {
		Resumed bool `json:"resumed"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("the controller's answer is not a resume: %w", err)
	}
	if a.Resumed {
		x.say("Router %s is resumed: its workloads follow their schedule and idle rules again.", x.n.router)
	} else {
		x.say("Router %s is not paused.", x.n.router)
	}
	return nil
}

// routersCommand is a `dormeto gcp routers` command on this router.
func (x *run) routersCommand(verb string) string {
	c := fmt.Sprintf("dormeto gcp routers %s --project %s --region %s", verb, x.n.project, x.n.region)
	if x.r.Configuration != "" {
		c += " --gcloud-configuration " + x.r.Configuration
	}
	return c
}
