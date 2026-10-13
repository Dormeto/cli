package awserr_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials/processcreds"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/kubernetes"
)

// sdkError is a failed call as the AWS SDK for Go v2 returns it: the API's
// error inside the response error, inside the operation error.
func sdkError(code, message string) error {
	return &smithy.OperationError{
		ServiceID:     "STS",
		OperationName: "GetCallerIdentity",
		Err: &awshttp.ResponseError{
			ResponseError: &smithyhttp.ResponseError{
				Response: &smithyhttp.Response{Response: &http.Response{StatusCode: 403}},
				Err:      &smithy.GenericAPIError{Code: code, Message: message},
			},
			RequestID: "req-1",
		},
	}
}

func TestAnAWSErrorIsItsCodeAndItsMessageAsNodeSaysThem(t *testing.T) {
	err := sdkError("ExpiredTokenException", "The security token included in the request is expired")
	if got := awserr.Name(err); got != "ExpiredTokenException" {
		t.Errorf("name %q", got)
	}
	if got := awserr.Message(err); got != "The security token included in the request is expired" {
		t.Errorf("message %q (the SDK's text: %q)", got, err.Error())
	}
	if got := awserr.Said(err); got != "ExpiredTokenException: The security token included in the request is expired" {
		t.Errorf("said %q", got)
	}
}

func TestAnAWSErrorWithoutAMessageSaysUnknownErrorAsTheNodeSDK(t *testing.T) {
	err := &smithy.GenericAPIError{Code: "NotFound"}
	if got := awserr.Said(err); got != "NotFound: UnknownError" {
		t.Errorf("said %q", got)
	}
}

func TestAKubernetesErrorIsNamedAsNodesKubernetesError(t *testing.T) {
	err := &kubernetes.Error{Status: 401, Reason: "Unauthorized", Message: "Unauthorized"}
	if got := awserr.Said(err); got != "KubernetesError: Unauthorized" {
		t.Errorf("said %q", got)
	}
	if got := awserr.Name(fmt.Errorf("wrapped: %w", err)); got != "KubernetesError" {
		t.Errorf("name of a wrapped one %q", got)
	}
}

func TestAnyOtherErrorIsNodesGenericError(t *testing.T) {
	err := errors.New("cluster unreachable")
	if got := awserr.Said(err); got != "Error: cluster unreachable" {
		t.Errorf("said %q", got)
	}
	if got := awserr.Message(nil); got != "" {
		t.Errorf("no error: %q", got)
	}
}

func TestAPrefixedErrorKeepsItsNameAndPrefixesItsMessageAsNodeDoes(t *testing.T) {
	inner := sdkError("ExpiredTokenException", "The security token included in the request is expired")
	err := awserr.Prefixed("qal", inner)
	if got := awserr.Said(err); got != "ExpiredTokenException: qal: The security token included in the request is expired" {
		t.Errorf("said %q", got)
	}
	var api smithy.APIError
	if !errors.As(err, &api) || api.ErrorCode() != "ExpiredTokenException" {
		t.Errorf("the API error is still found: %#v", err)
	}
	if got := awserr.Said(awserr.Prefixed("prd", errors.New("boom"))); got != "Error: prd: boom" {
		t.Errorf("said %q", got)
	}
	if got := err.Error(); got != "qal: "+inner.Error() {
		t.Errorf("Error() %q", got)
	}
}

// ssoExpired is an expired SSO session as the AWS SDK for Go v2 says it on a call: the provider's error inside the
// credential cache's, the signer's and the operation's.
func ssoExpired(cause error) error {
	return &smithy.OperationError{ServiceID: "STS", OperationName: "GetCallerIdentity",
		Err: fmt.Errorf("get identity: %w", fmt.Errorf("get credentials: %w", fmt.Errorf("failed to refresh cached credentials, %w", cause)))}
}

func TestTheCauseOfAnExpiredSSOSessionIsTheSDKsWordsAboutTheSessionOnOneLine(t *testing.T) {
	err := ssoExpired(&ssocreds.InvalidTokenError{})
	if got := awserr.Said(awserr.Cause(err)); got != "Error: the SSO session has expired or is invalid" {
		t.Errorf("said %q (the SDK's text: %q)", got, err.Error())
	}
	// An SSO session whose token cannot be refreshed: the SSO token provider's words, and all below them.
	err = ssoExpired(fmt.Errorf("refresh cached SSO token failed, %w", errors.New("cached SSO token is expired, or not present, and cannot be refreshed")))
	if got := awserr.Said(awserr.Cause(err)); got != "Error: refresh cached SSO token failed, cached SSO token is expired, or not present, and cannot be refreshed" {
		t.Errorf("said %q", got)
	}
}

func TestTheCauseIsTheCredentialProvidersOwnErrorWithEverythingBelowIt(t *testing.T) {
	// A credential_process profile (aws-vault, granted, saml2aws): the process failed, timed out, or printed no JSON.
	for leaf, says := range map[error]string{
		fmt.Errorf("error in credential_process: %w", errors.New("exit status 1")):               "Error: process provider error: error in credential_process: exit status 1",
		fmt.Errorf("credential process timed out: %w", context.DeadlineExceeded):                 "Error: process provider error: credential process timed out: context deadline exceeded",
		fmt.Errorf("parse failed of process output: %s, error: %w", "oops", io.ErrUnexpectedEOF): "Error: process provider error: parse failed of process output: oops, error: unexpected EOF",
	} {
		if got := awserr.Said(awserr.Cause(ssoExpired(&processcreds.ProviderError{Err: leaf}))); got != says {
			t.Errorf("said %q, want %q", got, says)
		}
	}
}

func TestTheCauseOfACallThatFailedAfterItsRetriesSaysTheAttempts(t *testing.T) {
	below := &retry.MaxAttemptsError{Attempt: 3, Err: &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: 0}},
			Err:      &smithyhttp.RequestSendError{Err: &url.Error{Op: "Post", URL: "https://sts.us-east-1.amazonaws.com/", Err: syscall.ECONNREFUSED}},
		},
	}}
	err := &smithy.OperationError{ServiceID: "STS", OperationName: "GetCallerIdentity", Err: below}
	if got, want := awserr.Said(awserr.Cause(err)), "Error: "+below.Error(); got != want || !strings.Contains(got, "exceeded maximum number of attempts, 3") {
		t.Errorf("said %q, want %q", got, want)
	}
}

func TestTheCauseStopsAboveAnOSOrNetworkErrorWhichSaysTooLittleAlone(t *testing.T) {
	missing := &fs.PathError{Op: "open", Path: "/home/u/.aws/sso/cache/0a1b.json", Err: syscall.ENOENT}
	err := ssoExpired(&ssocreds.InvalidTokenError{Err: missing})
	if got := awserr.Said(awserr.Cause(err)); got != "Error: the SSO session has expired or is invalid: open /home/u/.aws/sso/cache/0a1b.json: no such file or directory" {
		t.Errorf("said %q", got)
	}
	offline := &smithy.OperationError{ServiceID: "STS", OperationName: "GetCallerIdentity", Err: &smithyhttp.RequestSendError{Err: &url.Error{
		Op: "Post", URL: "https://sts.us-east-1.amazonaws.com/",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "sts.us-east-1.amazonaws.com", IsNotFound: true}},
	}}}
	if got := awserr.Said(awserr.Cause(offline)); got != `Error: request send failed, Post "https://sts.us-east-1.amazonaws.com/": dial tcp: lookup sts.us-east-1.amazonaws.com: no such host` {
		t.Errorf("said %q", got)
	}
}

func TestTheCauseStopsAboveAnInnerOperationWhichSaysWhatFailedInItsOwnWords(t *testing.T) {
	// A profile without credentials: the instance metadata service is the last provider tried.
	imds := &smithy.OperationError{ServiceID: "ec2imds", OperationName: "GetMetadata", Err: errors.New(`access disabled to EC2 IMDS via client option, or "AWS_EC2_METADATA_DISABLED" environment variable`)}
	err := ssoExpired(fmt.Errorf("no EC2 IMDS role found, %w", imds))
	if got := awserr.Said(awserr.Cause(err)); got != `Error: no EC2 IMDS role found, operation error ec2imds: GetMetadata, access disabled to EC2 IMDS via client option, or "AWS_EC2_METADATA_DISABLED" environment variable` {
		t.Errorf("said %q", got)
	}
}

func TestTheCauseOfAnAWSErrorIsTheErrorItselfAPrefixIsKept(t *testing.T) {
	api := sdkError("ExpiredTokenException", "The security token included in the request is expired")
	if got := awserr.Cause(api); got != api {
		t.Errorf("cause %v", got)
	}
	err := awserr.Prefixed("prd", ssoExpired(&ssocreds.InvalidTokenError{}))
	if got := awserr.Said(awserr.Cause(err)); got != "Error: prd: the SSO session has expired or is invalid" {
		t.Errorf("said %q", got)
	}
	plain := errors.New("failed to get shared config profile, nope")
	if got := awserr.Cause(plain); got != plain {
		t.Errorf("cause %v", got)
	}
	if awserr.Cause(nil) != nil {
		t.Error("the cause of no error")
	}
}

// F8: an SSO token that `aws sso login` renews (expired, invalid, its cache file missing or unreadable), as the SDK's
// SSO credential and token providers say it, wrapped or prefixed; nothing else.
func TestAnSSOTokenErrorIsToldFromAnyOther(t *testing.T) {
	missing := &fs.PathError{Op: "open", Path: "/home/u/.aws/sso/cache/0a1b.json", Err: syscall.ENOENT}
	for _, err := range []error{
		ssoExpired(&ssocreds.InvalidTokenError{}),
		ssoExpired(&ssocreds.InvalidTokenError{Err: fmt.Errorf("failed to read cached SSO token file, %w", missing)}),
		ssoExpired(fmt.Errorf("refresh cached SSO token failed, %w", errors.New("cached SSO token is expired, or not present, and cannot be refreshed"))),
		ssoExpired(fmt.Errorf("failed to read cached SSO token file, %w", missing)),
		ssoExpired(fmt.Errorf("failed to parse cached SSO token file, %w", errors.New("invalid character 'o' in literal null (expecting 'u')"))),
		awserr.Prefixed("prd", ssoExpired(&ssocreds.InvalidTokenError{})),
		awserr.Cause(ssoExpired(&ssocreds.InvalidTokenError{})),
	} {
		if !awserr.SSOToken(err) {
			t.Errorf("not an SSO token error: %v", err)
		}
	}
	for _, err := range []error{
		nil,
		sdkError("ExpiredTokenException", "The security token included in the request is expired"),
		ssoExpired(&processcreds.ProviderError{Err: errors.New("exit status 1")}),
		errors.New("failed to get shared config profile, nope"),
		ssoExpired(missing),
	} {
		if awserr.SSOToken(err) {
			t.Errorf("an SSO token error: %v", err)
		}
	}
}

// The prefix of an error (Prefixed): the profile that failed among several.
func TestThePrefixOfAnError(t *testing.T) {
	if got := awserr.Prefix(awserr.Cause(awserr.Prefixed("prd", ssoExpired(&ssocreds.InvalidTokenError{})))); got != "prd" {
		t.Errorf("prefix %q", got)
	}
	if got := awserr.Prefix(ssoExpired(&ssocreds.InvalidTokenError{})); got != "" {
		t.Errorf("prefix %q", got)
	}
}
