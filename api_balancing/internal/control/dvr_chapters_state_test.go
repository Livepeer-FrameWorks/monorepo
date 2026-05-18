package control

import (
	"context"
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
	// The WHERE clause now allows reclaiming a stale 'finalizing' row
	// past the deadline so a lost Helmsman result doesn't wedge the
	// chapter — closed XOR finalizing-with-expired-deadline.
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters.*WHERE chapter_id = \$1\s+AND \(state = 'closed'\s+OR \(state = 'finalizing'.*finalize_started_at.*make_interval.*\)\)`).
		WithArgs("chap-1", "art-hash", float64((30 * time.Minute).Seconds())).
		WillReturnResult(sqlmock.NewResult(0, 1))
	ok, err := MarkChapterFinalizing(context.Background(), "chap-1", "art-hash", 30*time.Minute)
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
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters`).
		WithArgs("chap-1", "art-hash", float64((30 * time.Minute).Seconds())).
		WillReturnResult(sqlmock.NewResult(0, 0))
	ok, err := MarkChapterFinalizing(context.Background(), "chap-1", "art-hash", 30*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when no rows updated")
	}
}

func TestMarkChapterFinalized_RequiresFinalizing(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state\s+= 'finalized'.*WHERE chapter_id = \$1\s+AND state\s+= 'finalizing'`).
		WithArgs("chap-1", int32(42), true, int64(1000), int64(5000)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := MarkChapterFinalized(context.Background(), "chap-1", 42, true, 1000, 5000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMarkChapterFrozen_RequiresFinalized(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state\s+= 'frozen'.*WHERE chapter_id = \$1\s+AND state\s+= 'finalized'`).
		WithArgs("chap-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
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
	if err := MarkChapterFailed(context.Background(), "chap-1", "frozen", "reason"); err == nil {
		t.Fatal("expected error for invalid terminal state")
	}
}

func TestMarkChapterFailed_AcceptsSourceMissing(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state\s+= \$2,\s+last_failure_reason = \$3.*WHERE chapter_id = \$1\s+AND state IN \('closed', 'finalizing'\)`).
		WithArgs("chap-1", ChapterStateFailedSourceMissing, "segments unavailable").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := MarkChapterFailed(context.Background(), "chap-1",
		ChapterStateFailedSourceMissing, "segments unavailable"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRetryChapterFinalize_OnlyFromFinalizing(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state\s+= 'closed',\s+last_failure_reason.*WHERE chapter_id = \$1\s+AND state\s+= 'finalizing'`).
		WithArgs("chap-1", "transient: disk pressure").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := RetryChapterFinalize(context.Background(), "chap-1", "transient: disk pressure"); err != nil {
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
