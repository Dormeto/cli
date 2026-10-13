package kubernetes

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// Ports router lambda/test/k8s.test.mjs: the SigV4 presign checked against
// AWS's documented example and against the AWS CLI's `aws eks get-token`.

func TestPresignReproducesTheS3DocumentationURLByteForByte(t *testing.T) {
	got := presignURL(presignRequest{
		Host: "examplebucket.s3.amazonaws.com", Path: "/test.txt", Region: "us-east-1", Service: "s3",
		Credentials: aws.Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		Now:         time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC), Expires: 86400, PayloadHash: "UNSIGNED-PAYLOAD",
	})
	want := "https://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20130524T000000Z" +
		"&X-Amz-Expires=86400&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

var (
	now    = time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)
	secret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
)

func decodeToken(t *testing.T, token string) *url.URL {
	t.Helper()
	const prefix = "k8s-aws-v1."
	if len(token) <= len(prefix) || token[:len(prefix)] != prefix {
		t.Fatalf("token %q lacks the prefix", token)
	}
	b, err := base64.RawURLEncoding.DecodeString(token[len(prefix):])
	if err != nil {
		t.Fatalf("token is not base64url without padding: %v", err)
	}
	u, err := url.Parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestTokenEqualsTheAWSCLITokenForLongTermCredentials(t *testing.T) {
	got := Token("eks-demo", "eu-west-1", aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: secret}, now)
	want := "k8s-aws-v1.aHR0cHM6Ly9zdHMuZXUtd2VzdC0xLmFtYXpvbmF3cy5jb20vP0FjdGlvbj1HZXRDYWxsZXJJZGVudGl0eSZWZXJzaW9uPTIwMTEtMDYtMTUmWC1BbXotQWxnb3JpdGhtPUFXUzQtSE1BQy1TSEEyNTYmWC1BbXotQ3JlZGVudGlhbD1BS0lERVhBTVBMRSUyRjIwMjYxMDExJTJGZXUtd2VzdC0xJTJGc3RzJTJGYXdzNF9yZXF1ZXN0JlgtQW16LURhdGU9MjAyNjEwMTFUMDkzMDAwWiZYLUFtei1FeHBpcmVzPTYwJlgtQW16LVNpZ25lZEhlYWRlcnM9aG9zdCUzQngtazhzLWF3cy1pZCZYLUFtei1TaWduYXR1cmU9ZmJkMDc3ZDUzNjdlY2Q1OTZjZjI2YjA3NWNiOGVmMDExMjhhMjk0MjlhOWU4ZTIwNDg2YzlkMjk1NzE0YmM5Zg"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestTokenEqualsTheAWSCLITokenForARoleSessionItsTokenSigned(t *testing.T) {
	got := Token("eks-demo", "eu-west-1", aws.Credentials{AccessKeyID: "ASIAEXAMPLE", SecretAccessKey: secret, SessionToken: "IQoJb3JpZ2luX2VjEExample/+token=="}, now)
	want := "k8s-aws-v1.aHR0cHM6Ly9zdHMuZXUtd2VzdC0xLmFtYXpvbmF3cy5jb20vP0FjdGlvbj1HZXRDYWxsZXJJZGVudGl0eSZWZXJzaW9uPTIwMTEtMDYtMTUmWC1BbXotQWxnb3JpdGhtPUFXUzQtSE1BQy1TSEEyNTYmWC1BbXotQ3JlZGVudGlhbD1BU0lBRVhBTVBMRSUyRjIwMjYxMDExJTJGZXUtd2VzdC0xJTJGc3RzJTJGYXdzNF9yZXF1ZXN0JlgtQW16LURhdGU9MjAyNjEwMTFUMDkzMDAwWiZYLUFtei1FeHBpcmVzPTYwJlgtQW16LVNpZ25lZEhlYWRlcnM9aG9zdCUzQngtazhzLWF3cy1pZCZYLUFtei1TZWN1cml0eS1Ub2tlbj1JUW9KYjNKcFoybHVYMlZqRUV4YW1wbGUlMkYlMkJ0b2tlbiUzRCUzRCZYLUFtei1TaWduYXR1cmU9Njc2YzBmMmZmNDEzNDUwNTEwMzlkYjk3OWNkZmJhMmU1ODE5NzI4ZjlhNDUyNDdhZWI2ZmRlYmFiOWFjOGIyMA"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestTokenIsAnSTSGetCallerIdentityURLOfTheRegionValid60SecondsTheClusterSigned(t *testing.T) {
	creds := aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: secret}
	token := Token("prod-1", "eu-west-3", creds, now)
	if !regexp.MustCompile(`^k8s-aws-v1\.[A-Za-z0-9_-]+$`).MatchString(token) {
		t.Fatalf("not base64url without padding: %s", token)
	}
	u := decodeToken(t, token)
	q := u.Query()
	for name, want := range map[string]string{
		"Action": "GetCallerIdentity", "X-Amz-Date": "20261011T093000Z", "X-Amz-Expires": "60",
		"X-Amz-Credential": "AKIDEXAMPLE/20261011/eu-west-3/sts/aws4_request", "X-Amz-SignedHeaders": "host;x-k8s-aws-id",
	} {
		if q.Get(name) != want {
			t.Errorf("%s = %q, want %q", name, q.Get(name), want)
		}
	}
	if u.Scheme+"://"+u.Host != "https://sts.eu-west-3.amazonaws.com" {
		t.Errorf("origin %s", u.Scheme+"://"+u.Host)
	}
	other := decodeToken(t, Token("prod-2", "eu-west-3", creds, now)).Query().Get("X-Amz-Signature")
	if other == q.Get("X-Amz-Signature") {
		t.Error("the cluster name is not signed")
	}
}
