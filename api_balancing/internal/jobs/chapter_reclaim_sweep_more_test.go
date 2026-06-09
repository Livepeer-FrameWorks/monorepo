package jobs

import (
	"context"
	"database/sql"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestReadRecordingNode pins the live-node gate: the latest non-orphaned
// artifact_nodes row only counts if that node is currently alive. A dead node or
// no row yields "" so the caller's abandon-grace branch takes over — a stale row
// must never wedge reclaim onto a node that will never ack.
func TestReadRecordingNode(t *testing.T) {
	ctx := context.Background()

	t.Run("no row returns empty", func(t *testing.T) {
		mockDB, mock, _ := sqlmock.New()
		defer mockDB.Close()
		mock.ExpectQuery(`FROM foghorn.artifact_nodes`).WithArgs("dvr-hash").WillReturnError(sql.ErrNoRows)
		s := &ChapterReclaimSweep{db: mockDB, logger: logging.NewLogger()}
		got, err := s.readRecordingNode(ctx, "dvr-hash")
		if err != nil || got != "" {
			t.Fatalf("no row must return (\"\", nil), got (%q, %v)", got, err)
		}
	})

	t.Run("row for dead node returns empty", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		defer sm.Shutdown()
		// State manager has no live nodes -> "dead-node" is not alive.
		mockDB, mock, _ := sqlmock.New()
		defer mockDB.Close()
		mock.ExpectQuery(`FROM foghorn.artifact_nodes`).WithArgs("dvr-hash").
			WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("dead-node"))
		s := &ChapterReclaimSweep{db: mockDB, logger: logging.NewLogger()}
		got, err := s.readRecordingNode(ctx, "dvr-hash")
		if err != nil || got != "" {
			t.Fatalf("dead node must return (\"\", nil), got (%q, %v)", got, err)
		}
	})

	t.Run("row for live node returns node id", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		defer sm.Shutdown()
		sm.SetNodeInfo("live-node", "", true, nil, nil, "", "", nil)
		sm.TouchNode("live-node", true) // recent heartbeat -> AliveNodeIDs includes it
		mockDB, mock, _ := sqlmock.New()
		defer mockDB.Close()
		mock.ExpectQuery(`FROM foghorn.artifact_nodes`).WithArgs("dvr-hash").
			WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("live-node"))
		s := &ChapterReclaimSweep{db: mockDB, logger: logging.NewLogger()}
		got, err := s.readRecordingNode(ctx, "dvr-hash")
		if err != nil || got != "live-node" {
			t.Fatalf("live node must be returned, got (%q, %v)", got, err)
		}
	})
}

// TestListSegmentsAwaitingLocalDelete pins the Phase-A status filter: only
// source-side statuses (uploaded/pending/failed_upload) whose overlapping
// chapters are all done are returned for Helmsman delete.
func TestListSegmentsAwaitingLocalDelete(t *testing.T) {
	mockDB, mock, _ := sqlmock.New()
	defer mockDB.Close()
	mock.ExpectQuery(`WITH overlapping`).
		WithArgs("dvr-hash", int64(1000), int64(2000), statusSetArg{want: []string{"uploaded", "pending", "failed_upload"}}).
		WillReturnRows(sqlmock.NewRows([]string{"segment_name", "s3_key"}).AddRow("seg-1.ts", "s3/key"))
	s := &ChapterReclaimSweep{db: mockDB, logger: logging.NewLogger()}
	got, err := s.listSegmentsAwaitingLocalDelete(context.Background(), "dvr-hash", 1000, 2000)
	if err != nil || len(got) != 1 || got[0].name != "seg-1.ts" {
		t.Fatalf("unexpected: %+v err %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestMarkSegmentOrphanUnreachable pins the state-machine guard: the UPDATE only
// touches non-terminal source-side rows (pending/uploaded/failed_upload), so an
// already-resolved row is never re-stamped.
func TestMarkSegmentOrphanUnreachable(t *testing.T) {
	mockDB, mock, _ := sqlmock.New()
	defer mockDB.Close()
	mock.ExpectExec(`UPDATE foghorn.dvr_segments\s+SET status = 'orphan_unreachable'`).
		WithArgs("dvr-hash", "seg-1.ts").
		WillReturnResult(sqlmock.NewResult(0, 1))
	s := &ChapterReclaimSweep{db: mockDB, logger: logging.NewLogger()}
	if err := s.markSegmentOrphanUnreachable(context.Background(), "dvr-hash", "seg-1.ts"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestMarkSegmentReclaimed pins the idempotent terminal transition (COALESCE on
// deleted_local_at so a re-run doesn't clobber the original delete timestamp).
func TestMarkSegmentReclaimed(t *testing.T) {
	mockDB, mock, _ := sqlmock.New()
	defer mockDB.Close()
	mock.ExpectExec(`UPDATE foghorn.dvr_segments\s+SET status = 'reclaimed'`).
		WithArgs("dvr-hash", "seg-1.ts").
		WillReturnResult(sqlmock.NewResult(0, 1))
	s := &ChapterReclaimSweep{db: mockDB, logger: logging.NewLogger()}
	if err := s.markSegmentReclaimed(context.Background(), "dvr-hash", "seg-1.ts"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRetryFinalizedChapterDTSH pins the stuck-finalize recovery sweep: rows
// with no live node row (empty node_id) are skipped rather than dispatched, so a
// chapter whose node is gone isn't sent a no-op DTSH trigger.
func TestRetryFinalizedChapterDTSH(t *testing.T) {
	mockDB, mock, _ := sqlmock.New()
	defer mockDB.Close()
	mock.ExpectQuery(`FROM foghorn.dvr_chapters`).
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash", "node_id"}).
			AddRow("art-1", "")) // empty node -> skipped, no control.TriggerDtshSync
	s := &ChapterReclaimSweep{db: mockDB, logger: logging.NewLogger()}
	s.retryFinalizedChapterDTSH(context.Background()) // must not panic / not dispatch
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
