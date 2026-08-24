package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
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
//   2. Reclassify remaining non-uploaded rows as lost_local. Chapter
//      finalization may trim a lost terminal tail, but internal lost rows
//      still move the chapter to failed_source_missing.
//   3. Close the active current chapter row by flipping is_current=false
//      and state=closed; the finalization queue then produces the chapter
//      VOD artifact.
//   4. Compute retention_until from the persisted dvr_retention_days
//      snapshot (post-end semantics; tier days at start, applied at end).
//   5. Transition the artifact: completed | completed_partial | failed.
//
// Replay viewers use chapter VOD artifacts; no whole-artifact playlist
// is written. Each chapter is addressed by its Commodore-minted public
// playback_id (commodore.dvr_chapter_playback).

// FinalizeRetrySeconds bounds how long FinalizeDVR will wait for outstanding
// pending/failed_upload segments before classifying them as lost_local. The
// upper bound for a finalize call is roughly this value plus chapter close
// bookkeeping and DB writes.
var FinalizeRetrySeconds = 60

const staleDVRFinalizingAfter = 10 * time.Minute

// FinalizeOptions carries the sidecar's local view of the recording. They
// are advisory; Foghorn computes the canonical artifact status from the
// dvr_segments ledger.
type FinalizeOptions struct {
	ReportedStatus  string
	ReportedError   string
	DurationSeconds int64
	SizeBytes       uint64
	StorageNodeID   string
	// ReportingNodeID is the authenticated node that reported this stop over the control stream. When
	// set, FinalizeDVR rejects the report unless it matches the durable dispatch owner
	// (dvr_start_dispatch.node_id) of an active recording — so a node that merely knows the DVR hash
	// cannot finalize another node's live recording. Empty for control-plane-internal callers
	// (RECORDING_END, StopDVR, the recovery worker), which are authoritative and skip the node bind.
	ReportingNodeID string
	// RetainStopObligation keeps the dvr_start_dispatch stop obligation (state='stop_pending' and the
	// re-dispatch fields) in place across this terminal transition. Default (false): a terminal DVR has no
	// live recording, so the obligation is cleared while the immutable recording owner is RETAINED
	// (dvr_start_dispatch -> {node_id}), leaving nothing for the recovery drain to match but keeping the
	// owner that post-stop segment reclaim authorizes against. Set true only when the caller is surfacing
	// 'failed' to the user while a recording may still be running and the control plane must keep
	// re-sending stop (the recovery hard-grace path) — then both node_id AND the obligation are kept.
	RetainStopObligation bool
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

	// Bind a node-reported stop to the durable dispatch owner BEFORE any mutation: an active recording
	// may only be finalized by the node it was dispatched to. The owner check is scoped to the tenant
	// that owns the hash (resolved from the PK row). A duplicate stop on a genuinely ABSENT row is a safe
	// no-op (idempotent mode); an existing terminal row still requires the owner match. Control-plane-
	// internal callers leave ReportingNodeID empty and skip this bind (they are authoritative). Fails
	// closed on a tenant-lookup query error (no claim, no mutation).
	if opts.ReportingNodeID != "" {
		tenantID, found, ownErr := dvrOwnerTenant(ctx, dvrHash)
		if ownErr != nil {
			return FinalizeResult{}, fmt.Errorf("resolve dvr owner tenant: %w", ownErr)
		}
		if !found {
			// A node-reported stop targeting a genuinely absent DVR is an idempotent no-op: there is
			// no row to claim, so return NoOp rather than falling through to a "dvr not found" error.
			return FinalizeResult{NoOp: true}, nil
		}
		ok, chkErr := dvrReportNodeAuthorized(ctx, dvrHash, tenantID, opts.ReportingNodeID, dvrAuthIdempotentStop)
		if chkErr != nil {
			return FinalizeResult{}, fmt.Errorf("verify dvr stop reporting node: %w", chkErr)
		}
		if !ok {
			logger.WithFields(logging.Fields{"dvr_hash": dvrHash, "reporting_node": opts.ReportingNodeID}).
				Warn("FinalizeDVR: rejecting stop from a node that is not the dispatched recording owner")
			return FinalizeResult{NoOp: true}, fmt.Errorf("dvr stop for %s rejected: reporting node %q is not the dispatched recording node", dvrHash, opts.ReportingNodeID)
		}
	}

	// Atomic claim of the active/stopping->finalizing transition. A stale
	// finalizing row is also reclaimable: a previous finalizer may have crashed
	// or failed after the claim but before writing the terminal status.
	claimed, err := foghorndb.New(db).ClaimDVRFinalization(ctx, foghorndb.ClaimDVRFinalizationParams{
		ArtifactHash: dvrHash, StaleSeconds: staleDVRFinalizingAfter.Seconds(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		// Already terminal or in flight. Read current status and return NoOp.
		current, readErr := readArtifactStatus(ctx, dvrHash)
		if readErr != nil {
			return FinalizeResult{}, readErr
		}
		// A finalize signal (typically the node's DVRStopped) reaching an already-terminal row IS the
		// stop acknowledgement: release any outstanding stop obligation so the recovery drain stops. The
		// clear is tenant-scoped (owner tenant resolved from the terminal row); the helper's own guard
		// clears only genuinely terminal rows, never an active/finalizing one.
		if !opts.RetainStopObligation {
			if clrTenant, clrFound, clrErr := dvrOwnerTenant(ctx, dvrHash); clrErr != nil {
				logger.WithError(clrErr).WithField("dvr_hash", dvrHash).Warn("FinalizeDVR: failed to resolve tenant to clear stop obligation on terminal NoOp")
			} else if clrFound {
				if clrErr := clearDVRStopObligation(ctx, dvrHash, clrTenant); clrErr != nil {
					logger.WithError(clrErr).WithField("dvr_hash", dvrHash).Warn("FinalizeDVR: failed to clear stop obligation on terminal NoOp")
				}
			}
		}
		if backfillErr := backfillExistingDVRRetention(ctx, dvrHash, logger); backfillErr != nil {
			return FinalizeResult{ArtifactStatus: current, NoOp: true}, backfillErr
		}
		return FinalizeResult{ArtifactStatus: current, NoOp: true}, nil
	}
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("claim finalizing: %w", err)
	}
	claimTenant := claimed.TenantID

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
		logger.WithField("segments_lost", lost).Warn("DVR finalized with lost segments; terminal chapter tail may be trimmed, internal chapter loss will fail the chapter")
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
		failApplied, failErr := setArtifactFailed(ctx, dvrHash, fmt.Sprintf("classification failed: %v", err), retentionUntilArg, endedAt, claimTenant, opts.RetainStopObligation)
		if failErr != nil {
			logger.WithError(failErr).WithField("dvr_hash", dvrHash).Warn("setArtifactFailed after classification error also failed")
		}
		if backfillErr := backfillDVRRetention(ctx, dvrHash, retentionUntilArg); backfillErr != nil {
			logger.WithError(backfillErr).WithField("dvr_hash", dvrHash).Error("DVR retention back-fill failed")
		}
		// Only report 'failed' if we actually wrote it; if the row was concurrently deleted/re-claimed,
		// report its real current status so a deleted artifact isn't surfaced as failed.
		status := "failed"
		if !failApplied {
			if cur, curErr := readArtifactStatus(ctx, dvrHash); curErr == nil {
				status = cur
			}
		}
		return FinalizeResult{ArtifactStatus: status, NoOp: !failApplied}, fmt.Errorf("classify final counts: %w", err)
	}
	if uploadedCount == 0 {
		failApplied, failErr := setArtifactFailed(ctx, dvrHash, "no playable segments", retentionUntilArg, endedAt, claimTenant, opts.RetainStopObligation)
		if failErr != nil {
			logger.WithError(failErr).WithField("dvr_hash", dvrHash).Warn("setArtifactFailed after no-playable also failed")
		}
		status := "failed"
		if !failApplied {
			if cur, curErr := readArtifactStatus(ctx, dvrHash); curErr == nil {
				status = cur
			}
		}
		result := FinalizeResult{ArtifactStatus: status, LostCount: lostCount, NoOp: !failApplied}
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

	// Close the terminal in-flight chapter. Truncates end_ms to the
	// recording's ended_at when the recording stopped mid-interval.
	// The closed row enters the finalization queue, which produces the
	// canonical .mkv chapter artifact in the background.
	terminalMs := endedAt.UnixMilli()
	if cErr := WithDVRChapterMutationLock(ctx, dvrHash, func() error {
		return CloseTerminalChapter(ctx, dvrHash, terminalMs, logger)
	}); cErr != nil {
		logger.WithError(cErr).WithField("dvr_hash", dvrHash).Warn("FinalizeDVR: close terminal chapter failed")
	}

	// The parent's final retention horizon and its propagation onto any already-allocated child
	// chapters (window chapters that finalized while the DVR was still recording inherited a NULL
	// keep-forever horizon) commit as ONE transaction, so the parent and its children never
	// diverge. Chapters allocated AFTER this point inherit the now-set horizon at allocation.
	finTx, txErr := db.BeginTx(ctx, nil)
	if txErr != nil {
		return FinalizeResult{ArtifactStatus: finalStatus, UploadedCount: uploadedCount, LostCount: lostCount}, fmt.Errorf("begin finalize tx: %w", txErr)
	}
	finCommitted := false
	defer func() {
		if !finCommitted {
			finTx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
		}
	}()
	// Guard on status='finalizing' + tenant: this finalizer claimed the row as 'finalizing', but the long
	// retry/classify work above ran outside a lock, so a concurrent DELETE could have moved it to
	// 'deleted'. Only transition a row STILL 'finalizing' (RowsAffected==0 => we lost the race, e.g. the
	// artifact was deleted) — never resurrect a terminal row with a stale 'completed'.
	qfin := foghorndb.New(finTx)
	n, finErr := qfin.CompleteDVRFinalization(ctx, foghorndb.CompleteDVRFinalizationParams{
		FinalStatus: finalStatus, SizeBytes: int64(opts.SizeBytes), DurationSeconds: opts.DurationSeconds,
		RetentionUntil: retentionNullTime(retentionUntilArg), EndedAt: sql.NullTime{Time: endedAt, Valid: true},
		RetainStopObligation: opts.RetainStopObligation, ArtifactHash: dvrHash, TenantID: claimTenant,
	})
	if finErr != nil {
		logger.WithError(finErr).Error("Failed to write final artifact status")
		return FinalizeResult{ArtifactStatus: finalStatus, UploadedCount: uploadedCount, LostCount: lostCount}, fmt.Errorf("write final artifact status: %w", finErr)
	}
	if n == 0 {
		// The row is no longer 'finalizing' (concurrently deleted or re-claimed). Do NOT commit a terminal
		// state or enqueue a STOPPED event over it — leave the tx to roll back.
		current, curErr := readArtifactStatus(ctx, dvrHash)
		if curErr != nil {
			logger.WithError(curErr).WithField("dvr_hash", dvrHash).Debug("FinalizeDVR: could not read current status after lost finalize race")
		}
		logger.WithFields(logging.Fields{"dvr_hash": dvrHash, "current_status": current}).
			Warn("FinalizeDVR: row left 'finalizing' during work (deleted/re-claimed); skipping terminal transition")
		return FinalizeResult{ArtifactStatus: current, UploadedCount: uploadedCount, LostCount: lostCount, NoOp: true}, nil
	}
	if _, propErr := PropagateChapterRetentionTx(ctx, finTx, dvrHash, retentionUntilArg); propErr != nil {
		logger.WithError(propErr).WithField("dvr_hash", dvrHash).Error("Failed to propagate retention to child chapters")
		return FinalizeResult{ArtifactStatus: finalStatus, UploadedCount: uploadedCount, LostCount: lostCount}, fmt.Errorf("propagate chapter retention: %w", propErr)
	}
	// Build and enqueue the terminal DVR STOPPED lifecycle event on THIS transaction, so the terminal
	// state and its analytics event commit atomically (no crash-lossy, Decklog-gated goroutine). Context
	// (tenant/stream/user/retention/started) is read from the just-updated row under the same tx.
	{
		row, scanErr := qfin.GetDVRLifecycleContext(ctx, dvrHash)
		if scanErr != nil {
			logger.WithError(scanErr).WithField("dvr_hash", dvrHash).Error("Failed to read DVR context for terminal lifecycle event")
			return FinalizeResult{ArtifactStatus: finalStatus, UploadedCount: uploadedCount, LostCount: lostCount}, fmt.Errorf("read dvr lifecycle context: %w", scanErr)
		}
		dvrData := &ipcpb.DVRLifecycleData{Status: ipcpb.DVRLifecycleData_STATUS_STOPPED, DvrHash: dvrHash}
		if opts.StorageNodeID != "" {
			dvrData.NodeId = &opts.StorageNodeID
		}
		rowTenant, rowUser, rowStreamID, rowInternal := row.TenantID, row.UserID, row.StreamID, row.StreamInternalName
		rowRetention, rowStarted := row.RetentionUntil, row.StartedAt
		if rowTenant != "" {
			dvrData.TenantId = &rowTenant
		}
		if rowStreamID != "" {
			dvrData.StreamId = &rowStreamID
		}
		if rowInternal.String != "" {
			dvrData.StreamInternalName = &rowInternal.String
		}
		if rowUser != "" {
			dvrData.UserId = &rowUser
		}
		if opts.SizeBytes > 0 {
			sb := opts.SizeBytes
			dvrData.SizeBytes = &sb
		}
		if opts.ReportedError != "" {
			dvrData.Error = &opts.ReportedError
		}
		if rowRetention.Valid {
			exp := rowRetention.Time.Unix()
			dvrData.ExpiresAt = &exp
		}
		if rowStarted.Valid {
			st := rowStarted.Time.Unix()
			dvrData.StartedAt = &st
		}
		et := endedAt.Unix()
		dvrData.EndedAt = &et
		if enqErr := artifactoutbox.EnqueueDVRLifecycleTx(ctx, finTx, dvrData); enqErr != nil {
			logger.WithError(enqErr).WithField("dvr_hash", dvrHash).Error("Failed to enqueue terminal DVR lifecycle event")
			return FinalizeResult{ArtifactStatus: finalStatus, UploadedCount: uploadedCount, LostCount: lostCount}, fmt.Errorf("enqueue dvr terminal lifecycle: %w", enqErr)
		}
	}
	if commitErr := finTx.Commit(); commitErr != nil {
		return FinalizeResult{ArtifactStatus: finalStatus, UploadedCount: uploadedCount, LostCount: lostCount}, fmt.Errorf("commit finalize: %w", commitErr)
	}
	finCommitted = true
	// size_bytes/duration_seconds were written to foghorn.artifacts above; the reconciler projects the
	// DVR duration onto the catalog (single writer), so no immediate duration RPC here.

	logger.WithFields(logging.Fields{
		"final_status":      finalStatus,
		"segments_uploaded": uploadedCount,
		"segments_lost":     lostCount,
		"retention_days":    retentionDays,
	}).Info("DVR finalized")

	result := FinalizeResult{
		ArtifactStatus: finalStatus,
		UploadedCount:  uploadedCount,
		LostCount:      lostCount,
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
	row, err := foghorndb.New(db).CountDVRFinalSegments(ctx, dvrHash)
	if err != nil {
		return 0, 0, err
	}
	return int(row.UploadedCount), int(row.LostCount), nil
}

// readPersistedRetentionDays reads the snapshot taken at DVR start.
// Returns 0 if not set; caller treats 0 as "no auto-expire".
func readPersistedRetentionDays(ctx context.Context, dvrHash string) int {
	if db == nil {
		return 0
	}
	days, err := foghorndb.New(db).GetDVRRetentionDays(ctx, dvrHash)
	if err != nil {
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
		refs := make([]*ipcpb.DVRSegmentRef, 0, len(pending))
		for _, r := range pending {
			names = append(names, r.SegmentName)
			refs = append(refs, &ipcpb.DVRSegmentRef{
				SegmentName:  r.SegmentName,
				Sequence:     r.Sequence,
				MediaStartMs: r.MediaStartMs,
				MediaEndMs:   r.MediaEndMs,
				DurationMs:   r.DurationMs,
				Status:       r.Status,
			})
		}
		if err := SendRetryDVRSegmentUpload(preferNodeID, &ipcpb.RetryDVRSegmentUpload{
			DvrHash:      dvrHash,
			SegmentNames: names,
			Segments:     refs,
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

// setArtifactFailed drives a DVR artifact to its terminal 'failed' state and commits the
// matching DVR STOPPED->FAILED lifecycle event on the SAME transaction, so an early-failure
// finalize (classification error, no playable segments) can never leave a 'failed' artifact
// without its durable analytics event — a capture bypass. Mirrors the atomic STOPPED emit at
// the bottom of FinalizeDVR; every caller therefore gets atomic state+event.
// setArtifactFailed writes the terminal 'failed' state + its FAILED lifecycle event atomically, guarded
// on the row still being 'finalizing' for this tenant. It returns applied=false (no error) when the
// guard matched nothing — the row was concurrently deleted/re-claimed — so the caller does NOT report a
// 'failed' status that was never written.
func setArtifactFailed(ctx context.Context, dvrHash, reason string, retentionUntilArg interface{}, endedAt time.Time, tenantID string, retainStopObligation bool) (applied bool, err error) {
	if db == nil {
		return false, sql.ErrConnDone
	}
	tx, txErr := db.BeginTx(ctx, nil)
	if txErr != nil {
		return false, fmt.Errorf("begin setArtifactFailed tx: %w", txErr)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
		}
	}()
	// Guard on status='finalizing' + tenant so a concurrent DELETE during finalization is not resurrected
	// as 'failed'. RowsAffected==0 => the row is no longer finalizing (deleted/re-claimed): skip the
	// terminal write AND the FAILED event.
	qtx := foghorndb.New(tx)
	n, execErr := qtx.FailDVRFinalization(ctx, foghorndb.FailDVRFinalizationParams{
		ErrorMessage: reason, RetentionUntil: retentionNullTime(retentionUntilArg), EndedAt: sql.NullTime{Time: endedAt, Valid: true},
		RetainStopObligation: retainStopObligation, ArtifactHash: dvrHash, TenantID: tenantID,
	})
	if execErr != nil {
		return false, fmt.Errorf("write failed artifact status: %w", execErr)
	}
	if n == 0 {
		return false, nil // lost the race (deleted/re-claimed) — nothing to fail, no event
	}

	// Read tenant/stream context from the just-updated row under the same tx (mirrors the
	// STOPPED path). Missing context degrades to unset optional fields rather than failing.
	row, scanErr := qtx.GetDVRLifecycleContext(ctx, dvrHash)
	if scanErr != nil {
		return false, fmt.Errorf("read dvr failed lifecycle context: %w", scanErr)
	}
	rowTenant, rowUser, rowStreamID, rowInternal := row.TenantID, row.UserID, row.StreamID, row.StreamInternalName
	rowRetention, rowStarted := row.RetentionUntil, row.StartedAt
	dvrData := &ipcpb.DVRLifecycleData{
		Status:  ipcpb.DVRLifecycleData_STATUS_FAILED,
		DvrHash: dvrHash,
		Error:   &reason,
	}
	if rowTenant != "" {
		dvrData.TenantId = &rowTenant
	}
	if rowStreamID != "" {
		dvrData.StreamId = &rowStreamID
	}
	if rowInternal.String != "" {
		dvrData.StreamInternalName = &rowInternal.String
	}
	if rowUser != "" {
		dvrData.UserId = &rowUser
	}
	if rowRetention.Valid {
		exp := rowRetention.Time.Unix()
		dvrData.ExpiresAt = &exp
	}
	if rowStarted.Valid {
		st := rowStarted.Time.Unix()
		dvrData.StartedAt = &st
	}
	et := endedAt.Unix()
	dvrData.EndedAt = &et
	if enqErr := artifactoutbox.EnqueueDVRLifecycleTx(ctx, tx, dvrData); enqErr != nil {
		return false, fmt.Errorf("enqueue dvr failed lifecycle: %w", enqErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return false, fmt.Errorf("commit setArtifactFailed: %w", commitErr)
	}
	committed = true
	return true, nil
}

func backfillDVRRetention(ctx context.Context, dvrHash string, retentionUntilArg interface{}) error {
	if CommodoreClient == nil || retentionUntilArg == nil {
		return nil
	}
	retentionTime, ok := retentionUntilArg.(time.Time)
	if !ok {
		return fmt.Errorf("retention_until has unexpected type for %s", dvrHash)
	}
	updateReq := &commodorepb.UpdateDVRRetentionRequest{
		TenantId:       readArtifactTenant(ctx, dvrHash),
		DvrHash:        dvrHash,
		RetentionUntil: timestamppb.New(retentionTime),
	}
	updateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, updateErr := CommodoreClient.UpdateDVRRetention(updateCtx, updateReq)
	if updateErr != nil {
		return fmt.Errorf("update Commodore DVR retention: %w", updateErr)
	}
	// Commodore reports Updated=false when the catalog row isn't there yet (registration lag):
	// silently acknowledging that would leave /library showing the wrong (or no) expiry. Surface
	// it as an error so the caller retries; the reconciler's snapshot projection is the durable
	// backstop that eventually carries retention_until onto the catalog regardless.
	if resp != nil && !resp.GetUpdated() {
		return fmt.Errorf("commodore DVR retention not applied (catalog row missing) for %s", dvrHash)
	}
	return nil
}

func backfillExistingDVRRetention(ctx context.Context, dvrHash string, logger logging.Logger) error {
	if db == nil {
		return sql.ErrConnDone
	}
	retentionUntil, err := foghorndb.New(db).GetDVRRetentionUntil(ctx, dvrHash)
	if err != nil {
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

// clearDVRStopObligation releases a stop obligation on a DVR that has reached a terminal state, so the
// recovery stop drain stops re-sending. It clears only the MUTABLE obligation (state/re-dispatch fields)
// and RETAINS the immutable recording owner (dvr_start_dispatch -> {node_id}), because post-stop segment
// reclaim still authorizes against that owner. Tenant-scoped by the resolved owner tenant. Guarded on a
// terminal, node-owned dispatch row: it never clears an active/finalizing recording's descriptor (whose
// owning finalizer decides whether to retain), and it is a no-op once the obligation is already gone.
func clearDVRStopObligation(ctx context.Context, dvrHash, tenantID string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	return foghorndb.New(db).ClearDVRStopObligation(ctx, foghorndb.ClearDVRStopObligationParams{ArtifactHash: dvrHash, TenantID: tenantID})
}

// dvrAuthMode selects how a node-reported DVR op is authorized against the persisted dispatch owner.
type dvrAuthMode int

const (
	// dvrAuthStrict authorizes ONLY when the reporting node equals the persisted dispatch owner,
	// REGARDLESS of lifecycle status. A missing tenant-owned row, an owner that cannot be resolved, or
	// a terminal row whose owner does not match the reporter all REJECT — terminal status never grants
	// access. Used by every segment-ledger mutation/read, the eviction decision, restart
	// reconciliation, and the progress path, where a mismatched node must not touch another
	// recording's state.
	dvrAuthStrict dvrAuthMode = iota
	// dvrAuthIdempotentStop differs from strict in EXACTLY one case: a GENUINELY ABSENT row (no DVR at
	// all) is authorized success, so a duplicate stop/finalize against nothing is a safe no-op. An
	// EXISTING row — terminal or active — still requires the reporting node to match the persisted owner,
	// so a wrong-node stop against a terminal-with-retained-obligation row is rejected and cannot clear
	// the real owner's compensating-stop drain. Used ONLY by the stop/finalize path.
	dvrAuthIdempotentStop
)

// dvrOwnerTenant reads the tenant that owns a DVR artifact from its (globally unique) hash. This PK
// lookup is the partition-scoping bootstrap: the returned tenant then scopes the owner check and every
// segment-ledger op for the DVR.
//
// Three-valued so callers fail closed on a query failure instead of mistaking it for "absent":
//   - genuine absence (no DVR row, or an empty tenant) -> ("", false, nil)
//   - any DB/query error, or a nil DB where a lookup is required -> ("", false, err)
//   - the resolved owner -> (tenantID, true, nil)
//
// Every node-originated caller MUST abort fail-closed on err != nil (no claim, no lifecycle event, no
// segment or in-memory mutation). found=false keeps its per-operation meaning (idempotent-stop no-op vs
// strict reject).
func dvrOwnerTenant(ctx context.Context, dvrHash string) (string, bool, error) {
	if db == nil {
		return "", false, sql.ErrConnDone
	}
	tenantID, err := foghorndb.New(db).GetDVROwnerTenant(ctx, dvrHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if tenantID == "" {
		return "", false, nil
	}
	return tenantID, true, nil
}

// dvrReportNodeAuthorized reports whether an authenticated node-reported DVR op may act on this hash. It
// is the single owner check for every node-originated DVR path, scoped to the owning tenant: its lookup
// filters foghorn.artifacts by (hash, tenant_id), so a caller's claimed tenant that does not own the
// hash resolves to no row.
//
// The dispatch owner is dvr_start_dispatch.node_id (durable from StartDVR). For an ACTIVE recording with
// an EMPTY owner the owner is deterministically derived from the unique recording-origin artifact_nodes
// row and bound durably (see backfillDVRDispatchOwner); no unique origin ⇒ reject.
//
// Modes:
//   - dvrAuthStrict: authorize only when the persisted owner == reportingNode. A missing row, a
//     terminal row whose owner mismatches, or an unresolvable empty owner all reject.
//   - dvrAuthIdempotentStop: identical to strict for any EXISTING row (terminal or active still require
//     the owner match); differs ONLY for a genuinely absent row, which it treats as a safe no-op success.
//
// A query error returns (false, err) so the caller fails closed.
func dvrReportNodeAuthorized(ctx context.Context, dvrHash, tenantID, reportingNode string, mode dvrAuthMode) (bool, error) {
	if db == nil {
		return false, sql.ErrConnDone
	}
	row, err := foghorndb.New(db).GetDVRReportAuthorization(ctx, foghorndb.GetDVRReportAuthorizationParams{ArtifactHash: dvrHash, TenantID: tenantID})
	if errors.Is(err, sql.ErrNoRows) {
		// GENUINELY ABSENT row (no DVR at all): the ONLY place strict and idempotent-stop differ — a
		// duplicate stop against nothing is a safe no-op, a strict op has nothing to authorize and fails
		// closed. An EXISTING row (terminal or not) falls through to the owner check below.
		return mode == dvrAuthIdempotentStop, nil
	}
	if err != nil {
		return false, err
	}
	status, dispatchNode := row.Status.String, row.DispatchNode
	active := status == "requested" || status == "starting" || status == "recording" ||
		status == "stopping" || status == "finalizing"
	if dispatchNode == "" {
		if !active {
			// Terminal row with no persisted owner: nothing to authorize against, and a settled row's
			// dispatch descriptor must not be mutated. Fail closed for BOTH modes — a terminal row does not
			// grant a blanket stop no-op; only a genuinely absent row does (handled above).
			return false, nil
		}
		// Active recording with no persisted owner: derive it from the single recording-origin
		// artifact_nodes row and bind it durably. No unique origin ⇒ fail closed.
		origin, derr := backfillDVRDispatchOwner(ctx, dvrHash, tenantID)
		if derr != nil {
			return false, derr
		}
		if origin == "" {
			return false, nil
		}
		dispatchNode = origin
	}
	return dispatchNode == reportingNode, nil
}

// backfillDVRDispatchOwner derives an active DVR's recording origin from artifact_nodes and persists it
// into dvr_start_dispatch.node_id so subsequent node-reported ops are bound to it. It runs in ONE
// transaction, tenant-scoped throughout:
//
//  1. lock the tenant-owned artifact row (FOR UPDATE) so concurrent backfills serialize;
//  2. if an owner is already persisted (a concurrent caller won), authorize against that value;
//  3. otherwise resolve the UNIQUE role='origin', non-orphaned artifact_nodes copy — zero or more than
//     one candidate is ambiguous and returns "" (no error) so the caller fails closed;
//  4. compare-and-set the still-empty owner, then return the owner ACTUALLY persisted (re-read on a lost
//     CAS) so the caller always authorizes against the winner, never its own candidate.
//
// It returns "" (no error) when there is no tenant-owned row or no unique origin.
func backfillDVRDispatchOwner(ctx context.Context, dvrHash, tenantID string) (string, error) {
	if db == nil {
		return "", sql.ErrConnDone
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on the non-commit paths
	qtx := foghorndb.New(tx)

	currentOwner, err := qtx.LockDVRDispatchOwner(ctx, foghorndb.LockDVRDispatchOwnerParams{ArtifactHash: dvrHash, TenantID: tenantID})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// A concurrent caller may already have bound the owner; authorize against the persisted value.
	if currentOwner != "" {
		return currentOwner, tx.Commit()
	}

	// Resolve the UNIQUE recording-origin node copy for this DVR. artifact_nodes has no tenant column, so
	// the candidate is bound to the owning tenant through an EXISTS on the parent foghorn.artifacts row;
	// a hash belonging to another tenant matches no candidate. COUNT(*)=1 collapses zero-or-many to the
	// empty string so an ambiguous origin fails closed rather than binding an arbitrary node.
	origin, err := qtx.GetUniqueDVRRecordingOrigin(ctx, foghorndb.GetUniqueDVRRecordingOriginParams{ArtifactHash: dvrHash, TenantID: tenantID})
	if err != nil {
		return "", err
	}
	if origin == "" {
		return "", nil
	}

	// Compare-and-set the still-empty owner (tenant-scoped).
	affected, err := qtx.BindDVRDispatchOwner(ctx, foghorndb.BindDVRDispatchOwnerParams{ArtifactHash: dvrHash, TenantID: tenantID, DispatchNode: origin})
	if err != nil {
		return "", err
	}
	if affected == 1 {
		// We bound the owner; the persisted value is our candidate.
		return origin, tx.Commit()
	}
	// The CAS matched no row: another caller bound the owner between our read and write. Authorize
	// against the persisted winner, not our candidate.
	persisted, err := qtx.GetDVRDispatchOwner(ctx, foghorndb.GetDVRDispatchOwnerParams{ArtifactHash: dvrHash, TenantID: tenantID})
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return persisted, nil
}

func readArtifactStatus(ctx context.Context, dvrHash string) (string, error) {
	if db == nil {
		return "", sql.ErrConnDone
	}
	s, err := foghorndb.New(db).GetDVRArtifactStatus(ctx, dvrHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("dvr %s not found", dvrHash)
	}
	return s.String, err
}

// readArtifactTenant fetches the tenant_id for a finalized DVR artifact.
// Used by the FinalizeDVR retention back-fill into Commodore. Returns the
// empty string on miss; the back-fill skips when tenant cannot be resolved
// rather than blocking finalize.
func readArtifactTenant(ctx context.Context, dvrHash string) string {
	if db == nil {
		return ""
	}
	t, err := foghorndb.New(db).GetArtifactTenantText(ctx, dvrHash)
	if err != nil {
		return ""
	}
	return t
}

func retentionNullTime(value interface{}) sql.NullTime {
	retention, ok := value.(time.Time)
	return sql.NullTime{Time: retention, Valid: ok}
}
