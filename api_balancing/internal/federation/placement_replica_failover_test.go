package federation

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// replicaPool dials each replica address with its own real client, as the
// Foghorn pool does, so a dead replica fails at the transport.
type replicaPool struct {
	t       *testing.T
	mu      sync.Mutex
	clients map[string]*foghorn.GRPCClient
	dialed  []string
}

func (pool *replicaPool) GetOrCreate(_, addr string) (*foghorn.GRPCClient, error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.dialed = append(pool.dialed, addr)
	if client := pool.clients[addr]; client != nil {
		return client, nil
	}
	client, err := foghorn.NewGRPCClient(foghorn.GRPCConfig{GRPCAddr: addr, ServiceToken: "placement-test-service", AllowInsecure: true, Logger: newFederationTestLogger()})
	if err != nil {
		return nil, err
	}
	pool.t.Cleanup(func() { _ = client.Close() })
	pool.clients[addr] = client
	return client, nil
}

// Only an unreachable replica moves the call to the next one. Any answer,
// including a refusal, is the cell's answer; asking another replica could
// only repeat or contradict it.
func TestCallCellReplicasFailsOverOnlyOnUnavailable(t *testing.T) {
	var tried []string
	call := func(answers map[string]error) func(context.Context, string) (string, error) {
		tried = nil
		return func(_ context.Context, addr string) (string, error) {
			tried = append(tried, addr)
			if err := answers[addr]; err != nil {
				return "", err
			}
			return addr, nil
		}
	}
	got, answered, err := callCellReplicas(context.Background(), []string{"a", "b"}, call(map[string]error{"a": status.Error(codes.Unavailable, "connection refused")}))
	if err != nil || got != "b" || answered != "b" || len(tried) != 2 {
		t.Fatalf("unavailable replica: %q from %q, %v, tried %v", got, answered, err, tried)
	}
	_, _, err = callCellReplicas(context.Background(), []string{"a", "b"}, call(map[string]error{"a": status.Error(codes.PermissionDenied, "refused")}))
	if status.Code(err) != codes.PermissionDenied || len(tried) != 1 {
		t.Fatalf("a refusal was retried on another replica: %v, tried %v", err, tried)
	}
	_, _, err = callCellReplicas(context.Background(), []string{"a", "b"}, call(map[string]error{
		"a": status.Error(codes.Unavailable, "connection refused"), "b": status.Error(codes.Unavailable, "connection refused"),
	}))
	if status.Code(err) != codes.Unavailable || len(tried) != 2 {
		t.Fatalf("every replica down: %v, tried %v", err, tried)
	}
}

// A remote cell is addressed through every healthy Foghorn replica that
// Quartermaster lists. With the first replica dead, preparation succeeds on
// the second instead of failing the request for the whole cell.
func TestRemotePreparationFailsOverToAnotherCellReplica(t *testing.T) {
	f := newDiscoveryFixture(t)
	var prepares atomic.Int32
	destination := placementRPCFixture{
		query: f.discovery.QueryPlacementCandidates,
		prepare: func(_ context.Context, req *placementpb.PreparePlacementRequest) (*placementpb.Preparation, error) {
			prepares.Add(1)
			return preparationWireResponse(req), nil
		},
	}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken: "placement-test-service", MetadataPolicy: middleware.MetadataPolicyDeny,
	})))
	NewFederationServer(FederationServerConfig{ClusterID: "us-registry-cluster", ControlCellID: "us-cell", Placement: destination, AllowFederationMutations: true}).RegisterServices(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	dead, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.Addr().String()
	_ = dead.Close()
	liveAddr := listener.Addr().String()

	peerCache, _ := setupTestCache(t)
	peers := newTestPeerManager(t, "eu-registry-cluster", peerCache, false)
	// Quartermaster lists both replicas; the peer channel address stays the first.
	peers.applyQuartermasterPeers(&quartermasterpb.ListPeersResponse{Peers: []*quartermasterpb.PeerCluster{{
		ClusterId: "us-virtual", ControlCellId: "us-cell", FoghornAddr: deadAddr, FoghornAddrs: []string{deadAddr, liveAddr},
	}}})
	if got := peers.GetControlCellAddrs("us-cell"); len(got) != 2 || got[0] != deadAddr || got[1] != liveAddr {
		t.Fatalf("control cell replicas = %v, want [%s %s]", got, deadAddr, liveAddr)
	}
	if got := peers.GetPeerAddr("us-virtual"); got != deadAddr {
		t.Fatalf("peer channel address = %s, want the stable first replica %s", got, deadAddr)
	}

	pool := &replicaPool{t: t, clients: make(map[string]*foghorn.GRPCClient)}
	transport := PlacementTransport{LocalCellID: "eu-cell", Client: NewFederationClient(FederationClientConfig{Pool: pool}), CellAddresses: peers.GetControlCellAddrs}
	cell := balancer.PlacementCell{ID: "us-cell", ClusterIDs: []string{"empty", "us"}}
	route := balancer.PlacementRouteRequest{TenantID: f.query.TenantId, ObjectID: f.query.ObjectId, InternalName: f.query.InternalName,
		Verb: placement.Serve, Protocol: "hls", PolicyDigest: f.query.PolicyDigest, PolicyRevision: f.query.PolicyRevision,
		ParentRevision: f.query.ParentRevision, SourceGeneration: f.query.SourceGeneration}
	attempt := balancer.PlacementPreparationRequest{Route: route, Choice: placement.Choice{ClusterID: "us", NodeID: "node-02"},
		AttemptID: testPreparationAttempt(t), ExpiresAt: time.Now().Add(5 * time.Second)}

	prepared, err := transport.Prepare(context.Background(), cell, attempt)
	if err != nil || prepared.Outcome != balancer.PlacementAccepted || prepared.NodeID != "node-02" || prepares.Load() != 1 {
		t.Fatalf("preparation with a dead first replica = %+v, %v (prepares=%d)", prepared, err, prepares.Load())
	}
	pool.mu.Lock()
	dialed := append([]string(nil), pool.dialed...)
	pool.mu.Unlock()
	if len(dialed) != 2 || dialed[0] != deadAddr || dialed[1] != liveAddr {
		t.Fatalf("replicas tried = %v, want the dead one then the live one", dialed)
	}
	observed, err := transport.Observe(context.Background(), cell, route)
	if err != nil || len(observed.Candidates) != 12 {
		t.Fatalf("discovery with a dead first replica = %d candidates, %v", len(observed.Candidates), err)
	}
}
