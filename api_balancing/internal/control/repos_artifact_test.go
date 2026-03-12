package control

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
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
	if err != sql.ErrConnDone {
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
	// INSERT with WHERE EXISTS FK guard
	mock.ExpectExec("INSERT INTO foghorn.artifact_nodes.*WHERE EXISTS.*SELECT 1 FROM foghorn.artifacts").
		WithArgs("hash-1", "node-1", "/data/clip.mp4", int64(1024), int64(0), int64(0), int64(0), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Mark stale
	mock.ExpectExec("UPDATE foghorn.artifact_nodes.*SET is_orphaned = true").
		WithArgs("node-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
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

func TestUpsertArtifacts_RollbackOnError(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts SET").
		WithArgs("hash-1", "", int64(0), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO foghorn.artifact_nodes").
		WithArgs("hash-1", "node-1", "", int64(0), int64(0), int64(0), int64(0), int64(0)).
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
		WithArgs("hash-1", "synced", "s3://bucket/key").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.SetSyncStatus(context.Background(), "hash-1", "synced", "s3://bucket/key")
	if err != nil {
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
	if err != sql.ErrConnDone {
		t.Fatalf("expected ErrConnDone, got %v", err)
	}
}

func TestAddCachedNode(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectExec("INSERT INTO foghorn.artifact_nodes.*ON CONFLICT.*DO UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.AddCachedNode(context.Background(), "hash-1", "node-1")
	if err != nil {
		t.Fatal(err)
	}
}

func TestAddCachedNodeWithPath(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectExec("INSERT INTO foghorn.artifact_nodes.*file_path.*size_bytes").
		WithArgs("hash-1", "node-1", "/data/clip.mp4", int64(2048)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.AddCachedNodeWithPath(context.Background(), "hash-1", "node-1", "/data/clip.mp4", 2048)
	if err != nil {
		t.Fatal(err)
	}
}

func TestMarkNodeArtifactsOrphaned(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectExec("UPDATE foghorn.artifact_nodes.*SET is_orphaned = true.*WHERE node_id.*AND is_orphaned = false").
		WithArgs("node-1").
		WillReturnResult(sqlmock.NewResult(0, 5))

	err := repo.MarkNodeArtifactsOrphaned(context.Background(), "node-1")
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
	err := repo.MarkNodeArtifactsOrphaned(context.Background(), "node-1")
	if err != sql.ErrConnDone {
		t.Fatalf("expected ErrConnDone, got %v", err)
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
