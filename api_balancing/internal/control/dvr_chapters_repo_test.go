package control

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// chapterRowValues returns one fully-populated foghorn.dvr_chapters row in the
// exact column order scanChapterRow expects. NULLable columns are passed nil so
// the sql.Null* scans land Valid=false.
func chapterRowCols() []string {
	return []string{
		"chapter_id", "artifact_hash", "mode", "interval_seconds",
		"start_ms", "end_ms", "is_current",
		"state", "playback_artifact_hash", "playback_id", "finalize_attempts",
		"finalize_started_at", "frozen_at",
		"last_failure_reason", "reclaim_started_at",
		"segment_count", "has_gaps",
		"actual_media_start_ms", "actual_media_end_ms",
		"created_at",
	}
}

func sampleChapterRow() *sqlmock.Rows {
	return sqlmock.NewRows(chapterRowCols()).AddRow(
		"chap-1", "art-1", "window_sized_chapters", nil,
		int64(1000), int64(2000), false,
		"finalized", "pb-hash", "pb-id", int64(2),
		nil, nil,
		nil, nil,
		int64(5), false,
		nil, nil,
		time.Unix(1700000000, 0),
	)
}

// PlayableChapterStates is the canonical playable set; it must match the
// isPlayableChapterState predicate (finalized/frozen/reclaimed).
func TestPlayableChapterStates(t *testing.T) {
	got := PlayableChapterStates()
	want := map[string]bool{ChapterStateFinalized: true, ChapterStateFrozen: true, ChapterStateReclaimed: true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want the 3 playable states", got)
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected playable state %q", s)
		}
		if !isPlayableChapterState(s) {
			t.Errorf("PlayableChapterStates lists %q but isPlayableChapterState says no", s)
		}
	}
}

// The chapter-closed notifier wakes the finalization queue. CloseChapter fires it
// only when a row actually transitioned (rows affected > 0); a no-op close must
// not wake the queue.
func TestCloseChapter_NotifiesOnlyWhenRowChanged(t *testing.T) {
	t.Run("fires when a row closes", func(t *testing.T) {
		mock := setupChapterTest(t)
		fired := false
		prev := chapterClosedNotifier
		SetChapterClosedNotifier(func() { fired = true })
		t.Cleanup(func() { chapterClosedNotifier = prev })

		mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET is_current = false,\s+state\s+= 'closed'\s+WHERE chapter_id = \$1\s+AND state\s+= 'open'`).
			WithArgs("chap-1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		if err := CloseChapter(context.Background(), "chap-1"); err != nil {
			t.Fatal(err)
		}
		if !fired {
			t.Fatal("notifier should fire when a row closed")
		}
	})

	t.Run("silent when nothing closed", func(t *testing.T) {
		mock := setupChapterTest(t)
		fired := false
		prev := chapterClosedNotifier
		SetChapterClosedNotifier(func() { fired = true })
		t.Cleanup(func() { chapterClosedNotifier = prev })

		mock.ExpectExec(`UPDATE foghorn.dvr_chapters`).
			WithArgs("chap-1").
			WillReturnResult(sqlmock.NewResult(0, 0))
		if err := CloseChapter(context.Background(), "chap-1"); err != nil {
			t.Fatal(err)
		}
		if fired {
			t.Fatal("notifier must not fire when no row changed")
		}
	})
}

// OpenChapter clears the previous current chapter and inserts the new one in one
// transaction, idempotent on chapter_id. The interval arg is NULL when unset.
func TestOpenChapter_TxClearsPreviousAndInserts(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET is_current = false`).
		WithArgs("art-1", "chap-1").
		WillReturnResult(sqlmock.NewResult(0, 0)) // no previous current
	mock.ExpectExec(`INSERT INTO foghorn.dvr_chapters`).
		WithArgs("chap-1", "art-1", "window_sized_chapters", nil, int64(1000), int64(2000), ChapterStateOpen).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := OpenChapter(context.Background(), DVRChapterRow{
		ChapterID: "chap-1", ArtifactHash: "art-1", Mode: "window_sized_chapters",
		StartMs: 1000, EndMs: 2000,
	})
	if err != nil {
		t.Fatalf("OpenChapter: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// CloseCurrentChapterForArtifact flips any current chapter for the artifact to
// closed (used at DVR finalize).
func TestCloseCurrentChapterForArtifact(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET is_current = false.*WHERE artifact_hash = \$1\s+AND is_current = true`).
		WithArgs("art-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := CloseCurrentChapterForArtifact(context.Background(), "art-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// MarkChapterReclaimed transitions frozen → reclaimed (terminal; range metadata
// stays, source segments gone).
func TestMarkChapterReclaimed(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state = 'reclaimed'\s+WHERE chapter_id = \$1\s+AND state\s+= 'frozen'`).
		WithArgs("chap-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := MarkChapterReclaimed(context.Background(), "chap-1"); err != nil {
		t.Fatal(err)
	}
}

// GetChapter scans the full 20-column row, or returns sql.ErrNoRows when absent.
func TestGetChapter(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(`SELECT chapter_id, artifact_hash, mode.*FROM foghorn.dvr_chapters\s+WHERE chapter_id = \$1`).
			WithArgs("chap-1").
			WillReturnRows(sampleChapterRow())
		got, err := GetChapter(context.Background(), "chap-1")
		if err != nil {
			t.Fatalf("GetChapter: %v", err)
		}
		if got.ChapterID != "chap-1" || got.ArtifactHash != "art-1" || got.State != "finalized" {
			t.Fatalf("scanned row = %+v", got)
		}
		if got.IntervalSeconds.Valid {
			t.Error("interval_seconds should scan as NULL")
		}
		if got.SegmentCount != 5 || got.PlaybackArtifactHash.String != "pb-hash" {
			t.Errorf("unexpected scan: segCount=%d pbHash=%v", got.SegmentCount, got.PlaybackArtifactHash)
		}
	})
	t.Run("not found", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(`SELECT chapter_id.*FROM foghorn.dvr_chapters\s+WHERE chapter_id = \$1`).
			WithArgs("missing").
			WillReturnError(sql.ErrNoRows)
		if _, err := GetChapter(context.Background(), "missing"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("want ErrNoRows, got %v", err)
		}
	})
}

// CurrentChapter returns the artifact's current chapter, or (nil, nil) when none
// — a missing current chapter is a normal state, not an error.
func TestCurrentChapter(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(`FROM foghorn.dvr_chapters\s+WHERE artifact_hash = \$1 AND is_current = true`).
			WithArgs("art-1").
			WillReturnRows(sampleChapterRow())
		got, err := CurrentChapter(context.Background(), "art-1")
		if err != nil || got == nil || got.ChapterID != "chap-1" {
			t.Fatalf("got (%+v,%v)", got, err)
		}
	})
	t.Run("none maps to nil,nil", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(`WHERE artifact_hash = \$1 AND is_current = true`).
			WithArgs("art-1").
			WillReturnError(sql.ErrNoRows)
		got, err := CurrentChapter(context.Background(), "art-1")
		if err != nil || got != nil {
			t.Fatalf("want (nil,nil) for no current chapter, got (%+v,%v)", got, err)
		}
	})
}

// LatestChapterBefore finds the most recent chapter strictly before a media
// offset, scoped by (mode, interval).
func TestLatestChapterBefore(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectQuery(`FROM foghorn.dvr_chapters\s+WHERE artifact_hash = \$1\s+AND mode = \$2\s+AND COALESCE\(interval_seconds, 0\) = \$3\s+AND start_ms < \$4`).
		WithArgs("art-1", "window_sized_chapters", int32(6), int64(5000)).
		WillReturnRows(sampleChapterRow())
	got, err := LatestChapterBefore(context.Background(), "art-1", "window_sized_chapters", 6, 5000)
	if err != nil || got == nil || got.ChapterID != "chap-1" {
		t.Fatalf("got (%+v,%v)", got, err)
	}
}

// ListChaptersNeedingFinalization returns closed/stale-finalizing chapters via
// scanChapterRowFromRows; an empty result is not an error.
func TestListChaptersNeedingFinalization(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectQuery(`FROM foghorn.dvr_chapters\s+WHERE state = 'closed'`).
		WithArgs(float64((10 * time.Minute).Seconds()), 25).
		WillReturnRows(sampleChapterRow())
	out, err := ListChaptersNeedingFinalization(context.Background(), 25, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ChapterID != "chap-1" {
		t.Fatalf("got %+v", out)
	}
}

// ListChaptersNeedingReclaim returns frozen, not-yet-reclaimed chapters (through
// the retry wrapper).
func TestListChaptersNeedingReclaim(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectQuery(`FROM foghorn.dvr_chapters\s+WHERE state = 'frozen'`).
		WithArgs(float64((5 * time.Minute).Seconds()), 50).
		WillReturnRows(sampleChapterRow())
	out, err := ListChaptersNeedingReclaim(context.Background(), 50, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("got %+v", out)
	}
}

// DeleteChapter removes the row outright.
func TestDeleteChapter(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectExec(`DELETE FROM foghorn.dvr_chapters WHERE chapter_id = \$1`).
		WithArgs("chap-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := DeleteChapter(context.Background(), "chap-1"); err != nil {
		t.Fatal(err)
	}
}

// DVRArtifactStillRecording is true only for an artifact in starting/recording;
// any other status or a query miss reads false (not recording).
func TestDVRArtifactStillRecording(t *testing.T) {
	t.Run("recording is true", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(`SELECT status FROM foghorn.artifacts WHERE artifact_hash = \$1 AND artifact_type = 'dvr'`).
			WithArgs("art-1").
			WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("recording"))
		if !DVRArtifactStillRecording(context.Background(), "art-1") {
			t.Fatal("recording status should be true")
		}
	})
	t.Run("completed is false", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(`SELECT status FROM foghorn.artifacts`).
			WithArgs("art-1").
			WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("completed"))
		if DVRArtifactStillRecording(context.Background(), "art-1") {
			t.Fatal("completed status should be false")
		}
	})
	t.Run("query miss is false", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(`SELECT status FROM foghorn.artifacts`).
			WithArgs("art-1").
			WillReturnError(sql.ErrNoRows)
		if DVRArtifactStillRecording(context.Background(), "art-1") {
			t.Fatal("missing artifact should be false")
		}
	})
}

// ClearCurrentChaptersForInactiveDVRs closes orphaned current chapters whose DVR
// is no longer recording, returning the count swept.
func TestClearCurrentChaptersForInactiveDVRs(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters c\s+SET is_current = false.*FROM foghorn.artifacts a`).
		WillReturnResult(sqlmock.NewResult(0, 3))
	n, err := ClearCurrentChaptersForInactiveDVRs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("swept = %d, want 3", n)
	}
}

// Every repo entry point fails closed with ErrConnDone when the DB handle is nil
// rather than panicking on a nil dereference.
func TestChapterRepo_NilDBGuards(t *testing.T) {
	prev := db
	db = nil
	t.Cleanup(func() { db = prev })

	ctx := context.Background()
	if err := OpenChapter(ctx, DVRChapterRow{}); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("OpenChapter nil db = %v", err)
	}
	if err := CloseChapter(ctx, "c"); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("CloseChapter nil db = %v", err)
	}
	if err := CloseCurrentChapterForArtifact(ctx, "a"); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("CloseCurrentChapterForArtifact nil db = %v", err)
	}
	if err := MarkChapterReclaimed(ctx, "c"); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("MarkChapterReclaimed nil db = %v", err)
	}
	if err := DeleteChapter(ctx, "c"); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("DeleteChapter nil db = %v", err)
	}
	if _, err := GetChapter(ctx, "c"); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("GetChapter nil db = %v", err)
	}
	if _, err := CurrentChapter(ctx, "a"); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("CurrentChapter nil db = %v", err)
	}
	if _, err := ListChaptersNeedingFinalization(ctx, 10, time.Minute); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("ListChaptersNeedingFinalization nil db = %v", err)
	}
	if DVRArtifactStillRecording(ctx, "a") {
		t.Error("DVRArtifactStillRecording nil db should be false")
	}
}
