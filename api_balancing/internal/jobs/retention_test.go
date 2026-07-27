package jobs

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// TestNewRetentionJobDefaults pins the documented fallbacks: an unconfigured
// interval/retention must default to 1h / 30 days, never zero (a zero ticker
// panics; a zero retention would expire everything immediately).
func TestNewRetentionJobDefaults(t *testing.T) {
	j := NewRetentionJob(RetentionConfig{})
	if j.interval != time.Hour {
		t.Errorf("interval = %v, want 1h", j.interval)
	}
	if j.retentionDays != 30 {
		t.Errorf("retentionDays = %d, want 30", j.retentionDays)
	}

	custom := NewRetentionJob(RetentionConfig{Interval: 5 * time.Minute, RetentionDays: 7})
	if custom.interval != 5*time.Minute || custom.retentionDays != 7 {
		t.Errorf("custom config not honored: interval=%v days=%d", custom.interval, custom.retentionDays)
	}
}

// TestRetentionScan pins the eligibility sweep: the UPDATE...RETURNING marks
// terminal artifacts deleted (expired retention OR legacy clip/vod past the
// fallback window) and the row loop drains every returned artifact. decklogClient
// is nil so the deterministic DB path is what's under test (lifecycle emission is
// fire-and-forget and exercised elsewhere).
func TestRetentionScan(t *testing.T) {
	selCols := []string{
		"artifact_hash", "artifact_type", "stream_internal_name", "tenant_id", "user_id",
		"size_bytes", "retention_until", "started_at", "ended_at", "manifest_path",
	}
	// expectExpireTx pins one non-DVR artifact's per-row expiry transaction: the guarded soft-
	// delete RE-EVALUATES the expiry predicate (RETURNING tenant) and the durable lifecycle outbox
	// INSERT commits with it.
	expectExpireTx := func(mock sqlmock.Sqlmock, hash string) {
		mock.ExpectBegin()
		mock.ExpectQuery(`UPDATE foghorn.artifacts SET status = 'deleted'.*RETURNING tenant_id`).
			WithArgs(hash, 30).
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("t1"))
		mock.ExpectExec(`INSERT INTO foghorn.artifact_event_outbox`).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
	}

	t.Run("expires each returned row in its own transaction", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer mockDB.Close()

		// SELECT (not bulk UPDATE) finds candidates; each is then expired in its own tx.
		mock.ExpectQuery(`SELECT artifact_hash.*FROM foghorn.artifacts`).WithArgs(30).
			WillReturnRows(sqlmock.NewRows(selCols).
				AddRow("clip-1", "clip", "s1", "t1", "u1", int64(100), nil, nil, nil, nil).
				AddRow("vod-1", "vod", nil, "t1", "u1", nil, nil, nil, nil, nil))
		expectExpireTx(mock, "clip-1")
		expectExpireTx(mock, "vod-1")

		j := NewRetentionJob(RetentionConfig{DB: mockDB, Logger: logging.NewLogger()})
		j.scan()

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("empty sweep is a no-op", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer mockDB.Close()
		mock.ExpectQuery(`SELECT artifact_hash.*FROM foghorn.artifacts`).WithArgs(30).
			WillReturnRows(sqlmock.NewRows(selCols))
		j := NewRetentionJob(RetentionConfig{DB: mockDB, Logger: logging.NewLogger()})
		j.scan()
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dvr chapters are excluded only from the age fallback, not the explicit deadline", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer mockDB.Close()
		// The explicit-deadline branch (retention_until elapsed) precedes the age fallback, and
		// the 'dvr_chapter' exclusion lives INSIDE the fallback branch.
		mock.ExpectQuery(`retention_until IS NOT NULL AND retention_until < NOW\(\)[\s\S]*artifact_type <> 'dvr'[\s\S]*origin_type, ''\) <> 'dvr_chapter'[\s\S]*retention_until IS NULL`).
			WithArgs(30).
			WillReturnRows(sqlmock.NewRows(selCols).
				AddRow("chap-art-1", "vod", nil, "t1", "u1", nil, nil, nil, nil, nil))
		expectExpireTx(mock, "chap-art-1")
		j := NewRetentionJob(RetentionConfig{DB: mockDB, Logger: logging.NewLogger()})
		j.scan()
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("query error is swallowed (logged, no panic)", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer mockDB.Close()
		mock.ExpectQuery(`SELECT artifact_hash.*FROM foghorn.artifacts`).WithArgs(30).WillReturnError(errors.New("boom"))
		j := NewRetentionJob(RetentionConfig{DB: mockDB, Logger: logging.NewLogger()})
		j.scan() // must not panic
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}
