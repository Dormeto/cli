package gcponboard

// `dormeto gcp workloads waiting-page show|set|clear <id>` and `dormeto gcp routers waiting-page show|set|clear`: the controller's
// GET, PUT and DELETE /v1/workloads/<id>/waiting-page and /v1/waiting-page, whose answers are AWS's (own, effective, from, favicon,
// warnings, terraform, by, at), printed as AWS's commands print them (fleet.PageView). The document is checked by the command
// before any call; the router's own refusals (a custom page while its custom pages are off, a bad title) are printed as it says them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/output"
)

// pageRouter is what a controller without the waiting page's routes lacks, in the words of its refusal.
const pageRouter = "the waiting page"

// WorkloadsPageShow, WorkloadsPageSet and WorkloadsPageClear are `dormeto gcp workloads waiting-page show|set|clear <id>`.
func WorkloadsPageShow(ctx context.Context, d Deps, r Request) int {
	return pageRun(ctx, d, r, true, http.MethodGet)
}
func WorkloadsPageSet(ctx context.Context, d Deps, r Request) int {
	return pageRun(ctx, d, r, true, http.MethodPut)
}
func WorkloadsPageClear(ctx context.Context, d Deps, r Request) int {
	return pageRun(ctx, d, r, true, http.MethodDelete)
}

// RouterPageShow, RouterPageSet and RouterPageClear are `dormeto gcp routers waiting-page show|set|clear`.
func RouterPageShow(ctx context.Context, d Deps, r Request) int {
	return pageRun(ctx, d, r, false, http.MethodGet)
}
func RouterPageSet(ctx context.Context, d Deps, r Request) int {
	return pageRun(ctx, d, r, false, http.MethodPut)
}
func RouterPageClear(ctx context.Context, d Deps, r Request) int {
	return pageRun(ctx, d, r, false, http.MethodDelete)
}

func pageRun(ctx context.Context, d Deps, r Request, workload bool, method string) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.waitingPage(workload, method))
}

// pageRefusal words a refusal of the routes: a 400's problems one to a line, as AWS's commands print them.
func pageRefusal(err error) error {
	var e *gcp.Error
	if errors.As(err, &e) && e.Code == http.StatusBadRequest && e.Message != "" {
		return &stop{1, strings.ReplaceAll(e.Message, "; ", "\n")}
	}
	return err
}

func (x *run) waitingPage(workload bool, method string) error {
	// The level as AWS's commands name it: the router by its name in the manager, a workload as <router>/<workload>.
	level, path := "router", "/v1/waiting-page"
	label := x.n.managed
	if workload {
		if x.r.ID == "" {
			return &stop{2, "the workload's ID is needed"}
		}
		level, path, label = "workload", "/v1/workloads/"+esc(x.r.ID)+"/waiting-page", x.target(x.r.ID)
	}
	if method == http.MethodPut && x.r.Page == nil {
		return &stop{2, "give --title, --description or --icon for a template, or --html for a custom page"}
	}
	if err := x.require("workloads"); err != nil {
		return err
	}
	if x.r.DryRun && method != http.MethodGet {
		var shown any
		if method == http.MethodPut {
			shown = map[string]any{"page": x.r.Page}
		}
		if workload {
			return x.workloadDryRun(method, path, shown)
		}
		return x.routerDryRunOf(method, path, shown)
	}
	var in any
	if method != http.MethodGet {
		by, err := x.d.Google.Tokens.Account(x.ctx)
		if err != nil {
			return err
		}
		body := map[string]any{"by": by}
		if method == http.MethodPut {
			body["page"] = x.r.Page
		}
		in = body
	}
	var raw json.RawMessage
	if err := x.withRoutes(pageRouter, method, path, in, &raw); err != nil {
		if stopped(err) {
			return err
		}
		return pageRefusal(err)
	}
	if x.r.JSON {
		return output.JSON(x.d.Out, raw)
	}
	var v fleet.PageView
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("the controller's answer is not a waiting page: %w", err)
	}
	switch method {
	case http.MethodGet:
		fmt.Fprint(x.d.Out, v.Text(label, level, x.d.Now().Location()))
	case http.MethodPut:
		x.say("%s: waiting page set (%s)", label, x.r.Page.Mode)
		for _, w := range v.Warnings {
			x.say("warning: %s", w)
		}
	default:
		x.say("%s: waiting page cleared", label)
	}
	return nil
}
