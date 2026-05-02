package triggers

import (
	"context"
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
	"frameworks/pkg/cache"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

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

	newTrigger := func(artifacts ...*pb.StoredArtifact) *pb.MistTrigger {
		return &pb.MistTrigger{
			TriggerPayload: &pb.MistTrigger_NodeLifecycleUpdate{
				NodeLifecycleUpdate: &pb.NodeLifecycleUpdate{
					NodeId:    "node-1",
					Artifacts: artifacts,
				},
			},
		}
	}

	artifactA := &pb.StoredArtifact{
		ClipHash:     "hash-a",
		StreamName:   "stream-a",
		FilePath:     "/data/hash-a.mp4",
		SizeBytes:    100,
		CreatedAt:    1700000000,
		Format:       "mp4",
		ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
		AccessCount:  1,
		LastAccessed: 1700000001,
	}
	artifactASamePlacement := &pb.StoredArtifact{
		ClipHash:     "hash-a",
		StreamName:   "stream-a",
		FilePath:     "/data/hash-a.mp4",
		SizeBytes:    100,
		CreatedAt:    1700000000,
		Format:       "mp4",
		ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
		AccessCount:  99,
		LastAccessed: 1700000900,
	}
	artifactB := &pb.StoredArtifact{
		ClipHash:     "hash-b",
		StreamName:   "stream-b",
		FilePath:     "/data/hash-b.mp4",
		SizeBytes:    200,
		CreatedAt:    1700000100,
		Format:       "mp4",
		ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
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

	artifactAWithDtsh := &pb.StoredArtifact{
		ClipHash:     "hash-a",
		StreamName:   "stream-a",
		FilePath:     "/data/hash-a.mp4",
		SizeBytes:    100,
		CreatedAt:    1700000000,
		Format:       "mp4",
		HasDtsh:      true,
		ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
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

type stubCommodoreInternalService struct {
	pb.UnimplementedInternalServiceServer
	validateResponse          *pb.ValidateStreamKeyResponse
	validateErr               error
	mu                        sync.Mutex
	validateClusterIDs        []string
	resolveIdentifierResponse *pb.ResolveIdentifierResponse
	resolveIdentifierErr      error
}

func (s *stubCommodoreInternalService) ValidateStreamKey(ctx context.Context, req *pb.ValidateStreamKeyRequest) (*pb.ValidateStreamKeyResponse, error) {
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

func (s *stubCommodoreInternalService) ResolveIdentifier(ctx context.Context, req *pb.ResolveIdentifierRequest) (*pb.ResolveIdentifierResponse, error) {
	return s.resolveIdentifierResponse, s.resolveIdentifierErr
}

func newTestProcessor(t *testing.T) *Processor {
	t.Helper()
	return &Processor{
		logger: logging.Logger(logrus.New()),
		streamCache: cache.New(cache.Options{
			TTL:                  10 * time.Minute,
			StaleWhileRevalidate: 0,
			NegativeTTL:          0,
			MaxEntries:           100,
		}, cache.MetricsHooks{}),
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
	processor.streamCache.Set(tenantID+":"+internalName, streamContext{TenantID: tenantID}, time.Minute)

	viewerID := sm.CreateVirtualViewer(nodeID, internalName, clientIP)

	resp, abort, err := processor.handlePlayRewrite(&pb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &pb.MistTrigger_PlayRewrite{
			PlayRewrite: &pb.ViewerResolveTrigger{
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

	_, _, err = processor.handleUserNew(&pb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &pb.MistTrigger_ViewerConnect{
			ViewerConnect: &pb.ViewerConnectTrigger{
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

	_, _, err = processor.handlePlayRewrite(&pb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &pb.MistTrigger_PlayRewrite{
			PlayRewrite: &pb.ViewerResolveTrigger{
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

	_, _, err = processor.handleUserEnd(&pb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &pb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &pb.ViewerDisconnectTrigger{
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
	processor.streamCache.Set(tenantID+":"+internalName, streamContext{TenantID: tenantID}, time.Minute)

	resp, abort, err := processor.handleUserNew(&pb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &pb.MistTrigger_ViewerConnect{
			ViewerConnect: &pb.ViewerConnectTrigger{
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

	_, _, err := processor.handleUserEnd(&pb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &pb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &pb.ViewerDisconnectTrigger{
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

	_, _, err = processor.handleUserEnd(&pb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &pb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &pb.ViewerDisconnectTrigger{
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

	resp, abort, err := processor.handleUserNew(&pb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &pb.MistTrigger_ViewerConnect{
			ViewerConnect: &pb.ViewerConnectTrigger{
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

	_, _, err = processor.handleUserEnd(&pb.MistTrigger{
		NodeId:   nodeID,
		TenantId: &tenantID,
		TriggerPayload: &pb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &pb.ViewerDisconnectTrigger{
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

func setupCommodoreClient(t *testing.T, response *pb.ValidateStreamKeyResponse, responseErr error) (*commodore.GRPCClient, func()) {
	client, cleanup, _ := setupCommodoreClientWithStub(t, response, responseErr)
	return client, cleanup
}

func setupCommodoreClientWithStub(t *testing.T, response *pb.ValidateStreamKeyResponse, responseErr error) (*commodore.GRPCClient, func(), *stubCommodoreInternalService) {
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
	pb.RegisterInternalServiceServer(server, stub)

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

func setupCommodoreResolveIdentifierClient(t *testing.T, response *pb.ResolveIdentifierResponse, responseErr error) (*commodore.GRPCClient, func()) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	server := grpc.NewServer()
	pb.RegisterInternalServiceServer(server, &stubCommodoreInternalService{
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
	trigger := &pb.MistTrigger{
		StreamId: &streamID,
		TenantId: &tenantID,
		TriggerPayload: &pb.MistTrigger_ProcessBilling{
			ProcessBilling: &pb.ProcessBillingEvent{
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
	trigger := &pb.MistTrigger{
		StreamId: &streamID,
		TriggerPayload: &pb.MistTrigger_StorageLifecycleData{
			StorageLifecycleData: &pb.StorageLifecycleData{
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
	commodoreClient, cleanup := setupCommodoreResolveIdentifierClient(t, &pb.ResolveIdentifierResponse{
		Found:          true,
		TenantId:       "tenant-storage",
		UserId:         "user-storage",
		StreamId:       "stream-storage",
		IdentifierType: "clip_hash",
	}, nil)
	t.Cleanup(cleanup)

	processor := newTestProcessor(t)
	processor.commodoreClient = commodoreClient

	trigger := &pb.MistTrigger{
		TriggerPayload: &pb.MistTrigger_StorageLifecycleData{
			StorageLifecycleData: &pb.StorageLifecycleData{
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
	trigger := &pb.MistTrigger{
		StreamId: &streamID,
		TriggerPayload: &pb.MistTrigger_DvrLifecycleData{
			DvrLifecycleData: &pb.DVRLifecycleData{
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
	commodoreClient, cleanup := setupCommodoreResolveIdentifierClient(t, &pb.ResolveIdentifierResponse{
		Found:          true,
		TenantId:       "tenant-dvr",
		UserId:         "user-dvr",
		StreamId:       "stream-dvr",
		IdentifierType: "dvr_hash",
	}, nil)
	t.Cleanup(cleanup)

	processor := newTestProcessor(t)
	processor.commodoreClient = commodoreClient

	trigger := &pb.MistTrigger{
		TriggerPayload: &pb.MistTrigger_DvrLifecycleData{
			DvrLifecycleData: &pb.DVRLifecycleData{
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
	response := &pb.ValidateStreamKeyResponse{
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

	trigger := &pb.MistTrigger{
		TriggerPayload: &pb.MistTrigger_PushRewrite{
			PushRewrite: &pb.PushRewriteTrigger{
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
}

func TestHandlePushRewrite_RejectsSuspendedTenant(t *testing.T) {
	response := &pb.ValidateStreamKeyResponse{
		Valid:        true,
		TenantId:     "tenant-5",
		InternalName: "suspended-stream",
		IsSuspended:  true,
	}
	commodoreClient, cleanup := setupCommodoreClient(t, response, nil)
	t.Cleanup(cleanup)

	processor := newTestProcessor(t)
	processor.commodoreClient = commodoreClient

	trigger := &pb.MistTrigger{
		TriggerPayload: &pb.MistTrigger_PushRewrite{
			PushRewrite: &pb.PushRewriteTrigger{
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
	if ingestErr.Code != pb.IngestErrorCode_INGEST_ERROR_ACCOUNT_SUSPENDED {
		t.Fatalf("expected suspended error code, got %v", ingestErr.Code)
	}
}

func TestHandlePushRewrite_RejectsNegativeBalanceTenant(t *testing.T) {
	response := &pb.ValidateStreamKeyResponse{
		Valid:             true,
		TenantId:          "tenant-6",
		InternalName:      "negative-balance-stream",
		IsBalanceNegative: true,
	}
	commodoreClient, cleanup := setupCommodoreClient(t, response, nil)
	t.Cleanup(cleanup)

	processor := newTestProcessor(t)
	processor.commodoreClient = commodoreClient

	trigger := &pb.MistTrigger{
		TriggerPayload: &pb.MistTrigger_PushRewrite{
			PushRewrite: &pb.PushRewriteTrigger{
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
	if ingestErr.Code != pb.IngestErrorCode_INGEST_ERROR_PAYMENT_REQUIRED {
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

	trigger := &pb.MistTrigger{TenantId: func() *string { s := "tenant-1"; return &s }()}
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

	trigger := &pb.MistTrigger{TenantId: func() *string { s := "tenant-b"; return &s }()}
	processor.applyStreamContext(trigger, "shared-stream")

	if trigger.GetOriginClusterId() != "cluster-b" {
		t.Fatalf("expected tenant-b origin cluster, got %q", trigger.GetOriginClusterId())
	}
	if trigger.GetClusterId() != "cluster-local" {
		t.Fatalf("expected local emitting cluster, got %q", trigger.GetClusterId())
	}
}

func TestHandlePushRewrite_PopulatesClusterContextFields(t *testing.T) {
	response := &pb.ValidateStreamKeyResponse{
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

	trigger := &pb.MistTrigger{
		TriggerPayload: &pb.MistTrigger_PushRewrite{
			PushRewrite: &pb.PushRewriteTrigger{StreamName: "stream-a", Hostname: "127.0.0.1"},
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
	response := &pb.ValidateStreamKeyResponse{
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
	trigger := &pb.MistTrigger{
		NodeId:    "edge-node-1",
		ClusterId: &mediaClusterID,
		TriggerPayload: &pb.MistTrigger_PushRewrite{
			PushRewrite: &pb.PushRewriteTrigger{StreamName: "stream-a", Hostname: "127.0.0.1"},
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
// so cache-hit/miss assertions are possible.
type fakeGatewayDiscoverer struct {
	mu      sync.Mutex
	hosts   map[string]string // cluster_id -> public host (empty string => not registered)
	calls   map[string]int    // cluster_id -> DiscoverServices call count
	errOnce map[string]error  // cluster_id -> error to return ONCE then clear
}

func newFakeGatewayDiscoverer(hosts map[string]string) *fakeGatewayDiscoverer {
	return &fakeGatewayDiscoverer{
		hosts:   hosts,
		calls:   map[string]int{},
		errOnce: map[string]error{},
	}
}

func (f *fakeGatewayDiscoverer) DiscoverServices(_ context.Context, serviceType, clusterID string, _ *pb.CursorPaginationRequest) (*pb.ServiceDiscoveryResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[clusterID]++

	if err, ok := f.errOnce[clusterID]; ok && err != nil {
		delete(f.errOnce, clusterID)
		return nil, err
	}

	if serviceType != "livepeer-gateway" {
		return &pb.ServiceDiscoveryResponse{}, nil
	}

	host, registered := f.hosts[clusterID]
	if !registered || host == "" {
		return &pb.ServiceDiscoveryResponse{}, nil
	}
	port := int32(443)
	return &pb.ServiceDiscoveryResponse{
		Instances: []*pb.ServiceInstance{{
			Host:     &host,
			Port:     &port,
			Protocol: "https",
		}},
	}, nil
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

const gatewayTemplate = `[{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"{{gateway_url}}\"}]"}]`

func TestSubstituteGatewayURL_OriginWins(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string]string{
		"selfhost-cluster": "gw.selfhost.example.com",
		"platform-cluster": "gw.platform.example.com",
	})
	p := newGatewayProcessor(t, disc, "foghorn-pool-1")

	got := p.SubstituteGatewayURL(gatewayTemplate, []string{"selfhost-cluster", "platform-cluster", p.clusterID})

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

func TestSubstituteGatewayURL_OfficialFallbackWhenOriginLacksGateway(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string]string{
		"selfhost-cluster": "", // origin advertises no gateway
		"platform-cluster": "gw.platform.example.com",
	})
	p := newGatewayProcessor(t, disc, "foghorn-pool-1")

	got := p.SubstituteGatewayURL(gatewayTemplate, []string{"selfhost-cluster", "platform-cluster", p.clusterID})

	if !strings.Contains(got, "gw.platform.example.com") {
		t.Fatalf("expected official gateway URL after origin miss, got %q", got)
	}
	if disc.callCount("selfhost-cluster") != 1 || disc.callCount("platform-cluster") != 1 {
		t.Fatalf("expected one call each: selfhost=%d platform=%d", disc.callCount("selfhost-cluster"), disc.callCount("platform-cluster"))
	}
}

func TestSubstituteGatewayURL_StripsWhenNoCandidateHasGateway(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string]string{
		"selfhost-cluster": "",
		"platform-cluster": "",
		"foghorn-pool-1":   "",
	})
	p := newGatewayProcessor(t, disc, "foghorn-pool-1")

	got := p.SubstituteGatewayURL(gatewayTemplate, []string{"selfhost-cluster", "platform-cluster", p.clusterID})

	if strings.Contains(got, "{{gateway_url}}") {
		t.Fatalf("expected Livepeer process to be stripped, template still present: %q", got)
	}
	if strings.Contains(got, "Livepeer") {
		t.Fatalf("expected Livepeer process to be stripped, still present: %q", got)
	}
	// Each candidate cluster should have been queried exactly once before stripping.
	for _, c := range []string{"selfhost-cluster", "platform-cluster", "foghorn-pool-1"} {
		if disc.callCount(c) != 1 {
			t.Fatalf("expected exactly 1 discovery call for %s, got %d", c, disc.callCount(c))
		}
	}
}

func TestSubstituteGatewayURL_PerClusterCacheSeparation(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string]string{
		"cluster-a": "gw.a.example.com",
		"cluster-b": "gw.b.example.com",
	})
	p := newGatewayProcessor(t, disc, "foghorn-pool-1")

	// First substitution against cluster-a populates that cache entry.
	gotA := p.SubstituteGatewayURL(gatewayTemplate, []string{"cluster-a"})
	if !strings.Contains(gotA, "gw.a.example.com") {
		t.Fatalf("expected cluster-a gateway, got %q", gotA)
	}

	// Substitution against cluster-b must NOT be served from cluster-a's cache.
	gotB := p.SubstituteGatewayURL(gatewayTemplate, []string{"cluster-b"})
	if !strings.Contains(gotB, "gw.b.example.com") {
		t.Fatalf("expected cluster-b gateway (separate cache), got %q", gotB)
	}
	if strings.Contains(gotB, "gw.a.example.com") {
		t.Fatalf("cluster-b lookup leaked cluster-a's cached URL: %q", gotB)
	}

	// Repeated substitution against cluster-a within TTL must hit the cache.
	gotA2 := p.SubstituteGatewayURL(gatewayTemplate, []string{"cluster-a"})
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

func TestSubstituteGatewayURL_DeduplicatesRepeatedCandidates(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string]string{
		"cluster-x": "", // no gateway
	})
	p := newGatewayProcessor(t, disc, "cluster-x")

	// origin == official == p.clusterID; resolver should de-duplicate.
	got := p.SubstituteGatewayURL(gatewayTemplate, []string{"cluster-x", "cluster-x", "cluster-x"})

	if strings.Contains(got, "{{gateway_url}}") {
		t.Fatalf("expected Livepeer to be stripped on full miss, got %q", got)
	}
	if disc.callCount("cluster-x") != 1 {
		t.Fatalf("expected cluster-x to be discovered exactly once despite being passed three times, got %d", disc.callCount("cluster-x"))
	}
}

func TestSubstituteGatewayURL_NilCandidatesFallsBackToLocalCluster(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string]string{
		"foghorn-pool-1": "gw.local.example.com",
	})
	p := newGatewayProcessor(t, disc, "foghorn-pool-1")

	// Nil candidates path is the queue-driven dispatcher's contract; preserves
	// today's single-cluster behavior using p.clusterID.
	got := p.SubstituteGatewayURL(gatewayTemplate, nil)

	if !strings.Contains(got, "gw.local.example.com") {
		t.Fatalf("expected local-cluster gateway via p.clusterID fallback, got %q", got)
	}
}

func TestSubstituteGatewayURL_StripIncrementsServiceUnavailableCounter(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string]string{
		"selfhost-cluster": "",
		"platform-cluster": "",
	})
	p := newGatewayProcessor(t, disc, "foghorn-pool-1")

	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_service_resolution_rejected_total",
		Help: "test counter",
	}, []string{"reason", "service"})
	p.SetMetrics(&ProcessorMetrics{ServiceResolutionRejected: counter})

	// Hit path: official has gateway; counter must NOT increment.
	discHit := newFakeGatewayDiscoverer(map[string]string{
		"selfhost-cluster": "",
		"platform-cluster": "gw.platform.example.com",
	})
	pHit := newGatewayProcessor(t, discHit, "foghorn-pool-1")
	pHit.SetMetrics(&ProcessorMetrics{ServiceResolutionRejected: counter})
	_ = pHit.SubstituteGatewayURL(gatewayTemplate, []string{"selfhost-cluster", "platform-cluster"})

	if got := counterValue(t, counter.WithLabelValues("service_unavailable", "livepeer-gateway")); got != 0 {
		t.Fatalf("hit path should not increment service_unavailable counter, got %v", got)
	}

	// Miss path: every candidate empty; counter must increment exactly once.
	_ = p.SubstituteGatewayURL(gatewayTemplate, []string{"selfhost-cluster", "platform-cluster"})

	if got := counterValue(t, counter.WithLabelValues("service_unavailable", "livepeer-gateway")); got != 1 {
		t.Fatalf("miss path should increment service_unavailable counter exactly once, got %v", got)
	}
}

func TestSubstituteGatewayURL_NoTemplateIsNoop(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string]string{
		"any-cluster": "gw.example.com",
	})
	p := newGatewayProcessor(t, disc, "any-cluster")

	const noTemplate = `[{"process":"AV","codec":"opus"}]`
	got := p.SubstituteGatewayURL(noTemplate, []string{"any-cluster"})

	if got != noTemplate {
		t.Fatalf("expected template-free JSON to pass through unchanged, got %q", got)
	}
	if disc.callCount("any-cluster") != 0 {
		t.Fatalf("expected zero discovery calls when no template present, got %d", disc.callCount("any-cluster"))
	}
}
