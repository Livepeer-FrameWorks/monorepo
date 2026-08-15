package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

func TestRequestIDMiddleware(t *testing.T) {
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/ping", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	if w.Header().Get("X-Request-ID") == "" {
		t.Fatalf("expected X-Request-ID header to be set")
	}
}

func TestRequestIDMiddlewarePreservesIncomingID(t *testing.T) {
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/ping", func(c *gin.Context) {
		requestID, ok := c.Get("request_id")
		if !ok {
			t.Fatal("expected request_id on context")
		}
		c.Header("X-Request-ID-Context", requestID.(string))
		c.String(http.StatusOK, "pong")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/ping", nil)
	req.Header.Set("X-Request-ID", "req-123")
	r.ServeHTTP(w, req)

	if got := w.Header().Get("X-Request-ID"); got != "req-123" {
		t.Fatalf("expected X-Request-ID header to be preserved, got %q", got)
	}
	if got := w.Header().Get("X-Request-ID-Context"); got != "req-123" {
		t.Fatalf("expected context request ID to match, got %q", got)
	}
}

func TestRequestIDMiddlewareGeneratesValidUUID(t *testing.T) {
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/ping", nil)
	r.ServeHTTP(w, req)

	requestID := w.Header().Get("X-Request-ID")
	if requestID == "" {
		t.Fatal("expected X-Request-ID header to be set")
	}
	if _, err := uuid.Parse(requestID); err != nil {
		t.Fatalf("expected valid UUID request ID, got %q", requestID)
	}
}

func TestTimeoutMiddleware(t *testing.T) {
	r := gin.New()
	r.Use(TimeoutMiddleware(10 * time.Millisecond))

	// Test handler that respects context cancellation
	r.GET("/context-aware", func(c *gin.Context) {
		select {
		case <-time.After(20 * time.Millisecond):
			c.String(http.StatusOK, "done")
		case <-c.Request.Context().Done():
			c.AbortWithStatus(http.StatusGatewayTimeout)
			return
		}
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/context-aware", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d", w.Code)
	}
}

func TestLoggingMiddleware(t *testing.T) {
	r := gin.New()
	logger := logging.NewLogger()
	r.Use(LoggingMiddleware(logger))
	r.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestRecoveryMiddleware(t *testing.T) {
	r := gin.New()
	logger := logging.NewLogger()
	r.Use(RecoveryMiddleware(logger))
	r.GET("/panic", func(c *gin.Context) { panic("boom") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/panic", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestCORSMiddlewareAllowsConfiguredOriginWithCredentials(t *testing.T) {
	r := gin.New()
	r.Use(CORSMiddleware([]string{"https://app.frameworks.network"}, false))
	r.POST("/graphql", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", nil)
	req.Header.Set("Origin", "https://app.frameworks.network")
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.frameworks.network" {
		t.Fatalf("expected configured origin to be reflected, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("expected credentialed CORS for configured origin, got %q", got)
	}
}

func TestCORSMiddlewareAllowsPublicProtocolPreflightWithoutCredentials(t *testing.T) {
	r := gin.New()
	r.Use(CORSMiddleware([]string{"https://app.frameworks.network"}, false))
	r.POST("/graphql", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodOptions, "/graphql", nil)
	req.Header.Set("Origin", "https://developer.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type,x-payment,x-wallet-address")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected public protocol preflight to pass, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected wildcard public CORS, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("expected no credentialed CORS for public origin, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "content-type,x-payment,x-wallet-address" {
		t.Fatalf("expected requested headers to be reflected, got %q", got)
	}
}

// A stream key is a publishing credential. Request logs are durable and widely
// shipped, so the /ingest/ path segment must never reach a log line verbatim.
func TestRedactSecretPathSegments(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/ingest/sk_live_abc123", "/ingest/<redacted>"},
		{"/ingest/sk_live_abc123/", "/ingest/<redacted>/"},
		{"/ingest/sk_live_abc123/extra", "/ingest/<redacted>/extra"},
		{"/ingest/", "/ingest/"},
		{"/webrtc/sk_live_abc123", "/webrtc/<redacted>"},
		{"/play/pb_public_id", "/play/pb_public_id"},
		{"/graphql", "/graphql"},
	}
	for _, tc := range cases {
		if got := redactSecretPathSegments(tc.in); got != tc.want {
			t.Errorf("redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// End-to-end guard on the logging middleware itself: whatever the request path
// contains, the emitted log entry must not carry the key.
func TestLoggingMiddlewareRedactsStreamKey(t *testing.T) {
	logger := logrus.New()
	var captured bytes.Buffer
	logger.SetOutput(&captured)
	logger.SetFormatter(&logrus.JSONFormatter{})

	r := gin.New()
	r.Use(LoggingMiddleware(logger))
	r.POST("/ingest/:streamKey", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/ingest/sk_live_secret", nil)
	r.ServeHTTP(w, req)

	if strings.Contains(captured.String(), "sk_live_secret") {
		t.Fatalf("stream key leaked into request log: %s", captured.String())
	}
	// The JSON formatter HTML-escapes the angle brackets, so match the word.
	if !strings.Contains(captured.String(), "redacted") {
		t.Fatalf("expected redacted path in log, got: %s", captured.String())
	}
}

// A panicking handler logs its own path, so the credential must be redacted on
// that route too — a crash is exactly when logs get shipped and read.
func TestRecoveryMiddlewareRedactsStreamKey(t *testing.T) {
	logger := logrus.New()
	var captured bytes.Buffer
	logger.SetOutput(&captured)
	logger.SetFormatter(&logrus.JSONFormatter{})

	r := gin.New()
	r.Use(RecoveryMiddleware(logger))
	r.POST("/ingest/:streamKey", func(c *gin.Context) { panic("boom") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/ingest/sk_live_secret", nil)
	r.ServeHTTP(w, req)

	if strings.Contains(captured.String(), "sk_live_secret") {
		t.Fatalf("stream key leaked into panic log: %s", captured.String())
	}
	if !strings.Contains(captured.String(), "redacted") {
		t.Fatalf("expected redacted path in panic log, got: %s", captured.String())
	}
}

// Foghorn's ingest front door is published to customer pages: a browser WHIP
// publish POSTs application/sdp, which is never a CORS-simple content type, so
// it always preflights from an origin that cannot be in ALLOWED_ORIGINS.
func TestCORSMiddlewareAllowsIngestFrontDoorPreflight(t *testing.T) {
	r := gin.New()
	r.Use(CORSMiddleware([]string{"https://app.frameworks.network"}, false))
	r.POST("/ingest/:streamKey", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodOptions, "/ingest/sk_live_1", nil)
	req.Header.Set("Origin", "https://customer.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected WHIP preflight to pass, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected wildcard public CORS, got %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected credential-bearing preflight to be non-cacheable, got %q", got)
	}
}

// The ingest prefix must not widen CORS for unrelated paths.
func TestCORSMiddlewareStillBlocksNonPublicPreflight(t *testing.T) {
	r := gin.New()
	r.Use(CORSMiddleware([]string{"https://app.frameworks.network"}, false))
	r.POST("/internal/thumbnails", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodOptions, "/internal/thumbnails", nil)
	req.Header.Set("Origin", "https://customer.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected non-public preflight to be blocked, got %d", w.Code)
	}
}

func TestCORSMiddlewareBlocksPublicProtocolCookiesFromUnconfiguredOrigin(t *testing.T) {
	r := gin.New()
	r.Use(CORSMiddleware([]string{"https://app.frameworks.network"}, false))
	r.POST("/graphql", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", nil)
	req.Header.Set("Origin", "https://developer.example")
	req.Header.Set("Cookie", "access_token=secret")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected cross-origin public API request with cookies to be blocked, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected public CORS header on blocked response, got %q", got)
	}
}

func TestCORSMiddlewareBlocksUnknownPreflightFromUnconfiguredOrigin(t *testing.T) {
	r := gin.New()
	r.Use(CORSMiddleware([]string{"https://app.frameworks.network"}, false))
	r.POST("/auth/me", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodOptions, "/auth/me", nil)
	req.Header.Set("Origin", "https://developer.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected unconfigured private preflight to be blocked, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no CORS origin for private route, got %q", got)
	}
}

func TestCORSMiddlewareBlocksWalletLoginPreflightFromUnconfiguredOrigin(t *testing.T) {
	r := gin.New()
	r.Use(CORSMiddleware([]string{"https://app.frameworks.network"}, false))
	r.POST("/auth/wallet-login", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodOptions, "/auth/wallet-login", nil)
	req.Header.Set("Origin", "https://developer.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected wallet login preflight from unconfigured origin to be blocked, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no CORS origin for wallet login public origin, got %q", got)
	}
}
