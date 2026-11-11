package datamigrate

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMigrationNamespaceSeparatesDatasetCompletionAndLeases(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	Register(Migration{ID: "dataset-backfill", StateNamespace: "source-b", Service: "periscope-metering", Run: func(_ context.Context, _ DB, _ RunOptions) (Progress, error) { return Progress{Done: true}, nil }})
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("FROM _data_migrations WHERE id").WithArgs("dataset-backfill@source-b").WillReturnError(sql.ErrNoRows)
	job, err := LoadJob(t.Context(), db, "dataset-backfill")
	if err != nil || job.Status != StatusPending || job.ID != "dataset-backfill" {
		t.Fatalf("dataset completion leaked: job=%+v err=%v", job, err)
	}
	mock.ExpectExec("UPDATE _data_migration_runs").WithArgs("dataset-backfill@source-b", "tenant", "tenant-1", "worker", float64(30), string(StatusRunning), string(StatusCompleted), string(StatusPaused)).WillReturnResult(sqlmock.NewResult(0, 1))
	acquired, err := Acquire(t.Context(), db, "dataset-backfill", ScopeKey{Kind: "tenant", Value: "tenant-1"}, "worker", 30*time.Second)
	if err != nil || !acquired {
		t.Fatalf("dataset lease=%v err=%v", acquired, err)
	}
	mock.ExpectExec("UPDATE _data_migrations").WithArgs("dataset-backfill@source-b", string(StatusCompleted)).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := MarkJobCompleted(t.Context(), db, "dataset-backfill"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
