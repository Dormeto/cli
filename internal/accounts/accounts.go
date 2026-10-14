// Package accounts says which accounts an estimate reaches (vision
// docs/specs/2026-10-10-savings-estimate-design.md §4, decision 4): an
// organization's active accounts when the profile may list them, else its own
// account only, and why. The profile's own account uses its own credentials;
// the others a role given or supposed (OrganizationAccountAccessRole), assumed
// when an account's turn comes (the sessions last an hour) — a refusal lists the
// account apart with AWS's reason and the fix. Restricted to the accounts given
// with --accounts: one not reached is listed apart with why; none of them,
// refused. Several profiles (such as one SSO profile per account): each reaches
// its accounts, merged; an account a profile logs in to directly is reached
// with it, never through another's role; --accounts restricts the merged
// accounts.
//
// Ports manager packages/estimate/src/accounts.ts (AccountsPorts,
// reachAccounts, reachProfiles).
package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/options"
)

// Ports are a profile's calls that reach accounts (Node's AccountsPorts): its
// own account (GetCallerIdentity), its organization (DescribeOrganization),
// the organization's active accounts (the inventory) and a role assumed in an
// account (AssumeRole).
type Ports interface {
	Whoami(ctx context.Context) (string, error)
	Organization(ctx context.Context) (string, error)
	// Inventory walks the organization, saying how far it is (progress nil: silent).
	Inventory(ctx context.Context, progress func(discover.InventoryProgress)) (discover.OrgInventory, error)
	Assume(ctx context.Context, account, role string) (aws.Credentials, error)
}

// ReachedAccount is an account reached. Own: the profile's own account, whose
// Credentials are nil (the profile's own); Profile: the profile reaching it.
type ReachedAccount struct {
	Account string `json:"account"`
	Name    string `json:"name"`
	Own     bool   `json:"own,omitempty"`
	Profile string `json:"profile,omitempty"`
	// Credentials are the account's: nil for the profile's own account, else
	// its role's, assumed when first asked and again five minutes before they
	// expire; a refusal is kept and said once.
	Credentials func(ctx context.Context) (*aws.Credentials, error) `json:"-"`
}

// Unreachable is an account not reached, why, and how to include it.
type Unreachable struct {
	Account string `json:"account"`
	Name    string `json:"name"`
	Reason  string `json:"reason"`
	Fix     string `json:"fix"`
}

// Unreachables lists the accounts not reached: those --accounts names that are
// not, and those refusing their role, as their turn comes. Safe for concurrent
// use; written as a JSON array.
type Unreachables struct {
	mu   sync.Mutex
	list []Unreachable
}

// List is a copy of the accounts listed so far.
func (u *Unreachables) List() []Unreachable {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]Unreachable{}, u.list...)
}

func (u *Unreachables) add(items ...Unreachable) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.list = append(u.list, items...)
}

// MarshalJSON writes the list.
func (u *Unreachables) MarshalJSON() ([]byte, error) { return json.Marshal(u.List()) }

// Reached is what an estimate reaches: the organization (nil when none, or
// several profiles in different ones), why only some accounts are (nil when
// all), the accounts, those not reached, and Listed, how many were reached
// before --accounts restricted them.
type Reached struct {
	Organization *string          `json:"organization"`
	Why          *string          `json:"why"`
	Accounts     []ReachedAccount `json:"accounts"`
	Unreachable  *Unreachables    `json:"unreachable"`
	Listed       int              `json:"listed"`
}

// Options of Reach and ReachProfiles. Only: the accounts given with --accounts
// (nil: every account reached). Now: the clock (time.Now when nil).
// Unreachable: where the accounts refusing their role are listed, shared by
// several profiles (a new list when nil).
type Options struct {
	Role        string
	Only        []string
	Now         func() time.Time
	Unreachable *Unreachables
	// Profile names the profile in what is said; Say says each step (nil: nothing); Listing passes the organization's
	// walk on as it goes (nil: nothing). The user, 2026-10-14: a large organization took a while without a word.
	Profile string
	Say     func(line string)
	Listing func(discover.InventoryProgress)
}

// Profile is a profile and its ports.
type Profile struct {
	Profile string
	Ports   Ports
}

// renew is how close to their expiry a role's credentials are assumed again
// (sessions last an hour; a big organization's run lasts longer).
const renew = 5 * time.Minute

const every = "a profile of the management account or of a delegated administrator estimates every account"

// missing says why an account given with --accounts is not reached, and the
// refusal when none is.
type missing struct {
	reason, fix string
	refusal     func(ids []string) string
}

// restrict keeps the accounts given among those reached, in their order; the
// others are listed apart (reason, fix), or refused when none is.
func restrict(r Reached, only []string, m missing) (Reached, error) {
	r.Listed = len(r.Accounts)
	if only == nil {
		return r, nil
	}
	accounts := []ReachedAccount{}
	for _, a := range r.Accounts {
		if slices.Contains(only, a.Account) {
			accounts = append(accounts, a)
		}
	}
	var absent []string
	for _, id := range only {
		if !slices.ContainsFunc(r.Accounts, func(a ReachedAccount) bool { return a.Account == id }) {
			absent = append(absent, id)
		}
	}
	if len(accounts) == 0 {
		return Reached{}, &options.UsageError{Message: fmt.Sprintf("--accounts: %s: %s", strings.Join(absent, ", "), m.refusal(absent))}
	}
	for _, id := range absent {
		r.Unreachable.add(Unreachable{Account: id, Name: "", Reason: m.reason, Fix: m.fix})
	}
	r.Accounts = accounts
	return r, nil
}

func own(self, name string) ReachedAccount {
	return ReachedAccount{Account: self, Name: name, Own: true, Credentials: func(context.Context) (*aws.Credentials, error) { return nil, nil }}
}

// assumedRole is an account's role: assumed when first asked, kept until five minutes
// before it expires; a refusal is listed apart once and kept. Concurrent asks
// share one AssumeRole.
type assumedRole struct {
	mu      sync.Mutex
	asked   bool
	refused bool
	creds   aws.Credentials
	err     error
	until   time.Time // zero: never expires
}

func (r *assumedRole) credentials(ctx context.Context, ports Ports, account discover.Account, name string, o Options) (*aws.Credentials, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refused {
		return nil, r.err
	}
	if r.asked && (r.until.IsZero() || o.Now().Before(r.until.Add(-renew))) {
		c := r.creds
		return &c, nil
	}
	c, err := ports.Assume(ctx, account.ID, name)
	if err != nil {
		if ctx.Err() != nil {
			// The run was stopped: no verdict on the role.
			return nil, err
		}
		r.refused, r.err = true, err
		o.Unreachable.add(Unreachable{
			Account: account.ID, Name: account.Name, Reason: awserr.Message(err),
			Fix: fmt.Sprintf("create a role this profile may assume, with read access, and name it with --role (tried %s)", name),
		})
		return nil, err
	}
	r.asked, r.creds, r.until = true, c, time.Time{}
	if c.CanExpire {
		r.until = c.Expires
	}
	return &c, nil
}

func defaults(o Options) Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Say == nil {
		o.Say = func(string) {}
	}
	if o.Unreachable == nil {
		o.Unreachable = &Unreachables{}
	}
	return o
}

// Reach gives the accounts a profile reaches (Node's reachAccounts).
func Reach(ctx context.Context, ports Ports, o Options) (Reached, error) {
	o = defaults(o)
	profile := o.Profile
	if profile == "" {
		profile = "given"
	}
	o.Say(fmt.Sprintf("Reading the profile %s: its account and its organization", profile))
	self, err := ports.Whoami(ctx)
	if err != nil {
		return Reached{}, err
	}
	// Its own account only, and why.
	alone := func(organization *string, why, reason, fix string) (Reached, error) {
		return restrict(Reached{Organization: organization, Why: &why, Accounts: []ReachedAccount{own(self, self)}, Unreachable: o.Unreachable}, o.Only,
			missing{reason: reason, fix: fix, refusal: func([]string) string { return why }})
	}
	organization, err := ports.Organization(ctx)
	if err != nil {
		if awserr.Name(err) == "AWSOrganizationsNotInUseException" {
			return alone(nil, fmt.Sprintf("not in an organization: only the account %s is estimated", self), "not in an organization", "estimate it with a profile of its own")
		}
		// Not allowed to describe it (a narrow read role): its own account, and why; anything else is the profile's failure.
		if !strings.Contains(awserr.Name(err), "AccessDenied") {
			return Reached{}, err
		}
		return alone(nil, fmt.Sprintf("this profile cannot describe its organization (%s): only its account %s is estimated", awserr.Said(err), self), "this profile cannot describe its organization", every)
	}
	o.Say(fmt.Sprintf("Profile %s: account %s in the organization %s; listing the organization's accounts", profile, self, organization))
	start := o.Now()
	inventory, err := ports.Inventory(ctx, o.Listing)
	if err == nil {
		o.Say(fmt.Sprintf("Listed %s in %s (%s)", count(len(inventory.Accounts), "active account"), count(len(inventory.OUs), "organizational unit"), clock(o.Now().Sub(start))))
	}
	if err != nil {
		return alone(&organization, fmt.Sprintf("this profile cannot list the organization's accounts (%s): only its account %s is estimated; %s", awserr.Said(err), self, every), "this profile cannot list the organization's accounts", every)
	}
	accounts := make([]ReachedAccount, 0, len(inventory.Accounts))
	for _, a := range inventory.Accounts {
		if a.ID == self {
			accounts = append(accounts, own(self, a.Name))
			continue
		}
		r := &assumedRole{}
		accounts = append(accounts, ReachedAccount{Account: a.ID, Name: a.Name, Credentials: func(ctx context.Context) (*aws.Credentials, error) {
			return r.credentials(ctx, ports, a, o.Role, o)
		}})
	}
	return restrict(Reached{Organization: &organization, Accounts: accounts, Unreachable: o.Unreachable}, o.Only, missing{
		reason: "not an active account of the organization " + organization,
		fix:    "check the id given with --accounts: the organization's active accounts are estimated",
		refusal: func(ids []string) string {
			if len(ids) == 1 {
				return "not an active account of the organization " + organization
			}
			return "not active accounts of the organization " + organization
		},
	})
}

// count is "1 active account", "3 active accounts".
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// clock is a duration as minutes and seconds, "0:37" (the progress lines' format).
func clock(d time.Duration) string {
	s := max(int(d/time.Second), 0)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func tag(accounts []ReachedAccount, profile string) []ReachedAccount {
	out := make([]ReachedAccount, len(accounts))
	for i, a := range accounts {
		a.Profile = profile
		out[i] = a
	}
	return out
}

// ReachProfiles gives the accounts several profiles reach, merged in their
// order: an account reached by several, with the profile whose own account it
// is when one is (and the name another knows); the organization when they
// share one; each profile's why. One profile: as reached alone (Node's
// reachProfiles). A profile that fails names itself in the error's message.
func ReachProfiles(ctx context.Context, profiles []Profile, o Options) (Reached, error) {
	o = defaults(o)
	if len(profiles) == 1 {
		o.Profile = profiles[0].Profile
		r, err := Reach(ctx, profiles[0].Ports, o)
		if err != nil {
			return Reached{}, err
		}
		r.Accounts = tag(r.Accounts, profiles[0].Profile)
		return r, nil
	}
	type reached struct {
		profile string
		reached Reached
	}
	var each []reached
	for _, p := range profiles {
		r, err := Reach(ctx, p.Ports, Options{Role: o.Role, Now: o.Now, Unreachable: o.Unreachable, Profile: p.Profile, Say: o.Say, Listing: o.Listing})
		if err != nil {
			// Which profile failed, such as an expired SSO session
			return Reached{}, awserr.Prefixed(p.Profile, err)
		}
		each = append(each, reached{p.Profile, r})
	}
	var order []string
	byID := map[string]ReachedAccount{}
	for _, e := range each {
		for _, account := range tag(e.reached.Accounts, e.profile) {
			known, ok := byID[account.Account]
			if !ok {
				order = append(order, account.Account)
			}
			// A profile that cannot list its organization knows its own account by its id only: another may know its name
			if !ok || (!known.Own && account.Own) {
				if account.Name == account.Account && ok {
					account.Name = known.Name
				}
				byID[account.Account] = account
			}
		}
	}
	accounts := make([]ReachedAccount, 0, len(order))
	for _, id := range order {
		accounts = append(accounts, byID[id])
	}
	var organizations, whys, names []string
	for _, e := range each {
		if org := e.reached.Organization; org != nil && *org != "" && !slices.Contains(organizations, *org) {
			organizations = append(organizations, *org)
		}
		if e.reached.Why != nil && *e.reached.Why != "" {
			whys = append(whys, e.profile+": "+*e.reached.Why)
		}
	}
	if len(organizations) > 1 {
		whys = append(whys, "the profiles are in several organizations: "+strings.Join(organizations, ", "))
	}
	for _, p := range profiles {
		names = append(names, p.Profile)
	}
	merged := Reached{Accounts: accounts, Unreachable: o.Unreachable}
	if len(organizations) == 1 {
		merged.Organization = &organizations[0]
	}
	if len(whys) > 0 {
		why := strings.Join(whys, "; ")
		merged.Why = &why
	}
	none := fmt.Sprintf("none of the profiles %s reaches it", strings.Join(names, ", "))
	return restrict(merged, o.Only, missing{
		reason: none, fix: "add a profile of this account to --profile, or use a profile of the management account, which reaches every account",
		refusal: func([]string) string { return none },
	})
}
