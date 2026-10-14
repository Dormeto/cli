package accounts

// The cases of manager packages/estimate/test/accounts.test.ts: the accounts an
// estimate reaches (spec §4, decision 4): an organization's active accounts,
// the profile's own with its own credentials, the others through a role given
// or supposed, assumed when an account's turn comes.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"

	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/options"
)

const (
	self         = "111111111111"
	role         = "OrganizationAccountAccessRole"
	everyAccount = "a profile of the management account or of a delegated administrator estimates every account"
)

var creds = aws.Credentials{AccessKeyID: "AKIA", SecretAccessKey: "s", SessionToken: "t"}

func named(id, name string) discover.Account {
	return discover.Account{ID: id, Name: name, OUPath: []string{"r-root"}, Tags: map[string]string{}}
}

// awsError is a failed call as the AWS SDK returns it.
func awsError(name, message string) error {
	return &smithy.GenericAPIError{Code: name, Message: message}
}

var cannotListError = awsError("AccessDeniedException", "You don't have permissions to access this resource.")

type fake struct {
	self         string
	whoErr       error
	organization string
	orgErr       error
	inventory    []discover.Account
	invErr       error
	refuse       map[string]bool
	expiration   func() time.Time
	walk         []discover.InventoryProgress // what the inventory says as it goes
	ous          map[string]discover.OU

	mu      sync.Mutex
	assumed [][2]string
}

// ports is Node's ports(): an organization of three accounts, the profile's the management one.
func ports() *fake {
	return &fake{
		self: self, organization: "o-abc123",
		inventory: []discover.Account{named(self, "management"), named("222222222222", "prod"), named("333333333333", "staging")},
	}
}

func (f *fake) Whoami(context.Context) (string, error) { return f.self, f.whoErr }

func (f *fake) Organization(context.Context) (string, error) { return f.organization, f.orgErr }

func (f *fake) Inventory(_ context.Context, progress func(discover.InventoryProgress)) (discover.OrgInventory, error) {
	for _, p := range f.walk {
		if progress != nil {
			progress(p)
		}
	}
	if f.invErr != nil {
		return discover.OrgInventory{}, f.invErr
	}
	ous := f.ous
	if ous == nil {
		ous = map[string]discover.OU{}
	}
	return discover.OrgInventory{Accounts: f.inventory, OUs: ous}, nil
}

func (f *fake) Assume(_ context.Context, account, role string) (aws.Credentials, error) {
	f.mu.Lock()
	f.assumed = append(f.assumed, [2]string{account, role})
	f.mu.Unlock()
	if f.refuse[account] {
		return aws.Credentials{}, awsError("AccessDenied", "User: arn:aws:iam::"+self+":user/me is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::"+account+":role/"+role)
	}
	if f.expiration != nil {
		c := creds
		c.CanExpire, c.Expires = true, f.expiration()
		return c, nil
	}
	return creds, nil
}

func (f *fake) calls() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string{}, f.assumed...)
}

// cannotList is a profile that may not list its organization's accounts.
func cannotList(self string) *fake {
	return &fake{self: self, organization: "o-abc123", invErr: cannotListError}
}

func mustReach(t *testing.T, p Ports, o Options) Reached {
	t.Helper()
	r, err := Reach(context.Background(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustReachProfiles(t *testing.T, profiles []Profile, o Options) Reached {
	t.Helper()
	r, err := ReachProfiles(context.Background(), profiles, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func credentialsOf(t *testing.T, a ReachedAccount) *aws.Credentials {
	t.Helper()
	c, err := a.Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\ngot  %#v\nwant %#v", what, got, want)
	}
}

func str(s string) *string { return &s }

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func idsAndNames(r Reached) [][2]string {
	out := [][2]string{}
	for _, a := range r.Accounts {
		out = append(out, [2]string{a.Account, a.Name})
	}
	return out
}

func ids(r Reached) []string {
	out := []string{}
	for _, a := range r.Accounts {
		out = append(out, a.Account)
	}
	return out
}

func refusal(t *testing.T, err error) string {
	t.Helper()
	var u *options.UsageError
	if !errors.As(err, &u) {
		t.Fatalf("a usage error is expected: %#v", err)
	}
	return u.Message
}

func TestAnOrganizationItsActiveAccountsTheProfilesOwnWithItsOwnCredentialsTheOthersWhenTheirTurnComes(t *testing.T) {
	p := ports()
	r := mustReach(t, p, Options{Role: role})
	eq(t, "organization", deref(r.Organization), "o-abc123")
	eq(t, "why", r.Why, (*string)(nil))
	eq(t, "unreachable", r.Unreachable.List(), []Unreachable{})
	eq(t, "listed", r.Listed, 3)
	eq(t, "accounts", idsAndNames(r), [][2]string{{self, "management"}, {"222222222222", "prod"}, {"333333333333", "staging"}})
	// Nothing assumed until an account's turn; the management account never.
	eq(t, "assumed", p.calls(), [][2]string{})
	if c := credentialsOf(t, r.Accounts[0]); c != nil {
		t.Errorf("the profile's own account uses its own credentials: %+v", c)
	}
	eq(t, "credentials", *credentialsOf(t, r.Accounts[1]), creds)
	credentialsOf(t, r.Accounts[1])
	eq(t, "assumed", p.calls(), [][2]string{{"222222222222", role}})
}

func TestAProfileThatCannotListTheOrganizationItsOwnAccountOnlyAndWhy(t *testing.T) {
	p := ports()
	p.invErr = cannotListError
	r := mustReach(t, p, Options{Role: role})
	eq(t, "accounts", ids(r), []string{self})
	eq(t, "organization", deref(r.Organization), "o-abc123")
	eq(t, "why", deref(r.Why), "this profile cannot list the organization's accounts (AccessDeniedException: You don't have permissions to access this resource.): only its account "+self+" is estimated; "+everyAccount)
}

func TestAnAccountOutsideAnyOrganizationItsOwnAccountOnly(t *testing.T) {
	p := ports()
	p.orgErr = awsError("AWSOrganizationsNotInUseException", "Your account is not a member of an organization.")
	r := mustReach(t, p, Options{Role: role})
	eq(t, "organization", r.Organization, (*string)(nil))
	eq(t, "why", deref(r.Why), "not in an organization: only the account "+self+" is estimated")
	eq(t, "accounts", ids(r), []string{self})
}

func TestAProfileThatMayNotDescribeItsOrganizationItsOwnAccountOnlyAndWhy(t *testing.T) {
	p := ports()
	p.orgErr = awsError("AccessDeniedException", "You are not authorized to perform: organizations:DescribeOrganization")
	r := mustReach(t, p, Options{Role: role})
	eq(t, "organization", r.Organization, (*string)(nil))
	eq(t, "why", deref(r.Why), "this profile cannot describe its organization (AccessDeniedException: You are not authorized to perform: organizations:DescribeOrganization): only its account "+self+" is estimated")
	eq(t, "accounts", ids(r), []string{self})
}

func TestARolesCredentialsAreAssumedAgainWhenTheyAreAboutToExpire(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	p := ports()
	p.expiration = func() time.Time { return now.Add(time.Hour) }
	r := mustReach(t, p, Options{Role: role, Now: func() time.Time { return now }})
	credentialsOf(t, r.Accounts[1])
	now = now.Add(30 * time.Minute)
	credentialsOf(t, r.Accounts[1])
	eq(t, "assumed once", len(p.calls()), 1)
	// Five minutes from expiring: assumed again.
	now = now.Add(26 * time.Minute)
	credentialsOf(t, r.Accounts[1])
	eq(t, "assumed again", len(p.calls()), 2)
}

func TestARoleThatCannotBeAssumedTheAccountIsListedApartWithAWSsReasonAndTheFix(t *testing.T) {
	p := ports()
	p.refuse = map[string]bool{"333333333333": true}
	r := mustReach(t, p, Options{Role: role})
	_, err := r.Accounts[2].Credentials(context.Background())
	if !strings.Contains(awserr.Message(err), "not authorized to perform: sts:AssumeRole") {
		t.Fatalf("%v", err)
	}
	eq(t, "unreachable", r.Unreachable.List(), []Unreachable{{
		Account: "333333333333", Name: "staging",
		Reason: "User: arn:aws:iam::" + self + ":user/me is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::333333333333:role/OrganizationAccountAccessRole",
		Fix:    "create a role this profile may assume, with read access, and name it with --role (tried OrganizationAccountAccessRole)",
	}})
	// Asked again, the same refusal, said once.
	if _, again := r.Accounts[2].Credentials(context.Background()); again == nil {
		t.Fatal("the refusal is kept")
	}
	eq(t, "said once", len(r.Unreachable.List()), 1)
	eq(t, "assumed once", len(p.calls()), 1)
}

func TestTheOrganizationsAccountsGivenInItsOrderOneItDoesNotHaveIsListedApartNothingAssumedElsewhere(t *testing.T) {
	p := ports()
	r := mustReach(t, p, Options{Role: role, Only: []string{"333333333333", "999999999999", self}})
	eq(t, "accounts", idsAndNames(r), [][2]string{{self, "management"}, {"333333333333", "staging"}})
	eq(t, "organization", deref(r.Organization), "o-abc123")
	eq(t, "why", r.Why, (*string)(nil))
	eq(t, "listed", r.Listed, 3)
	eq(t, "unreachable", r.Unreachable.List(), []Unreachable{{
		Account: "999999999999", Name: "", Reason: "not an active account of the organization o-abc123",
		Fix: "check the id given with --accounts: the organization's active accounts are estimated",
	}})
	credentialsOf(t, r.Accounts[1])
	eq(t, "assumed", p.calls(), [][2]string{{"333333333333", role}})
}

func TestNoneOfTheAccountsGivenInTheOrganizationRefusedSayingWhy(t *testing.T) {
	_, err := Reach(context.Background(), ports(), Options{Role: role, Only: []string{"999999999999", "888888888888"}})
	eq(t, "refusal", refusal(t, err), "--accounts: 999999999999, 888888888888: not active accounts of the organization o-abc123")
}

func TestAProfileThatCannotListTheOrganizationItsOwnAccountIfGivenTheOthersListedApartWithWhy(t *testing.T) {
	p := ports()
	p.invErr = cannotListError
	r := mustReach(t, p, Options{Role: role, Only: []string{self, "222222222222"}})
	eq(t, "accounts", ids(r), []string{self})
	eq(t, "listed", r.Listed, 1)
	eq(t, "unreachable", r.Unreachable.List(), []Unreachable{{
		Account: "222222222222", Name: "", Reason: "this profile cannot list the organization's accounts", Fix: everyAccount,
	}})
}

func TestAProfileThatEstimatesOnlyItsOwnAccountGivenOthersAloneRefusedSayingWhy(t *testing.T) {
	p := ports()
	p.orgErr = awsError("AWSOrganizationsNotInUseException", "Your account is not a member of an organization.")
	_, err := Reach(context.Background(), p, Options{Role: role, Only: []string{"222222222222"}})
	eq(t, "refusal", refusal(t, err), "--accounts: 222222222222: not in an organization: only the account "+self+" is estimated")
}

// The accounts several profiles reach (--profile a,b).

const other = "222222222222"

func TestOneProfileAsReachedAloneEachAccountKnowingItsProfile(t *testing.T) {
	r := mustReachProfiles(t, []Profile{{Profile: "mgmt", Ports: ports()}}, Options{Role: role, Only: []string{"333333333333"}})
	alone := mustReach(t, ports(), Options{Role: role, Only: []string{"333333333333"}})
	eq(t, "organization", deref(r.Organization), deref(alone.Organization))
	eq(t, "why", r.Why, alone.Why)
	eq(t, "listed", r.Listed, alone.Listed)
	eq(t, "unreachable", r.Unreachable.List(), alone.Unreachable.List())
	eq(t, "accounts", ids(r), ids(alone))
	eq(t, "profiles", []string{r.Accounts[0].Profile}, []string{"mgmt"})
}

func TestProfilesThatCannotListTheOrganizationEachItsOwnAccountWithItsOwnCredentialsNoneListedApart(t *testing.T) {
	qal, prd := cannotList(self), cannotList(other)
	r := mustReachProfiles(t, []Profile{{"qal", qal}, {"prd", prd}}, Options{Role: role, Only: []string{self, other}})
	got := [][2]string{}
	for _, a := range r.Accounts {
		got = append(got, [2]string{a.Account, a.Profile})
	}
	eq(t, "accounts", got, [][2]string{{self, "qal"}, {other, "prd"}})
	eq(t, "organization", deref(r.Organization), "o-abc123")
	eq(t, "unreachable", r.Unreachable.List(), []Unreachable{})
	eq(t, "listed", r.Listed, 2)
	if c := credentialsOf(t, r.Accounts[1]); c != nil {
		t.Errorf("its own credentials: %+v", c)
	}
	eq(t, "assumed", append(qal.calls(), prd.calls()...), [][2]string{})
	// Why each profile reaches its account only
	eq(t, "why", deref(r.Why), "qal: this profile cannot list the organization's accounts (AccessDeniedException: You don't have permissions to access this resource.): only its account "+self+" is estimated; "+everyAccount+"; prd: this profile cannot list the organization's accounts (AccessDeniedException: You don't have permissions to access this resource.): only its account "+other+" is estimated; "+everyAccount)
}

func TestAnAccountAnotherProfileLogsInToDirectlyReachedWithThatProfileNoRoleAssumedForIt(t *testing.T) {
	mgmt, prd := ports(), cannotList(other)
	r := mustReachProfiles(t, []Profile{{"mgmt", mgmt}, {"prd", prd}}, Options{Role: role})
	got := [][3]string{}
	for _, a := range r.Accounts {
		got = append(got, [3]string{a.Account, a.Name, a.Profile})
	}
	eq(t, "accounts", got, [][3]string{{self, "management", "mgmt"}, {other, "prod", "prd"}, {"333333333333", "staging", "mgmt"}})
	eq(t, "listed", r.Listed, 3)
	if c := credentialsOf(t, r.Accounts[1]); c != nil {
		t.Errorf("its own credentials: %+v", c)
	}
	eq(t, "assumed", mgmt.calls(), [][2]string{})
}

func TestAnIDNoProfileReachesListedApartSayingWhichProfilesWereTriedNoneReachedRefused(t *testing.T) {
	profiles := []Profile{{"qal", cannotList(self)}, {"prd", cannotList(other)}}
	r := mustReachProfiles(t, profiles, Options{Role: role, Only: []string{self, "999999999999"}})
	eq(t, "accounts", ids(r), []string{self})
	eq(t, "unreachable", r.Unreachable.List(), []Unreachable{{
		Account: "999999999999", Name: "", Reason: "none of the profiles qal, prd reaches it",
		Fix: "add a profile of this account to --profile, or use a profile of the management account, which reaches every account",
	}})
	_, err := ReachProfiles(context.Background(), profiles, Options{Role: role, Only: []string{"999999999999"}})
	eq(t, "refusal", refusal(t, err), "--accounts: 999999999999: none of the profiles qal, prd reaches it")
}

func TestARoleAProfileCannotAssumeTheAccountIsListedApartAsWithOneProfile(t *testing.T) {
	mgmt := ports()
	mgmt.refuse = map[string]bool{"333333333333": true}
	r := mustReachProfiles(t, []Profile{{"mgmt", mgmt}, {"prd", cannotList(other)}}, Options{Role: role})
	_, err := r.Accounts[2].Credentials(context.Background())
	if !strings.Contains(awserr.Message(err), "sts:AssumeRole") {
		t.Fatalf("%v", err)
	}
	got := []string{}
	for _, u := range r.Unreachable.List() {
		got = append(got, u.Account)
	}
	eq(t, "unreachable", got, []string{"333333333333"})
}

// Beyond Node's cases: what accounts.ts does that its tests do not show.

func TestAnotherOrganizationFailureIsTheProfilesFailure(t *testing.T) {
	p := ports()
	p.orgErr = awsError("ExpiredTokenException", "The security token included in the request is expired")
	if _, err := Reach(context.Background(), p, Options{Role: role}); err != p.orgErr {
		t.Fatalf("%v", err)
	}
}

func TestAProfileThatFailsAmongSeveralIsNamedInTheErrorItsNameKept(t *testing.T) {
	expired := cannotList(other)
	expired.whoErr = awsError("ExpiredTokenException", "The security token included in the request is expired")
	_, err := ReachProfiles(context.Background(), []Profile{{"qal", cannotList(self)}, {"prd", expired}}, Options{Role: role})
	eq(t, "said", awserr.Said(err), "ExpiredTokenException: prd: The security token included in the request is expired")
}

func TestProfilesInSeveralOrganizationsSaySo(t *testing.T) {
	a, b := cannotList(self), cannotList(other)
	b.organization = "o-def456"
	r := mustReachProfiles(t, []Profile{{"qal", a}, {"prd", b}}, Options{Role: role})
	eq(t, "organization", r.Organization, (*string)(nil))
	if !strings.HasSuffix(deref(r.Why).(string), "; the profiles are in several organizations: o-abc123, o-def456") {
		t.Errorf("why %q", deref(r.Why))
	}
}

func TestAccountsAskedAtOnceShareOneAssume(t *testing.T) {
	p := ports()
	r := mustReach(t, p, Options{Role: role})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, err := r.Accounts[1].Credentials(context.Background()); err != nil || *c != creds {
				t.Errorf("%v %+v", err, c)
			}
		}()
	}
	wg.Wait()
	eq(t, "assumed once", len(p.calls()), 1)
}

func TestReachedWritesNodesNames(t *testing.T) {
	p := ports()
	p.orgErr = awsError("AWSOrganizationsNotInUseException", "Your account is not a member of an organization.")
	r := mustReachProfiles(t, []Profile{{"solo", p}}, Options{Role: role})
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"organization":null,"why":"not in an organization: only the account 111111111111 is estimated","accounts":[{"account":"111111111111","name":"111111111111","own":true,"profile":"solo"}],"unreachable":[],"listed":1}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

// What reaching the accounts says, before any estimate (the user, 2026-10-14: a large organization took a while
// without a word): the profile read, the organization's accounts listed (its walk passed on), then how many and how
// long.
func TestItSaysWhatItReadsWhileReachingTheAccounts(t *testing.T) {
	p := ports()
	p.walk = []discover.InventoryProgress{{Read: 1, Found: 3, Accounts: 1}, {Read: 3, Found: 3, Accounts: 3}}
	p.ous = map[string]discover.OU{"r-root": {Name: "Root"}, "ou-a": {Name: "a"}, "ou-b": {Name: "b"}}
	start := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)
	calls := 0
	now := func() time.Time { calls++; return start.Add(time.Duration(calls-1) * 37 * time.Second) }
	var said []string
	var walked []discover.InventoryProgress
	mustReach(t, p, Options{Profile: "prospect", Now: now, Say: func(l string) { said = append(said, l) },
		Listing: func(x discover.InventoryProgress) { walked = append(walked, x) }})
	want := []string{
		"Reading the profile prospect: its account and its organization",
		"Profile prospect: account 111111111111 in the organization o-abc123; listing the organization's accounts",
		"Listed 3 active accounts in 3 organizational units (0:37)",
	}
	if !reflect.DeepEqual(said, want) {
		t.Fatalf("said %q, want %q", said, want)
	}
	if !reflect.DeepEqual(walked, p.walk) {
		t.Fatalf("walked %+v", walked)
	}
	// Several profiles: each named; nothing to say, nothing said.
	said = nil
	if _, err := ReachProfiles(context.Background(), []Profile{{"a", ports()}, {"b", cannotList("444444444444")}}, Options{Say: func(l string) { said = append(said, l) }}); err != nil {
		t.Fatal(err)
	}
	if len(said) < 3 || said[0] != "Reading the profile a: its account and its organization" || !slices.Contains(said, "Reading the profile b: its account and its organization") {
		t.Fatalf("said %q", said)
	}
	mustReach(t, ports(), Options{})
}
