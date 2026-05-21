package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/gin-gonic/gin"
)

// getCtx is a small wrapper so tests carry a context (lint: noctx). Uses
// the test's background context — sufficient for these in-process httptest
// targets.
func getCtx(t *testing.T, urlStr string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, urlStr, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// capturedRequest snapshots a request the fake-Mist server receives. The
// proxy writes to a real net.Conn so the upstream request observed by the
// test is exactly what Mist would see on the wire.
type capturedRequest struct {
	method string
	path   string
	query  string
	header http.Header
}

func newFakeMist(t *testing.T) (*httptest.Server, *capturedRequest, *sync.Mutex) {
	t.Helper()
	captured := &capturedRequest{}
	mu := &sync.Mutex{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		captured.method = r.Method
		captured.path = r.URL.Path
		captured.query = r.URL.RawQuery
		captured.header = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Set-Cookie", "mist_sid=abc; Path=/; HttpOnly")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	return srv, captured, mu
}

// proxyRouter wires the proxy at /_mist with no auth gate so tests can
// exercise the rewrite rules directly.
func proxyRouter(t *testing.T, upstream string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	proxy := newMistAdminReverseProxy(target)
	r := gin.New()
	r.Any("/_mist", func(c *gin.Context) { proxy.ServeHTTP(c.Writer, c.Request) })
	r.Any("/_mist/*proxy", func(c *gin.Context) { proxy.ServeHTTP(c.Writer, c.Request) })
	return r
}

func TestMistAdminProxy_StripsMistPrefix(t *testing.T) {
	mist, captured, mu := newFakeMist(t)
	router := proxyRouter(t, mist.URL)
	front := httptest.NewServer(router)
	defer front.Close()

	resp := getCtx(t, front.URL+"/_mist/api2")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if captured.path != "/api2" {
		t.Errorf("upstream path: got %q, want %q (the /_mist prefix must be stripped)", captured.path, "/api2")
	}
}

func TestMistAdminProxy_BarePrefixBecomesRoot(t *testing.T) {
	mist, captured, mu := newFakeMist(t)
	router := proxyRouter(t, mist.URL)
	front := httptest.NewServer(router)
	defer front.Close()

	resp := getCtx(t, front.URL+"/_mist")
	defer resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if captured.path != "/" {
		t.Errorf("upstream path: got %q, want %q (bare /_mist should rewrite to /)", captured.path, "/")
	}
}

func TestMistAdminProxy_QueryStringPreserved(t *testing.T) {
	mist, captured, mu := newFakeMist(t)
	router := proxyRouter(t, mist.URL)
	front := httptest.NewServer(router)
	defer front.Close()

	resp := getCtx(t, front.URL+"/_mist/api?logs=100&streams=1")
	defer resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if captured.query != "logs=100&streams=1" {
		t.Errorf("upstream query: got %q, want %q", captured.query, "logs=100&streams=1")
	}
}

func TestMistAdminProxy_ScrubsCredentialAndForwardedHeaders(t *testing.T) {
	mist, captured, mu := newFakeMist(t)
	router := proxyRouter(t, mist.URL)
	front := httptest.NewServer(router)
	defer front.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, front.URL+"/_mist/api2", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer platform-jwt-token")
	req.Header.Set("Cookie", "fw_session=secret; other=value")
	req.Header.Set("Forwarded", "for=203.0.113.1")
	req.Header.Set("X-Forwarded-For", "203.0.113.1, 203.0.113.2")
	req.Header.Set("X-Forwarded-Host", "edge.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Real-IP", "203.0.113.1")
	req.Header.Set("User-Agent", "test-client/1.0") // benign — should pass through

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	mustAbsent := []string{
		"Authorization",
		"Cookie",
		"Forwarded",
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
		"X-Real-IP",
	}
	for _, h := range mustAbsent {
		if got := captured.header.Get(h); got != "" {
			t.Errorf("header %q must be scrubbed before reaching Mist; got %q", h, got)
		}
	}
	if ua := captured.header.Get("User-Agent"); ua != "test-client/1.0" {
		t.Errorf("benign header should pass through; User-Agent = %q", ua)
	}
}

func TestMistAdminProxy_DoesNotInjectXForwardedForOnRewrite(t *testing.T) {
	// Even when the client sends no X-Forwarded-For, Go's older Director-
	// based ReverseProxy would synthesize one from RemoteAddr. The Rewrite
	// API does not. This test guards against any regression that switches
	// back to Director or otherwise reintroduces XFF — Mist's loopback
	// auto-auth refuses the bypass if XFF is present.
	mist, captured, mu := newFakeMist(t)
	router := proxyRouter(t, mist.URL)
	front := httptest.NewServer(router)
	defer front.Close()

	resp := getCtx(t, front.URL+"/_mist/api2")
	defer resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if got := captured.header.Get("X-Forwarded-For"); got != "" {
		t.Errorf("X-Forwarded-For must never be set on upstream (breaks Mist loopback bypass); got %q", got)
	}
	if got := captured.header.Get("Forwarded"); got != "" {
		t.Errorf("Forwarded must never be set on upstream; got %q", got)
	}
}

func TestMistAdminProxy_StripsUpstreamSetCookie(t *testing.T) {
	mist, _, _ := newFakeMist(t) // sets Set-Cookie unconditionally
	router := proxyRouter(t, mist.URL)
	front := httptest.NewServer(router)
	defer front.Close()

	resp := getCtx(t, front.URL+"/_mist/api2")
	defer resp.Body.Close()

	if cookies := resp.Header.Values("Set-Cookie"); len(cookies) != 0 {
		t.Errorf("Set-Cookie from Mist must be stripped before the browser sees it; got %v", cookies)
	}
}

func TestMistAdminProxy_PreservesWebSocketUpgrade(t *testing.T) {
	mist, captured, mu := newFakeMist(t)
	router := proxyRouter(t, mist.URL)
	front := httptest.NewServer(router)
	defer front.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, front.URL+"/_mist/ws", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if got := captured.header.Get("Upgrade"); !strings.EqualFold(got, "websocket") {
		t.Errorf("Upgrade header should reach upstream for WebSocket negotiation; got %q", got)
	}
	if got := captured.header.Get("Connection"); !strings.Contains(strings.ToLower(got), "upgrade") {
		t.Errorf("Connection header should include 'upgrade' on upstream; got %q", got)
	}
	if got := captured.header.Get("Sec-WebSocket-Key"); got == "" {
		t.Errorf("Sec-WebSocket-Key should reach upstream; got empty")
	}
}

func TestMistAdminProxy_NonLoopbackUpstreamReturns501(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := logging.NewLogger()
	handler := MistAdminProxy("http://mistserver:4242", logger)

	r := gin.New()
	r.Any("/_mist/*proxy", handler)

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp := getCtx(t, srv.URL+"/_mist/api2")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("non-loopback upstream must return 501; got %d", resp.StatusCode)
	}
}

func TestMistAdminProxy_InvalidUpstreamReturns500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := logging.NewLogger()
	handler := MistAdminProxy("not-a-valid-url", logger)

	r := gin.New()
	r.Any("/_mist/*proxy", handler)

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp := getCtx(t, srv.URL+"/_mist/api2")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("invalid MISTSERVER_URL must return 500; got %d", resp.StatusCode)
	}
}

func TestMistAdminProxy_LoopbackUpstreamShapes(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"127.1.2.3", true},
		{"::1", true},
		{"mistserver", false},
		{"10.0.0.1", false},
		{"203.0.113.1", false},
		{"example.com", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopbackHost(c.host); got != c.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestRequireMistAdmin_RejectsWhenNoCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Any("/_mist", RequireMistAdmin(logging.NewLogger()), func(c *gin.Context) {
		t.Fatalf("downstream handler must not be reached without credentials")
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp := getCtx(t, srv.URL+"/_mist")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no cookie must yield 401; got %d", resp.StatusCode)
	}
}
