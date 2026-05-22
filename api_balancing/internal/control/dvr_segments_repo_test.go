package control

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

func TestInsertDVRSegment_HealsLostLocalWithMatchingTiming(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	const (
		dvrHash      = "dvr-1"
		segmentName  = "seg_042.ts"
		mediaStartMs = int64(60_000)
		mediaEndMs   = int64(66_000)
		durationMs   = int64(6_000)
		existingSeq  = int64(42)
	)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM foghorn\.artifacts WHERE artifact_hash =`).
		WithArgs(dvrHash).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("completed"))
	mock.ExpectQuery(`SELECT sequence, status, media_start_ms, media_end_ms, duration_ms`).
		WithArgs(dvrHash, segmentName).
		WillReturnRows(sqlmock.NewRows([]string{"sequence", "status", "media_start_ms", "media_end_ms", "duration_ms"}).
			AddRow(existingSeq, "lost_local", mediaStartMs, mediaEndMs, durationMs))
	mock.ExpectExec(`UPDATE foghorn\.dvr_segments\s+SET status = 'pending'`).
		WithArgs(dvrHash, segmentName).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	seq, err := InsertDVRSegment(context.Background(), dvrHash, segmentName, "s3/key", mediaStartMs, mediaEndMs, durationMs, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seq != existingSeq {
		t.Fatalf("expected existing sequence %d, got %d", existingSeq, seq)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertDVRSegment_RejectsTimingMismatch(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	const dvrHash = "dvr-2"
	const segmentName = "seg_007.ts"

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM foghorn\.artifacts WHERE artifact_hash =`).
		WithArgs(dvrHash).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("completed"))
	// Existing row has different timing — a wrong file with the same name.
	mock.ExpectQuery(`SELECT sequence, status, media_start_ms, media_end_ms, duration_ms`).
		WithArgs(dvrHash, segmentName).
		WillReturnRows(sqlmock.NewRows([]string{"sequence", "status", "media_start_ms", "media_end_ms", "duration_ms"}).
			AddRow(int64(7), "lost_local", int64(100_000), int64(106_000), int64(6_000)))
	mock.ExpectRollback()

	_, err := InsertDVRSegment(context.Background(), dvrHash, segmentName, "s3/key", 200_000, 206_000, 6_000, false)
	if !errors.Is(err, ErrDVRSegmentTimingMismatch) {
		t.Fatalf("expected ErrDVRSegmentTimingMismatch, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertDVRSegment_AcceptsExistingSegmentAcrossClockDomains(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	const (
		dvrHash      = "dvr-clock-domain"
		segmentName  = "seg_011.ts"
		existingSeq  = int64(11)
		durationMs   = int64(8_000)
		relativeFrom = int64(2_368_023)
		absoluteFrom = int64(1_779_122_217_508)
	)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM foghorn\.artifacts WHERE artifact_hash =`).
		WithArgs(dvrHash).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("recording"))
	mock.ExpectQuery(`SELECT sequence, status, media_start_ms, media_end_ms, duration_ms`).
		WithArgs(dvrHash, segmentName).
		WillReturnRows(sqlmock.NewRows([]string{"sequence", "status", "media_start_ms", "media_end_ms", "duration_ms"}).
			AddRow(existingSeq, "uploaded", relativeFrom, relativeFrom+durationMs, durationMs))
	mock.ExpectCommit()

	seq, err := InsertDVRSegment(context.Background(), dvrHash, segmentName, "s3/key", absoluteFrom, absoluteFrom+durationMs, durationMs, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seq != existingSeq {
		t.Fatalf("expected existing sequence %d, got %d", existingSeq, seq)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertDVRSegment_PendingRetryAllowedAfterParentCompleted(t *testing.T) {
	// The seeded-completed-DVR + pending-segments case: parent reached
	// 'completed' but segment rows never uploaded. InsertDVRSegment must
	// allow the retry so sidecar startup reconciliation can finish the
	// upload.
	mock, _, _ := setupArtifactTestDeps(t)

	const dvrHash = "dvr-completed"
	const segmentName = "segment_0.ts"
	const existingSeq = int64(0)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM foghorn\.artifacts WHERE artifact_hash =`).
		WithArgs(dvrHash).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("completed"))
	mock.ExpectQuery(`SELECT sequence, status, media_start_ms, media_end_ms, duration_ms`).
		WithArgs(dvrHash, segmentName).
		WillReturnRows(sqlmock.NewRows([]string{"sequence", "status", "media_start_ms", "media_end_ms", "duration_ms"}).
			AddRow(existingSeq, "pending", int64(0), int64(10417), int64(10417)))
	mock.ExpectCommit()

	seq, err := InsertDVRSegment(context.Background(), dvrHash, segmentName, "s3/key", 0, 10417, 10417, false)
	if err != nil {
		t.Fatalf("expected pending retry to succeed under completed parent; got %v", err)
	}
	if seq != existingSeq {
		t.Fatalf("expected existing sequence %d, got %d", existingSeq, seq)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertDVRSegment_UploadedRetryRejectedAfterParentCompleted(t *testing.T) {
	// 'uploaded' rows are already settled; reject the retry when parent is
	// terminal so we don't loop on already-done segments.
	mock, _, _ := setupArtifactTestDeps(t)

	const dvrHash = "dvr-completed-uploaded"
	const segmentName = "segment_done.ts"

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM foghorn\.artifacts WHERE artifact_hash =`).
		WithArgs(dvrHash).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("completed"))
	mock.ExpectQuery(`SELECT sequence, status, media_start_ms, media_end_ms, duration_ms`).
		WithArgs(dvrHash, segmentName).
		WillReturnRows(sqlmock.NewRows([]string{"sequence", "status", "media_start_ms", "media_end_ms", "duration_ms"}).
			AddRow(int64(5), "uploaded", int64(0), int64(6000), int64(6000)))
	mock.ExpectRollback()

	_, err := InsertDVRSegment(context.Background(), dvrHash, segmentName, "s3/key", 0, 6000, 6000, false)
	if !errors.Is(err, ErrDVRSegmentTerminal) {
		t.Fatalf("expected ErrDVRSegmentTerminal for uploaded+terminal-parent retry, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertDVRSegment_ExistingPendingRetryReusesSequence(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	const dvrHash = "dvr-3"
	const segmentName = "seg_010.ts"
	const existingSeq = int64(10)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM foghorn\.artifacts WHERE artifact_hash =`).
		WithArgs(dvrHash).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("recording"))
	mock.ExpectQuery(`SELECT sequence, status, media_start_ms, media_end_ms, duration_ms`).
		WithArgs(dvrHash, segmentName).
		WillReturnRows(sqlmock.NewRows([]string{"sequence", "status", "media_start_ms", "media_end_ms", "duration_ms"}).
			AddRow(existingSeq, "pending", int64(0), int64(6_000), int64(6_000)))
	mock.ExpectCommit()

	seq, err := InsertDVRSegment(context.Background(), dvrHash, segmentName, "s3/key", 0, 6_000, 6_000, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seq != existingSeq {
		t.Fatalf("expected existing sequence %d, got %d", existingSeq, seq)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertDVRSegment_RetriesWholeTransactionOnReadRestart(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	const dvrHash = "dvr-retry"
	const segmentName = "seg_012.ts"
	const existingSeq = int64(12)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM foghorn\.artifacts WHERE artifact_hash =`).
		WithArgs(dvrHash).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("recording"))
	mock.ExpectQuery(`SELECT sequence, status, media_start_ms, media_end_ms, duration_ms`).
		WithArgs(dvrHash, segmentName).
		WillReturnError(&pq.Error{Code: "40001", Message: "Restart read required"})
	mock.ExpectRollback()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM foghorn\.artifacts WHERE artifact_hash =`).
		WithArgs(dvrHash).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("recording"))
	mock.ExpectQuery(`SELECT sequence, status, media_start_ms, media_end_ms, duration_ms`).
		WithArgs(dvrHash, segmentName).
		WillReturnRows(sqlmock.NewRows([]string{"sequence", "status", "media_start_ms", "media_end_ms", "duration_ms"}).
			AddRow(existingSeq, "pending", int64(0), int64(6_000), int64(6_000)))
	mock.ExpectCommit()

	seq, err := InsertDVRSegment(context.Background(), dvrHash, segmentName, "s3/key", 0, 6_000, 6_000, false)
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if seq != existingSeq {
		t.Fatalf("expected existing sequence %d, got %d", existingSeq, seq)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertDVRSegment_RecoveryInsertAllowedAfterParentCompleted(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	const dvrHash = "dvr-recovered"
	const segmentName = "segment_recovered.ts"

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM foghorn\.artifacts WHERE artifact_hash =`).
		WithArgs(dvrHash).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("completed"))
	mock.ExpectQuery(`SELECT sequence, status, media_start_ms, media_end_ms, duration_ms`).
		WithArgs(dvrHash, segmentName).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(sequence\), -1\) \+ 1 FROM foghorn\.dvr_segments`).
		WithArgs(dvrHash).
		WillReturnRows(sqlmock.NewRows([]string{"next"}).AddRow(int64(0)))
	mock.ExpectExec(`INSERT INTO foghorn\.dvr_segments`).
		WithArgs(dvrHash, segmentName, int64(0), int64(0), int64(6_000), int64(6_000), "s3/key").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	seq, err := InsertDVRSegment(context.Background(), dvrHash, segmentName, "s3/key", 0, 6_000, 6_000, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seq != 0 {
		t.Fatalf("expected sequence 0, got %d", seq)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
