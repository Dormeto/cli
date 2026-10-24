package main

// `dormeto workloads description` and `routers description`: read, set and clear the optional Markdown description of
// one workload or one router, through the manager's /iam routes. Exit codes as the fleet commands; more than one of
// --set, --file and --clear is a usage error (2).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/cli"
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
		text, err := d.fileText(c.InOrStdin())
		if err != nil {
			return "", nil, false, err
		}
		return "PUT", map[string]string{"text": text}, true, nil
	}
	return "", nil, false, nil
}

// maxFileBytes bounds what --file reads: far over the manager's 4,000 characters, short of a pipe by mistake.
const maxFileBytes = 64 << 10

// fileText is the text of --file: a path or - for standard input. Unlike --set "" and --clear, a file that is empty
// (or only white space) is refused, not taken for a clear: it is the one input that is empty by accident (a mistyped
// command piped in). A leading byte order mark is dropped.
func (d *descriptionFlags) fileText(stdin io.Reader) (string, error) {
	var in io.Reader = stdin
	name := "standard input"
	switch d.file {
	case "":
		return "", usagef("--file: name a file (- for standard input)")
	case "-":
	default:
		f, err := os.Open(d.file)
		if err != nil {
			return "", usagef("--file: %v", err)
		}
		defer f.Close()
		in, name = f, d.file
	}
	data, err := io.ReadAll(io.LimitReader(in, maxFileBytes+1))
	switch {
	case err != nil:
		return "", usagef("--file: %v", err)
	case len(data) > maxFileBytes:
		return "", usagef("--file: larger than 64 KiB")
	case !utf8.Valid(data):
		return "", usagef("--file: not UTF-8 text")
	}
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	if len(bytes.TrimSpace(data)) == 0 {
		return "", usagef("--file: %s is empty (use --clear to remove the description)", name)
	}
	return string(data), nil
}

// needsManager019 words the answer of a manager before 0.19.0 to a route it does not have; any other error is as it is.
func needsManager019(err error) error {
	var ae *fleet.APIError
	if errors.As(err, &ae) && ae.Status == 404 && !ae.Gateway && strings.HasPrefix(ae.Message, "no route for") {
		return fmt.Errorf("needs manager 0.19.0 or later (%s)", ae.Message)
	}
	return err
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
					if err := api.Get(ctx, detail+"/description", nil, &raw); err != nil {
						return needsManager019(err)
					}
					return printDescription(c, raw, asJSON, deps.now().Location())
				}
				var err error
				if method == "DELETE" {
					err = api.Delete(ctx, detail+"/description", &raw)
				} else {
					err = api.Put(ctx, detail+"/description", body, &raw)
				}
				if err != nil {
					return needsManager019(err)
				}
				return printRead(c, raw, asJSON, func(a struct{ Description *fleet.Description }) string {
					if a.Description == nil {
						return label + ": description cleared\n"
					}
					return fmt.Sprintf("%s: description set (%s)\n", label, cli.Plural(utf8.RuneCountInString(a.Description.Text), "character"))
				})
			})
		},
	}
	d.bind(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

// printDescription prints what the description route answers. A key that is null is no description; a key that is absent
// is an error, so that no client offers to write over a text it could not see. --json prints the same {"description": …} as the writes.
func printDescription(c *cobra.Command, detail json.RawMessage, asJSON bool, loc *time.Location) error {
	var a struct{ Description json.RawMessage }
	if err := json.Unmarshal(detail, &a); err != nil {
		return fmt.Errorf("decode the manager's answer: %w", err)
	}
	if a.Description == nil {
		return errors.New("the manager did not return the description")
	}
	if asJSON {
		return printJSON(c.OutOrStdout(), struct {
			Description json.RawMessage `json:"description"`
		}{a.Description})
	}
	var d *fleet.Description
	if err := json.Unmarshal(a.Description, &d); err != nil {
		return fmt.Errorf("decode the manager's answer: %w", err)
	}
	if d == nil || strings.TrimSpace(d.Text) == "" {
		fmt.Fprintln(c.OutOrStdout(), "no description")
		return nil
	}
	fmt.Fprint(c.OutOrStdout(), d.Printed(loc))
	return nil
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
