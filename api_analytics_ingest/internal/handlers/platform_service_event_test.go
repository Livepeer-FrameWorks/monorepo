package handlers

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

func TestHandleServiceEventPlatformScopedClusterEventSkipsTenantAudit(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	event := kafka.ServiceEvent{
		EventID:   uuid.NewString(),
		EventType: "cluster_created",
		Timestamp: time.Now(),
		Source:    "quartermaster",
		Data:      map[string]interface{}{"cluster_id": "edge-eu"},
	}

	if err := handler.HandleServiceEvent(event); err != nil {
		t.Fatalf("platform-scoped cluster event failed: %v", err)
	}
	if ingestErrors := conn.batches["ingest_errors"]; ingestErrors != nil && len(ingestErrors.rows) > 0 {
		t.Fatalf("platform-scoped event wrote an ingest error: %#v", ingestErrors.rows)
	}
	if apiEvents := conn.batches["api_events"]; apiEvents != nil && len(apiEvents.rows) > 0 {
		t.Fatalf("platform-scoped event wrote a tenant audit row: %#v", apiEvents.rows)
	}
}
