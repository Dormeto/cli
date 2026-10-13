package discover

// Phase 1 of the estimate for every account (vision
// docs/specs/2026-10-10-savings-estimate-design.md §4): each reached account's
// regions, the discovery of each, kept per account and region: a run reuses what
// is fresh and resumes the rest; a region that fails is said and tried again
// next time. --parallel accounts at a time (5 by default), an account's regions
// one by one, a region's workloads eight at a time; an account's role is
// assumed only when one of its regions is not kept, and its credentials asked
// again for each region scanned (the accounts package renews a role's five
// minutes before it expires).
//
// Ports manager packages/estimate/src/discover.ts (ProgressEvent, AccountEvent,
// discoverAll). Package accounts imports this one, so the accounts reached are
// given as Targets (the account and its credentials), not as accounts.Reached.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/cache"
	"github.com/Dormeto/cli/internal/limit"
)

// Refresh values: --refresh discovery scans everything again.
const RefreshDiscovery = "discovery"

// PhaseDiscovery is the Phase of the events discovery emits (Node's phases
// are discovery, requests and uptime).
const PhaseDiscovery = "discovery"

// Where a region came from.
const (
	FromCache = "cache"
	FromAWS   = "aws"
)

// ProgressEvent says a region is done: where it came from (the cache or AWS),
// how many workloads it holds and why it failed, if it did. JSON names as Node's.
type ProgressEvent struct {
	Phase     string `json:"phase"` // discovery, requests or uptime
	Account   string `json:"account"`
	Region    string `json:"region"`
	From      string `json:"from"` // cache or aws
	Workloads int    `json:"workloads"`
	Error     string `json:"error,omitempty"`
}

// Account states of an AccountEvent.
const (
	AccountStarted = "started"
	AccountDone    = "done"
)

// AccountEvent is an account of the discovery phase: started once its regions
// are known, done after the last (Error: its regions could not be listed, or
// its credentials not obtained).
type AccountEvent struct {
	Account string `json:"account"`
	State   string `json:"state"` // started or done
	Regions int    `json:"regions"`
	Error   string `json:"error,omitempty"`
}

// Target is a reached account and how to get its credentials (nil credentials:
// the profile's own).
type Target struct {
	Account     string
	Credentials func(ctx context.Context) (*aws.Credentials, error)
}

// AllOptions are the inputs of All.
type AllOptions struct {
	// Accounts in the organization's order; Unreachable says whether an account
	// was listed apart as refusing its role (asked when it fails; nil: none).
	Accounts    []Target
	Unreachable func(account string) bool
	// Regions is --regions, used as given (never cached as an account's enabled
	// regions); nil: each account's enabled ones (RegionsOf).
	Regions   []string
	RegionsOf func(ctx context.Context, account string, creds *aws.Credentials) ([]string, error)
	PortsOf   func(ctx context.Context, account, region string, creds *aws.Credentials) (RegionPorts, error)
	Cache     *cache.Cache
	// Refresh is "discovery" (scan everything again), "metrics" or "".
	Refresh string
	Now     func() time.Time
	// Progress and AccountProgress are called one at a time (a mutex), though
	// accounts run at once.
	Progress        func(ProgressEvent)
	AccountProgress func(AccountEvent)
	// Concurrency is the accounts discovered at once (--parallel; 5 when < 1).
	Concurrency int
}

// regionKept says a kept region is complete: kept before workloads had their
// tags and security groups, instances their state, or the region its companion
// databases with their status, it is scanned again.
func regionKept(r RegionDiscovery) bool {
	if r.Databases == nil {
		return false
	}
	for _, d := range r.Databases {
		if d.Status == "" {
			return false
		}
	}
	for _, w := range r.Workloads {
		if w.Tags == nil || w.SecurityGroups == nil || (w.Backend.Kind == KindInstance && !w.Managed && w.InstanceState == "") {
			return false
		}
	}
	return true
}

// All discovers every account's regions, the accounts at most Concurrency at a
// time, each account's regions one after the other. The regions come back in the
// organization's order, whatever finished first. A region that fails is a
// RegionDiscovery with its Error; an account that fails (not a refused role,
// already listed apart) is one with region "*". The error is the context's.
func All(ctx context.Context, o AllOptions) ([]RegionDiscovery, error) {
	var say sync.Mutex
	progress := func(e ProgressEvent) {
		if o.Progress != nil {
			say.Lock()
			defer say.Unlock()
			o.Progress(e)
		}
	}
	accountProgress := func(e AccountEvent) {
		if o.AccountProgress != nil {
			say.Lock()
			defer say.Unlock()
			o.AccountProgress(e)
		}
	}
	again := o.Refresh == RefreshDiscovery
	concurrency := o.Concurrency
	if concurrency < 1 {
		concurrency = 5
	}
	account := func(ctx context.Context, t Target) ([]RegionDiscovery, error) {
		found := []RegionDiscovery{}
		credentials := func() (*aws.Credentials, error) {
			if t.Credentials == nil {
				return nil, nil
			}
			return t.Credentials(ctx)
		}
		listed := 0
		err := func() error {
			regionsPath := fmt.Sprintf("regions/%s.json", t.Account)
			regions := o.Regions
			if regions == nil && !again {
				var kept []string
				ok, err := o.Cache.Get(regionsPath, cache.FreshDiscovery(o.Now()), &kept)
				if err != nil {
					return err
				}
				if ok && kept != nil {
					regions = kept
				}
			}
			if regions == nil {
				creds, err := credentials()
				if err != nil {
					return err
				}
				if regions, err = o.RegionsOf(ctx, t.Account, creds); err != nil {
					return err
				}
				if regions == nil {
					regions = []string{}
				}
				if err := o.Cache.Put(regionsPath, regions); err != nil {
					return err
				}
			}
			listed = len(regions)
			accountProgress(AccountEvent{Account: t.Account, State: AccountStarted, Regions: listed})
			for _, region := range regions {
				path := fmt.Sprintf("discovery/%s-%s.json", t.Account, region)
				var kept RegionDiscovery
				if !again {
					ok, err := o.Cache.Get(path, cache.FreshDiscovery(o.Now()), &kept)
					if err != nil {
						return err
					}
					if ok && regionKept(kept) {
						found = append(found, kept)
						progress(ProgressEvent{Phase: PhaseDiscovery, Account: t.Account, Region: region, From: FromCache, Workloads: len(kept.Workloads)})
						continue
					}
				}
				creds, err := credentials()
				if err != nil {
					return err
				}
				ports, err := o.PortsOf(ctx, t.Account, region, creds)
				if err != nil {
					return err
				}
				result, err := Region(ctx, ports, Where{Account: t.Account, Region: region})
				if err == nil {
					err = o.Cache.Put(path, result)
				}
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					result = RegionDiscovery{Account: t.Account, Region: region, Workloads: []Workload{}, Ineligible: []IneligibleRule{}, Error: awserr.Message(err)}
				}
				found = append(found, result)
				progress(ProgressEvent{Phase: PhaseDiscovery, Account: t.Account, Region: region, From: FromAWS, Workloads: len(result.Workloads), Error: result.Error})
			}
			return nil
		}()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// A refused role is already listed apart; any other failure of the
			// account is said, never dropped.
			said := awserr.Said(err)
			if o.Unreachable == nil || !o.Unreachable(t.Account) {
				found = append(found, RegionDiscovery{Account: t.Account, Region: "*", Workloads: []Workload{}, Ineligible: []IneligibleRule{}, Error: said})
			}
			accountProgress(AccountEvent{Account: t.Account, State: AccountDone, Regions: listed, Error: said})
			return found, nil
		}
		accountProgress(AccountEvent{Account: t.Account, State: AccountDone, Regions: listed})
		return found, nil
	}
	byAccount, err := limit.Map(ctx, o.Accounts, concurrency, account)
	if err != nil {
		return nil, err
	}
	regions := []RegionDiscovery{}
	for _, found := range byAccount {
		regions = append(regions, found...)
	}
	return regions, nil
}
