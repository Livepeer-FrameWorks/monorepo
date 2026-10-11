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

// A viewer of a DVR or clip replay ends with the replay's artifact hash and the
// source stream's id; a viewer of an uploaded VOD ends with the artifact hash
// alone. Daily stream analytics are a live stream's own audience: neither kind
// of view is counted on the source stream, and neither forms a row of its own
// under the zero stream id. The tenant's daily totals still count every view.
func TestArtifactViewsStayOutOfStreamDailyAnalytics_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, "fw-artifact-views-stream-daily").Native
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	ctx := context.Background()

	tenantID, streamID := uuid.NewString(), uuid.NewString()
	const replayHash, uploadHash = "20261005175800a1b2c3d4e5f60718", "20261005180000f1e2d3c4b5a69788"
	endedAt := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	userEnd := func(sessionID, streamName, streamIDValue, artifactHash string) {
		t.Helper()
		disconnect := &ipcpb.ViewerDisconnectTrigger{
			StreamName: streamName, SessionId: sessionID, Connector: "hls", Host: "203.0.113." + sessionID[len(sessionID)-1:],
			Duration: 120, UpBytes: 1 << 20, DownBytes: 1 << 10,
		}
		trigger := &ipcpb.MistTrigger{
			NodeId: "edge-artifact-1", TriggerType: "USER_END", RequestId: "user-end-" + sessionID,
			Timestamp: endedAt.UnixMilli(), TenantId: proto.String(tenantID), ClusterId: proto.String("cluster-eu"),
			TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{ViewerDisconnect: disconnect},
		}
		if streamIDValue != "" {
			trigger.StreamId = proto.String(streamIDValue)
			disconnect.StreamId = proto.String(streamIDValue)
		}
		if artifactHash != "" {
			trigger.ArtifactHash = proto.String(artifactHash)
			disconnect.ArtifactHash = proto.String(artifactHash)
		}
		payload, marshalErr := proto.Marshal(trigger)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if handleErr := h.HandleRawMistTriggerMessage(ctx, kafka.Message{Topic: "analytics.raw_mist_triggers", Value: payload, Headers: map[string]string{
			"tenant_id": tenantID, "cluster_id": "cluster-eu", "source_event_id": "user-end-" + sessionID,
			"trigger_type": "USER_END", "node_id": "edge-artifact-1",
		}}); handleErr != nil {
			t.Fatalf("USER_END %s: %v", sessionID, handleErr)
		}
	}
	userEnd("session-live-1", "live+daily", streamID, "")
	userEnd("session-replay-2", "vod+"+replayHash, streamID, replayHash)
	userEnd("session-upload-3", "vod+"+uploadHash, "", uploadHash)

	for _, view := range []string{"periscope.stream_analytics_daily_mv", "periscope.tenant_analytics_daily_mv"} {
		if execErr := conn.Exec(ctx, "SYSTEM REFRESH VIEW "+view); execErr != nil {
			t.Fatalf("refresh %s: %v", view, execErr)
		}
		if execErr := conn.Exec(ctx, "SYSTEM WAIT VIEW "+view); execErr != nil {
			t.Fatalf("wait %s: %v", view, execErr)
		}
	}

	tenant := uuid.MustParse(tenantID)
	if got := countRows(t, conn, `SELECT count() FROM periscope.stream_analytics_daily WHERE tenant_id = ? AND stream_id = toUUID('00000000-0000-0000-0000-000000000000')`, tenant); got != 0 {
		t.Errorf("daily stream analytics hold %d row(s) under the zero stream id", got)
	}
	var views uint64
	if err := conn.QueryRow(ctx, `SELECT total_views FROM periscope.stream_analytics_daily WHERE tenant_id = ? AND stream_id = ? AND day = toDate(?)`,
		tenant, uuid.MustParse(streamID), endedAt).Scan(&views); err != nil {
		t.Fatalf("read the stream's daily row: %v", err)
	}
	if views != 1 {
		t.Errorf("the stream's daily views = %d, want only its 1 live view", views)
	}
	var tenantViews uint64
	if err := conn.QueryRow(ctx, `SELECT total_views FROM periscope.tenant_analytics_daily WHERE tenant_id = ? AND day = toDate(?)`,
		tenant, endedAt).Scan(&tenantViews); err != nil {
		t.Fatalf("read the tenant's daily row: %v", err)
	}
	if tenantViews != 3 {
		t.Fatalf("the tenant's daily views = %d, want all 3", tenantViews)
	}
}
