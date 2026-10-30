// Package gcp is the CLI's Google Cloud client: REST over net/http with the
// tokens of gcloud (no Google client library), Google's error JSON as *Error,
// and the waiting of long-running and Compute Engine operations.
package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// now and sleep are the clock of the cache and of the polling; tests replace them.
var (
	now   = time.Now
	sleep = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
)

const tokenTTL = 5 * time.Minute

// Runner runs a command and returns its standard output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner is the default Runner, on os/exec; a failure carries stderr.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// TokenSource gives the credentials of the Google account in use.
type TokenSource interface {
	AccessToken(ctx context.Context) (string, error)
	IdentityToken(ctx context.Context, audience string) (string, error)
	Account(ctx context.Context) (string, error)
}

type gcloud struct {
	configuration string
	run           Runner

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewGcloud reads the tokens from gcloud's configuration (empty: the active
// one). A nil run is ExecRunner.
func NewGcloud(configuration string, run Runner) TokenSource {
	if run == nil {
		run = ExecRunner
	}
	return &gcloud{configuration: configuration, run: run}
}

func (g *gcloud) call(ctx context.Context, args ...string) (string, error) {
	if g.configuration != "" {
		args = append(args, "--configuration", g.configuration)
	}
	out, err := g.run(ctx, "gcloud", args...)
	if err != nil {
		return "", fmt.Errorf("gcloud %s: %w", strings.Join(args[:2], " "), err)
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", fmt.Errorf("gcloud %s: empty answer", strings.Join(args[:2], " "))
	}
	return v, nil
}

// printToken asks gcloud for a token; when gcloud cannot give one (its sign-in expired or revoked), the error says how to
// sign in again.
func (g *gcloud) printToken(ctx context.Context, args ...string) (string, error) {
	t, err := g.call(ctx, args...)
	if err != nil {
		login := "gcloud auth login"
		if g.configuration != "" {
			login += " --configuration " + g.configuration
		}
		return "", fmt.Errorf("%w: sign in with %s, then run the same command again", err, login)
	}
	return t, nil
}

func (g *gcloud) AccessToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" && now().Before(g.expires) {
		return g.token, nil
	}
	t, err := g.printToken(ctx, "auth", "print-access-token")
	if err != nil {
		return "", err
	}
	g.token, g.expires = t, now().Add(tokenTTL)
	return t, nil
}

// dropAccessToken forgets the cached access token: Google refused it (expired before the cache's 5 minutes ran out).
func (g *gcloud) dropAccessToken() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.token = ""
}

// dropper is a TokenSource that caches its access token.
type dropper interface{ dropAccessToken() }

func (g *gcloud) Account(ctx context.Context) (string, error) {
	return g.call(ctx, "config", "get-value", "account")
}

func (g *gcloud) IdentityToken(ctx context.Context, audience string) (string, error) {
	args := []string{"auth", "print-identity-token"}
	// Cloud Run accepts gcloud's own token for a user without an audience;
	// a service account needs one.
	account, err := g.Account(ctx)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(account, ".gserviceaccount.com") && audience != "" {
		args = append(args, "--audiences="+audience)
	}
	return g.printToken(ctx, args...)
}

// Error is a Google Cloud API error. Reason, Permission and Service are from its google.rpc.ErrorInfo: Service is the
// API a SERVICE_DISABLED names.
type Error struct {
	Code       int
	Status     string
	Reason     string
	Message    string
	Permission string
	Service    string
}

func (e *Error) Error() string {
	s := fmt.Sprintf("google: %d", e.Code)
	if e.Status != "" {
		s += " " + e.Status
	}
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.Permission != "" {
		s += " [permission " + e.Permission + "]"
	}
	return s
}

func asError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// IsNotFound is a 404.
func IsNotFound(err error) bool { e := asError(err); return e != nil && e.Code == 404 }

// IsConflict is a 409.
func IsConflict(err error) bool { e := asError(err); return e != nil && e.Code == 409 }

// IsDenied is a 403.
func IsDenied(err error) bool { e := asError(err); return e != nil && e.Code == 403 }

// IsPrecondition is a 412, or a 400 whose status is FAILED_PRECONDITION.
func IsPrecondition(err error) bool {
	e := asError(err)
	return e != nil && (e.Code == 412 || (e.Code == 400 && e.Status == "FAILED_PRECONDITION"))
}

func parseError(code int, body []byte) *Error {
	var p struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Type     string            `json:"@type"`
				Reason   string            `json:"reason"`
				Metadata map[string]string `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &p) != nil || (p.Error.Message == "" && p.Error.Status == "" && p.Error.Code == 0) {
		m := body
		if len(m) > 200 {
			m = m[:200]
		}
		return &Error{Code: code, Message: string(m)}
	}
	e := &Error{Code: code, Status: p.Error.Status, Message: p.Error.Message}
	for _, d := range p.Error.Details {
		if strings.HasSuffix(d.Type, "google.rpc.ErrorInfo") {
			e.Reason = d.Reason
			e.Permission, e.Service = d.Metadata["permission"], d.Metadata["service"]
			break
		}
	}
	return e
}

// Client calls Google Cloud REST APIs as the account of Tokens.
type Client struct {
	HTTP   *http.Client
	Tokens TokenSource
	// Sleep waits between the tries of a read that met a transient error (nil: a timer).
	Sleep func(ctx context.Context, d time.Duration) error
}

// readRetries is how many times a read is tried again after a transient error, waiting 1 s, 2 s, then 4 s.
const readRetries = 3

// transient tells that Google may answer the same request differently a moment later: throttled or unavailable.
func transient(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// reads tells that a request changes nothing, so sending it again is harmless: a GET (an operation's poll among them), a
// HEAD, or one of the POSTs that only read (getIamPolicy, testIamPermissions).
func reads(method, u string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return true
	}
	path, _, _ := strings.Cut(u, "?")
	return method == http.MethodPost && (strings.HasSuffix(path, ":getIamPolicy") || strings.HasSuffix(path, ":testIamPermissions"))
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	return sleep(ctx, d)
}

// Do sends in as JSON (when non-nil) and decodes the answer into out (when non-nil). A non-2xx answer is an *Error. A
// 401 (the token expired before gcloud's cache knew it) drops the cached token and sends the request once more with a
// new one from gcloud. A read answered 429, 500, 502, 503 or 504 is tried again up to 3 times, after 1 s, 2 s and 4 s;
// a request that may change something is sent once.
func (c *Client) Do(ctx context.Context, method, url string, in, out any) error {
	return c.exchange(ctx, method, url, c.Tokens.AccessToken, "", in, out)
}

// Invoke is Do with an identity token for audience instead of the access token: a call of a Cloud Run service whose
// invoker IAM check is on.
func (c *Client) Invoke(ctx context.Context, method, url, audience string, in, out any) error {
	token := func(ctx context.Context) (string, error) { return c.Tokens.IdentityToken(ctx, audience) }
	return c.exchange(ctx, method, url, token, "", in, out)
}

// Head is Do's HEAD, asking for the media types of accept: nil when the resource is there.
func (c *Client) Head(ctx context.Context, url string, accept ...string) error {
	return c.exchange(ctx, http.MethodHead, url, c.Tokens.AccessToken, strings.Join(accept, ", "), nil, nil)
}

func (c *Client) exchange(ctx context.Context, method, url string, token func(context.Context) (string, error), accept string, in, out any) error {
	var payload []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		payload = b
	}
	refreshed, retries := false, 0
	for {
		code, data, err := c.send(ctx, method, url, token, accept, payload)
		if err != nil {
			return err
		}
		if code == http.StatusUnauthorized && !refreshed {
			refreshed = true
			if d, ok := c.Tokens.(dropper); ok {
				d.dropAccessToken()
			}
			continue
		}
		if transient(code) && retries < readRetries && reads(method, url) {
			if err := c.sleep(ctx, time.Second<<retries); err != nil {
				return err
			}
			retries++
			continue
		}
		if code < 200 || code > 299 {
			return parseError(code, data)
		}
		if out != nil && len(bytes.TrimSpace(data)) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	}
}

// send sends one request (payload: its JSON body, nil for none; accept: its Accept header, "" for none) and returns the
// answer's code and body.
func (c *Client) send(ctx context.Context, method, url string, token func(context.Context) (string, error), accept string, payload []byte) (int, []byte, error) {
	bearer, err := token(ctx)
	if err != nil {
		return 0, nil, err
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	h := c.HTTP
	if h == nil {
		h = http.DefaultClient
	}
	res, err := h.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, nil, err
	}
	return res.StatusCode, data, nil
}

type operation struct {
	Name  string `json:"name"`
	Done  bool   `json:"done"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// grpcCodes maps an operation's gRPC error code to the HTTP code and the status name.
var grpcCodes = map[int]struct {
	http   int
	status string
}{
	3:  {400, "INVALID_ARGUMENT"},
	4:  {504, "DEADLINE_EXCEEDED"},
	5:  {404, "NOT_FOUND"},
	6:  {409, "ALREADY_EXISTS"},
	7:  {403, "PERMISSION_DENIED"},
	8:  {429, "RESOURCE_EXHAUSTED"},
	9:  {400, "FAILED_PRECONDITION"},
	10: {409, "ABORTED"},
	14: {503, "UNAVAILABLE"},
}

func fromGRPC(code int) (int, string) {
	if m, ok := grpcCodes[code]; ok {
		return m.http, m.status
	}
	return 500, "UNKNOWN"
}

// WaitOperation polls a long-running operation until it is done.
func WaitOperation(ctx context.Context, c *Client, url string, every, timeout time.Duration) error {
	deadline := now().Add(timeout)
	for {
		var op operation
		if err := c.Do(ctx, http.MethodGet, url, nil, &op); err != nil {
			return err
		}
		if op.Done {
			if op.Error != nil {
				code, status := fromGRPC(op.Error.Code)
				return &Error{Code: code, Status: status, Message: op.Error.Message}
			}
			return nil
		}
		if now().Add(every).After(deadline) {
			return fmt.Errorf("operation %s not done after %s", nameOr(op.Name, url), timeout)
		}
		if err := sleep(ctx, every); err != nil {
			return err
		}
	}
}

type computeOperation struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  *struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"error"`
}

const computePoll = 2 * time.Second

// WaitCompute polls a Compute Engine operation's selfLink until it is DONE.
func WaitCompute(ctx context.Context, c *Client, selfLink string, timeout time.Duration) error {
	deadline := now().Add(timeout)
	for {
		var op computeOperation
		if err := c.Do(ctx, http.MethodGet, selfLink, nil, &op); err != nil {
			return err
		}
		if op.Status == "DONE" {
			if op.Error != nil && len(op.Error.Errors) > 0 {
				msgs := make([]string, 0, len(op.Error.Errors))
				for _, e := range op.Error.Errors {
					msgs = append(msgs, e.Message)
				}
				return fmt.Errorf("compute operation %s failed: %s", nameOr(op.Name, selfLink), strings.Join(msgs, "; "))
			}
			return nil
		}
		if now().Add(computePoll).After(deadline) {
			return fmt.Errorf("compute operation %s not done after %s", nameOr(op.Name, selfLink), timeout)
		}
		if err := sleep(ctx, computePoll); err != nil {
			return err
		}
	}
}

func nameOr(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}
