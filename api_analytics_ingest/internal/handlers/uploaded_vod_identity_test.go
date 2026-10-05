package handlers

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

// An uploaded VOD has no stream. Its storage lifecycle is named by its asset
// hash and its track list by the artifact Foghorn resolved; both are kept
// without a missing-stream warning.
func TestUploadedVODStorageAndTrackEventsKeepTheirArtifact(t *testing.T) {
	conn := newFakeClickhouseConn()
	logger, hook := logrustest.NewNullLogger()
	handler := NewAnalyticsHandler(conn, logger, nil)
	hash := testArtifactHash

	if err := handler.HandleAnalyticsEvent(artifactEvent(t, "storage_lifecycle", &ipcpb.MistTrigger{
		TriggerType: "STORAGE_LIFECYCLE", NodeId: "edge-1",
		TriggerPayload: &ipcpb.MistTrigger_StorageLifecycleData{StorageLifecycleData: &ipcpb.StorageLifecycleData{
			Action: ipcpb.StorageLifecycleData_ACTION_CACHED, AssetType: "vod", AssetHash: testArtifactHash,
		}},
	})); err != nil {
		t.Fatalf("storage_lifecycle: %v", err)
	}
	if err := handler.HandleAnalyticsEvent(artifactEvent(t, "stream_track_list", &ipcpb.MistTrigger{
		TriggerType: "LIVE_TRACK_LIST", NodeId: "edge-1", ArtifactHash: &hash,
		TriggerPayload: &ipcpb.MistTrigger_TrackList{TrackList: &ipcpb.StreamTrackListTrigger{StreamName: "vod+vodInternal"}},
	})); err != nil {
		t.Fatalf("stream_track_list: %v", err)
	}

	storage := conn.batches["storage_events"]
	if storage == nil || len(storage.rows) != 1 || storage.rows[0][4] != testArtifactHash {
		t.Fatalf("expected one storage_events row for the asset, got %#v", storage)
	}
	tracks := conn.batches["track_list_events"]
	if tracks == nil || len(tracks.rows) != 1 || tracks.rows[0][3] != uuid.Nil {
		t.Fatalf("expected one artifact-keyed track_list_events row, got %#v", tracks)
	}
	for _, entry := range hook.AllEntries() {
		if entry.Level <= logrus.WarnLevel {
			t.Fatalf("uploaded VOD event logged %s: %q", entry.Level, entry.Message)
		}
	}
}
