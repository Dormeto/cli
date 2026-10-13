// Package awserr says an AWS error as the Node estimate says it: Node reads
// err.name and err.message (`${err.name}: ${err.message}` in accounts and
// discovery, err.message in the reasons of what a nap stops), where the AWS SDK
// for Go v2's Error() reads "operation error …, https response error …, api
// error Code: Message". Every place that turns an AWS or Kubernetes error into
// text goes through Name, Message or Said.
//
// Ports the name and message of the AWS SDK for JavaScript v3's service
// exceptions (@smithy/core decorateServiceException) and of the router's
// KubernetesError (lambda/src/lib/k8s.mjs).
package awserr

import (
	"errors"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/smithy-go"
)

// named is an error that names itself as Node's would (a Kubernetes error:
// KubernetesError); its message is its Error().
type named interface {
	error
	ErrorName() string
}

// Name is err.name as Node gives it: an AWS error's code (smithy's ErrorCode),
// a Kubernetes error's name (KubernetesError), else Node's generic Error.
func Name(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	var n named
	if errors.As(err, &n) {
		return n.ErrorName()
	}
	return "Error"
}

// Message is err.message as Node gives it: an AWS error's message (smithy's
// ErrorMessage; UnknownError when the API gave none, as the Node SDK says), a
// Kubernetes error's message, else Error(). A prefixed error's message is its
// prefix, ": " and its error's message.
func Message(err error) string {
	if err == nil {
		return ""
	}
	var p *prefixed
	if errors.As(err, &p) {
		return p.prefix + ": " + Message(p.err)
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		if m := api.ErrorMessage(); m != "" {
			return m
		}
		return "UnknownError"
	}
	var n named
	if errors.As(err, &n) {
		return n.Error()
	}
	return err.Error()
}

// Said is `${err.name}: ${err.message}`.
func Said(err error) string { return Name(err) + ": " + Message(err) }

// prefixed is an error whose message Node prefixed, its name unchanged.
type prefixed struct {
	prefix string
	err    error
}

func (p *prefixed) Error() string { return p.prefix + ": " + p.err.Error() }
func (p *prefixed) Unwrap() error { return p.err }

// Prefixed is err with its message prefixed, as Node's
// err.message = `${prefix}: ${err.message}` (such as the profile whose SSO
// session expired); its name and the error it wraps are kept.
func Prefixed(prefix string, err error) error {
	if err == nil {
		return nil
	}
	return &prefixed{prefix: prefix, err: err}
}

// Cause is err without the wrappers the AWS SDK for Go puts around a credential provider's error: the failed
// operation's ("operation error STS: GetCallerIdentity, "), the signer's ("get identity: ", "get credentials: ") and
// the credential cache's ("failed to refresh cached credentials, "). What remains is the provider's own error with
// everything below it, as Node's err.message gives the provider's words: an expired SSO session, "the SSO session has
// expired or is invalid"; a credential_process that fails, "process provider error: error in credential_process: exit
// status 1"; a call that failed after its retries, "exceeded maximum number of attempts, 3, …". Anything else is kept
// whole: an AWS API error, which Name and Message read already, is returned as it is; a prefix (Prefixed) is kept.
func Cause(err error) error {
	if err == nil {
		return nil
	}
	var p *prefixed
	if errors.As(err, &p) {
		return &prefixed{prefix: p.prefix, err: Cause(p.err)}
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		return err
	}
	// The operation the command asked for: implied (an operation below, such as the instance metadata service's, is kept).
	if op, ok := err.(*smithy.OperationError); ok && op.Err != nil {
		err = op.Err
	}
	for {
		next := errors.Unwrap(err)
		if next == nil || !sdkWrapper(err, next) {
			return err
		}
		err = next
	}
}

// sdkWrappers are the prefixes the SDK's signer and credential cache put before the error they wrap.
var sdkWrappers = []string{"get identity: ", "get credentials: ", "failed to refresh cached credentials, "}

// sdkWrapper says err only puts one of the SDK's prefixes before next.
func sdkWrapper(err, next error) bool {
	said, below := err.Error(), next.Error()
	for _, w := range sdkWrappers {
		if said == w+below {
			return true
		}
	}
	return false
}

// Prefix is the prefix of a prefixed error (Prefixed), such as the profile that failed among several; "" for none.
func Prefix(err error) string {
	var p *prefixed
	if errors.As(err, &p) {
		return p.prefix
	}
	return ""
}

// ssoTokenWords start the SSO token provider's errors that `aws sso login` cures (an sso-session profile): an expired
// token it cannot refresh, its cache file missing (never logged in) or not JSON. The provider says them in words only.
var ssoTokenWords = []string{"refresh cached SSO token failed", "failed to read cached SSO token file", "failed to parse cached SSO token file"}

// SSOToken says err is an SSO token error that `aws sso login` cures: the SSO session expired or invalid (the SDK's
// ssocreds.InvalidTokenError, a profile with its own sso_start_url), or the SSO token provider's expired, missing or
// unreadable token (an sso-session profile), wrapped or prefixed.
func SSOToken(err error) bool {
	var invalid *ssocreds.InvalidTokenError
	if errors.As(err, &invalid) {
		return true
	}
	return anyIn(err, func(e error) bool {
		said := e.Error()
		return slices.ContainsFunc(ssoTokenWords, func(w string) bool { return strings.HasPrefix(said, w) })
	})
}

// anyIn says some error of err's tree (err, then what it wraps) satisfies ok.
func anyIn(err error, ok func(error) bool) bool {
	if err == nil {
		return false
	}
	if ok(err) {
		return true
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return anyIn(u.Unwrap(), ok)
	case interface{ Unwrap() []error }:
		return slices.ContainsFunc(u.Unwrap(), func(e error) bool { return anyIn(e, ok) })
	}
	return false
}
