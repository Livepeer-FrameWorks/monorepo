package jobs

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// deferUnroutablePattern matches the write that returns an unroutable job to
// the queue with a next_attempt_at, so the next polls do not claim it again.
const deferUnroutablePattern = `UPDATE foghorn.processing_jobs\s+SET status = 'queued', processing_node_id = NULL, updated_at = NOW\(\),\s+next_attempt_at = NOW\(\) \+`

// unroutableDeferralSeconds matches a deferral within the jittered bounds of
// unroutableJobDeferral: 12 to 18 seconds, several poll intervals.
type unroutableDeferralSeconds struct{}

func (unroutableDeferralSeconds) Match(v driver.Value) bool {
	seconds, ok := v.(float64)
	return ok && seconds >= 12 && seconds <= 18
}

// TestDispatchJobNoNodeRevertsAndMarksArtifactQueued covers the routing-failure
// branch: with no node able to run the job's class (the default state manager
// has no alive nodes in a unit test), the job is returned to 'queued', held
// back from the claim for a jittered deferral, and its clip artifact is
// projected back to 'queued' with a reason — never left dangling in
// 'dispatched'. No gRPC dispatch is attempted.
func TestDispatchJobNoNodeRevertsAndMarksArtifactQueued(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// 1. return the claimed job to the queue, held back until its deferral passes.
	mock.ExpectExec(deferUnroutablePattern).
		WithArgs(unroutableDeferralSeconds{}, "job-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// 2. project the artifact back to queued (clip/vod only).
	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("queued", "hash-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.dispatchJob(context.Background(), &processingJob{
		JobID:        "job-1",
		TenantID:     "tenant-1",
		ArtifactHash: sql.NullString{String: "hash-1", Valid: true},
		ArtifactType: sql.NullString{String: "clip", Valid: true},
		JobType:      "process",
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestDispatchJobNoNodeWithoutArtifactOnlyReverts confirms the artifact
// projection is skipped when the job carries no artifact hash: a job with no
// artifact reverts to queued and touches nothing else.
func TestDispatchJobNoNodeWithoutArtifactOnlyReverts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec(deferUnroutablePattern).
		WithArgs(unroutableDeferralSeconds{}, "job-2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.dispatchJob(context.Background(), &processingJob{JobID: "job-2", TenantID: "tenant-1", JobType: "process"})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
