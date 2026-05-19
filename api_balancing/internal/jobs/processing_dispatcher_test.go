package jobs

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
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
		WithArgs(sqlmock.AnyArg(), "tenant-1", "art-1", "process", nil, nil, nil, nil, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("art-1", "tenant-1").
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

func TestProcessingDispatcherProjectsArtifactProcessingStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = \$3::text,\s+error_message = CASE WHEN \$3::text = 'processing'`).
		WithArgs("art-1", "tenant-1", "processing").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{
		DB:     db,
		Logger: logging.NewLogger(),
	})
	d.markArtifactProcessing(context.Background(), &processingJob{
		JobID:        "job-1",
		TenantID:     "tenant-1",
		ArtifactHash: sql.NullString{String: "art-1", Valid: true},
		ArtifactType: sql.NullString{String: "clip", Valid: true},
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessingDispatcherDoesNotProjectNonMediaArtifactStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{
		DB:     db,
		Logger: logging.NewLogger(),
	})
	d.markArtifactProcessing(context.Background(), &processingJob{
		JobID:        "job-1",
		TenantID:     "tenant-1",
		ArtifactHash: sql.NullString{String: "art-1", Valid: true},
		ArtifactType: sql.NullString{String: "dvr", Valid: true},
	})

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

func TestProcessingDispatcherDispatchScansNullOutputProfiles(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{
		"job_id", "tenant_id", "artifact_hash", "artifact_type", "job_type", "input_codec",
		"output_profiles", "status", "retry_count", "s3_url", "source_url", "source_params", "preferred_node_id", "processes_json", "internal_name", "stream_id", "stream_internal_name",
	}).AddRow(
		"job-1", "tenant-1", "artifact-1", "vod", "process", nil,
		nil, "dispatched", 0, nil, nil, nil, nil, "", "vod_internal", nil, nil,
	)
	mock.ExpectQuery("WITH claimed AS").
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE foghorn.processing_jobs").
		WithArgs("job-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{
		DB:     db,
		Logger: logging.NewLogger(),
	})
	d.dispatch()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
