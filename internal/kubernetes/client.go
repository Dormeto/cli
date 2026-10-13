package kubernetes

// Client ports the router's lambda/src/lib/k8s.mjs: createKubernetes (the
// reads: get and list), TIMEOUTS and KubernetesError. The estimate only reads,
// so the patch, create, remove and review calls are not ported.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Timeouts are the calls' bounds: connected (TLS included) within Connect,
// failed after Idle of silence (no byte read).
type Timeouts struct{ Connect, Idle time.Duration }

// DefaultTimeouts are the AWS calls' bounds (the router's TIMEOUTS): 3 seconds
// to connect, 10 seconds idle.
var DefaultTimeouts = Timeouts{Connect: 3 * time.Second, Idle: 10 * time.Second}

// Error is a failed Kubernetes call. Status is the HTTP status, 0 when no
// answer came; Reason the API's (Forbidden, NotFound, Conflict...), the HTTP
// status's text, Timeout, or the network error's code.
type Error struct {
	Status  int
	Reason  string
	Message string
}

// Error is the message.
func (e *Error) Error() string { return e.Message }

// ErrorName is the error's name as the router's KubernetesError has it
// (awserr.Name reads it).
func (e *Error) ErrorName() string { return "KubernetesError" }

// Client reads one cluster's API (GET only).
type Client interface {
	// Get returns the object at path (nil for an empty answer).
	Get(ctx context.Context, path string) (json.RawMessage, error)
	// List returns every item at path, following the API's continue tokens.
	List(ctx context.Context, path string, query map[string]string) ([]json.RawMessage, error)
}

type client struct {
	endpoint *url.URL
	token    string
	timeouts Timeouts
	http     *http.Client
}

// New makes the client of a cluster: endpoint and ca (PEM) from the cluster,
// token from Token. An empty ca trusts the system roots.
func New(endpoint string, ca []byte, token string) Client {
	return newClient(endpoint, ca, token, DefaultTimeouts)
}

// idleConn fails a read after idle without a byte, as the router's socket
// timeout does.
type idleConn struct {
	net.Conn
	idle time.Duration
}

func (c idleConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(p)
}

func newClient(endpoint string, ca []byte, token string, timeouts Timeouts) Client {
	u, err := url.Parse(endpoint)
	if err != nil {
		u = &url.URL{Scheme: "https", Host: endpoint}
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	if len(ca) > 0 {
		cfg.RootCAs = x509.NewCertPool()
		cfg.RootCAs.AppendCertsFromPEM(ca)
	}
	transport := &http.Transport{
		// TCP and TLS together within the connect bound.
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, timeouts.Connect)
			defer cancel()
			raw, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			conn := tls.Client(raw, cfg)
			if err := conn.HandshakeContext(ctx); err != nil {
				_ = raw.Close()
				return nil, err
			}
			return idleConn{Conn: conn, idle: timeouts.Idle}, nil
		},
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     timeouts.Idle,
	}
	return &client{endpoint: u, token: token, timeouts: timeouts, http: &http.Client{Transport: transport}}
}

func (c *client) call(ctx context.Context, path string, query map[string]string) (json.RawMessage, error) {
	ref, err := url.Parse(path)
	if err != nil {
		return nil, &Error{Reason: "Network", Message: fmt.Sprintf("GET %s: %v", path, err)}
	}
	u := c.endpoint.ResolveReference(ref)
	if len(query) > 0 {
		q := u.Query()
		for name, value := range query {
			q.Set(name, value)
		}
		u.RawQuery = q.Encode()
	}
	where := "GET " + u.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &Error{Reason: "Network", Message: fmt.Sprintf("%s: %v", where, err)}
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, c.failure(ctx, where, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, c.failure(ctx, where, err)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if len(strings.TrimSpace(string(body))) == 0 {
			return nil, nil
		}
		if !json.Valid(body) {
			return nil, nil // Node's parse gives undefined for text that is not JSON
		}
		return body, nil
	}
	reason := http.StatusText(res.StatusCode)
	if reason == "" {
		reason = "Unknown"
	}
	var st struct{ Kind, Reason, Message string }
	if json.Unmarshal(body, &st) == nil && st.Kind == "Status" {
		if st.Reason != "" {
			reason = st.Reason
		}
		message := st.Message
		if message == "" {
			message = fmt.Sprintf("%s: %d", where, res.StatusCode)
		}
		return nil, &Error{Status: res.StatusCode, Reason: reason, Message: message}
	}
	text := string(body)
	if len(text) > 300 {
		text = text[:300]
	}
	return nil, &Error{Status: res.StatusCode, Reason: reason, Message: fmt.Sprintf("%s: %d %s", where, res.StatusCode, text)}
}

// failure turns a transport error into an *Error (the caller's cancellation stays as is).
func (c *client) failure(ctx context.Context, where string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Reason: "Timeout", Message: fmt.Sprintf("%s: no connection within %d ms", where, c.timeouts.Connect.Milliseconds())}
	case errors.As(err, &ne) && ne.Timeout():
		return &Error{Reason: "Timeout", Message: fmt.Sprintf("%s: no answer for %d ms", where, c.timeouts.Idle.Milliseconds())}
	}
	return &Error{Reason: networkCode(err), Message: fmt.Sprintf("%s: %v", where, err)}
}

// networkCode is the code Node gives a network error.
func networkCode(err error) string {
	var dns *net.DNSError
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "ECONNREFUSED"
	case errors.Is(err, syscall.ECONNRESET):
		return "ECONNRESET"
	case errors.Is(err, syscall.ETIMEDOUT):
		return "ETIMEDOUT"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "EHOSTUNREACH"
	case errors.Is(err, syscall.ENETUNREACH):
		return "ENETUNREACH"
	case errors.As(err, &dns):
		return "ENOTFOUND"
	}
	return "Network"
}

func (c *client) Get(ctx context.Context, path string) (json.RawMessage, error) {
	return c.call(ctx, path, nil)
}

func (c *client) List(ctx context.Context, path string, query map[string]string) ([]json.RawMessage, error) {
	var items []json.RawMessage
	next := ""
	for {
		q := make(map[string]string, len(query)+1)
		for name, value := range query {
			q[name] = value
		}
		if next != "" {
			q["continue"] = next
		}
		raw, err := c.call(ctx, path, q)
		if err != nil {
			return nil, err
		}
		var page struct {
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
			Items []json.RawMessage `json:"items"`
		}
		if raw != nil {
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, &Error{Reason: "Network", Message: fmt.Sprintf("GET %s: %v", path, err)}
			}
		}
		items = append(items, page.Items...)
		if next = page.Metadata.Continue; next == "" {
			return items, nil
		}
	}
}
