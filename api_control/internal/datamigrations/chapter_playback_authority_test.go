package datamigrations

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

func TestRunChapterPlaybackAuthorityBatchesAndCompletes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("WITH candidates AS").WithArgs(50).
		WillReturnRows(sqlmock.NewRows([]string{"scanned", "changed", "skipped"}).AddRow(int64(12), int64(12), int64(0)))

	progress, err := runChapterPlaybackAuthority(context.Background(), db, datamigrate.RunOptions{BatchSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Scanned != 12 || progress.Changed != 12 || progress.Skipped != 0 || !progress.Done {
		t.Fatalf("unexpected progress: %+v", progress)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyChapterPlaybackAuthorityRejectsRemainingRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\)").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	if err := verifyChapterPlaybackAuthority(context.Background(), db); err == nil {
		t.Fatal("verification accepted an unrepaired chapter")
	}
}
