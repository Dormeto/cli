package installaws

import (
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/acm"
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
}

func TestAssumedCachesAnAssumeRoleProvider(t *testing.T) {
	base := config()
	got := Assumed(base, "arn:aws:iam::123456789012:role/x", "dormeto-install")
	if _, ok := got.Credentials.(*awssdk.CredentialsCache); !ok {
		t.Fatalf("credentials are %T, want a cache", got.Credentials)
	}
	if _, ok := base.Credentials.(*awssdk.CredentialsCache); ok {
		t.Error("the original config was modified")
	}
	if got.Region != base.Region {
		t.Errorf("region %q", got.Region)
	}
}
