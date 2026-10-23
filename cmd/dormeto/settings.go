package main

// `dormeto settings show|set`, `routers settings` and `workloads settings`: the hibernation setting of the manager's
// workloads, at the three levels (the manager's default for every router, one router, one workload), through the
// manager's /iam routes. Exit codes as the fleet commands.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
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

// globalSettings is the answer of GET and PUT /settings/workloads.
type globalSettings struct {
	Hibernate bool    `json:"hibernate"`
	UpdatedAt *string `json:"updatedAt"`
	UpdatedBy *string `json:"updatedBy"`
	Note      string  `json:"note"`
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
	cmd.AddCommand(newSettingsShow(f, deps), newSettingsSet(f, deps))
	return cmd
}

func newSettingsShow(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show", Short: "Show the manager's default hibernation of workloads", Args: noArgs, SilenceErrors: true,
		Long: "Show whether the EC2 instances of workloads are launched with hibernation by default, in every router that reads\n" +
			"the manager's settings, and who changed it and when. --json prints the manager's answer.",
		RunE: func(c *cobra.Command, _ []string) error {
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var raw json.RawMessage
				if err := api.Get(ctx, "/settings/workloads", nil, &raw); err != nil {
					return err
				}
				return printRead(c, raw, asJSON, func(g globalSettings) string { return g.line(deps.now().Location()) + "\n" })
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newSettingsSet(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		hibernate string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use: "set --hibernate true|false", Short: "Set the manager's default hibernation of workloads", Args: noArgs, SilenceErrors: true,
		Long: "Set whether the EC2 instances of workloads are launched with hibernation by default: true or false, for every\n" +
			"router that reads the manager's settings and does not set its own, from its next check (within 5 minutes). A\n" +
			"router's or a workload's own setting wins over it. Needs an administrator. --json prints the manager's answer.",
		RunE: func(c *cobra.Command, _ []string) error {
			value, err := parseHibernate(hibernate, false)
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var raw json.RawMessage
				if err := api.Put(ctx, "/settings/workloads", map[string]bool{"hibernate": *value}, &raw); err != nil {
					return err
				}
				return printRead(c, raw, asJSON, func(g globalSettings) string {
					return g.line(deps.now().Location()) + "\n" + g.Note + "\n"
				})
			})
		},
	}
	cmd.Flags().StringVar(&hibernate, "hibernate", "", "true or false (required)")
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
		Long: "Set whether the EC2 instances of the workloads are launched with hibernation and hibernated at a nap: true or\n" +
			"false for the workload, inherit to follow its router's setting, then the manager's. A workload with fallback\n" +
			"instance types never hibernates. A workload written by Terraform is refused (set hibernate in its workload\n" +
			"module), as is one on a router before 0.32.0; a refusal is printed for that workload, the others go on (exit 1).\n" +
			"Several targets are confirmed on a terminal. Needs an operator.",
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
			return w.run(c, args, "hibernate "+hibernateWord(value)+": would act on", "Set hibernate "+hibernateWord(value)+" on %s?",
				func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
					_, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
						var raw json.RawMessage
						if err := api.Put(ctx, t.Path("/settings"), map[string]*bool{"hibernate": value}, &raw); err != nil {
							return "", nil, err
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
