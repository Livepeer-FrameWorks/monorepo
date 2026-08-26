package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/gin-gonic/gin"
)

func TestAuthorizedMistCompatibilityAcceptsInputBalancerRequestShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("FOGHORN_PUBLIC_BASE", "https://foghorn.example")
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "capability-test-secret")

	manager := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	manager.SetNodeConnectionInfo(context.Background(), "edge-1", "edge.example", "tenant-1", "cluster-1", nil)
	previousLogger, previousLB := logger, lb
	logger = logging.NewLogger()
	lb = balancer.NewLoadBalancer(logger)
	t.Cleanup(func() { logger, lb = previousLogger, previousLB })

	base, err := url.Parse(control.FoghornBalancerBaseForNode("cluster-1", "edge-1"))
	if err != nil {
		t.Fatalf("parse capability base: %v", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + sourceByNodePathPrefix + "edge-1"
	base.RawPath = strings.TrimRight(base.EscapedPath(), "/") + sourceByNodePathPrefix + "edge-1"
	// This is the exact query shape MistInputBalancer creates after discarding
	// every query parameter from the configured balance URL.
	base.RawQuery = url.Values{"source": {"invalid!"}}.Encode()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, base.RequestURI(), nil)
	AuthorizedMistServerCompatibilityHandler(c)
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("Mist-shaped source request lost capability: %s", w.Body.String())
	}
}

func TestAuthorizedMistCompatibilityRejectsOperationsOutsideSourceLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("FOGHORN_PUBLIC_BASE", "https://foghorn.example")
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "capability-test-secret")

	manager := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	manager.SetNodeConnectionInfo(context.Background(), "edge-1", "edge.example", "tenant-1", "cluster-1", nil)

	for _, trailingPath := range []string{"/", "/other-stream"} {
		t.Run(trailingPath, func(t *testing.T) {
			base, err := url.Parse(control.FoghornBalancerBaseForNode("cluster-1", "edge-1"))
			if err != nil {
				t.Fatal(err)
			}
			base.Path = strings.TrimRight(base.Path, "/") + trailingPath
			base.RawPath = ""

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, base.RequestURI(), nil)
			AuthorizedMistServerCompatibilityHandler(c)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusForbidden, w.Body.String())
			}
		})
	}
}
