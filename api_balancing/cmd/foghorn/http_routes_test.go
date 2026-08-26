package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"

	"github.com/prometheus/client_golang/prometheus"
)

func TestFoghornHTTPRouteClassification(t *testing.T) {
	t.Setenv("SERVICE_TOKEN", "service-secret")
	t.Setenv("JWT_SECRET", "jwt-secret")
	logger := logging.NewLogger()
	health := monitoring.NewHealthChecker("foghorn-route-test", "test")
	metrics := monitoring.NewMetricsCollectorWithRegistry("foghorn-route-test", "test", "test", prometheus.NewRegistry())
	publicRouter, internalRouter := configureFoghornHTTPRouters(logger, health, metrics)

	publicPaths := map[string]bool{}
	for _, route := range publicRouter.Routes() {
		publicPaths[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"GET /play/*path", "GET /resolve/*path", "GET /ingest/:streamKey", "POST /ingest/:streamKey",
		"GET /ingest/", "POST /ingest/", "POST /webhooks/livepeer/auth",
	} {
		if !publicPaths[route] {
			t.Fatalf("public route %q is missing", route)
		}
	}
	for _, route := range []string{
		"GET /nodes/overview", "PUT /nodes/:node_id/mode", "GET /dashboard",
		"GET /debug/cache/stream-context", "GET /debug/stream-registry", "GET /debug/served-clusters",
	} {
		if publicPaths[route] {
			t.Fatalf("internal route %q leaked onto public router", route)
		}
	}

	for name, tc := range map[string]struct {
		router   http.Handler
		method   string
		path     string
		header   string
		wantCode int
	}{
		"public debug denied":          {publicRouter, http.MethodGet, "/debug/served-clusters", "", http.StatusUnauthorized},
		"public node mutation denied":  {publicRouter, http.MethodPut, "/nodes/edge-1/mode", "", http.StatusUnauthorized},
		"public weights always denied": {publicRouter, http.MethodGet, "/?weights=%7B%7D", "", http.StatusForbidden},
		"internal debug needs auth":    {internalRouter, http.MethodGet, "/debug/served-clusters", "", http.StatusUnauthorized},
		"service can read internal":    {internalRouter, http.MethodGet, "/debug/served-clusters", "Bearer service-secret", http.StatusOK},
		"service cannot set weights":   {internalRouter, http.MethodGet, "/?weights=%7B%7D", "Bearer service-secret", http.StatusForbidden},
		"service cannot set node mode": {internalRouter, http.MethodPut, "/nodes/edge-1/mode", "Bearer service-secret", http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp := httptest.NewRecorder()
			tc.router.ServeHTTP(resp, req)
			if resp.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", resp.Code, tc.wantCode, resp.Body.String())
			}
		})
	}
}
