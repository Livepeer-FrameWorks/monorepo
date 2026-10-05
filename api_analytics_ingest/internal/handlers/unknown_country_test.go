package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// A viewer whose address has no GeoIP country is stored with the same
// country marker on its connection event and on its final session, which
// feeds viewer-hours and geo rollups.
func TestUnknownCountryViewerUsesOneMarkerForSessionsAndViewerHours(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	tenantID := uuid.NewString()
	streamID := uuid.NewString()
	disconnect := &ipcpb.ViewerDisconnectTrigger{
		StreamName: "live+demo", StreamId: &streamID, SessionId: "sess-nogeo",
		Connector: "hls", Host: "10.1.2.3", Duration: 30,
	}
	trigger := &ipcpb.MistTrigger{
		NodeId: "edge-1", TriggerType: "USER_END", RequestId: "source-event-nogeo",
		Timestamp: time.Now().UnixMilli(), StreamId: &streamID,
		TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{ViewerDisconnect: disconnect},
	}

	if err := handler.HandleAnalyticsEvent(kafka.AnalyticsEvent{
		EventID: uuid.NewString(), EventType: "viewer_disconnect", Timestamp: time.Now(),
		Source: "decklog", TenantID: tenantID, Data: mustMistTriggerData(t, trigger),
	}); err != nil {
		t.Fatalf("viewer_disconnect: %v", err)
	}
	payload, err := proto.Marshal(trigger)
	if err != nil {
		t.Fatalf("marshal trigger: %v", err)
	}
	if err := handler.HandleRawMistTriggerMessage(context.Background(), kafka.Message{
		Value: payload,
		Headers: map[string]string{
			"tenant_id": tenantID, "source_event_id": "source-event-nogeo",
			"trigger_type": "USER_END", "node_id": "edge-1",
		},
		Topic: "analytics.raw_mist_triggers",
	}); err != nil {
		t.Fatalf("raw USER_END: %v", err)
	}

	connection := conn.batches["viewer_connection_events"]
	if connection == nil || len(connection.rows) != 1 {
		t.Fatalf("expected one viewer_connection_events row, got %#v", connection)
	}
	final := conn.batches["periscope.viewer_sessions_final"]
	if final == nil || len(final.rows) != 1 {
		t.Fatalf("expected one viewer_sessions_final row, got %#v", final)
	}
	sessionCountry := connection.rows[0][15] // viewer_connection_events.country_code
	finalCountry := final.rows[0][11]        // viewer_sessions_final.country_code
	if sessionCountry != "--" || finalCountry != "--" {
		t.Fatalf("unknown country: connection event %q, final session %q; want both %q", sessionCountry, finalCountry, "--")
	}
}
