package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

func setupRepoTest(t *testing.T) (*artifactRepositoryDB, sqlmock.Sqlmock) {
	t.Helper()
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() {
		db = prevDB
		mockDB.Close()
	})
	return &artifactRepositoryDB{}, mock
}

func TestUpsertArtifacts_EmptyNoop(t *testing.T) {
	repo, _ := setupRepoTest(t)
	err := repo.UpsertArtifacts(context.Background(), "node-1", nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpsertArtifacts_NilDB(t *testing.T) {
	prevDB := db
	db = nil
	defer func() { db = prevDB }()

	repo := &artifactRepositoryDB{}
	err := repo.UpsertArtifacts(context.Background(), "node-1", []state.ArtifactRecord{{ArtifactHash: "h1"}})
	if !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("expected ErrConnDone, got %v", err)
	}
}

func TestUpsertArtifacts_InsertsWithFKGuard(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	// UPDATE lifecycle row
	mock.ExpectExec("UPDATE foghorn.artifacts SET").
		WithArgs("hash-1", "", int64(0), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// Prior read locks the row and drives transition detection (not present yet).
	expectPlacementParentLock(mock)
	expectArtifactDeletionWatermarkLock(mock, "hash-1", 0)
	mock.ExpectQuery("SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnError(sql.ErrNoRows)
	// INSERT with WHERE EXISTS FK guard, RETURNING the inserted flag + row role/completeness.
	mock.ExpectQuery("INSERT INTO foghorn.artifact_nodes.*WHERE EXISTS.*SELECT 1 FROM foghorn.artifacts.*RETURNING").
		WithArgs("hash-1", "node-1", "/data/clip.mp4", int64(1024), int64(0), int64(0), int64(0), int64(0), int64(0), "cache", false).
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_complete"}).AddRow("cache", false))
	// Newly present → durable GAINED in the same transaction.
	expectNodeCopyOutbox(mock, "hash-1")
	// Mark stale (no rows orphaned → no LOST).
	mock.ExpectQuery("UPDATE foghorn.artifact_nodes.*SET is_orphaned = true.*RETURNING artifact_hash, role").
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "role"}))
	mock.ExpectCommit()

	err := repo.UpsertArtifacts(context.Background(), "node-1", []state.ArtifactRecord{
		{ArtifactHash: "hash-1", FilePath: "/data/clip.mp4", SizeBytes: 1024},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpsertArtifacts_RetriesDeadlock(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts SET").
		WithArgs("hash-1", "", int64(0), int64(0)).
		WillReturnError(&pq.Error{Code: "40P01", Message: "deadlock detected"})
	mock.ExpectRollback()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts SET").
		WithArgs("hash-1", "", int64(0), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectPlacementParentLock(mock)
	expectArtifactDeletionWatermarkLock(mock, "hash-1", 0)
	mock.ExpectQuery("SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("INSERT INTO foghorn.artifact_nodes.*WHERE EXISTS.*SELECT 1 FROM foghorn.artifacts.*RETURNING").
		WithArgs("hash-1", "node-1", "", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), "cache", false).
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_complete"}).AddRow("cache", false))
	expectNodeCopyOutbox(mock, "hash-1")
	mock.ExpectQuery("UPDATE foghorn.artifact_nodes.*SET is_orphaned = true.*RETURNING artifact_hash, role").
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "role"}))
	mock.ExpectCommit()

	err := repo.UpsertArtifacts(context.Background(), "node-1", []state.ArtifactRecord{
		{ArtifactHash: "hash-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpsertArtifacts_RollbackOnError(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts SET").
		WithArgs("hash-1", "", int64(0), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectPlacementParentLock(mock)
	expectArtifactDeletionWatermarkLock(mock, "hash-1", 0)
	mock.ExpectQuery("SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("INSERT INTO foghorn.artifact_nodes").
		WithArgs("hash-1", "node-1", "", int64(0), int64(0), int64(0), int64(0), int64(0), "cache", false).
		WillReturnError(fmt.Errorf("FK violation"))
	mock.ExpectRollback()

	err := repo.UpsertArtifacts(context.Background(), "node-1", []state.ArtifactRecord{
		{ArtifactHash: "hash-1"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSetSyncStatus_Updates(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectExec("UPDATE foghorn.artifacts.*SET sync_status.*s3_url.*WHERE artifact_hash").
		WithArgs("hash-1", "s3://bucket/key").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.SetSyncStatus(context.Background(), "hash-1", "synced", "s3://bucket/key")
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An empty s3URL must PRESERVE the stored value (COALESCE), never clear durable attribution.
func TestSetSyncStatus_EmptyURLPreservesExisting(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectExec(`UPDATE foghorn.artifacts.*SET sync_status = 'synced',\s+s3_url = COALESCE\(NULLIF\(\$2::text, ''\), s3_url\)`).
		WithArgs("hash-1", "").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.SetSyncStatus(context.Background(), "hash-1", "synced", ""); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSetSyncStatus_NilDB(t *testing.T) {
	prevDB := db
	db = nil
	defer func() { db = prevDB }()

	repo := &artifactRepositoryDB{}
	err := repo.SetSyncStatus(context.Background(), "hash", "synced", "")
	if !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("expected ErrConnDone, got %v", err)
	}
}

func TestAddCachedNode(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	expectPlacementParentLock(mock)
	expectArtifactDeletionWatermarkLock(mock, "hash-1", 0)
	mock.ExpectQuery("SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("INSERT INTO foghorn.artifact_nodes.*ON CONFLICT.*DO UPDATE.*RETURNING").
		WithArgs("hash-1", "node-1", "", int64(0), int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"size_bytes"}).AddRow(nil))
	expectNodeCopyOutbox(mock, "hash-1")
	mock.ExpectCommit()

	err := repo.AddCachedNode(context.Background(), "hash-1", "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAddCachedNodeWithPath(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	expectPlacementParentLock(mock)
	expectArtifactDeletionWatermarkLock(mock, "hash-1", 0)
	mock.ExpectQuery("SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("INSERT INTO foghorn.artifact_nodes.*file_path.*size_bytes.*RETURNING").
		WithArgs("hash-1", "node-1", "/data/clip.mp4", int64(2048), int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"size_bytes"}).AddRow(int64(2048)))
	expectNodeCopyOutbox(mock, "hash-1")
	mock.ExpectCommit()

	err := repo.AddCachedNodeWithPath(context.Background(), "hash-1", "node-1", "/data/clip.mp4", 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A STALE whole-node orphan must be dropped: the atomic (connection_fence, seq) CAS finds a newer
// pair already stored (RETURNING no row), so the delayed report from a superseded connection can't
// orphan copies a newer report restored.
func TestMarkNodeArtifactsOrphaned_StaleReportRejected(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	// CAS: (fence 5, seq 90) loses to the stored pair → no row.
	mock.ExpectQuery(`INSERT INTO foghorn.node_artifact_report_watermark AS w .* ON CONFLICT .* WHERE \(w.connection_fence, w.seq\) < \(EXCLUDED.connection_fence, EXCLUDED.seq\)\s+RETURNING connection_fence`).
		WithArgs("node-1", int64(5), int64(90)).
		WillReturnError(sql.ErrNoRows)
	// No orphan UPDATE — the stale report is dropped and the tx rolls back.
	mock.ExpectRollback()

	if err := repo.MarkNodeArtifactsOrphaned(context.Background(), "node-1", 0, 5, 90); err != nil {
		t.Fatalf("stale report must be a no-op, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A FRESH orphan ((fence, seq) that beats the stored pair) applies via the CAS and proceeds.
func TestMarkNodeArtifactsOrphaned_FreshReportAppliesAndAdvancesWatermark(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	// CAS applies (RETURNING the fence).
	mock.ExpectQuery(`INSERT INTO foghorn.node_artifact_report_watermark AS w .* ON CONFLICT .* WHERE \(w.connection_fence, w.seq\) < \(EXCLUDED.connection_fence, EXCLUDED.seq\)\s+RETURNING connection_fence`).
		WithArgs("node-1", int64(8), int64(90)).
		WillReturnRows(sqlmock.NewRows([]string{"connection_fence"}).AddRow(int64(8)))
	mock.ExpectQuery("UPDATE foghorn.artifact_nodes.*SET is_orphaned = true.*WHERE node_id.*AND is_orphaned = false.*RETURNING artifact_hash, role").
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "role"}).AddRow("hash-1", "cache"))
	expectNodeCopyLostOutbox(mock, "hash-1")
	mock.ExpectCommit()

	if err := repo.MarkNodeArtifactsOrphaned(context.Background(), "node-1", 0, 8, 90); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMarkNodeArtifactsOrphaned(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE foghorn.artifact_nodes.*SET is_orphaned = true.*WHERE node_id.*AND is_orphaned = false.*RETURNING artifact_hash, role").
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "role"}).AddRow("hash-1", "cache"))
	// Each orphaned copy emits a durable LOST (last_emitted_version reset to 0).
	expectNodeCopyLostOutbox(mock, "hash-1")
	mock.ExpectCommit()

	err := repo.MarkNodeArtifactsOrphaned(context.Background(), "node-1", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMarkNodeArtifactsOrphaned_NilDB(t *testing.T) {
	prevDB := db
	db = nil
	defer func() { db = prevDB }()

	repo := &artifactRepositoryDB{}
	err := repo.MarkNodeArtifactsOrphaned(context.Background(), "node-1", 0, 0, 0)
	if !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("expected ErrConnDone, got %v", err)
	}
}

func TestAllocateNodeControlFenceRetriesSerializationFailure(t *testing.T) {
	_, mock := setupRepoTest(t)
	mock.ExpectQuery("INSERT INTO foghorn.node_control_fence_counter").
		WithArgs("node-1").
		WillReturnError(&pq.Error{Code: "40001", Message: "restart read required"})
	mock.ExpectQuery("INSERT INTO foghorn.node_control_fence_counter").
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(int64(4503599627370497)))

	fence, err := AllocateNodeControlFence(context.Background(), "node-1")
	if err != nil || fence != 4503599627370497 {
		t.Fatalf("AllocateNodeControlFence fence=%d err=%v", fence, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIsSynced(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectQuery("SELECT EXISTS.*FROM foghorn.artifacts.*sync_status = 'synced'").
		WithArgs("hash-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	synced, err := repo.IsSynced(context.Background(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if !synced {
		t.Fatal("expected synced=true")
	}
}
