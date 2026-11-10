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
	if want := seenTries * 2; slept != want {
		t.Errorf("slept %d times, want %d (24 retries of each account, 2 minutes)", slept, want)
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
}

func TestInstallFailsAsBeforeWhenTheAccountStaysUnseenPastTheBound(t *testing.T) {
	g := newInstallRig(t)
	g.s.ReadLag = 1000
	code, out := g.run(t, europe)
	if code != 1 || !strings.Contains(out, "service account "+routerSA+": ") {
		t.Errorf("exit %d, want 1 naming the account:\n%s", code, out)
	}
	if n := len(*g.slept); n < seenTries {
		t.Errorf("slept %d times, want at least %d", n, seenTries)
	}
}
