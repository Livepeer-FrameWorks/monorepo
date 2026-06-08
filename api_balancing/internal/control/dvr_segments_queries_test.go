package control

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

func segmentRowCols() []string {
	return []string{
		"artifact_hash", "segment_name", "sequence",
		"media_start_ms", "media_end_ms", "duration_ms",
		"size_bytes", "s3_key", "status", "drop_reason",
		"created_at", "uploaded_at", "deleted_local_at", "dropped_at",
	}
}

func sampleSegmentRow() *sqlmock.Rows {
	return sqlmock.NewRows(segmentRowCols()).AddRow(
		"art-1", "seg-1", int64(1),
		int64(0), int64(6000), int64(6000),
		int64(2048), "s3://bucket/seg-1", "uploaded", nil,
		time.Unix(1700000000, 0), nil, nil, nil,
	)
}

// MarkDVRSegmentUploaded flips a pending/failed_upload row to uploaded with its
// size, gated on the source states so a duplicate ack can't regress a later state.
func TestMarkDVRSegmentUploaded(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	mock.ExpectExec(`UPDATE foghorn.dvr_segments\s+SET status = 'uploaded',\s+size_bytes = \$3,\s+uploaded_at = NOW\(\).*WHERE artifact_hash = \$1\s+AND segment_name = \$2\s+AND status IN \('pending', 'failed_upload'\)`).
		WithArgs("art-1", "seg-1", int64(2048)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := MarkDVRSegmentUploaded(context.Background(), "art-1", "seg-1", 2048); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// DVRSegmentProgress aggregates live (non-lost/reclaimed) segments into a count
// and total bytes for the finalization queue's progress view.
func TestDVRSegmentProgress(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	mock.ExpectQuery(`SELECT COUNT\(\*\), COALESCE\(SUM\(size_bytes\), 0\)\s+FROM foghorn.dvr_segments\s+WHERE artifact_hash = \$1\s+AND status NOT IN \('lost_local', 'reclaimed'\)`).
		WithArgs("art-1").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(int64(3), int64(9000)))
	count, size, err := DVRSegmentProgress(context.Background(), "art-1")
	if err != nil || count != 3 || size != 9000 {
		t.Fatalf("got (%d,%d,%v), want (3,9000,nil)", count, size, err)
	}
}

// MarkDVRSegmentDropped on an existing row transitions it to deleted_local (was
// uploaded) and stamps deleted_local_at; the row-exists path returns once the
// UPDATE affects a row, without inserting a placeholder.
func TestMarkDVRSegmentDropped_ExistingUploadedRow(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	mock.ExpectExec(`UPDATE foghorn.dvr_segments\s+SET status = \$3,\s+drop_reason = \$4.*WHERE artifact_hash = \$1\s+AND segment_name = \$2\s+AND status NOT IN \('deleted_local', 'lost_local', 'reclaimed'\)`).
		WithArgs("art-1", "seg-1", "deleted_local", "evicted", true).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := MarkDVRSegmentDropped(context.Background(), "art-1", "seg-1", "evicted", true, 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ListEvictableDVRSegments returns uploaded segment names past the window with no
// live chapter still covering them. A non-positive window short-circuits to no
// candidates (eviction disabled).
func TestListEvictableDVRSegments(t *testing.T) {
	t.Run("window disabled returns nothing", func(t *testing.T) {
		setupArtifactTestDeps(t)
		out, err := ListEvictableDVRSegments(context.Background(), "art-1", 0, 10)
		if err != nil || out != nil {
			t.Fatalf("got (%v,%v), want (nil,nil)", out, err)
		}
	})
	t.Run("returns evictable names", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`SELECT s.segment_name\s+FROM foghorn.dvr_segments s\s+WHERE s.artifact_hash = \$1\s+AND s.status = 'uploaded'`).
			WithArgs("art-1", sqlmock.AnyArg(), 100).
			WillReturnRows(sqlmock.NewRows([]string{"segment_name"}).AddRow("seg-1").AddRow("seg-2"))
		out, err := ListEvictableDVRSegments(context.Background(), "art-1", 60, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 2 || out[0] != "seg-1" {
			t.Fatalf("got %v", out)
		}
	})
}

// ListPendingDVRSegments returns pending/failed_upload rows older than the cutoff,
// scanned through scanDVRSegmentRows.
func TestListPendingDVRSegments(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	mock.ExpectQuery(`FROM foghorn.dvr_segments\s+WHERE artifact_hash = \$1\s+AND status IN \('pending', 'failed_upload'\)\s+AND created_at <= \$2`).
		WithArgs("art-1", sqlmock.AnyArg(), 500).
		WillReturnRows(sampleSegmentRow())
	out, err := ListPendingDVRSegments(context.Background(), "art-1", time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].SegmentName != "seg-1" || out[0].SizeBytes.Int64 != 2048 {
		t.Fatalf("got %+v", out)
	}
}

// ListDVRSegmentsOwnedByChapter returns rows whose media_start_ms falls in
// [startMs, endMs) — the start-of-segment ownership rule.
func TestListDVRSegmentsOwnedByChapter(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	mock.ExpectQuery(`FROM foghorn.dvr_segments\s+WHERE artifact_hash = \$1\s+AND media_start_ms >= \$2\s+AND media_start_ms <  \$3`).
		WithArgs("art-1", int64(0), int64(6000)).
		WillReturnRows(sampleSegmentRow())
	out, err := ListDVRSegmentsOwnedByChapter(context.Background(), "art-1", 0, 6000)
	if err != nil || len(out) != 1 {
		t.Fatalf("got (%+v,%v)", out, err)
	}
}

// ListDVRSegmentsForRange overlaps [startMs,endMs); the (0,0) form returns all
// segments for the artifact (admin path).
func TestListDVRSegmentsForRange(t *testing.T) {
	t.Run("bounded range", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`FROM foghorn.dvr_segments\s+WHERE artifact_hash = \$1\s+AND media_start_ms < \$3\s+AND media_end_ms > \$2`).
			WithArgs("art-1", int64(1000), int64(5000)).
			WillReturnRows(sampleSegmentRow())
		out, err := ListDVRSegmentsForRange(context.Background(), "art-1", 1000, 5000)
		if err != nil || len(out) != 1 {
			t.Fatalf("got (%+v,%v)", out, err)
		}
	})
	t.Run("unbounded all-rows form", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`FROM foghorn.dvr_segments\s+WHERE artifact_hash = \$1\s+ORDER BY media_start_ms ASC`).
			WithArgs("art-1").
			WillReturnRows(sampleSegmentRow())
		out, err := ListDVRSegmentsForRange(context.Background(), "art-1", 0, 0)
		if err != nil || len(out) != 1 {
			t.Fatalf("got (%+v,%v)", out, err)
		}
	})
}

// LookupDVRSegmentsByName fetches rows for a bounded name list via unnest; an
// empty list short-circuits without a query.
func TestLookupDVRSegmentsByName(t *testing.T) {
	t.Run("empty list short-circuits", func(t *testing.T) {
		setupArtifactTestDeps(t)
		out, err := LookupDVRSegmentsByName(context.Background(), "art-1", nil)
		if err != nil || out != nil {
			t.Fatalf("got (%v,%v), want (nil,nil)", out, err)
		}
	})
	t.Run("returns matching rows", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`FROM foghorn.dvr_segments\s+WHERE artifact_hash = \$1\s+AND segment_name = ANY\(\$2::text\[\]\)`).
			WithArgs("art-1", pq.StringArray([]string{"seg-1"})).
			WillReturnRows(sampleSegmentRow())
		out, err := LookupDVRSegmentsByName(context.Background(), "art-1", []string{"seg-1"})
		if err != nil || len(out) != 1 {
			t.Fatalf("got (%+v,%v)", out, err)
		}
	})
}

// MarkRemainingDVRSegmentsLost reclassifies the still-pending tail to lost_local
// at finalization and returns how many rows it drew the line under.
func TestMarkRemainingDVRSegmentsLost(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	mock.ExpectExec(`UPDATE foghorn.dvr_segments\s+SET status = 'lost_local',\s+drop_reason = \$2.*WHERE artifact_hash = \$1\s+AND status IN \('pending', 'failed_upload'\)`).
		WithArgs("art-1", "finalization window closed").
		WillReturnResult(sqlmock.NewResult(0, 4))
	n, err := MarkRemainingDVRSegmentsLost(context.Background(), "art-1", "finalization window closed")
	if err != nil || n != 4 {
		t.Fatalf("got (%d,%v), want (4,nil)", n, err)
	}
}

// Segment repo entry points fail closed with ErrConnDone on a nil DB handle.
func TestSegmentRepo_NilDBGuards(t *testing.T) {
	prev := db
	db = nil
	t.Cleanup(func() { db = prev })
	ctx := context.Background()

	if err := MarkDVRSegmentUploaded(ctx, "a", "s", 1); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("MarkDVRSegmentUploaded nil db = %v", err)
	}
	if _, _, err := DVRSegmentProgress(ctx, "a"); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("DVRSegmentProgress nil db = %v", err)
	}
	if err := MarkDVRSegmentDropped(ctx, "a", "s", "r", false, 0, 0, 0, 0); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("MarkDVRSegmentDropped nil db = %v", err)
	}
	if _, err := ListPendingDVRSegments(ctx, "a", time.Hour, 10); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("ListPendingDVRSegments nil db = %v", err)
	}
	if _, err := MarkRemainingDVRSegmentsLost(ctx, "a", "r"); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("MarkRemainingDVRSegmentsLost nil db = %v", err)
	}
}
