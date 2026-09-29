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

// newTestChandlerRouter builds the production router over a real S3 client aimed at s3URL.
func newTestChandlerRouter(t *testing.T, s3URL, metricsNamespace string) (*gin.Engine, *handlers.AssetHandler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := logging.NewLoggerWithService("chandler-test")
	assetHandler, err := handlers.NewAssetHandler(handlers.S3Config{
		Bucket:    "assets",
		Region:    "us-east-1",
		Endpoint:  s3URL,
		AccessKey: "test-access",
		SecretKey: "test-secret",
	}, cache.NewLRU(1024, time.Minute), logger,
		prometheus.NewCounter(prometheus.CounterOpts{Name: metricsNamespace + "_hits"}),
		prometheus.NewCounter(prometheus.CounterOpts{Name: metricsNamespace + "_misses"}),
		prometheus.NewCounter(prometheus.CounterOpts{Name: metricsNamespace + "_s3_errors"}),
	)
	if err != nil {
		t.Fatalf("NewAssetHandler: %v", err)
	}
	router := newChandlerRouter(server.RouterSpec{
		Service: "chandler",
		Logger:  logger,
		Health:  monitoring.NewHealthChecker("chandler", "test"),
		Ready:   monitoring.NewReadinessChecker("chandler", "test"),
		Metrics: monitoring.NewMetricsCollector(metricsNamespace, "test", "test"),
	}, assetHandler)
	return router, assetHandler
}

func getReady(router *gin.Engine) (int, time.Duration) {
	start := time.Now()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ready", nil))
	return w.Code, time.Since(start)
}

// waitReady polls /ready until it answers 200 or the deadline passes.
func waitReady(t *testing.T, router *gin.Engine) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		code, _ := getReady(router)
		if code == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("/ready never answered 200, last code %d", code)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The production router answers /ready from the real object-store probe: 503 until the readiness sentinel has been
// read through the S3 API, 200 once it has. Building the router also proves the asset routes and the service
// router do not both register /ready.
func TestChandlerRouterReadyFollowsStoreProbe(t *testing.T) {
	var storeReadable atomic.Bool
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

	router, assetHandler := newTestChandlerRouter(t, s3Server.URL, "chandler_router_test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go assetHandler.RunStoreProbe(ctx)

	time.Sleep(200 * time.Millisecond)
	if code, _ := getReady(router); code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 while the store denies the sentinel, got %d", code)
	}
	storeReadable.Store(true)
	cancel()
	probeCtx, probeCancel := context.WithCancel(context.Background())
	defer probeCancel()
	go assetHandler.RunStoreProbe(probeCtx)
	waitReady(t, router)
}

// A store read that stalls must not stall /ready or flip it: Quartermaster probes /ready with a 2 s budget, so a
// readiness answer that waits on a stalled object-store round trip times out and marks every instance sharing that
// store unhealthy at once. /ready answers from the last recorded probe instead, and a single stalled read inside the
// readiness window keeps the instance ready.
func TestChandlerRouterReadyDoesNotWaitOnStalledStore(t *testing.T) {
	var stalled atomic.Bool
	release := make(chan struct{})
	s3Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stalled.Load() {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		_, _ = w.Write([]byte("ready"))
	}))
	defer s3Server.Close()
	defer close(release)

	router, assetHandler := newTestChandlerRouter(t, s3Server.URL, "chandler_router_stall_test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go assetHandler.RunStoreProbe(ctx)

	waitReady(t, router)
	stalled.Store(true)

	code, elapsed := getReady(router)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("/ready took %s while the store stalled; it must answer without waiting on the store", elapsed)
	}
	if code != http.StatusOK {
		t.Fatalf("expected 200 while a single store read stalls inside the readiness window, got %d", code)
	}
}
