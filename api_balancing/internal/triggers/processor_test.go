package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/ingesterrors"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

// counterValue reads a single Counter's current value without pulling in
// prometheus/testutil (which transitively requires kylelemons/godebug).
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("counter Write failed: %v", err)
	}
	return m.GetCounter().GetValue()
}

// TestPayloadTypeAssertions verifies that handlers return errors for wrong payload types
// instead of panicking on nil pointer dereference.
func TestPayloadTypeAssertions(t *testing.T) {
	// Create a minimal processor (nil dependencies are fine for type assertion tests)
	p := &Processor{}

	tests := []struct {
		name           string
		handler        func(*ipcpb.MistTrigger) (string, bool, error)
		validTrigger   *ipcpb.MistTrigger
		invalidTrigger *ipcpb.MistTrigger
		expectedErr    string
	}{
		{
			name:    "handleProcessBilling",
			handler: p.handleProcessBilling,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
					PushRewrite: &ipcpb.PushRewriteTrigger{},
				},
			},
			expectedErr: "unexpected payload type for ProcessBilling",
		},
		{
			name:    "handleStorageLifecycleData",
			handler: p.handleStorageLifecycleData,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_StorageLifecycleData{
					StorageLifecycleData: &ipcpb.StorageLifecycleData{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for StorageLifecycleData",
		},
		{
			name:    "handleDVRLifecycleData",
			handler: p.handleDVRLifecycleData,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_DvrLifecycleData{
					DvrLifecycleData: &ipcpb.DVRLifecycleData{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for DVRLifecycleData",
		},
		{
			name:    "handlePushRewrite",
			handler: p.handlePushRewrite,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
					PushRewrite: &ipcpb.PushRewriteTrigger{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for PushRewrite",
		},
		{
			name:    "handlePlayRewrite",
			handler: p.handlePlayRewrite,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_PlayRewrite{
					PlayRewrite: &ipcpb.ViewerResolveTrigger{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for PlayRewrite",
		},
		{
			name:    "handleStreamSource",
			handler: p.handleStreamSource,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_StreamSource{
					StreamSource: &ipcpb.StreamSourceTrigger{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for StreamSource",
		},
		{
			name:    "handlePushEnd",
			handler: p.handlePushEnd,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_PushEnd{
					PushEnd: &ipcpb.PushEndTrigger{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for PushEnd",
		},
		{
			name:    "handlePushOutStart",
			handler: p.handlePushOutStart,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_PushOutStart{
					PushOutStart: &ipcpb.PushOutStartTrigger{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for PushOutStart",
		},
		{
			name:    "handleUserNew",
			handler: p.handleUserNew,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{
					ViewerConnect: &ipcpb.ViewerConnectTrigger{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for ViewerConnect",
		},
		{
			name:    "handleStreamBuffer",
			handler: p.handleStreamBuffer,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_StreamBuffer{
					StreamBuffer: &ipcpb.StreamBufferTrigger{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for StreamBuffer",
		},
		{
			name:    "handleStreamEnd",
			handler: p.handleStreamEnd,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_StreamEnd{
					StreamEnd: &ipcpb.StreamEndTrigger{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for StreamEnd",
		},
		{
			name:    "handleUserEnd",
			handler: p.handleUserEnd,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{
					ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for ViewerDisconnect",
		},
		{
			name:    "handleLiveTrackList",
			handler: p.handleLiveTrackList,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_TrackList{
					TrackList: &ipcpb.StreamTrackListTrigger{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for TrackList",
		},
		{
			name:    "handleRecordingEnd",
			handler: p.handleRecordingEnd,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_RecordingComplete{
					RecordingComplete: &ipcpb.RecordingCompleteTrigger{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for RecordingComplete",
		},
		{
			name:    "handleRecordingSegment",
			handler: p.handleRecordingSegment,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_RecordingSegment{
					RecordingSegment: &ipcpb.RecordingSegmentTrigger{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for RecordingSegment",
		},
		{
			name:    "handleStreamLifecycleUpdate",
			handler: p.handleStreamLifecycleUpdate,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
					StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for StreamLifecycleUpdate",
		},
		{
			name:    "handleClientLifecycleUpdate",
			handler: p.handleClientLifecycleUpdate,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ClientLifecycleUpdate{
					ClientLifecycleUpdate: &ipcpb.ClientLifecycleUpdate{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
			expectedErr: "unexpected payload type for ClientLifecycleUpdate",
		},
		{
			name:    "handleNodeLifecycleUpdate",
			handler: p.handleNodeLifecycleUpdate,
			validTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_NodeLifecycleUpdate{
					NodeLifecycleUpdate: &ipcpb.NodeLifecycleUpdate{},
				},
			},
			invalidTrigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
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
			nilTrigger := &ipcpb.MistTrigger{TriggerPayload: nil}
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

func TestHandleClientLifecycleDropsMissingStreamID(t *testing.T) {
	tenantID := "tenant-1"
	p := &Processor{logger: logging.NewLogger()}

	_, _, err := p.handleClientLifecycleUpdate(&ipcpb.MistTrigger{
		TriggerType: "CLIENT_LIFECYCLE_UPDATE",
		TriggerPayload: &ipcpb.MistTrigger_ClientLifecycleUpdate{
			ClientLifecycleUpdate: &ipcpb.ClientLifecycleUpdate{
				TenantId: &tenantID,
			},
		},
	})
	if err != nil {
		t.Fatalf("expected missing stream_id to drop without error, got %v", err)
	}
	if p.clientBatcher != nil {
		t.Fatal("expected missing stream_id sample not to initialize client lifecycle batcher")
	}
}

func TestHandleStreamLifecycleDropsMissingStreamID(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	tenantID := "tenant-1"
	inputs := uint32(1)
	p := &Processor{logger: logging.NewLogger()}

	_, _, err := p.handleStreamLifecycleUpdate(&ipcpb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE",
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
				TenantId:     &tenantID,
				InternalName: "processing+artifact-1",
				Status:       "live",
				TotalInputs:  &inputs,
			},
		},
	})
	if err != nil {
		t.Fatalf("expected missing stream_id to drop without error, got %v", err)
	}
	if got := state.DefaultManager().GetStreamState("artifact-1"); got != nil {
		t.Fatalf("expected missing stream_id lifecycle not to update stream state, got %#v", got)
	}
}

func TestHandleNodeLifecycleUpdate_TriggersImmediateReconcileOnlyOnArtifactMapChange(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	var callbackCount atomic.Int32
	control.SetOnArtifactMapUpdated(func(_ string) {
		callbackCount.Add(1)
	})
	t.Cleanup(func() {
		control.SetOnArtifactMapUpdated(nil)
	})

	p := &Processor{logger: logging.NewLogger()}

	newTrigger := func(artifacts ...*ipcpb.StoredArtifact) *ipcpb.MistTrigger {
		return &ipcpb.MistTrigger{
			TriggerPayload: &ipcpb.MistTrigger_NodeLifecycleUpdate{
				NodeLifecycleUpdate: &ipcpb.NodeLifecycleUpdate{
					NodeId:    "node-1",
					Artifacts: artifacts,
				},
			},
		}
	}

	artifactA := &ipcpb.StoredArtifact{
		ClipHash:     "hash-a",
		StreamName:   "stream-a",
		FilePath:     "/data/hash-a.mp4",
		SizeBytes:    100,
		CreatedAt:    1700000000,
		Format:       "mp4",
		ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
		AccessCount:  1,
		LastAccessed: 1700000001,
	}
	artifactASamePlacement := &ipcpb.StoredArtifact{
		ClipHash:     "hash-a",
		StreamName:   "stream-a",
		FilePath:     "/data/hash-a.mp4",
		SizeBytes:    100,
		CreatedAt:    1700000000,
		Format:       "mp4",
		ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
		AccessCount:  99,
		LastAccessed: 1700000900,
	}
	artifactB := &ipcpb.StoredArtifact{
		ClipHash:     "hash-b",
		StreamName:   "stream-b",
		FilePath:     "/data/hash-b.mp4",
		SizeBytes:    200,
		CreatedAt:    1700000100,
		Format:       "mp4",
		ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
	}

	if _, _, err := p.handleNodeLifecycleUpdate(newTrigger(artifactA, artifactB)); err != nil {
		t.Fatalf("first lifecycle update failed: %v", err)
	}
	if got := callbackCount.Load(); got != 1 {
		t.Fatalf("expected first artifact map to trigger callback once, got %d", got)
	}

	if _, _, err := p.handleNodeLifecycleUpdate(newTrigger(artifactB, artifactASamePlacement)); err != nil {
		t.Fatalf("reordered lifecycle update failed: %v", err)
	}
	if got := callbackCount.Load(); got != 1 {
		t.Fatalf("expected reordered/noisy artifact map to avoid callback, got %d", got)
	}

	artifactAWithDtsh := &ipcpb.StoredArtifact{
		ClipHash:     "hash-a",
		StreamName:   "stream-a",
		FilePath:     "/data/hash-a.mp4",
		SizeBytes:    100,
		CreatedAt:    1700000000,
		Format:       "mp4",
		HasDtsh:      true,
		ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
	}
	if _, _, err := p.handleNodeLifecycleUpdate(newTrigger(artifactAWithDtsh, artifactB)); err != nil {
		t.Fatalf("dtsh lifecycle update failed: %v", err)
	}
	if got := callbackCount.Load(); got != 2 {
		t.Fatalf("expected dtsh change to trigger callback, got %d", got)
	}
}

// TestPayloadTypeAssertions_ValidTypes verifies handlers don't error on correct payload types.
// Note: These will panic on nil logger/clients, so we only test that the type assertion passes.
func TestPayloadTypeAssertions_ValidTypes(t *testing.T) {
	tests := []struct {
		name    string
		trigger *ipcpb.MistTrigger
	}{
		{
			name: "ProcessBilling",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{},
				},
			},
		},
		{
			name: "StorageLifecycleData",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_StorageLifecycleData{
					StorageLifecycleData: &ipcpb.StorageLifecycleData{},
				},
			},
		},
		{
			name: "DvrLifecycleData",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_DvrLifecycleData{
					DvrLifecycleData: &ipcpb.DVRLifecycleData{},
				},
			},
		},
		{
			name: "PushRewrite",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
					PushRewrite: &ipcpb.PushRewriteTrigger{},
				},
			},
		},
		{
			name: "PlayRewrite",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_PlayRewrite{
					PlayRewrite: &ipcpb.ViewerResolveTrigger{},
				},
			},
		},
		{
			name: "StreamSource",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_StreamSource{
					StreamSource: &ipcpb.StreamSourceTrigger{},
				},
			},
		},
		{
			name: "PushEnd",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_PushEnd{
					PushEnd: &ipcpb.PushEndTrigger{},
				},
			},
		},
		{
			name: "PushOutStart",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_PushOutStart{
					PushOutStart: &ipcpb.PushOutStartTrigger{},
				},
			},
		},
		{
			name: "ViewerConnect",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{
					ViewerConnect: &ipcpb.ViewerConnectTrigger{},
				},
			},
		},
		{
			name: "StreamBuffer",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_StreamBuffer{
					StreamBuffer: &ipcpb.StreamBufferTrigger{},
				},
			},
		},
		{
			name: "StreamEnd",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_StreamEnd{
					StreamEnd: &ipcpb.StreamEndTrigger{},
				},
			},
		},
		{
			name: "ViewerDisconnect",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{
					ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{},
				},
			},
		},
		{
			name: "TrackList",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_TrackList{
					TrackList: &ipcpb.StreamTrackListTrigger{},
				},
			},
		},
		{
			name: "RecordingComplete",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_RecordingComplete{
					RecordingComplete: &ipcpb.RecordingCompleteTrigger{},
				},
			},
		},
		{
			name: "RecordingSegment",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_RecordingSegment{
					RecordingSegment: &ipcpb.RecordingSegmentTrigger{},
				},
			},
		},
		{
			name: "StreamLifecycleUpdate",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
					StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{},
				},
			},
		},
		{
			name: "ClientLifecycleUpdate",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_ClientLifecycleUpdate{
					ClientLifecycleUpdate: &ipcpb.ClientLifecycleUpdate{},
				},
			},
		},
		{
			name: "NodeLifecycleUpdate",
			trigger: &ipcpb.MistTrigger{
				TriggerPayload: &ipcpb.MistTrigger_NodeLifecycleUpdate{
					NodeLifecycleUpdate: &ipcpb.NodeLifecycleUpdate{},
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

func TestHandleStreamSource_MistNativePlaybackIDResolvesThroughContext(t *testing.T) {
	t.Setenv("BRAND_DOMAIN", "frameworks.network")
	commodoreClient, cleanup, stub := setupCommodoreClientWithStub(t, nil, nil)
	t.Cleanup(cleanup)
	stub.resolveStreamContextByKey = map[string]*commodorepb.ResolveStreamContextResponse{
		"playback_id:frameworks-demo": {
			Admitted:   true,
			IngestMode: "mist_native",
			StreamId:   "stream-uuid-1",
			PlaybackId: "frameworks-demo",
			// Production playback IDs are public aliases; the concrete Mist
			// stream can be a separate internal name.
			InternalName: "60546679b497415db2338cd5cae54992",
			TenantId:     "tenant-system",
		},
	}

	processor := newTestProcessor(t)
	processor.commodoreClient = commodoreClient
	processor.clusterID = "media-eu-1"

	resp, abort, err := processor.handleStreamSource(&ipcpb.MistTrigger{
		NodeId: "edge-eu-1",
		TriggerPayload: &ipcpb.MistTrigger_StreamSource{
			StreamSource: &ipcpb.StreamSourceTrigger{StreamName: "frameworks-demo"},
		},
	})
	if err != nil {
		t.Fatalf("handleStreamSource failed: %v", err)
	}
	if abort {
		t.Fatal("expected non-abort STREAM_SOURCE response")
	}
	if resp != "balance:https://foghorn.media-eu-1.frameworks.network" {
		t.Fatalf("unexpected STREAM_SOURCE response: %q", resp)
	}
	keys := stub.ResolveStreamContextKeys()
	want := []string{"internal_name:frameworks-demo", "playback_id:frameworks-demo"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected ResolveStreamContext lookups: got %v want %v", keys, want)
	}
}

func TestHandleStreamSource_LiveOriginPullReturnsDTSC(t *testing.T) {
	prevRegistry := control.StreamRegistryInstance
	registry := control.NewStreamRegistry(nil, "cluster-local", time.Minute)
	control.SetStreamRegistry(registry)
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	registry.MarkReplicating(
		"stream-1",
		"cluster-origin",
		"dtsc://edge-origin:4200/live+stream-1",
		"edge-local-1",
		"https://edge-local-1.example/view",
		"edge-origin-1",
	)

	processor := newTestProcessor(t)
	resp, abort, err := processor.handleStreamSource(&ipcpb.MistTrigger{
		NodeId: "edge-local-1",
		TriggerPayload: &ipcpb.MistTrigger_StreamSource{
			StreamSource: &ipcpb.StreamSourceTrigger{StreamName: "live+stream-1"},
		},
	})
	if err != nil {
		t.Fatalf("handleStreamSource failed: %v", err)
	}
	if abort {
		t.Fatal("expected non-abort STREAM_SOURCE response")
	}
	if resp != "dtsc://edge-origin:4200/live+stream-1" {
		t.Fatalf("STREAM_SOURCE response = %q", resp)
	}
}

// TestHandleStreamSource_LiveWithoutOriginPullDelegatesToSource: with no
// pre-arranged origin-pull, live+ returns balance:<base> so Mist's balancer
// asks /source — the SAME unified resolver bare mist-native and pull+ use.
// /source then runs local best-node selection, on-demand cross-cluster
// origin-pull arrange, and the live+ push:// terminal. Dead-ending at "" here
// (the old behavior) skipped straight to the local push input, so a viewer
// landing directly on an edge before /play arranged the pull never discovered
// a remote origin (US-ingest / EU-playback).
func TestHandleStreamSource_LiveWithoutOriginPullDelegatesToSource(t *testing.T) {
	t.Setenv("BRAND_DOMAIN", "frameworks.network")
	prevRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-local", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	processor := newTestProcessor(t)
	processor.clusterID = "media-eu-1"
	resp, abort, err := processor.handleStreamSource(&ipcpb.MistTrigger{
		NodeId: "edge-local-1",
		TriggerPayload: &ipcpb.MistTrigger_StreamSource{
			StreamSource: &ipcpb.StreamSourceTrigger{StreamName: "live+stream-1"},
		},
	})
	if err != nil {
		t.Fatalf("handleStreamSource failed: %v", err)
	}
	if abort {
		t.Fatal("expected non-abort balance delegation to /source")
	}
	if !strings.HasPrefix(resp, "balance:") {
		t.Fatalf("STREAM_SOURCE response = %q, want balance:<base> delegation to /source", resp)
	}
}

// TestHandleStreamSource_PullOriginPullReturnsDTSC covers the pull+
// federation case: another cluster already has the upstream and we
// DTSC-pull from them rather than dialing the upstream a second time.
// Before the federation hook, pull+ unconditionally returned
// balance:<foghorn> here and re-dialed upstream via /source.
func TestHandleStreamSource_PullOriginPullReturnsDTSC(t *testing.T) {
	prevRegistry := control.StreamRegistryInstance
	registry := control.NewStreamRegistry(nil, "cluster-local", time.Minute)
	control.SetStreamRegistry(registry)
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	registry.MarkReplicating(
		"stream-pull-1",
		"cluster-origin",
		"dtsc://edge-origin:4200/pull+stream-pull-1",
		"edge-local-1",
		"https://edge-local-1.example/view",
		"edge-origin-1",
	)

	processor := newTestProcessor(t)
	resp, abort, err := processor.handleStreamSource(&ipcpb.MistTrigger{
		NodeId: "edge-local-1",
		TriggerPayload: &ipcpb.MistTrigger_StreamSource{
			StreamSource: &ipcpb.StreamSourceTrigger{StreamName: "pull+stream-pull-1"},
		},
	})
	if err != nil {
		t.Fatalf("handleStreamSource failed: %v", err)
	}
	if abort {
		t.Fatal("expected non-abort STREAM_SOURCE response")
	}
	if resp != "dtsc://edge-origin:4200/pull+stream-pull-1" {
		t.Fatalf("STREAM_SOURCE response = %q", resp)
	}
}

// TestHandleStreamSource_DVRDefensiveOriginPullReturnsDTSC pins the
// dvr+ branch's federation hook. Cross-cluster DVR federation is wired
// via tryArrangeDVRCrossCluster (processor.go); this test sets the
// registry directly to verify the STREAM_SOURCE hook returns the peer
// DTSC URL once the registry has a Location for the dvr+ runtime name.
// Uses the dvr+ runtime name as-is for the registry key
// (sourceInternalKey doesn't strip dvr+).
func TestHandleStreamSource_DVRDefensiveOriginPullReturnsDTSC(t *testing.T) {
	prevRegistry := control.StreamRegistryInstance
	registry := control.NewStreamRegistry(nil, "cluster-local", time.Minute)
	control.SetStreamRegistry(registry)
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	registry.MarkReplicating(
		"dvr+abc123",
		"cluster-origin",
		"dtsc://edge-origin:4200/dvr+abc123",
		"edge-local-1",
		"https://edge-local-1.example/view",
		"edge-origin-1",
	)

	processor := newTestProcessor(t)
	resp, abort, err := processor.handleStreamSource(&ipcpb.MistTrigger{
		NodeId: "edge-local-1",
		TriggerPayload: &ipcpb.MistTrigger_StreamSource{
			StreamSource: &ipcpb.StreamSourceTrigger{StreamName: "dvr+abc123"},
		},
	})
	if err != nil {
		t.Fatalf("handleStreamSource failed: %v", err)
	}
	if abort {
		t.Fatal("expected non-abort STREAM_SOURCE response")
	}
	if resp != "dtsc://edge-origin:4200/dvr+abc123" {
		t.Fatalf("STREAM_SOURCE response = %q", resp)
	}
}

// TestHandleStreamSource_MistNativeOriginPullReturnsDTSC covers the bare
// mist-native federation hook: a managed stream replicated cross-cluster
// goes through STREAM_SOURCE directly rather than the balance: +
// /source round-trip. Saves an HTTP hop on the federated case.
func TestHandleStreamSource_MistNativeOriginPullReturnsDTSC(t *testing.T) {
	prevRegistry := control.StreamRegistryInstance
	registry := control.NewStreamRegistry(nil, "cluster-local", time.Minute)
	control.SetStreamRegistry(registry)
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	registry.MarkReplicating(
		"frameworks-demo",
		"cluster-origin",
		"dtsc://edge-origin:4200/frameworks-demo",
		"edge-local-1",
		"https://edge-local-1.example/view",
		"edge-origin-1",
	)

	processor := newTestProcessor(t)
	resp, abort, err := processor.handleStreamSource(&ipcpb.MistTrigger{
		NodeId: "edge-local-1",
		TriggerPayload: &ipcpb.MistTrigger_StreamSource{
			StreamSource: &ipcpb.StreamSourceTrigger{StreamName: "frameworks-demo"},
		},
	})
	if err != nil {
		t.Fatalf("handleStreamSource failed: %v", err)
	}
	if abort {
		t.Fatal("expected non-abort STREAM_SOURCE response")
	}
	if resp != "dtsc://edge-origin:4200/frameworks-demo" {
		t.Fatalf("STREAM_SOURCE response = %q", resp)
	}
}

// TestHandleStreamSource_OfflineReasonsLockedIn pins the offline:<reason>
// taxonomy returned from the various STREAM_SOURCE failure paths.
// Players don't parse the suffix but operators rely on it for debugging,
// so the strings are a stable contract. Mist's input_balancer recognizes
// the "offline:" prefix and produces a clean STRMSTAT_OFFLINE
// disconnect regardless of the suffix.
func TestHandleStreamSource_OfflineReasonsLockedIn(t *testing.T) {
	processor := newTestProcessor(t)

	cases := []struct {
		name   string
		stream string
		want   string
	}{
		{"dvr+ empty token", "dvr+", control.OfflineInvalidToken},
		{"pull+ empty token", "pull+", control.OfflineInvalidToken},
		{"processing+ empty hash", "processing+", control.OfflineInvalidToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, abort, err := processor.handleStreamSource(&ipcpb.MistTrigger{
				NodeId: "edge-local-1",
				TriggerPayload: &ipcpb.MistTrigger_StreamSource{
					StreamSource: &ipcpb.StreamSourceTrigger{StreamName: tc.stream},
				},
			})
			if err != nil {
				t.Fatalf("handleStreamSource err: %v", err)
			}
			if abort {
				t.Fatal("expected non-abort (offline: signal should flow back to balancer cleanly)")
			}
			if resp != tc.want {
				t.Fatalf("STREAM_SOURCE response = %q, want %q", resp, tc.want)
			}
		})
	}
}

// TestHandleStreamSource_OriginPullPinnedToOtherEdgeReturnsEmpty
// exercises the multi-edge guard shared by all four branches: when
// origin-pull is arranged but pinned to a DIFFERENT local edge, this
// edge must return "" so it doesn't start a duplicate untracked pull.
// Mist's fallback (push:// for live, balance: for pull, etc.) handles
// the not-this-edge case.
func TestHandleStreamSource_OriginPullPinnedToOtherEdgeReturnsEmpty(t *testing.T) {
	prevRegistry := control.StreamRegistryInstance
	registry := control.NewStreamRegistry(nil, "cluster-local", time.Minute)
	control.SetStreamRegistry(registry)
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	registry.MarkReplicating(
		"stream-pinned",
		"cluster-origin",
		"dtsc://edge-origin:4200/live+stream-pinned",
		"edge-local-A", // pull pinned to edge-local-A
		"https://edge-local-A.example/view",
		"edge-origin-1",
	)

	processor := newTestProcessor(t)
	// Different edge (edge-local-B) fires STREAM_SOURCE — must NOT pull.
	resp, abort, err := processor.handleStreamSource(&ipcpb.MistTrigger{
		NodeId: "edge-local-B",
		TriggerPayload: &ipcpb.MistTrigger_StreamSource{
			StreamSource: &ipcpb.StreamSourceTrigger{StreamName: "live+stream-pinned"},
		},
	})
	if err != nil {
		t.Fatalf("handleStreamSource failed: %v", err)
	}
	if abort {
		t.Fatal("expected non-abort (fallback) when pinned to another edge")
	}
	if resp != "" {
		t.Fatalf("STREAM_SOURCE response = %q, want empty (pinned-to-other-edge guard)", resp)
	}
}

func TestHandlePlayRewriteBareMistNativeResolvesThroughInternalName(t *testing.T) {
	oldCommodore := control.CommodoreClient
	control.SetCommodoreClient(nil)
	t.Cleanup(func() { control.SetCommodoreClient(oldCommodore) })

	commodoreClient, cleanup, stub := setupCommodoreClientWithStub(t, nil, nil)
	t.Cleanup(cleanup)
	stub.resolveStreamContextByKey = map[string]*commodorepb.ResolveStreamContextResponse{
		"internal_name:60546679b497415db2338cd5cae54992": {
			Admitted:     true,
			IngestMode:   "mist_native",
			StreamId:     "stream-uuid-1",
			PlaybackId:   "frameworks-demo",
			InternalName: "60546679b497415db2338cd5cae54992",
			TenantId:     "tenant-system",
		},
	}

	processor := newTestProcessor(t)
	processor.commodoreClient = commodoreClient
	processor.clusterID = "media-eu-1"

	resp, abort, err := processor.handlePlayRewrite(&ipcpb.MistTrigger{
		NodeId: "edge-eu-1",
		TriggerPayload: &ipcpb.MistTrigger_PlayRewrite{
			PlayRewrite: &ipcpb.ViewerResolveTrigger{
				RequestedStream: "60546679b497415db2338cd5cae54992",
				ViewerHost:      "192.0.2.10",
				OutputType:      "HTTP",
				RequestUrl:      "https://edge.example/view/60546679b497415db2338cd5cae54992.mkv?duration=30&startunix=-60",
			},
		},
	})
	if err != nil {
		t.Fatalf("handlePlayRewrite failed: %v", err)
	}
	if abort {
		t.Fatal("expected non-abort PLAY_REWRITE response")
	}
	if resp != "60546679b497415db2338cd5cae54992" {
		t.Fatalf("unexpected PLAY_REWRITE response: %q", resp)
	}
	keys := stub.ResolveStreamContextKeys()
	want := []string{"internal_name:60546679b497415db2338cd5cae54992"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected ResolveStreamContext lookups: got %v want %v", keys, want)
	}
}

type stubCommodoreInternalService struct {
	commodorepb.UnimplementedInternalServiceServer
	validateResponse          *commodorepb.ValidateStreamKeyResponse
	validateErr               error
	mu                        sync.Mutex
	validateClusterIDs        []string
	resolveIdentifierResponse *commodorepb.ResolveIdentifierResponse
	resolveIdentifierErr      error
	resolveStreamContextByKey map[string]*commodorepb.ResolveStreamContextResponse
	resolveStreamContextErr   error
	resolveStreamContextKeys  []string
}

func (s *stubCommodoreInternalService) ValidateStreamKey(ctx context.Context, req *commodorepb.ValidateStreamKeyRequest) (*commodorepb.ValidateStreamKeyResponse, error) {
	s.mu.Lock()
	s.validateClusterIDs = append(s.validateClusterIDs, req.GetClusterId())
	s.mu.Unlock()
	return s.validateResponse, s.validateErr
}

func (s *stubCommodoreInternalService) LastValidateClusterID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.validateClusterIDs) == 0 {
		return ""
	}
	return s.validateClusterIDs[len(s.validateClusterIDs)-1]
}

func (s *stubCommodoreInternalService) ResolveIdentifier(ctx context.Context, req *commodorepb.ResolveIdentifierRequest) (*commodorepb.ResolveIdentifierResponse, error) {
	return s.resolveIdentifierResponse, s.resolveIdentifierErr
}

func (s *stubCommodoreInternalService) ResolveStreamContext(ctx context.Context, req *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
	key := ""
	switch id := req.GetIdentifier().(type) {
	case *commodorepb.ResolveStreamContextRequest_StreamId:
		key = "stream_id:" + id.StreamId
	case *commodorepb.ResolveStreamContextRequest_PlaybackId:
		key = "playback_id:" + id.PlaybackId
	case *commodorepb.ResolveStreamContextRequest_InternalName:
		key = "internal_name:" + id.InternalName
	}
	s.mu.Lock()
	s.resolveStreamContextKeys = append(s.resolveStreamContextKeys, key)
	resp := s.resolveStreamContextByKey[key]
	err := s.resolveStreamContextErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return &commodorepb.ResolveStreamContextResponse{
			Admitted:        false,
			AdmissionReason: "stream not found",
			RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY,
		}, nil
	}
	return resp, nil
}

func (s *stubCommodoreInternalService) ResolveStreamContextKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.resolveStreamContextKeys))
	copy(out, s.resolveStreamContextKeys)
	return out
}

func newTestProcessor(t *testing.T) *Processor {
	t.Helper()
	p := &Processor{
		logger: logging.Logger(logrus.New()),
	}
	p.streamCache = cache.New(cache.Options{
		TTL:                  10 * time.Minute,
		StaleWhileRevalidate: 0,
		NegativeTTL:          0,
		MaxEntries:           100,
		SkipStore:            p.streamCacheHeld,
	}, cache.MetricsHooks{})
	return p
}

// TestInvalidatePlaybackAuthCacheHoldsDownPerKeyRepopulate confirms that an
// in-flight loader cannot replace a freshly-evicted entry while the per-key
// hold-down is active — the bug fix for the race between Commodore's
// invalidation fanout and a concurrent ResolveIdentifier.
func TestInvalidatePlaybackAuthCacheHoldsDownPerKeyRepopulate(t *testing.T) {
	processor := newTestProcessor(t)
	tenantID := "tenant-1"
	internalName := "stream-x"
	cacheKey := tenantID + ":" + internalName

	processor.streamCache.Set(cacheKey, streamContext{TenantID: tenantID}, time.Minute)

	processor.InvalidatePlaybackAuthCache(tenantID, []string{internalName})

	processor.streamCache.Set(cacheKey, streamContext{TenantID: tenantID, UserID: "stale"}, time.Minute)
	if _, ok := processor.streamCache.Peek(cacheKey); ok {
		t.Fatal("Set repopulated a held-down key; hold-down failed")
	}

	loaderCalls := 0
	val, ok, err := processor.streamCache.Get(context.Background(), cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
		loaderCalls++
		return streamContext{TenantID: tenantID, UserID: "stale-via-loader"}, true, nil
	})
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if loaderCalls != 1 {
		t.Fatalf("loader should have been called exactly once, got %d", loaderCalls)
	}
	if !ok || val == nil {
		t.Fatal("loader returned ok but Get reported miss")
	}
	if _, ok := processor.streamCache.Peek(cacheKey); ok {
		t.Fatal("Get(loader) repopulated a held-down key; hold-down failed")
	}
}

// TestInvalidateTenantCacheHoldsDownTenantPrefix confirms that a tenant-wide
// invalidation also blocks new keys arriving for that tenant during the hold
// window, not just keys that already existed at eviction time.
func TestInvalidateTenantCacheHoldsDownTenantPrefix(t *testing.T) {
	processor := newTestProcessor(t)
	tenantID := "tenant-1"

	processor.InvalidateTenantCache(tenantID)

	cacheKey := tenantID + ":new-stream-arriving-after-evict"
	processor.streamCache.Set(cacheKey, streamContext{TenantID: tenantID}, time.Minute)
	if _, ok := processor.streamCache.Peek(cacheKey); ok {
		t.Fatal("tenant hold-down did not block a new key for the same tenant")
	}

	otherKey := "tenant-2:still-allowed"
	processor.streamCache.Set(otherKey, streamContext{TenantID: "tenant-2"}, time.Minute)
	if _, ok := processor.streamCache.Peek(otherKey); !ok {
		t.Fatal("hold-down leaked across tenant boundary")
	}
}

// TestStreamCacheHoldExpires confirms the hold-down releases after the window
// elapses so legitimate writes resume.
func TestStreamCacheHoldExpires(t *testing.T) {
	processor := newTestProcessor(t)
	tenantID := "tenant-1"
	cacheKey := tenantID + ":stream-x"

	processor.holdStreamCacheKey(cacheKey)

	processor.streamCacheHoldsMu.Lock()
	processor.streamCacheKeyHolds[cacheKey] = time.Now().Add(-time.Millisecond)
	processor.streamCacheHoldsMu.Unlock()

	processor.streamCache.Set(cacheKey, streamContext{TenantID: tenantID}, time.Minute)
	if _, ok := processor.streamCache.Peek(cacheKey); !ok {
		t.Fatal("expired hold-down still blocking writes")
	}
}

func TestHandlePlayRewriteStartsCorrelatedPlaybackViewer(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	processor := newTestProcessor(t)
	tenantID := "tenant-1"
	nodeID := "node-1"
	internalName := "stream-count"
	clientIP := "192.0.2.10"
	processor.streamCache.Set(tenantID+":"+internalName, streamContext{
		TenantID:          tenantID,
		RequiresAuth:      false,
		RequiresAuthKnown: true,
	}, time.Minute)

	viewerID := sm.CreateVirtualViewer(nodeID, internalName, clientIP)

	resp, abort, err := processor.handlePlayRewrite(&ipcpb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_PlayRewrite{
			PlayRewrite: &ipcpb.ViewerResolveTrigger{
				RequestedStream: "live+" + internalName,
				ViewerHost:      clientIP,
				OutputType:      "HLS",
				RequestUrl:      "https://edge.example/view/hls/live+stream/index.m3u8?fwcid=" + viewerID,
			},
		},
	})
	if err != nil {
		t.Fatalf("handlePlayRewrite failed: %v", err)
	}
	if abort || resp != "live+"+internalName {
		t.Fatalf("expected allowed PLAY_REWRITE, got response=%q abort=%v", resp, abort)
	}
	if got := sm.GetStreamState(internalName).Viewers; got != 1 {
		t.Fatalf("expected 1 viewer after PLAY_REWRITE, got %d", got)
	}

	_, _, err = processor.handleUserNew(&ipcpb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{
			ViewerConnect: &ipcpb.ViewerConnectTrigger{
				StreamName: "live+" + internalName,
				Host:       clientIP,
				Connector:  "HLS",
				RequestUrl: "https://edge.example/view?fwcid=" + viewerID,
				SessionId:  "mist-session-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("handleUserNew failed: %v", err)
	}
	if got := sm.GetStreamState(internalName).Viewers; got != 1 {
		t.Fatalf("expected USER_NEW session attachment not to increment viewers, got %d", got)
	}

	_, _, err = processor.handlePlayRewrite(&ipcpb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_PlayRewrite{
			PlayRewrite: &ipcpb.ViewerResolveTrigger{
				RequestedStream: "live+" + internalName,
				ViewerHost:      clientIP,
				OutputType:      "HLS",
				RequestUrl:      "https://edge.example/view/hls/live+stream/index.m3u8?fwcid=" + viewerID,
			},
		},
	})
	if err != nil {
		t.Fatalf("duplicate handlePlayRewrite failed: %v", err)
	}
	if got := sm.GetStreamState(internalName).Viewers; got != 1 {
		t.Fatalf("expected duplicate PLAY_REWRITE not to increment viewers, got %d", got)
	}

	_, _, err = processor.handleUserEnd(&ipcpb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{
				SessionId:  "mist-session-1",
				StreamName: "live+" + internalName,
				Connector:  "HTTP",
				Host:       clientIP,
				Duration:   10,
			},
		},
	})
	if err != nil {
		t.Fatalf("handleUserEnd failed: %v", err)
	}
	if got := sm.GetStreamState(internalName).Viewers; got != 0 {
		t.Fatalf("expected generic HTTP USER_END with attached session to decrement viewers, got %d", got)
	}
}

func TestHandleUserNewDoesNotStartPlaybackViewer(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	processor := newTestProcessor(t)
	tenantID := "tenant-1"
	nodeID := "node-1"
	internalName := "stream-user-new"
	clientIP := "192.0.2.10"
	processor.streamCache.Set(tenantID+":"+internalName, streamContext{
		TenantID:          tenantID,
		RequiresAuth:      false,
		RequiresAuthKnown: true,
	}, time.Minute)

	resp, abort, err := processor.handleUserNew(&ipcpb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{
			ViewerConnect: &ipcpb.ViewerConnectTrigger{
				StreamName: "live+" + internalName,
				Host:       clientIP,
				Connector:  "HLS",
				RequestUrl: "https://edge.example/view",
				SessionId:  "mist-session-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("handleUserNew failed: %v", err)
	}
	if abort || resp != "true" {
		t.Fatalf("expected allowed USER_NEW, got response=%q abort=%v", resp, abort)
	}
	if got := sm.GetStreamState(internalName); got != nil {
		t.Fatalf("expected USER_NEW alone not to create viewer count, got %+v", got)
	}
}

func TestHandleUserEndCountsOnlyConfirmedPlaybackDisconnect(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	processor := newTestProcessor(t)
	tenantID := "tenant-1"
	nodeID := "node-1"
	internalName := "stream-disconnect"
	clientIP := "192.0.2.11"
	processor.streamCache.Set(tenantID+":"+internalName, streamContext{TenantID: tenantID}, time.Minute)

	viewerID := sm.CreateVirtualViewer(nodeID, internalName, clientIP)
	if confirmed := sm.ConfirmVirtualViewerByID(viewerID, nodeID, internalName, clientIP, "mist-session-1"); !confirmed {
		t.Fatal("expected virtual viewer confirmation")
	}
	sm.UpdateUserConnection(internalName, nodeID, tenantID, 1)

	_, _, err := processor.handleUserEnd(&ipcpb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{
				SessionId:  "mist-session-1",
				StreamName: "live+" + internalName,
				Connector:  "HLS",
				Host:       clientIP,
				Duration:   10,
			},
		},
	})
	if err != nil {
		t.Fatalf("handleUserEnd failed: %v", err)
	}
	if got := sm.GetStreamState(internalName).Viewers; got != 0 {
		t.Fatalf("expected viewer count to decrement after confirmed disconnect, got %d", got)
	}

	_, _, err = processor.handleUserEnd(&ipcpb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{
				SessionId:  "missing-session",
				StreamName: "live+" + internalName,
				Connector:  "HLS",
				Host:       clientIP,
				Duration:   10,
			},
		},
	})
	if err != nil {
		t.Fatalf("unmatched handleUserEnd failed: %v", err)
	}
	if got := sm.GetStreamState(internalName).Viewers; got != 0 {
		t.Fatalf("expected unmatched USER_END not to decrement below zero, got %d", got)
	}
}

func TestHandleUserTriggersIgnoreNonPlaybackConnectors(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	processor := newTestProcessor(t)
	tenantID := "tenant-1"
	nodeID := "node-1"
	internalName := "stream-thumb"

	resp, abort, err := processor.handleUserNew(&ipcpb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{
			ViewerConnect: &ipcpb.ViewerConnectTrigger{
				StreamName: "live+" + internalName,
				Host:       "192.0.2.12",
				Connector:  "ThumbVTT",
				RequestUrl: "https://edge.example/thumbs.vtt",
				SessionId:  "thumb-session",
			},
		},
	})
	if err != nil {
		t.Fatalf("non-playback handleUserNew failed: %v", err)
	}
	if abort || resp != "true" {
		t.Fatalf("expected non-playback USER_NEW to be allowed without counting, got response=%q abort=%v", resp, abort)
	}

	_, _, err = processor.handleUserEnd(&ipcpb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{
				SessionId:  "thumb-session",
				StreamName: "live+" + internalName,
				Connector:  "Raw/WS,info_json",
				Host:       "192.0.2.12",
			},
		},
	})
	if err != nil {
		t.Fatalf("non-playback handleUserEnd failed: %v", err)
	}
	if got := sm.GetStreamState(internalName); got != nil {
		t.Fatalf("expected non-playback triggers not to create stream state, got %+v", got)
	}
}

func setupCommodoreClient(t *testing.T, response *commodorepb.ValidateStreamKeyResponse, responseErr error) (*commodore.GRPCClient, func()) {
	client, cleanup, _ := setupCommodoreClientWithStub(t, response, responseErr)
	return client, cleanup
}

func setupCommodoreClientWithStub(t *testing.T, response *commodorepb.ValidateStreamKeyResponse, responseErr error) (*commodore.GRPCClient, func(), *stubCommodoreInternalService) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	server := grpc.NewServer()
	stub := &stubCommodoreInternalService{
		validateResponse: response,
		validateErr:      responseErr,
	}
	commodorepb.RegisterInternalServiceServer(server, stub)

	go func() {
		_ = server.Serve(listener)
	}()

	client, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:      listener.Addr().String(),
		Logger:        logging.Logger(logrus.New()),
		AllowInsecure: true,
	})
	if err != nil {
		server.Stop()
		_ = listener.Close()
		t.Fatalf("failed to create commodore client: %v", err)
	}

	cleanup := func() {
		_ = client.Close()
		server.Stop()
		_ = listener.Close()
	}

	return client, cleanup, stub
}

func setupCommodoreResolveIdentifierClient(t *testing.T, response *commodorepb.ResolveIdentifierResponse, responseErr error) (*commodore.GRPCClient, func()) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	server := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(server, &stubCommodoreInternalService{
		resolveIdentifierResponse: response,
		resolveIdentifierErr:      responseErr,
	})

	go func() {
		_ = server.Serve(listener)
	}()

	client, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:      listener.Addr().String(),
		Logger:        logging.Logger(logrus.New()),
		AllowInsecure: true,
	})
	if err != nil {
		server.Stop()
		_ = listener.Close()
		t.Fatalf("failed to create commodore client: %v", err)
	}

	cleanup := func() {
		_ = client.Close()
		server.Stop()
		_ = listener.Close()
	}

	return client, cleanup
}

func TestHandleProcessBilling_EnrichesFromCache(t *testing.T) {
	processor := newTestProcessor(t)
	tenantID := "tenant-1"
	internalName := "stream-abc"
	cacheKey := tenantID + ":" + internalName
	processor.streamCache.Set(cacheKey, streamContext{
		TenantID: tenantID,
		UserID:   "user-1",
		StreamID: "stream-id",
		Source:   "test",
	}, time.Minute)

	streamID := "stream-id"
	trigger := &ipcpb.MistTrigger{
		StreamId: &streamID,
		TenantId: &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
			ProcessBilling: &ipcpb.ProcessBillingEvent{
				StreamName: "live+" + internalName,
			},
		},
	}

	_, blocking, err := processor.handleProcessBilling(trigger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blocking {
		t.Fatalf("expected non-blocking response")
	}

	payload := trigger.GetProcessBilling()
	if payload.GetTenantId() != tenantID {
		t.Fatalf("expected tenant ID %q, got %q", tenantID, payload.GetTenantId())
	}
	if payload.GetStreamId() != streamID {
		t.Fatalf("expected stream ID %q, got %q", streamID, payload.GetStreamId())
	}
}

func TestHandleStorageLifecycleData_UsesCacheAndStreamIDFallback(t *testing.T) {
	processor := newTestProcessor(t)
	tenantID := "tenant-2"
	internalName := "storage-stream"
	cacheKey := tenantID + ":" + internalName
	processor.streamCache.Set(cacheKey, streamContext{
		TenantID: tenantID,
		UserID:   "user-2",
		StreamID: "stream-2",
		Source:   "test",
	}, time.Minute)

	streamID := "stream-2"
	trigger := &ipcpb.MistTrigger{
		StreamId: &streamID,
		TriggerPayload: &ipcpb.MistTrigger_StorageLifecycleData{
			StorageLifecycleData: &ipcpb.StorageLifecycleData{
				TenantId:     &tenantID,
				InternalName: &internalName,
			},
		},
	}

	_, blocking, err := processor.handleStorageLifecycleData(trigger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blocking {
		t.Fatalf("expected non-blocking response")
	}

	if trigger.GetUserId() != "user-2" {
		t.Fatalf("expected user ID to be enriched, got %q", trigger.GetUserId())
	}
	payload := trigger.GetStorageLifecycleData()
	if payload.GetStreamId() != streamID {
		t.Fatalf("expected stream ID %q, got %q", streamID, payload.GetStreamId())
	}
}

func TestHandleStorageLifecycleData_UsesAssetHashFallback(t *testing.T) {
	commodoreClient, cleanup := setupCommodoreResolveIdentifierClient(t, &commodorepb.ResolveIdentifierResponse{
		Found:          true,
		TenantId:       "tenant-storage",
		UserId:         "user-storage",
		StreamId:       "stream-storage",
		IdentifierType: "clip_hash",
	}, nil)
	t.Cleanup(cleanup)

	processor := newTestProcessor(t)
	processor.commodoreClient = commodoreClient

	trigger := &ipcpb.MistTrigger{
		TriggerPayload: &ipcpb.MistTrigger_StorageLifecycleData{
			StorageLifecycleData: &ipcpb.StorageLifecycleData{
				AssetHash: "clip-hash-1",
			},
		},
	}

	_, blocking, err := processor.handleStorageLifecycleData(trigger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blocking {
		t.Fatalf("expected non-blocking response")
	}
	if trigger.GetTenantId() != "tenant-storage" {
		t.Fatalf("expected tenant ID to be enriched, got %q", trigger.GetTenantId())
	}
	if trigger.GetUserId() != "user-storage" {
		t.Fatalf("expected user ID to be enriched, got %q", trigger.GetUserId())
	}
	if got := trigger.GetStorageLifecycleData().GetStreamId(); got != "stream-storage" {
		t.Fatalf("expected stream ID %q, got %q", "stream-storage", got)
	}
}

func TestHandleDVRLifecycleData_NormalizesInternalName(t *testing.T) {
	processor := newTestProcessor(t)
	tenantID := "tenant-3"
	internalName := "dvr-stream"
	cacheKey := tenantID + ":" + internalName
	processor.streamCache.Set(cacheKey, streamContext{
		TenantID: tenantID,
		UserID:   "user-3",
		StreamID: "stream-3",
		Source:   "test",
	}, time.Minute)

	streamID := "stream-3"
	wildcardName := "live+" + internalName
	trigger := &ipcpb.MistTrigger{
		StreamId: &streamID,
		TriggerPayload: &ipcpb.MistTrigger_DvrLifecycleData{
			DvrLifecycleData: &ipcpb.DVRLifecycleData{
				TenantId:           &tenantID,
				StreamInternalName: &wildcardName,
			},
		},
	}

	_, blocking, err := processor.handleDVRLifecycleData(trigger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blocking {
		t.Fatalf("expected non-blocking response")
	}

	if trigger.GetUserId() != "user-3" {
		t.Fatalf("expected user ID to be enriched, got %q", trigger.GetUserId())
	}
	payload := trigger.GetDvrLifecycleData()
	if payload.GetStreamId() != streamID {
		t.Fatalf("expected stream ID %q, got %q", streamID, payload.GetStreamId())
	}
}

func TestHandleDVRLifecycleData_UsesDVRHashFallback(t *testing.T) {
	commodoreClient, cleanup := setupCommodoreResolveIdentifierClient(t, &commodorepb.ResolveIdentifierResponse{
		Found:          true,
		TenantId:       "tenant-dvr",
		UserId:         "user-dvr",
		StreamId:       "stream-dvr",
		IdentifierType: "dvr_hash",
	}, nil)
	t.Cleanup(cleanup)

	processor := newTestProcessor(t)
	processor.commodoreClient = commodoreClient

	trigger := &ipcpb.MistTrigger{
		TriggerPayload: &ipcpb.MistTrigger_DvrLifecycleData{
			DvrLifecycleData: &ipcpb.DVRLifecycleData{
				DvrHash: "dvr-hash-1",
			},
		},
	}

	_, blocking, err := processor.handleDVRLifecycleData(trigger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blocking {
		t.Fatalf("expected non-blocking response")
	}
	if trigger.GetTenantId() != "tenant-dvr" {
		t.Fatalf("expected tenant ID to be enriched, got %q", trigger.GetTenantId())
	}
	if trigger.GetUserId() != "user-dvr" {
		t.Fatalf("expected user ID to be enriched, got %q", trigger.GetUserId())
	}
	if got := trigger.GetDvrLifecycleData().GetStreamId(); got != "stream-dvr" {
		t.Fatalf("expected stream ID %q, got %q", "stream-dvr", got)
	}
}

func TestHandlePushRewrite_CachesBillingContext(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("edge-node-1", "http://edge.example/view", true, nil, nil, "", "", nil)
	prevRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-local", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	response := &commodorepb.ValidateStreamKeyResponse{
		Valid:             true,
		UserId:            "user-4",
		TenantId:          "tenant-4",
		InternalName:      "push-stream",
		StreamId:          "stream-4",
		BillingModel:      "prepaid",
		IsSuspended:       false,
		IsBalanceNegative: false,
	}
	commodoreClient, cleanup := setupCommodoreClient(t, response, nil)
	t.Cleanup(cleanup)

	processor := newTestProcessor(t)
	processor.commodoreClient = commodoreClient

	trigger := &ipcpb.MistTrigger{
		NodeId: "edge-node-1",
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
			PushRewrite: &ipcpb.PushRewriteTrigger{
				StreamName: "push-stream",
				PushUrl:    "rtmp://example.com/live",
			},
		},
	}

	streamName, blocking, err := processor.handlePushRewrite(trigger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blocking {
		t.Fatalf("expected non-blocking response")
	}
	if streamName != "live+push-stream" {
		t.Fatalf("expected live+push-stream response, got %q", streamName)
	}

	if trigger.GetTenantId() != "tenant-4" {
		t.Fatalf("expected tenant ID to be set, got %q", trigger.GetTenantId())
	}
	if trigger.GetUserId() != "user-4" {
		t.Fatalf("expected user ID to be set, got %q", trigger.GetUserId())
	}
	if trigger.GetStreamId() != "stream-4" {
		t.Fatalf("expected stream ID to be set, got %q", trigger.GetStreamId())
	}

	cacheKey := "tenant-4:push-stream"
	cached, ok := processor.streamCache.Peek(cacheKey)
	if !ok {
		t.Fatalf("expected stream cache entry for %q", cacheKey)
	}
	info, ok := cached.(streamContext)
	if !ok {
		t.Fatalf("expected stream cache entry type, got %T", cached)
	}
	if info.BillingModel != "prepaid" || info.IsBalanceNegative || info.IsSuspended {
		t.Fatalf("unexpected billing context: %+v", info)
	}
	nodeID, baseURL, ok := control.GetStreamSource("push-stream")
	if !ok {
		t.Fatal("expected PUSH_REWRITE to seed stream source for immediate DVR start")
	}
	if nodeID != "edge-node-1" || baseURL != "http://edge.example/view" {
		t.Fatalf("unexpected stream source: node=%q base=%q", nodeID, baseURL)
	}
}

func TestHandlePushRewrite_RejectsSuspendedTenant(t *testing.T) {
	response := &commodorepb.ValidateStreamKeyResponse{
		Valid:        true,
		TenantId:     "tenant-5",
		InternalName: "suspended-stream",
		IsSuspended:  true,
	}
	commodoreClient, cleanup := setupCommodoreClient(t, response, nil)
	t.Cleanup(cleanup)

	processor := newTestProcessor(t)
	processor.commodoreClient = commodoreClient

	trigger := &ipcpb.MistTrigger{
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
			PushRewrite: &ipcpb.PushRewriteTrigger{
				StreamName: "suspended-stream",
			},
		},
	}

	_, blocking, err := processor.handlePushRewrite(trigger)
	if err == nil {
		t.Fatalf("expected error for suspended tenant")
	}
	if !blocking {
		t.Fatalf("expected blocking response")
	}
	var ingestErr *ingesterrors.IngestError
	if !errors.As(err, &ingestErr) {
		t.Fatalf("expected ingest error type, got %T", err)
	}
	if ingestErr.Code != ipcpb.IngestErrorCode_INGEST_ERROR_ACCOUNT_SUSPENDED {
		t.Fatalf("expected suspended error code, got %v", ingestErr.Code)
	}
}

func TestHandlePushRewrite_RejectsNegativeBalanceTenant(t *testing.T) {
	response := &commodorepb.ValidateStreamKeyResponse{
		Valid:             true,
		TenantId:          "tenant-6",
		InternalName:      "negative-balance-stream",
		IsBalanceNegative: true,
	}
	commodoreClient, cleanup := setupCommodoreClient(t, response, nil)
	t.Cleanup(cleanup)

	processor := newTestProcessor(t)
	processor.commodoreClient = commodoreClient

	trigger := &ipcpb.MistTrigger{
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
			PushRewrite: &ipcpb.PushRewriteTrigger{
				StreamName: "negative-balance-stream",
			},
		},
	}

	_, blocking, err := processor.handlePushRewrite(trigger)
	if err == nil {
		t.Fatalf("expected error for negative balance tenant")
	}
	if !blocking {
		t.Fatalf("expected blocking response")
	}
	var ingestErr *ingesterrors.IngestError
	if !errors.As(err, &ingestErr) {
		t.Fatalf("expected ingest error type, got %T", err)
	}
	if ingestErr.Code != ipcpb.IngestErrorCode_INGEST_ERROR_PAYMENT_REQUIRED {
		t.Fatalf("expected payment required error code, got %v", ingestErr.Code)
	}
}

func TestGetStreamOrigin_PrefixedStreamName(t *testing.T) {
	processor := newTestProcessor(t)

	// Cache stores bare internal name (as written by PUSH_REWRITE)
	processor.streamCache.Set("tenantA:abc123-def456", streamContext{
		TenantID:        "tenantA",
		OriginClusterID: "cluster-eu",
	}, time.Minute)

	t.Run("bare name matches", func(t *testing.T) {
		tenantID, clusterID := processor.GetStreamOrigin("abc123-def456")
		if tenantID != "tenantA" {
			t.Fatalf("expected tenantA, got %q", tenantID)
		}
		if clusterID != "cluster-eu" {
			t.Fatalf("expected cluster-eu, got %q", clusterID)
		}
	})

	t.Run("live+ prefixed name does NOT match (caller must strip)", func(t *testing.T) {
		// GetStreamOrigin expects bare internal name; the caller (getStreamTenantID)
		// is responsible for calling mist.ExtractInternalName before calling this.
		tenantID, _ := processor.GetStreamOrigin("live+abc123-def456")
		if tenantID != "" {
			t.Fatalf("expected empty (prefix not stripped by caller), got %q", tenantID)
		}
	})
}

func TestGetStreamOrigin_AmbiguousInternalName(t *testing.T) {
	processor := newTestProcessor(t)

	processor.streamCache.Set("tenant-a:mystream", streamContext{
		TenantID:        "tenant-a",
		OriginClusterID: "cluster-eu",
	}, time.Minute)
	processor.streamCache.Set("tenant-b:mystream", streamContext{
		TenantID:        "tenant-b",
		OriginClusterID: "cluster-us",
	}, time.Minute)

	tenantID, clusterID := processor.GetStreamOrigin("mystream")
	if tenantID != "" || clusterID != "" {
		t.Fatalf("expected empty returns for ambiguous name, got tenant=%q cluster=%q", tenantID, clusterID)
	}
}

func TestApplyStreamContext_SeparatesClusterAndOrigin(t *testing.T) {
	processor := newTestProcessor(t)
	processor.clusterID = "cluster-local"

	processor.streamCache.Set("tenant-1:stream-a", streamContext{
		TenantID:        "tenant-1",
		UserID:          "user-1",
		StreamID:        "stream-id-1",
		OriginClusterID: "cluster-origin",
	}, time.Minute)

	trigger := &ipcpb.MistTrigger{TenantId: func() *string { s := "tenant-1"; return &s }()}
	info := processor.applyStreamContext(trigger, "stream-a")

	if info.OriginClusterID != "cluster-origin" {
		t.Fatalf("expected origin cluster in context, got %q", info.OriginClusterID)
	}
	if trigger.GetOriginClusterId() != "cluster-origin" {
		t.Fatalf("expected origin_cluster_id from cache, got %q", trigger.GetOriginClusterId())
	}
	if trigger.GetClusterId() != "cluster-local" {
		t.Fatalf("expected emitting cluster_id from processor, got %q", trigger.GetClusterId())
	}
}

func TestApplyStreamContext_UsesTenantHintToAvoidCrossTenantMixups(t *testing.T) {
	processor := newTestProcessor(t)
	processor.clusterID = "cluster-local"

	processor.streamCache.Set("tenant-a:shared-stream", streamContext{
		TenantID:        "tenant-a",
		OriginClusterID: "cluster-a",
	}, time.Minute)
	processor.streamCache.Set("tenant-b:shared-stream", streamContext{
		TenantID:        "tenant-b",
		OriginClusterID: "cluster-b",
	}, time.Minute)

	trigger := &ipcpb.MistTrigger{TenantId: func() *string { s := "tenant-b"; return &s }()}
	processor.applyStreamContext(trigger, "shared-stream")

	if trigger.GetOriginClusterId() != "cluster-b" {
		t.Fatalf("expected tenant-b origin cluster, got %q", trigger.GetOriginClusterId())
	}
	if trigger.GetClusterId() != "cluster-local" {
		t.Fatalf("expected local emitting cluster, got %q", trigger.GetClusterId())
	}
}

func TestHandlePushRewrite_PopulatesClusterContextFields(t *testing.T) {
	prevRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-origin", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	response := &commodorepb.ValidateStreamKeyResponse{
		Valid:           true,
		TenantId:        "tenant-1",
		UserId:          "user-1",
		StreamId:        "stream-id-1",
		InternalName:    "stream-a",
		OriginClusterId: func() *string { s := "cluster-origin"; return &s }(),
	}
	commodoreClient, cleanup := setupCommodoreClient(t, response, nil)
	t.Cleanup(cleanup)

	processor := newTestProcessor(t)
	processor.commodoreClient = commodoreClient
	processor.clusterID = "cluster-local"

	trigger := &ipcpb.MistTrigger{
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
			PushRewrite: &ipcpb.PushRewriteTrigger{StreamName: "stream-a", Hostname: "127.0.0.1"},
		},
	}

	_, blocking, err := processor.handlePushRewrite(trigger)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if blocking {
		t.Fatal("expected PUSH_REWRITE to allow ingest (non-abort response)")
	}
	if trigger.GetOriginClusterId() != "cluster-origin" {
		t.Fatalf("expected origin_cluster_id to be populated, got %q", trigger.GetOriginClusterId())
	}
	if trigger.GetClusterId() != "cluster-origin" {
		t.Fatalf("expected cluster_id to use origin cluster, got %q", trigger.GetClusterId())
	}
}

func TestHandlePushRewrite_ValidatesUsingTriggerMediaCluster(t *testing.T) {
	prevRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "demo-media", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	response := &commodorepb.ValidateStreamKeyResponse{
		Valid:           true,
		TenantId:        "tenant-1",
		UserId:          "user-1",
		StreamId:        "stream-id-1",
		InternalName:    "stream-a",
		OriginClusterId: func() *string { s := "demo-media"; return &s }(),
	}
	commodoreClient, cleanup, stub := setupCommodoreClientWithStub(t, response, nil)
	t.Cleanup(cleanup)

	processor := newTestProcessor(t)
	processor.commodoreClient = commodoreClient
	processor.clusterID = "central-primary"

	mediaClusterID := "demo-media"
	trigger := &ipcpb.MistTrigger{
		NodeId:    "edge-node-1",
		ClusterId: &mediaClusterID,
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
			PushRewrite: &ipcpb.PushRewriteTrigger{StreamName: "stream-a", Hostname: "127.0.0.1"},
		},
	}

	_, blocking, err := processor.handlePushRewrite(trigger)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if blocking {
		t.Fatal("expected PUSH_REWRITE to allow ingest")
	}
	if got := stub.LastValidateClusterID(); got != "demo-media" {
		t.Fatalf("expected ValidateStreamKey cluster_id demo-media, got %q", got)
	}
	if trigger.GetClusterId() != "demo-media" {
		t.Fatalf("expected trigger cluster_id to remain demo-media, got %q", trigger.GetClusterId())
	}
}

// fakeGatewayDiscoverer satisfies livepeerGatewayDiscoverer in tests by returning
// a configured ServiceDiscoveryResponse per-cluster and counting calls per cluster
// so cache-hit/miss assertions are possible. Each cluster maps to zero or more
// instance hosts so broadcaster-fanout can be exercised.
type fakeGatewayDiscoverer struct {
	mu      sync.Mutex
	hosts   map[string][]string // cluster_id -> instance hosts (empty/absent => not registered)
	calls   map[string]int      // cluster_id -> DiscoverServices call count
	errOnce map[string]error    // cluster_id -> error to return ONCE then clear
}

func newFakeGatewayDiscoverer(hosts map[string][]string) *fakeGatewayDiscoverer {
	return &fakeGatewayDiscoverer{
		hosts:   hosts,
		calls:   map[string]int{},
		errOnce: map[string]error{},
	}
}

func (f *fakeGatewayDiscoverer) DiscoverServices(_ context.Context, serviceType, clusterID string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[clusterID]++

	if err, ok := f.errOnce[clusterID]; ok && err != nil {
		delete(f.errOnce, clusterID)
		return nil, err
	}

	if serviceType != "livepeer-gateway" {
		return &quartermasterpb.ServiceDiscoveryResponse{}, nil
	}

	resp := &quartermasterpb.ServiceDiscoveryResponse{}
	port := int32(443)
	for _, host := range f.hosts[clusterID] {
		if host == "" {
			continue
		}
		// public_instance_host carries the physical per-instance endpoint that
		// the broadcaster fanout prefers; mirror Quartermaster's metadata.
		resp.Instances = append(resp.Instances, &quartermasterpb.ServiceInstance{
			Host:         &host,
			Port:         &port,
			Protocol:     "https",
			HealthStatus: "healthy",
			Metadata: map[string]string{
				servicedefs.LivepeerGatewayMetadataPublicInstanceHost: host,
			},
		})
	}
	return resp, nil
}

func (f *fakeGatewayDiscoverer) callCount(clusterID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[clusterID]
}

func newGatewayProcessor(t *testing.T, disc livepeerGatewayDiscoverer, localCluster string) *Processor {
	t.Helper()
	p := newTestProcessor(t)
	p.clusterID = localCluster
	p.gatewayDiscoverer = disc
	return p
}

// Livepeer process configs no longer carry a {{gateway_url}} placeholder:
// Foghorn fills hardcoded_broadcasters structurally from discovery.
const gatewayTemplate = `[{"process":"Livepeer","source_track":"maxbps","track_select":"video=maxbps","target_profiles":[{"name":"360p","height":360}]}]`
const gatewayProfileTemplate = `[{"process":"Livepeer","source_track":"maxbps","track_select":"video=maxbps","target_profiles":[{"name":"360p","bitrate":900000,"fps":30,"height":360,"profile":"H264ConstrainedHigh","track_inhibit":"video=<640x360"}]}]`

// broadcasterAddresses extracts the address list from the (stringified) JSON
// hardcoded_broadcasters field of the first Livepeer process entry.
func broadcasterAddresses(t *testing.T, processesJSON string) []string {
	t.Helper()
	var processes []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(processesJSON), &processes); err != nil {
		t.Fatalf("parse processes JSON %q: %v", processesJSON, err)
	}
	for _, proc := range processes {
		var name string
		if err := json.Unmarshal(proc["process"], &name); err != nil || name != "Livepeer" {
			continue
		}
		var encoded string
		if err := json.Unmarshal(proc["hardcoded_broadcasters"], &encoded); err != nil {
			t.Fatalf("hardcoded_broadcasters not a string in %q: %v", processesJSON, err)
		}
		var entries []struct {
			Address string `json:"address"`
		}
		if err := json.Unmarshal([]byte(encoded), &entries); err != nil {
			t.Fatalf("parse hardcoded_broadcasters %q: %v", encoded, err)
		}
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.Address)
		}
		return out
	}
	t.Fatalf("no Livepeer process entry in %q", processesJSON)
	return nil
}

func TestApplyLivepeerBroadcasters_OriginWins(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string][]string{
		"selfhost-cluster": {"gw.selfhost.example.com"},
		"platform-cluster": {"gw.platform.example.com"},
	})
	p := newGatewayProcessor(t, disc, "foghorn-pool-1")

	got := p.ApplyLivepeerBroadcasters(gatewayTemplate, []string{"selfhost-cluster", "platform-cluster", p.clusterID})

	if !strings.Contains(got, "gw.selfhost.example.com") {
		t.Fatalf("expected origin (selfhost) gateway URL, got %q", got)
	}
	if strings.Contains(got, "gw.platform.example.com") {
		t.Fatalf("origin had a gateway, official should not have been used: %q", got)
	}
	if disc.callCount("platform-cluster") != 0 {
		t.Fatalf("origin won; should not have queried official cluster, got %d calls", disc.callCount("platform-cluster"))
	}
}

func TestApplyLivepeerBroadcasters_FansOutAllInstances(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string][]string{
		"platform-cluster": {"gw1.platform.example.com", "gw2.platform.example.com"},
	})
	p := newGatewayProcessor(t, disc, "foghorn-pool-1")

	got := p.ApplyLivepeerBroadcasters(gatewayTemplate, []string{"platform-cluster"})

	addrs := broadcasterAddresses(t, got)
	if len(addrs) != 2 {
		t.Fatalf("expected 2 broadcaster addresses, got %d: %v", len(addrs), addrs)
	}
	want := map[string]bool{
		"https://gw1.platform.example.com": false,
		"https://gw2.platform.example.com": false,
	}
	for _, a := range addrs {
		if _, ok := want[a]; !ok {
			t.Fatalf("unexpected broadcaster address %q in %v", a, addrs)
		}
		want[a] = true
	}
	for addr, seen := range want {
		if !seen {
			t.Fatalf("expected broadcaster %q in fanout, got %v", addr, addrs)
		}
	}
}

// staticDiscoverer returns a fixed instance set so a test can mix health states.
type staticDiscoverer struct {
	instances []*quartermasterpb.ServiceInstance
}

func (d *staticDiscoverer) DiscoverServices(_ context.Context, serviceType, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	if serviceType != "livepeer-gateway" {
		return &quartermasterpb.ServiceDiscoveryResponse{}, nil
	}
	return &quartermasterpb.ServiceDiscoveryResponse{Instances: d.instances}, nil
}

func TestApplyLivepeerBroadcasters_ExcludesUnhealthyInstances(t *testing.T) {
	healthy := "gw-healthy.example.com"
	unhealthy := "gw-unhealthy.example.com"
	port := int32(443)
	disc := &staticDiscoverer{instances: []*quartermasterpb.ServiceInstance{
		{Host: &healthy, Port: &port, Protocol: "https", HealthStatus: "healthy", Metadata: map[string]string{servicedefs.LivepeerGatewayMetadataPublicInstanceHost: healthy}},
		{Host: &unhealthy, Port: &port, Protocol: "https", HealthStatus: "unhealthy", Metadata: map[string]string{servicedefs.LivepeerGatewayMetadataPublicInstanceHost: unhealthy}},
	}}
	p := newGatewayProcessor(t, disc, "cluster-x")

	got := p.ApplyLivepeerBroadcasters(gatewayTemplate, []string{"cluster-x"})

	addrs := broadcasterAddresses(t, got)
	if len(addrs) != 1 || !strings.Contains(addrs[0], "gw-healthy") {
		t.Fatalf("expected only the healthy gateway in broadcasters, got %v", addrs)
	}
}

func TestApplyLivepeerBroadcasters_PooledOnlyInstanceFallsBackToLocalAV(t *testing.T) {
	// A healthy instance that advertises only a pooled public_host (no physical
	// public_instance_host) must not appear in the broadcaster list; with no
	// physical endpoints, processing falls back to local MistProcAV.
	pooled := "10.0.0.5"
	port := int32(443)
	disc := &staticDiscoverer{instances: []*quartermasterpb.ServiceInstance{
		{Host: &pooled, Port: &port, Protocol: "https", HealthStatus: "healthy", Metadata: map[string]string{
			servicedefs.LivepeerGatewayMetadataPublicHost: "livepeer.media-eu.frameworks.network",
		}},
	}}
	p := newGatewayProcessor(t, disc, "cluster-x")

	got := p.ApplyLivepeerBroadcasters(gatewayProfileTemplate, []string{"cluster-x"})

	if strings.Contains(got, "Livepeer") {
		t.Fatalf("expected pooled-only instance to be excluded and Livepeer replaced with local AV, got %q", got)
	}
	if !strings.Contains(got, `"process":"AV"`) {
		t.Fatalf("expected local AV fallback, got %q", got)
	}
}

func TestApplyLivepeerBroadcasters_DoesNotCacheDiscoveryErrors(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string][]string{
		"cluster-x": {"gw.x.example.com"},
	})
	disc.errOnce["cluster-x"] = errors.New("transient discovery failure")
	p := newGatewayProcessor(t, disc, "cluster-x")

	// First call hits the transient error → empty result, which must NOT cache.
	_ = p.ApplyLivepeerBroadcasters(gatewayProfileTemplate, []string{"cluster-x"})
	// Second call must re-resolve (error not cached) and now succeed.
	got := p.ApplyLivepeerBroadcasters(gatewayTemplate, []string{"cluster-x"})

	if !strings.Contains(got, "gw.x.example.com") {
		t.Fatalf("expected re-query to resolve after a transient error, got %q", got)
	}
	if disc.callCount("cluster-x") != 2 {
		t.Fatalf("expected 2 discovery calls (error not cached for the TTL), got %d", disc.callCount("cluster-x"))
	}
}

func TestApplyLivepeerBroadcasters_OfficialFallbackWhenOriginLacksGateway(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string][]string{
		"selfhost-cluster": nil, // origin advertises no gateway
		"platform-cluster": {"gw.platform.example.com"},
	})
	p := newGatewayProcessor(t, disc, "foghorn-pool-1")

	got := p.ApplyLivepeerBroadcasters(gatewayTemplate, []string{"selfhost-cluster", "platform-cluster", p.clusterID})

	if !strings.Contains(got, "gw.platform.example.com") {
		t.Fatalf("expected official gateway URL after origin miss, got %q", got)
	}
	if disc.callCount("selfhost-cluster") != 1 || disc.callCount("platform-cluster") != 1 {
		t.Fatalf("expected one call each: selfhost=%d platform=%d", disc.callCount("selfhost-cluster"), disc.callCount("platform-cluster"))
	}
}

func TestApplyLivepeerBroadcasters_FallsBackToLocalAVWhenNoCandidateHasGateway(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string][]string{
		"selfhost-cluster": nil,
		"platform-cluster": nil,
		"foghorn-pool-1":   nil,
	})
	p := newGatewayProcessor(t, disc, "foghorn-pool-1")

	got := p.ApplyLivepeerBroadcasters(gatewayProfileTemplate, []string{"selfhost-cluster", "platform-cluster", p.clusterID})

	if strings.Contains(got, "Livepeer") {
		t.Fatalf("expected Livepeer process to be converted to local AV, still present: %q", got)
	}
	if !strings.Contains(got, `"process":"AV"`) {
		t.Fatalf("expected local AV fallback process, got %q", got)
	}
	if !strings.Contains(got, `"resolution":"640x360"`) {
		t.Fatalf("expected target profile to become local AV resolution, got %q", got)
	}
	// Each candidate cluster should have been queried exactly once before falling back.
	for _, c := range []string{"selfhost-cluster", "platform-cluster", "foghorn-pool-1"} {
		if disc.callCount(c) != 1 {
			t.Fatalf("expected exactly 1 discovery call for %s, got %d", c, disc.callCount(c))
		}
	}
}

func TestApplyLivepeerBroadcasters_PerClusterCacheSeparation(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string][]string{
		"cluster-a": {"gw.a.example.com"},
		"cluster-b": {"gw.b.example.com"},
	})
	p := newGatewayProcessor(t, disc, "foghorn-pool-1")

	// First injection against cluster-a populates that cache entry.
	gotA := p.ApplyLivepeerBroadcasters(gatewayTemplate, []string{"cluster-a"})
	if !strings.Contains(gotA, "gw.a.example.com") {
		t.Fatalf("expected cluster-a gateway, got %q", gotA)
	}

	// Injection against cluster-b must NOT be served from cluster-a's cache.
	gotB := p.ApplyLivepeerBroadcasters(gatewayTemplate, []string{"cluster-b"})
	if !strings.Contains(gotB, "gw.b.example.com") {
		t.Fatalf("expected cluster-b gateway (separate cache), got %q", gotB)
	}
	if strings.Contains(gotB, "gw.a.example.com") {
		t.Fatalf("cluster-b lookup leaked cluster-a's cached URL: %q", gotB)
	}

	// Repeated injection against cluster-a within TTL must hit the cache.
	gotA2 := p.ApplyLivepeerBroadcasters(gatewayTemplate, []string{"cluster-a"})
	if !strings.Contains(gotA2, "gw.a.example.com") {
		t.Fatalf("expected cluster-a gateway on repeat, got %q", gotA2)
	}

	if disc.callCount("cluster-a") != 1 {
		t.Fatalf("expected cluster-a to be discovered exactly once (cache hit on repeat), got %d", disc.callCount("cluster-a"))
	}
	if disc.callCount("cluster-b") != 1 {
		t.Fatalf("expected cluster-b to be discovered exactly once, got %d", disc.callCount("cluster-b"))
	}
}

func TestApplyLivepeerBroadcasters_DeduplicatesRepeatedCandidates(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string][]string{
		"cluster-x": nil, // no gateway
	})
	p := newGatewayProcessor(t, disc, "cluster-x")

	// origin == official == p.clusterID; resolver should de-duplicate.
	got := p.ApplyLivepeerBroadcasters(gatewayProfileTemplate, []string{"cluster-x", "cluster-x", "cluster-x"})

	if !strings.Contains(got, `"process":"AV"`) {
		t.Fatalf("expected local AV fallback on full miss, got %q", got)
	}
	if disc.callCount("cluster-x") != 1 {
		t.Fatalf("expected cluster-x to be discovered exactly once despite being passed three times, got %d", disc.callCount("cluster-x"))
	}
}

func TestApplyLivepeerBroadcasters_NilCandidatesFallsBackToLocalCluster(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string][]string{
		"foghorn-pool-1": {"gw.local.example.com"},
	})
	p := newGatewayProcessor(t, disc, "foghorn-pool-1")

	// Nil candidates path is the queue-driven dispatcher's contract; preserves
	// today's single-cluster behavior using p.clusterID.
	got := p.ApplyLivepeerBroadcasters(gatewayTemplate, nil)

	if !strings.Contains(got, "gw.local.example.com") {
		t.Fatalf("expected local-cluster gateway via p.clusterID fallback, got %q", got)
	}
}

func TestApplyLivepeerBroadcasters_LocalFallbackIncrementsServiceUnavailableCounter(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string][]string{
		"selfhost-cluster": nil,
		"platform-cluster": nil,
	})
	p := newGatewayProcessor(t, disc, "foghorn-pool-1")

	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_service_resolution_rejected_total",
		Help: "test counter",
	}, []string{"reason", "service"})
	p.SetMetrics(&ProcessorMetrics{ServiceResolutionRejected: counter})

	// Hit path: official has gateway; counter must NOT increment.
	discHit := newFakeGatewayDiscoverer(map[string][]string{
		"selfhost-cluster": nil,
		"platform-cluster": {"gw.platform.example.com"},
	})
	pHit := newGatewayProcessor(t, discHit, "foghorn-pool-1")
	pHit.SetMetrics(&ProcessorMetrics{ServiceResolutionRejected: counter})
	_ = pHit.ApplyLivepeerBroadcasters(gatewayTemplate, []string{"selfhost-cluster", "platform-cluster"})

	if got := counterValue(t, counter.WithLabelValues("service_unavailable", "livepeer-gateway")); got != 0 {
		t.Fatalf("hit path should not increment service_unavailable counter, got %v", got)
	}

	// Miss path: every candidate empty; counter must increment exactly once.
	_ = p.ApplyLivepeerBroadcasters(gatewayTemplate, []string{"selfhost-cluster", "platform-cluster"})

	if got := counterValue(t, counter.WithLabelValues("service_unavailable", "livepeer-gateway")); got != 1 {
		t.Fatalf("miss path should increment service_unavailable counter exactly once, got %v", got)
	}
}

func TestApplyLivepeerBroadcasters_NoLivepeerProcessIsNoop(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string][]string{
		"any-cluster": {"gw.example.com"},
	})
	p := newGatewayProcessor(t, disc, "any-cluster")

	const noLivepeer = `[{"process":"AV","codec":"opus","track_select":"audio=all&video=none&subtitle=none"}]`
	got := p.ApplyLivepeerBroadcasters(noLivepeer, []string{"any-cluster"})

	if got != noLivepeer {
		t.Fatalf("expected Livepeer-free JSON to pass through unchanged, got %q", got)
	}
	if disc.callCount("any-cluster") != 0 {
		t.Fatalf("expected zero discovery calls when no Livepeer process present, got %d", disc.callCount("any-cluster"))
	}
}
