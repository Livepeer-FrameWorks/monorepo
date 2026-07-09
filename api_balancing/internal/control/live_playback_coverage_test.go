package control

import (
	"context"
	"sync"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// seedLiveEdgeNode registers a healthy, probe-verified edge node that the load
// balancer will score above zero: CapEdge=true, RAMMax/BWLimit set with idle
// up-speed so BWAvailable>0. Returns after recompute+touch+probe so the node
// appears IsActive in the balancer snapshot. Unique IDs per test avoid
// cross-test contamination through the package-global DefaultManager.
func seedLiveEdgeNode(t *testing.T, sm *state.StreamStateManager, nodeID, baseURL string, lat, lon float64, outputs map[string]any) {
	t.Helper()
	plat, plon := lat, lon
	sm.SetNodeInfo(nodeID, baseURL, true, &plat, &plon, "loc-"+nodeID, "", outputs)
	sm.UpdateNodeMetrics(nodeID, struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		ProcessingClasses    map[string]state.ClassCapacity
	}{
		CPU:     10,
		RAMMax:  16_000_000_000,
		BWLimit: 1_000_000_000,
		UpSpeed: 1_000, // negligible vs BWLimit -> BWAvailable > 0
		CapEdge: true,
	})
	sm.TouchNode(nodeID, true)
	sm.SetProbeVerified(nodeID, true)
}

// markLiveStreamPresent registers the stream as active with inputs on the node,
// which is the load balancer's presence requirement for a push (live+) stream:
// without an active input on some node, the balancer rejects every candidate
// (rejectStreamMissing/NoInputs). The key is the BARE internal name, matching
// what ResolveLivePlayback passes after trimming the live+ prefix.
func markLiveStreamPresent(sm *state.StreamStateManager, bareInternalName, nodeID, tenantID string) {
	sm.SetStreamInstanceInputs(bareInternalName, nodeID, 1)
	_ = sm.UpdateStreamFromBuffer("live+"+bareInternalName, bareInternalName, nodeID, tenantID, "FULL", "")
}

// liveHLSOutputs is a minimal Mist outputs map whose HLS template resolves to a
// non-empty URL ($ -> stream name), so BuildViewerEndpointFromOutputs yields a
// real endpoint.
func liveHLSOutputs() map[string]any {
	return map[string]any{"HLS": "/hls/$/index.m3u8"}
}

func newLiveDeps(sm *state.StreamStateManager, lat, lon float64) *PlaybackDependencies {
	return &PlaybackDependencies{
		LB:     balancer.NewLoadBalancer(logging.NewLogger()),
		GeoLat: lat,
		GeoLon: lon,
	}
}

// ResolveLivePlayback with no load balancer is a hard guard: live resolution
// cannot pick an edge without the balancer, so it must error rather than return
// an empty/degenerate endpoint set.
func TestResolveLivePlayback_NilLoadBalancerErrors(t *testing.T) {
	if _, err := ResolveLivePlayback(context.Background(), &PlaybackDependencies{}, "vk", "live+s", "stream-1", "t1"); err == nil {
		t.Fatal("nil load balancer must error")
	}
}

// Local-edge happy path: a single healthy edge node carrying HLS outputs is the
// only candidate, so live resolution must return it as the primary viewer
// endpoint with live metadata (tenant + content id) and protocol hints derived
// from the node's outputs. This locks the core live viewer-selection decision.
func TestResolveLivePlayback_LocalEdgeHappyPath(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedLiveEdgeNode(t, sm, "edge-local-1", "https://edge1.example.com", 52.0, 5.0, liveHLSOutputs())
	markLiveStreamPresent(sm, "demo_stream", "edge-local-1", "tenant-A")

	deps := newLiveDeps(sm, 52.0, 5.0)
	resp, err := ResolveLivePlayback(context.Background(), deps, "view-key-1", "live+demo_stream", "stream-77", "tenant-A")
	if err != nil {
		t.Fatalf("live resolution failed: %v", err)
	}
	if resp.GetPrimary() == nil {
		t.Fatal("expected a primary viewer endpoint")
	}
	if resp.GetPrimary().GetNodeId() != "edge-local-1" {
		t.Fatalf("primary should be the only healthy edge, got %q", resp.GetPrimary().GetNodeId())
	}
	if resp.GetPrimary().GetUrl() == "" {
		t.Fatal("primary endpoint url must be populated from HLS outputs")
	}
	md := resp.GetMetadata()
	if md == nil || md.GetTenantId() != "tenant-A" {
		t.Fatalf("metadata must carry the live tenant, got %+v", md)
	}
	if md.GetContentId() != "view-key-1" {
		t.Fatalf("content id must be the public view key, got %q", md.GetContentId())
	}
	if md.GetContentType() != "live" || !md.GetIsLive() {
		t.Fatalf("live metadata expected, got type=%q isLive=%v", md.GetContentType(), md.GetIsLive())
	}
	if len(md.GetProtocolHints()) == 0 {
		t.Fatal("protocol hints should be derived from the primary endpoint outputs")
	}
}

// Tenant isolation invariant: an edge node owned by tenant X must NOT be offered
// to a viewer resolving under tenant Y. The balancer's cluster-scope filter
// (driven by the tenant id passed to ResolveLivePlayback) is the gate; with no
// node in the requested tenant scope, resolution must fail closed.
func TestResolveLivePlayback_TenantIsolation(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedLiveEdgeNode(t, sm, "edge-tenantX-1", "https://edgex.example.com", 52.0, 5.0, liveHLSOutputs())
	// Pin the node's ownership to tenant-X so the cluster-scope filter excludes
	// it for any other tenant.
	sm.SetNodeConnectionInfo(context.Background(), "edge-tenantX-1", "edgex.example.com", "tenant-X", "", nil)
	markLiveStreamPresent(sm, "s", "edge-tenantX-1", "tenant-X")

	deps := newLiveDeps(sm, 52.0, 5.0)

	// Same tenant resolves fine (sanity: the node IS otherwise eligible).
	if _, err := ResolveLivePlayback(context.Background(), deps, "vk", "live+s", "stream-1", "tenant-X"); err != nil {
		t.Fatalf("same-tenant live resolution should succeed, got %v", err)
	}

	// Foreign tenant must not be routed to tenant-X's node.
	if _, err := ResolveLivePlayback(context.Background(), deps, "vk", "live+s", "stream-1", "tenant-Y"); err == nil {
		t.Fatal("a foreign tenant must not be offered another tenant's edge node")
	}
}

// Cross-cluster live redirect: when only a remote peer edge is available, live
// resolution emits a redirect endpoint to that peer cluster's play domain. The
// URL composition (https://<host>/play/<viewKey>) and the ClusterId stamp are
// the cross-cluster routing contract the viewer follows. No local node is
// seeded, so the remote candidate is the sole source of the answer.
func TestResolveLivePlayback_RemoteEdgeRedirect(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	// Seed a local edge so GetTopNodesWithScores does not error on an empty
	// snapshot; make it a different geo so the remote (co-located with viewer)
	// outscores it and becomes primary.
	seedLiveEdgeNode(t, sm, "edge-faraway-1", "https://far.example.com", 10.0, 10.0, liveHLSOutputs())

	deps := newLiveDeps(sm, 52.0, 5.0)
	deps.RemoteEdges = []balancer.RemoteEdgeCandidate{{
		ClusterID:   "peer-cluster-eu",
		NodeID:      "peer-edge-1",
		BaseURL:     "peer-edge.eu.example.com",
		GeoLat:      52.1,
		GeoLon:      5.1,
		BWAvailable: 1_000_000_000,
		RAMMax:      16_000_000_000,
		RAMUsed:     1_000_000_000,
		CPUPercent:  5,
	}}

	resp, err := ResolveLivePlayback(context.Background(), deps, "view-key-9", "live+s", "stream-9", "tenant-A")
	if err != nil {
		t.Fatalf("remote-edge live resolution failed: %v", err)
	}

	// Find the remote redirect endpoint among primary+fallbacks.
	remote := collectEndpoints(resp)
	redirect := findEndpoint(remote, "peer-edge-1")
	if redirect == nil {
		t.Fatalf("expected a redirect endpoint for the peer edge, got %+v", remote)
	}
	if redirect.GetProtocol() != "redirect" {
		t.Fatalf("peer edge endpoint protocol must be 'redirect', got %q", redirect.GetProtocol())
	}
	if redirect.GetClusterId() != "peer-cluster-eu" {
		t.Fatalf("redirect must carry the peer cluster id, got %q", redirect.GetClusterId())
	}
	if got, want := redirect.GetUrl(), "https://peer-edge.eu.example.com/play/view-key-9"; got != want {
		t.Fatalf("redirect URL composition = %q, want %q", got, want)
	}
}

// Federated peers advertise their full playback base URL — scheme and /view
// path included (EDGE_PUBLIC_URL, e.g. "https://edge.example/view") — not a
// bare host. The redirect builder must normalize that shape to
// https://<host>/play/<id> rather than prepending a second scheme.
func TestResolveLivePlayback_RemoteEdgeRedirectNormalizesFullBaseURL(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedLiveEdgeNode(t, sm, "edge-faraway-2", "https://far.example.com", 10.0, 10.0, liveHLSOutputs())

	deps := newLiveDeps(sm, 52.0, 5.0)
	deps.RemoteEdges = []balancer.RemoteEdgeCandidate{{
		ClusterID:   "peer-cluster-eu",
		NodeID:      "peer-edge-2",
		BaseURL:     "https://peer-edge.eu.example.com/view",
		GeoLat:      52.1,
		GeoLon:      5.1,
		BWAvailable: 1_000_000_000,
		RAMMax:      16_000_000_000,
		RAMUsed:     1_000_000_000,
		CPUPercent:  5,
	}}

	resp, err := ResolveLivePlayback(context.Background(), deps, "view-key-9", "live+s", "stream-9", "tenant-A")
	if err != nil {
		t.Fatalf("remote-edge live resolution failed: %v", err)
	}

	redirect := findEndpoint(collectEndpoints(resp), "peer-edge-2")
	if redirect == nil {
		t.Fatalf("expected a redirect endpoint for the peer edge, got %+v", collectEndpoints(resp))
	}
	if got, want := redirect.GetUrl(), "https://peer-edge.eu.example.com/play/view-key-9"; got != want {
		t.Fatalf("redirect URL normalization = %q, want %q", got, want)
	}
}

// Score-merge ordering: with both a local edge and a geo-near remote edge, the
// merged candidate set is re-sorted by score (highest first). The geo-near
// remote outscores the geo-far local even after the cross-cluster penalty, so
// the remote redirect must be the PRIMARY endpoint. This locks the merge+sort
// routing decision in ResolveLivePlayback.
func TestResolveLivePlayback_GeoNearRemoteWinsPrimary(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	// Local edge is geographically far from the viewer (low geo score).
	seedLiveEdgeNode(t, sm, "edge-far-local", "https://far.example.com", -30.0, -60.0, liveHLSOutputs())

	deps := newLiveDeps(sm, 52.0, 5.0)
	deps.RemoteEdges = []balancer.RemoteEdgeCandidate{{
		ClusterID:   "peer-near",
		NodeID:      "peer-near-1",
		BaseURL:     "near.peer.example.com",
		GeoLat:      52.0, // right on top of the viewer -> max geo score
		GeoLon:      5.0,
		BWAvailable: 1_250_000_000, // == remoteBWRefCapacity -> full bw score
		RAMMax:      16_000_000_000,
		RAMUsed:     0,
		CPUPercent:  0,
	}}

	resp, err := ResolveLivePlayback(context.Background(), deps, "vk", "live+s", "stream-1", "tenant-A")
	if err != nil {
		t.Fatalf("merge resolution failed: %v", err)
	}
	if resp.GetPrimary() == nil || resp.GetPrimary().GetNodeId() != "peer-near-1" {
		t.Fatalf("geo-near remote edge should win as primary, got %+v", resp.GetPrimary())
	}
	if resp.GetPrimary().GetProtocol() != "redirect" {
		t.Fatalf("winning remote primary must be a redirect, got %q", resp.GetPrimary().GetProtocol())
	}
}

// Stream-state enrichment: live metadata reflects live stream state recorded in
// the manager (status, viewers) keyed by the BARE internal name — confirming
// ResolveLivePlayback strips the live+ prefix before looking up state, so the
// enriched viewer count/buffer state are not silently dropped.
func TestResolveLivePlayback_EnrichesFromStreamState(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedLiveEdgeNode(t, sm, "edge-enrich-1", "https://e.example.com", 52.0, 5.0, liveHLSOutputs())
	// Stream state is keyed by bare internal name; ResolveLivePlayback is given
	// the wildcard live+ name and must trim it to find this for both presence
	// (balancer) and metadata enrichment.
	markLiveStreamPresent(sm, "demo_stream", "edge-enrich-1", "tenant-A")

	deps := newLiveDeps(sm, 52.0, 5.0)
	resp, err := ResolveLivePlayback(context.Background(), deps, "vk", "live+demo_stream", "stream-1", "tenant-A")
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	md := resp.GetMetadata()
	if md.GetStatus() != "live" || !md.GetIsLive() {
		t.Fatalf("metadata should reflect live stream state, got status=%q isLive=%v", md.GetStatus(), md.GetIsLive())
	}
	if md.GetBufferState() != "FULL" {
		t.Fatalf("buffer state should be carried from stream state, got %q", md.GetBufferState())
	}
}

// AuthoritativeClusterServable local + served-cluster branches: these complete
// the front-door authority decision that the pure-helper test only covers for
// the empty/peer cases. The artifact's authoritative byte-cluster is serveable
// when it is THIS foghorn's local cluster or any additional cluster this
// foghorn serves (multi-cluster foghorn), with no tenant peer entry required.
func TestAuthoritativeClusterServable_LocalAndServedBranches(t *testing.T) {
	prevLocal := localClusterID
	prevServed := servedClusters.Load()
	t.Cleanup(func() {
		localClusterID = prevLocal
		servedClusters.Store(prevServed)
	})
	servedClusters.Store(&sync.Map{})
	SetLocalClusterID("media-central-primary")
	AddServedCluster("media-edge-secondary")

	// Local cluster id is always serveable, even with an empty peer set.
	if !AuthoritativeClusterServable("media-central-primary", nil) {
		t.Fatal("local cluster must be serveable without any peer entry")
	}
	// An additionally-served cluster is serveable without a peer entry.
	if !AuthoritativeClusterServable("media-edge-secondary", nil) {
		t.Fatal("served cluster must be serveable without any peer entry")
	}
	// A cluster that is neither local, served, nor a peer must be refused.
	if AuthoritativeClusterServable("foreign-cluster", nil) {
		t.Fatal("unserved foreign cluster must be refused")
	}
	// ...but the same foreign cluster becomes serveable once it is an
	// authorized tenant peer.
	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "foreign-cluster"}}
	if !AuthoritativeClusterServable("foreign-cluster", peers) {
		t.Fatal("foreign cluster authorized as a tenant peer must be serveable")
	}
}

func collectEndpoints(resp *sharedpb.ViewerEndpointResponse) []*sharedpb.ViewerEndpoint {
	out := []*sharedpb.ViewerEndpoint{resp.GetPrimary()}
	return append(out, resp.GetFallbacks()...)
}

func findEndpoint(eps []*sharedpb.ViewerEndpoint, nodeID string) *sharedpb.ViewerEndpoint {
	for _, e := range eps {
		if e != nil && e.GetNodeId() == nodeID {
			return e
		}
	}
	return nil
}
