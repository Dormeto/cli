package main

// `dormeto whoami`: the caller the manager sees (the profile's identity, by its ARN) and its role, and the manager that
// answered: its version, its API's URL, environment and region.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

func newWhoami(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	var asJSON bool
	cmd := fleetCommand("whoami", "Show the caller the manager sees, its role, and the manager's version")
	cmd.Long = "Show the caller as the manager names it (the AWS profile's identity, by its ARN) and its role, then the manager\n" +
		"that answered: its version, its API's URL, the environment it was found by (not with --api-url) and its region."
	cmd.Args = noArgs
	f.bind(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print {me, version, apiUrl}: the answer of GET /me, the manager's version and the API's URL")
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
			var me json.RawMessage
			if err := api.Get(ctx, "/me", nil, &me); err != nil {
				return err
			}
			version, err := managerVersion(ctx, api)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(c.OutOrStdout(), struct {
					Me      json.RawMessage `json:"me"`
					Version string          `json:"version"`
					APIURL  string          `json:"apiUrl"`
				}{orRaw(me), version, api.BaseURL()})
			}
			var who struct{ Email, Role string }
			if err := json.Unmarshal(orRaw(me), &who); err != nil {
				return fmt.Errorf("decode the manager's answer: %w", err)
			}
			env := f.env
			if f.apiURL != "" { // the API was not found by its environment
				env = ""
			}
			fmt.Fprint(c.OutOrStdout(), fleet.Labelled([][2]string{
				{"arn", who.Email}, {"role", who.Role}, {"manager", version}, {"api url", api.BaseURL()}, {"env", env}, {"region", api.Region()},
			}))
			return nil
		})
	}
	return cmd
}
