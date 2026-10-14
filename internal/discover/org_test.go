package discover

// The cases of manager packages/discovery/test/org.test.ts.

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// root ─ acc 1, acc 2 (suspended)
//
//	└ ou-a ─ acc 3
//	         └ ou-b ─ acc 4, acc 5, acc 6 (three accounts: two pages)
var orgOUs = map[string][]OrganizationalUnit{
	"r-1u0b":           {{ID: "ou-1u0b-aaaaaaaa", Name: "a"}},
	"ou-1u0b-aaaaaaaa": {{ID: "ou-1u0b-bbbbbbbb", Name: "b"}},
	"ou-1u0b-bbbbbbbb": {},
}

var orgAccounts = map[string][]OrgAccount{
	"r-1u0b":           {{ID: "100000000001", Name: "one", State: "ACTIVE"}, {ID: "100000000002", Name: "two", State: "SUSPENDED"}},
	"ou-1u0b-aaaaaaaa": {{ID: "100000000003", Name: "three", State: "ACTIVE"}},
	"ou-1u0b-bbbbbbbb": {{ID: "100000000004", Name: "four", State: "ACTIVE"}, {ID: "100000000005", Name: "five", State: "ACTIVE"}, {ID: "100000000006", Name: "six", State: "ACTIVE"}},
}

var orgTags = map[string][]Tag{"100000000004": {{Key: "env", Value: "dev"}}}

// fakeOrg pages by two, as the Node fake does.
type fakeOrg struct {
	mu       sync.Mutex
	calls    []string
	accounts func(parent string) []OrgAccount
	tags     func(id string) ([]Tag, error)
}

func paged[T any](items []T, token string) ([]T, string) {
	start, _ := strconv.Atoi(token)
	end := min(start+2, len(items))
	if start > len(items) {
		start = len(items)
	}
	next := ""
	if start+2 < len(items) {
		next = strconv.Itoa(start + 2)
	}
	return items[start:end], next
}

func (f *fakeOrg) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeOrg) ListRoots(context.Context, ListRootsInput) (ListRootsOutput, error) {
	f.record("roots")
	return ListRootsOutput{Roots: []Root{{ID: "r-1u0b", Name: "Root"}}}, nil
}

func (f *fakeOrg) ListOrganizationalUnitsForParent(_ context.Context, in ListOrganizationalUnitsForParentInput) (ListOrganizationalUnitsForParentOutput, error) {
	items, next := paged(orgOUs[in.ParentID], in.NextToken)
	return ListOrganizationalUnitsForParentOutput{OrganizationalUnits: items, NextToken: next}, nil
}

func (f *fakeOrg) ListAccountsForParent(_ context.Context, in ListAccountsForParentInput) (ListAccountsForParentOutput, error) {
	f.record(strings.TrimSpace(fmt.Sprintf("accounts %s %s", in.ParentID, in.NextToken)))
	all := orgAccounts[in.ParentID]
	if f.accounts != nil {
		all = f.accounts(in.ParentID)
	}
	items, next := paged(all, in.NextToken)
	return ListAccountsForParentOutput{Accounts: items, NextToken: next}, nil
}

func (f *fakeOrg) ListTagsForResource(_ context.Context, in ListTagsForResourceInput) (ListTagsForResourceOutput, error) {
	f.record("tags " + in.ResourceID)
	if f.tags != nil {
		tags, err := f.tags(in.ResourceID)
		return ListTagsForResourceOutput{Tags: tags}, err
	}
	return ListTagsForResourceOutput{Tags: orgTags[in.ResourceID]}, nil
}

func TestLoadInventoryWalksTheTreeFollowsPagesAndGivesTheOUPath(t *testing.T) {
	f := &fakeOrg{}
	inv, err := LoadInventory(context.Background(), f, false)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		ID   string
		Path []string
	}
	var got []row
	for _, a := range inv.Accounts {
		got = append(got, row{a.ID, a.OUPath})
	}
	a, b := "ou-1u0b-aaaaaaaa", "ou-1u0b-bbbbbbbb"
	want := []row{
		{"100000000001", []string{"r-1u0b"}},
		{"100000000003", []string{"r-1u0b", a}},
		{"100000000004", []string{"r-1u0b", a, b}},
		{"100000000005", []string{"r-1u0b", a, b}},
		{"100000000006", []string{"r-1u0b", a, b}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("accounts: got %v, want %v", got, want)
	}
	wantOUs := map[string]OU{"r-1u0b": {Name: "Root"}, a: {Name: "a", Parent: parent("r-1u0b")}, b: {Name: "b", Parent: parent(a)}}
	if !reflect.DeepEqual(inv.OUs, wantOUs) {
		t.Errorf("ous: got %v, want %v", inv.OUs, wantOUs)
	}
	joined := strings.Join(f.calls, "|")
	if !strings.Contains(joined, "accounts ou-1u0b-bbbbbbbb 2") {
		t.Errorf("the second page of accounts was not asked: %s", joined)
	}
	if strings.Contains(joined, "tags") {
		t.Errorf("tags read although not asked: %s", joined)
	}
}

func TestLoadInventoryReadsAccountTagsOnlyWhenAsked(t *testing.T) {
	inv, err := LoadInventory(context.Background(), &fakeOrg{}, true)
	if err != nil {
		t.Fatal(err)
	}
	tags := map[string]map[string]string{}
	for _, a := range inv.Accounts {
		tags[a.ID] = a.Tags
	}
	if !reflect.DeepEqual(tags["100000000004"], map[string]string{"env": "dev"}) || !reflect.DeepEqual(tags["100000000005"], map[string]string{}) {
		t.Errorf("tags: %v", tags)
	}
}

func TestAccountTagsAreReadFiveAccountsAtATime(t *testing.T) {
	var mu sync.Mutex
	running, peak := 0, 0
	f := &fakeOrg{
		accounts: func(parent string) []OrgAccount {
			if parent != "r-1u0b" {
				return nil
			}
			out := make([]OrgAccount, 23)
			for i := range out {
				out[i] = OrgAccount{ID: strconv.Itoa(200000000000 + i), Name: fmt.Sprintf("a%d", i), State: "ACTIVE"}
			}
			return out
		},
		tags: func(id string) ([]Tag, error) {
			mu.Lock()
			running++
			peak = max(peak, running)
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
			return []Tag{{Key: "id", Value: id}}, nil
		},
	}
	inv, err := LoadInventory(context.Background(), f, true)
	if err != nil {
		t.Fatal(err)
	}
	if peak != 5 {
		t.Errorf("peak %d, want 5", peak)
	}
	if len(inv.Accounts) != 23 {
		t.Fatalf("%d accounts", len(inv.Accounts))
	}
	for _, a := range inv.Accounts {
		if a.Tags["id"] != a.ID {
			t.Errorf("account %s has tags %v", a.ID, a.Tags)
		}
	}
}

func TestLoadInventoryReturnsAPortsError(t *testing.T) {
	boom := fmt.Errorf("throttled")
	f := &fakeOrg{tags: func(string) ([]Tag, error) { return nil, boom }}
	if _, err := LoadInventory(context.Background(), f, true); err != boom {
		t.Errorf("got %v, want %v", err, boom)
	}
}

// The walk says how far it is after each organizational unit (the user, 2026-10-14: a large organization takes a while):
// the units read, of those found so far (the root included), and the active accounts found.
func TestLoadInventoryProgressAfterEachOrganizationalUnit(t *testing.T) {
	var got []InventoryProgress
	inv, err := LoadInventoryProgress(context.Background(), &fakeOrg{}, false, func(p InventoryProgress) { got = append(got, p) })
	if err != nil {
		t.Fatal(err)
	}
	want := []InventoryProgress{{Read: 1, Found: 2, Accounts: 1}, {Read: 2, Found: 3, Accounts: 2}, {Read: 3, Found: 3, Accounts: 5}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("progress %+v, want %+v", got, want)
	}
	if len(inv.Accounts) != 5 {
		t.Fatalf("accounts %d", len(inv.Accounts))
	}
	// Without a callback, as LoadInventory.
	if _, err := LoadInventoryProgress(context.Background(), &fakeOrg{}, false, nil); err != nil {
		t.Fatal(err)
	}
}
