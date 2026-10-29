package gcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeTokens struct{}

func (fakeTokens) AccessToken(context.Context) (string, error)           { return "tok", nil }
func (fakeTokens) IdentityToken(context.Context, string) (string, error) { return "id", nil }
func (fakeTokens) Account(context.Context) (string, error)               { return "a@b.c", nil }

// fakeClock replaces the package clock and sleep so that polling is instant.
func fakeClock(t *testing.T) *time.Time {
	t.Helper()
	cur := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	oldNow, oldSleep := now, sleep
	now = func() time.Time { return cur }
	sleep = func(_ context.Context, d time.Duration) error { cur = cur.Add(d); return nil }
	t.Cleanup(func() { now, sleep = oldNow, oldSleep })
	return &cur
}

func newClient(srv *httptest.Server) *Client {
	return &Client{HTTP: srv.Client(), Tokens: fakeTokens{}}
}

func TestDoSendsBearerAndJSON(t *testing.T) {
	var gotAuth, gotCT, gotBody, gotQuota string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotCT, gotQuota = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Header.Get("x-goog-user-project")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"name":"x"}`))
	}))
	defer srv.Close()
	var out struct{ Name string }
	if err := newClient(srv).Do(context.Background(), "POST", srv.URL, map[string]int{"a": 1}, &out); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" || gotCT != "application/json" || gotBody != `{"a":1}` || gotQuota != "" || out.Name != "x" {
		t.Fatalf("auth=%q ct=%q body=%q quota=%q out=%v", gotAuth, gotCT, gotBody, gotQuota, out)
	}
}

func TestDoWithoutBodyHasNoContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "" {
			t.Errorf("content type %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	if err := newClient(srv).Do(context.Background(), "GET", srv.URL, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestErrors(t *testing.T) {
	denied := `{"error":{"code":403,"message":"nope","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"IAM_PERMISSION_DENIED","metadata":{"permission":"compute.urlMaps.get"}}]}}`
	cases := []struct {
		name       string
		code       int
		body       string
		is         func(error) bool
		wantCode   int
		wantMsg    string
		wantPerm   string
		wantReason string
		wantStatus string
	}{
		{"denied", 403, denied, IsDenied, 403, "nope", "compute.urlMaps.get", "IAM_PERMISSION_DENIED", "PERMISSION_DENIED"},
		{"not found", 404, `{"error":{"code":404,"message":"gone","status":"NOT_FOUND"}}`, IsNotFound, 404, "gone", "", "", "NOT_FOUND"},
		{"conflict", 409, `{"error":{"code":409,"message":"dup","status":"ALREADY_EXISTS"}}`, IsConflict, 409, "dup", "", "", "ALREADY_EXISTS"},
		{"precondition 412", 412, `{"error":{"code":412,"message":"fp","status":"FAILED_PRECONDITION"}}`, IsPrecondition, 412, "fp", "", "", "FAILED_PRECONDITION"},
		{"precondition 400", 400, `{"error":{"code":400,"message":"bad","status":"FAILED_PRECONDITION"}}`, IsPrecondition, 400, "bad", "", "", "FAILED_PRECONDITION"},
		{"non JSON", 502, strings.Repeat("x", 300), func(error) bool { return true }, 502, strings.Repeat("x", 200), "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.code)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			err := newClient(srv).Do(context.Background(), "GET", srv.URL, nil, nil)
			var ge *Error
			if !errors.As(err, &ge) {
				t.Fatalf("not an *Error: %v", err)
			}
			if !c.is(fmt.Errorf("wrapped: %w", err)) {
				t.Fatalf("predicate false for %v", err)
			}
			if ge.Code != c.wantCode || ge.Message != c.wantMsg || ge.Permission != c.wantPerm || ge.Reason != c.wantReason || ge.Status != c.wantStatus {
				t.Fatalf("got %+v", ge)
			}
		})
	}
	if IsDenied(errors.New("x")) || IsNotFound(nil) || IsConflict(nil) || IsPrecondition(nil) {
		t.Fatal("predicates must be false for other errors")
	}
	if (&Error{Code: 400, Status: "INVALID_ARGUMENT"}).Error() == "" {
		t.Fatal("empty Error()")
	}
	if IsPrecondition(&Error{Code: 400, Status: "INVALID_ARGUMENT"}) {
		t.Fatal("400 without FAILED_PRECONDITION is not a precondition")
	}
}

func TestWaitOperation(t *testing.T) {
	fakeClock(t)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			_, _ = w.Write([]byte(`{"name":"op1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"op1","done":true}`))
	}))
	defer srv.Close()
	if err := WaitOperation(context.Background(), newClient(srv), srv.URL, time.Second, time.Minute); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 3 {
		t.Fatalf("polls %d", n.Load())
	}
}

func TestWaitOperationError(t *testing.T) {
	fakeClock(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"op1","done":true,"error":{"code":9,"message":"boom"}}`))
	}))
	defer srv.Close()
	err := WaitOperation(context.Background(), newClient(srv), srv.URL, time.Second, time.Minute)
	var ge *Error
	if !errors.As(err, &ge) || ge.Message != "boom" || ge.Code != 9 {
		t.Fatalf("got %v", err)
	}
}

func TestWaitOperationTimeout(t *testing.T) {
	fakeClock(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"operations/op42"}`))
	}))
	defer srv.Close()
	err := WaitOperation(context.Background(), newClient(srv), srv.URL, time.Second, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "operations/op42") {
		t.Fatalf("got %v", err)
	}
}

func TestWaitOperationPollError(t *testing.T) {
	fakeClock(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"x","status":"NOT_FOUND"}}`))
	}))
	defer srv.Close()
	if err := WaitOperation(context.Background(), newClient(srv), srv.URL, time.Second, time.Minute); !IsNotFound(err) {
		t.Fatalf("got %v", err)
	}
}

func TestWaitCompute(t *testing.T) {
	fakeClock(t)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 2 {
			_, _ = w.Write([]byte(`{"name":"op","status":"RUNNING"}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
	}))
	defer srv.Close()
	if err := WaitCompute(context.Background(), newClient(srv), srv.URL, time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestWaitComputeError(t *testing.T) {
	fakeClock(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"op","status":"DONE","error":{"errors":[{"code":"A","message":"first"},{"code":"B","message":"second"}]}}`))
	}))
	defer srv.Close()
	err := WaitCompute(context.Background(), newClient(srv), srv.URL, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") {
		t.Fatalf("got %v", err)
	}
}

func TestWaitComputeTimeout(t *testing.T) {
	fakeClock(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"op-7","status":"RUNNING"}`))
	}))
	defer srv.Close()
	err := WaitCompute(context.Background(), newClient(srv), srv.URL, 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "op-7") {
		t.Fatalf("got %v", err)
	}
}

type call struct {
	name string
	args []string
}

func recorder(out map[string]string, calls *[]call) Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, call{name, args})
		return []byte(out[args[0]+" "+args[1]] + "\n"), nil
	}
}

func TestGcloudArguments(t *testing.T) {
	var calls []call
	out := map[string]string{"auth print-access-token": "AT", "auth print-identity-token": "IT", "config get-value": "me@example.com"}
	g := NewGcloud("dormeto-dev", recorder(out, &calls))
	ctx := context.Background()
	if v, _ := g.AccessToken(ctx); v != "AT" {
		t.Fatal(v)
	}
	if v, _ := g.Account(ctx); v != "me@example.com" {
		t.Fatal(v)
	}
	if v, _ := g.IdentityToken(ctx, "https://aud"); v != "IT" {
		t.Fatal(v)
	}
	last := calls[len(calls)-1]
	if strings.Join(last.args, " ") != "auth print-identity-token --configuration dormeto-dev" || calls[0].name != "gcloud" {
		t.Fatalf("calls %+v", calls)
	}
	if strings.Join(calls[0].args, " ") != "auth print-access-token --configuration dormeto-dev" ||
		strings.Join(calls[1].args, " ") != "config get-value account --configuration dormeto-dev" {
		t.Fatalf("calls %+v", calls)
	}
}

func TestGcloudServiceAccountAudience(t *testing.T) {
	var calls []call
	out := map[string]string{"auth print-identity-token": "IT", "config get-value": "sa@p.iam.gserviceaccount.com"}
	g := NewGcloud("", recorder(out, &calls))
	if _, err := g.IdentityToken(context.Background(), "https://aud"); err != nil {
		t.Fatal(err)
	}
	last := strings.Join(calls[len(calls)-1].args, " ")
	if last != "auth print-identity-token --audiences=https://aud" {
		t.Fatalf("got %q (calls %+v)", last, calls)
	}
	if strings.Contains(strings.Join(calls[0].args, " "), "--configuration") {
		t.Fatalf("empty configuration must omit the flag: %+v", calls[0])
	}
}

func TestGcloudError(t *testing.T) {
	g := NewGcloud("x", func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("exec failed") })
	if _, err := g.AccessToken(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestAccessTokenCache(t *testing.T) {
	cur := fakeClock(t)
	var calls []call
	g := NewGcloud("c", recorder(map[string]string{"auth print-access-token": "AT"}, &calls))
	ctx := context.Background()
	_, _ = g.AccessToken(ctx)
	*cur = cur.Add(4 * time.Minute)
	_, _ = g.AccessToken(ctx)
	if len(calls) != 1 {
		t.Fatalf("calls %d", len(calls))
	}
	*cur = cur.Add(2 * time.Minute)
	_, _ = g.AccessToken(ctx)
	if len(calls) != 2 {
		t.Fatalf("calls %d", len(calls))
	}
}
