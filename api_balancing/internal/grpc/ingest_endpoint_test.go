package grpc

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	commodorecli "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// commodoreIngestFake doubles Commodore's InternalService for the ingest
// resolver, counting calls to the claiming RPC so the no-lease invariant is
// enforced on the gRPC surface too.
type commodoreIngestFake struct {
	commodorepb.UnimplementedInternalServiceServer

	streamContext   func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error)
	streamCtxHits   atomic.Int32
	validateKeyHits atomic.Int32
	lastRequest     atomic.Pointer[commodorepb.ResolveStreamContextRequest]
}

func (f *commodoreIngestFake) ResolveStreamContext(ctx context.Context, req *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
	f.streamCtxHits.Add(1)
	f.lastRequest.Store(req)
	if f.streamContext != nil {
		return f.streamContext(ctx, req)
	}
	return &commodorepb.ResolveStreamContextResponse{Admitted: true, ClusterPeers: ingestTestPeers()}, nil
}

type localIngestResolverFunc func(context.Context, string) (*commodorepb.ResolveStreamContextResponse, bool, error)

func (resolve localIngestResolverFunc) ResolveLocalIngestContext(ctx context.Context, streamKey string) (*commodorepb.ResolveStreamContextResponse, bool, error) {
	return resolve(ctx, streamKey)
}

func (f *commodoreIngestFake) ValidateStreamKey(ctx context.Context, req *commodorepb.ValidateStreamKeyRequest) (*commodorepb.ValidateStreamKeyResponse, error) {
	f.validateKeyHits.Add(1)
	return &commodorepb.ValidateStreamKeyResponse{Valid: true}, nil
}

func startCommodoreIngestFake(t *testing.T, fake *commodoreIngestFake) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := commodorecli.NewGRPCClient(commodorecli.GRPCConfig{
		GRPCAddr:      lis.Addr().String(),
		AllowInsecure: true,
		Logger:        logging.NewLogger(),
		Timeout:       5 * time.Second,
	})
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("commodore client: %v", err)
	}

	prev := control.CommodoreClient
	control.CommodoreClient = client
	t.Cleanup(func() {
		control.CommodoreClient = prev
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
}

func seedGRPCIngestNode(t *testing.T, sm *state.StreamStateManager, nodeID, host string) {
	t.Helper()
	lat, lon := 52.0, 4.0
	sm.SetNodeInfo(nodeID, "http://"+host, true, &lat, &lon, "loc-"+nodeID, "",
		map[string]any{"HLS": "http://" + host + "/hls/$/index.m3u8"})
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
	// Selection authorizes a node's authenticated virtual media cluster against
	// the cluster-peer envelope on the resolve response, so responses in these
	// tests carry ingestTestCluster (see ingestTestPeers). The entitlement rules
	// themselves are covered in control.
	sm.SetNodeConnectionInfo(context.Background(), nodeID, nodeID+":18090", "", ingestTestCluster, nil)
	sm.SetProbeVerified(nodeID, true)
}

func newIngestGRPCServer() *FoghornGRPCServer {
	return &FoghornGRPCServer{
		logger:    logging.NewLogger(),
		lb:        balancer.NewLoadBalancer(logging.NewLogger()),
		clusterID: "cluster-1",
	}
}

func TestResolveIngestEndpoint_RequiresStreamKey(t *testing.T) {
	srv := newIngestGRPCServer()
	_, err := srv.ResolveIngestEndpoint(context.Background(), &sharedpb.IngestEndpointRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

// The happy path returns full metadata: Commodore's finalizeIngestResponse is
// the trust boundary on this path and strips owner-only fields for non-owners.
func TestResolveIngestEndpoint_HappyPath(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedGRPCIngestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	fake := &commodoreIngestFake{streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
		return &commodorepb.ResolveStreamContextResponse{
			Admitted:           true,
			StreamId:           "stream-1",
			InternalName:       "internal-1",
			TenantId:           "tenant-1",
			IngestMode:         "push",
			IsRecordingEnabled: true,
			ClusterPeers:       ingestTestPeers(),
		}, nil
	}}
	startCommodoreIngestFake(t, fake)

	srv := newIngestGRPCServer()
	resp, err := srv.ResolveIngestEndpoint(context.Background(), &sharedpb.IngestEndpointRequest{StreamKey: "sk-abc"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resp.GetPrimary().GetNodeId() != "ingest-a" {
		t.Errorf("primary: got %q", resp.GetPrimary().GetNodeId())
	}
	if got, want := resp.GetPrimary().GetWhipUrl(), "http://ingest-a.example.com:18090/webrtc/sk-abc"; got != want {
		t.Errorf("whip: got %q want %q", got, want)
	}
	if resp.GetPrimary().GetKind() != sharedpb.IngestEndpointKind_INGEST_ENDPOINT_KIND_NODE_SPECIFIC {
		t.Errorf("kind: got %v", resp.GetPrimary().GetKind())
	}
	if resp.GetPrimary().GetClusterId() != "cluster-1" {
		t.Errorf("cluster: got %q", resp.GetPrimary().GetClusterId())
	}
	md := resp.GetMetadata()
	if md.GetStreamId() != "stream-1" || md.GetTenantId() != "tenant-1" || md.GetStreamKey() != "sk-abc" {
		t.Errorf("metadata: %+v", md)
	}

	// The resolver must reach Commodore by stream key and never take the lease.
	//
	// It must also declare no cluster. This Foghorn serves several media
	// clusters, so its own CLUSTER_ID does not name where the publish goes;
	// sending it submits an infrastructure cluster to a gate that asks whether
	// the tenant may publish there, denying every publish for tenants not
	// entitled to it. Commodore resolves the target and returns it as origin.
	if req := fake.lastRequest.Load(); req.GetStreamKey() != "sk-abc" || req.GetClusterId() != "" {
		t.Errorf("unexpected stream context request: %+v", req)
	}
	if hits := fake.validateKeyHits.Load(); hits != 0 {
		t.Errorf("ValidateStreamKey called %d times; resolution must not claim the ingest lease", hits)
	}
}

func TestResolveIngestEndpoint_InvalidKeyIsNotFound(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedGRPCIngestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	startCommodoreIngestFake(t, &commodoreIngestFake{
		streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			return &commodorepb.ResolveStreamContextResponse{
				Admitted:        false,
				IngestMode:      "push",
				RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY,
			}, nil
		},
	})

	srv := newIngestGRPCServer()
	_, err := srv.ResolveIngestEndpoint(context.Background(), &sharedpb.IngestEndpointRequest{StreamKey: "bogus"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("want NotFound, got %v", err)
	}
}

// Commodore fails closed as transient (Purser/Quartermaster unreachable), and
// that must surface as Unavailable rather than a denial or a stale success.
func TestResolveIngestEndpoint_CommodoreErrorIsUnavailable(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedGRPCIngestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	startCommodoreIngestFake(t, &commodoreIngestFake{
		streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			return nil, status.Error(codes.Unavailable, "billing status lookup failed")
		},
	})

	srv := newIngestGRPCServer()
	_, err := srv.ResolveIngestEndpoint(context.Background(), &sharedpb.IngestEndpointRequest{StreamKey: "sk-abc"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("want Unavailable, got %v", err)
	}
}

func TestResolveIngestEndpoint_ReadyLocalAuthorityFallsBackAfterConnectedAttempt(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedGRPCIngestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	fake := &commodoreIngestFake{streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
		return nil, status.Error(codes.Unavailable, "connected placement unavailable")
	}}
	startCommodoreIngestFake(t, fake)

	srv := newIngestGRPCServer()
	srv.localIngestResolver = localIngestResolverFunc(func(_ context.Context, streamKey string) (*commodorepb.ResolveStreamContextResponse, bool, error) {
		if streamKey != "sk-local" {
			t.Fatalf("local resolver key = %q", streamKey)
		}
		return &commodorepb.ResolveStreamContextResponse{
			Admitted: true, StreamId: "stream-local", InternalName: "internal-local", TenantId: "tenant-local",
			IngestMode: "push", OriginClusterId: proto.String(ingestTestCluster), ClusterPeers: ingestTestPeers(),
		}, true, nil
	})

	resp, err := srv.ResolveIngestEndpoint(context.Background(), &sharedpb.IngestEndpointRequest{StreamKey: "sk-local"})
	if err != nil {
		t.Fatalf("resolve local ingest: %v", err)
	}
	if resp.GetPrimary().GetNodeId() != "ingest-a" {
		t.Fatalf("local primary = %q", resp.GetPrimary().GetNodeId())
	}
	if hits := fake.streamCtxHits.Load(); hits != 1 {
		t.Fatalf("ready local authority made %d connected placement calls, want 1 before outage fallback", hits)
	}
}

func TestResolveIngestEndpoint_ConnectedClaimOverridesSignedOutageOwner(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedGRPCIngestNode(t, sm, "ingest-b", "ingest-b.example.com:18090")

	fake := &commodoreIngestFake{streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
		return &commodorepb.ResolveStreamContextResponse{
			Admitted: true, StreamId: "stream-claimed", InternalName: "internal-claimed", TenantId: "tenant-claimed",
			IngestMode: "push", OriginClusterId: proto.String("cluster-origin"), ActiveIngestClusterId: proto.String(ingestTestCluster),
			ClusterPeers: ingestTestPeers(),
		}, nil
	}}
	startCommodoreIngestFake(t, fake)

	srv := newIngestGRPCServer()
	srv.localIngestResolver = localIngestResolverFunc(func(context.Context, string) (*commodorepb.ResolveStreamContextResponse, bool, error) {
		return &commodorepb.ResolveStreamContextResponse{
			Admitted: true, StreamId: "stream-claimed", InternalName: "internal-claimed", TenantId: "tenant-claimed",
			IngestMode: "push", OriginClusterId: proto.String("cluster-origin"),
			ClusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "cluster-origin", ClusterType: "edge", HealthStatus: "healthy"}},
		}, true, nil
	})
	resp, err := srv.ResolveIngestEndpoint(context.Background(), &sharedpb.IngestEndpointRequest{StreamKey: "sk-claimed"})
	if err != nil {
		t.Fatalf("resolve claimed placement: %v", err)
	}
	if resp.GetPrimary().GetClusterId() != ingestTestCluster || resp.GetPrimary().GetNodeId() != "ingest-b" {
		t.Fatalf("connected claim did not override outage owner: %+v", resp.GetPrimary())
	}
	if fake.streamCtxHits.Load() != 1 {
		t.Fatalf("connected placement calls = %d, want 1", fake.streamCtxHits.Load())
	}
}

func TestResolveIngestEndpoint_LocalStoreErrorUsesConnectedCommodore(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedGRPCIngestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	fake := &commodoreIngestFake{streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
		return &commodorepb.ResolveStreamContextResponse{
			Admitted: true, StreamId: "stream-connected", InternalName: "internal-connected", TenantId: "tenant-connected",
			IngestMode: "push", ClusterPeers: ingestTestPeers(),
		}, nil
	}}
	startCommodoreIngestFake(t, fake)

	srv := newIngestGRPCServer()
	srv.localIngestResolver = localIngestResolverFunc(func(context.Context, string) (*commodorepb.ResolveStreamContextResponse, bool, error) {
		return nil, true, errors.New("local database unavailable")
	})
	resp, err := srv.ResolveIngestEndpoint(context.Background(), &sharedpb.IngestEndpointRequest{StreamKey: "sk-connected"})
	if err != nil {
		t.Fatalf("connected fallback after local store error: %v", err)
	}
	if resp.GetPrimary().GetNodeId() != "ingest-a" || fake.streamCtxHits.Load() != 1 {
		t.Fatalf("connected fallback response=%+v hits=%d", resp, fake.streamCtxHits.Load())
	}
}

func TestResolveIngestEndpoint_NoIngestNodesIsUnavailable(t *testing.T) {
	state.ResetDefaultManagerForTests()

	startCommodoreIngestFake(t, &commodoreIngestFake{
		streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			return &commodorepb.ResolveStreamContextResponse{Admitted: true, IngestMode: "push", TenantId: "t1"}, nil
		},
	})

	srv := newIngestGRPCServer()
	_, err := srv.ResolveIngestEndpoint(context.Background(), &sharedpb.IngestEndpointRequest{StreamKey: "sk-abc"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("want Unavailable, got %v", err)
	}
}

// ingestTestCluster is the virtual media cluster seeded ingest nodes belong to.
const ingestTestCluster = "cluster-1"

// ingestTestPeers is the envelope Commodore returns: plan-filtered and healthy,
// which is what makes membership alone sufficient to authorize a candidate.
func ingestTestPeers() []*clusterpeerpb.TenantClusterPeer {
	return []*clusterpeerpb.TenantClusterPeer{{ClusterId: ingestTestCluster, ClusterType: "edge", HealthStatus: "healthy"}}
}
