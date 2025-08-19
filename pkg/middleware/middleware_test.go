package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/pkg/logging"

	"github.com/gin-gonic/gin"
)

func TestRequestIDMiddleware(t *testing.T) {
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/ping", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	if w.Header().Get("X-Request-ID") == "" {
		t.Fatalf("expected X-Request-ID header to be set")
	}
}

func TestTimeoutMiddleware(t *testing.T) {
	r := gin.New()
	r.Use(TimeoutMiddleware(10 * time.Millisecond))
	r.GET("/slow", func(c *gin.Context) { time.Sleep(20 * time.Millisecond); c.String(http.StatusOK, "done") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/slow", nil)
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
	req, _ := http.NewRequest("GET", "/", nil)
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
	req, _ := http.NewRequest("GET", "/panic", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}
