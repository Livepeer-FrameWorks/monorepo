package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"

	"frameworks/api_assets/internal/cache"
	"frameworks/api_assets/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
)

// The production router answers /ready from the real object-store probe: 200 while the readiness sentinel is
// readable through the S3 API, 503 once the store refuses it. Building the router also proves the asset routes
// and the service router do not both register /ready.
func TestChandlerRouterReadyFollowsStoreProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var storeReadable atomic.Bool
	storeReadable.Store(true)
	s3Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if storeReadable.Load() {
			_, _ = w.Write([]byte("ready"))
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>denied</Message></Error>`))
	}))
	defer s3Server.Close()

	logger := logging.NewLoggerWithService("chandler-test")
	assetHandler, err := handlers.NewAssetHandler(handlers.S3Config{
		Bucket:    "assets",
		Region:    "us-east-1",
		Endpoint:  s3Server.URL,
		AccessKey: "test-access",
		SecretKey: "test-secret",
	}, cache.NewLRU(1024, time.Minute), logger,
		prometheus.NewCounter(prometheus.CounterOpts{Name: "router_test_hits"}),
		prometheus.NewCounter(prometheus.CounterOpts{Name: "router_test_misses"}),
		prometheus.NewCounter(prometheus.CounterOpts{Name: "router_test_s3_errors"}),
	)
	if err != nil {
		t.Fatalf("NewAssetHandler: %v", err)
	}

	router := newChandlerRouter(server.RouterSpec{
		Service: "chandler",
		Logger:  logger,
		Health:  monitoring.NewHealthChecker("chandler", "test"),
		Ready:   monitoring.NewReadinessChecker("chandler", "test"),
		Metrics: monitoring.NewMetricsCollector("chandler_router_test", "test", "test"),
	}, assetHandler)

	get := func() int {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ready", nil))
		return w.Code
	}

	if code := get(); code != http.StatusOK {
		t.Fatalf("expected 200 while the store sentinel is readable, got %d", code)
	}
	storeReadable.Store(false)
	if code := get(); code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when the store probe fails, got %d", code)
	}
}
