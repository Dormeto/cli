package fleet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

var creds = credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "")

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.Client(), srv.URL, "eu-west-1", creds, func() time.Time { return time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC) })
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestGetSignsAndPrefixesIAM(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/iam/routers" || r.URL.Query().Get("state") != "running" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		if a := r.Header.Get("Authorization"); !strings.HasPrefix(a, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261019/eu-west-1/execute-api/aws4_request") {
			t.Errorf("Authorization = %q", a)
		}
		if r.Header.Get("X-Amz-Date") != "20261019T080000Z" {
			t.Errorf("X-Amz-Date = %q", r.Header.Get("X-Amz-Date"))
		}
		io.WriteString(w, `{"rows":[{"name":"a"}]}`)
	})
	var out struct{ Rows []struct{ Name string } }
	if err := c.Get(context.Background(), "/routers", url.Values{"state": {"running"}}, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 1 || out.Rows[0].Name != "a" {
		t.Errorf("out = %+v", out)
	}
}

func TestPostSignsTheBody(t *testing.T) {
	var sigs []string
	var bodies []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		sigs = append(sigs, r.Header.Get("Authorization"))
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		io.WriteString(w, `{"id":"op1"}`)
	})
	var out struct{ ID string }
	for _, body := range []any{map[string]string{"a": "1"}, map[string]string{"a": "2"}} {
		if err := c.Post(context.Background(), "/operations", body, &out); err != nil {
			t.Fatal(err)
		}
	}
	if out.ID != "op1" || bodies[0] != `{"a":"1"}` || bodies[1] != `{"a":"2"}` {
		t.Errorf("out = %+v, bodies = %q", out, bodies)
	}
	if sigs[0] == sigs[1] {
		t.Error("the signature does not depend on the body")
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    string
		gateway bool
		code    string
	}{
		{"forbidden", 403, `{"message":"Forbidden"}`, "execute-api:Invoke", true, ""},
		{"expired token", 403, `{"message":"The security token included in the request is expired"}`, "execute-api:Invoke", true, ""},
		{"not found", 404, `{"message":"Not Found"}`, "update it to 0.15.0 or later", true, ""},
		{"manager", 409, `{"error":"conflict","message":"m"}`, "m", false, "conflict"},
		{"opaque", 500, `oops`, "500", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			})
			err := c.Post(context.Background(), "/x", nil, nil)
			var ae *APIError
			if !errors.As(err, &ae) {
				t.Fatalf("err = %v", err)
			}
			if ae.Status != tt.status || ae.Gateway != tt.gateway || ae.Code != tt.code || !strings.Contains(ae.Error(), tt.want) {
				t.Errorf("err = %+v (%s)", ae, ae)
			}
			if tt.name == "expired token" && !strings.Contains(ae.Error(), "(The security token included in the request is expired)") {
				t.Errorf("the gateway's message is lost: %q", ae)
			}
			if tt.name == "forbidden" && strings.Contains(ae.Error(), "(Forbidden)") {
				t.Errorf("a plain Forbidden is repeated: %q", ae)
			}
			if tt.name == "manager" && ae.Error() != "m" {
				t.Errorf("Error() = %q", ae.Error())
			}
		})
	}
}

func TestGetRetriesPostDoesNot(t *testing.T) {
	var calls atomic.Int32
	h := func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		io.WriteString(w, `{}`)
	}
	c := newTestClient(t, h)
	if err := c.Get(context.Background(), "/routers", nil, nil); err != nil || calls.Load() != 2 {
		t.Errorf("get: err = %v, calls = %d", err, calls.Load())
	}
	calls.Store(0)
	if err := c.Post(context.Background(), "/routers", nil, nil); err == nil || calls.Load() != 1 {
		t.Errorf("post: err = %v, calls = %d", err, calls.Load())
	}
}

func TestGetGivesUpAfterThreeAttempts(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
	})
	err := c.Get(context.Background(), "/routers", nil, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 429 || calls.Load() != 3 {
		t.Errorf("err = %v, calls = %d", err, calls.Load())
	}
}

func TestCredentialsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("called") })
	c.creds = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return aws.Credentials{}, errors.New("expired") })
	if err := c.Get(context.Background(), "/routers", nil, nil); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("err = %v", err)
	}
}

func TestCancelledDuringBackoffStops(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
	})
	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	err := c.Get(ctx, "/routers", nil, nil)
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Errorf("err = %v, calls = %d", err, calls.Load())
	}
}

func TestAnswerTooLarge(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte(" "), maxAnswer+1))
	})
	err := c.Get(context.Background(), "/routers", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("err = %v", err)
	}
}
