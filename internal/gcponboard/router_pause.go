package gcponboard

// `dormeto gcp routers pause` and `resume`: the controller's POST /v1/pause and /v1/resume, the router-wide pause, with the
// bodies of a workload's (`{reason, until?, keepAsleep?, by}` and `{by}`) and AWS's flags, lines and --json
// (`{"<router>": <answer>}`). The operator's gcloud account is the `by`. The router's checks (the 366 days) are the
// controller's, said as it says them; a router below 0.40.0 has no route for them, which is said with its version and the
// one to install.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
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
	in, _, err := x.pauseIn()
	if err != nil {
		return err
	}
	if err := x.require("workloads"); err != nil {
		return err
	}
	if x.r.DryRun {
		return x.routerDryRun("pause", in)
	}
	if in["by"], err = x.d.Google.Tokens.Account(x.ctx); err != nil {
		return err
	}
	var raw json.RawMessage
	if err := x.withRoutes(routerPauseWords, http.MethodPost, "/v1/pause", in, &raw); err != nil {
		return err
	}
	if x.r.JSON {
		return x.printByRouter(raw)
	}
	var a struct {
		Woken []string `json:"woken"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("the controller's answer is not a pause: %w", err)
	}
	// AWS's line: <router>: paused[ until <local time>][, keeping asleep][, woke <workloads>].
	line := x.n.managed + ": paused"
	if until, ok := in["until"].(string); ok {
		if t, err := time.Parse(time.RFC3339, until); err == nil {
			line += " until " + t.In(x.d.Now().Location()).Format("2006-01-02 15:04 MST")
		}
	}
	if x.r.KeepAsleep {
		line += ", keeping asleep"
	}
	if len(a.Woken) > 0 {
		line += ", woke " + strings.Join(a.Woken, ", ")
	}
	x.say("%s", line)
	return nil
}

func (x *run) resumeRouter() error {
	if err := x.require("workloads"); err != nil {
		return err
	}
	if x.r.DryRun {
		return x.routerDryRun("resume", map[string]any{})
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
		return x.printByRouter(raw)
	}
	var a struct {
		Resumed bool `json:"resumed"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("the controller's answer is not a resume: %w", err)
	}
	if a.Resumed {
		x.say("%s: resumed", x.n.managed)
	} else {
		x.say("%s: not paused", x.n.managed)
	}
	return nil
}

// printByRouter prints the controller's answer under the router's name in the manager, as AWS's routers pause and resume print
// theirs: {"<router>": <answer>}.
func (x *run) printByRouter(answer json.RawMessage) error {
	b, err := json.Marshal(map[string]json.RawMessage{x.n.managed: answer})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(x.d.Out, "%s\n", b)
	return err
}

// routerDryRun says what the pause or resume would send, to which router, and stops: no call (sayDry).
func (x *run) routerDryRun(verb string, in map[string]any) error {
	if len(in) == 0 {
		in = nil
	}
	return x.routerDryRunOf(http.MethodPost, "/v1/"+verb, in)
}
