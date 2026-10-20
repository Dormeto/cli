package main

// `dormeto users`: the manager's people (list, invite, role, groups, disable, enable, resend, reset-mfa). Changing a
// role and disabling are always confirmed on a terminal (--yes without one); the manager refuses to demote or disable
// the last administrator.

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

func newUsers(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("users", "List, invite and manage the manager's users")
	f.bind(cmd)
	cmd.AddCommand(newUsersList(f, deps), newUsersInvite(f, deps), newUsersRole(f, deps), newUsersGroups(f, deps), newUsersDisable(f, deps),
		newUsersSimple(f, deps, "enable", "Enable a user", "Enable the user: they can sign in again. Needs an administrator.", "/enable",
			func(a answer) string { return fmt.Sprintf("enabled %s", a.Email) }),
		newUsersSimple(f, deps, "resend", "Send a user's invitation again", "Send the invitation again to a user who has not signed in yet. Needs an administrator.", "/resend",
			func(a answer) string { return "invitation sent again to " + a.Email }),
		newUsersSimple(f, deps, "reset-mfa", "Reset a user's authenticator", "Reset the user's authenticator: they set it up again at their next sign-in. Needs an administrator.", "/mfa/reset",
			func(a answer) string {
				return fmt.Sprintf("mfa of %s reset%s", a.Email, map[bool]string{true: " (token deleted)", false: ""}[a.TokenDeleted])
			}))
	return cmd
}

func userPath(email, tail string) string { return "/users/" + url.PathEscape(email) + tail }

func oneEmail(args []string) (string, error) {
	email, err := fleet.NormalEmail(args[0])
	if err != nil {
		return "", usagef("%v", err)
	}
	return email, nil
}

func newUsersList(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "list", Short: "List the users: role, groups, status and MFA", Args: noArgs, SilenceErrors: true,
		Long: "List the users: email, role, custom groups, status (disabled when they are), whether their authenticator is\n" +
			"set up and the age of the account. --json prints the manager's rows. Needs an administrator.",
		RunE: func(c *cobra.Command, _ []string) error {
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				us, err := fleet.ListUsers(ctx, api)
				if err != nil {
					return err
				}
				if asJSON {
					if us == nil {
						us = []fleet.User{}
					}
					return printJSON(c.OutOrStdout(), us)
				}
				fmt.Fprint(c.OutOrStdout(), fleet.UsersText(us, deps.now()))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's rows")
	return cmd
}

func newUsersInvite(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		role   string
		groups []string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use: "invite <email> --role <member|viewer|operator|admin> [--group <name>]...", Short: "Invite a user with a role and custom groups",
		Args: argCount(1, 1, "<email>"), SilenceErrors: true,
		Long: "Invite a user: the manager emails them a temporary password. --role is member (sees only what their groups\n" +
			"grant), viewer, operator or admin; --group, repeated, puts them in existing custom groups. The answer says\n" +
			"whether the invitation was sent or waits for the address to be verified. Needs an administrator.",
		RunE: func(c *cobra.Command, args []string) error {
			email, err := oneEmail(args)
			if err == nil {
				if err = fleet.CheckRole(role); err != nil {
					err = usagef("--role: %v", err)
				}
			}
			if err == nil && slices.Contains(groups, "") {
				err = usagef("--group: a group name is empty")
			}
			if err != nil {
				return finish(c, err)
			}
			body := map[string]any{"email": email, "role": role, "groups": emptyIfNil(groups)}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				raw, err := callAPI(ctx, api, "POST", "/users", body)
				if err != nil {
					return err
				}
				return printAnswer(c, asJSON, raw, func(a answer) string {
					return fmt.Sprintf("invited %s as %s: invitation %s", email, role, a.Invitation)
				})
			})
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "member, viewer, operator or admin (required)")
	cmd.Flags().StringArrayVar(&groups, "group", nil, "an existing custom group; repeat for several")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newUsersRole(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		dryRun, yes, asJSON bool
	)
	cmd := &cobra.Command{
		Use: "role <email> <member|viewer|operator|admin>", Short: "Set a user's role",
		Args: argCount(2, 2, "<email> <role>"), SilenceErrors: true,
		Long: "Set the user's built-in role. It is always confirmed on a terminal (--yes without one; --dry-run names the user\n" +
			"and sends nothing). The manager refuses to change your own role and to demote the last administrator.\n" +
			"Needs an administrator.",
		RunE: func(c *cobra.Command, args []string) error {
			email, err := oneEmail(args)
			if err == nil {
				if err = fleet.CheckRole(args[1]); err != nil {
					err = usagef("%v", err)
				}
			}
			if err != nil {
				return finish(c, err)
			}
			role := args[1]
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				if done, err := confirmNames(ctx, c, deps, []string{email}, "user", fmt.Sprintf("Make %s %s?", email, role), true, dryRun, yes); done || err != nil {
					return err
				}
				raw, err := callAPI(ctx, api, "PUT", userPath(email, "/role"), map[string]any{"role": role})
				if err != nil {
					return err
				}
				return printAnswer(c, asJSON, raw, func(answer) string { return fmt.Sprintf("%s is now %s", email, role) })
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "name the user and stop")
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation (needed without a terminal)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newUsersGroups(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "groups <email> [<name>...]", Short: "Set a user's custom groups (none: clears them)",
		Args: argCount(1, -1, "<email> [<name>...]"), SilenceErrors: true,
		Long: "Set the user's custom groups to those named, replacing their groups; naming none clears them. It applies at\n" +
			"their next sign-in, within an hour. Needs an administrator.",
		RunE: func(c *cobra.Command, args []string) error {
			email, err := oneEmail(args)
			var names []string
			for _, n := range args[1:] {
				if err == nil && strings.TrimSpace(n) == "" {
					err = usagef("a group name is empty")
				}
				if !slices.Contains(names, n) {
					names = append(names, n)
				}
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				raw, err := callAPI(ctx, api, "PUT", userPath(email, "/groups"), map[string]any{"groups": emptyIfNil(names)})
				if err != nil {
					return err
				}
				return printAnswer(c, asJSON, raw, func(answer) string {
					return fmt.Sprintf("%s is in %s", email, orDefault(strings.Join(names, ", "), "no groups"))
				})
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newUsersDisable(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var dryRun, yes, asJSON bool
	cmd := &cobra.Command{
		Use: "disable <email>", Short: "Disable a user: they can no longer sign in",
		Args: argCount(1, 1, "<email>"), SilenceErrors: true,
		Long: "Disable the user: they can no longer sign in. It is always confirmed on a terminal (--yes without one;\n" +
			"--dry-run names the user and sends nothing). The manager refuses to disable you and the last administrator.\n" +
			"Needs an administrator.",
		RunE: func(c *cobra.Command, args []string) error {
			email, err := oneEmail(args)
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				if done, err := confirmNames(ctx, c, deps, []string{email}, "user", fmt.Sprintf("Disable %s? They can no longer sign in.", email), true, dryRun, yes); done || err != nil {
					return err
				}
				raw, err := callAPI(ctx, api, "POST", userPath(email, "/disable"), nil)
				if err != nil {
					return err
				}
				return printAnswer(c, asJSON, raw, func(answer) string { return fmt.Sprintf("disabled %s", email) })
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "name the user and stop")
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation (needed without a terminal)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

// newUsersSimple is a command that POSTs to a user's route and prints text of the answer: enable, resend, reset-mfa.
func newUsersSimple(f *fleetFlags, deps fleetDeps, use, short, long, tail string, text func(answer) string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: use + " <email>", Short: short, Long: long, Args: argCount(1, 1, "<email>"), SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			email, err := oneEmail(args)
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				raw, err := callAPI(ctx, api, "POST", userPath(email, tail), nil)
				if err != nil {
					return err
				}
				return printAnswer(c, asJSON, raw, func(a answer) string {
					if a.Email == "" {
						a.Email = email
					}
					return text(a)
				})
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}
