package control

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestClassifyFinalCounts pins the segment-tally that drives DVR finalize's
// success/partial/failed decision: 'uploaded' and 'deleted_local' both count as
// durably-stored, while 'lost_local' counts as lost. Miscategorizing either
// bucket flips a complete recording to "failed" or hides real data loss. A nil
// querier short-circuits to ErrConnDone so the sweep doesn't treat a missing DB
// as zero segments.
func TestClassifyFinalCounts(t *testing.T) {
	t.Run("nil db returns ErrConnDone", func(t *testing.T) {
		prev := db
		db = nil
		t.Cleanup(func() { db = prev })

		up, lost, err := classifyFinalCounts(context.Background(), "art")
		if !errors.Is(err, sql.ErrConnDone) {
			t.Fatalf("err = %v, want sql.ErrConnDone", err)
		}
		if up != 0 || lost != 0 {
			t.Fatalf("got (%d,%d), want (0,0)", up, lost)
		}
	})

	t.Run("splits uploaded/deleted_local from lost_local", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(`COUNT\(\*\) FILTER \(WHERE status IN \('uploaded', 'deleted_local'\)\)`).
			WithArgs("art").
			WillReturnRows(sqlmock.NewRows([]string{"uploaded", "lost"}).AddRow(5, 2))

		up, lost, err := classifyFinalCounts(context.Background(), "art")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if up != 5 || lost != 2 {
			t.Fatalf("got (uploaded=%d, lost=%d), want (5, 2)", up, lost)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}
