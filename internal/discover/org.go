package discover

// Organization inventory through the read-only Organizations delegation: the
// OU tree, the active accounts with their OU path (root first) and, when a
// criterion needs them, their tags.
//
// Ports manager packages/discovery/src/org.ts.

import (
	"context"
	"slices"
	"sync"
)

// tagReads is how many accounts' tags are read at a time: Organizations allows
// 10 calls a second (the SDK retries a throttled one).
const tagReads = 5

// pages follows a list call's NextToken until it is empty.
func pages[T any](call func(token string) ([]T, string, error)) ([]T, error) {
	var items []T
	token := ""
	for {
		page, next, err := call(token)
		if err != nil {
			return nil, err
		}
		items = append(items, page...)
		if token = next; token == "" {
			return items, nil
		}
	}
}

// LoadInventory reads the organization's OU tree and its active accounts, each
// with its OU path from the root; with tags, their tags too.
func LoadInventory(ctx context.Context, org OrganizationsPort, tags bool) (OrgInventory, error) {
	roots, err := pages(func(t string) ([]Root, string, error) {
		out, err := org.ListRoots(ctx, ListRootsInput{NextToken: t})
		return out.Roots, out.NextToken, err
	})
	if err != nil {
		return OrgInventory{}, err
	}
	type node struct {
		id   string
		path []string
	}
	inv := OrgInventory{Accounts: []Account{}, OUs: map[string]OU{}}
	var queue []node
	for _, root := range roots {
		if root.ID == "" {
			continue
		}
		name := root.Name
		if name == "" {
			name = "Root"
		}
		inv.OUs[root.ID] = OU{Name: name}
		queue = append(queue, node{root.ID, []string{root.ID}})
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		members, err := pages(func(t string) ([]OrgAccount, string, error) {
			out, err := org.ListAccountsForParent(ctx, ListAccountsForParentInput{ParentID: cur.id, NextToken: t})
			return out.Accounts, out.NextToken, err
		})
		if err != nil {
			return OrgInventory{}, err
		}
		for _, a := range members {
			// State replaces the deprecated Status; only active accounts can be scanned.
			state := a.State
			if state == "" {
				state = a.Status
			}
			if a.ID == "" || state != "ACTIVE" {
				continue
			}
			name := a.Name
			if name == "" {
				name = a.ID
			}
			inv.Accounts = append(inv.Accounts, Account{ID: a.ID, Name: name, OUPath: cur.path, Tags: map[string]string{}})
		}
		children, err := pages(func(t string) ([]OrganizationalUnit, string, error) {
			out, err := org.ListOrganizationalUnitsForParent(ctx, ListOrganizationalUnitsForParentInput{ParentID: cur.id, NextToken: t})
			return out.OrganizationalUnits, out.NextToken, err
		})
		if err != nil {
			return OrgInventory{}, err
		}
		for _, ou := range children {
			if ou.ID == "" {
				continue
			}
			name := ou.Name
			if name == "" {
				name = ou.ID
			}
			parent := cur.id
			inv.OUs[ou.ID] = OU{Name: name, Parent: &parent}
			queue = append(queue, node{ou.ID, append(slices.Clip(cur.path), ou.ID)})
		}
	}
	if tags {
		if err := readTags(ctx, org, inv.Accounts); err != nil {
			return OrgInventory{}, err
		}
	}
	return inv, nil
}

// readTags reads the accounts' tags, tagReads accounts at a time; the first
// error stops the reads and is returned.
func readTags(ctx context.Context, org OrganizationsPort, accounts []Account) error {
	var (
		mu       sync.Mutex
		next     int
		firstErr error
		wg       sync.WaitGroup
	)
	take := func() (int, bool) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr != nil || next >= len(accounts) {
			return 0, false
		}
		next++
		return next - 1, true
	}
	for range min(tagReads, len(accounts)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i, ok := take()
				if !ok {
					return
				}
				list, err := pages(func(t string) ([]Tag, string, error) {
					out, err := org.ListTagsForResource(ctx, ListTagsForResourceInput{ResourceID: accounts[i].ID, NextToken: t})
					return out.Tags, out.NextToken, err
				})
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
				} else {
					m := make(map[string]string, len(list))
					for _, tg := range list {
						m[tg.Key] = tg.Value
					}
					accounts[i].Tags = m
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return firstErr
}
