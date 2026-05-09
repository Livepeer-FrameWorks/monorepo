package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Single idempotent transition driving a DVR from recording to a terminal
// state. Stop paths such as RECORDING_END, StopDVR, and recording-state
// reconciliation call FinalizeDVR(dvr_hash); only the first caller wins the
// recording -> finalizing transition and does the work.
//
// Inside finalizing:
//   1. Bounded retry of pending/failed_upload segments via
//      RetryDVRSegmentUpload (sidecar re-attempts + emits MarkDVRSegmentUploaded).
//   2. Reclassify remaining non-uploaded rows as lost_local.
//   3. Close the active current chapter by writing its VOD-shaped manifest
//      with #EXT-X-ENDLIST and then flipping is_current=false.
//   4. Compute retention_until from the persisted dvr_retention_days
//      snapshot (post-end semantics; tier days at start, applied at end).
//   5. Transition the artifact: completed | completed_partial | failed.
//
// Archive playback is chapter-only; no whole-artifact manifest is written
// to S3. Replay viewers go through RetrieveDVRChapter for bounded views
// over the dvr_segments ledger.

// FinalizeRetrySeconds bounds how long FinalizeDVR will wait for outstanding
// pending/failed_upload segments before classifying them as lost_local. The
// upper bound for a finalize call is roughly this value plus chapter close
// bookkeeping and DB writes.
var FinalizeRetrySeconds = 60

// FinalizeOptions carries the sidecar's local view of the recording. They
// are advisory; Foghorn computes the canonical artifact status from the
// dvr_segments ledger.
type FinalizeOptions struct {
	ReportedStatus  string
	ReportedError   string
	DurationSeconds int64
	SizeBytes       uint64
	StorageNodeID   string
}

// FinalizeResult is what FinalizeDVR returns to the caller.
type FinalizeResult struct {
	ArtifactStatus string // completed | completed_partial | failed
	ManifestPath   string // always empty; archive playback is chapter-only
	UploadedCount  int
	LostCount      int
	NoOp           bool // true when another caller already finalized this DVR
}

// FinalizeDVR is the single entry point for DVR finalization. Idempotent:
// the first caller wins the recording->finalizing transition and does the
// work; subsequent callers return the existing terminal state with NoOp=true.
func FinalizeDVR(ctx context.Context, dvrHash string, opts FinalizeOptions) (FinalizeResult, error) {
	if dvrHash == "" {
		return FinalizeResult{}, errors.New("dvr_hash required")
	}
	if db == nil {
		return FinalizeResult{}, sql.ErrConnDone
	}

	logger := logging.NewLogger()

	// Atomic claim of the active/stopping->finalizing transition. If another
	// caller already moved past that lifecycle point we short-circuit.
	var prevStatus string
	err := db.QueryRowContext(ctx, `
		UPDATE foghorn.artifacts
		   SET status = 'finalizing',
		       updated_at = NOW(),
		       ended_at = COALESCE(ended_at, NOW())
		 WHERE artifact_hash = $1
		   AND artifact_type = 'dvr'
		   AND status IN ('requested', 'starting', 'recording', 'stopping')
	 RETURNING status
	`, dvrHash).Scan(&prevStatus)
	if errors.Is(err, sql.ErrNoRows) {
		// Already terminal or in flight. Read current status and return NoOp.
		current, readErr := readArtifactStatus(ctx, dvrHash)
		if readErr != nil {
			return FinalizeResult{}, readErr
		}
		if backfillErr := backfillExistingDVRRetention(ctx, dvrHash, logger); backfillErr != nil {
			return FinalizeResult{ArtifactStatus: current, NoOp: true}, backfillErr
		}
		return FinalizeResult{ArtifactStatus: current, NoOp: true}, nil
	}
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("claim finalizing: %w", err)
	}

	// Bounded retry of in-flight uploads via the sidecar control stream.
	// requestRetryDVRSegmentUploads is best-effort; segments that don't
	// finish in the budget become lost_local below.
	if FinalizeRetrySeconds > 0 {
		retryDeadline := time.Now().Add(time.Duration(FinalizeRetrySeconds) * time.Second)
		retryCtx, cancel := context.WithDeadline(ctx, retryDeadline)
		if waitErr := waitForOutstandingUploads(retryCtx, dvrHash, opts.StorageNodeID, logger); waitErr != nil && !errors.Is(waitErr, context.DeadlineExceeded) && !errors.Is(waitErr, context.Canceled) {
			logger.WithError(waitErr).WithField("dvr_hash", dvrHash).Warn("FinalizeDVR retry loop ended with error")
		}
		cancel()
	}

	lost, err := MarkRemainingDVRSegmentsLost(ctx, dvrHash, "upload_failed")
	if err != nil {
		logger.WithError(err).Warn("Failed to reclassify remaining segments as lost_local")
	}
	if lost > 0 {
		logger.WithField("segments_lost", lost).Warn("DVR finalized with lost segments; chapter manifests will include #EXT-X-GAP")
	}

	// Compute retention_until from the persisted dvr_retention_days column,
	// snapshotted at start time. Never re-resolve the tier here because
	// the tenant's plan may have changed during a months-long stream. Days = 0
	// means "no auto-expire" (admin-managed only). This applies to every
	// terminal outcome, including failed DVRs with no playable segments.
	endedAt := time.Now().UTC()
	retentionDays := readPersistedRetentionDays(ctx, dvrHash)
	var retentionUntilArg interface{}
	if retentionDays > 0 {
		retentionUntilArg = endedAt.Add(time.Duration(retentionDays) * 24 * time.Hour)
	}

	// Classification reads bounded aggregates only and never enumerates
	// the full segment list. For unbounded artifact lifetimes (months-
	// long 24/7 streams) the whole-table scan would explode; the chapter
	// generator already operates on bounded ranges per chapter. Final
	// status is derived from counts:
	//   uploaded > 0 AND lost == 0: completed
	//   uploaded > 0 AND lost > 0:  completed_partial
	//   uploaded == 0:              failed
	uploadedCount, lostCount, err := classifyFinalCounts(ctx, dvrHash)
	if err != nil {
		if failErr := setArtifactFailed(ctx, dvrHash, fmt.Sprintf("classification failed: %v", err), retentionUntilArg, endedAt); failErr != nil {
			logger.WithError(failErr).WithField("dvr_hash", dvrHash).Warn("setArtifactFailed after classification error also failed")
		}
		if backfillErr := backfillDVRRetention(ctx, dvrHash, retentionUntilArg); backfillErr != nil {
			logger.WithError(backfillErr).WithField("dvr_hash", dvrHash).Error("DVR retention back-fill failed")
		}
		return FinalizeResult{ArtifactStatus: "failed"}, fmt.Errorf("classify final counts: %w", err)
	}
	if uploadedCount == 0 {
		if failErr := setArtifactFailed(ctx, dvrHash, "no playable segments", retentionUntilArg, endedAt); failErr != nil {
			logger.WithError(failErr).WithField("dvr_hash", dvrHash).Warn("setArtifactFailed after no-playable also failed")
		}
		result := FinalizeResult{ArtifactStatus: "failed", LostCount: lostCount}
		if backfillErr := backfillDVRRetention(ctx, dvrHash, retentionUntilArg); backfillErr != nil {
			logger.WithError(backfillErr).WithField("dvr_hash", dvrHash).Error("DVR retention back-fill failed")
			return result, backfillErr
		}
		return result, nil
	}

	finalStatus := "completed"
	if lostCount > 0 {
		finalStatus = "completed_partial"
	}

	if cErr := WithDVRChapterMutationLock(ctx, dvrHash, func() error {
		return FinalizeCurrentChapter(ctx, dvrHash, logger)
	}); cErr != nil {
		logger.WithError(cErr).WithField("dvr_hash", dvrHash).Warn("FinalizeDVR: close current chapter failed")
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		   SET status            = $2,
		       size_bytes        = COALESCE(NULLIF($3, 0)::bigint, size_bytes),
		       duration_seconds  = COALESCE(NULLIF($4, 0)::int, duration_seconds),
		       retention_until   = $5,
		       updated_at        = NOW(),
		       ended_at          = COALESCE(ended_at, $6)
		 WHERE artifact_hash = $1
	`, dvrHash, finalStatus, int64(opts.SizeBytes), opts.DurationSeconds, retentionUntilArg, endedAt); err != nil {
		logger.WithError(err).Error("Failed to write final artifact status")
		return FinalizeResult{ArtifactStatus: finalStatus, UploadedCount: uploadedCount, LostCount: lostCount}, fmt.Errorf("write final artifact status: %w", err)
	}

	logger.WithFields(logging.Fields{
		"final_status":      finalStatus,
		"segments_uploaded": uploadedCount,
		"segments_lost":     lostCount,
		"retention_days":    retentionDays,
	}).Info("DVR finalized (manifest is chapter-only, no whole-artifact write)")

	result := FinalizeResult{
		ArtifactStatus: finalStatus,
		// ManifestPath intentionally empty: archive playback is chapter-only;
		// the canonical "what to play" surface is RetrieveDVRChapter.
		UploadedCount: uploadedCount,
		LostCount:     lostCount,
	}
	if backfillErr := backfillDVRRetention(ctx, dvrHash, retentionUntilArg); backfillErr != nil {
		logger.WithError(backfillErr).WithField("dvr_hash", dvrHash).Error("DVR retention back-fill failed")
		return result, backfillErr
	}
	return result, nil
}

// classifyFinalCounts returns (uploaded_or_deleted_local, lost_local) via
// a single bounded aggregate query. Used by FinalizeDVR to classify the
// terminal status without enumerating the segment list.
func classifyFinalCounts(ctx context.Context, dvrHash string) (int, int, error) {
	if db == nil {
		return 0, 0, sql.ErrConnDone
	}
	var uploaded, lost int
	err := db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status IN ('uploaded', 'deleted_local')),
			COUNT(*) FILTER (WHERE status = 'lost_local')
		  FROM foghorn.dvr_segments
		 WHERE artifact_hash = $1
	`, dvrHash).Scan(&uploaded, &lost)
	if err != nil {
		return 0, 0, err
	}
	return uploaded, lost, nil
}

// readPersistedRetentionDays reads the snapshot taken at DVR start.
// Returns 0 if not set; caller treats 0 as "no auto-expire".
func readPersistedRetentionDays(ctx context.Context, dvrHash string) int {
	if db == nil {
		return 0
	}
	var days sql.NullInt32
	if err := db.QueryRowContext(ctx, `
		SELECT dvr_retention_days
		  FROM foghorn.artifacts
		 WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, dvrHash).Scan(&days); err != nil {
		return 0
	}
	if !days.Valid {
		return 0
	}
	return int(days.Int32)
}

func waitForOutstandingUploads(ctx context.Context, dvrHash, preferNodeID string, logger logging.Logger) error {
	const retryBatchSize = 500

	// Light loop: every 2s, list a bounded pending/failed_upload batch, send
	// RetryDVRSegmentUpload to the recording sidecar, and exit when the batch
	// list empties or the deadline hits.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		pending, err := ListPendingDVRSegments(ctx, dvrHash, 0, retryBatchSize)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		names := make([]string, 0, len(pending))
		for _, r := range pending {
			names = append(names, r.SegmentName)
		}
		if err := SendRetryDVRSegmentUpload(preferNodeID, &pb.RetryDVRSegmentUpload{
			DvrHash:      dvrHash,
			SegmentNames: names,
		}); err != nil {
			logger.WithError(err).Debug("retry-upload push failed (will retry on next tick)")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func setArtifactFailed(ctx context.Context, dvrHash, reason string, retentionUntilArg interface{}, endedAt time.Time) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		   SET status = 'failed',
		       error_message = $2,
		       retention_until = $3,
		       updated_at = NOW(),
		       ended_at = COALESCE(ended_at, $4)
		 WHERE artifact_hash = $1
	`, dvrHash, reason, retentionUntilArg, endedAt)
	return err
}

func backfillDVRRetention(ctx context.Context, dvrHash string, retentionUntilArg interface{}) error {
	if CommodoreClient == nil || retentionUntilArg == nil {
		return nil
	}
	retentionTime, ok := retentionUntilArg.(time.Time)
	if !ok {
		return fmt.Errorf("retention_until has unexpected type for %s", dvrHash)
	}
	updateReq := &pb.UpdateDVRRetentionRequest{
		TenantId:       readArtifactTenant(ctx, dvrHash),
		DvrHash:        dvrHash,
		RetentionUntil: timestamppb.New(retentionTime),
	}
	updateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, updateErr := CommodoreClient.UpdateDVRRetention(updateCtx, updateReq); updateErr != nil {
		return fmt.Errorf("update Commodore DVR retention: %w", updateErr)
	}
	return nil
}

func backfillExistingDVRRetention(ctx context.Context, dvrHash string, logger logging.Logger) error {
	if db == nil {
		return sql.ErrConnDone
	}
	var retentionUntil sql.NullTime
	if err := db.QueryRowContext(ctx, `
		SELECT retention_until
		  FROM foghorn.artifacts
		 WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, dvrHash).Scan(&retentionUntil); err != nil {
		return err
	}
	if !retentionUntil.Valid {
		return nil
	}
	if err := backfillDVRRetention(ctx, dvrHash, retentionUntil.Time); err != nil {
		logger.WithError(err).WithField("dvr_hash", dvrHash).Error("DVR retention back-fill retry failed")
		return err
	}
	return nil
}

func readArtifactStatus(ctx context.Context, dvrHash string) (string, error) {
	if db == nil {
		return "", sql.ErrConnDone
	}
	var s string
	err := db.QueryRowContext(ctx, `
		SELECT status FROM foghorn.artifacts WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, dvrHash).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("dvr %s not found", dvrHash)
	}
	return s, err
}

// readArtifactTenant fetches the tenant_id for a finalized DVR artifact.
// Used by the FinalizeDVR retention back-fill into Commodore. Returns the
// empty string on miss; the back-fill skips when tenant cannot be resolved
// rather than blocking finalize.
func readArtifactTenant(ctx context.Context, dvrHash string) string {
	if db == nil {
		return ""
	}
	var t string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(tenant_id::text, '') FROM foghorn.artifacts WHERE artifact_hash = $1`,
		dvrHash,
	).Scan(&t); err != nil {
		return ""
	}
	return t
}
