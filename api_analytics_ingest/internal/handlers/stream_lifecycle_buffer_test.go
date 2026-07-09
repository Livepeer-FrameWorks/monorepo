package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
)

// TestStreamLifecycleDefaultsBufferStateForCurrentStateOnly pins the
// non-nullable-column workaround: stream_state_current.buffer_state must never
// be empty, so an absent buffer_state with a positive buffer_ms is materialized
// as "FULL". The historical stream_event_log keeps the raw (empty) value — the
// default is a current-state presentation concern, not a rewrite of what the
// node reported.
func TestStreamLifecycleDefaultsBufferStateForCurrentStateOnly(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	streamID := uuid.NewString()
	bufferMs := uint32(2000)
	data := mustMistTriggerData(t, &ipcpb.MistTrigger{
		StreamId: &streamID,
		NodeId:   "edge-eu-1",
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
				InternalName: "live+demo",
				Status:       "live",
				// BufferState left nil -> GetBufferState() == ""
				BufferMs: &bufferMs,
			},
		},
	})
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "stream_lifecycle_update",
		Timestamp: time.Unix(1710000000, 0),
		Source:    "decklog",
		TenantID:  uuid.NewString(),
		Data:      data,
	}

	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state := conn.batches["stream_state_current"]
	if state == nil || len(state.rows) != 1 {
		t.Fatalf("expected one stream_state_current row, got %#v", state)
	}
	if got := state.rows[0][5]; got != "FULL" {
		t.Fatalf("current-state buffer_state = %#v, want defaulted FULL", got)
	}

	log := conn.batches["stream_event_log"]
	if log == nil || len(log.rows) != 1 {
		t.Fatalf("expected one stream_event_log row, got %#v", log)
	}
	if got := log.rows[0][9]; got != "" {
		t.Fatalf("event-log buffer_state = %#v, want raw empty (default is current-state only)", got)
	}
}

// TestStreamLifecycleBufferStateNotDefaultedWithoutBuffer confirms the guard is
// conditional: with no buffer_ms signal there is nothing to infer, so an empty
// buffer_state stays empty rather than being asserted as FULL.
func TestStreamLifecycleBufferStateNotDefaultedWithoutBuffer(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	streamID := uuid.NewString()
	data := mustMistTriggerData(t, &ipcpb.MistTrigger{
		StreamId: &streamID,
		NodeId:   "edge-eu-1",
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
				InternalName: "live+demo",
				Status:       "live",
			},
		},
	})
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "stream_lifecycle_update",
		Timestamp: time.Unix(1710000000, 0),
		Source:    "decklog",
		TenantID:  uuid.NewString(),
		Data:      data,
	}

	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state := conn.batches["stream_state_current"]
	if state == nil || len(state.rows) != 1 {
		t.Fatalf("expected one stream_state_current row, got %#v", state)
	}
	if got := state.rows[0][5]; got != "" {
		t.Fatalf("buffer_state = %#v, want empty (no buffer_ms to infer FULL)", got)
	}
}

// TestStreamLifecycleSkipsInvalidStreamID proves the corruption guard: a
// lifecycle event whose resolved stream_id is not a UUID is dropped before any
// ClickHouse batch is opened, so it can never overwrite a real stream's current
// state with a garbage key.
func TestStreamLifecycleSkipsInvalidStreamID(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	badID := "not-a-uuid"
	data := mustMistTriggerData(t, &ipcpb.MistTrigger{
		StreamId: &badID,
		NodeId:   "edge-eu-1",
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{InternalName: "live+demo", Status: "live"},
		},
	})
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "stream_lifecycle_update",
		Timestamp: time.Unix(1710000000, 0),
		TenantID:  uuid.NewString(),
		Data:      data,
	}

	if err := handler.processStreamLifecycle(context.Background(), event); err != nil {
		t.Fatalf("invalid stream_id should skip cleanly, got err %v", err)
	}
	if len(conn.batches) != 0 {
		t.Fatalf("expected no batches for invalid stream_id, got %#v", conn.batches)
	}
}

// TestStreamLifecycleHonorsExplicitOfflineStatus pins the offline fast path:
// the poller's vanish diff reports a disappeared stream as an explicit
// status="offline" lifecycle update, and processStreamLifecycle must write
// that through to stream_state_current instead of applying its "live"
// default.
func TestStreamLifecycleHonorsExplicitOfflineStatus(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	streamID := uuid.NewString()
	inputs := uint32(0)
	viewers := uint32(0)
	bufferState := "EMPTY"
	data := mustMistTriggerData(t, &ipcpb.MistTrigger{
		StreamId: &streamID,
		NodeId:   "edge-eu-1",
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
				InternalName: "live+demo",
				Status:       "offline",
				BufferState:  &bufferState,
				TotalInputs:  &inputs,
				TotalViewers: &viewers,
			},
		},
	})
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "stream_lifecycle_update",
		Timestamp: time.Unix(1710000000, 0),
		Source:    "decklog",
		TenantID:  uuid.NewString(),
		Data:      data,
	}

	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state := conn.batches["stream_state_current"]
	if state == nil || len(state.rows) != 1 {
		t.Fatalf("expected one stream_state_current row, got %#v", state)
	}
	row := state.rows[0]
	if row[4] != "offline" {
		t.Fatalf("status = %#v, want explicit offline honored over the live default", row[4])
	}
	if row[5] != "EMPTY" {
		t.Fatalf("buffer_state = %#v, want EMPTY", row[5])
	}
}
