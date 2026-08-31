package control

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// TestIsActiveDVRStatus enforces the lifecycle status set that gates
// active DVR routing: any of these → the rolling DVR surface fed by
// the recording origin's local artefacts; anything else → the stopped
// DVR resolver falls back to the most-recent playable chapter's VOD
// playback ID. The set must stay in sync with foghorn.artifacts.status
// semantics (see schema/foghorn.sql).
func TestIsActiveDVRStatus(t *testing.T) {
	active := []string{"requested", "starting", "recording"}
	for _, s := range active {
		if !IsActiveDVRStatus(s) {
			t.Errorf("IsActiveDVRStatus(%q) = false, want true", s)
		}
	}
	// 'finalizing' is excluded: FinalizeDVR has claimed the stop, the
	// rolling manifest is closing, and the stopped-DVR resolver should
	// fall back to the latest playable chapter.
	notActive := []string{"", "finalizing", "completed", "completed_partial", "failed", "deleted", "ready", "anything"}
	for _, s := range notActive {
		if IsActiveDVRStatus(s) {
			t.Errorf("IsActiveDVRStatus(%q) = true, want false", s)
		}
	}
}

// TestLocalRollingDVRManifestPath verifies the on-disk layout
// constructed for the recording origin's rolling DVR manifest. The
// path shape must match what the Mist push writer produces (see
// dvr_manager.go: targetURI uses `<outputDir>/<dvr_hash>.m3u8` with
// outputDir = storage/dvr/<stream_id>/<dvr_hash>/).
func TestLocalRollingDVRManifestPath(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	const nodeID = "node-recording-1"
	const storageRoot = "/srv/frameworks/storage"
	sm.SetNodeStoragePaths(nodeID, storageRoot, "", "")

	cases := []struct {
		name       string
		streamName string
		dvrHash    string
		node       string
		want       string
	}{
		{
			name:       "happy path",
			streamName: "5eedfeed-11fe-ca57-feed-11feca570001",
			dvrHash:    "fedcba98",
			node:       nodeID,
			want:       filepath.Join(storageRoot, "dvr", "5eedfeed-11fe-ca57-feed-11feca570001", "fedcba98", "fedcba98.m3u8"),
		},
		{
			name:       "unknown node falls back to defaultStorageBase",
			streamName: "5eedfeed-11fe-ca57-feed-11feca570001",
			dvrHash:    "fedcba98",
			node:       "node-does-not-exist",
			want:       filepath.Join(defaultStorageBase, "dvr", "5eedfeed-11fe-ca57-feed-11feca570001", "fedcba98", "fedcba98.m3u8"),
		},
		{
			name:       "missing stream name returns empty",
			streamName: "",
			dvrHash:    "fedcba98",
			node:       nodeID,
			want:       "",
		},
		{
			name:       "missing dvr hash returns empty",
			streamName: "stream_abc",
			dvrHash:    "",
			node:       nodeID,
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LocalRollingDVRManifestPath(tc.streamName, tc.dvrHash, tc.node)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveLocalDVRArtifactDispatchUsesSignedIdentityAndDurableRuntime(t *testing.T) {
	localDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB, previousCommodore := db, CommodoreClient
	SetDB(localDB)
	CommodoreClient = nil
	t.Cleanup(func() {
		SetDB(previousDB)
		CommodoreClient = previousCommodore
		_ = localDB.Close()
	})

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(artifact_type, '')::text AS artifact_type")).
		WithArgs("dvr-hash-local").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_id", "stream_internal_name"}).
			AddRow("dvr", "stream-id-local", "stream-internal-local"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status\nFROM foghorn.artifacts")).
		WithArgs("dvr-hash-local").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("recording"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT node_id, COALESCE(is_orphaned, false)::boolean AS is_orphaned")).
		WithArgs("dvr-hash-local").
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "is_orphaned"}).AddRow("edge-recording", false))

	artifact := &commodorepb.ResolveArtifactInternalNameResponse{
		Found: true, ArtifactHash: "dvr-hash-local", InternalName: "dvr-internal-local",
		StreamId: "stream-id-local", ContentType: "dvr", TenantId: "tenant-local",
	}
	dispatch, err := ResolveLocalDVRArtifactDispatch(context.Background(), artifact, "playback-local", false)
	if err != nil {
		t.Fatalf("ResolveLocalDVRArtifactDispatch: %v", err)
	}
	if dispatch == nil || dispatch.DVRHash != "dvr-hash-local" || dispatch.StreamID != "stream-id-local" ||
		dispatch.StreamInternalName != "stream-internal-local" || dispatch.PlaybackID != "playback-local" ||
		dispatch.Status != "recording" || dispatch.RecordingNode != "edge-recording" {
		t.Fatalf("local DVR dispatch = %+v", dispatch)
	}
	if CommodoreClient != nil {
		t.Fatal("test unexpectedly installed a central control-plane client")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveLocalDVRArtifactDispatchUsesSignedParentNameWithoutLocalArtifactRow(t *testing.T) {
	localDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB, previousCommodore := db, CommodoreClient
	SetDB(localDB)
	CommodoreClient = nil
	t.Cleanup(func() {
		SetDB(previousDB)
		CommodoreClient = previousCommodore
		_ = localDB.Close()
	})

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(artifact_type, '')::text AS artifact_type")).
		WithArgs("remote-dvr-hash").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status\nFROM foghorn.artifacts")).
		WithArgs("remote-dvr-hash").WillReturnError(sql.ErrNoRows)

	dispatch, err := ResolveLocalDVRArtifactDispatch(context.Background(), &commodorepb.ResolveArtifactInternalNameResponse{
		Found: true, ArtifactHash: "remote-dvr-hash", InternalName: "remote-dvr-internal",
		StreamId: "parent-stream-id", ParentStreamInternalName: "parent-routing-name",
		ContentType: "dvr", TenantId: "tenant-remote",
	}, "remote-playback", false)
	if err != nil {
		t.Fatalf("ResolveLocalDVRArtifactDispatch: %v", err)
	}
	if dispatch == nil || dispatch.StreamInternalName != "parent-routing-name" || dispatch.StreamID != "parent-stream-id" {
		t.Fatalf("signed cross-cluster DVR dispatch = %+v", dispatch)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveLocalDVRArtifactDispatchRejectsDurableIdentityConflict(t *testing.T) {
	localDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	SetDB(localDB)
	t.Cleanup(func() {
		SetDB(previousDB)
		_ = localDB.Close()
	})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(artifact_type, '')::text AS artifact_type")).
		WithArgs("dvr-hash-conflict").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_id", "stream_internal_name"}).
			AddRow("dvr", "different-stream-id", "stream-internal"))

	dispatch, err := ResolveLocalDVRArtifactDispatch(context.Background(), &commodorepb.ResolveArtifactInternalNameResponse{
		Found: true, ArtifactHash: "dvr-hash-conflict", InternalName: "dvr-internal",
		StreamId: "signed-stream-id", ContentType: "dvr", TenantId: "tenant-local",
	}, "playback-local", false)
	if err == nil || dispatch != nil {
		t.Fatalf("conflicting durable identity = dispatch:%+v err:%v", dispatch, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
