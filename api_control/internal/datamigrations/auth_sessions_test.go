package datamigrations

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

func TestAuthSessionBackfillCompletesAnEmptyInstall(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT tenant_id FROM commodore.refresh_tokens").WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}))
	progress, err := runAuthSessions(context.Background(), db, datamigrate.RunOptions{})
	if err != nil || !progress.Done || progress.Changed != 0 {
		t.Fatalf("empty install: progress=%+v err=%v", progress, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthSessionBackfillDryRunReadsOnlyOneBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT id FROM commodore.refresh_tokens").WithArgs(2).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("first").AddRow("second"))
	progress, err := runAuthSessions(context.Background(), db, datamigrate.RunOptions{DryRun: true, BatchSize: 2})
	if err != nil || progress.Done || progress.Scanned != 2 || progress.Changed != 0 {
		t.Fatalf("dry run: progress=%+v err=%v", progress, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
