package installaws

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func config() awssdk.Config {
	return awssdk.Config{Region: "ap-south-1", Credentials: credentials.NewStaticCredentialsProvider("a", "b", "")}
}

func TestClientsRegionsAndRetries(t *testing.T) {
	c := Clients(config(), "eu-west-1")
	s3o := c.S3.(*s3.Client).Options()
	if s3o.Region != "eu-west-1" {
		t.Errorf("s3 region %q", s3o.Region)
	}
	if got := s3o.Retryer.MaxAttempts(); got != 5 {
		t.Errorf("s3 attempts %d", got)
	}
	acmo := c.ACM.(*acm.Client).Options()
	if acmo.Region != "us-east-1" {
		t.Errorf("acm region %q", acmo.Region)
	}
	if got := acmo.Retryer.MaxAttempts(); got != 5 {
		t.Errorf("acm attempts %d", got)
	}
	ebo := c.EventBridge.(*eventbridge.Client).Options()
	if ebo.Region != "eu-west-1" {
		t.Errorf("eventbridge region %q", ebo.Region)
	}
	if got := ebo.Retryer.MaxAttempts(); got != 5 {
		t.Errorf("eventbridge attempts %d", got)
	}
}

func TestAssumedCachesAnAssumeRoleProvider(t *testing.T) {
	base := config()
	got := Assumed(base, "eu-west-1", "arn:aws:iam::123456789012:role/x", "dormeto-install")
	if _, ok := got.Credentials.(*awssdk.CredentialsCache); !ok {
		t.Fatalf("credentials are %T, want a cache", got.Credentials)
	}
	if _, ok := base.Credentials.(*awssdk.CredentialsCache); ok {
		t.Error("the original config was modified")
	}
	if got.Region != "eu-west-1" {
		t.Errorf("region %q", got.Region)
	}
}

// stsAnswer is an HTTP client answering every request with an AssumeRole response, recording the requests' hosts:
// nothing leaves the machine.
type stsAnswer struct {
	mu    sync.Mutex
	hosts []string
}

func (f *stsAnswer) Do(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.hosts = append(f.hosts, r.URL.Host)
	f.mu.Unlock()
	body := `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials>` +
		`<AccessKeyId>ASIAEXAMPLE</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>token</SessionToken>` +
		`<Expiration>2099-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleResult>` +
		`<ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></AssumeRoleResponse>`
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/xml"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

// A profile without a region (the run's region from --region or asked): the member role is assumed through STS in the
// run's region all the same.
func TestAssumedInTheRunsRegion(t *testing.T) {
	answer := &stsAnswer{}
	cfg := awssdk.Config{Credentials: credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", ""), HTTPClient: answer}
	creds, err := Assumed(cfg, "eu-west-1", "arn:aws:iam::444444444444:role/OrganizationAccountAccessRole", "dmt-prod-install").Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessKeyID != "ASIAEXAMPLE" {
		t.Errorf("credentials %+v", creds)
	}
	if len(answer.hosts) != 1 || answer.hosts[0] != "sts.eu-west-1.amazonaws.com" {
		t.Errorf("STS called at %v", answer.hosts)
	}
}
