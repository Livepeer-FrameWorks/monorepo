//go:build schema_verify

package control

import (
	"database/sql"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
)

func TestR9EmptyTenantCannotMutateOrLockJob_RealPG(t *testing.T) {
	conn := startRealPG(t)
	seedActiveImportJob(t, conn)
	q := foghorndb.New(conn)
	changed, err := q.UpdateProcessingJobCache(t.Context(), foghorndb.UpdateProcessingJobCacheParams{JobID: processingFailureJob, ArtifactHash: sql.NullString{String: processingFailureHash, Valid: true}, ProcessingNodeID: sql.NullString{String: "edge-1", Valid: true}, ProcessesJson: sql.NullString{String: "[]", Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatal("empty tenant changed another tenant's processing policy")
	}
	if _, err := q.LockProcessingJobForCompletion(t.Context(), foghorndb.LockProcessingJobForCompletionParams{JobID: processingFailureJob}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty tenant matched completion: %v", err)
	}
	if _, err := q.LockProcessingJobForFailure(t.Context(), foghorndb.LockProcessingJobForFailureParams{JobID: processingFailureJob}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty tenant matched failure: %v", err)
	}
}
