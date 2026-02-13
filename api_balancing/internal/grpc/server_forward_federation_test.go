package grpc

import (
	"context"
	"fmt"
	"io"
	"testing"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
)

type mockFedRPC struct {
	calls    []forwardCall
	handlers map[string]bool  // clusterID → handled
	errors   map[string]error // clusterID → error
}

type forwardCall struct {
	clusterID    string
	addr         string
	command      string
	artifactHash string
	tenantID     string
	streamID     string
}

func (m *mockFedRPC) QueryStream(context.Context, string, string, *pb.QueryStreamRequest) (*pb.QueryStreamResponse, error) {
	return nil, nil
}
func (m *mockFedRPC) NotifyOriginPull(context.Context, string, string, *pb.OriginPullNotification) (*pb.OriginPullAck, error) {
	return nil, nil
}
func (m *mockFedRPC) PrepareArtifact(context.Context, string, string, *pb.PrepareArtifactRequest) (*pb.PrepareArtifactResponse, error) {
	return nil, nil
}
func (m *mockFedRPC) ForwardArtifactCommand(_ context.Context, clusterID, addr string, req *pb.ForwardArtifactCommandRequest) (*pb.ForwardArtifactCommandResponse, error) {
	m.calls = append(m.calls, forwardCall{
		clusterID:    clusterID,
		addr:         addr,
		command:      req.GetCommand(),
		artifactHash: req.GetArtifactHash(),
		tenantID:     req.GetTenantId(),
		streamID:     req.GetStreamId(),
	})
	if err, ok := m.errors[clusterID]; ok && err != nil {
		return nil, err
	}
	handled := false
	if m.handlers != nil {
		handled = m.handlers[clusterID]
	}
	return &pb.ForwardArtifactCommandResponse{Handled: handled}, nil
}

type mockPeerResolver struct {
	peers map[string]string // clusterID → addr
}

func (m *mockPeerResolver) GetPeerAddr(id string) string { return m.peers[id] }
func (m *mockPeerResolver) GetPeers() map[string]string  { return m.peers }

func newTestFoghornLogger() logging.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

func TestForwardArtifact_NilFederationClient(t *testing.T) {
	srv := &FoghornGRPCServer{
		logger:      newTestFoghornLogger(),
		peerManager: &mockPeerResolver{peers: map[string]string{"peer-1": "addr-1"}},
		// federationClient is nil
	}
	handled, err := srv.forwardArtifactToFederation(context.Background(), "delete_clip", "hash-1", "tenant-a", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Fatal("expected handled=false with nil federation client")
	}
}

func TestForwardArtifact_NilPeerManager(t *testing.T) {
	srv := &FoghornGRPCServer{
		logger:           newTestFoghornLogger(),
		federationClient: &mockFedRPC{},
		// peerManager is nil
	}
	handled, err := srv.forwardArtifactToFederation(context.Background(), "delete_clip", "hash-1", "tenant-a", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Fatal("expected handled=false with nil peer manager")
	}
}

func TestForwardArtifact_NoPeers(t *testing.T) {
	srv := &FoghornGRPCServer{
		logger:           newTestFoghornLogger(),
		federationClient: &mockFedRPC{},
		peerManager:      &mockPeerResolver{peers: map[string]string{}},
	}
	handled, err := srv.forwardArtifactToFederation(context.Background(), "delete_clip", "hash-1", "tenant-a", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Fatal("expected handled=false with no peers")
	}
}

func TestForwardArtifact_SkipsSelfCluster(t *testing.T) {
	fed := &mockFedRPC{handlers: map[string]bool{"self-cluster": true}}
	srv := &FoghornGRPCServer{
		logger:           newTestFoghornLogger(),
		federationClient: fed,
		peerManager:      &mockPeerResolver{peers: map[string]string{"self-cluster": "self-addr"}},
		clusterID:        "self-cluster",
	}
	handled, err := srv.forwardArtifactToFederation(context.Background(), "delete_clip", "hash-1", "tenant-a", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Fatal("expected handled=false when only self in peers")
	}
	if len(fed.calls) != 0 {
		t.Fatalf("expected no RPC calls (self should be skipped), got %d", len(fed.calls))
	}
}

func TestForwardArtifact_FirstPeerHandles(t *testing.T) {
	fed := &mockFedRPC{handlers: map[string]bool{"peer-1": true, "peer-2": true}}
	srv := &FoghornGRPCServer{
		logger:           newTestFoghornLogger(),
		federationClient: fed,
		peerManager: &mockPeerResolver{peers: map[string]string{
			"peer-1": "addr-1",
			"peer-2": "addr-2",
		}},
		clusterID: "self",
	}
	handled, err := srv.forwardArtifactToFederation(context.Background(), "delete_clip", "hash-1", "tenant-a", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	// Map iteration is non-deterministic, but at least one call was made and it succeeded
	if len(fed.calls) < 1 {
		t.Fatal("expected at least one RPC call")
	}
}

func TestForwardArtifact_NoPeerHandles(t *testing.T) {
	fed := &mockFedRPC{handlers: map[string]bool{"peer-1": false, "peer-2": false}}
	srv := &FoghornGRPCServer{
		logger:           newTestFoghornLogger(),
		federationClient: fed,
		peerManager: &mockPeerResolver{peers: map[string]string{
			"peer-1": "addr-1",
			"peer-2": "addr-2",
		}},
		clusterID: "self",
	}
	handled, err := srv.forwardArtifactToFederation(context.Background(), "delete_clip", "hash-1", "tenant-a", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Fatal("expected handled=false when no peer handles")
	}
	if len(fed.calls) != 2 {
		t.Fatalf("expected 2 RPC calls (both peers), got %d", len(fed.calls))
	}
}

func TestForwardArtifact_PeerError_ContinuesToNext(t *testing.T) {
	fed := &mockFedRPC{
		handlers: map[string]bool{"peer-ok": true},
		errors:   map[string]error{"peer-err": fmt.Errorf("connection refused")},
	}
	// Use a single-entry peer map for deterministic behavior with error peer
	srv := &FoghornGRPCServer{
		logger:           newTestFoghornLogger(),
		federationClient: fed,
		peerManager: &mockPeerResolver{peers: map[string]string{
			"peer-err": "addr-err",
			"peer-ok":  "addr-ok",
		}},
		clusterID: "self",
	}
	handled, err := srv.forwardArtifactToFederation(context.Background(), "delete_clip", "hash-1", "tenant-a", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true (peer-ok should handle after peer-err fails)")
	}
}

func TestForwardArtifact_PassesCorrectRequest(t *testing.T) {
	fed := &mockFedRPC{handlers: map[string]bool{"peer-1": true}}
	srv := &FoghornGRPCServer{
		logger:           newTestFoghornLogger(),
		federationClient: fed,
		peerManager: &mockPeerResolver{peers: map[string]string{
			"peer-1": "addr-1",
		}},
		clusterID: "self",
	}
	handled, err := srv.forwardArtifactToFederation(context.Background(), "stop_dvr", "dvr-hash-42", "tenant-x", "stream-99")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	if len(fed.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fed.calls))
	}
	call := fed.calls[0]
	if call.clusterID != "peer-1" {
		t.Fatalf("expected clusterID=peer-1, got %q", call.clusterID)
	}
	if call.addr != "addr-1" {
		t.Fatalf("expected addr=addr-1, got %q", call.addr)
	}
	if call.command != "stop_dvr" {
		t.Fatalf("expected command=stop_dvr, got %q", call.command)
	}
	if call.artifactHash != "dvr-hash-42" {
		t.Fatalf("expected artifactHash=dvr-hash-42, got %q", call.artifactHash)
	}
	if call.tenantID != "tenant-x" {
		t.Fatalf("expected tenantID=tenant-x, got %q", call.tenantID)
	}
	if call.streamID != "stream-99" {
		t.Fatalf("expected streamID=stream-99, got %q", call.streamID)
	}
}

func TestForwardArtifact_SkipsWhenTenantMissing(t *testing.T) {
	fed := &mockFedRPC{handlers: map[string]bool{"peer-1": true}}
	srv := &FoghornGRPCServer{
		logger:           newTestFoghornLogger(),
		federationClient: fed,
		peerManager: &mockPeerResolver{peers: map[string]string{
			"peer-1": "addr-1",
		}},
		clusterID: "self",
	}

	handled, err := srv.forwardArtifactToFederation(context.Background(), "delete_clip", "clip-hash-1", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Fatal("expected handled=false when tenant is missing")
	}
	if len(fed.calls) != 0 {
		t.Fatalf("expected no federation calls when tenant missing, got %d", len(fed.calls))
	}
}
