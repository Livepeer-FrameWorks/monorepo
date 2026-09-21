package monitoring

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func serveReady(t *testing.T, r *ReadinessChecker) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/ready", r.Handler())
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code
}

func TestReadinessCheckerComposesChecks(t *testing.T) {
	r := NewReadinessChecker("svc", "v1")
	r.AddCheck("db", func() CheckResult { return CheckResult{Status: StatusHealthy} })
	r.AddCheck("cache", func() CheckResult { return CheckResult{Status: StatusDegraded} })
	if code := serveReady(t, r); code != http.StatusOK {
		t.Fatalf("degraded dependency should stay ready, got %d", code)
	}

	r.AddCheck("broker", func() CheckResult { return CheckResult{Status: StatusUnhealthy} })
	if code := serveReady(t, r); code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy dependency should be not ready, got %d", code)
	}
}

func TestReadinessCheckerReportsDrainAfterShutdownStarts(t *testing.T) {
	r := NewReadinessChecker("svc", "v1")
	r.AddCheck("db", func() CheckResult { return CheckResult{Status: StatusHealthy} })
	if code := serveReady(t, r); code != http.StatusOK {
		t.Fatalf("expected ready before shutdown, got %d", code)
	}
	r.MarkShuttingDown()
	status := r.Check()
	if status.Status != StatusUnhealthy || status.Checks["shutdown"].Status != StatusUnhealthy {
		t.Fatalf("expected draining status, got %+v", status)
	}
	if code := serveReady(t, r); code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 while draining, got %d", code)
	}
}
