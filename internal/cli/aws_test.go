package cli

// The command's AWS (bin.ts): the price book's first refused lookup, said as Node says it; credentials that do not
// work through the real wiring (an expired SSO session, a profile never logged in, an unknown profile): AWS's words
// on one line, exit code 1, nothing sent.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/Dormeto/cli/internal/cache"
	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/stopped"
)

// refusing is a Price List API and Spot price history that refuse every call with err, counting them.
type refusing struct {
	err   error
	calls atomic.Int32
}

func (r *refusing) GetProducts(context.Context, prices.GetProductsInput) (prices.GetProductsOutput, error) {
	r.calls.Add(1)
	return prices.GetProductsOutput{}, r.err
}

func (r *refusing) History(context.Context, string, prices.SpotHistoryInput) (prices.SpotHistoryOutput, error) {
	r.calls.Add(1)
	return prices.SpotHistoryOutput{}, r.err
}

func t3(spot bool) stopped.Item {
	return stopped.Item{Kind: stopped.KindEC2, Type: "t3.medium", OS: "Linux", Count: 1, Spot: spot}
}

func TestThePriceBooksFirstRefusedLookupIsItsFailureSaidAsNodeSaysIt(t *testing.T) {
	throttled := &refusing{err: &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}}
	denied := &refusing{err: &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "You are not authorized to perform this operation."}}
	clock := func() time.Time { return testNow }
	book := priceBook(throttled, denied, cache.New(t.TempDir(), clock).Prices(), clock)
	if book.Failure == nil || book.Failure() != "" {
		t.Fatal("a failure before any lookup")
	}
	if usd, _ := book.Hourly(context.Background(), regR, t3(false), prices.Discounts{}); usd != nil {
		t.Fatalf("a refused lookup priced %v", *usd)
	}
	if usd, _ := book.Hourly(context.Background(), regR, t3(true), prices.Discounts{}); usd != nil {
		t.Fatalf("a refused Spot lookup priced %v", *usd)
	}
	if throttled.calls.Load() == 0 || denied.calls.Load() == 0 {
		t.Fatalf("asked the Price List %d times, Spot %d times", throttled.calls.Load(), denied.calls.Load())
	}
	// The first refusal is kept, whatever came after.
	if got := book.Failure(); got != "ThrottlingException: Rate exceeded" {
		t.Fatalf("failure %q", got)
	}
}

func TestALookupCancelledWithTheRunIsNoRefusal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	clock := func() time.Time { return testNow }
	book := priceBook(&refusing{err: context.Canceled}, nil, cache.New(t.TempDir(), clock).Prices(), clock)
	_, _ = book.Hourly(ctx, regR, t3(false), prices.Discounts{})
	if got := book.Failure(); got != "" {
		t.Fatalf("failure %q", got)
	}
}

func TestAWorkloadUnpricedBecauseThePriceListRefusedSaysWhyInTheReport(t *testing.T) {
	e, _ := fakeAWS(fakeOptions{})
	e.Prices = func(c prices.Cache) PriceBook {
		return priceBook(&refusing{err: &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}}, nil, c, func() time.Time { return testNow })
	}
	r := newRun(t, e)
	out := t.TempDir()
	if code := Main([]string{"--profile", "p", "--out", out}, r.deps); code != 0 {
		t.Fatalf("exit code %d: %q", code, r.errors())
	}
	if reason := at(t, written(t, out), "leftOut", 0, "reason"); reason != "prices unreadable (ThrottlingException: Rate exceeded)" {
		t.Fatalf("left out because %v", reason)
	}
}

// isolate gives the test its own home, AWS config and credentials files, no AWS variable of the environment, and
// every AWS endpoint a local server counting the requests it gets (none is expected).
func isolate(t *testing.T, config string) (home string, hits *atomic.Int32) {
	t.Helper()
	home = t.TempDir()
	for _, k := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_MAX_ATTEMPTS", "AWS_RETRY_MODE", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_SDK_LOAD_CONFIG"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	hits = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AWS_ENDPOINT_URL", srv.URL)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(home, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, "credentials"))
	if err := os.WriteFile(filepath.Join(home, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, hits
}

// cacheToken writes an SSO token cache entry, as `aws sso login` does.
func cacheToken(t *testing.T, home, key, body string) {
	t.Helper()
	dir := filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum([]byte(key))
	if err := os.WriteFile(filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const startURL = "https://example.awsapps.com/start"

// ssoConfig: a profile with its own sso_start_url (legacy), two of an SSO session (corp), one without credentials.
const ssoConfig = `[profile legacy]
sso_start_url = ` + startURL + `
sso_region = eu-west-1
sso_account_id = 111111111111
sso_role_name = ReadOnly
region = eu-west-1

[profile session]
sso_session = corp
sso_account_id = 111111111111
sso_role_name = ReadOnly
region = eu-west-1

[profile prd]
sso_session = corp
sso_account_id = 222222222222
sso_role_name = ReadOnly
region = eu-west-1

[sso-session corp]
sso_start_url = ` + startURL + `
sso_region = eu-west-1
sso_registration_scopes = sso:account:access

[profile bare]
region = eu-west-1
`

const expiredToken = `{"startUrl": "` + startURL + `", "region": "eu-west-1", "accessToken": "token", "expiresAt": "2026-10-01T00:00:00Z"}`

func TestCredentialsThatDoNotWorkThroughTheRealWiringAWSsWordsOnOneLineExitCode1NothingSent(t *testing.T) {
	cases := []struct{ name, profiles, key, says string }{
		{"an expired SSO session (a profile's own start URL)", "legacy", startURL, "Error: the SSO session has expired or is invalid"},
		{"an expired SSO session (aws sso login --sso-session)", "session", "corp", "Error: cached SSO token is expired, or not present, and cannot be refreshed"},
		{"several profiles: the first failing named", "session,prd", "corp", "Error: session: cached SSO token is expired, or not present, and cannot be refreshed"},
		{"a profile never logged in: the token file missing", "session", "", "Error: failed to read cached SSO token file, open <home>/.aws/sso/cache/<sha1 of corp>.json: no such file or directory"},
		{"a profile missing from the config", "nope", "", "Error: failed to get shared config profile, nope"},
		{"a profile without credentials (the instance metadata service off)", "bare", "",
			`Error: no EC2 IMDS role found, operation error ec2imds: GetMetadata, access disabled to EC2 IMDS via client option, or "AWS_EC2_METADATA_DISABLED" environment variable`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home, hits := isolate(t, ssoConfig)
			if c.key != "" {
				cacheToken(t, home, c.key, expiredToken)
			}
			r := newRun(t, nil)
			r.deps.AWS = AWSOf
			code := Main([]string{"--profile", c.profiles, "--out", t.TempDir()}, r.deps)
			errs := r.errors()
			sum := sha1.Sum([]byte("corp"))
			for i := range errs {
				errs[i] = strings.ReplaceAll(errs[i], hex.EncodeToString(sum[:]), "<sha1 of corp>")
				errs[i] = strings.ReplaceAll(errs[i], home, "<home>")
			}
			if code != 1 || !slices.Equal(errs, []string{c.says}) {
				t.Fatalf("exit code %d, errors %q", code, errs)
			}
			if hits.Load() != 0 {
				t.Errorf("%d requests sent with credentials that do not work", hits.Load())
			}
			if entries, _ := os.ReadDir(r.cacheRoot); len(entries) != 0 {
				t.Errorf("a cache made: %v", entries)
			}
		})
	}
}
