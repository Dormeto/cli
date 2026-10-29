package main

// `dormeto gcp check`: the flags read into a gcponboard.Request and the run called with the real dependencies — the
// tokens of gcloud (its active configuration, or --gcloud-configuration) and HTTPS with a 30 s timeout. Exit codes: 0;
// 1 when a command lacks a permission or Google refuses; 2 for a bad flag; 128 and the signal's number when stopped.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/gcponboard"
)

// gcpRun is one of internal/gcponboard's commands.
type gcpRun func(ctx context.Context, d gcponboard.Deps, r gcponboard.Request) int

// gcpRuns are the runs of the gcp commands: internal/gcponboard's, or a test's.
type gcpRuns struct{ check gcpRun }

// theGcpRuns are internal/gcponboard's.
var theGcpRuns = gcpRuns{check: gcponboard.RunCheck}

func newGcp(runs gcpRuns) *cobra.Command {
	cmd := &cobra.Command{Use: "gcp", Short: "Dormeto in Google Cloud", Args: noArgs, SilenceErrors: true}
	cmd.AddCommand(newGcpCheck(runs.check))
	return cmd
}

func newGcpCheck(run gcpRun) *cobra.Command {
	var project, region, configuration string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check which permissions you lack for each dormeto gcp command (read-only)",
		Long: "Check, read-only, the permissions you hold in a Google Cloud project against what each dormeto gcp command\n" +
			"needs, and which of Dormeto's APIs are on. Nothing is changed. Exit 1 when a command lacks a permission.",
		Args: noArgs, SilenceErrors: true,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx, signalled, stop := interruptible(c.Context())
			defer stop()
			d := gcponboard.Deps{
				Google: &gcp.Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Tokens: gcp.NewGcloud(configuration, nil)},
				Out:    c.OutOrStdout(), Err: c.ErrOrStderr(),
			}
			code := run(ctx, d, gcponboard.Request{Project: project, Region: region, JSON: asJSON})
			if s := signalled(); s != nil {
				code = signalCode(s)
			}
			if code != 0 {
				return exitCode(code)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "the Google Cloud project's ID (required)")
	f.StringVar(&region, "region", "", "the router's region")
	f.StringVar(&configuration, "gcloud-configuration", "", "the gcloud configuration to use (default: the active one)")
	f.BoolVar(&asJSON, "json", false, "print the report as JSON")
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintln(c.ErrOrStderr(), err)
		return exitCode(2)
	})
	cmd.PreRunE = func(c *cobra.Command, _ []string) error {
		if project == "" {
			fmt.Fprintln(c.ErrOrStderr(), "--project is required")
			return exitCode(2)
		}
		return nil
	}
	return cmd
}
