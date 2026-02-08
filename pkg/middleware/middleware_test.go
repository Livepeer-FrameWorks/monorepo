package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/pkg/logging"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
