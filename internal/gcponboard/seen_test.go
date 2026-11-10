package gcponboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/gcp"
)

// Measured live on 2026-11-10: Google answered 404 on the service accounts a connect had created seconds before, and
// showed them a minute later. What a run created is read again for up to 2 minutes; what it did not create is not.

const delayLine = "Google has not shown what this run created yet"

func TestConnectWaitsForGoogleToShowTheAccountsItCreated(t *testing.T) {
	s := connectServer(t)
	s.ReadLag = 2
	d, out, errs := connectDeps(s)
	var slept []time.Duration
	d.Sleep = func(_ context.Context, w time.Duration) error { slept = append(slept, w); return nil }
	m := newFakeManager()
	if code := Connect(context.Background(), d, connectRequest(m)); code != 0 {
		t.Fatalf("exit %d:\n%s\n%s", code, out, errs)
	}
	if len(m.puts) == 0 || !strings.Contains(out.String(), "✓ verified") {
		t.Errorf("not verified and recorded:\n%s", out)
	}
	if n := strings.Count(errs.String(), delayLine); n != 1 {
		t.Errorf("%d lines about Google's delay, want 1:\n%s", n, errs)
	}
	if len(slept) != 4 {
		t.Errorf("slept %v: two 404s for each of the two accounts, 5 s each", slept)
	}
	for _, w := range slept {
		if w != 5*time.Second {
			t.Errorf("waited %s, want 5s", w)
		}
	}
}

func TestConnectFailsAsBeforeWhenTheAccountsStayUnseenPastTheBound(t *testing.T) {
	s := connectServer(t)
	s.ReadLag = 1000
	d, out, errs := connectDeps(s)
	slept := 0
	d.Sleep = func(context.Context, time.Duration) error { slept++; return nil }
	m := newFakeManager()
	code := Connect(context.Background(), d, connectRequest(m))
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out.String(), "✗ verification: service account "+discoveryEmail+": ") || !strings.Contains(out.String(), "Unknown service account") {
		t.Errorf("verification does not name the missing account:\n%s", out)
	}
	if !strings.Contains(out.String(), "The manager's record") {
		t.Errorf("no word on the record:\n%s", out)
	}
	if slept != seenTries {
		t.Errorf("slept %d times, want %d: 2 minutes for the whole run, not for each account", slept, seenTries)
	}
	if n := strings.Count(errs.String(), delayLine); n != 1 {
		t.Errorf("%d lines about Google's delay, want 1", n)
	}
}

func TestConnectDoesNotRetryAnAccountItDidNotCreate(t *testing.T) {
	s := connectServer(t)
	m := newFakeManager()
	if code, out := runConnect(t, s, connectRequest(m)); code != 0 {
		t.Fatalf("first connect exit %d:\n%s", code, out)
	}
	s.Lock()
	delete(s.ServiceAccounts, discoveryEmail)
	s.Unlock()
	// Connect again: the plan sees the account missing and creates it; so read a thing the run did not create instead.
	d, _, errs := connectDeps(s)
	slept := 0
	d.Sleep = func(context.Context, time.Duration) error { slept++; return nil }
	x := newLink(context.Background(), d, connectRequest(m), "connect")
	calls := 0
	err := x.seen(x.serviceAccountURL(discoveryEmail), func() error {
		calls++
		return x.d.Google.Do(x.ctx, "GET", x.serviceAccountURL(discoveryEmail), nil, nil)
	})
	if !gcp.IsNotFound(err) || calls != 1 || slept != 0 || strings.Contains(errs.String(), delayLine) {
		t.Errorf("err %v, %d calls, %d sleeps, stderr %q: a 404 of what the run did not create is the answer", err, calls, slept, errs)
	}
}

func TestInstallWaitsForGoogleToShowTheAccountItCreated(t *testing.T) {
	g := newInstallRig(t)
	g.s.ReadLag = 2
	code, out := g.run(t, europe)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if n := strings.Count(out, delayLine); n != 1 {
		t.Errorf("%d lines about Google's delay, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "verified") {
		t.Errorf("not verified:\n%s", out)
	}
	if n := len(*g.slept); n != 2 {
		t.Errorf("slept %d times, want 2 (the two 404s of the account)", n)
	}
}

func TestInstallFailsAsBeforeWhenTheAccountStaysUnseenPastTheBound(t *testing.T) {
	g := newInstallRig(t)
	g.s.ReadLag = 1000
	code, out := g.run(t, europe)
	if code != 1 || !strings.Contains(out, "service account "+routerSA+": ") {
		t.Errorf("exit %d, want 1 naming the account:\n%s", code, out)
	}
	if n := len(*g.slept); n != seenTries {
		t.Errorf("slept %d times, want %d: the account and its policy share one budget", n, seenTries)
	}
}

// seenRig is a run whose Google answers 404 to every read: the retries counted by the sleeps.
func seenRig(t *testing.T) (*run, *int) {
	t.Helper()
	slept := 0
	x := newRun(context.Background(), Deps{Err: &strings.Builder{}, Sleep: func(context.Context, time.Duration) error { slept++; return nil }}, Request{})
	return x, &slept
}

func notFound() error { return &gcp.Error{Code: 404, Status: "NOT_FOUND"} }

func TestSeenRetriesTheReadsOfWhatTheRunCreatedAndNothingElse(t *testing.T) {
	const acct = "https://iam.googleapis.com/v1/projects/p/serviceAccounts/a@p.iam.gserviceaccount.com"
	const role = "https://iam.googleapis.com/v1/projects/p/roles/foo"
	const pool = "https://iam.googleapis.com/v1/projects/1/locations/global/workloadIdentityPools/dmt-dev"
	for _, c := range []struct {
		name, made, read string
		retried          bool
	}{
		{"the account's policy", acct, acct + ":getIamPolicy?options.requestedPolicyVersion=3", true},
		{"the account", acct, acct, true},
		{"a role", role, role, true},
		{"the provider of a pool created here", pool, pool + "/providers/aws-1", true},
		{"a sibling role with the same start", role, role + "bar", false},
		{"something else while other things are made", role, acct, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			x, slept := seenRig(t)
			x.made(c.made)
			calls := 0
			err := x.seen(c.read, func() error { calls++; return notFound() })
			if !gcp.IsNotFound(err) {
				t.Errorf("err %v, want the 404", err)
			}
			if c.retried && (calls != seenTries+1 || *slept != seenTries) {
				t.Errorf("%d calls, %d sleeps, want %d and %d", calls, *slept, seenTries+1, seenTries)
			}
			if !c.retried && (calls != 1 || *slept != 0) {
				t.Errorf("%d calls, %d sleeps, want 1 and 0", calls, *slept)
			}
		})
	}
}

func TestSeenHasOneBudgetForTheWholeRun(t *testing.T) {
	x, slept := seenRig(t)
	const acct = "https://iam.googleapis.com/v1/projects/p/serviceAccounts/a"
	const role = "https://iam.googleapis.com/v1/projects/p/roles/foo"
	x.made(acct)
	x.made(role)
	for _, u := range []string{acct, role, acct + ":getIamPolicy", role} {
		calls := 0
		x.seen(u, func() error { calls++; return notFound() })
		if *slept > seenTries {
			t.Fatalf("%d sleeps after %s, want at most %d", *slept, u, seenTries)
		}
		if *slept == seenTries && u != acct && calls != 1 {
			t.Errorf("%s: %d calls once the budget is spent, want 1", u, calls)
		}
	}
	if *slept != seenTries {
		t.Errorf("%d sleeps, want %d", *slept, seenTries)
	}
}
