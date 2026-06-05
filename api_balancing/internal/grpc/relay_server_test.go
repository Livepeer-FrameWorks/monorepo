package grpc

import (
	"context"
	"maps"
	"slices"
	"sync"
	"testing"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornrelaypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_relay"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeControlStream struct {
	ipcpb.HelmsmanControl_ConnectServer
	mu   sync.Mutex
	sent []*ipcpb.ControlMessage
}

func (f *fakeControlStream) Send(msg *ipcpb.ControlMessage) error {
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

	_, err := srv.ForwardCommand(context.Background(), &foghornrelaypb.ForwardCommandRequest{})
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

	_, err := srv.ForwardCommand(context.Background(), &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1"})
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

	resp, err := srv.ForwardCommand(context.Background(), &foghornrelaypb.ForwardCommandRequest{
		TargetNodeId: "node-missing",
		Command:      &foghornrelaypb.ForwardCommandRequest_DvrStop{DvrStop: &ipcpb.DVRStopRequest{}},
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
		cmd     *foghornrelaypb.ForwardCommandRequest
		payload string
	}{
		{"config_seed", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_ConfigSeed{ConfigSeed: &ipcpb.ConfigSeed{NodeId: "node-1"}}}, "config_seed"},
		{"dvr_start", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_DvrStart{DvrStart: &ipcpb.DVRStartRequest{}}}, "dvr_start_request"},
		{"dvr_stop", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_DvrStop{DvrStop: &ipcpb.DVRStopRequest{}}}, "dvr_stop_request"},
		{"clip_delete", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_ClipDelete{ClipDelete: &ipcpb.ClipDeleteRequest{}}}, "clip_delete"},
		{"dvr_delete", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_DvrDelete{DvrDelete: &ipcpb.DVRDeleteRequest{}}}, "dvr_delete"},
		{"vod_delete", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_VodDelete{VodDelete: &ipcpb.VodDeleteRequest{}}}, "vod_delete"},
		{"dtsh_sync", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_DtshSync{DtshSync: &ipcpb.DtshSyncRequest{}}}, "dtsh_sync_request"},
		{"stop_sessions", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_StopSessions{StopSessions: &ipcpb.StopSessionsRequest{}}}, "stop_sessions_request"},
		{"invalidate_sessions", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_InvalidateSessions{InvalidateSessions: &ipcpb.InvalidateSessionsRequest{}}}, "invalidate_sessions_request"},
		{"activate_push_targets", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_ActivatePushTargets{ActivatePushTargets: &ipcpb.ActivatePushTargets{}}}, "activate_push_targets"},
		{"deactivate_push_targets", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_DeactivatePushTargets{DeactivatePushTargets: &ipcpb.DeactivatePushTargets{}}}, "deactivate_push_targets"},
		{"processing_job", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_ProcessingJob{ProcessingJob: &ipcpb.ProcessingJobRequest{}}}, "processing_job_request"},
		{"freeze", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_Freeze{Freeze: &ipcpb.FreezeRequest{}}}, "freeze_request"},
		{"desired_state_update", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_DesiredStateUpdate{DesiredStateUpdate: &ipcpb.DesiredStateUpdate{}}}, "desired_state_update"},
		{"apply_managed_stream", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_ApplyManagedStream{ApplyManagedStream: &ipcpb.ApplyManagedStream{Name: "demo"}}}, "apply_managed_stream"},
		{"retract_managed_stream", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_RetractManagedStream{RetractManagedStream: &ipcpb.RetractManagedStream{Name: "demo"}}}, "retract_managed_stream"},
		{"drain_stream", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_DrainStream{DrainStream: &ipcpb.DrainStreamRequest{RuntimeName: "live+demo"}}}, "drain_stream_request"},
		{"dvr_update_source", &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "node-1", Command: &foghornrelaypb.ForwardCommandRequest_DvrUpdateSource{DvrUpdateSource: &ipcpb.DVRUpdateSourceRequest{DvrHash: "abc", SourceRuntimeName: "live+demo"}}}, "dvr_update_source_request"},
	}

	oneofFields := foghornrelaypb.File_foghorn_relay_proto.Messages().ByName("ForwardCommandRequest").Oneofs().ByName("command").Fields()
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
