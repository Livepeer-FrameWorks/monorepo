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

func TestLastPushedCertExpiry_DeduplicatesSameCert(t *testing.T) {
	// Clear any previous state
	lastPushedCertExpiry = sync.Map{}

	nodeID := "test-node"
	expiry := int64(1700000000)

	// First store
	lastPushedCertExpiry.Store(nodeID, expiry)

	// Check it's there
	prev, ok := lastPushedCertExpiry.Load(nodeID)
	if !ok {
		t.Fatal("expected entry in lastPushedCertExpiry")
	}
	if prev.(int64) != expiry {
		t.Errorf("expected expiry %d, got %d", expiry, prev.(int64))
	}

	// Same expiry should be a no-op (cert refresh skips)
	prev2, ok2 := lastPushedCertExpiry.Load(nodeID)
	if !ok2 || prev2.(int64) != expiry {
		t.Error("expected same expiry on second load")
	}

	// Different expiry should be detected
	newExpiry := int64(1700086400)
	lastPushedCertExpiry.Store(nodeID, newExpiry)
	prev3, _ := lastPushedCertExpiry.Load(nodeID)
	if prev3.(int64) != newExpiry {
		t.Errorf("expected updated expiry %d, got %d", newExpiry, prev3.(int64))
	}
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
