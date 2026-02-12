package handlers

import (
	"context"
	"fmt"
	"net"
	"testing"

	"frameworks/api_balancing/internal/federation"
	"frameworks/pkg/clients/foghorn"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

// stubFederationService is a controllable FoghornFederation gRPC service for tests.
type stubFederationService struct {
	pb.UnimplementedFoghornFederationServer
	queryResponse *pb.QueryStreamResponse
	queryErr      error
}

func (s *stubFederationService) QueryStream(_ context.Context, _ *pb.QueryStreamRequest) (*pb.QueryStreamResponse, error) {
	return s.queryResponse, s.queryErr
}

// mockPeerResolver satisfies peerAddrResolver for tests.
type mockPeerResolver struct {
	addrs map[string]string
}

func (m *mockPeerResolver) GetPeerAddr(clusterID string) string {
	return m.addrs[clusterID]
}

func (m *mockPeerResolver) GetPeerGeo(_ string) (float64, float64) {
	return 0, 0
}

// setupFederationTestDeps starts a stub gRPC server, creates a FederationClient backed
// by a FoghornPool pointing at it, and returns a mock peer resolver. Cleanup stops everything.
func setupFederationTestDeps(t *testing.T, stub *stubFederationService, peerClusterID string) (
	*federation.FederationClient, *mockPeerResolver,
) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	server := grpc.NewServer()
	pb.RegisterFoghornFederationServer(server, stub)
	go func() { _ = server.Serve(listener) }()

	log := logging.Logger(logrus.New())
	pool := foghorn.NewPool(foghorn.PoolConfig{Logger: log})

	client := federation.NewFederationClient(federation.FederationClientConfig{
		Pool:   pool,
		Logger: log,
	})

	resolver := &mockPeerResolver{
		addrs: map[string]string{peerClusterID: listener.Addr().String()},
	}

	t.Cleanup(func() {
		_ = pool.Close()
		server.Stop()
		_ = listener.Close()
	})

	return client, resolver
}

// saveFederationGlobals saves and restores handler-level globals used by confirmRemoteStream.
func saveFederationGlobals(t *testing.T) {
	t.Helper()
	origClient := federationClient
	origPeer := peerManager
	origCluster := clusterID
	t.Cleanup(func() {
		federationClient = origClient
		peerManager = origPeer
		clusterID = origCluster
	})
}

func TestConfirmRemoteStream_WithCandidates(t *testing.T) {
	saveFederationGlobals(t)

	stub := &stubFederationService{
		queryResponse: &pb.QueryStreamResponse{
			Candidates: []*pb.EdgeCandidate{
				{NodeId: "node-1", BaseUrl: "edge1.example.com", BwScore: 100},
			},
		},
	}

	client, resolver := setupFederationTestDeps(t, stub, "remote-cluster-1")
	federationClient = client
	peerManager = resolver
	clusterID = "local-cluster"

	got := confirmRemoteStream(context.Background(), "remote-cluster-1", "test-stream", "tenant-1", 40.0, -74.0)
	if !got {
		t.Error("expected confirmRemoteStream to return true when candidates exist")
	}
}

func TestConfirmRemoteStream_NoCandidates(t *testing.T) {
	saveFederationGlobals(t)

	stub := &stubFederationService{
		queryResponse: &pb.QueryStreamResponse{
			Candidates: []*pb.EdgeCandidate{},
		},
	}

	client, resolver := setupFederationTestDeps(t, stub, "remote-cluster-1")
	federationClient = client
	peerManager = resolver
	clusterID = "local-cluster"

	got := confirmRemoteStream(context.Background(), "remote-cluster-1", "test-stream", "tenant-1", 40.0, -74.0)
	if got {
		t.Error("expected confirmRemoteStream to return false when no candidates")
	}
}

func TestConfirmRemoteStream_RPCError(t *testing.T) {
	saveFederationGlobals(t)

	stub := &stubFederationService{
		queryErr: fmt.Errorf("unavailable"),
	}

	client, resolver := setupFederationTestDeps(t, stub, "remote-cluster-1")
	federationClient = client
	peerManager = resolver
	clusterID = "local-cluster"

	got := confirmRemoteStream(context.Background(), "remote-cluster-1", "test-stream", "tenant-1", 40.0, -74.0)
	if got {
		t.Error("expected confirmRemoteStream to return false on RPC error")
	}
}

func TestConfirmRemoteStream_NilDeps(t *testing.T) {
	saveFederationGlobals(t)

	federationClient = nil
	peerManager = nil

	got := confirmRemoteStream(context.Background(), "remote-cluster-1", "test-stream", "tenant-1", 40.0, -74.0)
	if got {
		t.Error("expected confirmRemoteStream to return false when deps are nil")
	}
}

func TestConfirmRemoteStream_UnknownPeer(t *testing.T) {
	saveFederationGlobals(t)

	stub := &stubFederationService{
		queryResponse: &pb.QueryStreamResponse{
			Candidates: []*pb.EdgeCandidate{
				{NodeId: "node-1", BaseUrl: "edge1.example.com", BwScore: 100},
			},
		},
	}

	client, resolver := setupFederationTestDeps(t, stub, "remote-cluster-1")
	federationClient = client
	peerManager = resolver
	clusterID = "local-cluster"

	got := confirmRemoteStream(context.Background(), "unknown-cluster", "test-stream", "tenant-1", 40.0, -74.0)
	if got {
		t.Error("expected confirmRemoteStream to return false for unknown peer")
	}
}
