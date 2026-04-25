package cmd

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	pb "frameworks/pkg/proto"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func mustGenWgKeys(t *testing.T) (privB64, pubB64 string) {
	t.Helper()
	k, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	return k.String(), k.PublicKey().String()
}

// TestDoctor_ValidPair builds a manifest with two hosts that match QM,
// confirms both pass identity + peer-set validation.
func TestDoctor_ValidPair(t *testing.T) {
	_, host1Pub := mustGenWgKeys(t)
	_, host2Pub := mustGenWgKeys(t)

	manifest := &inventory.Manifest{
		Type:    "cluster",
		Profile: "test",
		Hosts: map[string]inventory.Host{
			"core-1": {
				Name: "core-1", WireguardIP: "10.88.0.2", WireguardPublicKey: host1Pub,
				WireguardPort: 51820, WireguardPrivateKey: "ignored",
			},
			"core-2": {
				Name: "core-2", WireguardIP: "10.88.0.3", WireguardPublicKey: host2Pub,
				WireguardPort: 51820, WireguardPrivateKey: "ignored",
			},
		},
	}
	qm := []*pb.InfrastructureNode{
		{NodeId: "n1", NodeName: "core-1", ClusterId: testCluster, ExternalIp: strPtr("1.2.3.4"), WireguardIp: strPtr("10.88.0.2"), WireguardPublicKey: strPtr(host1Pub), WireguardPort: i32Ptr(51820), Status: "active"},
		{NodeId: "n2", NodeName: "core-2", ClusterId: testCluster, ExternalIp: strPtr("1.2.3.5"), WireguardIp: strPtr("10.88.0.3"), WireguardPublicKey: strPtr(host2Pub), WireguardPort: i32Ptr(51820), Status: "active"},
	}

	results := diagnoseManifest(manifest, []string{"core-1", "core-2"}, qm)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if !r.identityOK {
			t.Errorf("%s identity failed: %s", r.host, r.identityIssue)
		}
		if !r.peersOK {
			t.Errorf("%s peers failed: %s", r.host, r.peerIssue)
		}
		if r.peerCount != 1 {
			t.Errorf("%s peer count = %d, want 1", r.host, r.peerCount)
		}
	}
}

// TestDoctor_PeerWithBadAllowedIP confirms wgpolicy violations on the
// hypothetical peer set surface as peerIssue.
func TestDoctor_PeerSelfPeerDetectedFromManifestPubKey(t *testing.T) {
	_, sharedPub := mustGenWgKeys(t)
	manifest := &inventory.Manifest{
		Type:    "cluster",
		Profile: "test",
		Hosts: map[string]inventory.Host{
			"core-1": {
				Name: "core-1", WireguardIP: "10.88.0.2", WireguardPublicKey: sharedPub,
				WireguardPort: 51820, WireguardPrivateKey: "ignored",
			},
		},
	}
	// QM has a *different* node that somehow shares the same public key —
	// from the host's perspective this looks like a self-peer.
	qm := []*pb.InfrastructureNode{
		{NodeId: "self", NodeName: "core-1", ClusterId: testCluster, ExternalIp: strPtr("1.2.3.4"), WireguardIp: strPtr("10.88.0.2"), WireguardPublicKey: strPtr(sharedPub), WireguardPort: i32Ptr(51820), Status: "active"},
		{NodeId: "ghost", NodeName: "ghost", ClusterId: testCluster, ExternalIp: strPtr("1.2.3.5"), WireguardIp: strPtr("10.88.0.3"), WireguardPublicKey: strPtr(sharedPub), WireguardPort: i32Ptr(51820), Status: "active"},
	}

	results := diagnoseManifest(manifest, []string{"core-1"}, qm)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.peersOK {
		t.Fatalf("expected peers FAIL on shared pub key, got ok")
	}
	if !strings.Contains(r.peerIssue, "matches self") {
		t.Errorf("expected self-peer rejection in peerIssue, got: %s", r.peerIssue)
	}
}

func TestDoctor_BadSelfPort(t *testing.T) {
	_, hostPub := mustGenWgKeys(t)
	manifest := &inventory.Manifest{
		Type:    "cluster",
		Profile: "test",
		Hosts: map[string]inventory.Host{
			"core-1": {
				Name: "core-1", WireguardIP: "10.88.0.2", WireguardPublicKey: hostPub,
				WireguardPort:       0, // invalid
				WireguardPrivateKey: "ignored",
			},
		},
	}

	results := diagnoseManifest(manifest, []string{"core-1"}, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].identityOK {
		t.Fatalf("port 0 should fail identity validation")
	}
	if !strings.Contains(results[0].identityIssue, "listen port") {
		t.Errorf("expected port issue, got: %s", results[0].identityIssue)
	}
}

func TestDoctor_BadManifestPublicKey(t *testing.T) {
	manifest := &inventory.Manifest{
		Type:    "cluster",
		Profile: "test",
		Hosts: map[string]inventory.Host{
			"core-1": {
				Name: "core-1", WireguardIP: "10.88.0.2",
				WireguardPublicKey:  "garbage", // unparseable
				WireguardPort:       51820,
				WireguardPrivateKey: "ignored",
			},
		},
	}
	// Provide a QM row that matches everything except the (unparseable) key
	// so doctor reaches the manifest pub-key parse step.
	qm := []*pb.InfrastructureNode{{
		NodeId: "n1", NodeName: "core-1", ClusterId: testCluster,
		ExternalIp: strPtr("1.2.3.4"), WireguardIp: strPtr("10.88.0.2"),
		WireguardPublicKey: strPtr("garbage"), WireguardPort: i32Ptr(51820),
		Status: "active",
	}}

	results := diagnoseManifest(manifest, []string{"core-1"}, qm)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !strings.Contains(results[0].peerIssue, "manifest wireguard_public_key parse") {
		t.Errorf("expected pub-key parse error, got: %s", results[0].peerIssue)
	}
}

// TestDoctor_NoQMSelfRow verifies that a manifest host without a matching
// Quartermaster row fails — real SyncMesh returns NotFound for that case,
// and doctor must surface it instead of running peer-set validation
// against a hypothetical that would never run in production.
func TestDoctor_NoQMSelfRow(t *testing.T) {
	_, hostPub := mustGenWgKeys(t)
	manifest := &inventory.Manifest{
		Type:    "cluster",
		Profile: "test",
		Hosts: map[string]inventory.Host{
			"core-1": {
				Name: "core-1", WireguardIP: "10.88.0.2", WireguardPublicKey: hostPub,
				WireguardPort: 51820, WireguardPrivateKey: "ignored",
			},
		},
	}
	results := diagnoseManifest(manifest, []string{"core-1"}, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].identityOK {
		t.Fatalf("expected identity FAIL when no QM self row exists, got ok")
	}
	if !strings.Contains(results[0].identityIssue, "no Quartermaster row") {
		t.Errorf("expected NotFound-equivalent message, got: %s", results[0].identityIssue)
	}
	if results[0].peersOK {
		t.Errorf("peer validation should not run when self row is missing")
	}
}

// TestDoctor_ManifestVsQMSelfDivergence verifies SyncMesh-equivalent
// FailedPrecondition: when manifest's stored WG identity diverges from
// what Quartermaster has stored, doctor must surface it.
func TestDoctor_ManifestVsQMSelfDivergence(t *testing.T) {
	_, manifestPub := mustGenWgKeys(t)
	_, qmPub := mustGenWgKeys(t) // different
	manifest := &inventory.Manifest{
		Type:    "cluster",
		Profile: "test",
		Hosts: map[string]inventory.Host{
			"core-1": {
				Name: "core-1", WireguardIP: "10.88.0.2", WireguardPublicKey: manifestPub,
				WireguardPort: 51820, WireguardPrivateKey: "ignored",
			},
		},
	}
	qm := []*pb.InfrastructureNode{{
		NodeId: "n1", NodeName: "core-1", ClusterId: testCluster,
		ExternalIp:         strPtr("1.2.3.4"),
		WireguardIp:        strPtr("10.88.0.2"),
		WireguardPublicKey: strPtr(qmPub),
		WireguardPort:      i32Ptr(51820),
		Status:             "active",
	}}
	results := diagnoseManifest(manifest, []string{"core-1"}, qm)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].identityOK {
		t.Fatalf("expected identity FAIL on QM divergence, got ok")
	}
	if !strings.Contains(results[0].identityIssue, "wireguard_public_key") {
		t.Errorf("expected pub-key divergence in message, got: %s", results[0].identityIssue)
	}
}

// TestBuildPeerSet_PrefersExternalIP confirms the same external→internal
// fallback Quartermaster's SyncMesh applies.
func TestBuildPeerSet_PrefersExternalIP(t *testing.T) {
	_, pub := mustGenWgKeys(t)
	cluster := []*pb.InfrastructureNode{
		{NodeId: "self", NodeName: "self", ClusterId: testCluster},
		{NodeId: "peer", NodeName: "peer", ClusterId: testCluster, ExternalIp: strPtr("203.0.113.7"), InternalIp: strPtr("10.0.0.7"), WireguardIp: strPtr("10.88.0.7"), WireguardPublicKey: strPtr(pub), WireguardPort: i32Ptr(51820), Status: "active"},
	}
	self := cluster[0]

	peers, err := buildPeerSetForHost(cluster, self)
	if err != nil {
		t.Fatalf("buildPeerSetForHost: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if peers[0].Endpoint == nil || peers[0].Endpoint.IP.String() != "203.0.113.7" {
		t.Errorf("expected external IP 203.0.113.7, got %v", peers[0].Endpoint)
	}
}

func TestBuildPeerSet_FallsBackToInternalIP(t *testing.T) {
	_, pub := mustGenWgKeys(t)
	cluster := []*pb.InfrastructureNode{
		{NodeId: "self", NodeName: "self", ClusterId: testCluster},
		{NodeId: "peer", NodeName: "peer", ClusterId: testCluster, InternalIp: strPtr("10.0.0.7"), WireguardIp: strPtr("10.88.0.7"), WireguardPublicKey: strPtr(pub), WireguardPort: i32Ptr(51820), Status: "active"},
	}
	self := cluster[0]

	peers, err := buildPeerSetForHost(cluster, self)
	if err != nil {
		t.Fatalf("buildPeerSetForHost: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if peers[0].Endpoint.IP.String() != "10.0.0.7" {
		t.Errorf("expected internal IP fallback 10.0.0.7, got %v", peers[0].Endpoint)
	}
}

func TestBuildPeerSet_SkipsInactivePeers(t *testing.T) {
	// SyncMesh's SQL filters peers to status='active'; doctor must match
	// or it would simulate apply for peers Privateer would never receive.
	_, pub := mustGenWgKeys(t)
	cluster := []*pb.InfrastructureNode{
		{NodeId: "self", NodeName: "self", ClusterId: testCluster},
		{NodeId: "decommissioned", NodeName: "decommissioned", ClusterId: testCluster, ExternalIp: strPtr("203.0.113.7"), WireguardIp: strPtr("10.88.0.7"), WireguardPublicKey: strPtr(pub), WireguardPort: i32Ptr(51820), Status: "offline"},
	}
	self := cluster[0]

	peers, err := buildPeerSetForHost(cluster, self)
	if err != nil {
		t.Fatalf("buildPeerSetForHost: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("expected 0 peers (offline node excluded), got %d", len(peers))
	}
}

func TestBuildPeerSet_SkipsPeerWithNoIP(t *testing.T) {
	_, pub := mustGenWgKeys(t)
	cluster := []*pb.InfrastructureNode{
		{NodeId: "self", NodeName: "self", ClusterId: testCluster},
		{NodeId: "ghost", NodeName: "ghost", ClusterId: testCluster, WireguardIp: strPtr("10.88.0.7"), WireguardPublicKey: strPtr(pub), WireguardPort: i32Ptr(51820), Status: "active"},
		// no external_ip, no internal_ip — Quartermaster also excludes.
	}
	self := cluster[0]

	peers, err := buildPeerSetForHost(cluster, self)
	if err != nil {
		t.Fatalf("buildPeerSetForHost: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("expected 0 peers (ghost has no endpoint), got %d", len(peers))
	}
}
