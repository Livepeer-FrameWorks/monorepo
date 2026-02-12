package federation

import (
	"context"
	"io"
	"testing"

	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/ctxkeys"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func newFederationTestLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

func serviceAuthContext() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
}

func TestQueryStream_RequiresServiceAuthAndTenant(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{Logger: newFederationTestLogger(), ClusterID: "cluster-a"})

	_, err := srv.QueryStream(context.Background(), &pb.QueryStreamRequest{
		StreamName:        "stream-1",
		RequestingCluster: "cluster-b",
		TenantId:          "tenant-a",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected permission denied for non-service auth, got %v", err)
	}

	_, err = srv.QueryStream(serviceAuthContext(), &pb.QueryStreamRequest{
		StreamName:        "stream-1",
		RequestingCluster: "cluster-b",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid argument when tenant missing, got %v", err)
	}
}

func TestNotifyOriginPull_RejectsTenantMismatch(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	nodeID := "node-1"
	streamName := "stream-1"
	tenantID := "tenant-a"

	sm.SetNodeInfo(nodeID, nodeID, true, nil, nil, "", "", nil)
	if err := sm.UpdateStreamFromBuffer(streamName, streamName, nodeID, tenantID, "READY", ""); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}

	srv := NewFederationServer(FederationServerConfig{Logger: newFederationTestLogger(), ClusterID: "cluster-a"})
	resp, err := srv.NotifyOriginPull(serviceAuthContext(), &pb.OriginPullNotification{
		StreamName:    streamName,
		SourceNodeId:  nodeID,
		DestClusterId: "cluster-b",
		DestNodeId:    "dest-node",
		TenantId:      "tenant-b",
	})
	if err != nil {
		t.Fatalf("NotifyOriginPull returned error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatalf("expected pull to be rejected for tenant mismatch")
	}
}

type mockPeerChannelStream struct {
	ctx  context.Context
	msgs []*pb.PeerMessage
	idx  int
}

func (m *mockPeerChannelStream) Context() context.Context     { return m.ctx }
func (m *mockPeerChannelStream) SetHeader(metadata.MD) error  { return nil }
func (m *mockPeerChannelStream) SendHeader(metadata.MD) error { return nil }
func (m *mockPeerChannelStream) SetTrailer(metadata.MD)       {}
func (m *mockPeerChannelStream) Send(*pb.PeerMessage) error   { return nil }
func (m *mockPeerChannelStream) SendMsg(any) error            { return nil }
func (m *mockPeerChannelStream) RecvMsg(any) error            { return nil }

func (m *mockPeerChannelStream) Recv() (*pb.PeerMessage, error) {
	if m.idx >= len(m.msgs) {
		return nil, io.EOF
	}
	msg := m.msgs[m.idx]
	m.idx++
	return msg, nil
}

func TestPeerChannel_RejectsClusterIDMismatch(t *testing.T) {
	cache, _ := setupTestCache(t)
	srv := NewFederationServer(FederationServerConfig{Logger: newFederationTestLogger(), ClusterID: "cluster-a", Cache: cache})
	stream := &mockPeerChannelStream{
		ctx: serviceAuthContext(),
		msgs: []*pb.PeerMessage{
			{ClusterId: "cluster-b", Payload: &pb.PeerMessage_PeerHeartbeat{PeerHeartbeat: &pb.PeerHeartbeat{}}},
			{ClusterId: "cluster-c", Payload: &pb.PeerMessage_PeerHeartbeat{PeerHeartbeat: &pb.PeerHeartbeat{}}},
		},
	}

	err := srv.PeerChannel(stream)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected permission denied on cluster mismatch, got %v", err)
	}
}

func TestPeerChannel_RequiresInitialClusterID(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{Logger: newFederationTestLogger(), ClusterID: "cluster-a"})
	stream := &mockPeerChannelStream{
		ctx: serviceAuthContext(),
		msgs: []*pb.PeerMessage{
			{ClusterId: "", Payload: &pb.PeerMessage_PeerHeartbeat{PeerHeartbeat: &pb.PeerHeartbeat{}}},
		},
	}

	err := srv.PeerChannel(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid argument for missing initial cluster id, got %v", err)
	}
}

func TestPeerChannel_RejectsNonServiceAuth(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{Logger: newFederationTestLogger(), ClusterID: "cluster-a"})
	stream := &mockPeerChannelStream{ctx: context.Background()}
	err := srv.PeerChannel(stream)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected permission denied, got %v", err)
	}
}
