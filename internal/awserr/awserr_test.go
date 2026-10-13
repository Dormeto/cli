package awserr_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
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
