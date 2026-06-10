package jobs

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"frameworks/api_balancing/internal/control"
)

// withControlDB swaps the control package's global *sql.DB (the one
// MarkChapterFailed / ListDVRSegmentsOwnedByChapter read) for a sqlmock and
// restores the previous value on cleanup. dispatchChapter routes its terminal
// state transitions through that global, so the test must own it.
func withControlDB(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	prev := control.GetDB()
	control.SetDB(mockDB)
	t.Cleanup(func() {
		control.SetDB(prev)
		_ = mockDB.Close()
	})
	return mock
}

// dispatchChapter must terminal-fail a chapter as failed_permanent once it has
// burned through chapterFinalizationMaxAttempts, WITHOUT reading the parent DVR
// or segment list (those would otherwise re-attempt a doomed dispatch). The DB
// UPDATE is the only side effect, and it must target the failed_permanent state.
func TestDispatchChapter_MaxAttemptsExceeded(t *testing.T) {
	mock := withControlDB(t)

	// MarkChapterFailed runs exactly one UPDATE constrained to closed/finalizing
	// rows; the terminal state arg must be failed_permanent.
	mock.ExpectExec(`UPDATE foghorn\.dvr_chapters`).
		WithArgs("chap-maxed", control.ChapterStateFailedPermanent, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	q := &ChapterFinalizationQueue{logger: logging.NewLogger()}
	c := control.DVRChapterRow{
		ChapterID:        "chap-maxed",
		ArtifactHash:     "dvrhash",
		FinalizeAttempts: chapterFinalizationMaxAttempts,
	}

	if err := q.dispatchChapter(context.Background(), c); err != nil {
		t.Fatalf("dispatchChapter returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// One attempt under the ceiling must NOT short-circuit to failed_permanent: the
// guard is >= maxAttempts. Here the chapter has attempts-1 so dispatch proceeds
// past the guard and reads the parent DVR. We make readParentDVR fail (parent
// row missing) so the test stops deterministically right after the guard,
// proving the max-attempts branch was skipped (no failed_permanent UPDATE).
func TestDispatchChapter_UnderMaxAttemptsProceedsPastGuard(t *testing.T) {
	mock := withControlDB(t)

	mockDB, qmock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()
	// readParentDVR queries foghorn.artifacts; return no rows so dispatch aborts
	// with "read parent DVR" before touching segments or marking anything failed.
	qmock.ExpectQuery(`FROM foghorn\.artifacts`).
		WithArgs("dvrhash").
		WillReturnError(sql.ErrNoRows)

	q := &ChapterFinalizationQueue{db: mockDB, logger: logging.NewLogger()}
	c := control.DVRChapterRow{
		ChapterID:        "chap-near",
		ArtifactHash:     "dvrhash",
		FinalizeAttempts: chapterFinalizationMaxAttempts - 1,
	}

	if err := q.dispatchChapter(context.Background(), c); err == nil {
		t.Fatal("expected error from readParentDVR, got nil")
	}
	// The control DB must have received NO UPDATE: the max-attempts guard did not fire.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("control DB should be untouched, but: %v", err)
	}
	if err := qmock.ExpectationsWereMet(); err != nil {
		t.Fatalf("parent DVR query unmet: %v", err)
	}
}

// A chapter whose media range owns zero segments is terminal-failed as
// failed_source_missing (there is nothing to finalize). This exercises the
// readParentDVR -> ListDVRSegmentsOwnedByChapter -> empty -> MarkChapterFailed
// chain across both the queue's own db and the control global db.
func TestDispatchChapter_NoSegmentsFailsSourceMissing(t *testing.T) {
	controlMock := withControlDB(t)

	mockDB, qmock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()

	// q.db owns the parent-DVR read; control.db owns the segment list + the
	// terminal-fail UPDATE. Point ListDVRSegmentsOwnedByChapter at controlMock.
	parentCols := []string{
		"tenant_id", "user_id", "stream_id", "stream_internal_name",
		"origin_cluster_id", "storage_cluster_id", "recording_node",
	}
	qmock.ExpectQuery(`FROM foghorn\.artifacts`).
		WithArgs("dvrhash").
		WillReturnRows(sqlmock.NewRows(parentCols).
			AddRow("tenant-1", "user-1", "stream-1", "stream-int", "cluster-1", "cluster-1", ""))

	// Segment list returns zero rows for this chapter's media range.
	segCols := []string{
		"artifact_hash", "segment_name", "sequence",
		"media_start_ms", "media_end_ms", "duration_ms",
		"size_bytes", "s3_key", "status", "drop_reason",
		"created_at", "uploaded_at", "deleted_local_at", "dropped_at",
	}
	controlMock.ExpectQuery(`FROM foghorn\.dvr_segments`).
		WithArgs("dvrhash", int64(0), int64(10000)).
		WillReturnRows(sqlmock.NewRows(segCols))

	// Terminal fail: failed_source_missing.
	controlMock.ExpectExec(`UPDATE foghorn\.dvr_chapters`).
		WithArgs("chap-empty", control.ChapterStateFailedSourceMissing, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	q := &ChapterFinalizationQueue{db: mockDB, logger: logging.NewLogger()}
	c := control.DVRChapterRow{
		ChapterID:        "chap-empty",
		ArtifactHash:     "dvrhash",
		StartMs:          0,
		EndMs:            10000,
		FinalizeAttempts: 0,
		CreatedAt:        time.Now(),
	}

	if err := q.dispatchChapter(context.Background(), c); err != nil {
		t.Fatalf("dispatchChapter returned error: %v", err)
	}
	if err := qmock.ExpectationsWereMet(); err != nil {
		t.Fatalf("parent DVR query unmet: %v", err)
	}
	if err := controlMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("control DB expectations unmet: %v", err)
	}
}

// When buildSegmentRefs reports missing>0 (a segment that is neither local nor
// recoverable from S3 — here a reclaimed row), dispatchChapter must terminal-fail
// the chapter as failed_source_missing rather than dispatch a job with a hole in
// it. This wires the buildSegmentRefs "missing" count to the state transition.
func TestDispatchChapter_MissingSegmentsFailsSourceMissing(t *testing.T) {
	controlMock := withControlDB(t)

	mockDB, qmock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()

	parentCols := []string{
		"tenant_id", "user_id", "stream_id", "stream_internal_name",
		"origin_cluster_id", "storage_cluster_id", "recording_node",
	}
	qmock.ExpectQuery(`FROM foghorn\.artifacts`).
		WithArgs("dvrhash").
		WillReturnRows(sqlmock.NewRows(parentCols).
			AddRow("tenant-1", "user-1", "stream-1", "stream-int", "cluster-1", "cluster-1", ""))

	segCols := []string{
		"artifact_hash", "segment_name", "sequence",
		"media_start_ms", "media_end_ms", "duration_ms",
		"size_bytes", "s3_key", "status", "drop_reason",
		"created_at", "uploaded_at", "deleted_local_at", "dropped_at",
	}
	now := time.Now()
	// A single 'reclaimed' segment: buildSegmentRefs counts it as missing and
	// attaches no recovery URL, so dispatch must fail the chapter source-missing.
	controlMock.ExpectQuery(`FROM foghorn\.dvr_segments`).
		WithArgs("dvrhash", int64(0), int64(10000)).
		WillReturnRows(sqlmock.NewRows(segCols).
			AddRow("dvrhash", "s0", int64(0), int64(0), int64(2000), int64(2000),
				nil, "", "reclaimed", nil, now, nil, nil, nil))

	controlMock.ExpectExec(`UPDATE foghorn\.dvr_chapters`).
		WithArgs("chap-missing", control.ChapterStateFailedSourceMissing, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	q := &ChapterFinalizationQueue{db: mockDB, logger: logging.NewLogger()}
	c := control.DVRChapterRow{
		ChapterID:        "chap-missing",
		ArtifactHash:     "dvrhash",
		StartMs:          0,
		EndMs:            10000,
		FinalizeAttempts: 0,
		CreatedAt:        now,
	}

	if err := q.dispatchChapter(context.Background(), c); err != nil {
		t.Fatalf("dispatchChapter returned error: %v", err)
	}
	if err := qmock.ExpectationsWereMet(); err != nil {
		t.Fatalf("parent DVR query unmet: %v", err)
	}
	if err := controlMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("control DB expectations unmet: %v", err)
	}
}
