package control

import (
	"sync"
	"testing"
	"time"

	pb "frameworks/pkg/proto"

	"frameworks/pkg/logging"

	navclient "frameworks/pkg/clients/navigator"
	qmclient "frameworks/pkg/clients/quartermaster"
)

// mockStream satisfies pb.HelmsmanControl_ConnectServer for registry population.
type mockStream struct {
	pb.HelmsmanControl_ConnectServer
	sent []*pb.ControlMessage
}

func (m *mockStream) Send(msg *pb.ControlMessage) error {
	m.sent = append(m.sent, msg)
	return nil
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

func TestFetchClusterTLSBundle_NilClients(t *testing.T) {
	oldQM := quartermasterClient
	oldNav := navigatorClient
	defer func() {
		quartermasterClient = oldQM
		navigatorClient = oldNav
	}()

	t.Run("both nil", func(t *testing.T) {
		quartermasterClient = nil
		navigatorClient = nil
		bundle, found, err := fetchClusterTLSBundle("any-node")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if found {
			t.Fatal("expected found=false")
		}
		if bundle != nil {
			t.Fatal("expected nil bundle")
		}
	})

	t.Run("quartermaster nil only", func(t *testing.T) {
		quartermasterClient = nil
		navigatorClient = &navclient.Client{}
		bundle, found, err := fetchClusterTLSBundle("any-node")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if found || bundle != nil {
			t.Fatal("expected nil/false when quartermaster is nil")
		}
	})

	t.Run("navigator nil only", func(t *testing.T) {
		quartermasterClient = &qmclient.GRPCClient{}
		navigatorClient = nil
		bundle, found, err := fetchClusterTLSBundle("any-node")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if found || bundle != nil {
			t.Fatal("expected nil/false when navigator is nil")
		}
	})
}

func TestResolveClusterTLSBundle_NilClients(t *testing.T) {
	oldQM := quartermasterClient
	oldNav := navigatorClient
	defer func() {
		quartermasterClient = oldQM
		navigatorClient = oldNav
	}()

	quartermasterClient = nil
	navigatorClient = nil

	bundle := resolveClusterTLSBundle("any-node")
	if bundle != nil {
		t.Fatal("expected nil bundle when clients are nil")
	}
}

func TestRefreshTLSBundles_EmptyRegistry(t *testing.T) {
	oldRegistry := registry
	oldQM := quartermasterClient
	oldNav := navigatorClient
	defer func() {
		registry = oldRegistry
		quartermasterClient = oldQM
		navigatorClient = oldNav
	}()

	quartermasterClient = nil
	navigatorClient = nil
	lastPushedTLSState = sync.Map{}

	registry = &Registry{
		conns: make(map[string]*conn),
		log:   logging.NewLogger(),
	}

	// Should return immediately with no panics
	refreshTLSBundles(logging.NewLogger())
}

func TestRefreshTLSBundles_SkipsWhenStateUnchanged(t *testing.T) {
	oldRegistry := registry
	oldQM := quartermasterClient
	oldNav := navigatorClient
	defer func() {
		registry = oldRegistry
		quartermasterClient = oldQM
		navigatorClient = oldNav
	}()

	quartermasterClient = nil
	navigatorClient = nil
	lastPushedTLSState = sync.Map{}

	ms := &mockStream{}
	registry = &Registry{
		conns: make(map[string]*conn),
		log:   logging.NewLogger(),
	}
	registry.conns["edge-1"] = &conn{
		stream:      ms,
		last:        time.Now(),
		peerAddr:    "10.0.0.1:5000",
		canonicalID: "edge-1",
	}

	// Pre-populate with tlsStateNoCert — same state fetchClusterTLSBundle
	// will produce with nil clients, so refresh should skip.
	lastPushedTLSState.Store("edge-1", tlsStateNoCert)

	refreshTLSBundles(logging.NewLogger())

	if len(ms.sent) != 0 {
		t.Fatalf("expected no Send calls when state unchanged, got %d", len(ms.sent))
	}

	// State should remain unchanged
	val, _ := lastPushedTLSState.Load("edge-1")
	if val.(string) != tlsStateNoCert {
		t.Fatalf("expected state to remain %q, got %q", tlsStateNoCert, val)
	}
}

func TestRefreshTLSBundles_PushesOnStateChange(t *testing.T) {
	oldRegistry := registry
	oldQM := quartermasterClient
	oldNav := navigatorClient
	defer func() {
		registry = oldRegistry
		quartermasterClient = oldQM
		navigatorClient = oldNav
	}()

	// Nil clients → fetchClusterTLSBundle returns (nil, false, nil)
	quartermasterClient = nil
	navigatorClient = nil
	lastPushedTLSState = sync.Map{}

	ms := &mockStream{}
	registry = &Registry{
		conns: make(map[string]*conn),
		log:   logging.NewLogger(),
	}
	registry.conns["edge-1"] = &conn{
		stream:      ms,
		last:        time.Now(),
		peerAddr:    "10.0.0.1:5000",
		canonicalID: "edge-1",
	}

	// Simulate a previously-pushed cert state (some hash).
	// With nil clients, next state will be tlsStateNoCert → state change detected.
	lastPushedTLSState.Store("edge-1", "abcdef1234567890")

	refreshTLSBundles(logging.NewLogger())

	// Should have pushed a ConfigSeed with Tls == nil
	if len(ms.sent) != 1 {
		t.Fatalf("expected 1 Send call on state change, got %d", len(ms.sent))
	}
	seed := ms.sent[0].GetConfigSeed()
	if seed == nil {
		t.Fatal("expected ConfigSeed payload")
	}
	if seed.GetTls() != nil {
		t.Fatal("expected Tls to be nil in pushed seed (cert removed)")
	}

	// State should now be tlsStateNoCert
	val, ok := lastPushedTLSState.Load("edge-1")
	if !ok {
		t.Fatal("expected state entry after push")
	}
	if val.(string) != tlsStateNoCert {
		t.Fatalf("expected state %q, got %q", tlsStateNoCert, val)
	}
}
