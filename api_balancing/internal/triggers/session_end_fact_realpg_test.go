//go:build schema_verify

package triggers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// A stream that ends while its edge's Helmsman is down loses Mist's STREAM_END (Mist does not retry
// it), so Foghorn learns of the end only from the poller's vanish. That vanish must still deliver the
// session's stream_end fact, and a STREAM_END for the same session arriving later must carry the same
// identity so Periscope keeps one fact for the session. A session started on the node after the end
// event belongs to a later Mist lifetime and is not the one that ended.
func TestVanishOnlyEndForwardsSessionEndFact_RealPG(t *testing.T) {
	conn := startFoghornTriggersRealPG(t)
	control.SetDB(conn)
	t.Cleanup(func() { control.SetDB(nil) })
	ctx := context.Background()

	tenant, streamID := uuid.NewString(), uuid.NewString()
	const (
		internal = "vanish-only-end"
		node     = "edge-a"
	)
	endedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	endMillis := endedAt.UnixMilli()
	var olderSession, endedSession, laterSession string
	for _, s := range []struct {
		id        *string
		pid       int64
		trigger   string
		startedMs int64
		endedMs   int64
	}{
		{&olderSession, 4241, "start-older", endMillis - 10*60_000, endMillis - 5*60_000},
		{&endedSession, 4242, "start-ended", endMillis - 2*60_000, endMillis - 15_000},
		{&laterSession, 4243, "start-later", endMillis + 5_000, endMillis + 30_000},
	} {
		if err := conn.QueryRowContext(ctx, `
			INSERT INTO foghorn.ingest_sessions
				(tenant_id, node_id, stream_internal_name, connector_pid, start_trigger_uuid,
				 started_at_unix_millis, ended_at, ended_at_unix_millis, ended_reason, projection_state, source_revision)
			VALUES ($1, $2, $3, $4, $5, $6, to_timestamp($7 / 1000.0), $7, 'push_input_close', 'active', 1)
			RETURNING id::text`,
			tenant, node, internal, s.pid, s.trigger, s.startedMs, s.endedMs).Scan(s.id); err != nil {
			t.Fatalf("seed ingest session %s: %v", s.trigger, err)
		}
	}
	wantID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("frameworks:ingest-session-end:"+endedSession)).String()

	reg := installRegistryForTest(t)
	projectSourceForTest(t, reg, internal, node, 4242, "start-ended", endedSession, 1)
	capture, client := startDecklogCapture(t)
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	p.decklogClient = client

	tenantID, sid := tenant, streamID
	vanish, err := proto.Marshal(&ipcpb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE", NodeId: node, Timestamp: endedAt.Unix(),
		TenantId: &tenantID, StreamId: &sid, ClusterId: proto.String("media-a"),
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
			TenantId: &tenantID, StreamId: &sid, NodeId: node, InternalName: internal, Status: "offline",
		}},
	})
	if err != nil {
		t.Fatalf("marshal vanish: %v", err)
	}
	legs, err := p.ApplyOfflineEffect(ctx, control.OfflineEffect{
		TenantID: tenant, InternalName: internal, NodeID: node, SourceGeneration: endedSession, SourceRevision: 2,
		DecklogTrigger: vanish,
	})
	if err != nil || !legs.DecklogDone {
		t.Fatalf("vanish ApplyOfflineEffect legs=%+v err=%v, want the Decklog leg done", legs, err)
	}
	var ends, lifecycles []*ipcpb.MistTrigger
	for _, got := range capture.received() {
		switch {
		case got.GetStreamEnd() != nil:
			ends = append(ends, got)
		case got.GetStreamLifecycleUpdate() != nil:
			lifecycles = append(lifecycles, got)
		}
	}
	if len(ends) != 1 {
		t.Fatalf("vanish-only end forwarded %d STREAM_END(s), want the session's inferred end once", len(ends))
	}
	end := ends[0]
	if end.GetTriggerType() != "STREAM_END" || end.GetRequestId() != wantID || end.GetEventId() != wantID ||
		end.GetTenantId() != tenant || end.GetStreamId() != streamID || end.GetNodeId() != node ||
		end.GetClusterId() != "media-a" || !end.GetStreamEnd().GetInferred() ||
		end.GetStreamEnd().GetStreamName() != "live+"+internal {
		t.Fatalf("inferred end = %v, want STREAM_END %s for stream %s on %s marked inferred", end, wantID, streamID, node)
	}
	if len(lifecycles) != 1 {
		t.Fatalf("vanish lifecycle forwarded %d time(s), want once", len(lifecycles))
	}

	reqID := "helmsman-stream-end-1"
	streamEnd, err := proto.Marshal(&ipcpb.MistTrigger{
		TriggerType: "STREAM_END", NodeId: node, Timestamp: endedAt.Unix(), RequestId: reqID,
		TenantId: &tenantID, StreamId: &sid,
		TriggerPayload: &ipcpb.MistTrigger_StreamEnd{StreamEnd: &ipcpb.StreamEndTrigger{
			StreamName: "live+" + internal, TriggerUnixMillis: endMillis - 500, UploadedBytes: proto.Int64(4096),
		}},
	})
	if err != nil {
		t.Fatalf("marshal STREAM_END: %v", err)
	}
	if _, err := p.ApplyOfflineEffect(ctx, control.OfflineEffect{
		TenantID: tenant, InternalName: internal, NodeID: node, SourceGeneration: endedSession, SourceRevision: 3,
		DecklogTrigger: streamEnd,
	}); err != nil {
		t.Fatalf("late STREAM_END ApplyOfflineEffect: %v", err)
	}
	ends = ends[:0]
	for _, got := range capture.received() {
		if got.GetStreamEnd() != nil {
			ends = append(ends, got)
		}
	}
	if len(ends) != 2 {
		t.Fatalf("Decklog received %d STREAM_END(s) after the late STREAM_END, want 2", len(ends))
	}
	late := ends[1]
	if late.GetRequestId() != wantID || late.GetEventId() != wantID || late.GetStreamEnd().GetInferred() ||
		late.GetStreamEnd().GetUploadedBytes() != 4096 {
		t.Fatalf("late STREAM_END = %v, want Mist's counters under the session end identity %s", late, wantID)
	}
}

// A Helmsman that restarts while a stream ends loses both of the stream's end reports: Mist's
// STREAM_END finds no listener and is not retried, and the restarted Helmsman's poller has no
// previous poll to diff, so it never reports the vanish. Foghorn still sees the node's snapshots:
// a stream the node listed before its current control connection and has not listed since ended in
// the gap, and its last ended session gets its end. A stream that disappears while the connection
// that listed it is up is left to Helmsman's own vanish report.
func TestStreamEndedAcrossHelmsmanRestartForwardsSessionEnd_RealPG(t *testing.T) {
	conn := startFoghornTriggersRealPG(t)
	control.SetDB(conn)
	t.Cleanup(func() { control.SetDB(nil) })
	ctx := context.Background()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	installRegistryForTest(t)

	tenant := uuid.NewString()
	const node = "edge-restart"
	now := time.Now().UTC()
	sessions := map[string]struct{ streamID, sessionID string }{}
	for i, internal := range []string{"ended-during-restart", "ended-while-connected"} {
		streamID, sessionID := uuid.NewString(), ""
		if err := conn.QueryRowContext(ctx, `
			INSERT INTO foghorn.ingest_sessions
				(tenant_id, node_id, stream_internal_name, connector_pid, start_trigger_uuid, stream_id,
				 started_at_unix_millis, ended_at, ended_at_unix_millis, ended_reason, projection_state, source_revision)
			VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), $8, 'push_input_close', 'active', 1)
			RETURNING id::text`,
			tenant, node, internal, 5000+i, "start-"+internal, streamID,
			now.Add(-10*time.Minute).UnixMilli(), now.Add(-time.Minute).UnixMilli()).Scan(&sessionID); err != nil {
			t.Fatalf("seed ingest session %s: %v", internal, err)
		}
		sessions[internal] = struct{ streamID, sessionID string }{streamID, sessionID}
	}

	capture, client := startDecklogCapture(t)
	p := newTestProcessor(t)
	p.decklogClient = client
	p.clusterID = "media-a"
	connectedSince := now.Add(-time.Hour)
	p.nodeConnectedSince = func(nodeID string) (time.Time, bool) { return connectedSince, nodeID == node }
	snapshot := func(names ...string) {
		t.Helper()
		streams := map[string]*ipcpb.StreamData{}
		for _, name := range names {
			streams["live+"+name] = &ipcpb.StreamData{Total: 1, Inputs: 1}
		}
		if _, _, err := p.handleNodeLifecycleUpdate(nodeLifecycleTrigger(&ipcpb.NodeLifecycleUpdate{
			NodeId: node, IsHealthy: true, EventType: "node_lifecycle_update", Timestamp: time.Now().Unix(), Streams: streams,
		})); err != nil {
			t.Fatal(err)
		}
	}
	ends := func() []*ipcpb.MistTrigger {
		var out []*ipcpb.MistTrigger
		for _, got := range capture.received() {
			if got.GetStreamEnd() != nil {
				out = append(out, got)
			}
		}
		return out
	}

	snapshot("ended-during-restart")
	time.Sleep(5 * time.Millisecond)
	connectedSince = time.Now()
	time.Sleep(5 * time.Millisecond)
	snapshot("ended-while-connected")
	snapshot()
	snapshot()

	got := ends()
	if len(got) != 1 {
		t.Fatalf("Decklog received %d STREAM_END(s), want the restart-gap session's end once: %v", len(got), got)
	}
	want := sessions["ended-during-restart"]
	wantID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("frameworks:ingest-session-end:"+want.sessionID)).String()
	end := got[0]
	if end.GetRequestId() != wantID || end.GetEventId() != wantID || end.GetTenantId() != tenant ||
		end.GetStreamId() != want.streamID || end.GetNodeId() != node || end.GetClusterId() != "media-a" ||
		!end.GetStreamEnd().GetInferred() || end.GetStreamEnd().GetStreamName() != "live+ended-during-restart" {
		t.Fatalf("restart-gap end = %v, want inferred STREAM_END %s for stream %s", end, wantID, want.streamID)
	}
}
