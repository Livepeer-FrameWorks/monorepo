//go:build schema_verify

package handlers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerch"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// One ingest session is projected into stream_runtime_5m twice: by the live
// branch while stream_state_current says live, and by the final branch once
// its STREAM_END lands. Mist reports a started_at one second after the
// stream_start event-log row the final projection resolves its start from.
// Both branches must write under one session key so the final rows replace
// the live rows, leaving the session's runtime counted once.
func TestStreamRuntimeLiveAndFinalShareOneSession_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, "fw-stream-runtime-session").Native
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	ctx := context.Background()

	const node, cluster, internal = "edge-a", "media-a", "runtime-session"
	tenantID, streamID := uuid.NewString(), uuid.NewString()
	startedAt := time.Now().UTC().Add(-time.Hour).Truncate(5 * time.Minute)
	endedAt := startedAt.Add(30 * time.Minute)

	if err := conn.Exec(ctx, `INSERT INTO periscope.stream_event_log
		(event_id, timestamp, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type, status, event_data)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'stream_start', 'live', '{}')`,
		uuid.New(), startedAt, uuid.MustParse(tenantID), uuid.MustParse(streamID), internal, node, cluster); err != nil {
		t.Fatalf("seed stream_start: %v", err)
	}
	if err := conn.Exec(ctx, `INSERT INTO periscope.stream_state_current
		(tenant_id, stream_id, internal_name, node_id, cluster_id, status, buffer_state,
		 current_viewers, total_inputs, uploaded_bytes, downloaded_bytes, viewer_seconds, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'live', 'FULL', 2, 1, 0, 0, 0, ?, ?)`,
		uuid.MustParse(tenantID), uuid.MustParse(streamID), internal, node, cluster,
		startedAt.Add(time.Second), endedAt.Add(-time.Minute)); err != nil {
		t.Fatalf("seed live stream_state_current: %v", err)
	}

	liveWindowStart := startedAt.Add(15 * time.Minute)
	if err := h.rebuildStreamRuntime5m(ctx, liveWindowStart, liveWindowStart.Add(5*time.Minute)); err != nil {
		t.Fatalf("live runtime rebuild: %v", err)
	}

	projectionStart := time.Now().UTC().Add(-time.Second)
	endID := uuid.NewString()
	deliverStreamEnds(t, conn, h, false, streamEndProducer{
		decklogAt: endedAt.Add(time.Second),
		trigger: &ipcpb.MistTrigger{
			TriggerType: "STREAM_END", NodeId: node, Timestamp: endedAt.Unix(), RequestId: endID, EventId: endID,
			TenantId: proto.String(tenantID), StreamId: proto.String(streamID), ClusterId: proto.String(cluster),
			TriggerPayload: &ipcpb.MistTrigger_StreamEnd{StreamEnd: &ipcpb.StreamEndTrigger{
				StreamName: "live+" + internal, TotalViewers: proto.Int64(2), TriggerUnixMillis: endedAt.UnixMilli(),
			}},
		},
	})
	projectionEnd := time.Now().UTC().Add(time.Second)

	var finalStartMS int64
	if err := conn.QueryRow(ctx, `SELECT source_started_at_ms FROM periscope.stream_sessions_final_v
		WHERE tenant_id = ? AND stream_id = ?`, tenantID, streamID).Scan(&finalStartMS); err != nil {
		t.Fatalf("read final session: %v", err)
	}
	if finalStartMS != startedAt.UnixMilli() {
		t.Fatalf("final session start = %d, want the event-log start %d", finalStartMS, startedAt.UnixMilli())
	}

	if err := h.rebuildStreamRuntime5m(ctx, projectionStart, projectionEnd); err != nil {
		t.Fatalf("final runtime rebuild: %v", err)
	}

	var sessions, runtimeSeconds uint64
	if err := conn.QueryRow(ctx, `SELECT uniqExact(source_event_id), sum(active_seconds)
		FROM periscope.stream_runtime_5m_v
		WHERE tenant_id = ? AND stream_id = ?`, tenantID, streamID).Scan(&sessions, &runtimeSeconds); err != nil {
		t.Fatalf("read stream runtime: %v", err)
	}
	if sessions != 1 || runtimeSeconds != 1800 {
		t.Fatalf("stream_runtime_5m_v sessions=%d runtime=%ds, want 1 session of 1800s", sessions, runtimeSeconds)
	}
}

// Foghorn delivers a push session's stream_start (its PUSH_REWRITE, whose
// event_id is the ingest session id) through the admission-effects worker, so
// the session's lifecycle reports can reach Periscope first. A live rebuild
// that runs in that gap resolves the session's start from the first lifecycle
// report, one second after the stream_start the final row later resolves its
// start from. Both branches must still write the session under one key: the
// ingest session id the lifecycle report and the stream_start both carry.
func TestStreamRuntimeLiveBeforeStreamStartSharesOneSession_RealClickHouse(t *testing.T) {
	f := newStreamRuntimeFixture(t, "fw-stream-runtime-late-start")
	sessionID := uuid.NewString()

	f.lifecycle(f.startedAt.Add(time.Second), sessionID)
	f.liveRebuild(f.startedAt.Add(15 * time.Minute))
	f.streamStart(f.startedAt, sessionID)
	f.endAndRebuild()

	f.requireOneSession()
}

// A publisher that reconnects within Mist's resume window opens a second
// ingest session on the same Mist stream, which ends with one STREAM_END. The
// final row spans both sessions from the first stream_start, so the live rows
// written during the second session stay keyed by the first session.
func TestStreamRuntimeReconnectKeepsTheFirstSessionKey_RealClickHouse(t *testing.T) {
	f := newStreamRuntimeFixture(t, "fw-stream-runtime-reconnect")
	first, second := uuid.NewString(), uuid.NewString()

	f.streamStart(f.startedAt, first)
	f.lifecycle(f.startedAt.Add(time.Second), first)
	f.liveRebuild(f.startedAt.Add(5 * time.Minute))
	f.streamStart(f.startedAt.Add(12*time.Minute), second)
	f.lifecycle(f.startedAt.Add(12*time.Minute+time.Second), second)
	f.liveRebuild(f.startedAt.Add(15 * time.Minute))
	f.endAndRebuild()

	f.requireOneSession()
}

// Live rows written before the ingest session id was recorded are keyed by
// the session's start. The session's final rows, keyed by its ingest session,
// zero them so the session keeps one runtime.
func TestStreamRuntimeFinalClearsStartKeyedLiveRows_RealClickHouse(t *testing.T) {
	f := newStreamRuntimeFixture(t, "fw-stream-runtime-start-keyed")
	sessionID := uuid.NewString()

	f.streamStart(f.startedAt, sessionID)
	startKey := streamRuntimeSessionKey(f.tenantID, f.node, f.streamID, f.startedAt.UnixMilli())
	for _, window := range []time.Duration{10 * time.Minute, 15 * time.Minute} {
		if err := f.conn.Exec(f.ctx, `INSERT INTO periscope.stream_runtime_5m
			(window_start, tenant_id, cluster_id, stream_id, source_event_id, active_seconds, peak_viewers, projection_version_ms)
			VALUES (?, ?, ?, ?, ?, 300, 2, ?)`,
			f.startedAt.Add(window), uuid.MustParse(f.tenantID), f.cluster, uuid.MustParse(f.streamID), startKey,
			time.Now().Add(-time.Hour).UnixMilli()); err != nil {
			t.Fatalf("seed start-keyed live row: %v", err)
		}
	}
	f.endAndRebuild()

	f.requireOneSession()
}

type streamRuntimeFixture struct {
	t                       *testing.T
	ctx                     context.Context
	conn                    database.ClickHouseNativeConn
	h                       *AnalyticsHandler
	node, cluster, internal string
	tenantID, streamID      string
	startedAt, endedAt      time.Time
}

func newStreamRuntimeFixture(t *testing.T, name string) *streamRuntimeFixture {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, name).Native
	startedAt := time.Now().UTC().Add(-time.Hour).Truncate(5 * time.Minute)
	return &streamRuntimeFixture{
		t: t, ctx: context.Background(), conn: conn, h: NewAnalyticsHandler(conn, logging.NewLogger(), nil),
		node: "edge-a", cluster: "media-a", internal: name,
		tenantID: uuid.NewString(), streamID: uuid.NewString(),
		startedAt: startedAt, endedAt: startedAt.Add(30 * time.Minute),
	}
}

func (f *streamRuntimeFixture) deliver(eventType string, at time.Time, trigger *ipcpb.MistTrigger) {
	f.t.Helper()
	trigger.NodeId = f.node
	trigger.TenantId = proto.String(f.tenantID)
	trigger.StreamId = proto.String(f.streamID)
	trigger.ClusterId = proto.String(f.cluster)
	event := kafka.AnalyticsEvent{
		EventID: trigger.GetEventId(), EventType: eventType, Timestamp: at, Source: "foghorn",
		TenantID: f.tenantID, Data: mustMistTriggerData(f.t, trigger),
	}
	if err := f.h.HandleAnalyticsEventContext(f.ctx, event); err != nil {
		f.t.Fatalf("deliver %s: %v", eventType, err)
	}
}

// lifecycle delivers the source's lifecycle report as Foghorn forwards it:
// Mist's started_at and the push session's ingest generation.
func (f *streamRuntimeFixture) lifecycle(mistStartedAt time.Time, sessionID string) {
	f.t.Helper()
	startedAtUnix := mistStartedAt.Unix()
	f.deliver("stream_lifecycle_update", mistStartedAt, &ipcpb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE", EventId: uuid.NewString(),
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
			NodeId: f.node, TenantId: proto.String(f.tenantID), InternalName: "live+" + f.internal, Status: "live",
			TotalViewers: proto.Uint32(2), TotalInputs: proto.Uint32(1), StartedAt: &startedAtUnix,
			StreamId: proto.String(f.streamID), IngestGeneration: proto.String(sessionID),
		}},
	})
}

// streamStart delivers the session's PUSH_REWRITE, whose event_id Foghorn
// sets to the ingest session id.
func (f *streamRuntimeFixture) streamStart(at time.Time, sessionID string) {
	f.t.Helper()
	f.deliver("push_rewrite", at, &ipcpb.MistTrigger{
		TriggerType: "PUSH_REWRITE", EventId: sessionID,
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: &ipcpb.PushRewriteTrigger{
			StreamName: "live+" + f.internal, NodeId: proto.String(f.node), StreamId: proto.String(f.streamID),
		}},
	})
}

func (f *streamRuntimeFixture) liveRebuild(windowStart time.Time) {
	f.t.Helper()
	if err := f.h.rebuildStreamRuntime5m(f.ctx, windowStart, windowStart.Add(5*time.Minute)); err != nil {
		f.t.Fatalf("live runtime rebuild: %v", err)
	}
}

// endAndRebuild delivers the session's STREAM_END and runs the final rebuild
// over its projection.
func (f *streamRuntimeFixture) endAndRebuild() {
	f.t.Helper()
	projectionStart := time.Now().UTC().Add(-time.Second)
	endID := uuid.NewString()
	deliverStreamEnds(f.t, f.conn, f.h, false, streamEndProducer{
		decklogAt: f.endedAt.Add(time.Second),
		trigger: &ipcpb.MistTrigger{
			TriggerType: "STREAM_END", NodeId: f.node, Timestamp: f.endedAt.Unix(), RequestId: endID, EventId: endID,
			TenantId: proto.String(f.tenantID), StreamId: proto.String(f.streamID), ClusterId: proto.String(f.cluster),
			TriggerPayload: &ipcpb.MistTrigger_StreamEnd{StreamEnd: &ipcpb.StreamEndTrigger{
				StreamName: "live+" + f.internal, TotalViewers: proto.Int64(2), TriggerUnixMillis: f.endedAt.UnixMilli(),
			}},
		},
	})
	var finalStartMS int64
	if err := f.conn.QueryRow(f.ctx, `SELECT source_started_at_ms FROM periscope.stream_sessions_final_v
		WHERE tenant_id = ? AND stream_id = ?`, f.tenantID, f.streamID).Scan(&finalStartMS); err != nil {
		f.t.Fatalf("read final session: %v", err)
	}
	if finalStartMS != f.startedAt.UnixMilli() {
		f.t.Fatalf("final session start = %d, want the first stream_start %d", finalStartMS, f.startedAt.UnixMilli())
	}
	if err := f.h.rebuildStreamRuntime5m(f.ctx, projectionStart, time.Now().UTC().Add(time.Second)); err != nil {
		f.t.Fatalf("final runtime rebuild: %v", err)
	}
}

func (f *streamRuntimeFixture) requireOneSession() {
	f.t.Helper()
	var sessions, runtimeSeconds uint64
	if err := f.conn.QueryRow(f.ctx, `SELECT uniqExactIf(source_event_id, active_seconds > 0), sum(active_seconds)
		FROM periscope.stream_runtime_5m_v
		WHERE tenant_id = ? AND stream_id = ?`, f.tenantID, f.streamID).Scan(&sessions, &runtimeSeconds); err != nil {
		f.t.Fatalf("read stream runtime: %v", err)
	}
	if sessions != 1 || runtimeSeconds != 1800 {
		f.t.Fatalf("stream_runtime_5m_v sessions=%d runtime=%ds, want 1 session of 1800s", sessions, runtimeSeconds)
	}
}
