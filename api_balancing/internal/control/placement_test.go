package control

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"frameworks/api_balancing/internal/state"
)

func expectPlacementParentLock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT artifact_hash FROM foghorn.artifacts WHERE artifact_hash = \\$1 FOR UPDATE").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}).AddRow("locked"))
}

func expectArtifactDeletionWatermarkLock(mock sqlmock.Sqlmock, hash string, _ int64, nodeIDs ...string) {
	nodeID := "node-1"
	if len(nodeIDs) > 0 {
		nodeID = nodeIDs[0]
	}
	mock.ExpectQuery("SELECT deleted_at_ms FROM foghorn.artifact_node_deletion_watermark").
		WithArgs(hash, nodeID).
		WillReturnError(sql.ErrNoRows)
}

// expectNodeCopyOutbox sets up the tenant lookup + present-transition emit (GAINED /
// UPDATED, which records the row's live version).
func expectNodeCopyOutbox(mock sqlmock.Sqlmock, hash string) {
	mock.ExpectQuery("SELECT tenant_id::text FROM foghorn.artifacts").
		WithArgs(hash).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-1"))
	expectNodeCopyEmit(mock, hash, int64(1))
}

// expectNodeCopyLostOutbox is the LOST variant: last_emitted_version is reset to 0 (the
// presence mirror marks the copy absent in the projection).
func expectNodeCopyLostOutbox(mock sqlmock.Sqlmock, hash string) {
	mock.ExpectQuery("SELECT tenant_id::text FROM foghorn.artifacts").
		WithArgs(hash).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-1"))
	expectNodeCopyEmit(mock, hash, int64(0))
}

// expectNodeCopyEmit is the tenant-less tail (key-scoped version → last_emitted_version UPDATE →
// outbox insert) enqueueNodeCopy performs; rowVersion is what it records on the row
// (the live version for present events, 0 for LOST).
func expectNodeCopyEmit(mock sqlmock.Sqlmock, hash string, rowVersion int64) {
	mock.ExpectQuery("INSERT INTO foghorn.artifact_node_copy_version_counter").
		WithArgs(hash, "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(int64(1)))
	mock.ExpectExec("UPDATE foghorn.artifact_nodes SET last_emitted_version").
		WithArgs(rowVersion, hash, "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").
		WithArgs("artifact_node_copy", "tenant-1", "", hash, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// A finalize that first marks an artifact complete emits a durable GAINED (origin).
func TestRegisterOriginArtifact_EmitsGainedOnComplete(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	expectPlacementParentLock(mock)
	mock.ExpectQuery("SELECT role, is_complete, is_orphaned FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnError(sql.ErrNoRows) // not present yet
	mock.ExpectQuery("INSERT INTO foghorn.artifact_nodes.*'origin'.*RETURNING").
		WithArgs("hash-1", "node-1", "/f.mp4", int64(10), true).
		WillReturnRows(sqlmock.NewRows([]string{"is_complete"}).AddRow(true))
	expectNodeCopyOutbox(mock, "hash-1")
	mock.ExpectCommit()

	if err := repo.RegisterOriginArtifact(context.Background(), "hash-1", "node-1", "/f.mp4", 10, true); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Re-registering an already-complete origin does not re-emit.
func TestRegisterOriginArtifact_NoEmitWhenAlreadyComplete(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	expectPlacementParentLock(mock)
	mock.ExpectQuery("SELECT role, is_complete, is_orphaned FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_complete", "is_orphaned"}).AddRow("origin", true, false))
	mock.ExpectQuery("INSERT INTO foghorn.artifact_nodes.*'origin'.*RETURNING").
		WithArgs("hash-1", "node-1", "", int64(0), true).
		WillReturnRows(sqlmock.NewRows([]string{"is_complete"}).AddRow(true))
	// No tenant lookup, no version, no outbox row.
	mock.ExpectCommit()

	if err := repo.RegisterOriginArtifact(context.Background(), "hash-1", "node-1", "", 0, true); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Promoting a present, complete cache copy to origin emits UPDATED (role change).
func TestRegisterOriginArtifact_EmitsUpdatedOnPromotion(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	expectPlacementParentLock(mock)
	mock.ExpectQuery("SELECT role, is_complete, is_orphaned FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_complete", "is_orphaned"}).AddRow("cache", true, false))
	mock.ExpectQuery("INSERT INTO foghorn.artifact_nodes.*'origin'.*RETURNING").
		WithArgs("hash-1", "node-1", "", int64(0), true).
		WillReturnRows(sqlmock.NewRows([]string{"is_complete"}).AddRow(true))
	expectNodeCopyOutbox(mock, "hash-1")
	mock.ExpectCommit()

	if err := repo.RegisterOriginArtifact(context.Background(), "hash-1", "node-1", "", 0, true); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A sync-complete cache placement on a newly-present node (AddCachedNode) emits a
// durable GAINED (cache) when the node isn't already the artifact's origin.
func TestAddCachedNode_EmitsGainedCache(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	expectPlacementParentLock(mock)
	expectArtifactDeletionWatermarkLock(mock, "hash-1", 0)
	mock.ExpectQuery("SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnError(sql.ErrNoRows) // newly present
	mock.ExpectQuery("INSERT INTO foghorn.artifact_nodes.*'cache'.*RETURNING").
		WithArgs("hash-1", "node-1", "", int64(0), int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"size_bytes"}).AddRow(nil))
	expectNodeCopyOutbox(mock, "hash-1")
	mock.ExpectCommit()

	if err := repo.AddCachedNode(context.Background(), "hash-1", "node-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// AddCachedNode on an already-present, incomplete poller row (sync completing it)
// still emits (UPDATED) so the projection's is_complete flips false→true.
func TestAddCachedNode_EmitsOnCompletenessFlip(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	expectPlacementParentLock(mock)
	expectArtifactDeletionWatermarkLock(mock, "hash-1", 0)
	mock.ExpectQuery("SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_orphaned", "is_complete"}).AddRow("cache", false, false)) // present but incomplete
	mock.ExpectQuery("INSERT INTO foghorn.artifact_nodes.*'cache'.*RETURNING").
		WithArgs("hash-1", "node-1", "", int64(0), int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"size_bytes"}).AddRow(int64(2048))) // already present
	expectNodeCopyOutbox(mock, "hash-1")
	mock.ExpectCommit()

	if err := repo.AddCachedNode(context.Background(), "hash-1", "node-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Explicit deletion/eviction of a node's copy emits a durable LOST.
func TestDeleteNodeArtifact_EmitsLost(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	expectPlacementParentLock(mock)
	mock.ExpectQuery("WITH deletion_watermark AS .*DELETE FROM foghorn.artifact_nodes").
		WithArgs("hash-1", "node-1", int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("cache"))
	expectNodeCopyLostOutbox(mock, "hash-1")
	mock.ExpectCommit()

	if _, err := repo.DeleteNodeArtifact(context.Background(), "hash-1", "node-1", 0); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteNodeArtifact_StaleReplayDoesNotDeleteReacquiredPlacement(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	expectPlacementParentLock(mock)
	mock.ExpectQuery("WITH deletion_watermark AS .*DELETE FROM foghorn.artifact_nodes").
		WithArgs("hash-1", "node-1", int64(1234)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_orphaned", "is_complete"}).AddRow("cache", false, true))
	mock.ExpectCommit()

	outcome, err := repo.DeleteNodeArtifact(context.Background(), "hash-1", "node-1", 1234)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != state.NodeArtifactDeletionFenced {
		t.Fatalf("stale replay outcome=%q, want fenced", outcome)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteNodeArtifact_DistinguishesAbsentCopy(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	expectPlacementParentLock(mock)
	mock.ExpectQuery("WITH deletion_watermark AS .*DELETE FROM foghorn.artifact_nodes").
		WithArgs("hash-1", "node-1", int64(1234)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectCommit()

	outcome, err := repo.DeleteNodeArtifact(context.Background(), "hash-1", "node-1", 1234)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != state.NodeArtifactDeletionAbsent {
		t.Fatalf("duplicate deletion outcome=%q, want absent", outcome)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteNodeArtifact_DistinguishesMissingParent(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT artifact_hash FROM foghorn.artifacts WHERE artifact_hash = \\$1 FOR UPDATE").
		WithArgs("hash-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectCommit()

	outcome, err := repo.DeleteNodeArtifact(context.Background(), "hash-1", "node-1", 1234)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != state.NodeArtifactDeletionParentMissing {
		t.Fatalf("missing-parent deletion outcome=%q, want parent_missing", outcome)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A poller report that restores a previously-orphaned artifact emits GAINED — so
// ClickHouse doesn't stay present=false after a disconnect→reconnect.
func TestUpsertArtifacts_ReconnectEmitsGained(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts SET").
		WithArgs("hash-1", "", int64(0), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectPlacementParentLock(mock)
	expectArtifactDeletionWatermarkLock(mock, "hash-1", 0)
	mock.ExpectQuery("SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-1", "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_orphaned", "is_complete"}).AddRow("cache", true, false)) // was orphaned
	mock.ExpectQuery("INSERT INTO foghorn.artifact_nodes.*WHERE EXISTS.*RETURNING").
		WithArgs("hash-1", "node-1", "", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), "cache", false).
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_complete"}).AddRow("cache", false))
	expectNodeCopyOutbox(mock, "hash-1")
	// stale sweep finds nothing this pass
	mock.ExpectQuery("UPDATE foghorn.artifact_nodes.*is_orphaned = true.*RETURNING artifact_hash, role").
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "role"}))
	mock.ExpectCommit()

	if err := repo.UpsertArtifacts(context.Background(), "node-1", []state.ArtifactRecord{{ArtifactHash: "hash-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A versioned whole-node report upserts its present artifacts but does NOT run a scan-driven negative
// diff: absent rows are NOT orphaned in-tx. Report {A} on a node durably holding {A,B} upserts A and
// leaves B to the stale sweep — B is NOT LOST here.
func TestUpsertArtifacts_VersionedReportDefersNegativeDiffToStaleSweep(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	// Versioned report → revision-guard watermark upsert accepts it.
	mock.ExpectQuery("INSERT INTO foghorn.node_artifact_report_watermark").
		WithArgs("node-1", int64(5), int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"connection_fence"}).AddRow(int64(5)))
	// Present artifact A: meta update, FOR UPDATE re-read (already present+complete, unchanged → no
	// emit), upsert returns unchanged.
	mock.ExpectExec("UPDATE foghorn.artifacts SET").
		WithArgs("hash-a", "", int64(0), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectPlacementParentLock(mock)
	expectArtifactDeletionWatermarkLock(mock, "hash-a", 0)
	mock.ExpectQuery("SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes.*FOR UPDATE").
		WithArgs("hash-a", "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_orphaned", "is_complete"}).AddRow("cache", false, false))
	mock.ExpectQuery("INSERT INTO foghorn.artifact_nodes.*WHERE EXISTS.*RETURNING").
		WithArgs("hash-a", "node-1", "", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), "cache", false).
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_complete"}).AddRow("cache", false))
	// Whole-node reports do NOT perform scan-driven negative diffing: the versioned report upserts A but
	// does NOT immediately orphan the absent B. B is reconciled by the stale sweep / cordon /
	// fenced-takeover, never by a same-tx authoritative removal — so there is NO diff-orphan query and NO
	// B LOST emit here. The time-based sweep owns removals.
	mock.ExpectQuery("UPDATE foghorn.artifact_nodes.*last_seen_at < NOW\\(\\).*RETURNING artifact_hash, role").
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "role"}))
	mock.ExpectCommit()

	if err := repo.UpsertArtifacts(context.Background(), "node-1", []state.ArtifactRecord{
		{ArtifactHash: "hash-a", ReportConnectionFence: 5, ReportSeq: 3},
	}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// RefreshNodeCopy synchronously emits GAINED for a present copy whose projection is
// absent (last_emitted_version=0) — the path a raw writer takes after restoring a row
// that was previously LOST.
func TestRefreshNodeCopy_EmitsWhenAbsent(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT an.role, an.is_complete.*FROM foghorn.artifact_nodes an.*JOIN foghorn.artifacts.*FOR UPDATE OF an").
		WithArgs("hash-1", "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_complete", "size_bytes", "last_emitted_version", "tenant_id"}).
			AddRow("origin", false, int64(0), int64(0), "tenant-1"))
	expectNodeCopyEmit(mock, "hash-1", int64(1))
	mock.ExpectCommit()

	if err := repo.RefreshNodeCopy(context.Background(), "hash-1", "node-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// RefreshNodeCopy is a no-op when the projection already shows the copy present
// (last_emitted_version != 0) — no duplicate GAINED.
func TestRefreshNodeCopy_NoopWhenPresent(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT an.role, an.is_complete.*FROM foghorn.artifact_nodes an.*JOIN foghorn.artifacts.*FOR UPDATE OF an").
		WithArgs("hash-1", "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_complete", "size_bytes", "last_emitted_version", "tenant_id"}).
			AddRow("origin", true, int64(0), int64(9), "tenant-1")) // already present
	mock.ExpectRollback()

	if err := repo.RefreshNodeCopy(context.Background(), "hash-1", "node-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Reconciliation seeds a never-emitted (last_emitted_version=0) present copy: candidate
// scan, then per-row FOR UPDATE + emit under its own transaction.
func TestReconcileNodeCopies_SeedsUnemitted(t *testing.T) {
	repo, mock := setupRepoTest(t)

	// (1) Stale-present sweep — its own tx, nothing stale this pass.
	mock.ExpectBegin()
	mock.ExpectQuery("WITH stale AS.*UPDATE foghorn.artifact_nodes an SET is_orphaned = true").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id", "role"}))
	mock.ExpectCommit()

	// (2) Candidate scan (keys only).
	mock.ExpectQuery("SELECT artifact_hash, node_id FROM foghorn.artifact_nodes.*last_emitted_version = 0").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}).AddRow("hash-1", "node-1"))
	// Per-row transaction: FOR UPDATE re-read, still present + unemitted, then emit.
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT an.role, an.is_complete.*FROM foghorn.artifact_nodes an.*JOIN foghorn.artifacts.*FOR UPDATE OF an").
		WithArgs("hash-1", "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_complete", "size_bytes", "last_emitted_version", "tenant_id"}).
			AddRow("origin", true, int64(100), int64(0), "tenant-1"))
	expectNodeCopyEmit(mock, "hash-1", int64(1))
	mock.ExpectCommit()

	n, err := repo.ReconcileNodeCopies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("emitted = %d, want 1", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A disconnected / empty-reporting node emits a durable LOST per held artifact.
func TestMarkNodeArtifactsOrphaned_EmitsLost(t *testing.T) {
	repo, mock := setupRepoTest(t)

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE foghorn.artifact_nodes.*is_orphaned = true.*RETURNING artifact_hash, role").
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "role"}).AddRow("hash-1", "origin"))
	expectNodeCopyLostOutbox(mock, "hash-1")
	mock.ExpectCommit()

	if err := repo.MarkNodeArtifactsOrphaned(context.Background(), "node-1", 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
