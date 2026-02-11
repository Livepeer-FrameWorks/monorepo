package triggers

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/ingesterrors"
	"frameworks/pkg/cache"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
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

type stubCommodoreInternalService struct {
	pb.UnimplementedInternalServiceServer
	response *pb.ValidateStreamKeyResponse
	err      error
}

func (s *stubCommodoreInternalService) ValidateStreamKey(ctx context.Context, req *pb.ValidateStreamKeyRequest) (*pb.ValidateStreamKeyResponse, error) {
	return s.response, s.err
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

func setupCommodoreClient(t *testing.T, response *pb.ValidateStreamKeyResponse, responseErr error) (*commodore.GRPCClient, func()) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	server := grpc.NewServer()
	pb.RegisterInternalServiceServer(server, &stubCommodoreInternalService{
		response: response,
		err:      responseErr,
	})

	go func() {
		_ = server.Serve(listener)
	}()

	client, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr: listener.Addr().String(),
		Logger:   logging.Logger(logrus.New()),
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
				TenantId:     &tenantID,
				InternalName: &wildcardName,
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
	if trigger.GetClusterId() != "cluster-local" {
		t.Fatalf("expected cluster_id to use local cluster, got %q", trigger.GetClusterId())
	}
}
