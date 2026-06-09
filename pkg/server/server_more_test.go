package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

func newTestRouter(t *testing.T, name string) *gin.Engine {
	t.Helper()
	logger := logging.NewLogger()
	hc := monitoring.NewHealthChecker(name, "v1")
	mc := monitoring.NewMetricsCollectorWithRegistry(name, "v1", "abc", prometheus.NewRegistry())
	return SetupServiceRouter(logger, "svc", hc, mc)
}

func TestSetupServiceRouter_ReleaseModeSetFromEnv(t *testing.T) {
	prev := gin.Mode()
	t.Cleanup(func() { gin.SetMode(prev) })

	t.Setenv("GIN_MODE", "release")
	_ = newTestRouter(t, "svc-release")
	if gin.Mode() != gin.ReleaseMode {
		t.Fatalf("GIN_MODE=release must put gin in release mode, got %q", gin.Mode())
	}
}

func TestSetupServiceRouter_AllowedOriginsHonoredInReleaseMode(t *testing.T) {
	prev := gin.Mode()
	t.Cleanup(func() { gin.SetMode(prev) })

	t.Setenv("GIN_MODE", "release")
	t.Setenv("ALLOWED_ORIGINS", "https://app.example.com")
	r := newTestRouter(t, "svc-origins")
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	cases := []struct {
		name       string
		origin     string
		wantHeader string
	}{
		{"configured origin is echoed", "https://app.example.com", "https://app.example.com"},
		{"unconfigured origin is rejected", "https://evil.example.org", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req, _ := http.NewRequestWithContext(context.Background(), "GET", "/ping", nil)
			req.Header.Set("Origin", c.origin)
			r.ServeHTTP(w, req)
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != c.wantHeader {
				t.Fatalf("origin %q: Access-Control-Allow-Origin = %q, want %q", c.origin, got, c.wantHeader)
			}
		})
	}
}
