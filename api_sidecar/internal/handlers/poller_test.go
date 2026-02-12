package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"frameworks/pkg/logging"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newPollerContext(method, path, body string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	return ctx, rec
}

func TestEvaluateNodeHealth(t *testing.T) {
	tests := []struct {
		name        string
		hasMistData bool
		cpu         float64
		mem         float64
		shm         float64
		expect      bool
	}{
		{
			name:        "no mist data",
			hasMistData: false,
			cpu:         10,
			mem:         10,
			shm:         10,
			expect:      false,
		},
		{
			name:        "cpu degraded",
			hasMistData: true,
			cpu:         91,
			mem:         10,
			shm:         10,
			expect:      false,
		},
		{
			name:        "memory degraded",
			hasMistData: true,
			cpu:         10,
			mem:         95,
			shm:         10,
			expect:      false,
		},
		{
			name:        "shm degraded",
			hasMistData: true,
			cpu:         10,
			mem:         10,
			shm:         92,
			expect:      false,
		},
		{
			name:        "healthy",
			hasMistData: true,
			cpu:         90,
			mem:         90,
			shm:         90,
			expect:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateNodeHealth(tt.hasMistData, tt.cpu, tt.mem, tt.shm)
			if got != tt.expect {
				t.Fatalf("expected %v, got %v", tt.expect, got)
			}
		})
	}
}

func TestAddPrometheusNode_BindsInlineRequestAndFallsBackEdgeURL(t *testing.T) {
	oldMonitor := prometheusMonitor
	oldLogger := monitorLogger
	t.Cleanup(func() {
		prometheusMonitor = oldMonitor
		monitorLogger = oldLogger
	})

	prometheusMonitor = &PrometheusMonitor{}
	monitorLogger = logging.NewLogger()

	ctx, rec := newPollerContext(http.MethodPost, "/prometheus/nodes", `{
		"node_id":"node-1",
		"base_url":"http://mist.internal:4242"
	}`)
	AddPrometheusNode(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if prometheusMonitor.nodeID != "node-1" {
		t.Fatalf("node_id: got %q", prometheusMonitor.nodeID)
	}
	if prometheusMonitor.baseURL != "http://mist.internal:4242" {
		t.Fatalf("base_url: got %q", prometheusMonitor.baseURL)
	}
	if prometheusMonitor.edgePublicURL != "http://mist.internal:4242" {
		t.Fatalf("expected edge_public_url fallback to base_url, got %q", prometheusMonitor.edgePublicURL)
	}
}

func TestAddPrometheusNode_UsesProvidedEdgePublicURL(t *testing.T) {
	oldMonitor := prometheusMonitor
	oldLogger := monitorLogger
	t.Cleanup(func() {
		prometheusMonitor = oldMonitor
		monitorLogger = oldLogger
	})

	prometheusMonitor = &PrometheusMonitor{}
	monitorLogger = logging.NewLogger()

	ctx, rec := newPollerContext(http.MethodPost, "/prometheus/nodes", `{
		"node_id":"node-2",
		"base_url":"http://mist.internal:4242",
		"edge_public_url":"https://edge.example.com"
	}`)
	AddPrometheusNode(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if prometheusMonitor.edgePublicURL != "https://edge.example.com" {
		t.Fatalf("expected explicit edge_public_url, got %q", prometheusMonitor.edgePublicURL)
	}
}

func TestAddPrometheusNode_InvalidJSONReturnsBadRequest(t *testing.T) {
	oldMonitor := prometheusMonitor
	oldLogger := monitorLogger
	t.Cleanup(func() {
		prometheusMonitor = oldMonitor
		monitorLogger = oldLogger
	})

	prometheusMonitor = &PrometheusMonitor{}
	monitorLogger = logging.NewLogger()

	ctx, rec := newPollerContext(http.MethodPost, "/prometheus/nodes", `{"node_id":`)
	AddPrometheusNode(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if _, ok := resp["error"]; !ok {
		t.Fatalf("expected error field, got %v", resp)
	}
}
