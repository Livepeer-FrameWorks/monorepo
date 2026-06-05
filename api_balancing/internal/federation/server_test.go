package federation

import (
	"context"
	"io"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type testPeerChannelServerStream struct {
	ctx      context.Context
	messages []*foghornfederationpb.PeerMessage
	idx      int
}

func (s *testPeerChannelServerStream) Send(*foghornfederationpb.PeerMessage) error { return nil }

func (s *testPeerChannelServerStream) Recv() (*foghornfederationpb.PeerMessage, error) {
	if s.idx >= len(s.messages) {
		return nil, io.EOF
	}
	msg := s.messages[s.idx]
	s.idx++
	return msg, nil
}

func (s *testPeerChannelServerStream) SetHeader(metadata.MD) error { return nil }

func (s *testPeerChannelServerStream) SendHeader(metadata.MD) error { return nil }

func (s *testPeerChannelServerStream) SetTrailer(metadata.MD) {}

func (s *testPeerChannelServerStream) Context() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

func (s *testPeerChannelServerStream) SendMsg(any) error { return nil }

func (s *testPeerChannelServerStream) RecvMsg(any) error { return nil }

func TestPeerChannel_RejectsEmptyClusterID(t *testing.T) {
	cache, _ := setupTestCache(t)
	srv := NewFederationServer(FederationServerConfig{
		Logger:    testLogger(),
		ClusterID: "cluster-a",
		Cache:     cache,
	})

	svcCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	err := srv.PeerChannel(&testPeerChannelServerStream{
		ctx: svcCtx,
		messages: []*foghornfederationpb.PeerMessage{{
			ClusterId: "",
			Payload: &foghornfederationpb.PeerMessage_EdgeTelemetry{EdgeTelemetry: &foghornfederationpb.EdgeTelemetry{
				StreamName: "s",
				NodeId:     "n",
			}},
		}},
	})

	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestPeerChannel_RejectsClusterIDChangeWithinStream(t *testing.T) {
	cache, _ := setupTestCache(t)
	srv := NewFederationServer(FederationServerConfig{
		Logger:    testLogger(),
		ClusterID: "cluster-a",
		Cache:     cache,
	})

	svcCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	err := srv.PeerChannel(&testPeerChannelServerStream{
		ctx: svcCtx,
		messages: []*foghornfederationpb.PeerMessage{
			{ClusterId: "cluster-b", Payload: &foghornfederationpb.PeerMessage_PeerHeartbeat{PeerHeartbeat: &foghornfederationpb.PeerHeartbeat{ProtocolVersion: 1}}},
			{ClusterId: "cluster-c", Payload: &foghornfederationpb.PeerMessage_PeerHeartbeat{PeerHeartbeat: &foghornfederationpb.PeerHeartbeat{ProtocolVersion: 1}}},
		},
	})

	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}
