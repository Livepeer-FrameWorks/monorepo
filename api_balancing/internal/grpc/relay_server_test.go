package grpc

import (
	"context"
	"maps"
	"slices"
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

	_, err := srv.ForwardCommand(context.Background(), &pb.ForwardCommandRequest{TargetNodeId: "node-1"})
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
		field   string
		cmd     *pb.ForwardCommandRequest
		payload string
	}{
		{"config_seed", &pb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &pb.ForwardCommandRequest_ConfigSeed{ConfigSeed: &pb.ConfigSeed{NodeId: "node-1"}}}, "config_seed"},
		{"clip_pull", &pb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &pb.ForwardCommandRequest_ClipPull{ClipPull: &pb.ClipPullRequest{}}}, "clip_pull_request"},
		{"dvr_start", &pb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &pb.ForwardCommandRequest_DvrStart{DvrStart: &pb.DVRStartRequest{}}}, "dvr_start_request"},
		{"dvr_stop", &pb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &pb.ForwardCommandRequest_DvrStop{DvrStop: &pb.DVRStopRequest{}}}, "dvr_stop_request"},
		{"clip_delete", &pb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &pb.ForwardCommandRequest_ClipDelete{ClipDelete: &pb.ClipDeleteRequest{}}}, "clip_delete"},
		{"dvr_delete", &pb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &pb.ForwardCommandRequest_DvrDelete{DvrDelete: &pb.DVRDeleteRequest{}}}, "dvr_delete"},
		{"vod_delete", &pb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &pb.ForwardCommandRequest_VodDelete{VodDelete: &pb.VodDeleteRequest{}}}, "vod_delete"},
		{"defrost", &pb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &pb.ForwardCommandRequest_Defrost{Defrost: &pb.DefrostRequest{}}}, "defrost_request"},
		{"dtsh_sync", &pb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &pb.ForwardCommandRequest_DtshSync{DtshSync: &pb.DtshSyncRequest{}}}, "dtsh_sync_request"},
		{"stop_sessions", &pb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &pb.ForwardCommandRequest_StopSessions{StopSessions: &pb.StopSessionsRequest{}}}, "stop_sessions_request"},
		{"activate_push_targets", &pb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &pb.ForwardCommandRequest_ActivatePushTargets{ActivatePushTargets: &pb.ActivatePushTargets{}}}, "activate_push_targets"},
		{"deactivate_push_targets", &pb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &pb.ForwardCommandRequest_DeactivatePushTargets{DeactivatePushTargets: &pb.DeactivatePushTargets{}}}, "deactivate_push_targets"},
	}

	oneofFields := pb.File_foghorn_relay_proto.Messages().ByName("ForwardCommandRequest").Oneofs().ByName("command").Fields()
	protoFields := make(map[string]struct{}, oneofFields.Len())
	for i := 0; i < oneofFields.Len(); i++ {
		protoFields[string(oneofFields.Get(i).Name())] = struct{}{}
	}
	commandFields := map[string]struct{}{}
	for _, tc := range commands {
		commandFields[tc.field] = struct{}{}
	}
	if !maps.Equal(protoFields, commandFields) {
		t.Fatalf("relay dispatch coverage mismatch: proto=%v tests=%v", sortedKeys(protoFields), sortedKeys(commandFields))
	}

	for _, tc := range commands {
		t.Run(tc.field, func(t *testing.T) {
			resp, err := srv.ForwardCommand(context.Background(), tc.cmd)
			if err != nil {
				t.Fatalf("ForwardCommand(%s): %v", tc.field, err)
			}
			if !resp.Delivered {
				t.Fatalf("ForwardCommand(%s): expected Delivered=true, got error=%s", tc.field, resp.Error)
			}

			stream.mu.Lock()
			msg := stream.sent[len(stream.sent)-1]
			stream.mu.Unlock()
			gotPayload := string(msg.ProtoReflect().WhichOneof(msg.ProtoReflect().Descriptor().Oneofs().ByName("payload")).Name())
			if gotPayload != tc.payload {
				t.Fatalf("ForwardCommand(%s): expected payload=%s got=%s", tc.field, tc.payload, gotPayload)
			}
		})
	}
}

func sortedKeys(m map[string]struct{}) []string {
	keys := slices.Collect(maps.Keys(m))
	slices.Sort(keys)
	return keys
}
