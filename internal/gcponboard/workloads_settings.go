package gcponboard

// `dormeto gcp workloads settings <id>...` and `dormeto gcp routers settings`: the controller's PUT /v1/workloads/<id>/settings
// and PUT or GET /v1/settings, with the AWS commands' flags and lines. The flags' body is flat (each key a value, or null to give
// it back to the levels), `by` the operator's account, as a schedule's. What Google Cloud fixes (waitSeconds, maxHolders and
// drainSeconds) the command refuses before any call, with the router's reasons; the other checks are the router's, said as it
// says them. A router before the levels has no route for these calls: its 404 "no route" says so, with its version.

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
	"github.com/Dormeto/cli/internal/schedule"
)

// WorkloadsSettings is `dormeto gcp workloads settings <id>... [flags]`.
func WorkloadsSettings(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.workloadsSettings())
}

// RouterSettings is `dormeto gcp routers settings [flags]`: with no flag, the router's level is shown.
func RouterSettings(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.routerSettings())
}

// levelsRouter is what a controller without the levels' routes lacks, in the words of its refusal.
const levelsRouter = "the settings levels"

// withLevels is a call of the controller that a router before the levels does not route: its 404 "no route" is said with the
// router's version and the one to install, unlike the 404 of a workload it does not know.
func (x *run) withLevels(method, path string, in, out any) error {
	return x.withRoutes(levelsRouter, method, path, in, out)
}

// withRoutes is withLevels for the routes of another part of the router, named by words in the older router's refusal.
func (x *run) withRoutes(words, method, path string, in, out any) error {
	err := x.controller(method, path, in, out)
	var e *gcp.Error
	if errors.As(err, &e) && e.Code == http.StatusNotFound && strings.HasPrefix(e.Message, "no route for ") {
		version, herr := x.health()
		if herr != nil || version == "" {
			version = "of " + x.n.router
		}
		return &stop{1, fmt.Sprintf("router %s doesn't have %s: dormeto gcp routers install --version %s", version, words, embeddedRouterVersion())}
	}
	return err
}

// stopped tells that an error is a stop: the run ends there, for every workload.
func stopped(err error) bool {
	var s *stop
	return errors.As(err, &s)
}

func (x *run) workloadsSettings() error {
	if len(x.r.IDs) == 0 {
		return &stop{2, "name at least one workload, as the argument"}
	}
	if len(x.r.Levels) == 0 {
		return &stop{2, "give --hibernate, --type or a setting"}
	}
	if err := x.require("workloads"); err != nil {
		return err
	}
	said := workloadSaid(x.r.Levels)
	targets := make([]string, len(x.r.IDs))
	for i, id := range x.r.IDs {
		targets[i] = x.target(id)
	}
	if x.r.DryRun {
		var requests []dryRequest
		for i, id := range x.r.IDs {
			requests = append(requests, dryRequestOf(targets[i], http.MethodPut, "/v1/workloads/"+esc(id)+"/settings", x.r.Levels))
		}
		return x.sayDry(targets, requests)
	}
	// As AWS's: several workloads are confirmed on a terminal, and need --yes without one.
	question := fmt.Sprintf("Set %s for %s?", said, strings.Join(targets, ", "))
	if done, err := x.ask(output.Question{Names: targets, Noun: "workload", Text: question}); done || err != nil {
		return err
	}
	by, err := x.d.Google.Tokens.Account(x.ctx)
	if err != nil {
		return err
	}
	in := map[string]any{"by": by}
	for k, v := range x.r.Levels {
		in[k] = v
	}
	answers := map[string]json.RawMessage{}
	failed := false
	for i, id := range x.r.IDs {
		var raw json.RawMessage
		if err := x.withLevels(http.MethodPut, "/v1/workloads/"+esc(id)+"/settings", in, &raw); err != nil {
			if stopped(err) {
				return err
			}
			fmt.Fprintf(x.d.Err, "%s: %s\n", targets[i], refusal(err))
			failed = true
			continue
		}
		answers[targets[i]] = raw
		var a struct {
			Hibernation *fleet.Hibernation `json:"hibernation"`
		}
		_ = json.Unmarshal(raw, &a)
		line := fmt.Sprintf("%s: %s", targets[i], said)
		if _, ok := x.r.Levels["hibernate"]; ok && a.Hibernation != nil {
			line += " (effective: " + a.Hibernation.Text() + ")"
		}
		fmt.Fprintln(x.textOut(), line)
	}
	if x.r.JSON {
		if err := output.JSON(x.d.Out, answers); err != nil {
			return err
		}
	}
	if failed {
		return &stop{1, ""}
	}
	return nil
}

// hibernateWord is a --hibernate value of a body as it is said: on, off, or inherit for none.
func hibernateWord(v any) string {
	switch b := v.(type) {
	case *bool:
		return fleet.HibernateWord(b)
	case bool:
		return fleet.HibernateWord(&b)
	}
	return "inherit"
}

// workloadSaid says what a workload's flags set: "hibernate to on, its settings, type batch".
func workloadSaid(levels map[string]any) string {
	var words []string
	if v, ok := levels["hibernate"]; ok {
		words = append(words, "hibernate to "+hibernateWord(v))
	}
	for k := range levels {
		if k != "hibernate" && k != "type" {
			words = append(words, "its settings")
			break
		}
	}
	if v, ok := levels["type"]; ok {
		if name, ok := v.(string); ok {
			words = append(words, "type "+name)
		} else {
			words = append(words, "type inherit")
		}
	}
	return strings.Join(words, ", ")
}

// routerLevel is the answer of GET and PUT /v1/settings.
type routerLevel struct {
	Level    string            `json:"level"`
	Settings fleet.RouterLevel `json:"settings"`
}

func (x *run) routerSettings() error {
	if err := x.require("workloads"); err != nil {
		return err
	}
	if len(x.r.Levels) == 0 {
		return x.showRouterSettings()
	}
	if x.r.DryRun {
		return x.routerDryRunOf(http.MethodPut, "/v1/settings", x.r.Levels)
	}
	by, err := x.d.Google.Tokens.Account(x.ctx)
	if err != nil {
		return err
	}
	in := map[string]any{"by": by}
	for k, v := range x.r.Levels {
		in[k] = v
	}
	var raw json.RawMessage
	if err := x.withLevels(http.MethodPut, "/v1/settings", in, &raw); err != nil {
		return err
	}
	if x.r.JSON {
		return x.printByRouter(raw)
	}
	var a routerLevel
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("the controller's answer is not the router's settings: %w", err)
	}
	var out []string
	if _, ok := x.r.Levels["defaults"]; ok {
		out = append(out, "workload settings set")
	}
	if _, ok := x.r.Levels["hibernate"]; ok {
		out = append(out, "hibernate "+hibernateWord(a.Settings.Hibernate))
	}
	if _, ok := x.r.Levels["joinMinutes"]; ok {
		if a.Settings.JoinMinutes == nil {
			out = append(out, "join windows inherit")
		} else {
			out = append(out, schedule.JoinText(*a.Settings.JoinMinutes))
		}
	}
	x.say("%s: %s", x.n.managed, strings.Join(out, ", "))
	return nil
}

func (x *run) showRouterSettings() error {
	var raw json.RawMessage
	if err := x.withLevels(http.MethodGet, "/v1/settings", nil, &raw); err != nil {
		return err
	}
	if x.r.JSON {
		// AWS's: the router's settings by its name.
		var a struct {
			Settings json.RawMessage `json:"settings"`
		}
		if err := json.Unmarshal(raw, &a); err != nil || len(a.Settings) == 0 {
			return fmt.Errorf("the controller's answer is not the router's settings: %s", raw)
		}
		return x.printByRouter(a.Settings)
	}
	var a routerLevel
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("the controller's answer is not the router's settings: %w", err)
	}
	// AWS's words, as `dormeto aws routers settings` shows a router's level.
	fmt.Fprint(x.d.Out, a.Settings.Text(x.n.managed, x.d.Now().Location()))
	return nil
}

// levelDetails prints a workload's settings each with the level it comes from, then its type, hibernation and rules.
func (x *run) levelDetails(w workload) {
	if t := w.Type.Text(); t != "" {
		x.say("  type: %s", t)
	}
	for _, s := range fleet.LevelSettings {
		if n, ok := w.Settings[s.Key].(float64); ok {
			x.say("  %s: %s (%s)", s.Label, s.Format(int(n)), w.sourceText(s.Key))
		}
	}
	if m, ok := w.Settings["wakeMode"].(string); ok {
		x.say("  wake mode: %s (%s)", m, w.sourceText("wakeMode"))
	}
	if w.Hibernation != nil {
		x.say("  hibernate: %s", w.Hibernation.Text())
	}
	if len(w.FallbackInstanceTypes) > 0 {
		x.say("  fallback machine types: %s", strings.Join(w.FallbackInstanceTypes, ", "))
	}
	if vms := w.onFallback(); len(vms) > 0 {
		x.say("  running on a fallback type: %s", strings.Join(vms, ", "))
	}
	for _, r := range []struct {
		label string
		rules []describedRule
	}{{"no-wake rules", w.NoWakeRules}, {"wake rules", w.WakeRules}} {
		if len(r.rules) == 0 {
			x.say("  %s: none", r.label)
			continue
		}
		x.say("  %s:", r.label)
		for _, rule := range r.rules {
			x.say("    %s (%s)", fleet.RuleText(rule.NoWake), rule.level())
		}
	}
}

// sourceText is the level a setting comes from, in AWS's words (fleet.SourceText).
func (w workload) sourceText(key string) string {
	var name *string
	if w.Type != nil {
		name = w.Type.Name
	}
	return fleet.SourceText(w.SettingsFrom[key], name)
}
