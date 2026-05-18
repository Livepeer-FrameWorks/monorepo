package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"

	"github.com/gin-gonic/gin"
)

func TestSetupServiceRouter(t *testing.T) {
	logger := logging.NewLogger()
	hc := monitoring.NewHealthChecker("svc-setup", "v1")
	mc := monitoring.NewMetricsCollector("svc-setup", "v1", "abc")
	r := SetupServiceRouter(logger, "svc", hc, mc)
	r.GET("/ping", func(c *gin.Context) { c.String(200, "pong") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/ping", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestSetupServiceRouterHandlesAlternateTrailingSlash(t *testing.T) {
	logger := logging.NewLogger()
	hc := monitoring.NewHealthChecker("svc-slash", "v1")
	mc := monitoring.NewMetricsCollector("svc-slash", "v1", "abc")
	r := SetupServiceRouter(logger, "svc", hc, mc)
	r.POST("/api/action", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "POST", "/api/action/", nil)
	req.Header.Set("Origin", "https://app.frameworks.network")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected alternate trailing slash to dispatch, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.frameworks.network" {
		t.Fatalf("expected CORS header on slash mismatch, got %q", got)
	}
}

func TestSetupServiceRouterAlternateTrailingSlashDefaultsToOK(t *testing.T) {
	logger := logging.NewLogger()
	hc := monitoring.NewHealthChecker("svc-slash-default", "v1")
	mc := monitoring.NewMetricsCollector("svc-slash-default", "v1", "abc")
	r := SetupServiceRouter(logger, "svc", hc, mc)
	r.POST("/graphql", func(c *gin.Context) {
		_, _ = c.Writer.Write([]byte(`{"data":{"__typename":"Query"}}`))
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "POST", "/graphql/", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected fallback handler to default to 200, got %d", w.Code)
	}
}
