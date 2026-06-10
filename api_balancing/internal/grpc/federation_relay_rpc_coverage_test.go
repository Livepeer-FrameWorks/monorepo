package grpc

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/federation"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// newRemoteEdgeCacheFedRelay stands up a real RemoteEdgeCache over miniredis so
// the federation-forward read paths (remoteArtifactLookup, collectRemoteEdges)
// exercise the actual scan/unmarshal logic instead of a hand-rolled stub.
func newRemoteEdgeCacheFedRelay(t *testing.T, clusterID string) *federation.RemoteEdgeCache {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return federation.NewRemoteEdgeCache(client, clusterID, newTestFoghornLogger())
}

// Invariant: a context carrying KeyNoForward suppresses the federation fan-out
// entirely. This is the relay-loop guard — a command that arrived FROM a peer
// (federation.server.go sets KeyNoForward before local dispatch) must never be
// re-forwarded back out, even when peers are present and would handle it.
func TestForwardArtifact_NoForwardCtxSuppressesFanoutFedRelay(t *testing.T) {
	fed := &mockFedRPC{handlers: map[string]bool{"peer-1": true}}
	srv := &FoghornGRPCServer{
		logger:           newTestFoghornLogger(),
		federationClient: fed,
		peerManager:      &mockPeerResolver{peers: map[string]string{"peer-1": "addr-1"}},
		clusterID:        "self",
	}

	ctx := context.WithValue(context.Background(), ctxkeys.KeyNoForward, true)
	handled, err := srv.forwardArtifactToFederation(ctx, "delete_clip", "hash-1", "tenant-a", "stream-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Fatal("expected handled=false: no-forward ctx must not fan out to peers")
	}
	if len(fed.calls) != 0 {
		t.Fatalf("no-forward ctx must short-circuit before any RPC; got %d calls", len(fed.calls))
	}
}

// Invariant: with no remote edge cache wired, the artifact lookup adapter is nil
// so cross-cluster artifact discovery is simply disabled (single-cluster path).
func TestRemoteArtifactLookup_NilCacheReturnsNilFedRelay(t *testing.T) {
	srv := &FoghornGRPCServer{logger: newTestFoghornLogger()}
	if lk := srv.remoteArtifactLookup(); lk != nil {
		t.Fatalf("expected nil lookup with no remote edge cache, got %T", lk)
	}
}

// Invariant: the remoteArtifactAdapter translates cache RemoteArtifactEntry rows
// into control.RemoteArtifactInfo for the playback resolver, preserving the
// node/base-url/geo fields that drive cross-cluster artifact routing. A lookup
// for a DIFFERENT hash must return nothing — the scan is hash-scoped, so one
// artifact's peer locations never leak into another's resolution.
func TestRemoteArtifactLookup_TranslatesEntriesByHashFedRelay(t *testing.T) {
	ctx := context.Background()
	cache := newRemoteEdgeCacheFedRelay(t, "local-cluster")

	want := &federation.RemoteArtifactEntry{
		ArtifactHash: "clip-hash-A",
		ArtifactType: "clip",
		NodeID:       "edge-node-7",
		BaseURL:      "https://edge7.peer.example",
		SizeBytes:    4096,
		AccessCount:  3,
		LastAccessed: 1700000000,
		GeoLat:       52.37,
		GeoLon:       4.89,
		TenantID:     "tenant-A",
	}
	if err := cache.SetRemoteArtifact(ctx, "peer-cluster-X", want); err != nil {
		t.Fatalf("seed remote artifact: %v", err)
	}

	srv := &FoghornGRPCServer{logger: newTestFoghornLogger(), remoteEdgeCache: cache}
	lk := srv.remoteArtifactLookup()
	if lk == nil {
		t.Fatal("expected non-nil lookup when cache is wired")
	}

	infos, err := lk.GetRemoteArtifacts(ctx, "clip-hash-A")
	if err != nil {
		t.Fatalf("GetRemoteArtifacts: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected exactly 1 remote location for clip-hash-A, got %d", len(infos))
	}
	got := infos[0]
	if got.PeerCluster != "peer-cluster-X" {
		t.Fatalf("PeerCluster=%q want peer-cluster-X", got.PeerCluster)
	}
	if got.NodeID != "edge-node-7" || got.BaseURL != "https://edge7.peer.example" {
		t.Fatalf("node/baseurl mistranslated: node=%q url=%q", got.NodeID, got.BaseURL)
	}
	if got.SizeBytes != 4096 || got.AccessCount != 3 || got.LastAccessed != 1700000000 {
		t.Fatalf("size/access fields mistranslated: %+v", got)
	}
	if got.GeoLat != 52.37 || got.GeoLon != 4.89 {
		t.Fatalf("geo mistranslated: lat=%v lon=%v", got.GeoLat, got.GeoLon)
	}

	// Hash isolation: a different artifact's hash must not surface this entry.
	other, err := lk.GetRemoteArtifacts(ctx, "vod-hash-B")
	if err != nil {
		t.Fatalf("GetRemoteArtifacts(other): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("hash-scoped scan leaked: expected 0 for vod-hash-B, got %d", len(other))
	}
}

// Invariant: collectRemoteEdges only emits candidates for peers that are
// (a) not self, not empty, not a locally-served cluster, AND (b) still alive
// (heartbeat key present — the liveness gate that prevents a peer dead 30-60s
// from attracting cross-cluster routing on its longer-lived edge summary).
func TestCollectRemoteEdges_LivenessGateAndSkipsFedRelay(t *testing.T) {
	ctx := context.Background()
	cache := newRemoteEdgeCacheFedRelay(t, "self-cluster")

	srv := &FoghornGRPCServer{
		logger:          newTestFoghornLogger(),
		remoteEdgeCache: cache,
		clusterID:       "self-cluster",
	}

	// A live peer: heartbeat present + edge summary with one node.
	if err := cache.SetPeerHeartbeat(ctx, "live-peer", &federation.PeerHeartbeatRecord{EdgeCount: 1}); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}
	if err := cache.SetEdgeSummary(ctx, "live-peer", &federation.EdgeSummaryRecord{
		Edges: []*federation.EdgeSummaryEntry{{
			NodeID:         "peer-edge-1",
			BaseURL:        "https://peer-edge1.example",
			GeoLat:         48.85,
			GeoLon:         2.35,
			BWAvailableAvg: 900000000,
			CPUPercentAvg:  20.0,
			RAMUsed:        1000,
			RAMMax:         8000,
		}},
	}); err != nil {
		t.Fatalf("seed edge summary: %v", err)
	}

	// A stale peer: edge summary present but NO heartbeat → must be gated out.
	if err := cache.SetEdgeSummary(ctx, "stale-peer", &federation.EdgeSummaryRecord{
		Edges: []*federation.EdgeSummaryEntry{{NodeID: "stale-edge", BaseURL: "https://stale.example"}},
	}); err != nil {
		t.Fatalf("seed stale summary: %v", err)
	}

	peers := []*clusterpeerpb.TenantClusterPeer{
		{ClusterId: "self-cluster"}, // self → skipped
		{ClusterId: ""},             // empty → skipped
		{ClusterId: "stale-peer"},   // no heartbeat → liveness-gated out
		{ClusterId: "live-peer"},    // live + summary → candidate
	}

	candidates := srv.collectRemoteEdges(ctx, peers)
	if len(candidates) != 1 {
		t.Fatalf("expected exactly 1 candidate (live-peer only), got %d: %+v", len(candidates), candidates)
	}
	c := candidates[0]
	if c.ClusterID != "live-peer" {
		t.Fatalf("candidate from wrong cluster: %q", c.ClusterID)
	}
	if c.NodeID != "peer-edge-1" || c.BaseURL != "https://peer-edge1.example" {
		t.Fatalf("edge fields mistranslated: node=%q url=%q", c.NodeID, c.BaseURL)
	}
	if c.BWAvailable != 900000000 || c.CPUPercent != 20.0 || c.RAMMax != 8000 {
		t.Fatalf("capacity fields mistranslated: %+v", c)
	}
}

// Invariant: a peer whose heartbeat is present but whose edge summary is missing
// contributes no candidates — collectRemoteEdges requires BOTH the liveness key
// and the summary record before it routes viewers across the cluster boundary.
func TestCollectRemoteEdges_LivePeerNoSummaryYieldsNothingFedRelay(t *testing.T) {
	ctx := context.Background()
	cache := newRemoteEdgeCacheFedRelay(t, "self-cluster")
	srv := &FoghornGRPCServer{
		logger:          newTestFoghornLogger(),
		remoteEdgeCache: cache,
		clusterID:       "self-cluster",
	}

	if err := cache.SetPeerHeartbeat(ctx, "live-but-bare", &federation.PeerHeartbeatRecord{EdgeCount: 0}); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}

	candidates := srv.collectRemoteEdges(ctx, []*clusterpeerpb.TenantClusterPeer{{ClusterId: "live-but-bare"}})
	if len(candidates) != 0 {
		t.Fatalf("expected 0 candidates with heartbeat but no summary, got %d", len(candidates))
	}
}
