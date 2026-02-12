package grpc

import (
	"context"
	"sync"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeControlStream struct {
	pb.HelmsmanControl_ConnectServer
	mu   sync.Mutex
	sent []*pb.ControlMessage
}

func (f *fakeControlStream) Send(msg *pb.ControlMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	return nil
}

func setupLocalRegistry(t *testing.T, nodeID string) *fakeControlStream {
	t.Helper()
	stream := &fakeControlStream{}
	cleanup := control.SetupTestRegistry(nodeID, stream)
	t.Cleanup(cleanup)
	return stream
}

func TestForwardCommand_MissingNodeID(t *testing.T) {
	srv := NewRelayServer(logging.NewLogger())

	_, err := srv.ForwardCommand(context.Background(), &pb.ForwardCommandRequest{})
	if err == nil {
		t.Fatal("expected error for empty target_node_id")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestForwardCommand_UnknownCommandType(t *testing.T) {
	srv := NewRelayServer(logging.NewLogger())

	_, err := srv.ForwardCommand(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: "node-1",
	})
	if err == nil {
		t.Fatal("expected error for nil command")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestForwardCommand_NodeNotConnected(t *testing.T) {
	cleanup := control.SetupTestRegistry("", nil)
	t.Cleanup(cleanup)
	srv := NewRelayServer(logging.NewLogger())

	resp, err := srv.ForwardCommand(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: "node-missing",
		Command:      &pb.ForwardCommandRequest_DvrStop{DvrStop: &pb.DVRStopRequest{}},
	})
	if err != nil {
		t.Fatalf("expected nil gRPC error (soft failure), got %v", err)
	}
	if resp.Delivered {
		t.Fatal("expected Delivered=false for missing node")
	}
	if resp.Error == "" {
		t.Fatal("expected non-empty error in response")
	}
}

func TestForwardCommand_AllCommandTypes(t *testing.T) {
	stream := setupLocalRegistry(t, "node-1")
	srv := NewRelayServer(logging.NewLogger())

	commands := []struct {
		name string
		cmd  *pb.ForwardCommandRequest
	}{
		{"ConfigSeed", &pb.ForwardCommandRequest{
			TargetNodeId: "node-1",
			Command:      &pb.ForwardCommandRequest_ConfigSeed{ConfigSeed: &pb.ConfigSeed{NodeId: "node-1"}},
		}},
		{"ClipPull", &pb.ForwardCommandRequest{
			TargetNodeId: "node-1",
			Command:      &pb.ForwardCommandRequest_ClipPull{ClipPull: &pb.ClipPullRequest{}},
		}},
		{"DVRStart", &pb.ForwardCommandRequest{
			TargetNodeId: "node-1",
			Command:      &pb.ForwardCommandRequest_DvrStart{DvrStart: &pb.DVRStartRequest{}},
		}},
		{"DVRStop", &pb.ForwardCommandRequest{
			TargetNodeId: "node-1",
			Command:      &pb.ForwardCommandRequest_DvrStop{DvrStop: &pb.DVRStopRequest{}},
		}},
		{"ClipDelete", &pb.ForwardCommandRequest{
			TargetNodeId: "node-1",
			Command:      &pb.ForwardCommandRequest_ClipDelete{ClipDelete: &pb.ClipDeleteRequest{}},
		}},
		{"DVRDelete", &pb.ForwardCommandRequest{
			TargetNodeId: "node-1",
			Command:      &pb.ForwardCommandRequest_DvrDelete{DvrDelete: &pb.DVRDeleteRequest{}},
		}},
		{"VodDelete", &pb.ForwardCommandRequest{
			TargetNodeId: "node-1",
			Command:      &pb.ForwardCommandRequest_VodDelete{VodDelete: &pb.VodDeleteRequest{}},
		}},
		{"Defrost", &pb.ForwardCommandRequest{
			TargetNodeId: "node-1",
			Command:      &pb.ForwardCommandRequest_Defrost{Defrost: &pb.DefrostRequest{}},
		}},
		{"DtshSync", &pb.ForwardCommandRequest{
			TargetNodeId: "node-1",
			Command:      &pb.ForwardCommandRequest_DtshSync{DtshSync: &pb.DtshSyncRequest{}},
		}},
		{"StopSessions", &pb.ForwardCommandRequest{
			TargetNodeId: "node-1",
			Command:      &pb.ForwardCommandRequest_StopSessions{StopSessions: &pb.StopSessionsRequest{}},
		}},
	}

	for _, tc := range commands {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := srv.ForwardCommand(context.Background(), tc.cmd)
			if err != nil {
				t.Fatalf("ForwardCommand(%s): %v", tc.name, err)
			}
			if !resp.Delivered {
				t.Fatalf("ForwardCommand(%s): expected Delivered=true, got error=%s", tc.name, resp.Error)
			}
		})
	}

	stream.mu.Lock()
	count := len(stream.sent)
	stream.mu.Unlock()
	if count != len(commands) {
		t.Fatalf("expected %d messages sent to stream, got %d", len(commands), count)
	}
}
