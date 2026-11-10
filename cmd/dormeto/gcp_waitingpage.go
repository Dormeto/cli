package main

// `dormeto gcp workloads|routers waiting-page show|set|clear`: the branded waiting page of a workload or of the Google Cloud router,
// on AWS's flags and checks (pageDocument): a template from --icon, --title and --description, or a custom page from --html, the
// icon and the page checked before any call (exit 2). The router's answers are printed as AWS's (fleet.PageView).

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/gcponboard"
)

// gcpPageRuns are the three runs of one level's waiting-page commands.
type gcpPageRuns struct{ show, set, clear gcpRun }

// newGcpWaitingPage is the `waiting-page` group of the workloads' level (workload) or of the router's.
func newGcpWaitingPage(runs gcpPageRuns, version string, workload bool) *cobra.Command {
	noun := "router"
	if workload {
		noun = "workload"
	}
	cmd := &cobra.Command{Use: "waiting-page", Short: "Show, set or clear the " + noun + "'s waiting page", Args: noArgs, SilenceErrors: true,
		Long: "Show, set or clear the " + noun + "'s waiting page, as AWS's waiting-page commands do, through the router's controller. A level's own\n" +
			"document replaces what it had; a field a template leaves out is inherited from the level above. A custom page (--html) is\n" +
			"served only when the router's custom pages are on (dormeto gcp routers install --custom-pages)."}
	cmd.AddCommand(newGcpPageShow(runs.show, version, workload, noun), newGcpPageSet(runs.set, version, workload, noun), newGcpPageClear(runs.clear, version, workload, noun))
	return cmd
}

// gcpPageTarget is the command on one workload (its argument) or on the router.
func gcpPageTarget(verb, short, long string, run gcpRun, version string, workload bool, f *gcpFlags) *cobra.Command {
	var id string
	use := verb
	if workload {
		use += " <workload>"
	}
	cmd := gcpCommand(use, short, long, f, []string{"project", "region"}, version, func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
		r.ID = id
		return run(ctx, d, r)
	})
	cmd.Args = func(c *cobra.Command, args []string) error {
		want := 0
		if workload {
			want = 1
		}
		if len(args) != want {
			if workload {
				fmt.Fprintf(c.ErrOrStderr(), "name exactly one workload, as the argument (got %d)\n", len(args))
			} else {
				fmt.Fprintf(c.ErrOrStderr(), "unexpected argument %q: the router's page has no target\n", args[0])
			}
			return exitCode(2)
		}
		if workload {
			id = args[0]
		}
		return nil
	}
	gcpRouterFlags(cmd, f)
	return cmd
}

func newGcpPageShow(run gcpRun, version string, workload bool, noun string) *cobra.Command {
	var f gcpFlags
	return gcpPageTarget("show", "Show the "+noun+"'s waiting page",
		"Show the waiting page the "+noun+" shows: its mode, the icon, title and description (or the custom page) and the level each\n"+
			"comes from, for a workload the favicon read from the application, who set this level and the warnings. An icon or a page is\n"+
			"printed as its type and size, never its content. A custom page stored while the router's custom pages are off is shown as off.\n"+
			"--json prints the controller's whole answer. A router without the waiting page says so, with the version to install.", run, version, workload, &f)
}

func newGcpPageClear(run gcpRun, version string, workload bool, noun string) *cobra.Command {
	var f gcpFlags
	return gcpPageTarget("clear", "Remove the "+noun+"'s waiting page",
		"Remove the document this level holds: the "+noun+" inherits the level above, or the default page. --json prints the controller's answer.\n"+
			"A router without the waiting page says so, with the version to install.", run, version, workload, &f)
}

func newGcpPageSet(run gcpRun, version string, workload bool, noun string) *cobra.Command {
	var f gcpFlags
	var icon, title, description, html string
	var doc *fleet.PageDoc
	cmd := gcpPageTarget("set", "Set the "+noun+"'s waiting page (replacing the level's document)",
		"Set the "+noun+"'s waiting page, replacing what this level has. A template is written by --icon (auto: the favicon read from the\n"+
			"application; none; or an image file: PNG, JPEG, WebP, SVG or ICO, at most 64 KiB, its type taken from its bytes), --title (at most\n"+
			"80 characters, one line) and --description (at most 500 characters); a field left out is inherited from the level above.\n"+
			"--html writes a custom page instead (a file or - for standard input: UTF-8, not blank, at most 256 KiB); it cannot go with\n"+
			"the template's flags, and the router refuses it unless its custom pages are on (dormeto gcp routers install --custom-pages).\n"+
			"The file is checked before any call (exit 2). The page's warnings are printed. --json prints the controller's answer.",
		func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int {
			r.Page = doc
			return run(ctx, d, r)
		}, version, workload, &f)
	prior := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if err := prior(c, args); err != nil {
			return err
		}
		p, err := pageDocument(c, icon, title, description, html)
		if err != nil {
			return finish(c, err)
		}
		doc = &p
		return nil
	}
	fl := cmd.Flags()
	fl.StringVar(&icon, "icon", "", "auto, none or an image file (PNG, JPEG, WebP, SVG or ICO, at most 64 KiB)")
	fl.StringVar(&title, "title", "", "the page's title, at most 80 characters")
	fl.StringVar(&description, "description", "", "the page's description, plain text, at most 500 characters")
	fl.StringVar(&html, "html", "", "a custom page: an HTML file, - for standard input (at most 256 KiB; the router's custom pages must be on)")
	return cmd
}
