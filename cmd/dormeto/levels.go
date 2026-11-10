package main

// Settings at scale (vision settings at scale design §10): the workload settings of a level, the same flags at the
// organization (`settings set`), a router (`routers settings`), a workload (`workloads settings`) and a type (`types set`),
// inherit unsetting a value on every level (default, its old name, too at the organization's and the types'), and the
// organization's types.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/spf13/cobra"
)

// levelSettings3 are the numeric settings of a level (shared with the Google Cloud commands through fleet).
var levelSettings3 = fleet.LevelSettings

// builtInTypes are the types derived from a workload's backends.
var builtInTypes = []string{"instance", "asg", "ecs", "k8s", "databases"}

var typeName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// levelFlags are the flags of one level; unset is the word that leaves a setting to the levels below (inherit, on every
// level), aliases the other words read as it (default, its old name at the organization's and the types').
type levelFlags struct {
	unset     string
	aliases   []string
	numbers   map[string]*string
	wakeMode  string
	noWake    string
	wakeRules string
	// max overrides a numeric setting's upper end by its flag: Google Cloud's wake timeout stops at 1700.
	max map[string]int
}

// rangeOf is a numeric setting's range as this command takes it.
func (l *levelFlags) rangeOf(s fleet.LevelSetting) (int, int) {
	if m, ok := l.max[s.Flag]; ok {
		return s.Min, m
	}
	return s.Min, s.Max
}

// onGoogleCloud is what a setting's flag says of Google Cloud's limit, where it differs: the wake timeout stops at 1700,
// and the hold, the held requests and the drain are 0 (the Google Cloud commands say their own).
var onGoogleCloud = map[string]string{
	"wake-timeout-seconds": " (60 to 1700 on Google Cloud)",
	"wait-seconds":         " (fixed at 0 on Google Cloud: give inherit, any value is refused)",
	"max-holders":          " (fixed at 0 on Google Cloud: give inherit, any value is refused)",
	"drain-seconds":        " (fixed at 0 on Google Cloud: give inherit, any value is refused)",
}

func bindLevelFlags(cmd *cobra.Command, unset string, aliases ...string) *levelFlags {
	l := &levelFlags{unset: unset, aliases: aliases, numbers: map[string]*string{}}
	for _, s := range levelSettings3 {
		v := new(string)
		l.numbers[s.Flag] = v
		cmd.Flags().StringVar(v, s.Flag, "", fmt.Sprintf("the %s, %d to %d%s, or %s", s.Label, s.Min, s.Max, onGoogleCloud[s.Flag], unset))
	}
	cmd.Flags().StringVar(&l.wakeMode, "wake-mode", "", "any, browser, or "+unset)
	cmd.Flags().StringVar(&l.noWake, "no-wake-rules-file", "", "a JSON list of no-wake rules (a path, - for the standard input), or "+unset)
	cmd.Flags().StringVar(&l.wakeRules, "wake-rules-file", "", "a JSON list of wake rules (a path, - for the standard input), or "+unset)
	return l
}

// unsets tells whether a value is the unset word, or one of its aliases.
func (l *levelFlags) unsets(v string) bool { return v == l.unset || slices.Contains(l.aliases, v) }

// body reads the flags given, each refused before any call: a value in its range, or the unset word for null.
func (l *levelFlags) body(c *cobra.Command) (map[string]any, error) {
	out := map[string]any{}
	for _, s := range levelSettings3 {
		if !c.Flags().Changed(s.Flag) {
			continue
		}
		v := strings.TrimSpace(*l.numbers[s.Flag])
		if l.unsets(v) {
			out[s.Key] = nil
			continue
		}
		n, err := strconv.Atoi(v)
		lo, hi := l.rangeOf(s)
		if err != nil || n < lo || n > hi {
			return nil, usagef("--%s: give a whole number from %d to %d, or %s", s.Flag, lo, hi, l.unset)
		}
		out[s.Key] = n
	}
	if c.Flags().Changed("wake-mode") {
		switch {
		case l.wakeMode == "any" || l.wakeMode == "browser":
			out["wakeMode"] = l.wakeMode
		case l.unsets(l.wakeMode):
			out["wakeMode"] = nil
		default:
			return nil, usagef("--wake-mode: give any, browser or %s", l.unset)
		}
	}
	for _, r := range []struct{ flag, key, value string }{{"no-wake-rules-file", "noWakeRules", l.noWake}, {"wake-rules-file", "wakeRules", l.wakeRules}} {
		if !c.Flags().Changed(r.flag) {
			continue
		}
		if l.unsets(r.value) {
			out[r.key] = nil
			continue
		}
		rules, err := readRules(c, r.value)
		if err != nil {
			return nil, usagef("--%s: %v", r.flag, err)
		}
		out[r.key] = rules
	}
	return out, nil
}

// readRules reads a JSON list of rules from a file, or the standard input for -; the router checks each rule.
func readRules(c *cobra.Command, path string) ([]any, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(c.InOrStdin())
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	var rules []any
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("a JSON list of rules: %v", err)
	}
	return rules, nil
}

// levelLines says the settings a level sets, one each, in the order of the flags.
func levelLines(d map[string]any) []string { return fleet.LevelLines(d) }

// defaultsLine is the organization's workload settings in `settings show|set`.
func defaultsLine(raw json.RawMessage) string {
	var a struct {
		Defaults *map[string]any `json:"defaults"`
	}
	if json.Unmarshal(raw, &a) != nil || a.Defaults == nil {
		return ""
	}
	lines := levelLines(*a.Defaults)
	if len(lines) == 0 {
		return "Workload settings: the built-in values\n"
	}
	return "Workload settings: " + strings.Join(lines, ", ") + "\n"
}

func newTypes(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("types", "Show and set the organization's workload types")
	f.bind(cmd)
	cmd.AddCommand(newTypesList(f, deps), newTypesShow(f, deps), newTypesSet(f, deps), newTypesDelete(f, deps))
	acceptYes(cmd)
	acceptDryRun(cmd)
	return cmd
}

// typesAnswer is the manager's /settings/types, each type kept as the manager wrote it.
type typesAnswer struct {
	Types   []map[string]any `json:"types"`
	BuiltIn []string         `json:"builtIn"`
	Note    string           `json:"note"`
}

func readTypes(ctx context.Context, api *fleet.Client) (typesAnswer, error) {
	var a typesAnswer
	if err := api.Get(ctx, "/settings/types", nil, &a); err != nil {
		if fleet.IsNotFound(err) {
			return a, fmt.Errorf("workload types need manager 0.22.0 or later")
		}
		return a, err
	}
	if len(a.BuiltIn) == 0 {
		a.BuiltIn = builtInTypes
	}
	return a, nil
}

// typeText is one type: its name, its selector and its defaults.
func typeText(name string, t map[string]any) string {
	var tags []string
	if m, ok := t["tags"].(map[string]any); ok {
		for k, v := range m {
			tags = append(tags, fmt.Sprintf("%s=%v", k, v))
		}
		sort.Strings(tags)
	}
	selector := "built-in"
	if !slices.Contains(builtInTypes, name) {
		selector = strings.Join(tags, ", ")
	}
	settings, _ := t["settings"].(map[string]any)
	lines := levelLines(settings)
	if len(lines) == 0 {
		lines = []string{"no defaults"}
	}
	return fmt.Sprintf("%s (%s): %s", name, selector, strings.Join(lines, ", "))
}

func newTypesList(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "list", Short: "List the workload types: the custom ones in order, then the built-in ones", Args: noArgs, SilenceErrors: true,
		Long: "List the organization's workload types. A workload's type is its own (set on it), else the first custom type\n" +
			"whose tags it has, else the built-in one its backends give (instance, asg, ecs, k8s, or databases with an RDS or\n" +
			"Aurora companion). A type's defaults come after the workload's own values, before its router's. --json prints\n" +
			"the manager's answer.",
		RunE: func(c *cobra.Command, _ []string) error {
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				a, err := readTypes(ctx, api)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), a)
				}
				out := c.OutOrStdout()
				for _, t := range a.Types {
					if name, _ := t["name"].(string); !slices.Contains(a.BuiltIn, name) {
						fmt.Fprintln(out, typeText(name, t))
					}
				}
				for _, name := range a.BuiltIn {
					entry := map[string]any{}
					for _, t := range a.Types {
						if t["name"] == name {
							entry = t
						}
					}
					fmt.Fprintln(out, typeText(name, entry))
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newTypesShow(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show <name>", Short: "Show a workload type", Args: cobra.ExactArgs(1), SilenceErrors: true,
		Long: "Show a workload type: its selector (its tags, or built-in), its defaults and its description. --json prints the\n" +
			"type as the manager holds it ({name, tags, settings, description}) and builtIn.",
		RunE: func(c *cobra.Command, args []string) error {
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				a, err := readTypes(ctx, api)
				if err != nil {
					return err
				}
				entry := map[string]any(nil)
				for _, t := range a.Types {
					if t["name"] == args[0] {
						entry = t
					}
				}
				builtIn := slices.Contains(a.BuiltIn, args[0])
				switch {
				case entry == nil && !builtIn:
					return usagef("no type %s", args[0])
				case entry == nil:
					entry = map[string]any{"name": args[0], "settings": map[string]any{}}
				}
				if asJSON {
					shown := maps.Clone(entry)
					shown["builtIn"] = builtIn
					if shown["settings"] == nil {
						shown["settings"] = map[string]any{}
					}
					return printJSON(c.OutOrStdout(), shown)
				}
				fmt.Fprintln(c.OutOrStdout(), typeText(args[0], entry))
				if d, ok := entry["description"].(string); ok && d != "" {
					fmt.Fprintln(c.OutOrStdout(), d)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the type as the manager holds it, and builtIn")
	return cmd
}

func newTypesSet(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		description string
		tags        []string
	)
	cmd := &cobra.Command{
		Use: "set <name> [--description <text>] [--tag k=v]... [--idle-minutes <minutes>|inherit] ...", Short: "Add or change a workload type",
		Args: cobra.ExactArgs(1), SilenceErrors: true,
		Long: "Add a custom type (lower-case letters, digits and hyphens, at least one --tag that selects it), or change one:\n" +
			"its description, its tags (given, they replace the old ones), its defaults (each given value replaces the one held,\n" +
			"inherit removes it, as on every level; default, its old name, too). A built-in type (instance, asg, ecs, k8s,\n" +
			"databases) takes defaults only. The routers apply it at their next check, within 5 minutes. Needs an administrator.",
	}
	levels := bindLevelFlags(cmd, "inherit", "default")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		name := args[0]
		builtIn := slices.Contains(builtInTypes, name)
		defaults, err := levels.body(c)
		var selector map[string]any
		if err == nil && len(tags) > 0 {
			if builtIn {
				err = usagef("%s is a built-in type: it takes defaults, no tags", name)
			} else {
				selector = map[string]any{}
				for _, t := range tags {
					k, v, ok := strings.Cut(t, "=")
					if !ok || strings.TrimSpace(k) == "" {
						err = usagef("--tag: give key=value")
						break
					}
					selector[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
		}
		if err == nil && !builtIn && !typeName.MatchString(name) {
			err = usagef("a type's name is lower-case letters, digits and hyphens, starting with a letter (at most 32)")
		}
		if err != nil {
			return finish(c, err)
		}
		return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
			a, err := readTypes(ctx, api)
			if err != nil {
				return err
			}
			list := a.Types
			at := slices.IndexFunc(list, func(t map[string]any) bool { return t["name"] == name })
			if at < 0 {
				if !builtIn && selector == nil {
					return usagef("a new type needs at least one --tag that selects it")
				}
				list = append(list, map[string]any{"name": name, "settings": map[string]any{}})
				at = len(list) - 1
			}
			entry := list[at]
			settings, _ := entry["settings"].(map[string]any)
			if settings == nil {
				settings = map[string]any{}
			}
			for k, v := range defaults {
				if v == nil {
					delete(settings, k)
				} else {
					settings[k] = v
				}
			}
			entry["settings"] = settings
			if selector != nil {
				entry["tags"] = selector
			}
			if c.Flags().Changed("description") {
				if description == "" {
					delete(entry, "description")
				} else {
					entry["description"] = description
				}
			}
			if builtIn && len(settings) == 0 {
				list = slices.Delete(list, at, at+1)
			}
			return putTypes(ctx, c, api, list, "type "+name+" saved")
		})
	}
	cmd.Flags().StringVar(&description, "description", "", "what the type is for (empty removes it)")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "a tag that selects the type, key=value (repeat it)")
	bindTypesJSON(cmd)
	return cmd
}

// bindTypesJSON binds --json of types set and delete: the manager's answer, {types, builtIn, note}, the line on stderr.
func bindTypesJSON(cmd *cobra.Command) {
	cmd.Flags().Bool("json", false, "print the manager's answer, {types, builtIn, note} (the line goes to stderr)")
}

func newTypesDelete(f *fleetFlags, deps fleetDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use: "delete <name>", Short: "Remove a custom workload type", Args: cobra.ExactArgs(1), SilenceErrors: true,
		Long: "Remove a custom type: its workloads take the next type their tags select, else their built-in one. A built-in\n" +
			"type stays: set its defaults to inherit instead. Needs an administrator.",
		RunE: func(c *cobra.Command, args []string) error {
			name := args[0]
			if slices.Contains(builtInTypes, name) {
				return finish(c, usagef("%s is a built-in type: set its defaults to inherit instead", name))
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				a, err := readTypes(ctx, api)
				if err != nil {
					return err
				}
				at := slices.IndexFunc(a.Types, func(t map[string]any) bool { return t["name"] == name })
				if at < 0 {
					return usagef("no type %s", name)
				}
				return putTypes(ctx, c, api, slices.Delete(a.Types, at, at+1), "type "+name+" removed")
			})
		},
	}
	bindTypesJSON(cmd)
	return cmd
}

// putTypes sends the list and says done with the manager's note; under --json, the manager's answer, the line on stderr.
func putTypes(ctx context.Context, c *cobra.Command, api *fleet.Client, list []map[string]any, done string) error {
	if list == nil {
		list = []map[string]any{}
	}
	var raw json.RawMessage
	if err := api.Put(ctx, "/settings/types", map[string]any{"types": list}, &raw); err != nil {
		return err
	}
	var a typesAnswer
	_ = json.Unmarshal(raw, &a)
	fmt.Fprintf(textOut(c), "%s: %s\n", done, a.Note)
	if jsonFlag(c) {
		return printJSON(c.OutOrStdout(), orRaw(raw))
	}
	return nil
}
