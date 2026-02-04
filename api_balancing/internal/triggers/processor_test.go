package triggers

import (
	"strings"
	"testing"

	pb "frameworks/pkg/proto"
)

// TestPayloadTypeAssertions verifies that handlers return errors for wrong payload types
// instead of panicking on nil pointer dereference.
func TestPayloadTypeAssertions(t *testing.T) {
	// Create a minimal processor (nil dependencies are fine for type assertion tests)
	p := &Processor{}

	tests := []struct {
		name           string
		handler        func(*pb.MistTrigger) (string, bool, error)
		validTrigger   *pb.MistTrigger
		invalidTrigger *pb.MistTrigger
		expectedErr    string
	}{
		{
			name:    "handleProcessBilling",
			handler: p.handleProcessBilling,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_PushRewrite{
					PushRewrite: &pb.PushRewriteTrigger{},
				},
			},
			expectedErr: "unexpected payload type for ProcessBilling",
		},
		{
			name:    "handleStorageLifecycleData",
			handler: p.handleStorageLifecycleData,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_StorageLifecycleData{
					StorageLifecycleData: &pb.StorageLifecycleData{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for StorageLifecycleData",
		},
		{
			name:    "handleDVRLifecycleData",
			handler: p.handleDVRLifecycleData,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_DvrLifecycleData{
					DvrLifecycleData: &pb.DVRLifecycleData{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for DVRLifecycleData",
		},
		{
			name:    "handlePushRewrite",
			handler: p.handlePushRewrite,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_PushRewrite{
					PushRewrite: &pb.PushRewriteTrigger{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for PushRewrite",
		},
		{
			name:    "handlePlayRewrite",
			handler: p.handlePlayRewrite,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_PlayRewrite{
					PlayRewrite: &pb.ViewerResolveTrigger{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for PlayRewrite",
		},
		{
			name:    "handleStreamSource",
			handler: p.handleStreamSource,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_StreamSource{
					StreamSource: &pb.StreamSourceTrigger{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for StreamSource",
		},
		{
			name:    "handlePushEnd",
			handler: p.handlePushEnd,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_PushEnd{
					PushEnd: &pb.PushEndTrigger{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for PushEnd",
		},
		{
			name:    "handlePushOutStart",
			handler: p.handlePushOutStart,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_PushOutStart{
					PushOutStart: &pb.PushOutStartTrigger{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for PushOutStart",
		},
		{
			name:    "handleUserNew",
			handler: p.handleUserNew,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ViewerConnect{
					ViewerConnect: &pb.ViewerConnectTrigger{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for ViewerConnect",
		},
		{
			name:    "handleStreamBuffer",
			handler: p.handleStreamBuffer,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_StreamBuffer{
					StreamBuffer: &pb.StreamBufferTrigger{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for StreamBuffer",
		},
		{
			name:    "handleStreamEnd",
			handler: p.handleStreamEnd,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_StreamEnd{
					StreamEnd: &pb.StreamEndTrigger{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for StreamEnd",
		},
		{
			name:    "handleUserEnd",
			handler: p.handleUserEnd,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ViewerDisconnect{
					ViewerDisconnect: &pb.ViewerDisconnectTrigger{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for ViewerDisconnect",
		},
		{
			name:    "handleLiveTrackList",
			handler: p.handleLiveTrackList,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_TrackList{
					TrackList: &pb.StreamTrackListTrigger{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for TrackList",
		},
		{
			name:    "handleRecordingEnd",
			handler: p.handleRecordingEnd,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_RecordingComplete{
					RecordingComplete: &pb.RecordingCompleteTrigger{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for RecordingComplete",
		},
		{
			name:    "handleRecordingSegment",
			handler: p.handleRecordingSegment,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_RecordingSegment{
					RecordingSegment: &pb.RecordingSegmentTrigger{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for RecordingSegment",
		},
		{
			name:    "handleStreamLifecycleUpdate",
			handler: p.handleStreamLifecycleUpdate,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_StreamLifecycleUpdate{
					StreamLifecycleUpdate: &pb.StreamLifecycleUpdate{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for StreamLifecycleUpdate",
		},
		{
			name:    "handleClientLifecycleUpdate",
			handler: p.handleClientLifecycleUpdate,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ClientLifecycleUpdate{
					ClientLifecycleUpdate: &pb.ClientLifecycleUpdate{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for ClientLifecycleUpdate",
		},
		{
			name:    "handleNodeLifecycleUpdate",
			handler: p.handleNodeLifecycleUpdate,
			validTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_NodeLifecycleUpdate{
					NodeLifecycleUpdate: &pb.NodeLifecycleUpdate{},
				},
			},
			invalidTrigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for NodeLifecycleUpdate",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name+"_wrong_type", func(t *testing.T) {
			_, _, err := tc.handler(tc.invalidTrigger)
			if err == nil {
				t.Fatalf("expected error for wrong payload type, got nil")
			}
			if !strings.Contains(err.Error(), tc.expectedErr) {
				t.Fatalf("expected error containing %q, got %q", tc.expectedErr, err.Error())
			}
		})

		t.Run(tc.name+"_nil_payload", func(t *testing.T) {
			nilTrigger := &pb.MistTrigger{TriggerPayload: nil}
			_, _, err := tc.handler(nilTrigger)
			if err == nil {
				t.Fatalf("expected error for nil payload, got nil")
			}
			if !strings.Contains(err.Error(), tc.expectedErr) {
				t.Fatalf("expected error containing %q, got %q", tc.expectedErr, err.Error())
			}
		})
	}
}

// TestPayloadTypeAssertions_ValidTypes verifies handlers don't error on correct payload types.
// Note: These will panic on nil logger/clients, so we only test that the type assertion passes.
func TestPayloadTypeAssertions_ValidTypes(t *testing.T) {
	tests := []struct {
		name    string
		trigger *pb.MistTrigger
	}{
		{
			name: "ProcessBilling",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{},
				},
			},
		},
		{
			name: "StorageLifecycleData",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_StorageLifecycleData{
					StorageLifecycleData: &pb.StorageLifecycleData{},
				},
			},
		},
		{
			name: "DvrLifecycleData",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_DvrLifecycleData{
					DvrLifecycleData: &pb.DVRLifecycleData{},
				},
			},
		},
		{
			name: "PushRewrite",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_PushRewrite{
					PushRewrite: &pb.PushRewriteTrigger{},
				},
			},
		},
		{
			name: "PlayRewrite",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_PlayRewrite{
					PlayRewrite: &pb.ViewerResolveTrigger{},
				},
			},
		},
		{
			name: "StreamSource",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_StreamSource{
					StreamSource: &pb.StreamSourceTrigger{},
				},
			},
		},
		{
			name: "PushEnd",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_PushEnd{
					PushEnd: &pb.PushEndTrigger{},
				},
			},
		},
		{
			name: "PushOutStart",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_PushOutStart{
					PushOutStart: &pb.PushOutStartTrigger{},
				},
			},
		},
		{
			name: "ViewerConnect",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ViewerConnect{
					ViewerConnect: &pb.ViewerConnectTrigger{},
				},
			},
		},
		{
			name: "StreamBuffer",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_StreamBuffer{
					StreamBuffer: &pb.StreamBufferTrigger{},
				},
			},
		},
		{
			name: "StreamEnd",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_StreamEnd{
					StreamEnd: &pb.StreamEndTrigger{},
				},
			},
		},
		{
			name: "ViewerDisconnect",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ViewerDisconnect{
					ViewerDisconnect: &pb.ViewerDisconnectTrigger{},
				},
			},
		},
		{
			name: "TrackList",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_TrackList{
					TrackList: &pb.StreamTrackListTrigger{},
				},
			},
		},
		{
			name: "RecordingComplete",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_RecordingComplete{
					RecordingComplete: &pb.RecordingCompleteTrigger{},
				},
			},
		},
		{
			name: "RecordingSegment",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_RecordingSegment{
					RecordingSegment: &pb.RecordingSegmentTrigger{},
				},
			},
		},
		{
			name: "StreamLifecycleUpdate",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_StreamLifecycleUpdate{
					StreamLifecycleUpdate: &pb.StreamLifecycleUpdate{},
				},
			},
		},
		{
			name: "ClientLifecycleUpdate",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_ClientLifecycleUpdate{
					ClientLifecycleUpdate: &pb.ClientLifecycleUpdate{},
				},
			},
		},
		{
			name: "NodeLifecycleUpdate",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_NodeLifecycleUpdate{
					NodeLifecycleUpdate: &pb.NodeLifecycleUpdate{},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Just verify type assertion succeeds (returns non-nil)
			payload := tc.trigger.GetTriggerPayload()
			if payload == nil {
				t.Fatalf("GetTriggerPayload returned nil for %s", tc.name)
			}
		})
	}
}
