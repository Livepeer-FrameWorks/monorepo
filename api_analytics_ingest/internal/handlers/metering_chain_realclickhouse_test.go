//go:build schema_verify

package handlers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerch"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

func TestSourceFactProjectionAndLedgerReplay_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, "fw-metering-chain-ingest").Native
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	ctx := context.Background()

	tenantID := uuid.NewString()
	streamID := uuid.NewString()
	const sourceID = "metering-chain-user-end"
	const servingClusterID = "cluster-us"
	const originClusterID = "cluster-eu"
	const controlCellID = "control-eu"
	endedAt := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	projectionStart := time.Now().UTC().Add(-time.Second)

	makeMessage := func(duration int64) kafka.Message {
		trigger := &ipcpb.MistTrigger{
			NodeId: "edge-chain-1", TriggerType: "USER_END", RequestId: sourceID,
			Timestamp: endedAt.UnixMilli(), TenantId: proto.String(tenantID), ClusterId: proto.String(servingClusterID),
			OriginClusterId: proto.String(originClusterID), ControlCellId: proto.String(controlCellID), StreamId: proto.String(streamID),
			TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{
				StreamName: "live+chain", StreamId: proto.String(streamID), SessionId: "session-chain-1",
				Connector: "hls", Host: "203.0.113.1", Duration: duration, UpBytes: 2 << 30, DownBytes: 1 << 30,
			}},
		}
		payload, marshalErr := proto.Marshal(trigger)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return kafka.Message{Topic: "analytics.raw_mist_triggers", Value: payload, Headers: map[string]string{
			"tenant_id": tenantID, "cluster_id": servingClusterID, "source_event_id": sourceID,
			"trigger_type": "USER_END", "node_id": "edge-chain-1",
		}}
	}

	if err := h.HandleRawMistTriggerMessage(ctx, makeMessage(420)); err != nil {
		t.Fatalf("first source fact: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := h.HandleRawMistTriggerMessage(ctx, makeMessage(420)); err != nil {
		t.Fatalf("pure replay: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := h.HandleRawMistTriggerMessage(ctx, makeMessage(480)); err != nil {
		t.Fatalf("corrected replay: %v", err)
	}
	restreamGenerationID, restreamTargetID := uuid.NewString(), uuid.NewString()
	const restreamSourceID = "metering-chain-restream-final"
	makeRestreamMessage := func(durationMS int64, bytesSent uint64) kafka.Message {
		report := &ipcpb.PushTargetStatusReport{
			TargetId: restreamTargetID, TenantId: tenantID, StreamId: streamID, StreamName: "live+chain",
			SourceGeneration: restreamGenerationID, TargetRevision: 3, Platform: "youtube",
			State: ipcpb.RestreamState_RESTREAM_STATE_IDLE, Reason: ipcpb.RestreamReason_RESTREAM_REASON_COMPLETED,
			StartedAtMs: endedAt.UnixMilli() - durationMS, EndedAtMs: endedAt.UnixMilli(), DurationMs: durationMS,
			BytesSent: bytesSent, BytesObserved: true, SourceEventId: restreamSourceID,
		}
		trigger := &ipcpb.MistTrigger{
			NodeId: "edge-chain-1", TriggerType: "RESTREAM_STATUS_FINAL", RequestId: restreamSourceID,
			Timestamp: endedAt.UnixMilli(), TenantId: proto.String(tenantID), ClusterId: proto.String(servingClusterID),
			OriginClusterId: proto.String(originClusterID), ControlCellId: proto.String(controlCellID), StreamId: proto.String(streamID),
			TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: report},
		}
		payload, marshalErr := proto.Marshal(trigger)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return kafka.Message{Topic: "analytics.raw_mist_triggers", Value: payload, Headers: map[string]string{
			"tenant_id": tenantID, "cluster_id": servingClusterID, "source_event_id": restreamSourceID,
			"trigger_type": "RESTREAM_STATUS_FINAL", "node_id": "edge-chain-1",
		}}
	}
	if err := h.HandleRawMistTriggerMessage(ctx, makeRestreamMessage(600_000, 3<<30)); err != nil {
		t.Fatalf("first restream source fact: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := h.HandleRawMistTriggerMessage(ctx, makeRestreamMessage(600_000, 3<<30)); err != nil {
		t.Fatalf("restream pure replay: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := h.HandleRawMistTriggerMessage(ctx, makeRestreamMessage(660_000, 4<<30)); err != nil {
		t.Fatalf("restream corrected replay: %v", err)
	}
	projectionEnd := time.Now().UTC().Add(time.Second)

	var rawLogical, finalLogical uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM (SELECT source_request_id FROM periscope.raw_mist_triggers GROUP BY node_id, trigger_type, source_request_id)`).Scan(&rawLogical); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT count() FROM periscope.viewer_sessions_final_v WHERE tenant_id = ? AND node_id = ? AND session_id = ?`, tenantID, "edge-chain-1", "session-chain-1").Scan(&finalLogical); err != nil {
		t.Fatal(err)
	}
	if rawLogical != 2 || finalLogical != 1 {
		t.Fatalf("logical fact counts raw=%d viewer_final=%d, want 2/1", rawLogical, finalLogical)
	}
	var gotServing, gotOrigin, gotControl string
	if err := conn.QueryRow(ctx, `SELECT cluster_id, origin_cluster_id, control_cell_id
		FROM periscope.viewer_sessions_topology_v
		WHERE tenant_id = ? AND node_id = ? AND session_id = ?`, tenantID, "edge-chain-1", "session-chain-1").
		Scan(&gotServing, &gotOrigin, &gotControl); err != nil {
		t.Fatal(err)
	}
	if gotServing != servingClusterID || gotOrigin != originClusterID || gotControl != controlCellID {
		t.Fatalf("real ClickHouse placement serving=%q origin=%q control=%q, want %q/%q/%q",
			gotServing, gotOrigin, gotControl, servingClusterID, originClusterID, controlCellID)
	}
	var duration uint32
	var billableAtMS, latestProjectionMS int64
	if err := conn.QueryRow(ctx, `SELECT duration_seconds, billable_at_ms, latest_projection_version_ms FROM periscope.viewer_sessions_final_v WHERE tenant_id = ? AND node_id = ? AND session_id = ?`, tenantID, "edge-chain-1", "session-chain-1").Scan(&duration, &billableAtMS, &latestProjectionMS); err != nil {
		t.Fatal(err)
	}
	if duration != 480 || billableAtMS >= latestProjectionMS {
		t.Fatalf("materialized final duration=%d billable=%d latest=%d", duration, billableAtMS, latestProjectionMS)
	}
	var divergences uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM periscope.projection_divergences WHERE table_name = 'viewer_sessions_final' AND field = 'duration_seconds'`).Scan(&divergences); err != nil {
		t.Fatal(err)
	}
	if divergences != 1 {
		t.Fatalf("duration correction divergences=%d, want 1", divergences)
	}
	var restreamDuration, restreamBytes uint64
	if err := conn.QueryRow(ctx, `SELECT duration_ms, bytes_sent FROM periscope.restream_sessions_final_v
		WHERE tenant_id = ? AND node_id = ? AND source_event_id = ?`, tenantID, "edge-chain-1", restreamSourceID).
		Scan(&restreamDuration, &restreamBytes); err != nil {
		t.Fatal(err)
	}
	if restreamDuration != 660_000 || restreamBytes != 4<<30 {
		t.Fatalf("materialized restream duration=%d bytes=%d, want 660000/%d", restreamDuration, restreamBytes, uint64(4<<30))
	}
	var restreamDivergences uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM periscope.projection_divergences
		WHERE table_name = 'restream_sessions_final' AND field IN ('duration_ms', 'bytes_sent')`).Scan(&restreamDivergences); err != nil {
		t.Fatal(err)
	}
	if restreamDivergences != 2 {
		t.Fatalf("restream correction divergences=%d, want 2", restreamDivergences)
	}

	if err := h.rebuildViewerUsage5m(ctx, projectionStart, projectionEnd); err != nil {
		t.Fatalf("first ledger rebuild: %v", err)
	}
	if err := h.rebuildViewerUsage5m(ctx, projectionStart, projectionEnd); err != nil {
		t.Fatalf("replayed ledger rebuild: %v", err)
	}
	var ledgerSeconds uint64
	if err := conn.QueryRow(ctx, `SELECT sum(seconds_observed) FROM periscope.viewer_usage_5m_v WHERE tenant_id = ? AND node_id = ? AND session_id = ?`, tenantID, "edge-chain-1", "session-chain-1").Scan(&ledgerSeconds); err != nil {
		t.Fatal(err)
	}
	if ledgerSeconds != 480 {
		t.Fatalf("materialized ledger seconds=%d, want corrected 480", ledgerSeconds)
	}
	var physicalRows, logicalRows uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM periscope.viewer_usage_5m WHERE tenant_id = ?`, tenantID).Scan(&physicalRows); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT count() FROM periscope.viewer_usage_5m_v WHERE tenant_id = ?`, tenantID).Scan(&logicalRows); err != nil {
		t.Fatal(err)
	}
	if physicalRows != 2*logicalRows {
		t.Fatalf("ledger replay physical=%d logical=%d, want exact 2x collapse", physicalRows, logicalRows)
	}
	if err := h.rebuildDeliveryUsage5m(ctx, projectionStart, projectionEnd); err != nil {
		t.Fatalf("first delivery ledger rebuild: %v", err)
	}
	if err := h.rebuildDeliveryUsage5m(ctx, projectionStart, projectionEnd); err != nil {
		t.Fatalf("replayed delivery ledger rebuild: %v", err)
	}
	var restreamLedgerSeconds, restreamLedgerBytes uint64
	if err := conn.QueryRow(ctx, `SELECT sum(seconds_observed), sum(down_bytes_observed)
		FROM periscope.delivery_usage_5m_v WHERE tenant_id = ? AND node_id = ?
		  AND delivery_kind = 'restream' AND delivery_id = ? AND platform = 'youtube'`,
		tenantID, "edge-chain-1", restreamSourceID).Scan(&restreamLedgerSeconds, &restreamLedgerBytes); err != nil {
		t.Fatal(err)
	}
	if restreamLedgerSeconds != 660 || restreamLedgerBytes != 4<<30 {
		t.Fatalf("restream delivery ledger seconds=%d bytes=%d, want 660/%d", restreamLedgerSeconds, restreamLedgerBytes, uint64(4<<30))
	}

	apiTimestamp := endedAt.Add(-2 * time.Hour)
	apiIngestedMS := time.Now().UnixMilli()
	apiInsert := `INSERT INTO periscope.api_requests
		(timestamp,tenant_id,source_node,source_event_id,ingested_at_ms,auth_type,operation_name,operation_type,request_count,error_count,total_duration_ms,total_complexity,user_hashes,token_hashes)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	apiArgs := []any{apiTimestamp, tenantID, "skipper", "late-api-source:0", apiIngestedMS, "service", "Search", "skipper_search_query", uint32(3), uint32(1), uint64(120), uint32(9), []uint64{11, 12}, []uint64{21}}
	if err := conn.Exec(ctx, apiInsert, apiArgs...); err != nil {
		t.Fatal(err)
	}
	apiArgs[4] = apiIngestedMS + 1
	if err := conn.Exec(ctx, apiInsert, apiArgs...); err != nil {
		t.Fatal(err)
	}
	apiProjectionStart := time.UnixMilli(apiIngestedMS - 1)
	apiProjectionEnd := time.UnixMilli(apiIngestedMS + 2)
	if err := h.rebuildApiUsage5m(ctx, apiProjectionStart, apiProjectionEnd); err != nil {
		t.Fatalf("late API ledger rebuild: %v", err)
	}
	if err := h.rebuildApiUsage5m(ctx, apiProjectionStart, apiProjectionEnd); err != nil {
		t.Fatalf("replayed API ledger rebuild: %v", err)
	}
	var apiRequests uint64
	if err := conn.QueryRow(ctx, `SELECT sum(requests) FROM periscope.api_usage_5m_v WHERE tenant_id = ? AND operation_type = 'skipper_search_query'`, tenantID).Scan(&apiRequests); err != nil {
		t.Fatal(err)
	}
	if apiRequests != 3 {
		t.Fatalf("late/replayed API ledger requests=%d, want 3", apiRequests)
	}
}

// A regional final reaches the aggregator through MirrorMaker2 under a
// source-prefixed topic and can also be redelivered at least once. Both copies
// must converge to one raw journal row, one final fact, and one ledger entry.
func TestMirroredRawTriggerProjectsOnce_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, "fw-metering-mirror-ingest").Native
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	ctx := context.Background()

	tenantID := uuid.NewString()
	streamID := uuid.NewString()
	const sourceID = "mirrored-user-end"
	const nodeID = "edge-us-mirror-1"
	const sessionID = "session-us-mirror-1"
	endedAt := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	projectionStart := time.Now().UTC().Add(-time.Second)

	trigger := &ipcpb.MistTrigger{
		NodeId: nodeID, TriggerType: "USER_END", RequestId: sourceID,
		Timestamp: endedAt.UnixMilli(), TenantId: proto.String(tenantID), ClusterId: proto.String("media-us-1"),
		OriginClusterId: proto.String("media-us-1"), StreamId: proto.String(streamID),
		TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{
			StreamName: "live+mirror", StreamId: proto.String(streamID), SessionId: sessionID,
			Connector: "webrtc", Host: "198.51.100.7", Duration: 300, UpBytes: 1 << 20, DownBytes: 1 << 30,
		}},
	}
	payload, err := proto.Marshal(trigger)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{
		"tenant_id": tenantID, "cluster_id": "media-us-1", "source_event_id": sourceID,
		"trigger_type": "USER_END", "node_id": nodeID, "source_region": "us-east",
	}

	for _, topic := range []string{"us-east.analytics.raw_mist_triggers", "us-east.analytics.raw_mist_triggers", "analytics.raw_mist_triggers"} {
		if err := h.HandleRawMistTriggerMessage(ctx, kafka.Message{Topic: topic, Value: payload, Headers: headers}); err != nil {
			t.Fatalf("deliver via %s: %v", topic, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	projectionEnd := time.Now().UTC().Add(time.Second)

	var rawLogical, finalLogical uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM (SELECT source_request_id FROM periscope.raw_mist_triggers
		WHERE tenant_id = ? GROUP BY node_id, trigger_type, source_request_id)`, tenantID).Scan(&rawLogical); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT count() FROM periscope.viewer_sessions_final_v
		WHERE tenant_id = ? AND node_id = ? AND session_id = ?`, tenantID, nodeID, sessionID).Scan(&finalLogical); err != nil {
		t.Fatal(err)
	}
	if rawLogical != 1 || finalLogical != 1 {
		t.Fatalf("mirrored delivery logical counts raw=%d viewer_final=%d, want 1/1", rawLogical, finalLogical)
	}

	if err := h.rebuildViewerUsage5m(ctx, projectionStart, projectionEnd); err != nil {
		t.Fatalf("ledger rebuild: %v", err)
	}
	var ledgerSeconds uint64
	if err := conn.QueryRow(ctx, `SELECT sum(seconds_observed) FROM periscope.viewer_usage_5m_v
		WHERE tenant_id = ? AND node_id = ? AND session_id = ?`, tenantID, nodeID, sessionID).Scan(&ledgerSeconds); err != nil {
		t.Fatal(err)
	}
	if ledgerSeconds != 300 {
		t.Fatalf("mirrored delivery ledger seconds=%d, want 300", ledgerSeconds)
	}
}
