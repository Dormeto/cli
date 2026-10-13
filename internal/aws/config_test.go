package aws

// Credentials that do not work (Review focus 1) give the SDK's message as an
// error, never a panic, without a call to AWS; the clients' retries (Review
// focus 4): ELB in adaptive mode, the others the SDK's standard retryer.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/Dormeto/cli/internal/awserr"
)

// isolate gives the test its own home, AWS config and credentials files, and
// no AWS variable of the environment.
func isolate(t *testing.T, config string) string {
	t.Helper()
	home := t.TempDir()
	for _, k := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_MAX_ATTEMPTS", "AWS_RETRY_MODE", "AWS_ENDPOINT_URL", "AWS_ROLE_ARN",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(home, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, "credentials"))
	if err := os.WriteFile(filepath.Join(home, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "credentials"), []byte("[static]\naws_access_key_id = AKIDEXAMPLE\naws_secret_access_key = secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
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

// nowhere is an endpoint that counts the requests it gets: none is expected.
func nowhere(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits
}

const startURL = "https://example.awsapps.com/start"

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

[sso-session corp]
sso_start_url = ` + startURL + `
sso_region = eu-west-1
sso_registration_scopes = sso:account:access

[profile static]
region = eu-west-1
`

func TestAnUnknownProfileIsAnErrorWithTheSDKsMessage(t *testing.T) {
	isolate(t, ssoConfig)
	_, err := Load(context.Background(), "nope")
	if err == nil || !strings.Contains(awserr.Message(err), "failed to get shared config profile, nope") {
		t.Fatalf("%v", err)
	}
}

func TestAnExpiredSSOTokenCacheIsAnErrorWithTheSDKsMessageAndNoCall(t *testing.T) {
	cases := []struct{ profile, key, token, says string }{
		// A profile with its own sso_start_url: the cache keyed by the start URL.
		{"legacy", startURL, `{"startUrl": "` + startURL + `", "region": "eu-west-1", "accessToken": "token", "expiresAt": "2026-10-01T00:00:00Z"}`, "the SSO session has expired or is invalid"},
		// An SSO session (aws sso login --sso-session): the cache keyed by its name, no refresh token kept.
		{"session", "corp", `{"startUrl": "` + startURL + `", "region": "eu-west-1", "accessToken": "token", "expiresAt": "2026-10-01T00:00:00Z"}`, "cached SSO token is expired, or not present, and cannot be refreshed"},
	}
	for _, c := range cases {
		t.Run(c.profile, func(t *testing.T) {
			home := isolate(t, ssoConfig)
			cacheToken(t, home, c.key, c.token)
			cfg, err := Load(context.Background(), c.profile)
			if err != nil {
				t.Fatal(err)
			}
			endpoint, hits := nowhere(t)
			cfg.BaseEndpoint = &endpoint
			_, err = NewAccounts(cfg).Whoami(context.Background())
			if err == nil || !strings.Contains(awserr.Message(err), c.says) {
				t.Fatalf("%v", err)
			}
			if _, err := cfg.Credentials.Retrieve(context.Background()); err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("the credentials, as the Kubernetes token reads them: %v", err)
			}
			if hits.Load() != 0 {
				t.Errorf("%d calls made with credentials that do not work", hits.Load())
			}
		})
	}
}

func TestAProfileOfTheCredentialsFileLoads(t *testing.T) {
	isolate(t, ssoConfig)
	cfg, err := Load(context.Background(), "static")
	if err != nil {
		t.Fatal(err)
	}
	c, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil || c.AccessKeyID != "AKIDEXAMPLE" {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestAnAccountUsesTheProfilesCredentialsOrItsRoles(t *testing.T) {
	profile := awssdk.Config{Region: "eu-west-1", Credentials: credentials.NewStaticCredentialsProvider("AKIDPROFILE", "s", "")}
	if own, _ := Of(profile, nil).Credentials.Retrieve(context.Background()); own.AccessKeyID != "AKIDPROFILE" {
		t.Errorf("the profile's own account: its own credentials, not %+v", own)
	}
	role := awssdk.Credentials{AccessKeyID: "ASIAROLE", SecretAccessKey: "s", SessionToken: "t"}
	c, err := Of(profile, &role).Credentials.Retrieve(context.Background())
	if err != nil || c.AccessKeyID != "ASIAROLE" || c.SessionToken != "t" {
		t.Fatalf("%v %+v", err, c)
	}
	if p, _ := profile.Credentials.Retrieve(context.Background()); p.AccessKeyID != "AKIDPROFILE" {
		t.Error("the profile's configuration is left as it was")
	}
}

func retryerOf(t *testing.T, client any) (awssdk.Retryer, awssdk.RetryMode, string) {
	t.Helper()
	switch c := client.(type) {
	case *elasticloadbalancingv2.Client:
		return c.Options().Retryer, c.Options().RetryMode, c.Options().Region
	case *autoscaling.Client:
		return c.Options().Retryer, c.Options().RetryMode, c.Options().Region
	case *ec2.Client:
		return c.Options().Retryer, c.Options().RetryMode, c.Options().Region
	case *ecs.Client:
		return c.Options().Retryer, c.Options().RetryMode, c.Options().Region
	case *cloudwatch.Client:
		return c.Options().Retryer, c.Options().RetryMode, c.Options().Region
	case *eks.Client:
		return c.Options().Retryer, c.Options().RetryMode, c.Options().Region
	case *rds.Client:
		return c.Options().Retryer, c.Options().RetryMode, c.Options().Region
	case *pricing.Client:
		return c.Options().Retryer, c.Options().RetryMode, c.Options().Region
	case *sts.Client:
		return c.Options().Retryer, c.Options().RetryMode, c.Options().Region
	case *organizations.Client:
		return c.Options().Retryer, c.Options().RetryMode, c.Options().Region
	}
	t.Fatalf("not an SDK client: %T", client)
	return nil, "", ""
}

func TestELBClientsRetryInAdaptiveModeTheOthersInTheStandardMode(t *testing.T) {
	cfg := awssdk.Config{Region: "eu-west-3", Credentials: credentials.NewStaticCredentialsProvider("AKID", "s", ""),
		// A user's max_attempts does not change the estimate's (Node's explicit maxAttempts).
		RetryMaxAttempts: 9}
	r := NewRegion(cfg, "eu-west-1", "the profile")
	retryer, mode, region := retryerOf(t, r.elbv2.client)
	if _, ok := retryer.(*retry.AdaptiveMode); !ok || mode != awssdk.RetryModeAdaptive || retryer.MaxAttempts() != 5 || region != "eu-west-1" {
		t.Errorf("ELB: %T (%s), %d attempts, %s", retryer, mode, retryer.MaxAttempts(), region)
	}
	standard := map[string]any{
		"autoscaling": r.autoscaling.client, "ec2": r.ec2, "ecs": r.ecs, "cloudwatch": r.cloudwatch.client,
		"eks": r.eks.client, "rds": r.rds.client,
	}
	for name, client := range standard {
		retryer, mode, region := retryerOf(t, client)
		if _, ok := retryer.(*retry.Standard); !ok || mode != awssdk.RetryModeStandard || retryer.MaxAttempts() != 5 || region != "eu-west-1" {
			t.Errorf("%s: %T (%s), %d attempts, %s", name, retryer, mode, retryer.MaxAttempts(), region)
		}
	}
	// The global calls: us-east-1, the SDK's standard retryer (STS's wrapped with an error of its own it retries), 3 attempts.
	plain := awssdk.Config{Region: "eu-west-3", Credentials: cfg.Credentials}
	a := NewAccounts(plain).(*accountsPort)
	for name, client := range map[string]any{"sts": a.sts, "organizations": a.organizations.client, "pricing": NewPricing(plain).(*pricingPort).client} {
		retryer, mode, region := retryerOf(t, client)
		if mode != awssdk.RetryModeStandard || retryer.MaxAttempts() != 3 || region != "us-east-1" {
			t.Errorf("%s: %T (%s), %d attempts, %s", name, retryer, mode, retryer.MaxAttempts(), region)
		}
	}
}
