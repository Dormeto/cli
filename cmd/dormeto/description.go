package main

// `dormeto workloads description` and `routers description`: read, set and clear the optional Markdown description of
// one workload or one router, through the manager's /iam routes. Exit codes as the fleet commands; more than one of
// --set, --file and --clear is a usage error (2).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

// descriptionFlags are the three ways to write a description.
type descriptionFlags struct {
	set, file string
	clear     bool
}

func (d *descriptionFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&d.set, "set", "", "the description, Markdown (empty clears it)")
	fl.StringVar(&d.file, "file", "", "read the description from a file (- for standard input)")
	fl.BoolVar(&d.clear, "clear", false, "remove the description")
}

// request is the write the flags ask for: a method and its body, or write false to read. At most one flag.
func (d *descriptionFlags) request(c *cobra.Command) (method string, body any, write bool, err error) {
	fl := c.Flags()
	given := 0
	for _, name := range []string{"set", "file", "clear"} {
		if fl.Changed(name) {
			given++
		}
	}
	switch {
	case given > 1:
		return "", nil, false, usagef("give one of --set, --file and --clear")
	case fl.Changed("clear"):
		if !d.clear {
			return "", nil, false, usagef("--clear=false does nothing: leave it out to read")
		}
		return "DELETE", nil, true, nil
	case fl.Changed("set"):
		return "PUT", map[string]string{"text": d.set}, true, nil
	case fl.Changed("file"):
		var data []byte
		var err error
		switch d.file {
		case "":
			return "", nil, false, usagef("--file: name a file (- for standard input)")
		case "-":
			data, err = io.ReadAll(c.InOrStdin())
		default:
			data, err = os.ReadFile(d.file)
		}
		if err != nil {
			return "", nil, false, usagef("--file: %v", err)
		}
		return "PUT", map[string]string{"text": string(data)}, true, nil
	}
	return "", nil, false, nil
}

// newDescription builds the command of one kind: target gives the name the answer is said under and the route of the
// workload or router, whose /description is written.
func newDescription(f *fleetFlags, deps fleetDeps, use, short, long string, target func(args []string) (label, detail string, err error)) *cobra.Command {
	var (
		d      descriptionFlags
		asJSON bool
	)
	cmd := &cobra.Command{
		Use: use, Short: short, Long: long, SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			label, detail, err := target(args)
			var method string
			var body any
			var write bool
			if err == nil {
				method, body, write, err = d.request(c)
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var raw json.RawMessage
				if !write {
					if err := api.Get(ctx, detail, nil, &raw); err != nil {
						return err
					}
					return printRead(c, raw, asJSON, func(a struct{ Description *fleet.Description }) string {
						if a.Description == nil || strings.TrimSpace(a.Description.Text) == "" {
							return "no description\n"
						}
						return a.Description.Printed(deps.now().Location())
					})
				}
				var err error
				if method == "DELETE" {
					err = api.Delete(ctx, detail+"/description", &raw)
				} else {
					err = api.Put(ctx, detail+"/description", body, &raw)
				}
				if err != nil {
					return err
				}
				return printRead(c, raw, asJSON, func(a struct{ Description *fleet.Description }) string {
					if a.Description == nil {
						return label + ": description cleared\n"
					}
					return fmt.Sprintf("%s: description set (%d characters)\n", label, utf8.RuneCountInString(a.Description.Text))
				})
			})
		},
	}
	d.bind(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newWorkloadsDescription(f *fleetFlags, deps fleetDeps) *cobra.Command {
	return newDescription(f, deps, "description <router>/<workload> [--set <text> | --file <path or -> | --clear]",
		"Show, set or clear a workload's description",
		"Without a flag, print the workload's description as written (Markdown), who wrote it and when, or \"no description\".\n"+
			"--set gives the text, --file reads it from a file (- for standard input), --clear removes it; give at most one.\n"+
			"At most 4,000 characters; an empty text clears. Needs an operator to write. --json prints the manager's answer.",
		func(args []string) (string, string, error) {
			t, err := oneTarget(args)
			return t.String(), t.Path(""), err
		})
}

func newRoutersDescription(f *fleetFlags, deps fleetDeps) *cobra.Command {
	return newDescription(f, deps, "description <router> [--set <text> | --file <path or -> | --clear]",
		"Show, set or clear a router's description",
		"Without a flag, print the router's description as written (Markdown), who wrote it and when, or \"no description\".\n"+
			"--set gives the text, --file reads it from a file (- for standard input), --clear removes it; give at most one.\n"+
			"At most 4,000 characters; an empty text clears. Needs an administrator to write. --json prints the manager's answer.",
		func(args []string) (string, string, error) {
			if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
				return "", "", usagef("name exactly one router")
			}
			return args[0], routerPath(args[0], ""), nil
		})
}
