package discover

// Phase 1 of the estimate's standalone part (vision standalone workloads design §7): the standalone scan's candidates
// of a region, each eligible or managed one described as the router would (what a nap of its resource stops), the
// ineligible ones with the scan's reasons, and what the scan could not read. Kept apart from the region's HTTP
// discovery (its own cache file), so an estimate without the standalone resources reads nothing of them.

import (
	"context"

	"github.com/Dormeto/cli/internal/limit"
	"github.com/Dormeto/cli/internal/stopped"
)

// StandaloneResource is an eligible (or managed) standalone candidate of a region, described: its resource (Kind one of
// the resource kinds, ID its backend id: an instance id, a group's name, a service's ARN, a database's identifier),
// what a nap stops, its tags.
type StandaloneResource struct {
	Account    string              `json:"account"`
	Region     string              `json:"region"`
	Kind       string              `json:"kind"`
	ID         string              `json:"id"`
	ARN        string              `json:"arn"`
	Name       string              `json:"name"`
	Tags       map[string][]string `json:"tags"`
	Managed    bool                `json:"managed,omitempty"`
	WorkloadID string              `json:"workloadId,omitempty"`
	Stopped    []stopped.Item      `json:"stopped"`
	// An instance's state when discovered (not one Dormeto manages).
	InstanceState string `json:"instanceState,omitempty"`
}

// StandaloneIneligible is a standalone candidate the router would refuse, with the scan's reason.
type StandaloneIneligible struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	ARN    string `json:"arn"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// StandaloneRegion is what the standalone scan kept of an account and region (the cache's value): its resources, its
// ineligible candidates, and what it could not read (the scan's report).
type StandaloneRegion struct {
	Account    string                 `json:"account"`
	Region     string                 `json:"region"`
	Resources  []StandaloneResource   `json:"resources"`
	Ineligible []StandaloneIneligible `json:"ineligible"`
	Problems   []string               `json:"problems"`
	Unread     []string               `json:"unread"`
}

// standaloneBackend is what a nap of a standalone resource stops, as the router describes it: an instance (its state,
// unless Dormeto manages it), a group's desired capacity and instances, a service's cluster and desired count, a
// database.
func standaloneBackend(ctx context.Context, ports RegionPorts, rule StandaloneRule) (backendNow, error) {
	switch rule.Backend.Kind {
	case KindRDSInstance:
		return backendNow{running: &stopped.Backend{Kind: stopped.BackendRDSInstance, ID: rule.Backend.ID}}, nil
	case KindRDSCluster:
		return backendNow{running: &stopped.Backend{Kind: stopped.BackendRDSCluster, ID: rule.Backend.ID}}, nil
	}
	return routerBackend(ctx, ports, Backend{Kind: rule.Backend.Kind, ID: rule.Backend.ID}, rule.Eligibility == Managed)
}

// describeStandalone describes the eligible and managed candidates (eight at a time, in the scan's order): a
// description that fails is an item that cannot be priced, said in the estimate.
func describeStandalone(ctx context.Context, ports RegionPorts, where Where, scan StandaloneScan) (StandaloneRegion, error) {
	found := StandaloneRegion{Account: where.Account, Region: where.Region, Resources: []StandaloneResource{}, Ineligible: []StandaloneIneligible{},
		Problems: append([]string{}, scan.Report.Problems...), Unread: append([]string{}, scan.Report.Unread...)}
	var eligible []StandaloneRule
	for _, c := range scan.Rules {
		if c.Eligibility == Ineligible {
			found.Ineligible = append(found.Ineligible, StandaloneIneligible{Kind: c.Backend.Kind, ID: c.Backend.ID, ARN: c.ResourceArn, Name: c.Name, Reason: c.Reason})
			continue
		}
		eligible = append(eligible, c)
	}
	described, err := limit.Map(ctx, eligible, workloadsAtOnce, func(ctx context.Context, c StandaloneRule) (StandaloneResource, error) {
		r := StandaloneResource{Account: where.Account, Region: where.Region, Kind: c.Backend.Kind, ID: c.Backend.ID, ARN: c.ResourceArn, Name: c.Name,
			Tags: c.Tags, Managed: c.Eligibility == Managed, WorkloadID: c.WorkloadID}
		if r.Tags == nil {
			r.Tags = map[string][]string{}
		}
		var items []stopped.Item
		err := func() error {
			now, err := standaloneBackend(ctx, ports, c)
			if err != nil {
				return err
			}
			r.InstanceState = now.instanceState
			if now.running != nil {
				items, err = stopped.Describe(ctx, ports.Stopped, []stopped.Backend{*now.running})
			}
			return err
		}()
		if err != nil {
			if ctx.Err() != nil {
				return StandaloneResource{}, ctx.Err()
			}
			items = []stopped.Item{unknownItem(c.Backend.Kind+":"+c.Backend.ID, err)}
		}
		if items == nil {
			items = []stopped.Item{}
		}
		r.Stopped = items
		return r, nil
	})
	if err != nil {
		return StandaloneRegion{}, err
	}
	found.Resources = append(found.Resources, described...)
	return found, ctx.Err()
}
