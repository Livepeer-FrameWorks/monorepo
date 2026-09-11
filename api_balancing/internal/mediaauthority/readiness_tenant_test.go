package mediaauthority

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestReadinessPairRejectsMismatchedObjectAtomically(t *testing.T) {
	for _, surface := range []string{"playback", "ingest", "source"} {
		for _, objectRows := range []int64{0, 1} {
			t.Run(surface+map[int64]string{0: "/mismatch", 1: "/match"}[objectRows], func(t *testing.T) {
				store, mock, closeDB := newFixtureStore(t, "cell-a")
				defer closeDB()
				promote := store.MarkPlaybackPairLocalReadReady
				column := "local_read_ready"
				switch surface {
				case "ingest":
					promote, column = store.MarkIngestPairLocalReady, "local_ingest_ready"
				case "source":
					promote, column = store.MarkSourcePairLocalReady, "local_source_ready"
				}
				mock.ExpectBegin()
				mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.tenant_authority_projection\nSET "+column+" = TRUE")).
					WithArgs("tenant-a", int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.media_object_authority_projection\nSET "+column+" = TRUE, updated_at = NOW()\nWHERE tenant_id = $1::uuid\n  AND authority_id = $2\n  AND authority_version = $3")).
					WithArgs("tenant-a", "object-a", int64(9)).WillReturnResult(sqlmock.NewResult(0, objectRows))
				if objectRows == 0 {
					mock.ExpectRollback()
				} else {
					mock.ExpectCommit()
				}
				marked, err := promote(context.Background(), "tenant-a", 7, "object-a", 9)
				if marked != (objectRows == 1) || (err == nil) != (objectRows == 1) {
					t.Fatalf("promotion marked=%v err=%v for object rows=%d", marked, err, objectRows)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
