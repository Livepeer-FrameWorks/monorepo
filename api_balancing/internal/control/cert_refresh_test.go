package control

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"sync"
	"testing"
	"time"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	navclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/navigator"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
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

func testTLSBundle(t *testing.T, bundleID string, dnsNames ...string) *pb.TLSCertBundle {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     dnsNames,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return &pb.TLSCertBundle{
		BundleId:  bundleID,
		Domain:    dnsNames[0],
		CertPem:   string(certPEM),
		KeyPem:    string(keyPEM),
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
}

func TestServerCertHolderSelectsServedClusterBySNI(t *testing.T) {
	var holder serverCertHolder
	internal := testTLSBundle(t, "file:/etc/frameworks/pki/services/foghorn/tls.crt", "foghorn.internal")
	eu := testTLSBundle(t, "cluster:media-eu-1", "*.media-eu-1.frameworks.network")
	us := testTLSBundle(t, "cluster:media-us-1", "*.media-us-1.frameworks.network")

	if err := holder.StoreBundles([]*pb.TLSCertBundle{internal, eu, us}); err != nil {
		t.Fatalf("StoreBundles: %v", err)
	}

	cert, err := holder.GetCertificate(&tls.ClientHelloInfo{ServerName: "foghorn.media-us-1.frameworks.network"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if cert.Leaf == nil || len(cert.Leaf.DNSNames) == 0 || cert.Leaf.DNSNames[0] != "*.media-us-1.frameworks.network" {
		t.Fatalf("selected SANs = %v, want US wildcard", cert.Leaf.DNSNames)
	}

	cert, err = holder.GetCertificate(&tls.ClientHelloInfo{ServerName: "foghorn.internal"})
	if err != nil {
		t.Fatalf("GetCertificate internal: %v", err)
	}
	if cert.Leaf == nil || len(cert.Leaf.DNSNames) == 0 || cert.Leaf.DNSNames[0] != "foghorn.internal" {
		t.Fatalf("selected SANs = %v, want internal leaf", cert.Leaf.DNSNames)
	}
}

func TestServerCertHolderRejectsEmptyBundles(t *testing.T) {
	var holder serverCertHolder
	if err := holder.StoreBundles(nil); err == nil {
		t.Fatal("expected empty bundle set to fail")
	}
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

func TestStripWildcardSiteWithoutTLS(t *testing.T) {
	seed := &pb.ConfigSeed{
		Site: &pb.SiteConfig{SiteAddress: "*.cluster.frameworks.network"},
	}
	stripWildcardSiteWithoutTLS(seed)
	if seed.GetSite() != nil {
		t.Fatal("wildcard site without TLS should be stripped")
	}

	withTLS := &pb.ConfigSeed{
		Tls:  &pb.TLSCertBundle{CertPem: "cert", KeyPem: "key"},
		Site: &pb.SiteConfig{SiteAddress: "*.cluster.frameworks.network"},
	}
	stripWildcardSiteWithoutTLS(withTLS)
	if withTLS.GetSite() == nil {
		t.Fatal("wildcard site with TLS should be retained")
	}

	apex := &pb.ConfigSeed{Site: &pb.SiteConfig{SiteAddress: "edge.frameworks.network"}}
	stripWildcardSiteWithoutTLS(apex)
	if apex.GetSite() == nil {
		t.Fatal("non-wildcard site without TLS should be retained for Caddy-managed ACME")
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

func TestClusterTLSBundleLookupMatchesNavigatorClusterSlug(t *testing.T) {
	oldGetCluster := getClusterFn
	defer func() { getClusterFn = oldGetCluster }()

	getClusterFn = func(context.Context, string) (*pb.InfrastructureCluster, error) {
		return &pb.InfrastructureCluster{ClusterName: "Media EU 1"}, nil
	}

	bundleID, wildcard, ok := clusterTLSBundleLookup("!!!", "frameworks.network")
	if !ok {
		t.Fatal("expected lookup to resolve from cluster name fallback")
	}
	if bundleID != "cluster:media-eu-1" {
		t.Fatalf("bundle ID = %q, want cluster:media-eu-1", bundleID)
	}
	if wildcard != "*.media-eu-1.frameworks.network" {
		t.Fatalf("wildcard = %q, want *.media-eu-1.frameworks.network", wildcard)
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
