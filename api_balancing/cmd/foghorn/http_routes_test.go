package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"frameworks/api_balancing/internal/appconfig"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"

	"github.com/prometheus/client_golang/prometheus"
)

func TestFoghornHTTPRouteClassification(t *testing.T) {
	t.Cleanup(appconfig.Install(func() *appconfig.Foghorn {
		return &appconfig.Foghorn{ServiceToken: "service-secret", JWTSecret: "jwt-secret"}
	}))
	logger := logging.NewLogger()
	health := monitoring.NewHealthChecker("foghorn-route-test", "test")
	metrics := monitoring.NewMetricsCollectorWithRegistry("foghorn-route-test", "test", "test", prometheus.NewRegistry())
	readiness := monitoring.NewReadinessChecker("foghorn-route-test", "test")
	publicSpec := server.RouterSpec{Service: "foghorn", Logger: logger, Health: health, Ready: readiness, Metrics: metrics}
	internalSpec := publicSpec
	internalSpec.DebugToken = "service-secret"
	internalSpec.DebugConfig = func() any { return appconfig.Current() }
	publicRouter, internalRouter := configureFoghornHTTPRouters(publicSpec, internalSpec)

	publicPaths := map[string]bool{}
	for _, route := range publicRouter.Routes() {
		publicPaths[route.Method+" "+route.Path] = true
	}
	internalPaths := map[string]bool{}
	for _, route := range internalRouter.Routes() {
		internalPaths[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"GET /play/*path", "GET /resolve/*path", "GET /ingest/:streamKey", "POST /ingest/:streamKey",
		"GET /ingest/", "POST /ingest/", "POST /webhooks/livepeer/auth", "GET /health", "GET /ready", "GET /metrics",
	} {
		if !publicPaths[route] {
			t.Fatalf("public route %q is missing", route)
		}
	}
	for _, route := range []string{
		"GET /nodes/overview", "PUT /nodes/:node_id/mode", "GET /dashboard",
		"GET /debug/cache/stream-context", "GET /debug/stream-registry", "GET /debug/served-clusters",
		"GET /debug/pprof/*profile", "GET /debug/config",
	} {
		if publicPaths[route] {
			t.Fatalf("internal route %q leaked onto public router", route)
		}
		if !internalPaths[route] {
			t.Fatalf("internal route %q is missing", route)
		}
	}
	for _, route := range []string{"GET /health", "GET /ready", "GET /metrics"} {
		if !internalPaths[route] {
			t.Fatalf("internal route %q is missing", route)
		}
	}

	for name, tc := range map[string]struct {
		router     http.Handler
		method     string
		path       string
		header     string
		remoteAddr string
		wantCode   int
	}{
		"public debug denied":           {publicRouter, http.MethodGet, "/debug/served-clusters", "", "", http.StatusUnauthorized},
		"public node mutation denied":   {publicRouter, http.MethodPut, "/nodes/edge-1/mode", "", "", http.StatusUnauthorized},
		"public weights always denied":  {publicRouter, http.MethodGet, "/?weights=%7B%7D", "", "", http.StatusForbidden},
		"internal debug needs auth":     {internalRouter, http.MethodGet, "/debug/served-clusters", "", "", http.StatusUnauthorized},
		"service can read internal":     {internalRouter, http.MethodGet, "/debug/served-clusters", "Bearer service-secret", "", http.StatusOK},
		"service cannot set weights":    {internalRouter, http.MethodGet, "/?weights=%7B%7D", "Bearer service-secret", "", http.StatusForbidden},
		"service cannot set node mode":  {internalRouter, http.MethodPut, "/nodes/edge-1/mode", "Bearer service-secret", "", http.StatusForbidden},
		"internal pprof from loopback":  {internalRouter, http.MethodGet, "/debug/pprof/cmdline", "Bearer service-secret", "127.0.0.1:40000", http.StatusOK},
		"internal pprof needs bearer":   {internalRouter, http.MethodGet, "/debug/pprof/cmdline", "", "127.0.0.1:40000", http.StatusUnauthorized},
		"internal pprof public address": {internalRouter, http.MethodGet, "/debug/pprof/cmdline", "Bearer service-secret", "192.0.2.10:40000", http.StatusForbidden},
		"internal ready":                {internalRouter, http.MethodGet, "/ready", "", "", http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			if tc.remoteAddr != "" {
				req.RemoteAddr = tc.remoteAddr
			}
			resp := httptest.NewRecorder()
			tc.router.ServeHTTP(resp, req)
			if resp.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", resp.Code, tc.wantCode, resp.Body.String())
			}
		})
	}
}
