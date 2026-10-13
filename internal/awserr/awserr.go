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
	"io/fs"
	"net"
	"net/url"
	"os"
	"syscall"

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

// Cause is err without the AWS SDK for Go's wrappers of a failed operation or credential lookup ("operation error
// STS: GetCallerIdentity, get identity: get credentials: failed to refresh cached credentials, …"): its innermost
// error that still says something alone, as Node's err.message gives the credential provider's words (an expired SSO
// session: "the SSO session has expired or is invalid"). An OS or network error says too little alone (no such file
// or directory), an inner operation's error too little of what it was for (the instance metadata service's, for a
// profile without credentials): the error above it, which names the file, the request or what was looked for, is the
// cause. An AWS API error, which Name and Message read already, is returned as it is; a prefix (Prefixed) is kept.
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
	for {
		next := errors.Unwrap(err)
		if _, inner := next.(*smithy.OperationError); next == nil || inner || system(next) {
			return err
		}
		err = next
	}
}

// system says err is the operating system's or the network's.
func system(err error) bool {
	switch err.(type) {
	case *fs.PathError, *os.LinkError, *os.SyscallError, syscall.Errno, *url.Error, *net.OpError, *net.DNSError, *net.AddrError:
		return true
	}
	return false
}
