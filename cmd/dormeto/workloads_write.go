package main

// `dormeto workloads wake`, `nap`, `pause`, `rename` and `tags`: the manager's other writes on workloads, on the same
// targets, confirmation and exit codes as keep-awake and resume (see workloads.go). Every argument the manager refuses by
// a rule the CLI knows is refused here first (exit 2), nothing sent.

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

// maxPause is the longest a pause lasts: the manager's 366 days.
const maxPause = 366 * 24 * time.Hour

func newWorkloadsWake(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var (
		wait    bool
		timeout time.Duration
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use: "wake (<router>/<workload>... | --tag k=v...)", Short: "Wake workloads now",
		Long: "Ask the routers to wake the workloads now. --wait follows them every 5 s until all are awake (FAILED ends it\n" +
			"with the failure, exit 1; --timeout, exit 1; Ctrl-C, exit 130). Several targets are confirmed on a terminal.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			err := w.targets.check(args, false)
			if err == nil && wait && timeout <= 0 {
				err = usagef("--timeout: %s is not a positive duration", timeout)
			}
			if err != nil {
				return finish(c, err)
			}
			return w.run(c, args, "wake", "Wake %d workloads?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				ok, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
					var raw json.RawMessage
					if err := api.Post(ctx, t.Path("/wake"), nil, &raw); err != nil {
						return "", nil, err
					}
					var res struct{ State string }
					_ = json.Unmarshal(raw, &res)
					if res.State != "" {
						return fmt.Sprintf("%s: wake requested (%s)\n", t, res.State), raw, nil
					}
					return t.String() + ": wake requested\n", raw, nil
				})
				if !wait || len(ok) == 0 {
					return err
				}
				if werr := waitAwakeJSON(ctx, c, deps, api, ok, timeout, false, asJSON); werr != nil {
					return werr
				}
				return err
			})
		},
	}
	w.bindShared(cmd)
	fl := cmd.Flags()
	fl.BoolVar(&wait, "wait", false, "wait until the workloads are awake")
	fl.DurationVar(&timeout, "timeout", 15*time.Minute, "with --wait, stop waiting after this long")
	bindJSON(cmd, &asJSON)
	return cmd
}

func newWorkloadsNap(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var asJSON bool
	cmd := &cobra.Command{
		Use: "nap (<router>/<workload>... | --tag k=v...)", Short: "Ask workloads to nap now",
		Long: "Ask the routers to put the workloads to sleep now. A router refuses a workload that is paused or not awake: its\n" +
			"reason is printed for that workload, the others go on (exit 1).",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := w.targets.check(args, false); err != nil {
				return finish(c, err)
			}
			return w.run(c, args, "nap", "Put %d workloads to sleep?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				_, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
					var raw json.RawMessage
					if err := api.Post(ctx, t.Path("/nap"), nil, &raw); err != nil {
						return "", nil, err
					}
					return t.String() + ": nap requested\n", raw, nil
				})
				return err
			})
		},
	}
	w.bindShared(cmd)
	bindJSON(cmd, &asJSON)
	return cmd
}

func newWorkloadsPause(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var (
		reason, dur, until string
		keepAsleep         bool
		asJSON             bool
	)
	cmd := &cobra.Command{
		Use: "pause (<router>/<workload>... | --tag k=v...) --reason <text> [--for <duration> | --until <time>] [--keep-asleep]", Short: "Pause workloads: no automatic nap",
		Long: "Pause the workloads: the routers do not put them to sleep, and wake them first unless --keep-asleep. --for is a\n" +
			"duration up to 366 days (90m, 2h, 3d); --until is an RFC 3339 time or HH:MM today in the local time zone;\n" +
			"without either the pause lasts until `dormeto workloads resume`. --reason (1 to 256 characters) is shown in the\n" +
			"manager. Needs the nap permission, and the wake one unless --keep-asleep.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			now := deps.now()
			end, set, err := endOf(now, dur, until, maxPause, false)
			if err == nil {
				err = w.targets.check(args, false)
			}
			reason = strings.TrimSpace(reason)
			switch {
			case err != nil:
			case reason == "":
				err = usagef("--reason: a reason is needed (1 to 256 characters)")
			case utf8.RuneCountInString(reason) > 256:
				err = usagef("--reason: at most 256 characters (%d given)", utf8.RuneCountInString(reason))
			}
			if err != nil {
				return finish(c, err)
			}
			body := struct {
				Reason     string `json:"reason"`
				Until      string `json:"until,omitempty"`
				KeepAsleep bool   `json:"keepAsleep,omitempty"`
			}{Reason: reason, KeepAsleep: keepAsleep}
			lasts := "until resumed"
			if set {
				body.Until = end.UTC().Format("2006-01-02T15:04:05Z")
				lasts = "until " + end.In(now.Location()).Format("2006-01-02 15:04 MST")
			}
			return w.run(c, args, "pause "+lasts+": would act on", "Pause %d workloads?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				_, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
					var raw json.RawMessage
					if err := api.Post(ctx, t.Path("/pause"), body, &raw); err != nil {
						return "", nil, err
					}
					var res struct{ Woken []string }
					_ = json.Unmarshal(raw, &res)
					line := fmt.Sprintf("%s: paused %s", t, lasts)
					if len(res.Woken) > 0 {
						line += "; woken " + strings.Join(res.Woken, ", ")
					}
					return line + "\n", raw, nil
				})
				return err
			})
		},
	}
	w.bindShared(cmd)
	fl := cmd.Flags()
	fl.StringVar(&reason, "reason", "", "why, 1 to 256 characters, shown in the manager (required)")
	fl.StringVar(&dur, "for", "", "how long, a duration up to 366 days (90m, 2h, 3d); default until resumed")
	fl.StringVar(&until, "until", "", "until this time: RFC 3339, or HH:MM today in the local time zone")
	fl.BoolVar(&keepAsleep, "keep-asleep", false, "do not wake the workloads first")
	bindJSON(cmd, &asJSON)
	return cmd
}

func newWorkloadsRename(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var asJSON bool
	cmd := &cobra.Command{
		Use: "rename <router>/<workload> <name>", Short: "Set the display name of a workload",
		Long: "Set the display name of one workload (1 to 64 characters, no control characters; routers 0.20.0 and later;\n" +
			"operators and administrators). A workload written by Terraform is refused by the router.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 2 {
				return finish(c, usagef("name exactly one target (<router>/<workload id>) and the new name"))
			}
			name := strings.TrimSpace(args[1])
			err := w.targets.check(args[:1], true)
			switch {
			case err != nil:
			case name == "":
				err = usagef("name: required")
			case utf8.RuneCountInString(name) > 64:
				err = usagef("name: at most 64 characters (%d given)", utf8.RuneCountInString(name))
			case strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
				err = usagef("name: no control characters")
			}
			if err != nil {
				return finish(c, err)
			}
			return w.run(c, args[:1], fmt.Sprintf("rename to %q: would act on", name), "Rename %d workloads?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				_, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
					var raw json.RawMessage
					if err := api.Post(ctx, t.Path("/rename"), map[string]string{"name": name}, &raw); err != nil {
						return "", nil, err
					}
					var res struct{ Name string }
					_ = json.Unmarshal(raw, &res)
					return fmt.Sprintf("%s: renamed to %s\n", t, orDefault(res.Name, name)), raw, nil
				})
				return err
			})
		},
	}
	w.bindConfirmation(cmd)
	bindJSON(cmd, &asJSON)
	return cmd
}

// tagEdit is what `tags` does to each workload's tags: emptied by clear, less unset, plus set.
type tagEdit struct {
	set   map[string]string
	unset []string
	clear bool
}

// apply gives the edited tags of current (a new map).
func (e tagEdit) apply(current map[string]string) map[string]string {
	out := map[string]string{}
	if !e.clear {
		maps.Copy(out, current)
	}
	for _, k := range e.unset {
		delete(out, k)
	}
	maps.Copy(out, e.set)
	return out
}

// tagsText is tags as k=v, sorted, or "no tags".
func tagsText(tags map[string]string) string {
	if len(tags) == 0 {
		return "no tags"
	}
	parts := make([]string, 0, len(tags))
	for _, k := range slices.Sorted(maps.Keys(tags)) {
		parts = append(parts, k+"="+tags[k])
	}
	return strings.Join(parts, ", ")
}

// parseSet reads --set values, key=value: the value may hold "=", a key is given once.
func parseSet(values []string) (map[string]string, error) { return parseTagFlag("--set", values) }

// parseTagFlag is parseSet for the flag of that name (--set-tag of onboard): tags given as key=value, checked as the
// manager checks them.
func parseTagFlag(flag string, values []string) (map[string]string, error) {
	out := map[string]string{}
	for _, v := range values {
		k, val, ok := strings.Cut(v, "=")
		if !ok || k == "" {
			return nil, usagef("%s: %q is not key=value", flag, v)
		}
		if _, dup := out[k]; dup {
			return nil, usagef("%s: key %q given twice", flag, k)
		}
		out[k] = val
	}
	if p := fleet.TagsProblem(out); p != "" {
		return nil, usagef("%s: %s", flag, p)
	}
	return out, nil
}

func newWorkloadsTags(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var (
		sets, unsets []string
		clear        bool
		asJSON       bool
	)
	cmd := &cobra.Command{
		Use: "tags (<router>/<workload>... | --tag k=v...) [--set k=v]... [--unset k]... [--clear]", Short: "Change the tags of workloads",
		Long: "Change the tags of the workloads: each one's tags are read, emptied by --clear, less every --unset key, plus\n" +
			"every --set, and sent only when they changed (\"unchanged\" otherwise). Tags are given by flags alone, a key may\n" +
			"hold \"/\". The manager's rules are checked first: at most 50 tags, a key of 1 to 128 characters, a value of up\n" +
			"to 256, letters, digits, spaces and _ . : / = + - @, no key starting with dormeto:. Routers 0.30.0 and later;\n" +
			"administrators. A refusal of one workload is printed for it, the others go on (exit 1). --json prints the\n" +
			"manager's answers by <router>/<workload>, each {workloadId, tags}: the tags as they are for one unchanged.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			err := w.targets.check(args, false)
			var set map[string]string
			if err == nil {
				set, err = parseSet(sets)
			}
			switch {
			case err != nil:
			case len(sets) == 0 && len(unsets) == 0 && !clear:
				err = usagef("give at least one of --set, --unset and --clear")
			case slices.Contains(unsets, ""):
				err = usagef("--unset: a key is needed")
			}
			for _, k := range unsets {
				if err == nil && set != nil {
					if _, both := set[k]; both {
						err = usagef("key %q is both --set and --unset", k)
					}
				}
			}
			if err != nil {
				return finish(c, err)
			}
			edit := tagEdit{set: set, unset: unsets, clear: clear}
			return w.run(c, args, "change the tags of", "Change the tags of %d workloads?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				// Read and check every workload before sending any, so that a refused result sends nothing.
				type plan struct {
					tags    map[string]string
					changed bool
					err     error
				}
				plans := map[fleet.Target]plan{}
				for _, t := range targets {
					var st struct{ Tags map[string]string }
					if err := api.Get(ctx, t.Path(""), nil, &st); err != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						plans[t] = plan{err: err}
						continue
					}
					next := edit.apply(st.Tags)
					changed := !maps.Equal(next, nonNil(st.Tags))
					if p := fleet.TagsProblem(next); changed && p != "" {
						return usagef("%s: %s", t, p)
					}
					plans[t] = plan{tags: next, changed: changed}
				}
				_, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
					p := plans[t]
					switch {
					case p.err != nil:
						return "", nil, p.err
					case !p.changed: // no PUT: the answer is the PUT's shape, the tags as they are
						current, _ := json.Marshal(map[string]any{"workloadId": t.ID, "tags": p.tags})
						return t.String() + ": unchanged\n", current, nil
					}
					var raw json.RawMessage
					if err := api.Put(ctx, t.Path("/tags"), map[string]map[string]string{"tags": p.tags}, &raw); err != nil {
						return "", nil, err
					}
					return fmt.Sprintf("%s: %s\n", t, tagsText(p.tags)), raw, nil
				})
				return err
			})
		},
	}
	w.bindShared(cmd)
	fl := cmd.Flags()
	fl.StringArrayVar(&sets, "set", nil, "set this tag, key=value (repeatable)")
	fl.StringArrayVar(&unsets, "unset", nil, "remove this tag, by key (repeatable)")
	fl.BoolVar(&clear, "clear", false, "remove every tag first (--set then adds)")
	bindJSON(cmd, &asJSON)
	return cmd
}

// nonNil is m, or an empty map when nil.
func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
