package kubernetes

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ports router lambda/test/k8s.test.mjs (createKubernetes's reads): a fake API
// server whose certificate stands for the cluster's certificate authority.

type seenRequest struct {
	Method, URL, Authorization, Accept string
}

// apiServer starts a TLS server answering with handler; it returns the server,
// the cluster CA (its certificate, PEM) and the requests seen.
func apiServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, []byte, func() []seenRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []seenRequest
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, seenRequest{r.Method, r.URL.String(), r.Header.Get("Authorization"), r.Header.Get("Accept")})
		mu.Unlock()
		handler(w, r)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // a refused client's handshake error is expected
	srv.StartTLS()
	t.Cleanup(srv.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	return srv, ca, func() []seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenRequest(nil), seen...)
	}
}

// selfSigned is a certificate authority that signed nothing here.
func selfSigned(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "another"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func status(code int, reason, message string) map[string]any {
	return map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "code": code, "reason": reason, "message": message}
}

func asError(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v (%T) is not a *kubernetes.Error", err, err)
	}
	return e
}

func TestACallTrustsTheClusterCASendsTheBearerTokenAndReturnsTheAnswer(t *testing.T) {
	srv, ca, seen := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": "web"}})
	})
	k := New(srv.URL, ca, "k8s-aws-v1.abc")
	got, err := k.Get(context.Background(), "/apis/apps/v1/namespaces/demo/deployments/web")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Kind     string
		Metadata struct{ Name string }
	}
	if err := json.Unmarshal(got, &v); err != nil || v.Kind != "Deployment" || v.Metadata.Name != "web" {
		t.Fatalf("got %s (%v)", got, err)
	}
	s := seen()
	if len(s) != 1 || s[0] != (seenRequest{"GET", "/apis/apps/v1/namespaces/demo/deployments/web", "Bearer k8s-aws-v1.abc", "application/json"}) {
		t.Fatalf("seen %+v", s)
	}
}

func TestAServerWhoseCertificateTheClusterCADidNotSignIsRefusedNothingSent(t *testing.T) {
	srv, _, seen := apiServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{}) })
	otherCA := selfSigned(t)
	for name, ca := range map[string][]byte{"another CA": otherCA, "the system roots": nil} {
		_, err := New(srv.URL, ca, "secret").Get(context.Background(), "/api/v1/namespaces")
		e := asError(t, err)
		if e.Status != 0 {
			t.Errorf("%s: status %d", name, e.Status)
		}
		if !strings.Contains(e.Message, "certificate") {
			t.Errorf("%s: message %q", name, e.Message)
		}
	}
	if len(seen()) != 0 {
		t.Fatalf("a request reached the server: %+v", seen())
	}
}

func Test403And404AreErrorsWithTheStatusTheReasonAndTheMessageOfTheAPI(t *testing.T) {
	srv, ca, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "forbidden") {
			writeJSON(w, 403, status(403, "Forbidden", `deployments.apps "web" is forbidden: User "dormeto" cannot get resource`))
			return
		}
		writeJSON(w, 404, status(404, "NotFound", `deployments.apps "gone" not found`))
	})
	k := New(srv.URL, ca, "t")
	_, err := k.Get(context.Background(), "/forbidden")
	if e := asError(t, err); *e != (Error{403, "Forbidden", `deployments.apps "web" is forbidden: User "dormeto" cannot get resource`}) {
		t.Fatalf("%+v", e)
	}
	_, err = k.Get(context.Background(), "/gone")
	if e := asError(t, err); *e != (Error{404, "NotFound", `deployments.apps "gone" not found`}) {
		t.Fatalf("%+v", e)
	}
	if err.Error() != `deployments.apps "gone" not found` {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestAnErrorAnswerThatIsNotAKubernetesStatusKeepsTheHTTPStatusAndItsText(t *testing.T) {
	srv, ca, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	})
	_, err := New(srv.URL, ca, "t").Get(context.Background(), "/healthz")
	e := asError(t, err)
	if e.Status != 502 || e.Reason != "Bad Gateway" || e.Message != "GET /healthz: 502 <html>bad gateway</html>" {
		t.Fatalf("%+v", e)
	}
}

func TestListPassesTheQueryAndFollowsContinueTokensReturningEveryItem(t *testing.T) {
	srv, ca, seen := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("continue") == "page2" {
			writeJSON(w, 200, map[string]any{"kind": "PodList", "metadata": map[string]any{}, "items": []any{map[string]any{"n": 3}}})
			return
		}
		writeJSON(w, 200, map[string]any{"kind": "PodList", "metadata": map[string]any{"continue": "page2"}, "items": []any{map[string]any{"n": 1}, map[string]any{"n": 2}}})
	})
	items, err := New(srv.URL, ca, "t").List(context.Background(), "/api/v1/namespaces/demo/pods", map[string]string{"labelSelector": "app=web,tier in (a,b)"})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(items); string(b) != `[{"n":1},{"n":2},{"n":3}]` {
		t.Fatalf("items %s", b)
	}
	s := seen()
	if len(s) != 2 {
		t.Fatalf("seen %+v", s)
	}
	for i, r := range s {
		u, _ := url.Parse(r.URL)
		if u.Path != "/api/v1/namespaces/demo/pods" || u.Query().Get("labelSelector") != "app=web,tier in (a,b)" {
			t.Errorf("request %d: %s", i, r.URL)
		}
	}
}

func TestAnAnswerWithoutItemsIsAnEmptyList(t *testing.T) {
	srv, ca, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"kind": "PodList"}) })
	items, err := New(srv.URL, ca, "t").List(context.Background(), "/api/v1/pods", nil)
	if err != nil || len(items) != 0 {
		t.Fatalf("%v %v", items, err)
	}
}

func TestTheClientConnectsWithin3SecondsAndFailsAfter10SecondsOfSilence(t *testing.T) {
	if DefaultTimeouts != (Timeouts{Connect: 3 * time.Second, Idle: 10 * time.Second}) {
		t.Fatalf("%+v", DefaultTimeouts)
	}
}

func TestAServerSilentAfterTheRequestFailsTheCallWithATimeoutOnceTheIdleBoundPasses(t *testing.T) {
	done := make(chan struct{})
	srv, ca, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) { <-done })
	t.Cleanup(func() { close(done) })
	k := newClient(srv.URL, ca, "t", Timeouts{Connect: time.Second, Idle: 200 * time.Millisecond})
	started := time.Now()
	_, err := k.Get(context.Background(), "/api/v1/pods")
	e := asError(t, err)
	if e.Status != 0 || e.Reason != "Timeout" {
		t.Fatalf("%+v", e)
	}
	if d := time.Since(started); d < 150*time.Millisecond || d > 2*time.Second {
		t.Fatalf("failed after %v", d)
	}
}

func TestADripFeedingServerIsNotIdle(t *testing.T) {
	srv, ca, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		for i := 0; i < 5; i++ {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write([]byte(" "))
			w.(http.Flusher).Flush()
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	got, err := newClient(srv.URL, ca, "t", Timeouts{Connect: time.Second, Idle: 300 * time.Millisecond}).Get(context.Background(), "/x")
	if err != nil || strings.TrimSpace(string(got)) != `{"ok":true}` {
		t.Fatalf("%s %v", got, err)
	}
}

func TestAnEndpointThatNeverCompletesTheTLSHandshakeFailsTheCallOnceTheConnectBoundPasses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conns []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	})
	_, ca, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {})
	k := newClient("https://"+ln.Addr().String(), ca, "t", Timeouts{Connect: 200 * time.Millisecond, Idle: 5 * time.Second})
	started := time.Now()
	_, err = k.Get(context.Background(), "/api/v1/pods")
	e := asError(t, err)
	if e.Status != 0 || e.Reason != "Timeout" {
		t.Fatalf("%+v", e)
	}
	if d := time.Since(started); d < 150*time.Millisecond || d > 2*time.Second {
		t.Fatalf("failed after %v", d)
	}
}

func TestARefusedConnectionIsAnErrorWithTheNetworkErrorsCode(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	_, err = New("https://"+addr, nil, "t").Get(context.Background(), "/api")
	if e := asError(t, err); e.Status != 0 || e.Reason != "ECONNREFUSED" {
		t.Fatalf("%+v", e)
	}
}

func TestACancelledContextFailsTheCall(t *testing.T) {
	srv, ca, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{}) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(srv.URL, ca, "t").Get(ctx, "/x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}
