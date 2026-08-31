package control

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
)

func TestBalancerCapabilityBindsNodeClusterAndExpiry(t *testing.T) {
	t.Setenv("FOGHORN_PUBLIC_BASE", "https://foghorn.example")
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "capability-test-secret")
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	sm.SetNodeConnectionInfo(context.Background(), "edge-1", "edge.example", "tenant-1", "cluster-1", nil)

	raw := FoghornBalancerBaseForNode("cluster-1", "edge-1")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse capability URL: %v", err)
	}
	if u.RawQuery != "" {
		t.Fatalf("capability leaked into query string: %q", u.RawQuery)
	}
	now := time.Now().UTC()
	if nodeID, clusterID, path, ok := VerifyBalancerCapabilityPath(u.EscapedPath(), now); !ok || nodeID != "edge-1" || clusterID != "cluster-1" || path != "/" {
		t.Fatalf("valid capability rejected: node=%q cluster=%q path=%q ok=%v", nodeID, clusterID, path, ok)
	}
	tampered := strings.Replace(u.EscapedPath(), "/edge-1/", "/edge-2/", 1)
	if _, _, _, ok := VerifyBalancerCapabilityPath(tampered, now); ok {
		t.Fatal("capability accepted after path node tampering")
	}
	if _, _, _, ok := VerifyBalancerCapabilityPath(u.EscapedPath(), now.Add(balancerCapabilityTTL+time.Minute)); ok {
		t.Fatal("expired capability accepted")
	}

	// Verification is self-contained: a valid, unexpired URL remains usable
	// after Foghorn restarts with an empty in-memory node table.
	state.ResetDefaultManagerForTests()
	if _, clusterID, _, ok := VerifyBalancerCapabilityPath(u.EscapedPath(), now); !ok || clusterID != "cluster-1" {
		t.Fatal("capability depended on volatile node state")
	}

	sm = state.ResetDefaultManagerForTests()
	sm.SetNodeConnectionInfo(context.Background(), "edge-1", "edge.example", "tenant-1", "cluster-2", nil)
	if _, clusterID, _, ok := VerifyBalancerCapabilityPath(u.EscapedPath(), now); !ok || clusterID != "cluster-1" {
		t.Fatal("signed capability was not stable across volatile-state changes")
	}
}

func TestFoghornBalancerBaseForNodeFailsClosedWithoutIdentityOrSecret(t *testing.T) {
	t.Setenv("FOGHORN_PUBLIC_BASE", "https://foghorn.example")
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "")
	t.Setenv("SERVICE_TOKEN", "must-not-authorize-balancer-capabilities")
	if got := FoghornBalancerBaseForNode("cluster-1", "edge-1"); got != "" {
		t.Fatalf("base without signing authority = %q, want empty", got)
	}
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "secret")
	if got := FoghornBalancerBaseForNode("", "edge-1"); got != "" {
		t.Fatalf("base without cluster identity = %q, want empty", got)
	}
}
