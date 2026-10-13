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
