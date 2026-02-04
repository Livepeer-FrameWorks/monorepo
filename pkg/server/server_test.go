package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"

	"github.com/gin-gonic/gin"
)

func TestSetupServiceRouter(t *testing.T) {
	logger := logging.NewLogger()
	hc := monitoring.NewHealthChecker("svc", "v1")
	mc := monitoring.NewMetricsCollector("svc", "v1", "abc")
	r := SetupServiceRouter(logger, "svc", hc, mc)
	r.GET("/ping", func(c *gin.Context) { c.String(200, "pong") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/ping", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
