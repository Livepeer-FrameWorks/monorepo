package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"google.golang.org/grpc/codes"
)

func TestReserveLocalIngestAdmissionCountsInflight(t *testing.T) {
	previousRegistry := StreamRegistryInstance
	StreamRegistryInstance = NewStreamRegistry(nil, "cluster-a", time.Minute)
	localIngestAdmission.Lock()
	localIngestAdmission.inflight = 0
	localIngestAdmission.connecting = 0
	localIngestAdmission.Unlock()
	t.Cleanup(func() {
		StreamRegistryInstance = previousRegistry
		localIngestAdmission.Lock()
		localIngestAdmission.inflight = 0
		localIngestAdmission.connecting = 0
		localIngestAdmission.Unlock()
	})

	release, err := ReserveLocalIngestAdmission(1)
	if err != nil {
		t.Fatalf("reserve first slot: %v", err)
	}
	if _, err := ReserveLocalIngestAdmission(1); !errors.Is(err, ErrLocalIngestCapacity) {
		t.Fatalf("second concurrent reservation = %v, want capacity error", err)
	}
	release()
}

func TestReserveLocalIngestAdmissionCountsProjectedPublishersOwnedByThisReplica(t *testing.T) {
	previousRegistry := StreamRegistryInstance
	r := NewStreamRegistry(nil, "cluster-a", time.Minute)
	StreamRegistryInstance = r
	holdControlConnection(t, "node-local")
	localIngestAdmission.Lock()
	localIngestAdmission.inflight = 0
	localIngestAdmission.connecting = 0
	localIngestAdmission.Unlock()
	t.Cleanup(func() {
		StreamRegistryInstance = previousRegistry
		localIngestAdmission.Lock()
		localIngestAdmission.inflight = 0
		localIngestAdmission.connecting = 0
		localIngestAdmission.Unlock()
	})

	projectSourceForTest(t, r, "stream-local", "node-local", 10, "trigger-local", "generation-local", 1)
	projectSourceForTest(t, r, "stream-peer", "node-peer", 20, "trigger-peer", "generation-peer", 2)

	if count := localSourceProjectionCount(); count != 1 {
		t.Fatalf("local projected publisher count = %d, want 1", count)
	}
	if _, err := ReserveLocalIngestAdmission(1); !errors.Is(err, ErrLocalIngestCapacity) {
		t.Fatalf("reservation at projected capacity = %v, want capacity error", err)
	}
}

func TestReserveLocalIngestNodeConnectionIncludesPublishersMovedByFailover(t *testing.T) {
	previousRegistry := StreamRegistryInstance
	r := NewStreamRegistry(nil, "cluster-a", time.Minute)
	StreamRegistryInstance = r
	localIngestAdmission.Lock()
	localIngestAdmission.inflight = 0
	localIngestAdmission.connecting = 0
	localIngestAdmission.Unlock()
	t.Cleanup(func() {
		StreamRegistryInstance = previousRegistry
		localIngestAdmission.Lock()
		localIngestAdmission.inflight = 0
		localIngestAdmission.connecting = 0
		localIngestAdmission.Unlock()
	})

	projectSourceForTest(t, r, "stream-local", "node-local", 10, "trigger-local", "generation-local", 1)
	projectSourceForTest(t, r, "stream-moving-a", "node-moving", 20, "trigger-a", "generation-a", 2)
	projectSourceForTest(t, r, "stream-moving-b", "node-moving", 21, "trigger-b", "generation-b", 3)
	holdControlConnection(t, "node-local")

	if _, err := reserveLocalIngestNodeConnection("node-moving", 2); !errors.Is(err, ErrLocalIngestCapacity) {
		t.Fatalf("failover reservation beyond renewal capacity = %v, want capacity error", err)
	}
	release, err := reserveLocalIngestNodeConnection("node-moving", 3)
	if err != nil {
		t.Fatalf("reserve failover at renewal capacity: %v", err)
	}
	if _, err := ReserveLocalIngestAdmission(3); !errors.Is(err, ErrLocalIngestCapacity) {
		t.Fatalf("new publisher admitted while failover reservation consumes capacity: %v", err)
	}
	release()
}

// seedIngestNode registers a healthy ingest-capable node. Mirrors
// seedLiveEdgeNode but sets CapIngest, which is what the balancer filters on
// when the capability context key says "ingest".
func seedIngestNodeInCluster(t *testing.T, sm *state.StreamStateManager, nodeID, baseURL string, lat, lon float64, outputs map[string]any, clusterID string) {
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
		CPU:       10,
		RAMMax:    16_000_000_000,
		BWLimit:   1_000_000_000,
		UpSpeed:   1_000,
		CapIngest: true,
	})
	sm.TouchNode(nodeID, true)
	sm.SetProbeVerified(nodeID, true)
	// Bind the node to a virtual media cluster: selection authorizes on the
	// node's authenticated cluster, and an unattributed node is refused.
	// Tests that need a different cluster overwrite this afterwards.
	sm.SetNodeConnectionInfo(context.Background(), nodeID, nodeID+":18090", "", clusterID, nil)
}

// seedIngestNode registers a node in the default virtual media cluster.
func seedIngestNode(t *testing.T, sm *state.StreamStateManager, nodeID, baseURL string, lat, lon float64, outputs map[string]any) {
	t.Helper()
	seedIngestNodeInCluster(t, sm, nodeID, baseURL, lat, lon, outputs, defaultIngestCluster)
}

// defaultIngestCluster is the virtual media cluster seeded nodes belong to
// unless a test says otherwise.
const defaultIngestCluster = "cluster-a"

func ingestOutputs(host string) map[string]any {
	return map[string]any{"HLS": "http://" + host + "/hls/$/index.m3u8"}
}

// newIngestDeps builds selector dependencies. The Foghorn's own cluster is not
// among them: selection authorizes against the response envelope, never against
// the process's identity.
func newIngestDeps(lat, lon float64, _ string) *IngestDependencies {
	return &IngestDependencies{
		LB:     balancer.NewLoadBalancer(logging.NewLogger()),
		GeoLat: lat,
		GeoLon: lon,
	}
}

// healthyPeers builds the cluster-peer envelope Commodore returns: already
// narrowed to the tenant's plan classes and to healthy clusters, which is why
// membership alone authorizes a candidate.
func healthyPeers(clusterIDs ...string) []*clusterpeerpb.TenantClusterPeer {
	peers := make([]*clusterpeerpb.TenantClusterPeer, 0, len(clusterIDs))
	for _, id := range clusterIDs {
		peers = append(peers, &clusterpeerpb.TenantClusterPeer{ClusterId: id, HealthStatus: "healthy"})
	}
	return peers
}

func admittedCtx(tenantID string) *commodorepb.ResolveStreamContextResponse {
	return &commodorepb.ResolveStreamContextResponse{
		ClusterPeers:       healthyPeers(defaultIngestCluster),
		Admitted:           true,
		StreamId:           "stream-1",
		InternalName:       "internal-1",
		TenantId:           tenantID,
		IngestMode:         "push",
		IsRecordingEnabled: true,
	}
}

func TestIngestAddressForNodePreservesAdvertisedAuthority(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestNode(t, sm, "edge-1", "http://edge-1.example.com:18090/view", 0, 0, ingestOutputs("wrong.example.com:443"))

	got, ok := ingestAddressForNode("edge-1", ingestOutputs("wrong.example.com:443"))
	if !ok {
		t.Fatal("expected advertised address")
	}
	if got.scheme != "http" || got.authority != "edge-1.example.com:18090" || got.hostname != "edge-1.example.com" {
		t.Fatalf("unexpected address: %+v", got)
	}
}

// Locks the URL rendering contract: WHIP keeps the advertised HTTP authority,
// while RTMP and SRT use their fleet-wide ports.
func TestBuildIngestEndpoint_ProtocolURLs(t *testing.T) {
	ep := buildIngestEndpoint("node-1", ingestPublicAddress{
		scheme: "https", authority: "edge-1.example.com", hostname: "edge-1.example.com",
	}, "sk-abc", "eu-west", "cluster-1", 42)
	if ep == nil {
		t.Fatal("expected endpoint")
	}
	if got, want := ep.GetWhipUrl(), "https://edge-1.example.com/webrtc/sk-abc"; got != want {
		t.Errorf("whip: got %q want %q", got, want)
	}
	if got, want := ep.GetRtmpUrl(), "rtmp://edge-1.example.com:1935/live/sk-abc"; got != want {
		t.Errorf("rtmp: got %q want %q", got, want)
	}
	if got, want := ep.GetSrtUrl(), "srt://edge-1.example.com:8889?streamid=sk-abc"; got != want {
		t.Errorf("srt: got %q want %q", got, want)
	}
	if ep.GetKind() != sharedpb.IngestEndpointKind_INGEST_ENDPOINT_KIND_NODE_SPECIFIC {
		t.Errorf("kind: got %v", ep.GetKind())
	}
	if ep.GetClusterId() != "cluster-1" || ep.GetRegion() != "eu-west" || ep.GetLoadScore() != 42 {
		t.Errorf("unexpected endpoint metadata: %+v", ep)
	}
}

// A bare IPv6 host has to be bracketed for the URL authority, and SRT/RTMP
// host:port joins must not produce "::1:1935".
func TestBuildIngestEndpoint_IPv6Authority(t *testing.T) {
	ep := buildIngestEndpoint("node-1", ingestPublicAddress{
		scheme: "http", authority: "[::1]:18090", hostname: "::1",
	}, "sk", "", "cluster-1", 1)
	if ep == nil {
		t.Fatal("expected endpoint")
	}
	if got, want := ep.GetWhipUrl(), "http://[::1]:18090/webrtc/sk"; got != want {
		t.Errorf("whip: got %q want %q", got, want)
	}
	if got, want := ep.GetRtmpUrl(), "rtmp://[::1]:1935/live/sk"; got != want {
		t.Errorf("rtmp: got %q want %q", got, want)
	}
	if !strings.HasPrefix(ep.GetSrtUrl(), "srt://[::1]:8889?") {
		t.Errorf("srt: got %q", ep.GetSrtUrl())
	}
}

func TestBuildIngestEndpoint_RequiresHostAndKey(t *testing.T) {
	if buildIngestEndpoint("n", ingestPublicAddress{}, "sk", "", "c", 1) != nil {
		t.Error("empty host must yield no endpoint")
	}
	if buildIngestEndpoint("n", ingestPublicAddress{scheme: "https", authority: "h", hostname: "h"}, "", "", "c", 1) != nil {
		t.Error("empty stream key must yield no endpoint")
	}
}

func TestParseIngestPublicAddressRejectsEmbeddedCredentials(t *testing.T) {
	if _, ok := parseIngestPublicAddress("https://user:password@ingest.example.com:8443/view"); ok {
		t.Fatal("client-facing node address with userinfo must not be emitted")
	}
}

// The evaluator is the single admission authority for both transports; every
// rejection reason must map to its own status rather than collapsing into
// "invalid key", which would report a degraded cluster as a bad credential.
func TestEvaluateIngestAdmission_ReasonMapping(t *testing.T) {
	cases := []struct {
		name       string
		resp       *commodorepb.ResolveStreamContextResponse
		wantHTTP   int
		wantCode   string
		wantGRPC   codes.Code
		wantDenied bool
	}{
		{
			name:       "nil response fails closed",
			resp:       nil,
			wantHTTP:   403,
			wantCode:   "INGEST_DENIED",
			wantGRPC:   codes.PermissionDenied,
			wantDenied: true,
		},
		{
			name:       "admitted push stream",
			resp:       admittedCtx("t1"),
			wantDenied: false,
		},
		{
			name:       "invalid key",
			resp:       denied(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY),
			wantHTTP:   404,
			wantCode:   "INVALID_STREAM_KEY",
			wantGRPC:   codes.NotFound,
			wantDenied: true,
		},
		{
			name:       "user inactive",
			resp:       denied(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_USER_INACTIVE),
			wantHTTP:   403,
			wantCode:   "ACCOUNT_INACTIVE",
			wantGRPC:   codes.PermissionDenied,
			wantDenied: true,
		},
		{
			name:       "pull mode reason",
			resp:       denied(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_PULL_MODE),
			wantHTTP:   409,
			wantCode:   "PULL_MODE_STREAM",
			wantGRPC:   codes.FailedPrecondition,
			wantDenied: true,
		},
		{
			name:       "tenant suspended",
			resp:       denied(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_TENANT_SUSPENDED),
			wantHTTP:   403,
			wantCode:   "ACCOUNT_SUSPENDED",
			wantGRPC:   codes.PermissionDenied,
			wantDenied: true,
		},
		{
			name:       "balance negative",
			resp:       denied(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_BALANCE_NEGATIVE),
			wantHTTP:   402,
			wantCode:   "PAYMENT_REQUIRED",
			wantGRPC:   codes.FailedPrecondition,
			wantDenied: true,
		},
		{
			name:       "cluster not entitled",
			resp:       denied(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED),
			wantHTTP:   403,
			wantCode:   "CLUSTER_NOT_ENTITLED",
			wantGRPC:   codes.PermissionDenied,
			wantDenied: true,
		},
		{
			name:       "cluster class mismatch",
			resp:       denied(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_CLASS_MISMATCH),
			wantHTTP:   403,
			wantCode:   "CLUSTER_CLASS_MISMATCH",
			wantGRPC:   codes.PermissionDenied,
			wantDenied: true,
		},
		{
			name:       "protocol not supported",
			resp:       denied(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_PROTOCOL_NOT_SUPPORTED),
			wantHTTP:   415,
			wantCode:   "PROTOCOL_NOT_SUPPORTED",
			wantGRPC:   codes.InvalidArgument,
			wantDenied: true,
		},
		{
			name:       "cluster unhealthy is transient, not a bad key",
			resp:       denied(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_UNHEALTHY),
			wantHTTP:   503,
			wantCode:   "CLUSTER_UNHEALTHY",
			wantGRPC:   codes.Unavailable,
			wantDenied: true,
		},
		{
			name:       "duplicate ingest is a conflict, not a bad key",
			resp:       denied(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST),
			wantHTTP:   409,
			wantCode:   "DUPLICATE_INGEST",
			wantGRPC:   codes.AlreadyExists,
			wantDenied: true,
		},
		{
			name:       "not admitted with no reason fails closed",
			resp:       denied(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_UNSPECIFIED),
			wantHTTP:   403,
			wantCode:   "INGEST_DENIED",
			wantGRPC:   codes.PermissionDenied,
			wantDenied: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			denial := EvaluateIngestAdmission(tc.resp)
			if !tc.wantDenied {
				if denial != nil {
					t.Fatalf("expected admission, got denial %+v", denial)
				}
				return
			}
			if denial == nil {
				t.Fatal("expected denial")
			}
			if denial.HTTPStatus != tc.wantHTTP {
				t.Errorf("http: got %d want %d", denial.HTTPStatus, tc.wantHTTP)
			}
			if denial.Code != tc.wantCode {
				t.Errorf("code: got %q want %q", denial.Code, tc.wantCode)
			}
			if denial.GRPCCode != tc.wantGRPC {
				t.Errorf("grpc: got %v want %v", denial.GRPCCode, tc.wantGRPC)
			}
		})
	}
}

// Precedence guard: Commodore admits pull streams (they are valid for playback
// and materialization), so an admitted-plus-pull response must still be refused
// for publishing. Checking `admitted` first would silently accept it.
func TestEvaluateIngestAdmission_AdmittedPullStreamStillDenied(t *testing.T) {
	resp := admittedCtx("t1")
	resp.IngestMode = "pull"

	denial := EvaluateIngestAdmission(resp)
	if denial == nil {
		t.Fatal("admitted pull stream must not accept push ingest")
	}
	if denial.Code != "PULL_MODE_STREAM" {
		t.Fatalf("got %q want PULL_MODE_STREAM", denial.Code)
	}
}

func denied(reason commodorepb.StreamKeyRejectionReason) *commodorepb.ResolveStreamContextResponse {
	return &commodorepb.ResolveStreamContextResponse{
		Admitted:        false,
		IngestMode:      "push",
		RejectionReason: reason,
	}
}

func TestResolveIngestEndpoints_NilLoadBalancerErrors(t *testing.T) {
	if _, err := ResolveIngestEndpoints(context.Background(), &IngestDependencies{}, admittedCtx("t1"), "sk"); err == nil {
		t.Fatal("nil load balancer must error")
	}
}

// Cluster entitlement belongs to a resolved tenant. A context without one has
// no authority envelope, so it must be refused before candidates from the
// shared pool are considered.
func TestResolveIngestEndpoints_MissingTenantFailsClosed(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestNode(t, sm, "ingest-shared", "http://n:18090", 52.0, 4.0, ingestOutputs("n.example.com:18090"))

	if _, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "cluster-1"), admittedCtx(""), "sk"); err == nil {
		t.Fatal("stream context without a tenant must not resolve")
	}
}

// Usability (a resolvable public host) is not part of the balancer's ranking,
// so the resolver must look past unusable top-ranked nodes instead of
// reporting that no ingest capacity exists.
func TestResolveIngestEndpoints_LooksPastUnusableTopNodes(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	// Six higher-scoring nodes with no resolvable host would exhaust a
	// five-node fetch before the usable one is ever considered.
	for _, id := range []string{"bad-1", "bad-2", "bad-3", "bad-4", "bad-5", "bad-6"} {
		seedIngestNode(t, sm, id, "", 52.0, 4.0, map[string]any{})
	}
	seedIngestNode(t, sm, "good-1", "http://good-1:18090", 52.0, 4.0, ingestOutputs("good-1.example.com:18090"))

	resp, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "cluster-1"), admittedCtx("t1"), "sk")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resp.GetPrimary().GetNodeId() != "good-1" {
		t.Fatalf("primary: got %q want good-1", resp.GetPrimary().GetNodeId())
	}
}

// Happy path: an ingest-capable node with resolvable outputs becomes the
// primary, and metadata is carried through from the stream context.
func TestResolveIngestEndpoints_PicksIngestCapableNode(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestNode(t, sm, "ingest-node-1", "http://ingest-node-1:18090", 52.0, 4.0, ingestOutputs("ingest-1.example.com:18090"))

	resp, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "cluster-1"), admittedCtx("t1"), "sk-abc")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resp.GetPrimary().GetNodeId() != "ingest-node-1" {
		t.Fatalf("primary: got %q", resp.GetPrimary().GetNodeId())
	}
	if got, want := resp.GetPrimary().GetWhipUrl(), "http://ingest-node-1:18090/webrtc/sk-abc"; got != want {
		t.Errorf("whip: got %q want %q", got, want)
	}
	md := resp.GetMetadata()
	if md.GetStreamId() != "stream-1" || md.GetTenantId() != "t1" || md.GetStreamKey() != "sk-abc" || !md.GetRecordingEnabled() {
		t.Errorf("unexpected metadata: %+v", md)
	}
}

func TestResolveIngestEndpoints_UsesPublicBaseURLWithoutOutputSnapshot(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestNode(t, sm, "ingest-node-1", "http://ingest.example.com:18090/view", 52.0, 4.0, nil)

	resp, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "cluster-1"), admittedCtx("t1"), "sk")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got, want := resp.GetPrimary().GetWhipUrl(), "http://ingest.example.com:18090/webrtc/sk"; got != want {
		t.Fatalf("whip: got %q want %q", got, want)
	}
}

// Edge-only nodes must never be handed to a publisher: the capability filter is
// what keeps ingest off nodes that only serve viewers.
func TestResolveIngestEndpoints_SkipsNonIngestNodes(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedLiveEdgeNode(t, sm, "edge-only-1", "http://edge-only-1:18090", 52.0, 4.0, ingestOutputs("edge-only-1.example.com:18090"))

	if _, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "cluster-1"), admittedCtx("t1"), "sk"); err == nil {
		t.Fatal("edge-only node must not be offered for ingest")
	}
}

// A node with no resolvable public host cannot produce a usable URL, so it is
// skipped rather than emitted with a malformed authority.
func TestResolveIngestEndpoints_SkipsNodesWithoutResolvableHost(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestNode(t, sm, "ingest-node-nohost", "", 52.0, 4.0, map[string]any{})

	if _, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "cluster-1"), admittedCtx("t1"), "sk"); err == nil {
		t.Fatal("node without resolvable host must not be offered")
	}
}

// A node in the resolving cluster is still returned, carrying that cluster.
func TestResolveIngestEndpoints_ReturnsLocalClusterNode(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestNode(t, sm, "local-node", "http://n:18090", 52.0, 4.0, ingestOutputs("n.example.com:18090"))
	sm.SetNodeConnectionInfo(context.Background(), "local-node", "n.example.com:18090", "", "cluster-a", nil)

	resp, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "cluster-a"), admittedCtx("t1"), "sk")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := resp.GetPrimary().GetNodeId(); got != "local-node" {
		t.Fatalf("primary = %q, want local-node", got)
	}
	if got := resp.GetPrimary().GetClusterId(); got != "cluster-a" {
		t.Fatalf("cluster id = %q, want cluster-a", got)
	}
}

func TestResolveIngestEndpoints_RejectsUnentitledPooledCluster(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()

	seedIngestNode(t, sm, "foreign-node", "https://foreign.example.com", 52.0, 4.0, ingestOutputs("foreign.example.com"))
	sm.SetNodeConnectionInfo(context.Background(), "foreign-node", "foreign.example.com", "", "foreign-cluster", nil)

	if _, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "cluster-a"), admittedCtx("tenant-a"), "sk"); err == nil {
		t.Fatal("node from an unentitled pooled cluster must not be offered")
	}
}

// Commodore drops unhealthy clusters from the envelope, so one appearing here
// is already anomalous — but it must still not be offered, or the publisher is
// sent somewhere PUSH_REWRITE refuses. Unknown health fails closed too.
func TestResolveIngestEndpoints_RejectsUnhealthyPeerInEnvelope(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()

	seedIngestNode(t, sm, "peer-node", "https://peer.example.com", 52.0, 4.0, ingestOutputs("peer.example.com"))
	sm.SetNodeConnectionInfo(context.Background(), "peer-node", "peer.example.com", "", "peer-cluster", nil)
	streamCtx := admittedCtx("tenant-a")
	streamCtx.ClusterPeers = []*clusterpeerpb.TenantClusterPeer{{
		ClusterId:    "peer-cluster",
		HealthStatus: "degraded",
	}}

	if _, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "cluster-a"), streamCtx, "sk"); err == nil {
		t.Fatal("degraded entitled peer must not be offered")
	}
}

// A node that reports no cluster is unattributed: there is nothing to check an
// entitlement against, so it is refused rather than assumed to be in the
// resolving Foghorn's own cluster.
func TestResolveIngestEndpoints_RefusesClusterlessNode(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestNodeInCluster(t, sm, "clusterless-node", "http://n:18090", 52.0, 4.0, ingestOutputs("n.example.com:18090"), "")

	if _, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "cluster-a"), admittedCtx("t1"), "sk"); err == nil {
		t.Fatal("an unattributed node was offered for ingest")
	}
}

// One physical Foghorn serves many virtual media clusters and accepts any
// valid stream key. The candidate set is bounded by what Quartermaster grants
// the stream's owner — the same predicate the serve path uses — not by the
// Foghorn's process cluster and not by a single resolved origin. An owner
// entitled to several clusters gets nodes from all of them ranked together.
func TestResolveIngestEndpoints_RanksAcrossEveryEntitledCluster(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestNode(t, sm, "edge-demo", "http://a:18090", 52.0, 4.0, ingestOutputs("a.example.com:18090"))
	sm.SetNodeConnectionInfo(context.Background(), "edge-demo", "a.example.com:18090", "", "demo-media", nil)
	seedIngestNode(t, sm, "edge-eu", "http://b:18090", 52.0, 4.0, ingestOutputs("b.example.com:18090"))
	sm.SetNodeConnectionInfo(context.Background(), "edge-eu", "b.example.com:18090", "", "eu-media", nil)

	// Foghorn's own cluster is a platform cluster, not either media cluster.
	streamCtx := admittedCtx("t1")
	streamCtx.ClusterPeers = healthyPeers("demo-media", "eu-media")

	resp, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "central-primary"), streamCtx, "sk")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := map[string]bool{resp.GetPrimary().GetClusterId(): true}
	for _, fb := range resp.GetFallbacks() {
		got[fb.GetClusterId()] = true
	}
	if !got["demo-media"] || !got["eu-media"] {
		t.Fatalf("entitled clusters collapsed to %v", got)
	}
}

// A cluster the owner is not entitled to is never offered, however healthy or
// well-scored its nodes are. Entitlement is Quartermaster's cluster↔tenant
// grant, evaluated over the node's authenticated cluster.
func TestResolveIngestEndpoints_ExcludesUnentitledClusters(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestNode(t, sm, "edge-other", "http://o:18090", 52.0, 4.0, ingestOutputs("o.example.com:18090"))
	sm.SetNodeConnectionInfo(context.Background(), "edge-other", "o.example.com:18090", "", "someone-elses-media", nil)

	streamCtx := admittedCtx("t1")
	streamCtx.ClusterPeers = healthyPeers("demo-media")

	if _, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "central-primary"), streamCtx, "sk"); err == nil {
		t.Fatal("a node in an unentitled cluster was offered")
	}
}

// While a publisher holds the stream, a reconnect must go back to the cluster
// already ingesting: anywhere else, PUSH_REWRITE refuses it as a duplicate.
// The live claim therefore collapses the entitled set to one cluster.
func TestResolveIngestEndpoints_LiveClaimPinsToClaimingCluster(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestNode(t, sm, "edge-demo", "http://a:18090", 52.0, 4.0, ingestOutputs("a.example.com:18090"))
	sm.SetNodeConnectionInfo(context.Background(), "edge-demo", "a.example.com:18090", "", "demo-media", nil)
	seedIngestNode(t, sm, "edge-eu", "http://b:18090", 52.0, 4.0, ingestOutputs("b.example.com:18090"))
	sm.SetNodeConnectionInfo(context.Background(), "edge-eu", "b.example.com:18090", "", "eu-media", nil)

	streamCtx := admittedCtx("t1")
	streamCtx.ClusterPeers = healthyPeers("demo-media", "eu-media")
	claimed := "eu-media"
	streamCtx.ActiveIngestClusterId = &claimed

	resp, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "central-primary"), streamCtx, "sk")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := resp.GetPrimary().GetClusterId(); got != "eu-media" {
		t.Fatalf("primary cluster = %q, want the claiming eu-media", got)
	}
	for _, fb := range resp.GetFallbacks() {
		if fb.GetClusterId() != "eu-media" {
			t.Fatalf("fallback escaped the live claim: %q", fb.GetClusterId())
		}
	}
}

// origin_cluster_id must NOT act as the pin: it falls back to the tenant's
// route and is always populated, so treating it as placement would silently
// bind every unclaimed publish to one cluster.
func TestResolveIngestEndpoints_OriginAloneDoesNotPin(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	stubClusterEntitlements(t, map[string][]string{"t1": {"demo-media", "eu-media"}})

	seedIngestNode(t, sm, "edge-eu", "http://b:18090", 52.0, 4.0, ingestOutputs("b.example.com:18090"))
	sm.SetNodeConnectionInfo(context.Background(), "edge-eu", "b.example.com:18090", "", "eu-media", nil)

	streamCtx := admittedCtx("t1")
	streamCtx.ClusterPeers = healthyPeers("demo-media", "eu-media")
	routed := "demo-media" // tenant route, nobody publishing
	streamCtx.OriginClusterId = &routed

	resp, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "central-primary"), streamCtx, "sk")
	if err != nil {
		t.Fatalf("an unclaimed publish was pinned to its tenant route: %v", err)
	}
	if got := resp.GetPrimary().GetClusterId(); got != "eu-media" {
		t.Fatalf("primary cluster = %q, want the other entitled cluster", got)
	}
}

// A degraded default cluster must not sink the publish when another authorized
// cluster is healthy. Commodore drops the degraded one from the envelope, and
// selection ranks whatever is left — the asymmetry that gating one resolved
// cluster would have gotten wrong.
func TestResolveIngestEndpoints_UsesHealthyClusterWhenDefaultIsDegraded(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestNodeInCluster(t, sm, "edge-degraded", "http://a:18090", 52.0, 4.0, ingestOutputs("a.example.com:18090"), "degraded-media")
	seedIngestNodeInCluster(t, sm, "edge-healthy", "http://b:18090", 52.0, 4.0, ingestOutputs("b.example.com:18090"), "healthy-media")

	// The envelope Commodore returns carries only the healthy cluster.
	streamCtx := admittedCtx("t1")
	streamCtx.ClusterPeers = healthyPeers("healthy-media")
	routed := "degraded-media"
	streamCtx.OriginClusterId = &routed

	resp, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "central-primary"), streamCtx, "sk")
	if err != nil {
		t.Fatalf("a healthy authorized cluster was not used: %v", err)
	}
	if got := resp.GetPrimary().GetClusterId(); got != "healthy-media" {
		t.Fatalf("primary cluster = %q, want healthy-media", got)
	}
}

// The mirror case: a cluster the tenant holds a raw Quartermaster grant to,
// but which this tenant's plan or current health excludes, is absent from the
// envelope and must not be offered. Re-deriving entitlement from raw grants
// would have offered it, and PUSH_REWRITE would then have refused the publish.
func TestResolveIngestEndpoints_ExcludesClusterMissingFromEnvelope(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestNodeInCluster(t, sm, "edge-granted", "http://a:18090", 52.0, 4.0, ingestOutputs("a.example.com:18090"), "plan-excluded-media")
	// Raw grant exists — and is deliberately not consulted.
	stubClusterEntitlements(t, map[string][]string{"t1": {"plan-excluded-media"}})

	streamCtx := admittedCtx("t1")
	streamCtx.ClusterPeers = healthyPeers("other-media")

	if _, err := ResolveIngestEndpoints(context.Background(), newIngestDeps(52.0, 4.0, "central-primary"), streamCtx, "sk"); err == nil {
		t.Fatal("a cluster absent from Commodore's envelope was offered")
	}
}
