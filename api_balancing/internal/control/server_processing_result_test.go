package control

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProcessProcessingJobResult_NilDB(t *testing.T) {
	_, _, _ = setupArtifactTestDeps(t)
	db = nil
	logger := logging.NewLogger()

	processProcessingJobResult(&pb.ProcessingJobResult{
		JobId:  "job-1",
		Status: "completed",
	}, "node-1", logger)
	// should not panic
}

func TestProcessProcessingJobResult_Completed_NoOutput(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.processing_jobs.*SET status = 'completed'").
		WithArgs("job-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&pb.ProcessingJobResult{
		JobId:  "job-1",
		Status: "completed",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessProcessingJobResult_Completed_WithOutputMeta(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.processing_jobs.*SET status = 'completed'").
		WithArgs("job-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&pb.ProcessingJobResult{
		JobId:   "job-1",
		Status:  "completed",
		Outputs: map[string]string{"resolution": "1080p"},
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessProcessingJobResult_Completed_RegistersProcessedOutput(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.processing_jobs.*SET status = 'completed'").
		WithArgs("job-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// Lookup artifact hash
	mock.ExpectQuery("SELECT artifact_hash.*FROM foghorn.processing_jobs").
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_url", "format"}).
			AddRow("art-hash", "s3://old/upload.avi", "avi"))

	// Update artifact format + reset sync while retaining the old source URL
	// until the replacement upload is durably synced.
	mock.ExpectExec("UPDATE foghorn.artifacts.*SET format.*sync_status = 'pending'.*storage_location = 'local'").
		WithArgs("mp4", "art-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&pb.ProcessingJobResult{
		JobId:           "job-1",
		Status:          "completed",
		OutputPath:      "/data/processed/output.mp4",
		OutputSizeBytes: 5000,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	repo.mu.Lock()
	if len(repo.addCachedNodePathCalls) != 1 {
		t.Fatalf("expected 1 AddCachedNodeWithPath, got %d", len(repo.addCachedNodePathCalls))
	}
	call := repo.addCachedNodePathCalls[0]
	if call.Hash != "art-hash" || call.NodeID != "node-1" || call.Path != "/data/processed/output.mp4" || call.Size != 5000 {
		t.Fatalf("unexpected call: %+v", call)
	}
	repo.mu.Unlock()

	// Check in-memory state
	sm := state.DefaultManager()
	snap := sm.GetAllNodesSnapshot()
	found := false
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			for _, a := range n.Artifacts {
				if a.ClipHash == "art-hash" {
					found = true
					if a.Format != "mp4" {
						t.Fatalf("expected format=mp4, got %s", a.Format)
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("processed artifact not found in in-memory state")
	}
}

func TestProcessProcessingJobResult_Completed_DoesNotDeleteOldS3UploadBeforeReplacementSync(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.processing_jobs").
		WithArgs("job-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery("SELECT artifact_hash").
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_url", "format"}).
			AddRow("art-hash", "s3://bucket/old/upload.avi", "avi"))

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("mp4", "art-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&pb.ProcessingJobResult{
		JobId:      "job-1",
		Status:     "completed",
		OutputPath: "/data/output.mp4",
	}, "node-1", logger)

	time.Sleep(20 * time.Millisecond)
	s3Mock.mu.Lock()
	if len(s3Mock.deleteByURLCalls) != 0 {
		t.Fatalf("expected no DeleteByURL before replacement sync, got %d", len(s3Mock.deleteByURLCalls))
	}
	s3Mock.mu.Unlock()
}

func TestProcessProcessingJobResult_Completed_SetsS3URLToNull(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.processing_jobs").
		WithArgs("job-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery("SELECT artifact_hash").
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_url", "format"}).
			AddRow("art-hash", "", "avi"))

	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'pending'.*storage_location = 'local'").
		WithArgs("mp4", "art-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&pb.ProcessingJobResult{
		JobId:      "job-1",
		Status:     "completed",
		OutputPath: "/data/output.mp4",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessProcessingJobResult_Failed(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.processing_jobs.*SET status = 'failed'.*error_message").
		WithArgs("job-fail", "ffmpeg crashed").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&pb.ProcessingJobResult{
		JobId:  "job-fail",
		Status: "failed",
		Error:  "ffmpeg crashed",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessProcessingJobResult_CallsHandler(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	var handlerCalled bool
	prevHandler := onProcessingJobResult
	onProcessingJobResult = func(_ context.Context, jobID, status string, _ map[string]string, _ string) {
		handlerCalled = true
		if jobID != "job-1" || status != "completed" {
			t.Fatalf("unexpected handler args: jobID=%s status=%s", jobID, status)
		}
	}
	t.Cleanup(func() { onProcessingJobResult = prevHandler })

	mock.ExpectExec("UPDATE foghorn.processing_jobs").
		WithArgs("job-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&pb.ProcessingJobResult{
		JobId:  "job-1",
		Status: "completed",
	}, "node-1", logger)

	if !handlerCalled {
		t.Fatal("onProcessingJobResult handler was not called")
	}
}

func TestProcessProcessingJobResult_UnknownStatus(t *testing.T) {
	_, _, _ = setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	processProcessingJobResult(&pb.ProcessingJobResult{
		JobId:  "job-1",
		Status: "unknown_status",
	}, "node-1", logger)
	// should not panic, should just log and return
}
