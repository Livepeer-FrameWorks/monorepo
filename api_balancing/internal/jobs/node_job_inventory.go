package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// nodeInventoryAssignmentMargin excludes work assigned to the node just before
// the registration: it may still be on its way over the new connection. The
// margin is measured from the job's assignment, never its latest progress.
const nodeInventoryAssignmentMargin = 2 * time.Second

// ReconcileNodeJobInventory re-dispatches the work Foghorn assigned to nodeID
// before registeredAt that the node's registration did not report as running.
// A restarted sidecar reports none of its previous jobs, so they are handed
// out again at once instead of after their lease expires. Processing jobs are
// requeued; a chapter finalization whose current attempt is not reported is
// made re-claimable, and the attempt counter rejects any late result from it.
func ReconcileNodeJobInventory(ctx context.Context, db *sql.DB, nodeID string, reported []string, registeredAt time.Time, maxRetries int, logger logging.Logger) error {
	if db == nil || nodeID == "" {
		return nil
	}
	before := registeredAt.Add(-nodeInventoryAssignmentMargin)
	q := foghorndb.New(db)

	reportedJobIDs := make([]string, 0, len(reported))
	reportedAttempts := make(map[string]int32)
	for _, id := range reported {
		if chapterID, attempt, ok := control.ChapterFinalizeIdentityFromJobID(id); ok {
			reportedAttempts[chapterID] = attempt
			continue
		}
		reportedJobIDs = append(reportedJobIDs, id)
	}

	requeued, err := q.RequeueUnreportedNodeProcessingJobs(ctx, foghorndb.RequeueUnreportedNodeProcessingJobsParams{
		NodeID:         sql.NullString{String: nodeID, Valid: true},
		AssignedBefore: sql.NullTime{Time: before, Valid: true},
		MaxRetries:     sql.NullInt32{Int32: int32(maxRetries), Valid: true},
		ReportedJobIds: reportedJobIDs,
	})
	if err != nil {
		return fmt.Errorf("requeue unreported processing jobs: %w", err)
	}

	chapters, err := q.ListNodeFinalizingDVRChapters(ctx, foghorndb.ListNodeFinalizingDVRChaptersParams{
		NodeID:        sql.NullString{String: nodeID, Valid: true},
		StartedBefore: sql.NullTime{Time: before, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("list finalizing chapters: %w", err)
	}
	var expired []string
	for _, chapter := range chapters {
		if attempt, ok := reportedAttempts[chapter.ChapterID]; ok && attempt == chapter.FinalizeAttempts {
			continue
		}
		n, expErr := q.ExpireDVRChapterFinalizeAttempt(ctx, foghorndb.ExpireDVRChapterFinalizeAttemptParams{
			ChapterID: chapter.ChapterID, NodeID: sql.NullString{String: nodeID, Valid: true}, FinalizeAttempts: chapter.FinalizeAttempts,
		})
		if expErr != nil {
			return fmt.Errorf("expire chapter %s finalize attempt: %w", chapter.ChapterID, expErr)
		}
		if n > 0 {
			expired = append(expired, chapter.ChapterID)
		}
	}

	if requeued > 0 || len(expired) > 0 {
		if logger != nil {
			logger.WithFields(logging.Fields{
				"node_id":                   nodeID,
				"requeued_processing_rows":  requeued,
				"expired_chapter_finalizes": expired,
				"reported_jobs":             len(reported),
			}).Info("Re-dispatching work a registering node did not report as running")
		}
		if requeued > 0 {
			NotifyProcessingJobQueued()
		}
		if len(expired) > 0 {
			control.NotifyChapterMutationCommitted()
		}
	}
	return nil
}
