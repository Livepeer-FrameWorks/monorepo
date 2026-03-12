package control

import (
	"testing"

	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProcessSyncComplete_NilRepo_EarlyReturn(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	artifactRepo = nil // override
	logger := logging.NewLogger()

	processSyncComplete(&pb.SyncComplete{
		AssetHash: "hash-1",
		Status:    "success",
	}, "node-1", logger)

	// No DB expectations set — should not query
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessSyncComplete_Success_WithS3URL(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts.*SET storage_location = 'local'.*sync_status = 'synced'").
		WithArgs("s3://bucket/key", true, "hash-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processSyncComplete(&pb.SyncComplete{
		AssetHash:    "hash-1",
		Status:       "success",
		S3Url:        "s3://bucket/key",
		DtshIncluded: true,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.syncStatusCalls) != 1 {
		t.Fatalf("expected 1 SetSyncStatus call, got %d", len(repo.syncStatusCalls))
	}
	if repo.syncStatusCalls[0].Status != "synced" {
		t.Fatalf("expected status=synced, got %s", repo.syncStatusCalls[0].Status)
	}
	if len(repo.addCachedNodeCalls) != 1 {
		t.Fatalf("expected 1 AddCachedNode call, got %d", len(repo.addCachedNodeCalls))
	}
	if repo.addCachedNodeCalls[0].NodeID != "node-1" {
		t.Fatalf("expected node-1, got %s", repo.addCachedNodeCalls[0].NodeID)
	}
}

func TestProcessSyncComplete_Success_RebuildsClipURL(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	// s3_url empty → triggers rebuild
	mock.ExpectQuery("SELECT.*FROM foghorn.artifacts.*WHERE artifact_hash").
		WithArgs("clip-hash").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_internal_name", "format", "tenant_id"}).
			AddRow("clip", "stream-1", "mp4", "tenant-1"))

	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'synced'").
		WithArgs(sqlmock.AnyArg(), false, "clip-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processSyncComplete(&pb.SyncComplete{
		AssetHash: "clip-hash",
		Status:    "success",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	repo.mu.Lock()
	if repo.syncStatusCalls[0].S3URL == "" {
		t.Fatal("expected rebuilt s3_url, got empty")
	}
	repo.mu.Unlock()
}

func TestProcessSyncComplete_Success_RebuildsDVRURL(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectQuery("SELECT.*FROM foghorn.artifacts").
		WithArgs("dvr-hash").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_internal_name", "format", "tenant_id"}).
			AddRow("dvr", "stream-dvr", "", "tenant-1"))

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs(sqlmock.AnyArg(), false, "dvr-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processSyncComplete(&pb.SyncComplete{
		AssetHash: "dvr-hash",
		Status:    "success",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessSyncComplete_Success_RebuildsVodURL(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectQuery("SELECT.*FROM foghorn.artifacts").
		WithArgs("vod-hash").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_internal_name", "format", "tenant_id"}).
			AddRow("vod", "stream-vod", "mkv", "tenant-1"))

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs(sqlmock.AnyArg(), false, "vod-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processSyncComplete(&pb.SyncComplete{
		AssetHash: "vod-hash",
		Status:    "success",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessSyncComplete_Success_UsesReportingNodeID(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs(sqlmock.AnyArg(), false, "hash-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processSyncComplete(&pb.SyncComplete{
		AssetHash: "hash-1",
		Status:    "success",
		S3Url:     "s3://pre-set",
		NodeId:    "reporting-node",
	}, "fallback-node", logger)

	repo.mu.Lock()
	if repo.addCachedNodeCalls[0].NodeID != "reporting-node" {
		t.Fatalf("expected reporting-node, got %s", repo.addCachedNodeCalls[0].NodeID)
	}
	repo.mu.Unlock()
}

func TestProcessSyncComplete_EvictedRemote(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	sm := state.DefaultManager()
	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "remote-hash", FilePath: "/data/remote.mp4"},
	})

	mock.ExpectExec("UPDATE foghorn.artifacts.*SET storage_location = 's3'.*sync_status = 'synced'").
		WithArgs("remote-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM foghorn.artifact_nodes").
		WithArgs("remote-hash", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processSyncComplete(&pb.SyncComplete{
		AssetHash: "remote-hash",
		Status:    "evicted_remote",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	repo.mu.Lock()
	if len(repo.syncStatusCalls) != 1 || repo.syncStatusCalls[0].Status != "synced" {
		t.Fatal("expected SetSyncStatus(synced)")
	}
	repo.mu.Unlock()

	// Verify artifact was removed from in-memory state
	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			for _, a := range n.Artifacts {
				if a.ClipHash == "remote-hash" {
					t.Fatal("artifact should have been removed from node state")
				}
			}
		}
	}
}

func TestProcessSyncComplete_Failed(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'failed'").
		WithArgs("connection reset", "hash-fail").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processSyncComplete(&pb.SyncComplete{
		AssetHash: "hash-fail",
		Status:    "failed",
		Error:     "connection reset",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	repo.mu.Lock()
	if len(repo.syncStatusCalls) != 1 || repo.syncStatusCalls[0].Status != "failed" {
		t.Fatal("expected SetSyncStatus(failed)")
	}
	repo.mu.Unlock()
}
