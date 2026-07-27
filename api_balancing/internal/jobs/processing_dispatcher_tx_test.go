package jobs

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Invariant: the ...Tx variant inserts the processing job on the CALLER's transaction —
// advisory-xact-lock, dedup, INSERT, and the guarded clip->queued UPDATE all run on the passed *sql.Tx
// so the job commits atomically with the caller's artifact row. It returns the new job id and does NOT
// commit (the caller owns commit).
func TestInsertProcessingJobWithSourceParamsTx_InsertsOnCallerTx(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	// Caller inserts the artifact row first (mimics CreateClip).
	mock.ExpectExec(`INSERT INTO foghorn\.artifacts`).WillReturnResult(sqlmock.NewResult(0, 1))
	// The tx variant: advisory lock, dedup (no active job), insert job, clip->queued UPDATE.
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("clip-h", "process").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT job_id\s+FROM foghorn\.processing_jobs`).
		WithArgs("clip-h", "process").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO foghorn\.processing_jobs`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'queued'`).
		WithArgs("clip-h", "t1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, execErr := tx.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash) VALUES ('clip-h')`); execErr != nil {
		t.Fatalf("artifact insert: %v", execErr)
	}
	jobID, insErr := InsertProcessingJobWithSourceParamsTx(ctx, tx, "t1", "clip-h", "process", nil, "", "", map[string]string{"k": "v"}, "node-1")
	if insErr != nil {
		t.Fatalf("tx insert: %v", insErr)
	}
	if jobID == "" {
		t.Fatal("expected a new job id")
	}
	if commitErr := tx.Commit(); commitErr != nil {
		t.Fatalf("commit: %v", commitErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: a failing job insert inside the caller's tx rolls the WHOLE tx back,
// so the artifact row the caller inserted is NOT committed — no orphan 'queued' clip without a job.
func TestInsertProcessingJobWithSourceParamsTx_FailureRollsBackCallerTx(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO foghorn\.artifacts`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("clip-h", "process").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT job_id\s+FROM foghorn\.processing_jobs`).
		WithArgs("clip-h", "process").WillReturnError(sql.ErrNoRows)
	// The job INSERT fails; the caller must roll the whole tx back (artifact insert included).
	mock.ExpectExec(`INSERT INTO foghorn\.processing_jobs`).
		WillReturnError(errors.New("processing_jobs insert boom"))
	mock.ExpectRollback()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, execErr := tx.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash) VALUES ('clip-h')`); execErr != nil {
		t.Fatalf("artifact insert: %v", execErr)
	}
	_, insErr := InsertProcessingJobWithSourceParamsTx(ctx, tx, "t1", "clip-h", "process", nil, "", "", nil, "")
	if insErr == nil {
		t.Fatal("expected job insert to fail")
	}
	// The caller (CreateClip via withArtifactLifecycleTx) rolls back on any closure error.
	if rbErr := tx.Rollback(); rbErr != nil {
		t.Fatalf("rollback: %v", rbErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant:an existing active job for the artifact+job_type is returned instead of
// inserting a duplicate — the dedup is preserved in the tx variant.
func TestInsertProcessingJobWithSourceParamsTx_DedupReturnsExisting(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("clip-h", "process").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT job_id\s+FROM foghorn\.processing_jobs`).
		WithArgs("clip-h", "process").
		WillReturnRows(sqlmock.NewRows([]string{"job_id"}).AddRow("existing-job"))
	mock.ExpectCommit()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID, insErr := InsertProcessingJobWithSourceParamsTx(ctx, tx, "t1", "clip-h", "process", nil, "", "", nil, "")
	if insErr != nil {
		t.Fatalf("tx dedup: %v", insErr)
	}
	if jobID != "existing-job" {
		t.Fatalf("expected existing-job, got %q", jobID)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		t.Fatalf("commit: %v", commitErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: the tx variant requires an artifact hash (dedup + advisory lock are keyed on it).
func TestInsertProcessingJobWithSourceParamsTx_RequiresArtifactHash(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectRollback()
	ctx := context.Background()
	tx, _ := db.BeginTx(ctx, nil)
	if _, insErr := InsertProcessingJobWithSourceParamsTx(ctx, tx, "t1", "", "process", nil, "", "", nil, ""); insErr == nil {
		t.Fatal("expected error for empty artifact hash")
	}
	_ = tx.Rollback()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
