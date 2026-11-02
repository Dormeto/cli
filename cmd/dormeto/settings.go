package main

// `dormeto aws settings show|set`, `routers settings` and `workloads settings`: the hibernation setting of the manager's
// workloads, at the three levels (the manager's default for every router, one router, one workload), and the join of
// windows at the first two (a workload's is on its schedule), through the manager's /iam routes. Exit codes as the fleet
// commands.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/schedule"
)

// parseHibernate reads a --hibernate value: true or false, and inherit (nil) where inherit is allowed.
func parseHibernate(s string, inherit bool) (*bool, error) {
	switch {
	case s == "true" || s == "false":
		b := s == "true"
		return &b, nil
	case s == "inherit" && inherit:
		return nil, nil
	case inherit:
		return nil, usagef("--hibernate: give true, false or inherit")
	}
	return nil, usagef("--hibernate: give true or false")
}

// hibernateWord is a value as it is printed: on, off, or inherit for nil.
func hibernateWord(b *bool) string {
	if b == nil {
		return "inherit"
	}
	return fleet.OnOff(*b)
}

// levelSettings is what --hibernate and --join-minutes ask of a level. Only what is given is sent: an absent key stays as
// it was, so there is always a key, and never a {} that changes nothing.
type levelSettings struct {
	body    map[string]any
	words   []string // "hibernate to on", "join minutes to 30"
	joining bool
	join    *int // nil to inherit
}

// readLevelSettings reads the two flags, refused before any call; inherit is whether --hibernate takes inherit.
func readLevelSettings(c *cobra.Command, hibernate, join string, inherit bool) (levelSettings, error) {
	l := levelSettings{body: map[string]any{}}
	if c.Flags().Changed("hibernate") {
		value, err := parseHibernate(hibernate, inherit)
		if err != nil {
			return l, err
		}
		l.body["hibernate"] = value
		l.words = append(l.words, "hibernate to "+hibernateWord(value))
	}
	if c.Flags().Changed("join-minutes") {
		value, err := schedule.ParseJoinMinutes(join)
		if err != nil {
			return l, usagef("--join-minutes: give %v", err)
		}
		l.body["joinMinutes"] = value
		l.joining, l.join = true, value
		l.words = append(l.words, "join minutes to "+joinWord(value))
	}
	if len(l.body) == 0 {
		return l, usagef("give --hibernate or --join-minutes")
	}
	return l, nil
}

// joinWord is a value of --join-minutes as it was given.
func joinWord(minutes *int) string {
	if minutes == nil {
		return "inherit"
	}
	return fmt.Sprint(*minutes)
}

// applied is whether an answer holds the minutes asked: a manager 0.20.0 passes hibernate on and drops the rest.
func (l levelSettings) applied(answer *int) bool {
	return !l.joining || l.join == nil || (answer != nil && *answer == *l.join)
}

// settingsRefusal says what a refusal of a level's settings means when the manager or the router is too old. A manager
// 0.20.0 asks for hibernate; it is checked here, so that refusal is only of a --join-minutes without it.
func settingsRefusal(err error) error {
	var ae *fleet.APIError
	if errors.As(err, &ae) && ae.Status == http.StatusBadRequest && strings.HasPrefix(ae.Message, "hibernate must be") {
		return fmt.Errorf("--join-minutes needs manager 0.21.0 or later (%s)", ae.Message)
	}
	return needsRecurrenceRouter(needsManager019(err))
}

// globalSettings is the answer of GET and PUT /settings/workloads.
type globalSettings struct {
	Hibernate       bool            `json:"hibernate"`
	JoinMinutes     json.RawMessage `json:"joinMinutes"`     // null when unset, empty from a manager that does not have it
	EndedWindowDays json.RawMessage `json:"endedWindowDays"` // the same, manager 0.21.0
	UpdatedAt       *string         `json:"updatedAt"`
	UpdatedBy       *string         `json:"updatedBy"`
	Note            string          `json:"note"`
}

// endedDaysDefault is how long after its end a window stays in its schedule when the organization does not say (router 0.36.0).
const endedDaysDefault = 365

// parseEndedDays reads --ended-window-days: 0 keeps ended windows, 30 to 3650 days, default (nil) unsets it.
func parseEndedDays(s string) (*int, error) {
	if s == "default" {
		return nil, nil
	}
	days, err := strconv.Atoi(s)
	if err != nil || (days != 0 && (days < 30 || days > 3650)) {
		return nil, usagef("--ended-window-days: give 0 to keep ended windows, 30 to 3650 days, or default (%d)", endedDaysDefault)
	}
	return &days, nil
}

// endedLine is "Ended windows: removed 90 days after their end\n", "" for a manager that does not answer it.
func (g globalSettings) endedLine() string {
	if len(g.EndedWindowDays) == 0 {
		return ""
	}
	var days *int
	if json.Unmarshal(g.EndedWindowDays, &days) != nil || days == nil {
		return fmt.Sprintf("Ended windows: not set (removed %d days after their end by default)\n", endedDaysDefault)
	}
	if *days == 0 {
		return "Ended windows: kept\n"
	}
	return fmt.Sprintf("Ended windows: removed %d days after their end\n", *days)
}

// joinLine is "Join windows: up to 30 min apart\n", "" for a manager that does not answer it.
func (g globalSettings) joinLine() string {
	if len(g.JoinMinutes) == 0 {
		return ""
	}
	if minutes := g.joinMinutes(); minutes != nil {
		return "Join windows: " + schedule.JoinGap(*minutes) + "\n"
	}
	return "Join windows: not set (" + schedule.JoinGap(schedule.JoinDefault) + " by default)\n"
}

func (g globalSettings) joinMinutes() *int {
	var minutes *int
	if json.Unmarshal(g.JoinMinutes, &minutes) != nil {
		return nil
	}
	return minutes
}

// line is "Hibernate: on (changed by ann at 2026-10-19 10:00 CEST)", or "(never changed)".
func (g globalSettings) line(loc *time.Location) string {
	s := "Hibernate: " + fleet.OnOff(g.Hibernate)
	if g.UpdatedAt == nil && g.UpdatedBy == nil {
		return s + " (never changed)"
	}
	var by []string
	if g.UpdatedBy != nil {
		by = append(by, "by "+*g.UpdatedBy)
	}
	if g.UpdatedAt != nil {
		by = append(by, "at "+fleet.When(*g.UpdatedAt, loc))
	}
	return s + " (changed " + strings.Join(by, " ") + ")"
}

func newSettings(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("settings", "Show and set the manager's defaults for every router's workloads")
	f.bind(cmd)
	cmd.AddCommand(newSettingsShow(f, deps), newSettingsSet(f, deps), newSettingsWaitingPage(f, deps))
	return cmd
}

func newSettingsShow(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show", Short: "Show the manager's defaults for the workloads", Args: noArgs, SilenceErrors: true,
		Long: "Show whether the EC2 instances launched with hibernation are hibernated, not stopped, when a workload naps, by\n" +
			"default in every router that reads the manager's settings, and who changed it and when, the largest gap that\n" +
			"joins two schedule windows of one kind, and how long an ended window stays. --json prints the manager's answer.",
		RunE: func(c *cobra.Command, _ []string) error {
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var raw json.RawMessage
				if err := api.Get(ctx, "/settings/workloads", nil, &raw); err != nil {
					return needsManager019(err)
				}
				return printRead(c, raw, asJSON, func(g globalSettings) string {
					return g.line(deps.now().Location()) + "\n" + g.joinLine() + g.endedLine()
				})
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newSettingsSet(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		hibernate, join, ended string
		asJSON                 bool
	)
	cmd := &cobra.Command{
		Use: "set [--hibernate true|false] [--join-minutes <minutes>|inherit] [--ended-window-days <days>|default]", Short: "Set the manager's defaults for the workloads", Args: noArgs, SilenceErrors: true,
		Long: "Set whether the EC2 instances launched with hibernation are hibernated, not stopped, when a workload naps: true\n" +
			"or false by default, for every router that reads the manager's settings and does not set its own, from its next\n" +
			"check (within 5 minutes). Every other instance is stopped as before. A router's or a workload's own setting wins\n" +
			"over it. --join-minutes is the largest gap that joins two schedule windows of one kind, 0 to 120, a multiple of\n" +
			"5, 0 never joins; inherit unsets it, and the default is 15. --ended-window-days is how long after its end date a\n" +
			"window stays in its schedule before routers 0.36.0 remove it, with the dates without a window as old: 30 to 3650\n" +
			"days, 0 keeps them, default unsets it (365). What is not given stays as it was. Needs an administrator. --json\n" +
			"prints the manager's answer.",
		RunE: func(c *cobra.Command, _ []string) error {
			ending := c.Flags().Changed("ended-window-days")
			settings := levelSettings{body: map[string]any{}}
			var err error
			if c.Flags().Changed("hibernate") || c.Flags().Changed("join-minutes") || !ending {
				settings, err = readLevelSettings(c, hibernate, join, false)
			}
			if err == nil && ending {
				var days *int
				if days, err = parseEndedDays(ended); err == nil {
					settings.body["endedWindowDays"] = days
				}
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var raw json.RawMessage
				if err := api.Put(ctx, "/settings/workloads", settings.body, &raw); err != nil {
					var ae *fleet.APIError
					if !settings.joining && ending && errors.As(err, &ae) && ae.Status == http.StatusBadRequest && strings.HasPrefix(ae.Message, "hibernate must be") {
						return fmt.Errorf("--ended-window-days needs manager 0.21.0 or later (%s)", ae.Message)
					}
					return settingsRefusal(err)
				}
				err := printRead(c, raw, asJSON, func(g globalSettings) string {
					return g.line(deps.now().Location()) + "\n" + g.joinLine() + g.endedLine() + g.Note + "\n"
				})
				var g globalSettings
				if err != nil || json.Unmarshal(raw, &g) != nil {
					return err
				}
				if !settings.applied(g.joinMinutes()) {
					fmt.Fprintln(c.ErrOrStderr(), "--join-minutes was not applied: it needs manager 0.21.0 or later")
					err = exitCode(1)
				}
				if ending && len(g.EndedWindowDays) == 0 {
					fmt.Fprintln(c.ErrOrStderr(), "--ended-window-days was not applied: it needs manager 0.21.0 or later")
					err = exitCode(1)
				}
				return err
			})
		},
	}
	cmd.Flags().StringVar(&hibernate, "hibernate", "", "true or false")
	cmd.Flags().StringVar(&join, "join-minutes", "", "the largest gap that joins two windows, 0 to 120, a multiple of 5; inherit unsets it (default 15)")
	cmd.Flags().StringVar(&ended, "ended-window-days", "", "days after its end a window stays, 30 to 3650, 0 keeps them; default unsets it (365)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newWorkloadsSettings(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var (
		hibernate string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use: "settings (<router>/<workload>... | --tag k=v...) --hibernate true|false|inherit", Short: "Set the hibernation of workloads",
		Long: "Set whether the EC2 instances launched with hibernation are hibernated, not stopped, when the workloads nap: true\n" +
			"or false for the workload, inherit to follow its router's setting, then the manager's. A workload with fallback\n" +
			"instance types does not hibernate unless it is set true on the workload itself (a hibernated instance cannot\n" +
			"change type). A workload written by Terraform is refused (set hibernate in its workload module), as is one on\n" +
			"a router before 0.32.0; a refusal is printed for that workload, the others go on (exit 1). Several targets are\n" +
			"confirmed on a terminal. Needs an operator.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			err := w.targets.check(args, false)
			var value *bool
			if err == nil {
				value, err = parseHibernate(hibernate, true)
			}
			if err != nil {
				return finish(c, err)
			}
			return w.run(c, args, "hibernate "+hibernateWord(value)+": would act on", "Set hibernate to "+hibernateWord(value)+" for %s?",
				func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
					_, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
						var raw json.RawMessage
						if err := api.Put(ctx, t.Path("/settings"), map[string]*bool{"hibernate": value}, &raw); err != nil {
							return "", nil, needsManager019(err)
						}
						var a struct {
							Settings    struct{ Hibernate *bool }
							Hibernation *fleet.Hibernation
						}
						_ = json.Unmarshal(raw, &a)
						line := fmt.Sprintf("%s: hibernate %s", t, hibernateWord(a.Settings.Hibernate))
						if a.Hibernation != nil {
							line += " (effective: " + a.Hibernation.Text() + ")"
						}
						return line + "\n", raw, nil
					})
					return err
				})
		},
	}
	w.bindShared(cmd)
	cmd.Flags().StringVar(&hibernate, "hibernate", "", "true, false, or inherit (the router's, then the manager's)")
	bindJSON(cmd, &asJSON)
	return cmd
}
