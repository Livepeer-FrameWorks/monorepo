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
	endedAt := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	projectionStart := time.Now().UTC().Add(-time.Second)

	makeMessage := func(duration int64) kafka.Message {
		trigger := &ipcpb.MistTrigger{
			NodeId: "edge-chain-1", TriggerType: "USER_END", RequestId: sourceID,
			Timestamp: endedAt.UnixMilli(), TenantId: proto.String(tenantID), ClusterId: proto.String("cluster-chain-1"), StreamId: proto.String(streamID),
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
			"tenant_id": tenantID, "cluster_id": "cluster-chain-1", "source_event_id": sourceID,
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
	projectionEnd := time.Now().UTC().Add(time.Second)

	var rawLogical, finalLogical uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM (SELECT source_request_id FROM periscope.raw_mist_triggers GROUP BY node_id, trigger_type, source_request_id)`).Scan(&rawLogical); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT count() FROM periscope.viewer_sessions_final_v WHERE tenant_id = ? AND node_id = ? AND session_id = ?`, tenantID, "edge-chain-1", "session-chain-1").Scan(&finalLogical); err != nil {
		t.Fatal(err)
	}
	if rawLogical != 1 || finalLogical != 1 {
		t.Fatalf("logical fact counts raw=%d final=%d, want 1/1", rawLogical, finalLogical)
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
