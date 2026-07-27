package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// TestArtifactEventsCarryStableEventID pins the outbox-replay idempotency contract:
// each lifecycle handler must stamp the stable event_id (the outbox row id delivered as
// event.EventID) into the artifact_events history row. artifact_events is an append-only
// ReplicatedMergeTree (no engine collapse); a redelivered event is deduped at READ time by
// the artifact_events_deduped view, which keys a non-empty event_id to one row. event_id is
// the LAST column of the artifact_events INSERT, so it is the last appended value; the
// artifact_events write is the SECOND batch (rows[1]) after artifact_state_current (rows[0]).
func TestArtifactEventsCarryStableEventID(t *testing.T) {
	streamID := uuid.NewString()

	cl := &ipcpb.ClipLifecycleData{
		StreamInternalName: proto.String("live+clip"),
		ClipHash:           "cliphash1",
	}
	clipMT := &ipcpb.MistTrigger{
		StreamId:       proto.String(streamID),
		TriggerPayload: &ipcpb.MistTrigger_ClipLifecycleData{ClipLifecycleData: cl},
	}

	dvr := &ipcpb.DVRLifecycleData{DvrHash: "dvrhash1", StreamInternalName: proto.String("live+dvr")}
	dvrMT := &ipcpb.MistTrigger{
		StreamId:       proto.String(streamID),
		TriggerPayload: &ipcpb.MistTrigger_DvrLifecycleData{DvrLifecycleData: dvr},
	}

	vod := &ipcpb.VodLifecycleData{VodHash: "vodhash1"}
	vodMT := &ipcpb.MistTrigger{
		StreamId:       proto.String(streamID),
		TriggerPayload: &ipcpb.MistTrigger_VodLifecycleData{VodLifecycleData: vod},
	}

	for _, c := range []struct {
		name    string
		eventID string
		mt      *ipcpb.MistTrigger
	}{
		{
			name:    "clip",
			eventID: "evt-clip-42",
			mt:      clipMT,
		},
		{
			name:    "dvr",
			eventID: "evt-dvr-42",
			mt:      dvrMT,
		},
		{
			name:    "vod",
			eventID: "evt-vod-42",
			mt:      vodMT,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			batch := &captureBatch{}
			h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

			event := mistTriggerEvent(t, "tenant-1", time.Unix(1_700_000_000, 0).UTC(), c.mt)
			event.EventID = c.eventID // simulate the stable outbox row id on the wire

			var err error
			switch c.name {
			case "clip":
				err = h.processClipLifecycle(context.Background(), event)
			case "dvr":
				err = h.processDVRLifecycle(context.Background(), event)
			case "vod":
				err = h.processVodLifecycle(context.Background(), event)
			}
			if err != nil {
				t.Fatalf("process %s lifecycle: %v", c.name, err)
			}
			if len(batch.rows) != 2 {
				t.Fatalf("expected dual-write (artifact_state_current + artifact_events), got %d rows", len(batch.rows))
			}

			artifactEventsRow := batch.rows[1]
			gotEventID := artifactEventsRow[len(artifactEventsRow)-1]
			if gotEventID != c.eventID {
				t.Errorf("artifact_events event_id (last column) = %v, want %q", gotEventID, c.eventID)
			}
		})
	}
}
