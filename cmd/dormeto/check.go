package main

// `dormeto aws check`: what the profile lacks for each `dormeto aws` method, from IAM's simulation of its policies
// (internal/awscheck). It finds the manager as any manager command does and reads its version; it calls no route that
// writes, and changes nothing.

import (
	"context"
	"errors"
	"net/url"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/awscheck"
	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/install"
)

// theCheckClients are the SDK's IAM and STS clients of the profile's configuration.
func theCheckClients(cfg awssdk.Config) (awscheck.IAM, awscheck.STS) {
	return iam.NewFromConfig(cfg), sts.NewFromConfig(cfg)
}

func newAwsCheck(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	var asJSON bool
	cmd := fleetCommand("check", "Show the permissions the profile lacks for each command (read-only)")
	cmd.Long = "Show, read-only, for each method of `dormeto aws`, ✓ when the profile holds every permission it needs, ✗ with\n" +
		"the missing ones otherwise, from IAM's simulation of the profile's policies (iam:SimulatePrincipalPolicy, on its\n" +
		"role for an assumed role, which iam:GetRole reads). A manager's method needs apigateway:GET to find the API (not\n" +
		"with --api-url) and execute-api:Invoke on each route its commands call, a {…} of a path being *: a policy narrowed\n" +
		"to some routers or workloads shows those routes missing. install and uninstall need the install's actions,\n" +
		"estimate its reads. With no manager found in the region for the environment, only those three are checked. A\n" +
		"simulation IAM refuses leaves every method not checked, with a warning, and exits 0. The simulation leaves out\n" +
		"service control policies. Exit codes: 0; 1 when a method lacks a permission or AWS refuses; 2 for a bad flag."
	cmd.Args = noArgs
	f.bind(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print {account, caller, manager: {api, version} | null, commands: [{command, checked, held, missing}], warnings}")
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		if err := f.check(); err != nil {
			return finish(c, err)
		}
		ctx, signalled, stop := interruptible(c.Context())
		defer stop()
		rep, err := awsCheck(ctx, f, deps)
		if s := signalled(); s != nil {
			return exitCode(signalCode(s))
		}
		if err == nil {
			if asJSON {
				err = awscheck.PrintJSON(c.OutOrStdout(), rep)
			} else {
				err = awscheck.PrintText(c.OutOrStdout(), rep)
			}
		}
		if err == nil && !rep.OK() {
			return exitCode(1)
		}
		return finish(c, err)
	}
	return cmd
}

// awsCheck finds the manager (by its name, or --api-url), reads its version, then checks the profile's rights.
func awsCheck(ctx context.Context, f *fleetFlags, deps fleetDeps) (awscheck.Report, error) {
	cfg, err := deps.load(ctx, f.profile)
	if err != nil {
		return awscheck.Report{}, err
	}
	region := f.region
	if region == "" {
		region = cfg.Region
	}
	if region == "" && signingRegion(f.apiURL) == "" {
		return awscheck.Report{}, noRegion()
	}
	if region != "" {
		cfg.Region = region // --region overrides the profile's
	}
	r := awscheck.Request{Env: f.env, Region: region}
	api := f.apiURL
	if api == "" {
		api, err = deps.locator(cfg).APIURL(ctx, f.env, region)
		switch {
		case errors.Is(err, fleet.ErrNoAPI):
			api = ""
		case err != nil && install.Refused(err):
			api, r.LookupRefused = "", true
		case err != nil:
			return awscheck.Report{}, err
		}
	}
	if api != "" {
		m := &awscheck.Manager{API: api, APIID: apiID(api), Region: signingRegion(api), ByName: f.apiURL == ""}
		if m.Region == "" {
			m.Region = region
		}
		if m.ByName {
			m.API = "dmt-" + f.env + "-manager-api"
		}
		m.Version, m.VersionErr = managerVersion(ctx, fleet.New(deps.http, api, m.Region, cfg.Credentials, deps.now))
		if ctx.Err() != nil {
			return awscheck.Report{}, ctx.Err()
		}
		r.Manager = m
	}
	i, s := deps.checkClients(cfg)
	return awscheck.Check(ctx, i, s, r)
}

// apiID is the API's id in an execute-api URL's host ("" for any other host).
func apiID(apiURL string) string {
	u, err := url.Parse(apiURL)
	if err != nil || !apiHost.MatchString(u.Hostname()) {
		return ""
	}
	id, _, _ := strings.Cut(u.Hostname(), ".")
	return id
}
