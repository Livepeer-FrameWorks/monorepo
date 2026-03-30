package jobs

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestInsertProcessingJob_InsertsNewActiveJob(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("art-1", "process").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT job_id\\s+FROM foghorn.processing_jobs").
		WithArgs("art-1", "process").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO foghorn.processing_jobs").
		WithArgs(sqlmock.AnyArg(), "tenant-1", "art-1", "process", nil, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	jobID, err := InsertProcessingJob(context.Background(), db, "tenant-1", "art-1", "process", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jobID == "" {
		t.Fatal("expected non-empty job ID")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertProcessingJob_ReturnsExistingActiveJob(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("art-1", "process").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT job_id\\s+FROM foghorn.processing_jobs").
		WithArgs("art-1", "process").
		WillReturnRows(sqlmock.NewRows([]string{"job_id"}).AddRow("existing-job"))
	mock.ExpectCommit()

	jobID, err := InsertProcessingJob(context.Background(), db, "tenant-1", "art-1", "process", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jobID != "existing-job" {
		t.Fatalf("expected existing-job, got %s", jobID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
