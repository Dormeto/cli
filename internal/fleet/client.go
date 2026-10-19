package fleet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// getAttempts is how many times a GET is tried (429 and 5xx are retried); a POST is never retried, it may have started
// an operation.
const getAttempts = 3

// maxAnswer bounds what is read of an answer (a listing of 1,000 workloads is a few MB).
const maxAnswer = 32 << 20

// ErrNotSent marks an error from before the request left: the credentials or the signature failed, nothing reached
// the manager.
var ErrNotSent = errors.New("not started")

// APIError is a refusal from API Gateway (Gateway) or from the manager (Code and Message).
type APIError struct {
	Status        int
	Code, Message string
	Gateway       bool

	retryAfter    time.Duration // the Retry-After of a 429 or 503, when it has one and it is readable
	hasRetryAfter bool
}

// notSentError is an error from before the request left; it is ErrNotSent.
type notSentError struct{ err error }

func (e *notSentError) Error() string        { return e.err.Error() }
func (e *notSentError) Unwrap() error        { return e.err }
func (e *notSentError) Is(target error) bool { return target == ErrNotSent }

// unreadableError is a 2xx answer whose body cannot be decoded (of a GET or a POST): the call was done, only its result
// is lost; for a start, the operation may exist.
type unreadableError struct{ err error }

func (e *unreadableError) Error() string { return e.err.Error() }
func (e *unreadableError) Unwrap() error { return e.err }

// maxRetryAfter caps how long a Retry-After is waited.
const maxRetryAfter = 30 * time.Second

// retryAfter reads a Retry-After header (seconds or an HTTP date); a wait in the past is zero.
func retryAfter(h string, now time.Time) (time.Duration, bool) {
	h = strings.TrimSpace(h)
	var d time.Duration
	if secs, err := strconv.ParseInt(h, 10, 32); err == nil {
		d = time.Duration(secs) * time.Second
	} else if at, err := http.ParseTime(h); err == nil {
		d = at.Sub(now)
	} else {
		return 0, false
	}
	return min(max(d, 0), maxRetryAfter), true
}

func (e *APIError) Error() string {
	switch {
	case e.Gateway && e.Status == http.StatusForbidden:
		hint := "the manager's API refused the request (forbidden): the profile's identity needs execute-api:Invoke " +
			"on the manager API's /iam/* routes"
		if e.Message != "Forbidden" { // an expired token, a clock skew, a wrong signing region: the gateway says which
			hint += " (" + e.Message + ")"
		}
		return hint
	case e.Gateway && e.Status == http.StatusNotFound:
		return "the manager's API has no /iam routes: the manager predates them, update it to 0.15.0 or later"
	case e.Message != "":
		return e.Message
	}
	return fmt.Sprintf("the manager's API answered %d", e.Status)
}

// Client calls the manager's /iam routes, signing each request with SigV4 for execute-api.
type Client struct {
	http    *http.Client
	base    string
	region  string
	creds   aws.CredentialsProvider
	now     func() time.Time
	signer  *v4.Signer
	sleep   func(context.Context, time.Duration) error
	backoff time.Duration
}

// New is a Client of the API at baseURL (no trailing path), signing for region with creds; the timeout is httpClient's.
func New(httpClient *http.Client, baseURL, region string, creds aws.CredentialsProvider, now func() time.Time) *Client {
	return &Client{
		http: httpClient, base: strings.TrimRight(baseURL, "/"), region: region, creds: creds, now: now,
		signer: v4.NewSigner(), sleep: sleepCtx, backoff: 500 * time.Millisecond,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Get calls GET /iam<path>?query and decodes the JSON answer into out (nil to ignore it).
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	u := path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var err error
	for i := 1; i <= getAttempts; i++ {
		err = c.do(ctx, http.MethodGet, u, nil, out)
		if err == nil || !retryable(err) || i == getAttempts {
			return err
		}
		wait := time.Duration(i) * c.backoff
		if ae, ok := err.(*APIError); ok && ae.hasRetryAfter {
			wait = ae.retryAfter
		}
		if wait <= 0 { // a Retry-After of zero (or past): at once
			continue
		}
		if serr := c.sleep(ctx, wait); serr != nil {
			return serr
		}
	}
	return err
}

// retryable tells whether err is an answer worth another try: 429 or 5xx.
func retryable(err error) bool {
	e, ok := err.(*APIError)
	return ok && (e.Status == http.StatusTooManyRequests || e.Status >= 500)
}

// Post calls POST /iam<path> with body as JSON (nil for none) and decodes the answer into out; never retried.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	return c.do(ctx, http.MethodPost, path, payload, out)
}

func (c *Client) do(ctx context.Context, method, path string, payload []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/iam"+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	cr, err := c.creds.Retrieve(ctx)
	if err != nil {
		return &notSentError{fmt.Errorf("AWS credentials: %w", err)}
	}
	sum := sha256.Sum256(payload)
	if err := c.signer.SignHTTP(ctx, cr, req, hex.EncodeToString(sum[:]), "execute-api", c.region, c.now()); err != nil {
		return &notSentError{fmt.Errorf("sign the request: %w", err)}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return err
	}
	if len(data) > maxAnswer {
		return fmt.Errorf("the manager's answer is too large (over %d MiB)", maxAnswer>>20)
	}
	if resp.StatusCode >= 300 {
		ae := parseError(resp.StatusCode, data)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			ae.retryAfter, ae.hasRetryAfter = retryAfter(resp.Header.Get("Retry-After"), c.now())
		}
		return ae
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &unreadableError{fmt.Errorf("decode the manager's answer: %w", err)}
	}
	return nil
}

// parseError reads API Gateway's {"message"} or the manager's {"error","message"}; the former has no error code.
func parseError(status int, data []byte) *APIError {
	var b struct{ Error, Message string }
	_ = json.Unmarshal(data, &b)
	return &APIError{Status: status, Code: b.Error, Message: b.Message, Gateway: b.Error == "" && (status == http.StatusForbidden || status == http.StatusNotFound) && b.Message != ""}
}
