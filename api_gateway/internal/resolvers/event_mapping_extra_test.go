package resolvers

import (
	"strings"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func int64ptr(v int64) *int64    { return &v }
func uint64ptr(v uint64) *uint64 { return &v }

func TestMapSignalmanStorageEvent(t *testing.T) {
	t.Run("nil_event", func(t *testing.T) {
		if mapSignalmanStorageEvent(nil) != nil {
			t.Fatal("expected nil for nil event")
		}
	})
	t.Run("non_storage_payload_returns_nil", func(t *testing.T) {
		ev := &signalmanpb.SignalmanEvent{Data: &signalmanpb.EventData{}}
		if mapSignalmanStorageEvent(ev) != nil {
			t.Fatal("expected nil when no storage payload")
		}
	})
	t.Run("maps_fields_and_lowercases_action", func(t *testing.T) {
		tenant := "tenant-1"
		ev := &signalmanpb.SignalmanEvent{
			Timestamp: timestamppb.New(time.Unix(1000, 0)),
			TenantId:  &tenant,
			Data: &signalmanpb.EventData{
				Payload: &signalmanpb.EventData_StorageLifecycle{
					StorageLifecycle: &ipcpb.StorageLifecycleData{
						Action:     ipcpb.StorageLifecycleData_ACTION_SYNCED,
						AssetType:  "dvr",
						AssetHash:  "hash-1",
						StreamId:   stringPtr("stream-1"),
						SizeBytes:  4096,
						NodeId:     stringPtr("node-1"),
						DurationMs: int64ptr(50),
					},
				},
			},
		}
		got := mapSignalmanStorageEvent(ev)
		if got == nil {
			t.Fatal("expected storage event")
		}
		if got.Action != "synced" {
			t.Errorf("action = %q, want synced", got.Action)
		}
		if got.StreamId != "stream-1" || got.AssetHash != "hash-1" || got.AssetType != "dvr" {
			t.Errorf("unexpected identity fields: %+v", got)
		}
		if got.SizeBytes != 4096 {
			t.Errorf("size = %d, want 4096", got.SizeBytes)
		}
		if got.TenantId != "tenant-1" {
			t.Errorf("tenant = %q, want tenant-1", got.TenantId)
		}
		if got.DurationMs == nil || *got.DurationMs != 50 {
			t.Errorf("durationMs = %v, want 50", got.DurationMs)
		}
		if !strings.HasPrefix(got.Id, "live:hash-1:synced:") {
			t.Errorf("id = %q, want prefix live:hash-1:synced:", got.Id)
		}
	})
}

func TestInt64PtrIfNonZero(t *testing.T) {
	if int64PtrIfNonZero(0) != nil {
		t.Error("expected nil for zero")
	}
	if got := int64PtrIfNonZero(7); got == nil || *got != 7 {
		t.Errorf("got %v, want 7", got)
	}
}

func TestMapSignalmanProcessingEvent(t *testing.T) {
	t.Run("nil_event", func(t *testing.T) {
		if mapSignalmanProcessingEvent(nil) != nil {
			t.Fatal("expected nil for nil event")
		}
	})
	t.Run("non_processing_payload_returns_nil", func(t *testing.T) {
		ev := &signalmanpb.SignalmanEvent{Data: &signalmanpb.EventData{}}
		if mapSignalmanProcessingEvent(ev) != nil {
			t.Fatal("expected nil when no processing payload")
		}
	})
	t.Run("payload_tenant_takes_precedence", func(t *testing.T) {
		eventTenant := "event-tenant"
		ev := &signalmanpb.SignalmanEvent{
			Timestamp: timestamppb.New(time.Unix(2000, 0)),
			TenantId:  &eventTenant,
			Data: &signalmanpb.EventData{
				Payload: &signalmanpb.EventData_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{
						StreamId:    stringPtr("stream-2"),
						NodeId:      "node-2",
						ProcessType: "Livepeer",
						DurationMs:  120,
						TenantId:    stringPtr("payload-tenant"),
					},
				},
			},
		}
		got := mapSignalmanProcessingEvent(ev)
		if got == nil {
			t.Fatal("expected processing record")
		}
		if got.TenantId != "payload-tenant" {
			t.Errorf("tenant = %q, want payload-tenant (payload wins over event)", got.TenantId)
		}
		if got.StreamId != "stream-2" || got.NodeId != "node-2" || got.ProcessType != "Livepeer" {
			t.Errorf("unexpected identity fields: %+v", got)
		}
		if got.DurationMs != 120 {
			t.Errorf("durationMs = %d, want 120", got.DurationMs)
		}
		if !strings.HasPrefix(got.Id, "live:stream-2:node-2:") {
			t.Errorf("id = %q, want prefix live:stream-2:node-2:", got.Id)
		}
	})
	t.Run("falls_back_to_event_tenant", func(t *testing.T) {
		eventTenant := "event-tenant"
		ev := &signalmanpb.SignalmanEvent{
			Timestamp: timestamppb.New(time.Unix(2000, 0)),
			TenantId:  &eventTenant,
			Data: &signalmanpb.EventData{
				Payload: &signalmanpb.EventData_ProcessBilling{
					ProcessBilling: &ipcpb.ProcessBillingEvent{StreamId: stringPtr("s"), NodeId: "n"},
				},
			},
		}
		got := mapSignalmanProcessingEvent(ev)
		if got == nil || got.TenantId != "event-tenant" {
			t.Errorf("tenant = %v, want event-tenant", got)
		}
	})
}

func TestMapSignalmanConnectionEvent(t *testing.T) {
	t.Run("unhandled_event_type_returns_nil", func(t *testing.T) {
		ev := &signalmanpb.SignalmanEvent{
			EventType: signalmanpb.EventType_EVENT_TYPE_UNSPECIFIED,
			Data:      &signalmanpb.EventData{},
		}
		if mapSignalmanConnectionEvent(ev) != nil {
			t.Fatal("expected nil for unhandled event type")
		}
	})
	t.Run("connect_maps_identity", func(t *testing.T) {
		ev := &signalmanpb.SignalmanEvent{
			EventType: signalmanpb.EventType_EVENT_TYPE_VIEWER_CONNECT,
			Timestamp: timestamppb.New(time.Unix(3000, 0)),
			Data: &signalmanpb.EventData{
				Payload: &signalmanpb.EventData_ViewerConnect{
					ViewerConnect: &ipcpb.ViewerConnectTrigger{
						StreamId:  stringPtr("stream-3"),
						SessionId: "sess-3",
					},
				},
			},
		}
		got := mapSignalmanConnectionEvent(ev)
		if got == nil {
			t.Fatal("expected connection event")
		}
		if got.EventType != "connect" {
			t.Errorf("eventType = %q, want connect", got.EventType)
		}
		if got.StreamId != "stream-3" || got.SessionId != "sess-3" {
			t.Errorf("unexpected identity: %+v", got)
		}
		if !strings.HasPrefix(got.EventId, "live:conn:stream-3:sess-3:") {
			t.Errorf("eventId = %q, want prefix live:conn:stream-3:sess-3:", got.EventId)
		}
	})
	t.Run("disconnect_sums_bytes_and_duration", func(t *testing.T) {
		ev := &signalmanpb.SignalmanEvent{
			EventType: signalmanpb.EventType_EVENT_TYPE_VIEWER_DISCONNECT,
			Timestamp: timestamppb.New(time.Unix(3000, 0)),
			Data: &signalmanpb.EventData{
				Payload: &signalmanpb.EventData_ViewerDisconnect{
					ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{
						StreamId:         stringPtr("stream-4"),
						SessionId:        "sess-4",
						SecondsConnected: uint64ptr(42),
						UpBytes:          100,
						DownBytes:        900,
					},
				},
			},
		}
		got := mapSignalmanConnectionEvent(ev)
		if got == nil {
			t.Fatal("expected connection event")
		}
		if got.EventType != "disconnect" {
			t.Errorf("eventType = %q, want disconnect", got.EventType)
		}
		if got.SessionDurationSeconds != 42 {
			t.Errorf("duration = %d, want 42", got.SessionDurationSeconds)
		}
		if got.BytesTransferred != 1000 {
			t.Errorf("bytes = %d, want 1000 (up+down)", got.BytesTransferred)
		}
	})
}
