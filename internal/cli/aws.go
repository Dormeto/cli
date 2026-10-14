package cli

// The estimate command's AWS: the AWS SDK wired to the command, one profile's at a time — the profile's credentials
// (its SSO session included), a role assumed per member account, discovery's clients per account and region, the
// Price List API (us-east-1) and Spot price history for the product's price book. Read-only: Describe, List and Get
// calls, and sts:AssumeRole (internal/aws).
//
// Ports manager packages/estimate/src/bin.ts (awsOf, prices).

import (
	"context"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	awsports "github.com/Dormeto/cli/internal/aws"
	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/options"
	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/stopped"
)

// reader names who reads the EKS clusters, in the reason of one it cannot read.
const reader = "the profile"

// AWSOf is one profile's AWS (several are given with --profile a,b, each reaching its accounts): its shared
// configuration, read now; its credentials resolved when a call needs them. A profile that cannot be loaded (missing
// from the config) fails every call with the SDK's message: the first, who the profile is, gives exit code 1.
func AWSOf(_ options.Options, profile string) EstimateAWS {
	cfg, err := awsports.Load(context.Background(), profile)
	if err != nil {
		return failing(err)
	}
	return EstimateAWS{
		Accounts: awsports.NewAccounts(cfg),
		RegionsOf: func(ctx context.Context, _ string, creds *aws.Credentials) ([]string, error) {
			return awsports.EnabledRegions(ctx, awsports.Of(cfg, creds))
		},
		// One Region per account and region asked: its clients, with the account's credentials as asked for this region.
		PortsOf: func(_ context.Context, account, region string, creds *aws.Credentials) (discover.RegionPorts, error) {
			r := awsports.NewRegion(awsports.Of(cfg, creds), region, reader)
			return r.Discover(discover.Where{Account: account, Region: region}, time.Now), nil
		},
		CloudWatchOf: func(_ context.Context, _, region string, creds *aws.Credentials) (metrics.CloudWatchPort, error) {
			return awsports.NewRegion(awsports.Of(cfg, creds), region, reader).Metrics(), nil
		},
		// The prices are public: the profile's own account reads them.
		Prices: func(cache prices.Cache) PriceBook {
			return priceBook(awsports.NewPricing(cfg), awsports.NewSpot(cfg), cache, nil)
		},
	}
}

// refusal keeps the first price lookup AWS refused, said as Node says it.
type refusal struct {
	mu   sync.Mutex
	said string
}

// record keeps err unless one is kept already, or the run was stopped (no verdict on the lookup).
func (r *refusal) record(ctx context.Context, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.said == "" {
		r.said = awserr.Said(awserr.Cause(err))
	}
}

func (r *refusal) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.said
}

// refusalsOfPricing and refusalsOfSpot record the refusals of the ports they wrap.
type refusalsOfPricing struct {
	prices.PricingPort
	r *refusal
}

func (p refusalsOfPricing) GetProducts(ctx context.Context, in prices.GetProductsInput) (prices.GetProductsOutput, error) {
	out, err := p.PricingPort.GetProducts(ctx, in)
	p.r.record(ctx, err)
	return out, err
}

type refusalsOfSpot struct {
	prices.SpotPort
	r *refusal
}

func (s refusalsOfSpot) History(ctx context.Context, region string, in prices.SpotHistoryInput) (prices.SpotHistoryOutput, error) {
	out, err := s.SpotPort.History(ctx, region, in)
	s.r.record(ctx, err)
	return out, err
}

// priceBook is the product's price book over the Price List API and Spot price history (spot nil: no Spot price),
// its prices kept in cache. It takes a refused lookup as no price: the first refusal is kept, its Failure, so the
// report says why (bin.ts wraps the ports the same way).
func priceBook(pricing prices.PricingPort, spot prices.SpotPort, cache prices.Cache, now func() time.Time) PriceBook {
	r := &refusal{}
	var spots prices.SpotPort
	if spot != nil {
		spots = refusalsOfSpot{spot, r}
	}
	book := prices.NewBook(refusalsOfPricing{pricing, r}, spots, cache, now)
	return PriceBook{Hourly: book.Hourly, Failure: r.String}
}

// failingAccounts fails every call with err.
type failingAccounts struct{ err error }

func (f failingAccounts) Whoami(context.Context) (string, error)       { return "", f.err }
func (f failingAccounts) Organization(context.Context) (string, error) { return "", f.err }
func (f failingAccounts) Inventory(context.Context, func(discover.InventoryProgress)) (discover.OrgInventory, error) {
	return discover.OrgInventory{}, f.err
}
func (f failingAccounts) Assume(context.Context, string, string) (aws.Credentials, error) {
	return aws.Credentials{}, f.err
}

// failing is a profile's AWS failing every call with err.
func failing(err error) EstimateAWS {
	return EstimateAWS{
		Accounts: failingAccounts{err},
		RegionsOf: func(context.Context, string, *aws.Credentials) ([]string, error) {
			return nil, err
		},
		PortsOf: func(context.Context, string, string, *aws.Credentials) (discover.RegionPorts, error) {
			return discover.RegionPorts{}, err
		},
		CloudWatchOf: func(context.Context, string, string, *aws.Credentials) (metrics.CloudWatchPort, error) {
			return nil, err
		},
		Prices: func(cache prices.Cache) PriceBook {
			return PriceBook{Hourly: func(context.Context, string, stopped.Item, prices.Discounts) (*float64, error) { return nil, err }}
		},
	}
}
