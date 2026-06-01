package handlers

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/gin-gonic/gin"
)

func TestActiveReplicationSource(t *testing.T) {
	// Install a registry against the local cluster and seed an in-flight
	// replication. activeReplicationSource is now backed by the registry's
	// LocalReplication accessor instead of the federation cache.
	prior := control.StreamRegistryInstance
	r := control.NewStreamRegistry(nil, "media-us-1", time.Minute)
	control.SetStreamRegistry(r)
	t.Cleanup(func() { control.SetStreamRegistry(prior) })

	const streamName = "frameworks-demo"
	const sourceURL = "dtsc://edge-eu-1.media-eu-1.frameworks.network:4200/frameworks-demo"
	r.MarkReplicating(streamName, "media-eu-1", sourceURL, "edge-us-1", "edge-us-1.media-us-1.frameworks.network", "edge-eu-1")

	// Caller matches the pinned dest — returns the URL.
	got, handled := activeReplicationSource(context.Background(), streamName, "edge-us-1")
	if !handled {
		t.Fatal("expected active replication source")
	}
	if got != sourceURL {
		t.Fatalf("source = %q, want %q", got, sourceURL)
	}

	// Caller is a different local edge — handled but empty so caller refuses
	// instead of starting a duplicate pull.
	if got, handled := activeReplicationSource(context.Background(), streamName, "edge-us-2"); !handled || got != "" {
		t.Fatalf("pinned-elsewhere should handle with empty URL; got=%q handled=%v", got, handled)
	}

	// Unknown caller (NAT, brand-new node) cannot prove it is the pin
	// owner; treated as pinned-elsewhere.
	if got, handled := activeReplicationSource(context.Background(), streamName, ""); !handled || got != "" {
		t.Fatalf("unknown caller should handle with empty URL; got=%q handled=%v", got, handled)
	}

	if got, handled := activeReplicationSource(context.Background(), "other-stream", "edge-us-1"); handled || got != "" {
		t.Fatalf("unexpected source for other stream: %q handled=%v", got, handled)
	}
}

func TestSourceCallerNodeIDPrefersBalancerPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/source/by-node/edge-us-1?source=frameworks-demo", nil)
	c.Request = req

	got := sourceCallerNodeID(c, req.URL.Query(), "203.0.113.10")
	if got != "edge-us-1" {
		t.Fatalf("sourceCallerNodeID = %q, want edge-us-1", got)
	}
}

func TestSourceCallerNodeIDTrimsTrailingPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/source/by-node/edge-us-1/extra?source=frameworks-demo", nil)
	c.Request = req

	got := sourceCallerNodeID(c, req.URL.Query(), "203.0.113.10")
	if got != "edge-us-1" {
		t.Fatalf("sourceCallerNodeID = %q, want edge-us-1", got)
	}
}
