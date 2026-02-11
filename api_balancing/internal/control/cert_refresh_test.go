package control

import (
	"sync"
	"testing"
	"time"

	pb "frameworks/pkg/proto"

	"frameworks/pkg/logging"
)

// mockStream satisfies pb.HelmsmanControl_ConnectServer for registry population.
type mockStream struct {
	pb.HelmsmanControl_ConnectServer
}

func TestRefreshTLSBundles_UsesCanonicalID(t *testing.T) {
	// Save and restore global registry
	oldRegistry := registry
	defer func() { registry = oldRegistry }()

	registry = &Registry{
		conns: make(map[string]*conn),
		log:   logging.NewLogger(),
	}

	// Add a connection where canonicalID differs from connID
	registry.conns["conn-abc"] = &conn{
		stream:      &mockStream{},
		last:        time.Now(),
		peerAddr:    "10.0.0.1:5000",
		canonicalID: "canonical-xyz",
	}

	// Add a connection where canonicalID is empty (should fall back to connID)
	registry.conns["conn-def"] = &conn{
		stream:   &mockStream{},
		last:     time.Now(),
		peerAddr: "10.0.0.2:5000",
	}

	// Collect the node IDs that refreshTLSBundles would iterate over
	registry.mu.RLock()
	type nodeInfo struct {
		connID, canonicalID string
	}
	var nodes []nodeInfo
	for id, c := range registry.conns {
		cid := c.canonicalID
		if cid == "" {
			cid = id
		}
		nodes = append(nodes, nodeInfo{id, cid})
	}
	registry.mu.RUnlock()

	// Verify canonical ID resolution
	found := make(map[string]string) // connID -> canonicalID
	for _, n := range nodes {
		found[n.connID] = n.canonicalID
	}

	if found["conn-abc"] != "canonical-xyz" {
		t.Errorf("expected canonical-xyz for conn-abc, got %q", found["conn-abc"])
	}
	if found["conn-def"] != "conn-def" {
		t.Errorf("expected conn-def (fallback) for conn-def, got %q", found["conn-def"])
	}
}

func TestLastPushedTLSState_DeduplicatesStateChanges(t *testing.T) {
	lastPushedTLSState = sync.Map{}

	connID := "test-node"
	first := &pb.TLSCertBundle{
		CertPem:   "cert-a",
		KeyPem:    "key-a",
		Domain:    "*.cluster.frameworks.network",
		ExpiresAt: 1700000000,
	}
	state1 := tlsBundleState(first)
	lastPushedTLSState.Store(connID, state1)

	prev, ok := lastPushedTLSState.Load(connID)
	if !ok {
		t.Fatal("expected entry in lastPushedTLSState")
	}
	if prev.(string) != state1 {
		t.Fatalf("expected state %q, got %q", state1, prev)
	}

	// Same expiry but different certificate material must produce a new state.
	rotated := &pb.TLSCertBundle{
		CertPem:   "cert-b",
		KeyPem:    "key-a",
		Domain:    "*.cluster.frameworks.network",
		ExpiresAt: 1700000000,
	}
	state2 := tlsBundleState(rotated)
	if state2 == state1 {
		t.Fatal("expected changed TLS state when cert material changes")
	}

	lastPushedTLSState.Store(connID, state2)
	updated, _ := lastPushedTLSState.Load(connID)
	if updated.(string) != state2 {
		t.Fatalf("expected updated state %q, got %q", state2, updated)
	}

	lastPushedTLSState.Store(connID, tlsStateNoCert)
	cleared, _ := lastPushedTLSState.Load(connID)
	if cleared.(string) != tlsStateNoCert {
		t.Fatalf("expected no-cert state, got %q", cleared)
	}
}

func TestTLSBundleState(t *testing.T) {
	t.Run("nil bundle", func(t *testing.T) {
		if got := tlsBundleState(nil); got != tlsStateNoCert {
			t.Fatalf("expected %q, got %q", tlsStateNoCert, got)
		}
	})

	t.Run("stable hash and sensitive to all fields", func(t *testing.T) {
		base := &pb.TLSCertBundle{CertPem: "cert", KeyPem: "key", Domain: "*.a", ExpiresAt: 10}
		again := &pb.TLSCertBundle{CertPem: "cert", KeyPem: "key", Domain: "*.a", ExpiresAt: 10}
		if tlsBundleState(base) != tlsBundleState(again) {
			t.Fatal("expected identical bundles to hash identically")
		}

		cases := []*pb.TLSCertBundle{
			{CertPem: "cert2", KeyPem: "key", Domain: "*.a", ExpiresAt: 10},
			{CertPem: "cert", KeyPem: "key2", Domain: "*.a", ExpiresAt: 10},
			{CertPem: "cert", KeyPem: "key", Domain: "*.b", ExpiresAt: 10},
			{CertPem: "cert", KeyPem: "key", Domain: "*.a", ExpiresAt: 11},
		}
		baseHash := tlsBundleState(base)
		for i, tc := range cases {
			if tlsBundleState(tc) == baseHash {
				t.Fatalf("case %d unexpectedly hashed equal to base", i)
			}
		}
	})
}

func TestConnCanonicalID_StoredAfterResolution(t *testing.T) {
	// Save and restore global registry
	oldRegistry := registry
	defer func() { registry = oldRegistry }()

	registry = &Registry{
		conns: make(map[string]*conn),
		log:   logging.NewLogger(),
	}

	// Simulate initial registration (canonicalID empty)
	nodeID := "helmsman-123"
	registry.conns[nodeID] = &conn{
		stream:   &mockStream{},
		last:     time.Now(),
		peerAddr: "10.0.0.1:5000",
	}

	// Simulate canonical ID resolution (as done in the Register handler)
	canonicalNodeID := "qm-resolved-456"
	registry.mu.Lock()
	if c, ok := registry.conns[nodeID]; ok {
		c.canonicalID = canonicalNodeID
	}
	registry.mu.Unlock()

	// Verify it's stored
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()

	if c.canonicalID != canonicalNodeID {
		t.Errorf("expected canonicalID %q, got %q", canonicalNodeID, c.canonicalID)
	}
}
