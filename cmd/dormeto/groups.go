package main

// `dormeto groups`: the manager's custom groups and their grants (list, show, create, update, delete). Deleting is
// always confirmed on a terminal (--yes without one).

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

const grantHelp = "--grant <tags>:<permissions> (repeat for several, at most 20): the permissions read, wake and nap, comma-separated,\n" +
	"on the workloads that carry all the tags, key=value comma-separated (none: every workload), such as\n" +
	"team=payments,env=dev:read,wake or :read."

func newGroups(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("groups", "List, show, create, update and delete the custom groups")
	f.bind(cmd)
	cmd.AddCommand(newGroupsList(f, deps), newGroupsShow(f, deps), newGroupsCreate(f, deps), newGroupsUpdate(f, deps), newGroupsDelete(f, deps))
	return cmd
}

func groupPath(name string) string { return "/groups/" + url.PathEscape(name) }

// groupAnswer prints a group the manager answered ({group}) or says it as text.
func groupAnswer(c *cobra.Command, asJSON bool, raw []byte, verb, name string) error {
	return printAnswer(c, asJSON, raw, func(answer) string { return fmt.Sprintf("%s group %s", verb, name) })
}

func newGroupsList(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "list", Short: "List the custom groups and their grants", Args: noArgs, SilenceErrors: true,
		Long: "List the custom groups: name, description, how many grants and the age of the last change. --json prints the\n" +
			"manager's answer, {groups}. Needs an administrator.",
		RunE: func(c *cobra.Command, _ []string) error {
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				gs, err := fleet.ListGroups(ctx, api)
				if err != nil {
					return err
				}
				if asJSON {
					if gs == nil {
						gs = []fleet.Group{}
					}
					return printJSON(c.OutOrStdout(), map[string]any{"groups": gs})
				}
				fmt.Fprint(c.OutOrStdout(), fleet.GroupsText(gs, deps.now()))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer, {groups}")
	return cmd
}

func newGroupsShow(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show <name>", Short: "Show a group: description, last change and every grant", Args: argCount(1, 1, "<name>"), SilenceErrors: true,
		Long: "Show a custom group: its description, who changed it last and when (local time zone), and each grant. --json prints the manager's row. Needs an administrator.",
		RunE: func(c *cobra.Command, args []string) error {
			name := args[0]
			if strings.TrimSpace(name) == "" {
				return finish(c, usagef("the group name is empty"))
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				gs, err := fleet.ListGroups(ctx, api)
				if err != nil {
					return err
				}
				for _, g := range gs {
					if g.Name == name {
						if asJSON {
							return printJSON(c.OutOrStdout(), g)
						}
						fmt.Fprint(c.OutOrStdout(), fleet.GroupText(g, deps.now().Location()))
						return nil
					}
				}
				return fmt.Errorf("no group %s", name)
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's row")
	return cmd
}

func newGroupsCreate(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		description string
		grants      []string
		asJSON      bool
	)
	cmd := &cobra.Command{
		Use: "create <name> [--description <text>] [--grant <tags>:<permissions>]...", Short: "Create a custom group", Args: argCount(1, 1, "<name>"), SilenceErrors: true,
		Long: "Create a custom group: its name is 1 to 48 lower-case letters, digits and hyphens, not starting with dmt-; its\n" +
			"description at most 200 characters. " + grantHelp + " Needs an administrator.",
		RunE: func(c *cobra.Command, args []string) error {
			name := args[0]
			err := fleet.CheckGroupName(name)
			if err == nil {
				err = fleet.CheckDescription(description)
			}
			var gs []fleet.Grant
			if err == nil {
				gs, err = fleet.ParseGrants(grants)
			}
			if err != nil {
				return finish(c, usagef("%v", err))
			}
			body := map[string]any{"name": name, "description": description, "grants": gs}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				raw, err := callAPI(ctx, api, "POST", "/groups", body)
				if err != nil {
					return err
				}
				return groupAnswer(c, asJSON, raw, "created", name)
			})
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "what the group is for, at most 200 characters")
	cmd.Flags().StringArrayVar(&grants, "grant", nil, "<tags>:<permissions>, such as team=payments:read,wake; repeat for several")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newGroupsUpdate(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		description string
		grants      []string
		noGrants    bool
		asJSON      bool
	)
	cmd := &cobra.Command{
		Use: "update <name> [--description <text>] [--grant <tags>:<permissions>]... [--no-grants]", Short: "Change a group's description or replace its grants",
		Args: argCount(1, 1, "<name>"), SilenceErrors: true,
		Long: "Change a custom group: give at least one of --description, --grant and --no-grants. The grants given replace the\n" +
			"group's grants (take `dormeto groups show` first to keep some); --no-grants removes them all. " + grantHelp + "\n" +
			"Needs an administrator.",
		RunE: func(c *cobra.Command, args []string) error {
			name := args[0]
			fl := c.Flags()
			var err error
			switch {
			case strings.TrimSpace(name) == "":
				err = errors.New("the group name is empty")
			case noGrants && len(grants) > 0:
				err = errors.New("--grant and --no-grants exclude each other")
			case !fl.Changed("description") && len(grants) == 0 && !noGrants:
				err = errors.New("nothing to change: give --description, --grant or --no-grants")
			}
			body := map[string]any{}
			if err == nil && fl.Changed("description") {
				if err = fleet.CheckDescription(description); err == nil {
					body["description"] = description
				}
			}
			if err == nil && (len(grants) > 0 || noGrants) {
				var gs []fleet.Grant
				if gs, err = fleet.ParseGrants(grants); err == nil {
					body["grants"] = gs
				}
			}
			if err != nil {
				return finish(c, usagef("%v", err))
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				raw, err := callAPI(ctx, api, "PUT", groupPath(name), body)
				if err != nil {
					return err
				}
				return groupAnswer(c, asJSON, raw, "updated", name)
			})
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "what the group is for, at most 200 characters")
	cmd.Flags().StringArrayVar(&grants, "grant", nil, "<tags>:<permissions>; the grants given replace the group's")
	cmd.Flags().BoolVar(&noGrants, "no-grants", false, "remove every grant")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newGroupsDelete(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var dryRun, yes, asJSON bool
	cmd := &cobra.Command{
		Use: "delete <name>", Short: "Delete a custom group and its grants", Args: argCount(1, 1, "<name>"), SilenceErrors: true,
		Long: "Delete a custom group and its grants: its members lose what it granted at their next sign-in. It is always\n" +
			"confirmed on a terminal (--yes without one; --dry-run names the group and sends nothing). Needs an administrator.",
		RunE: func(c *cobra.Command, args []string) error {
			name := args[0]
			if strings.TrimSpace(name) == "" {
				return finish(c, usagef("the group name is empty"))
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				q := fmt.Sprintf("Delete group %s? Its grants are removed and its members lose them.", name)
				if done, err := confirmNames(ctx, c, deps, []string{name}, "group", q, true, dryRun, yes); done || err != nil {
					return err
				}
				raw, err := callAPI(ctx, api, "DELETE", groupPath(name), nil)
				if err != nil {
					return err
				}
				return groupAnswer(c, asJSON, raw, "deleted", name)
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "name the group and stop")
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation (needed without a terminal)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}
