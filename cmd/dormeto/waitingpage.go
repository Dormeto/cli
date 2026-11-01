package main

// `dormeto workloads|routers|settings waiting-page show|set|clear`: the branded waiting page's setting of one workload,
// one router or the organization, through the manager's /iam routes. `set` replaces that level's document: a template
// from --title, --description and --icon, or a custom page from --html. The icon and the page are checked before any
// call (exit 2). Exit codes as the fleet commands.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

// pageLevel says what a waiting-page command acts on.
type pageLevel struct {
	level string // workload, router or organization
	// target gives the label the answer is said under and the manager's path of the level's waiting-page route.
	target func(args []string) (label, path string, err error)
	use    string // the target in the usage line, "" for none
	noun   string // "workload", "router" or "organization", for the help
}

var (
	workloadPage = pageLevel{"workload", func(args []string) (string, string, error) {
		t, err := oneTarget(args)
		return t.String(), t.Path("/waiting-page"), err
	}, " <router>/<workload>", "workload"}
	routerPage = pageLevel{"router", func(args []string) (string, string, error) {
		if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
			return "", "", usagef("name exactly one router")
		}
		return args[0], routerPath(args[0], "/waiting-page"), nil
	}, " <router>", "router"}
	organizationPage = pageLevel{"organization", func(args []string) (string, string, error) {
		if len(args) > 0 {
			return "", "", usagef("unexpected argument %q: the organization's page has no target", args[0])
		}
		return "the organization", "/settings/waiting-page", nil
	}, "", "organization"}
)

// newWaitingPage is the `waiting-page` group of one level.
func newWaitingPage(f *fleetFlags, deps fleetDeps, l pageLevel) *cobra.Command {
	cmd := fleetCommand("waiting-page", "Show, set or clear the "+l.noun+"'s waiting page")
	cmd.AddCommand(newWaitingPageShow(f, deps, l), newWaitingPageSet(f, deps, l), newWaitingPageClear(f, deps, l))
	return cmd
}

func newWorkloadsWaitingPage(f *fleetFlags, deps fleetDeps) *cobra.Command {
	return newWaitingPage(f, deps, workloadPage)
}
func newRoutersWaitingPage(f *fleetFlags, deps fleetDeps) *cobra.Command {
	return newWaitingPage(f, deps, routerPage)
}
func newSettingsWaitingPage(f *fleetFlags, deps fleetDeps) *cobra.Command {
	return newWaitingPage(f, deps, organizationPage)
}

// pageError words a refusal of the waiting-page routes: the manager's sentence, the problems a 400 lists, and for a 403
// and a 409 the sentence when the manager sent none. label prefixes it, "" for none.
func pageError(label string, err error) error {
	var ae *fleet.APIError
	if !errors.As(err, &ae) || ae.Gateway {
		return prefixed(label, err)
	}
	text := ae.Message
	switch {
	case len(ae.Errors) > 0:
		text = strings.Join(ae.Errors, "\n")
	case text == "" && ae.Status == http.StatusForbidden:
		text = "a custom waiting page needs an administrator"
	case text == "" && ae.Status == http.StatusConflict:
		text = "upgrade the router to 0.35.0"
	default:
		return prefixed(label, err)
	}
	return prefixed(label, errors.New(text))
}

func prefixed(label string, err error) error {
	if label == "" || label == "the organization" {
		return err
	}
	return fmt.Errorf("%s: %w", label, err)
}

func newWaitingPageShow(f *fleetFlags, deps fleetDeps, l pageLevel) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show" + l.use, Short: "Show the " + l.noun + "'s waiting page", SilenceErrors: true,
		Long: "Show the waiting page the " + l.noun + " shows: its mode, the icon, title and description (or the custom page) and the level each\n" +
			"comes from, for a workload the favicon read from the application with its date or why there is none, who set this level and\n" +
			"the warnings. An icon or a page is printed as its type and size, never its content. --json prints the manager's whole\n" +
			"answer, the icon and the page included. Needs a router 0.35.0 or later.",
		RunE: func(c *cobra.Command, args []string) error {
			label, path, err := l.target(args)
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var raw json.RawMessage
				if err := api.Get(ctx, path, nil, &raw); err != nil {
					return pageError(label, err)
				}
				return printRead(c, raw, asJSON, func(v fleet.PageView) string { return v.Text(label, l.level, deps.now().Location()) })
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

// readLimited reads a file (- for standard input) up to limit bytes and one more, so that a larger one is seen.
func readLimited(name string, stdin io.Reader, limit int) ([]byte, error) {
	in := stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		in = f
	}
	return io.ReadAll(io.LimitReader(in, int64(limit)+1))
}

// pageDocument is the document the flags of set ask for: a template from --icon, --title and --description, or a custom
// page from --html, never both. The icon and the page are read and checked here, before any call.
func pageDocument(c *cobra.Command, icon, title, description, html string) (fleet.PageDoc, error) {
	fl := c.Flags()
	if fl.Changed("html") {
		for _, other := range []string{"icon", "title", "description"} {
			if fl.Changed(other) {
				return fleet.PageDoc{}, usagef("--html writes a custom page: it cannot go with --%s (a template's field)", other)
			}
		}
		if html == "" {
			return fleet.PageDoc{}, usagef("--html: name a file (- for standard input)")
		}
		data, err := readLimited(html, c.InOrStdin(), fleet.MaxHTMLBytes)
		if err != nil {
			return fleet.PageDoc{}, usagef("--html: %v", err)
		}
		page, err := fleet.CheckHTML(data)
		if err != nil {
			return fleet.PageDoc{}, usagef("--html: %v", err)
		}
		return fleet.PageDoc{Mode: "custom", HTML: &page}, nil
	}
	doc := fleet.PageDoc{Mode: "template"}
	if fl.Changed("icon") {
		switch icon {
		case "":
			return doc, usagef("--icon: give auto, none or an image file")
		case "auto", "none":
			doc.Icon = &icon
		default:
			data, err := readLimited(icon, nil, fleet.MaxIconBytes)
			if err != nil {
				return doc, usagef("--icon: %v", err)
			}
			uri, err := fleet.IconDataURI(data)
			if err != nil {
				return doc, usagef("--icon: %v", err)
			}
			doc.Icon = &uri
		}
	}
	if fl.Changed("title") {
		doc.Title = &title
	}
	if fl.Changed("description") {
		doc.Description = &description
	}
	if doc.Icon == nil && doc.Title == nil && doc.Description == nil {
		return doc, usagef("give --title, --description or --icon for a template, or --html for a custom page")
	}
	return doc, nil
}

func newWaitingPageSet(f *fleetFlags, deps fleetDeps, l pageLevel) *cobra.Command {
	var icon, title, description, html string
	var asJSON bool
	cmd := &cobra.Command{
		Use: "set" + l.use + " [--icon auto|none|<file>] [--title <text>] [--description <text>] | --html <file or ->", SilenceErrors: true,
		Short: "Set the " + l.noun + "'s waiting page (replacing the level's document)",
		Long: "Set the " + l.noun + "'s waiting page, replacing what this level has. A template is written by --icon (auto: the favicon read from the\n" +
			"application; none; or an image file: PNG, JPEG, WebP, SVG or ICO, at most 64 KiB, its type taken from its bytes), --title (at most\n" +
			"80 characters, one line) and --description (at most 500 characters); a field left out is inherited from the level above.\n" +
			"--html writes a custom page instead (a file or - for standard input: UTF-8, not blank, at most 256 KiB); it cannot go with\n" +
			"the template's flags, and it runs scripts on the application's own address, so it needs an administrator at every level.\n" +
			"The file is checked before any call (exit 2). The page's warnings are printed. A workload written by Terraform is refused.\n" +
			"Needs a router 0.35.0 or later. --json prints the manager's answer.",
		RunE: func(c *cobra.Command, args []string) error {
			label, path, err := l.target(args)
			var doc fleet.PageDoc
			if err == nil {
				doc, err = pageDocument(c, icon, title, description, html)
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var raw json.RawMessage
				if err := api.Put(ctx, path, map[string]fleet.PageDoc{"page": doc}, &raw); err != nil {
					return pageError(label, err)
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), orRaw(raw))
				}
				var v fleet.PageView
				_ = json.Unmarshal(raw, &v)
				word := doc.Mode
				fmt.Fprintf(c.OutOrStdout(), "%s: waiting page set (%s)\n", label, word)
				for _, w := range v.Warnings {
					fmt.Fprintf(c.OutOrStdout(), "warning: %s\n", w)
				}
				return nil
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&icon, "icon", "", "auto, none or an image file (PNG, JPEG, WebP, SVG or ICO, at most 64 KiB)")
	fl.StringVar(&title, "title", "", "the page's title, at most 80 characters")
	fl.StringVar(&description, "description", "", "the page's description, plain text, at most 500 characters")
	fl.StringVar(&html, "html", "", "a custom page: an HTML file, - for standard input (at most 256 KiB; needs an administrator)")
	fl.BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newWaitingPageClear(f *fleetFlags, deps fleetDeps, l pageLevel) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "clear" + l.use, Short: "Remove the " + l.noun + "'s waiting page", SilenceErrors: true,
		Long: "Remove the document this level holds: the " + l.noun + " inherits the level above, or the default page. Clearing a custom\n" +
			"page needs an administrator. A level Terraform wrote is refused. --json prints the manager's answer.",
		RunE: func(c *cobra.Command, args []string) error {
			label, path, err := l.target(args)
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var raw json.RawMessage
				if err := api.Delete(ctx, path, &raw); err != nil {
					return pageError(label, err)
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), orRaw(raw))
				}
				fmt.Fprintf(c.OutOrStdout(), "%s: waiting page cleared\n", label)
				return nil
			})
		},
	}
	bindJSONAnswer(cmd, &asJSON)
	return cmd
}

func bindJSONAnswer(cmd *cobra.Command, asJSON *bool) {
	cmd.Flags().BoolVar(asJSON, "json", false, "print the manager's answer")
}
