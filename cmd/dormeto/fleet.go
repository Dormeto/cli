package main

// `dormeto aws routers list`, `dormeto aws routers upgrade` and `dormeto aws workloads list`: the manager's routers and workloads,
// read through its IAM-authorized API (internal/fleet) with the AWS profile that `dormeto aws install` uses. The flags that
// say where the manager is (--profile, --region, --env, --api-url) are shared by the fleet commands; the API is found
// by its name unless --api-url gives it. Exit codes: 0; 1 when AWS or the manager refuses; 2 for a bad flag or
// argument; 128 and the signal's number when SIGINT or SIGTERM stopped it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/aws"
	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/output"
)

// fleetDeps are what the fleet commands depend on, so that tests inject fakes: the profile's AWS configuration, the
// lookup of the manager's API, the HTTP client, the clock and the check that stdin is a terminal.
type fleetDeps struct {
	load     func(ctx context.Context, profile string) (awssdk.Config, error)
	locator  func(cfg awssdk.Config) fleet.Locator
	http     *http.Client
	now      func() time.Time
	terminal func(io.Reader) bool
	sleep    func(context.Context, time.Duration) error                                            // between two reads of an operation
	timeout  func(context.Context, time.Duration) (context.Context, context.CancelFunc)            // ends the following
	confirm  func(ctx context.Context, in io.Reader, out io.Writer, question string) (bool, error) // y/N on a terminal
}

// theFleetDeps are the real ones: the SDK's configuration of the profile, API Gateway's lookup, HTTPS with a 30 s
// timeout.
func theFleetDeps() fleetDeps {
	return fleetDeps{
		load:     aws.Load,
		locator:  fleet.NewLocator,
		http:     &http.Client{Timeout: 30 * time.Second},
		now:      time.Now,
		terminal: isTerminal,
		sleep:    sleepFor,
		timeout:  context.WithTimeout,
		confirm:  promptConfirm,
	}
}

// usageError is a bad flag or argument: it exits 2, whereas any other error of a run exits 1.
type usageError struct{ error }

func usagef(format string, a ...any) error { return usageError{fmt.Errorf(format, a...)} }

// finish prints err on stderr and gives the exit code it stands for; nil is success. An exitCode is already said: it
// is returned as it is.
func finish(c *cobra.Command, err error) error {
	if err == nil {
		return nil
	}
	var code exitCode
	if errors.As(err, &code) {
		return err
	}
	fmt.Fprintln(c.ErrOrStderr(), err)
	if errors.As(err, &usageError{}) {
		return exitCode(2)
	}
	return exitCode(1)
}

// fleetCommand is a group of fleet commands: a bad flag of its subcommands exits 2.
func fleetCommand(use, short string) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, SilenceErrors: true, SilenceUsage: true}
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintln(c.ErrOrStderr(), err)
		return exitCode(2)
	})
	return cmd
}

// fleetFlags say where the manager is: --profile, --region (the manager's), --env and --api-url.
type fleetFlags struct{ profile, region, env, apiURL string }

// bind adds the flags to cmd's persistent flags, its subcommands' too.
func (f *fleetFlags) bind(cmd *cobra.Command) {
	p := cmd.PersistentFlags()
	p.StringVar(&f.profile, "profile", "", "the AWS profile (default: the SDK's)")
	p.StringVar(&f.region, "region", "", "the manager's region (default: the profile's)")
	bindManager(p, &f.env, &f.apiURL)
}

var envName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,8}[a-z0-9])?$`)

// check refuses a flag that cannot name a manager, before any call.
func (f *fleetFlags) check() error {
	if !envName.MatchString(f.env) {
		return usagef("--env: %q is not 1 to 10 lowercase letters, digits or inner hyphens", f.env)
	}
	if f.apiURL != "" {
		if u, err := url.Parse(f.apiURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return usagef("--api-url: %q is not an http(s) URL", f.apiURL)
		}
	}
	return nil
}

var apiHost = regexp.MustCompile(`^[a-z0-9]+\.execute-api\.([a-z0-9-]+)\.amazonaws\.com(\.cn)?$`)

// signingRegion is the region in an API Gateway URL's host, "" for any other.
func signingRegion(apiURL string) string {
	u, err := url.Parse(apiURL)
	if err != nil {
		return ""
	}
	if m := apiHost.FindStringSubmatch(u.Hostname()); m != nil {
		return m[1]
	}
	return ""
}

// connect is the client of the manager's API: the profile's configuration, the API found by name in the manager's
// region (or --api-url), requests signed for the region in its host (else the manager's).
func (f *fleetFlags) connect(ctx context.Context, deps fleetDeps) (*fleet.Client, error) {
	cfg, err := deps.load(ctx, f.profile)
	if err != nil {
		return nil, err
	}
	region := f.region
	if region == "" {
		region = cfg.Region
	}
	api := f.apiURL
	if api == "" {
		if region == "" {
			return nil, noRegion()
		}
		cfg.Region = region // --region overrides the profile's
		if api, err = deps.locator(cfg).APIURL(ctx, f.env, region); err != nil {
			if errors.Is(err, fleet.ErrNoAPI) {
				err = fmt.Errorf("%w (use --env, --region or --api-url)", err)
			}
			return nil, err
		}
	}
	sign := signingRegion(api)
	if sign == "" {
		if sign = region; sign == "" {
			return nil, noRegion()
		}
	}
	return fleet.New(deps.http, api, sign, cfg.Credentials, deps.now), nil
}

func noRegion() error {
	return usagef("--region: the manager's region is needed (the profile has none)")
}

// run connects, then calls do, interruptible: a signal ends it with 128 and its number. A flag's own checks are done by
// the caller first, so that a bad one exits 2 before any call.
func (f *fleetFlags) run(c *cobra.Command, deps fleetDeps, do func(ctx context.Context, api *fleet.Client) error) error {
	if err := f.check(); err != nil {
		return finish(c, err)
	}
	ctx, signalled, stop := interruptible(c.Context())
	defer stop()
	api, err := f.connect(ctx, deps)
	var dry *fleet.DryRun
	if err == nil && clientDryRun(c) {
		dry = &fleet.DryRun{Out: textOut(c)}
		api.SetDryRun(dry)
	}
	if err == nil {
		err = do(ctx, api)
	}
	if s := signalled(); s != nil {
		return exitCode(signalCode(s))
	}
	if api != nil { // the client's dry run, or the one a command's own --dry-run made its client's (sayRequests)
		dry = api.DryRun()
	}
	if dry != nil && errors.Is(err, fleet.ErrDryRun) {
		err = endDryRun(c, dry)
	}
	return finish(c, err)
}

// endDryRun ends a dry run stopped at the writes it said: nothing was sent, so no error (exit 0); --json prints what it
// would have sent.
func endDryRun(c *cobra.Command, dry *fleet.DryRun) error {
	if jsonFlag(c) {
		return output.JSON(c.OutOrStdout(), dry.JSON())
	}
	return nil
}

// routerFilters are the flags that narrow routers: --account, --router-region, --name and --tag.
type routerFilters struct {
	accounts, regions, tags []string
	name                    string
}

func (r *routerFilters) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringSliceVar(&r.accounts, "account", nil, "only the routers of these AWS accounts or Google Cloud projects (repeatable or comma-separated)")
	f.StringSliceVar(&r.regions, "router-region", nil, "only the routers in these regions (--region is the manager's)")
	f.StringVar(&r.name, "name", "", "only the routers whose name matches this glob (*, ?, […])")
	f.StringArrayVar(&r.tags, "tag", nil, "only the routers that serve a workload with this Dormeto tag, key=value (repeatable: all must match)")
}

// selection is the filters, with the router names and --all of a command that takes them (nil and false for a
// listing), as a fleet.Selection; refused when a flag is bad or, for an upgrade, when nothing is selected.
func (r *routerFilters) selection(names []string, all, forUpgrade bool) (fleet.Selection, error) {
	tags, err := fleet.ParseTags(r.tags)
	if err != nil {
		return fleet.Selection{}, usageError{err}
	}
	sel := fleet.Selection{Names: items(names), Accounts: items(r.accounts), RouterRegions: items(r.regions), NameGlob: r.name, All: all}
	if len(tags) > 0 {
		sel.Tags = tags
	}
	if err := sel.Validate(forUpgrade); err != nil {
		return sel, usageError{err}
	}
	return sel, nil
}

// routerWarnings print on stderr, once per router, that a router did not answer.
type routerWarnings struct {
	c    *cobra.Command
	seen map[string]bool
}

func newRouterWarnings(c *cobra.Command) *routerWarnings {
	return &routerWarnings{c: c, seen: map[string]bool{}}
}

// warn says that router did not answer (message), then note when there is one; a router already warned of is not again.
func (w *routerWarnings) warn(router, message, note string) {
	if w.seen[router] {
		return
	}
	w.seen[router] = true
	if note != "" {
		note = " (" + note + ")"
	}
	fmt.Fprintf(w.c.ErrOrStderr(), "router %s did not answer: %s%s\n", router, message, note)
}

// taggedRouters is the set of routers that serve at least one workload carrying all the tags: one GET /workloads.
// A router that did not answer is warned of: its workloads' tags are unknown, so it cannot be selected.
func taggedRouters(ctx context.Context, api *fleet.Client, c *cobra.Command, warnings *routerWarnings, tags []string) (map[string]bool, error) {
	var w fleet.Workloads
	if err := api.Get(ctx, "/workloads", url.Values{"tag": tags}, &w); err != nil {
		return nil, err
	}
	for _, e := range w.Errors {
		warnings.warn(e.Router, e.Message, "its workloads' tags are unknown")
	}
	if w.More() {
		fmt.Fprintln(c.ErrOrStderr(), "the manager listed only part of the workloads with these tags: some routers may be missing")
	}
	set := map[string]bool{}
	for _, wl := range w.Rows {
		set[wl.Router] = true
	}
	return set, nil
}

// newRouters is `dormeto aws routers` and its subcommands (list and upgrade here, the others in routers.go).
func newRouters(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("routers", "List, show, install, upgrade, pause and uninstall the manager's routers")
	f.bind(cmd)
	cmd.AddCommand(newRoutersList(f, deps), newRoutersUpgrade(f, deps), newRoutersShow(f, deps), newRoutersInstall(f, deps),
		newRoutersUninstall(f, deps), newRoutersPause(f, deps), newRoutersResume(f, deps), newRoutersSettings(f, deps), newRoutersDescription(f, deps), newRoutersWaitingPage(f, deps))
	return cmd
}

func newRoutersList(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		filters  routerFilters
		cloud    cloudFilter
		outdated bool
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use: "list", Short: "List the routers of both clouds, their versions, health and workloads", Args: noArgs, SilenceErrors: true,
		Long: "List the manager's routers: name, cloud (aws or gcp), account or project, region, source, version (with the version\n" +
			"available when it is higher), health and workloads. --tag keeps the routers that serve a workload with those Dormeto\n" +
			"tags. A router that does not answer is listed, its error on stderr.",
		RunE: func(c *cobra.Command, _ []string) error {
			sel, err := filters.selection(nil, false, false)
			if err == nil {
				err = cloud.check()
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var all fleet.Routers
				if err := api.Get(ctx, "/routers", nil, &all); err != nil {
					return err
				}
				all.Rows = onlyCloud(cloud, all.Rows, func(r fleet.Router) string { return r.Cloud })
				warnings := newRouterWarnings(c)
				var tagged map[string]bool
				if len(sel.Tags) > 0 {
					if tagged, err = taggedRouters(ctx, api, c, warnings, filters.tags); err != nil {
						return err
					}
				}
				rows, err := sel.Select(all.Rows, tagged)
				if err != nil {
					return usageError{err}
				}
				considered := rows
				if outdated {
					rows = slices.DeleteFunc(slices.Clone(rows), func(r fleet.Router) bool { return !r.Outdated(all.Available) })
				}
				for _, r := range considered { // whatever --outdated shows
					if r.Error != "" {
						warnings.warn(r.Name, r.Error, "")
					}
				}
				if asJSON {
					return output.JSON(c.OutOrStdout(), fleet.Routers{Rows: rows, Available: all.Available})
				}
				out := c.OutOrStdout()
				if len(rows) > 0 {
					fmt.Fprint(out, fleet.RoutersTable(rows, all.Available))
				}
				fmt.Fprintln(out, fleet.RoutersSummary(considered, all.Available))
				return nil
			})
		},
	}
	filters.bind(cmd)
	cloud.bind(cmd)
	cmd.Flags().BoolVar(&outdated, "outdated", false, "only the routers below the version available")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the API's answer, its rows after the filters")
	return cmd
}

// newWorkloads is `dormeto aws workloads` and its subcommand list.
func newWorkloads(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("workloads", "List and show workloads, keep them awake and schedule them")
	f.bind(cmd)
	cmd.AddCommand(newWorkloadsList(f, deps), newWorkloadsKeepAwake(f, deps), newWorkloadsResume(f, deps), newWorkloadsSchedule(f, deps),
		newWorkloadsShow(f, deps), newWorkloadsHistory(f, deps), newWorkloadsIdleWindow(f, deps),
		newWorkloadsWake(f, deps), newWorkloadsNap(f, deps), newWorkloadsPause(f, deps), newWorkloadsRename(f, deps), newWorkloadsTags(f, deps),
		newWorkloadsOnboard(f, deps), newWorkloadsOffboard(f, deps), newWorkloadsExclude(f, deps), newWorkloadsSettings(f, deps), newWorkloadsDescription(f, deps), newWorkloadsWaitingPage(f, deps))
	return cmd
}

func newWorkloadsList(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		filters listFilters
		router  string
		cloud   cloudFilter
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use: "list", Short: "List the workloads of both clouds, their state and tags", Args: noArgs, SilenceErrors: true,
		Long: "List the manager's workloads: router, id, name, state, Dormeto tags (k=v) and the time since the last change.\n" +
			"A router that does not answer is reported on stderr, the others are listed. The workloads of both clouds are\n" +
			"listed, Google Cloud's and AWS's: --cloud aws or gcp keeps one cloud's rows and their total, of the page read when the\n" +
			"manager lists only a page (its total then counts both clouds). --json's byState stays the manager's, which counts\n" +
			"every workload of both clouds whatever the filters.",
		RunE: func(c *cobra.Command, _ []string) error {
			if _, err := fleet.ParseTags(filters.tags); err != nil {
				return finish(c, usageError{err})
			}
			if err := cloud.check(); err != nil {
				return finish(c, err)
			}
			q := url.Values{}
			if s := upper(filters.states); len(s) > 0 {
				q.Set("state", strings.Join(s, ","))
			}
			if len(filters.tags) > 0 {
				q["tag"] = filters.tags // API Gateway joins repeated parameters with commas, the manager splits them
			}
			if router != "" {
				q.Set("router", router)
			}
			if filters.search != "" {
				q.Set("q", filters.search)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var raw json.RawMessage
				if err := api.Get(ctx, "/workloads", q, &raw); err != nil {
					return err
				}
				var w fleet.Workloads
				if err := json.Unmarshal(raw, &w); err != nil {
					return fmt.Errorf("decode the manager's answer: %w", err)
				}
				warnings := newRouterWarnings(c)
				for _, e := range w.Errors {
					warnings.warn(e.Router, e.Message, "")
				}
				if cloud != "" {
					w.Rows = onlyCloud(cloud, w.Rows, func(r fleet.Workload) string { return r.Cloud })
					if w.More() { // the manager's total counts both clouds and more pages: --cloud filters the page read only
						fmt.Fprintln(c.ErrOrStderr(), "the manager listed only part of the workloads: --cloud keeps the rows of the page read")
					} else {
						w.Total = len(w.Rows)
					}
					var err error
					if raw, err = filterWorkloadsJSON(raw, cloud, !w.More()); err != nil {
						return fmt.Errorf("decode the manager's answer: %w", err)
					}
				}
				out := c.OutOrStdout()
				if asJSON {
					var b bytes.Buffer
					if err := json.Indent(&b, raw, "", "  "); err != nil {
						return err
					}
					_, err := fmt.Fprintf(out, "%s\n", b.Bytes())
					return err
				}
				if len(w.Rows) > 0 {
					fmt.Fprint(out, fleet.WorkloadsTable(w.Rows, deps.now()))
				}
				fmt.Fprintln(out, fleet.WorkloadsSummary(w))
				return nil
			})
		},
	}
	filters.bind(cmd)
	fl := cmd.Flags()
	fl.StringVar(&router, "router", "", "only the workloads of this router")
	cloud.bind(cmd)
	fl.BoolVar(&asJSON, "json", false, "print the API's answer")
	return cmd
}

// upper are the values trimmed and upper-cased, the empty ones left out.
func upper(values []string) []string {
	var out []string
	for _, v := range items(values) {
		out = append(out, strings.ToUpper(v))
	}
	return out
}
