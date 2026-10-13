// Package cli is the estimate command (vision docs/specs/2026-10-10-savings-estimate-design.md §3, §8): the options,
// the accounts, the three phases with their progress, the reports and the summary. Exit code 0; 1 when AWS refuses
// (credentials, an organization call); 2 on a bad option (accounts given that cannot be estimated too). AWS comes
// through EstimateAWS (aws.go wires the SDK; the tests, fakes).
//
// Ports manager packages/estimate/src/cli.ts (EstimateAws, CliDeps, main).
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Dormeto/cli/internal/accounts"
	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/cache"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/estimate"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/options"
	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/progress"
	"github.com/Dormeto/cli/internal/report"
	"github.com/Dormeto/cli/internal/stopped"
)

// EstimateAWS is one profile's AWS (Node's EstimateAws): the accounts it reaches, an account's enabled regions, the
// ports of an account's region, its CloudWatch, and the price book. creds nil: the profile's own.
type EstimateAWS struct {
	Accounts     accounts.Ports
	RegionsOf    func(ctx context.Context, account string, creds *aws.Credentials) ([]string, error)
	PortsOf      func(ctx context.Context, account, region string, creds *aws.Credentials) (discover.RegionPorts, error)
	CloudWatchOf func(ctx context.Context, account, region string, creds *aws.Credentials) (metrics.CloudWatchPort, error)
	Prices       func(cache prices.Cache) PriceBook
}

// PriceBook is the price book the estimate reads: an item's hourly price in a region, net of the discounts (nil: no
// price), and the first price lookup AWS refused, said as `name: message` (Failure nil or "": none), so the
// unpriced workloads say why.
type PriceBook struct {
	Hourly  func(ctx context.Context, region string, item stopped.Item, d prices.Discounts) (*float64, error)
	Failure func() string
}

// Deps are the command's dependencies (Node's CliDeps).
type Deps struct {
	// Context is the run's: cancelled on Ctrl-C (nil: never).
	Context   context.Context
	Now       func() time.Time
	CacheRoot string
	Log       func(line string)
	Error     func(line string)
	// AWS is each profile's: an account is read with the profile reaching it.
	AWS func(o options.Options, profile string) EstimateAWS
	// WriteFile writes a report (nil: the file is written, its directory made).
	WriteFile func(path, text string) error
	// Status is a terminal's: each phase's progress on one line rewritten in place (nil: a line per account or region).
	Status progress.Status
	// Version is the JSON report's tool version.
	Version string
}

// Interrupted is the exit code of a run stopped by Ctrl-C (its context cancelled): 128 + SIGINT, what a shell
// reports for the Node estimate, killed by the signal. Nothing is said, no report is written.
const Interrupted = 130

// save writes a report, its directory made (Node's save).
func save(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// plural is "1 account" or "5 accounts" at a time.
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Main runs the estimate command with its arguments (those after "estimate") and returns its exit code: 0; 2 with
// the message and the usage for a bad option, or the message for accounts given that cannot be estimated (known once
// AWS answered); 1 with `name: message` for anything else (credentials that do not work, an organization call AWS
// refuses), on one line, AWS's words without the SDK's wrappers (awserr.Cause); Interrupted when the context is
// cancelled. --help prints the usage (exit code 0).
func Main(args []string, deps Deps) int {
	deps = deps.filled()
	o, err := options.Parse(args)
	if errors.Is(err, options.ErrHelp) {
		deps.Log(options.Usage)
		return 0
	}
	var usage *options.UsageError
	if errors.As(err, &usage) {
		deps.Error(usage.Message)
		deps.Error(options.Usage)
		return 2
	}
	if err == nil {
		err = estimateRun(deps.Context, o, deps)
	}
	switch {
	case err == nil:
		return 0
	case deps.Context.Err() != nil:
		return Interrupted
	case errors.As(err, &usage):
		// Accounts given that cannot be estimated: a bad option, known once AWS has said which accounts there are.
		deps.Error(usage.Message)
		return 2
	}
	deps.Error(awserr.Said(awserr.Cause(err)))
	return 1
}

// filled gives the dependencies left out their defaults: no cancellation, the clock, nothing said.
func (d Deps) filled() Deps {
	if d.Context == nil {
		d.Context = context.Background()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = func(string) {}
	}
	if d.Error == nil {
		d.Error = func(string) {}
	}
	return d
}

// estimateRun is the run once the options are read: the accounts, the three phases, the reports and the summary.
func estimateRun(ctx context.Context, o options.Options, deps Deps) error {
	awsByProfile := map[string]EstimateAWS{}
	profiles := make([]accounts.Profile, len(o.Profiles))
	for i, p := range o.Profiles {
		awsByProfile[p] = deps.AWS(o, p)
		profiles[i] = accounts.Profile{Profile: p, Ports: awsByProfile[p].Accounts}
	}
	reached, err := accounts.ReachProfiles(ctx, profiles, accounts.Options{Role: o.Role, Only: o.Accounts, Now: deps.Now})
	if err != nil {
		return err
	}
	// An account's AWS: the profile reaching it.
	accountOf := func(account string) *accounts.ReachedAccount {
		i := slices.IndexFunc(reached.Accounts, func(a accounts.ReachedAccount) bool { return a.Account == account })
		if i < 0 {
			return nil
		}
		return &reached.Accounts[i]
	}
	awsOf := func(account string) EstimateAWS {
		if a := accountOf(account); a != nil && a.Profile != "" {
			return awsByProfile[a.Profile]
		}
		return awsByProfile[o.Profiles[0]]
	}
	// The cache is the organization's, else the first account's.
	var key string
	switch {
	case reached.Organization != nil:
		key = *reached.Organization
	case len(reached.Accounts) > 0:
		key = reached.Accounts[0].Account
	default:
		return errors.New("no account reached")
	}
	dir := filepath.Join(deps.CacheRoot, key)
	c := cache.New(dir, deps.Now)
	counted := fmt.Sprintf("%d accounts", len(reached.Accounts))
	if o.Accounts != nil {
		counted = fmt.Sprintf("%d of its %d accounts, --accounts", len(reached.Accounts), reached.Listed)
	}
	var what string
	switch {
	case reached.Organization != nil && *reached.Organization != "":
		what = fmt.Sprintf("the organization %s (%s)", *reached.Organization, counted)
	case len(reached.Accounts) == 1:
		what = "the account " + key
	default:
		ids := make([]string, len(reached.Accounts))
		for i, a := range reached.Accounts {
			ids[i] = a.Account
		}
		what = "the accounts " + strings.Join(ids, ", ")
	}
	deps.Log(fmt.Sprintf("Estimating %s; cache %s", what, dir))
	if reached.Why != nil && *reached.Why != "" {
		deps.Log(*reached.Why)
	}
	refresh := ""
	if o.Refresh != nil {
		refresh = *o.Refresh
	}
	views := progress.Options{Now: deps.Now, Log: deps.Log, Status: deps.Status}

	deps.Log(fmt.Sprintf("Phase 1: discovery (%s at a time)", plural(o.Parallel, "account")))
	targets := make([]discover.Target, len(reached.Accounts))
	for i, a := range reached.Accounts {
		targets[i] = discover.Target{Account: a.Account, Credentials: a.Credentials}
	}
	view := progress.DiscoveryView(len(reached.Accounts), views)
	discovered, err := discover.All(ctx, discover.AllOptions{
		Accounts: targets, Unreachable: func(account string) bool { return unreachable(reached, account) }, Regions: o.Regions,
		RegionsOf: func(ctx context.Context, account string, creds *aws.Credentials) ([]string, error) {
			return awsOf(account).RegionsOf(ctx, account, creds)
		},
		PortsOf: func(ctx context.Context, account, region string, creds *aws.Credentials) (discover.RegionPorts, error) {
			return awsOf(account).PortsOf(ctx, account, region, creds)
		},
		Cache: c, Refresh: refresh, Now: deps.Now, Progress: view.Region, AccountProgress: view.Account, Concurrency: o.Parallel,
	})
	view.End()
	if err != nil {
		return err
	}
	// Excluded by their tags before their requests are read.
	regions, excluded := estimate.ExcludeByTags(discovered, o.ExcludeTags)

	deps.Log(fmt.Sprintf("Phase 2: requests (%s at a time)", plural(o.Parallel, "region")))
	// An account's CloudWatch in a region, with its credentials as asked for that region (a role renewed in time).
	cloudwatchOf := func(ctx context.Context, account, region string) (metrics.CloudWatchPort, error) {
		var creds *aws.Credentials
		if a := accountOf(account); a != nil && a.Credentials != nil {
			var err error
			if creds, err = a.Credentials(ctx); err != nil {
				return nil, err
			}
		}
		return awsOf(account).CloudWatchOf(ctx, account, region, creds)
	}
	// Phase 2's progress (the requests, then the uptime): in a terminal one line each, else a line per region.
	reading := progress.RegionsView("Requests", views)
	requests, err := metrics.RequestsAll(ctx, metrics.RequestsOptions{
		Regions: regions, CloudWatchOf: cloudwatchOf, Cache: c, Refresh: refresh, Now: deps.Now, Concurrency: o.Parallel,
		Progress: reading.Done, OnPlan: reading.Plan, OnStart: reading.Start,
	})
	reading.End()
	if err != nil {
		return err
	}
	// When the EC2 instances without an Auto Scaling group ran, and the ECS services had a task, over the same period.
	kinds := metrics.UptimeKinds{EC2: o.Ec2Uptime, ECS: o.EcsUptime, RDS: o.RdsUptime}
	var uptime []metrics.RegionUptime // nil: not measured (Node's null)
	if kinds.EC2 || kinds.ECS || kinds.RDS {
		measuring := progress.RegionsView("Uptime", views)
		uptime, err = metrics.UptimeAll(ctx, metrics.UptimeOptions{
			Regions: regions, Requests: requests, CloudWatchOf: cloudwatchOf, Cache: c, Refresh: refresh, Now: deps.Now, Kinds: &kinds,
			Concurrency: o.Parallel, Progress: measuring.Done, OnPlan: measuring.Plan, OnStart: measuring.Start,
		})
		measuring.End()
		if err != nil {
			return err
		}
		if uptime == nil {
			uptime = []metrics.RegionUptime{}
		}
	}

	deps.Log("Phase 3: estimate")
	// The prices are public: any profile reads them.
	book := awsByProfile[o.Profiles[0]].Prices(c.Prices())
	e, err := estimate.All(ctx, estimate.Input{
		Reached: reached, Regions: regions, Requests: requests,
		Hourly: func(ctx context.Context, region string, item stopped.Item) (*float64, error) {
			return book.Hourly(ctx, region, item, o.Discounts)
		},
		PriceFailure: book.Failure, Baseline: o.Baseline, Options: o, Now: deps.Now(), Excluded: excluded, Uptime: uptime,
	})
	if err != nil {
		return err
	}
	date := deps.Now().UTC().Format("2006-01-02")
	md := filepath.Join(o.Out, "estimate-"+date+".md")
	json := filepath.Join(o.Out, "estimate-"+date+".json")
	write := deps.WriteFile
	if write == nil {
		write = save
	}
	if err := write(md, report.Markdown(e)); err != nil {
		return err
	}
	if err := write(json, string(report.JSON(e, deps.Version))); err != nil {
		return err
	}
	for _, line := range report.Summary(e) {
		deps.Log(line)
	}
	deps.Log(fmt.Sprintf("Reports: %s and %s", md, json))
	return nil
}

// unreachable says whether an account was listed apart as refusing its role.
func unreachable(reached accounts.Reached, account string) bool {
	if reached.Unreachable == nil {
		return false
	}
	return slices.ContainsFunc(reached.Unreachable.List(), func(u accounts.Unreachable) bool { return u.Account == account })
}
