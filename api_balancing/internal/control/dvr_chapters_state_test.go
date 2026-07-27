package control

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// Chapter state-machine helpers and the artifact-origin policy walk
// drive chapter finalization. Each transition guards its precondition
// in SQL — these tests pin the row-affecting behavior so future schema
// refactors that change WHERE clauses surface immediately.

func setupChapterTest(t *testing.T) sqlmock.Sqlmock {
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
	return mock
}

func TestMarkChapterFinalizing_AcceptsClosedOrStaleFinalizing(t *testing.T) {
	mock := setupChapterTest(t)
	// The WHERE clause now allows reclaiming a stale 'finalizing' row past the deadline so a lost
	// Helmsman result doesn't wedge the chapter. Now one tx: UPDATE + PROCESSING lifecycle enqueue.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters.*WHERE chapter_id = \$1\s+AND \(state = 'closed'\s+OR \(state = 'finalizing'.*finalize_started_at.*make_interval.*\)\)`).
		WithArgs("chap-1", "art-hash", float64((30 * time.Minute).Seconds()), "node-x").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	ok, err := MarkChapterFinalizing(context.Background(), "chap-1", "art-hash", "tenant-1", "node-x", 30*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true when one row updated")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMarkChapterFinalizing_SkipWhenAlreadyAdvanced(t *testing.T) {
	mock := setupChapterTest(t)
	// 0 rows updated → no lifecycle, tx rolls back.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters`).
		WithArgs("chap-1", "art-hash", float64((30 * time.Minute).Seconds()), "node-x").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	ok, err := MarkChapterFinalizing(context.Background(), "chap-1", "art-hash", "tenant-1", "node-x", 30*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when no rows updated")
	}
}

// MarkChapterFinalizedTx runs the transition on a tx and returns the row count so the atomic
// finalize path can require exactly one transitioned row.
func TestMarkChapterFinalizedTx_ReturnsRowCount(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state\s+= 'finalized'.*WHERE chapter_id = \$1\s+AND state\s+= 'finalizing'`).
		WithArgs("chap-1", int32(3), false, nil, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	rows, err := MarkChapterFinalizedTx(context.Background(), tx, "chap-1", 3, false, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// PropagateChapterRetention pushes the parent DVR's retention horizon onto its child chapter
// artifacts (linked via dvr_chapters.playback_artifact_hash), returning the count updated.
func TestPropagateChapterRetention(t *testing.T) {
	mock := setupChapterTest(t)
	until := time.Unix(1800000000, 0)
	mock.ExpectExec(`UPDATE foghorn.artifacts a\s+SET retention_until = \$2.*FROM foghorn.dvr_chapters c\s+WHERE c.artifact_hash = \$1\s+AND a.artifact_hash = c.playback_artifact_hash`).
		WithArgs("dvr-1", until).
		WillReturnResult(sqlmock.NewResult(0, 2))
	n, err := PropagateChapterRetention(context.Background(), "dvr-1", until)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Fatalf("n = %d, want 2", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// keep-forever ⇒ NULL: a nil horizon propagates NULL to the children.
func TestPropagateChapterRetention_KeepForeverNulls(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectExec(`UPDATE foghorn.artifacts a\s+SET retention_until = \$2`).
		WithArgs("dvr-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if _, err := PropagateChapterRetention(context.Background(), "dvr-1", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// SoftDeleteDVRAndChapters commits the durable deletion as one tx: cascade child chapter
// artifacts (RETURNING hashes) → remove chapter rows → soft-delete the parent. All BEFORE any
// physical byte cleanup.
func TestSoftDeleteDVRAndChapters(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectBegin()
	// Tenant-scoped parent soft-delete (RowsAffected confirms the transition), then the shared
	// tenant-scoped child cascade.
	mock.ExpectExec(`UPDATE foghorn.artifacts SET status = 'deleted'.*WHERE artifact_hash = \$1 AND tenant_id = \$2::uuid AND artifact_type = 'dvr' AND status <> 'deleted'`).
		WithArgs("dvr-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`UPDATE foghorn.artifacts a\s+SET status = 'deleted'.*FROM foghorn.dvr_chapters c\s+WHERE c.artifact_hash = \$1.*a.tenant_id = \$2::uuid.*RETURNING a.artifact_hash`).
		WithArgs("dvr-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}).AddRow("chap-art-1").AddRow("chap-art-2"))
	mock.ExpectExec(`DELETE FROM foghorn.dvr_chapters c\s+USING foghorn.artifacts parent.*parent.tenant_id = \$2::uuid`).
		WithArgs("dvr-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 2))
	// Per-child VOD-deleted events, then the DVR-deleted event — all in the same tx.
	mock.ExpectExec(`INSERT INTO foghorn.artifact_event_outbox`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn.artifact_event_outbox`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn.artifact_event_outbox`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	hashes, transitioned, err := SoftDeleteDVRAndChapters(context.Background(), "dvr-1", "tenant-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !transitioned {
		t.Fatal("expected parentTransitioned=true")
	}
	if len(hashes) != 2 || hashes[0] != "chap-art-1" || hashes[1] != "chap-art-2" {
		t.Fatalf("hashes = %v", hashes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Transition-idempotent: deleting an already-deleted DVR affects 0 parent rows
// (parentTransitioned=false), so NO DVR-deleted lifecycle event is re-enqueued — but the cascade
// still runs so an already-deleted parent's orphaned children are repaired. The tx commits cleanly.
func TestSoftDeleteDVRAndChapters_AlreadyDeletedNoReemit(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectBegin()
	// Parent already deleted → guarded UPDATE affects no rows.
	mock.ExpectExec(`UPDATE foghorn.artifacts SET status = 'deleted'.*WHERE artifact_hash = \$1 AND tenant_id = \$2::uuid AND artifact_type = 'dvr' AND status <> 'deleted'`).
		WithArgs("dvr-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// Cascade still runs but finds no live children.
	mock.ExpectQuery(`UPDATE foghorn.artifacts a\s+SET status = 'deleted'.*FROM foghorn.dvr_chapters c\s+WHERE c.artifact_hash = \$1.*a.tenant_id = \$2::uuid.*RETURNING a.artifact_hash`).
		WithArgs("dvr-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}))
	mock.ExpectExec(`DELETE FROM foghorn.dvr_chapters c\s+USING foghorn.artifacts parent.*parent.tenant_id = \$2::uuid`).
		WithArgs("dvr-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// No per-child events, and crucially NO DVR-deleted event.
	mock.ExpectCommit()

	hashes, transitioned, err := SoftDeleteDVRAndChapters(context.Background(), "dvr-1", "tenant-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if transitioned {
		t.Fatal("expected parentTransitioned=false for an already-deleted DVR")
	}
	if len(hashes) != 0 {
		t.Fatalf("hashes = %v, want none", hashes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The repair path: an already-deleted parent (RowsAffected=0) whose children are still
// live. The cascade soft-deletes the children (RETURNING their hashes) and emits their VOD-deleted
// events, but NO DVR-deleted event (parent already gone). This is what RepairDeletedDVRChildrenBatch
// drives per parent.
func TestSoftDeleteDVRAndChapters_RepairsLegacyChildren(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn.artifacts SET status = 'deleted'.*tenant_id = \$2::uuid.*status <> 'deleted'`).
		WithArgs("dvr-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`UPDATE foghorn.artifacts a\s+SET status = 'deleted'.*a.tenant_id = \$2::uuid.*RETURNING a.artifact_hash`).
		WithArgs("dvr-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}).AddRow("chap-art-9"))
	mock.ExpectExec(`DELETE FROM foghorn.dvr_chapters c\s+USING foghorn.artifacts parent.*parent.tenant_id = \$2::uuid`).
		WithArgs("dvr-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// One per-child VOD-deleted event; NO DVR-deleted event.
	mock.ExpectExec(`INSERT INTO foghorn.artifact_event_outbox`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	hashes, transitioned, err := SoftDeleteDVRAndChapters(context.Background(), "dvr-1", "tenant-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if transitioned {
		t.Fatal("expected parentTransitioned=false (parent already deleted)")
	}
	if len(hashes) != 1 || hashes[0] != "chap-art-9" {
		t.Fatalf("hashes = %v, want [chap-art-9]", hashes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Tenant is required: a cascade with no tenant must refuse rather than run an unscoped delete.
func TestSoftDeleteDVRAndChapters_RequiresTenant(t *testing.T) {
	_ = setupChapterTest(t)
	if _, _, err := SoftDeleteDVRAndChapters(context.Background(), "dvr-1", ""); err == nil {
		t.Fatal("expected error when tenant_id empty")
	}
}

// The repair path: a deleted DVR parent (dvr-1, tenant-1) whose dvr_chapters still point at a LIVE
// child (its dvr_chapter_playback.dvr_hash is NULL, so the parent-keyed cascade never reached it).
// The batch cascades that parent's children per its own tenant so each child projects its deletion.
func TestRepairDeletedDVRChildrenBatch(t *testing.T) {
	mock := setupChapterTest(t)
	// Candidate query: deleted parents with a live child.
	mock.ExpectQuery(`SELECT p.artifact_hash, p.tenant_id.*FROM foghorn.artifacts p\s+WHERE p.artifact_type = 'dvr' AND p.status = 'deleted'\s+AND EXISTS \(SELECT 1 FROM foghorn.dvr_chapters c WHERE c.artifact_hash = p.artifact_hash\)`).
		WithArgs(200).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "tenant_id"}).AddRow("dvr-1", "tenant-1"))
	// Per-parent tenant-scoped cascade in its own tx.
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE foghorn.artifacts a\s+SET status = 'deleted'.*a.tenant_id = \$2::uuid.*RETURNING a.artifact_hash`).
		WithArgs("dvr-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}).AddRow("chap-art-1"))
	mock.ExpectExec(`DELETE FROM foghorn.dvr_chapters c\s+USING foghorn.artifacts parent.*parent.tenant_id = \$2::uuid`).
		WithArgs("dvr-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn.artifact_event_outbox`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	repaired, err := RepairDeletedDVRChildrenBatch(context.Background(), 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired != 1 {
		t.Fatalf("repaired = %d, want 1", repaired)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMarkChapterFrozen_RequiresFinalized(t *testing.T) {
	mock := setupChapterTest(t)
	// MarkChapterFrozen now wraps MarkChapterFrozenTx in a transaction.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state\s+= 'frozen'.*WHERE chapter_id = \$1\s+AND state\s+= 'finalized'`).
		WithArgs("chap-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := MarkChapterFrozen(context.Background(), "chap-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMarkChapterReclaimStarted_GatesByFreshness(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET reclaim_started_at = NOW\(\).*WHERE chapter_id = \$1\s+AND state\s+= 'frozen'.*reclaim_started_at IS NULL.*make_interval`).
		WithArgs("chap-1", float64((5 * time.Minute).Seconds())).
		WillReturnResult(sqlmock.NewResult(0, 1))
	ok, err := MarkChapterReclaimStarted(context.Background(), "chap-1", 5*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true when row updated")
	}
}

func TestMarkChapterFailed_RejectsBadTerminalState(t *testing.T) {
	setupChapterTest(t)
	if err := MarkChapterFailed(context.Background(), "chap-1", "frozen", "reason", ""); err == nil {
		t.Fatal("expected error for invalid terminal state")
	}
}

func TestMarkChapterFailed_AcceptsSourceMissing(t *testing.T) {
	mock := setupChapterTest(t)
	// Now one transaction: transition the chapter (RETURNING its playback hash) then fail the
	// allocated child artifact so it isn't stranded 'finalizing'.
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE foghorn.dvr_chapters\s+SET state\s+= \$2,\s+last_failure_reason = \$3`).
		WithArgs("chap-1", ChapterStateFailedSourceMissing, "segments unavailable", "").
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash"}).AddRow("chap-art-1"))
	mock.ExpectQuery(`UPDATE foghorn.artifacts\s+SET status = 'failed'.*RETURNING tenant_id`).
		WithArgs("chap-art-1", "segments unavailable").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-1"))
	mock.ExpectExec(`INSERT INTO foghorn.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := MarkChapterFailed(context.Background(), "chap-1",
		ChapterStateFailedSourceMissing, "segments unavailable", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// A chapter already past a failable state (RETURNING no row) is a no-op: no artifact write,
// tx rolls back cleanly.
func TestMarkChapterFailed_NoOpWhenNotFailable(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE foghorn.dvr_chapters\s+SET state\s+= \$2`).
		WithArgs("chap-1", ChapterStateFailedPermanent, "boom", "").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	if err := MarkChapterFailed(context.Background(), "chap-1", ChapterStateFailedPermanent, "boom", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestRetryChapterFinalize_OnlyFromFinalizing(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state\s+= 'closed',\s+last_failure_reason.*WHERE chapter_id = \$1\s+AND state\s+= 'finalizing'`).
		WithArgs("chap-1", "transient: disk pressure", "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := RetryChapterFinalize(context.Background(), "chap-1", "transient: disk pressure", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// SetChapterPlaybackID caches the Commodore-minted public key on the
// chapter row. Idempotent; subsequent mints with the same value are
// a no-op at the DB level (one row updated).
func TestSetChapterPlaybackID_CachesOnChapterRow(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET playback_id = \$2\s+WHERE chapter_id\s+= \$1`).
		WithArgs("chap-1", "pb_chap_abc").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := SetChapterPlaybackID(context.Background(), "chap-1", "pb_chap_abc"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestSetChapterPlaybackID_NoopOnEmptyArgs(t *testing.T) {
	setupChapterTest(t)
	// No mock.ExpectExec — calling with empty args must not touch the DB.
	if err := SetChapterPlaybackID(context.Background(), "", "pb_chap_abc"); err != nil {
		t.Fatalf("empty chapter_id should be a no-op, got: %v", err)
	}
	if err := SetChapterPlaybackID(context.Background(), "chap-1", ""); err != nil {
		t.Fatalf("empty playback_id should be a no-op, got: %v", err)
	}
}
