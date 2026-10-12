package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"frameworks/api_balancing/internal/control"
)

var settleSegCols = []string{
	"artifact_hash", "segment_name", "sequence",
	"media_start_ms", "media_end_ms", "duration_ms",
	"size_bytes", "s3_key", "status", "drop_reason",
	"created_at", "uploaded_at", "deleted_local_at", "dropped_at",
}

var settleParentCols = []string{
	"tenant_id", "user_id", "stream_id", "stream_internal_name",
	"origin_cluster_id", "storage_cluster_id", "retention_until", "recording_node",
	"recording_active", "started_at_ms",
}

// settleLedger is scenario 04's first window as the ledger held it when the
// sweeper closed the chapter: 6 s segments from the recording's first media,
// with the segment that starts at +115.666 s and runs past the +120 s boundary
// not recorded yet.
func settleLedger(startedAt int64, withBoundarySegment bool) *sqlmock.Rows {
	rows := sqlmock.NewRows(settleSegCols)
	now := time.Now()
	starts := []int64{0, 6000, 12000, 18000, 24000, 30000, 36000, 42000, 48000, 54000}
	ends := []int64{6000, 12000, 18000, 24000, 30000, 36000, 42000, 48000, 54000, 60000}
	for s := int64(60000); s < 115666; {
		e := s + 6000
		if s == 60000 {
			e = 67666
		}
		starts, ends = append(starts, s), append(ends, e)
		s = e
	}
	if withBoundarySegment {
		starts, ends = append(starts, 115666), append(ends, 121666)
	}
	for i := range starts {
		rows.AddRow("dvrhash", "seg", int64(i), startedAt+starts[i], startedAt+ends[i], ends[i]-starts[i],
			nil, "k", "uploaded", nil, now, nil, nil, nil)
	}
	return rows
}

func settleParent(active bool, startedAt int64) *sqlmock.Rows {
	return sqlmock.NewRows(settleParentCols).
		AddRow("tenant-1", "user-1", "stream-1", "stream-int", "cluster-1", "cluster-1", nil, "", active, startedAt)
}

// A window chapter of a live recording must not be dispatched while the
// segment running across its boundary is missing from the ledger: that
// segment starts inside this chapter, so no later chapter would hold it.
func TestDispatchChapter_WaitsForTheBoundarySegment(t *testing.T) {
	controlMock := withControlDB(t)
	mockDB, qmock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()

	startedAt := time.Now().Add(-125 * time.Second).UnixMilli()
	endMs := startedAt + 120000
	qmock.ExpectQuery(`FROM foghorn\.artifacts`).WithArgs("dvrhash").WillReturnRows(settleParent(true, startedAt))
	controlMock.ExpectQuery(`FROM foghorn\.dvr_segments\s+WHERE artifact_hash = \$1 AND media_start_ms < \$3 AND media_end_ms > \$2`).
		WithArgs("dvrhash", startedAt, endMs).
		WillReturnRows(settleLedger(startedAt, false))

	q := &ChapterFinalizationQueue{db: mockDB, logger: logging.NewLogger()}
	c := control.DVRChapterRow{ChapterID: "chap-1", ArtifactHash: "dvrhash", StartMs: startedAt, EndMs: endMs, CreatedAt: time.Now()}
	if err := q.dispatchChapter(context.Background(), c); err != nil {
		t.Fatalf("dispatchChapter: %v", err)
	}
	if err := controlMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("control DB: %v", err)
	}
}

// Once the boundary segment is recorded the chapter proceeds to its owned
// segment list (empty here, which fails it source-missing).
func TestDispatchChapter_ProceedsOnceTheLedgerCoversTheChapter(t *testing.T) {
	controlMock := withControlDB(t)
	mockDB, qmock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()

	startedAt := time.Now().Add(-125 * time.Second).UnixMilli()
	endMs := startedAt + 120000
	qmock.ExpectQuery(`FROM foghorn\.artifacts`).WithArgs("dvrhash").WillReturnRows(settleParent(true, startedAt))
	controlMock.ExpectQuery(`media_start_ms < \$3 AND media_end_ms > \$2`).
		WithArgs("dvrhash", startedAt, endMs).
		WillReturnRows(settleLedger(startedAt, true))
	controlMock.ExpectQuery(`media_start_ms >= \$2 AND media_start_ms < \$3`).
		WithArgs("dvrhash", startedAt, endMs).
		WillReturnRows(sqlmock.NewRows(settleSegCols))
	controlMock.ExpectBegin()
	controlMock.ExpectQuery(`UPDATE foghorn\.dvr_chapters.*RETURNING playback_artifact_hash`).
		WithArgs(control.ChapterStateFailedSourceMissing, sqlmock.AnyArg(), "chap-1", int32(0), "").
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash"}).AddRow(nil))
	controlMock.ExpectCommit()

	q := &ChapterFinalizationQueue{db: mockDB, logger: logging.NewLogger()}
	c := control.DVRChapterRow{ChapterID: "chap-1", ArtifactHash: "dvrhash", StartMs: startedAt, EndMs: endMs, CreatedAt: time.Now()}
	if err := q.dispatchChapter(context.Background(), c); err != nil {
		t.Fatalf("dispatchChapter: %v", err)
	}
	if err := controlMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("control DB: %v", err)
	}
}

// A hole that outlasts the settle wait is a real media gap; the chapter
// finalizes with what was recorded instead of waiting forever.
func TestDispatchChapter_HoleOutlastingTheSettleWaitProceeds(t *testing.T) {
	controlMock := withControlDB(t)
	mockDB, qmock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()

	startedAt := time.Now().Add(-120*time.Second - chapterLedgerSettleMaxWait - time.Minute).UnixMilli()
	endMs := startedAt + 120000
	qmock.ExpectQuery(`FROM foghorn\.artifacts`).WithArgs("dvrhash").WillReturnRows(settleParent(true, startedAt))
	controlMock.ExpectQuery(`media_start_ms < \$3 AND media_end_ms > \$2`).
		WithArgs("dvrhash", startedAt, endMs).
		WillReturnRows(settleLedger(startedAt, false))
	controlMock.ExpectQuery(`media_start_ms >= \$2 AND media_start_ms < \$3`).
		WithArgs("dvrhash", startedAt, endMs).
		WillReturnRows(sqlmock.NewRows(settleSegCols))
	controlMock.ExpectBegin()
	controlMock.ExpectQuery(`UPDATE foghorn\.dvr_chapters.*RETURNING playback_artifact_hash`).
		WithArgs(control.ChapterStateFailedSourceMissing, sqlmock.AnyArg(), "chap-1", int32(0), "").
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash"}).AddRow(nil))
	controlMock.ExpectCommit()

	q := &ChapterFinalizationQueue{db: mockDB, logger: logging.NewLogger()}
	c := control.DVRChapterRow{ChapterID: "chap-1", ArtifactHash: "dvrhash", StartMs: startedAt, EndMs: endMs, CreatedAt: time.Now()}
	if err := q.dispatchChapter(context.Background(), c); err != nil {
		t.Fatalf("dispatchChapter: %v", err)
	}
	if err := controlMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("control DB: %v", err)
	}
}

func TestDVRSegmentsCoverRange(t *testing.T) {
	seg := func(start, end int64) control.DVRSegmentRow {
		return control.DVRSegmentRow{MediaStartMs: start, MediaEndMs: end}
	}
	for _, tc := range []struct {
		name string
		rows []control.DVRSegmentRow
		want bool
	}{
		{"empty", nil, false},
		{"contiguous through the end", []control.DVRSegmentRow{seg(0, 6000), seg(6000, 12000)}, true},
		{"the predecessor covers the start", []control.DVRSegmentRow{seg(-2000, 4000), seg(4000, 10000)}, true},
		{"millisecond drift between segments", []control.DVRSegmentRow{seg(0, 6000), seg(6003, 12000)}, true},
		{"boundary segment missing", []control.DVRSegmentRow{seg(0, 6000), seg(6000, 9000)}, false},
		{"first segment missing", []control.DVRSegmentRow{seg(6000, 12000)}, false},
		{"middle segment missing", []control.DVRSegmentRow{seg(0, 4000), seg(8000, 12000)}, false},
	} {
		if got := control.DVRSegmentsCoverRange(tc.rows, 0, 10000); got != tc.want {
			t.Errorf("%s: covered=%v, want %v", tc.name, got, tc.want)
		}
	}
}
