package control

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

var chapterClosedNotifier func()

func SetChapterClosedNotifier(fn func()) {
	chapterClosedNotifier = fn
}

func notifyChapterClosed() {
	if chapterClosedNotifier != nil {
		chapterClosedNotifier()
	}
}

// Chapter rows record range metadata + the state machine that drives
// finalization (chapter → VOD artifact remux) and reclaim (delete
// source segments once the chapter artifact is durably frozen).
// dvr_chapter_generator.go records boundary openings/closes;
// jobs/chapter_finalization_queue.go drives closed → finalizing →
// finalized → frozen; jobs/chapter_reclaim_sweep.go drives frozen →
// reclaimed.

// Chapter modes match the CHECK constraint on dvr_chapters.mode.
const (
	ChapterModeWindowSized   = "window_sized_chapters"
	ChapterModeFixedInterval = "fixed_interval"
)

// Chapter state values match the CHECK constraint on dvr_chapters.state.
const (
	ChapterStateOpen                = "open"
	ChapterStateClosed              = "closed"
	ChapterStateFinalizing          = "finalizing"
	ChapterStateFinalized           = "finalized"
	ChapterStateFrozen              = "frozen"
	ChapterStateReclaimed           = "reclaimed"
	ChapterStateFailedSourceMissing = "failed_source_missing"
	ChapterStateFailedPermanent     = "failed_permanent"
)

// PlayableChapterStates are the states whose playback_artifact_hash is
// usable for playback. Reclaimed chapters still have the artifact;
// source segments are gone but the canonical .mkv lives on S3/warm.
func PlayableChapterStates() []string {
	return []string{ChapterStateFinalized, ChapterStateFrozen, ChapterStateReclaimed}
}

// DVRChapterRow is one row from foghorn.dvr_chapters.
type DVRChapterRow struct {
	ChapterID            string
	ArtifactHash         string
	Mode                 string
	IntervalSeconds      sql.NullInt32
	StartMs              int64
	EndMs                int64
	IsCurrent            bool
	State                string
	PlaybackArtifactHash sql.NullString
	PlaybackID           sql.NullString
	FinalizeAttempts     int32
	FinalizeStartedAt    sql.NullTime
	FrozenAt             sql.NullTime
	LastFailureReason    sql.NullString
	ReclaimStartedAt     sql.NullTime
	SegmentCount         int32
	HasGaps              bool
	// Actual MKV span; null until MarkChapterFinalized. May differ from
	// StartMs/EndMs when chapter boundaries don't align with segments.
	ActualMediaStartMs sql.NullInt64
	ActualMediaEndMs   sql.NullInt64
	CreatedAt          time.Time
}

func mapDVRChapter(row foghorndb.FoghornDvrChapter) DVRChapterRow {
	return DVRChapterRow{
		ChapterID: row.ChapterID, ArtifactHash: row.ArtifactHash, Mode: row.Mode,
		IntervalSeconds: row.IntervalSeconds, StartMs: row.StartMs, EndMs: row.EndMs,
		IsCurrent: row.IsCurrent, State: row.State, PlaybackArtifactHash: row.PlaybackArtifactHash,
		PlaybackID: row.PlaybackID, FinalizeAttempts: row.FinalizeAttempts,
		FinalizeStartedAt: row.FinalizeStartedAt, FrozenAt: row.FrozenAt,
		LastFailureReason: row.LastFailureReason, ReclaimStartedAt: row.ReclaimStartedAt,
		SegmentCount: row.SegmentCount, HasGaps: row.HasGaps,
		ActualMediaStartMs: row.ActualMediaStartMs, ActualMediaEndMs: row.ActualMediaEndMs,
		CreatedAt: row.CreatedAt,
	}
}

// SetChapterPlaybackID caches the Commodore-minted public playback_id
// on the chapter row. Idempotent. The cache is non-authoritative — the
// chapter playback resolver always falls back to
// commodore.dvr_chapter_playback if the cache is empty or stale.
func SetChapterPlaybackID(ctx context.Context, chapterID, playbackID string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	if chapterID == "" || playbackID == "" {
		return nil
	}
	return foghorndb.New(db).SetDVRChapterPlaybackID(ctx, foghorndb.SetDVRChapterPlaybackIDParams{
		ChapterID: chapterID, PlaybackID: sql.NullString{String: playbackID, Valid: true},
	})
}

// BuildChapterID is the canonical chapter identity. Stable: same inputs
// always produce the same ID. Mode/policy changes that yield different
// (start_ms, end_ms) boundaries produce different IDs.
//
// stream_id is intentionally NOT in the hash — dvr_artifact_id already
// namespaces uniquely, and including stream_id would destabilize the ID
// across the artifact's stream_internal_name rename edge case.
func BuildChapterID(dvrArtifactID, mode string, intervalSeconds int32, startMs, endMs int64) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%d|%d|%d", dvrArtifactID, mode, intervalSeconds, startMs, endMs)
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:32]
}

// OpenChapter records a new open chapter row at boundary rotation.
// Idempotent on chapter_id: re-recording the same chapter is a no-op.
// Clears is_current on any previously-current chapter for the same
// artifact in the same transaction.
func OpenChapter(ctx context.Context, c DVRChapterRow) error {
	if db == nil {
		return sql.ErrConnDone
	}
	var notify bool
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		q := foghorndb.New(tx)
		closedPrevious, txErr := q.ClosePreviousCurrentDVRChapters(ctx, foghorndb.ClosePreviousCurrentDVRChaptersParams{
			ArtifactHash: c.ArtifactHash, ChapterID: c.ChapterID,
		})
		if txErr != nil {
			return fmt.Errorf("close previous current chapter: %w", txErr)
		}

		state := c.State
		if state == "" {
			state = ChapterStateOpen
		}

		err := q.UpsertOpenDVRChapter(ctx, foghorndb.UpsertOpenDVRChapterParams{
			ChapterID: c.ChapterID, ArtifactHash: c.ArtifactHash, Mode: c.Mode,
			IntervalSeconds: c.IntervalSeconds, StartMs: c.StartMs, EndMs: c.EndMs, State: state,
		})
		if err != nil {
			return fmt.Errorf("open chapter: %w", err)
		}
		notify = state == ChapterStateClosed || closedPrevious > 0
		return nil
	})
	if err != nil {
		return err
	}
	if notify {
		notifyChapterClosed()
	}
	return nil
}

// CloseChapter flips a single chapter from is_current=true,state='open'
// to is_current=false,state='closed'. The finalization queue picks it
// up on its next sweep. No-op if the chapter is already closed or has
// progressed further.
func CloseChapter(ctx context.Context, chapterID string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	rows, err := foghorndb.New(db).CloseDVRChapter(ctx, chapterID)
	if err != nil {
		return fmt.Errorf("close chapter: %w", err)
	}
	if rows > 0 {
		notifyChapterClosed()
	}
	return nil
}

// CloseCurrentChapterForArtifact flips any current chapter of the
// artifact to closed. Used at DVR finalize so the terminal chapter
// enters the finalization queue.
func CloseCurrentChapterForArtifact(ctx context.Context, artifactHash string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	rows, err := foghorndb.New(db).CloseCurrentDVRChapterForArtifact(ctx, artifactHash)
	if err != nil {
		return fmt.Errorf("close current chapter for artifact: %w", err)
	}
	if rows > 0 {
		notifyChapterClosed()
	}
	return nil
}

// MarkChapterFinalizing transitions closed → finalizing OR refreshes
// a stale finalizing row (one whose dispatch deadline has lapsed
// without a PUSH_END result). Increments finalize_attempts and stamps
// finalize_started_at so the next stale-finalizing scan re-targets the
// row only if Helmsman drops the result again.
//
// Returning false means the row is already terminal or someone else
// just claimed it — caller should skip.
//
// The unique partial index on foghorn.artifacts(origin_id) WHERE
// origin_type='dvr_chapter' enforces that retries reuse the same
// playback artifact row.
func MarkChapterFinalizing(ctx context.Context, chapterID, playbackHash, tenantID, finalizeNodeID string, staleTimeout time.Duration) (ok bool, err error) {
	if db == nil {
		return false, sql.ErrConnDone
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("mark chapter finalizing: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
		}
	}()
	// finalize_node_id is persisted here — before the job is sent to the node — so the result/progress handlers
	// can bind the reporting connection to the assignment (chapter-finalize jobs have no processing_jobs row).
	n, err := foghorndb.New(tx).ClaimDVRChapterFinalization(ctx, foghorndb.ClaimDVRChapterFinalizationParams{
		ChapterID:            chapterID,
		PlaybackArtifactHash: sql.NullString{String: playbackHash, Valid: playbackHash != ""},
		StaleSeconds:         staleTimeout.Seconds(), FinalizeNodeID: finalizeNodeID,
	})
	if err != nil {
		return false, fmt.Errorf("mark chapter finalizing: %w", err)
	}
	if n == 0 {
		return false, nil // not claimable (already advanced / another worker); no lifecycle emitted
	}
	// Enqueue the PROCESSING lifecycle in the SAME transaction so the state transition and its
	// analytics event commit atomically (never lost between a separate commit and a fire-and-forget
	// enqueue).
	startedAt := time.Now().Unix()
	vodData := &ipcpb.VodLifecycleData{
		Status:    ipcpb.VodLifecycleData_STATUS_PROCESSING,
		VodHash:   playbackHash,
		StartedAt: &startedAt,
	}
	if tenantID != "" {
		vodData.TenantId = &tenantID
	}
	if enqErr := artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, vodData); enqErr != nil {
		return false, fmt.Errorf("enqueue chapter processing lifecycle: %w", enqErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return false, fmt.Errorf("mark chapter finalizing: commit: %w", commitErr)
	}
	committed = true
	return true, nil
}

// MarkChapterFinalized transitions finalizing → finalized after the
// processing job's PUSH_END handler validates output. segment_count
// and has_gaps come from the actual artifact contents; mediaStartMs /
// mediaEndMs come from the first/last owned segments and pin the MKV
// timeline to wall-clock without drift even when chapter boundaries
// don't align to segment boundaries. Pass 0 for media bounds when
// unknown (column stays NULL).
// MarkChapterFinalizedTx runs the finalizing → finalized transition on the caller's
// transaction and returns the number of rows transitioned. The chapter-finalize handler
// requires this to be exactly 1 — a 0 means the row was NOT in 'finalizing' (a duplicate
// completion, a concurrent worker, or a rolled-back retry), so the whole atomic finalize
// must roll back rather than persist readiness/origin against an unowned transition.
func MarkChapterFinalizedTx(ctx context.Context, tx *sql.Tx, chapterID string, segmentCount int32, hasGaps bool, mediaStartMs, mediaEndMs int64) (int64, error) {
	mediaStartArg, mediaEndArg := chapterMediaBoundNulls(mediaStartMs, mediaEndMs)
	rows, err := foghorndb.New(tx).MarkDVRChapterFinalized(ctx, foghorndb.MarkDVRChapterFinalizedParams{
		ChapterID: chapterID, SegmentCount: segmentCount, HasGaps: hasGaps,
		MediaStartMs: mediaStartArg, MediaEndMs: mediaEndArg,
	})
	if err != nil {
		return 0, fmt.Errorf("mark chapter finalized: %w", err)
	}
	return rows, nil
}

func chapterMediaBoundNulls(mediaStartMs, mediaEndMs int64) (start, end sql.NullInt64) {
	if mediaStartMs > 0 {
		start = sql.NullInt64{Int64: mediaStartMs, Valid: true}
	}
	if mediaEndMs > mediaStartMs {
		end = sql.NullInt64{Int64: mediaEndMs, Valid: true}
	}
	return start, end
}

// MarkChapterFrozen transitions finalized → frozen once the playback
// artifact is sync_status='synced' AND dtsh_synced=true. The reclaim
// sweep can now delete source segments + temporary S3 segment objects.
func MarkChapterFrozen(ctx context.Context, chapterID string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort
	if err := MarkChapterFrozenTx(ctx, tx, chapterID); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkChapterFrozenTx is the transactional form so a chapter can be frozen atomically with the
// sync-completion that promoted its playback artifact to synced+dtsh.
func MarkChapterFrozenTx(ctx context.Context, tx *sql.Tx, chapterID string) error {
	err := foghorndb.New(tx).MarkDVRChapterFrozen(ctx, chapterID)
	if err != nil {
		return fmt.Errorf("mark chapter frozen: %w", err)
	}
	return nil
}

// MarkChapterReclaimStarted gates the reclaim sweep so concurrent
// workers don't issue duplicate Helmsman ReclaimDVRSegment orders.
// Returns false if reclaim_started_at is recent (within freshness
// window) — caller should skip this chapter.
func MarkChapterReclaimStarted(ctx context.Context, chapterID string, freshness time.Duration) (ok bool, err error) {
	if db == nil {
		return false, sql.ErrConnDone
	}
	n, err := foghorndb.New(db).MarkDVRChapterReclaimStarted(ctx, foghorndb.MarkDVRChapterReclaimStartedParams{
		ChapterID: chapterID, Secs: freshness.Seconds(),
	})
	if err != nil {
		return false, fmt.Errorf("mark chapter reclaim started: %w", err)
	}
	return n > 0, nil
}

// MarkChapterReclaimed transitions frozen → reclaimed after all source
// segments have been deleted locally and from the temporary S3 freeze.
// The row remains as range metadata; playback uses
// playback_artifact_hash.
func MarkChapterReclaimed(ctx context.Context, chapterID string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	err := foghorndb.New(db).MarkDVRChapterReclaimed(ctx, chapterID)
	if err != nil {
		return fmt.Errorf("mark chapter reclaimed: %w", err)
	}
	return nil
}

// MarkChapterFailed sets a terminal failure state plus a human-readable
// reason. Used when recovery from source-missing is exhausted, or when
// the input ledger is unrecoverable.
// MarkChapterFailed drives a chapter to a terminal failed state and fails its allocated playback artifact in
// one transaction. expectedNode binds the transition to the reporting connection when non-empty (same
// row-locked finalize_node_id predicate as RetryChapterFinalize — a node-reported failure may only affect its
// own assignment); empty is passed ONLY by trusted internal recovery (the finalization queue), never from a
// node report.
func MarkChapterFailed(ctx context.Context, chapterID, terminalState, reason, expectedNode string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	switch terminalState {
	case ChapterStateFailedSourceMissing, ChapterStateFailedPermanent:
	default:
		return fmt.Errorf("invalid terminal state %q", terminalState)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mark chapter failed: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
		}
	}()
	// Transition the chapter and recover its allocated playback artifact hash in one shot.
	q := foghorndb.New(tx)
	// A NODE-reported terminal failure (expectedNode != '') may ONLY act on the attempt currently dispatched to
	// that node — state MUST still be 'finalizing' with a matching finalize_node_id. A retry that already
	// bounced the chapter to 'closed' cleared finalize_node_id, so a delayed report from the retired node
	// matches nothing. Only INTERNAL recovery (expectedNode == '') may terminalize a 'closed' row (e.g. max
	// attempts exceeded before redispatch).
	playbackHash, scanErr := q.FailDVRChapter(ctx, foghorndb.FailDVRChapterParams{
		ChapterID: chapterID, State: terminalState,
		LastFailureReason: sql.NullString{String: reason, Valid: true}, ExpectedNode: expectedNode,
	})
	if errors.Is(scanErr, sql.ErrNoRows) {
		// Chapter wasn't in a failable state (already terminal/frozen) — nothing to do.
		return nil
	}
	if scanErr != nil {
		return fmt.Errorf("mark chapter failed: %w", scanErr)
	}
	// A terminally-failed chapter must not leave its allocated playback artifact stuck
	// 'finalizing' (it would show "processing" forever and never be reclaimed). Fail it in the
	// same transaction; the guard leaves an already-terminal/deleted/ready artifact untouched.
	if playbackHash.Valid && playbackHash.String != "" {
		artTenant, artErr := q.FailDVRChapterArtifact(ctx, foghorndb.FailDVRChapterArtifactParams{
			ArtifactHash: playbackHash.String, ErrorMessage: sql.NullString{String: reason, Valid: true},
		})
		if artErr != nil && !errors.Is(artErr, sql.ErrNoRows) {
			return fmt.Errorf("mark chapter artifact failed: %w", artErr)
		}
		// The artifact was newly failed → enqueue its failed lifecycle in the SAME transaction, so
		// the state change and its analytics event commit atomically (never lost to a crash between
		// a separate commit and a fire-and-forget enqueue).
		if artErr == nil {
			errMsg := reason
			vodData := &ipcpb.VodLifecycleData{
				Status:  ipcpb.VodLifecycleData_STATUS_FAILED,
				VodHash: playbackHash.String,
				Error:   &errMsg,
			}
			if artTenant != "" {
				t := artTenant
				vodData.TenantId = &t
			}
			if enqErr := artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, vodData); enqErr != nil {
				return fmt.Errorf("enqueue chapter failure lifecycle: %w", enqErr)
			}
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return fmt.Errorf("mark chapter failed: commit: %w", commitErr)
	}
	committed = true
	return nil
}

// RetryChapterFinalize rolls finalizing → closed after a transient
// failure so the queue picks the row up again on its next sweep.
// last_failure_reason carries the transient cause for operator
// visibility.
// RetryChapterFinalize bounces a 'finalizing' chapter back to 'closed' so the queue re-dispatches it.
// expectedNode binds the transition to the reporting connection when non-empty (a node-reported failure/bounce
// may only affect the attempt currently dispatched to that node — the guarded UPDATE reads finalize_node_id
// under the row lock, closing the reassignment TOCTOU); empty is passed ONLY by trusted internal recovery
// (the finalization queue), never derived from a node report, so it acts authoritatively.
func RetryChapterFinalize(ctx context.Context, chapterID, reason, expectedNode string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	// Clear finalize_node_id when leaving 'finalizing': the attempt is retired, so the old node's assignment
	// must not authorize any later transition (a delayed report from it must not terminalize the re-queued
	// chapter before redispatch reassigns the node).
	err := foghorndb.New(db).RetryDVRChapterFinalize(ctx, foghorndb.RetryDVRChapterFinalizeParams{
		ChapterID: chapterID, LastFailureReason: sql.NullString{String: reason, Valid: true}, ExpectedNode: expectedNode,
	})
	if err != nil {
		return fmt.Errorf("retry chapter finalize: %w", err)
	}
	return nil
}

// ListChaptersNeedingFinalization returns chapters in 'closed' state (or stuck in
// 'finalizing' past THEIR OWN dispatch deadline). The stuck-finalizing cutoff is the same
// per-chapter deadline Helmsman was given — max(2*chapter_duration, minTimeout) capped at
// maxTimeout — computed in SQL per row, so a chapter whose job deadline was 30m is recovered
// after ~30m instead of being parked for the flat 24h cap. Backed by
// idx_foghorn_dvr_chapters_pending.
func ListChaptersNeedingFinalization(ctx context.Context, limit int, minTimeout, maxTimeout time.Duration) ([]DVRChapterRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := foghorndb.New(db).ListDVRChaptersNeedingFinalization(ctx, foghorndb.ListDVRChaptersNeedingFinalizationParams{
		Secs: minTimeout.Seconds(), Secs_2: maxTimeout.Seconds(), Limit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list chapters needing finalization: %w", err)
	}
	out := make([]DVRChapterRow, len(rows))
	for i := range rows {
		out[i] = mapDVRChapter(rows[i].FoghornDvrChapter)
	}
	return out, nil
}

// ListChaptersNeedingReclaim returns frozen chapters whose source
// segments haven't been reclaimed yet. Backed by
// idx_foghorn_dvr_chapters_reclaim. Caller MUST call
// MarkChapterReclaimStarted before issuing reclaim orders to prevent
// duplicate work.
func ListChaptersNeedingReclaim(ctx context.Context, limit int, freshness time.Duration) ([]DVRChapterRow, error) {
	var out []DVRChapterRow
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		rows, err := listChaptersNeedingReclaimOnce(ctx, limit, freshness)
		if err != nil {
			return err
		}
		out = rows
		return nil
	})
	return out, err
}

func listChaptersNeedingReclaimOnce(ctx context.Context, limit int, freshness time.Duration) ([]DVRChapterRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := foghorndb.New(db).ListDVRChaptersNeedingReclaim(ctx, foghorndb.ListDVRChaptersNeedingReclaimParams{
		Secs: freshness.Seconds(), Limit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list chapters needing reclaim: %w", err)
	}
	out := make([]DVRChapterRow, len(rows))
	for i := range rows {
		out[i] = mapDVRChapter(rows[i].FoghornDvrChapter)
	}
	return out, nil
}

// GetChapter returns the chapter row by ID, or sql.ErrNoRows.
func GetChapter(ctx context.Context, chapterID string) (*DVRChapterRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	row, err := foghorndb.New(db).GetDVRChapter(ctx, chapterID)
	if err != nil {
		return nil, err
	}
	mapped := mapDVRChapter(row.FoghornDvrChapter)
	return &mapped, nil
}

func getChaptersByID(ctx context.Context, chapterIDs []string) (map[string]DVRChapterRow, error) {
	out := make(map[string]DVRChapterRow, len(chapterIDs))
	if len(chapterIDs) == 0 {
		return out, nil
	}
	if db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := foghorndb.New(db).GetDVRChaptersByID(ctx, chapterIDs)
	if err != nil {
		return nil, fmt.Errorf("get chapters by id: %w", err)
	}
	for _, row := range rows {
		c := mapDVRChapter(row.FoghornDvrChapter)
		out[c.ChapterID] = c
	}
	return out, nil
}

// CurrentChapter returns the in-flight chapter for an artifact, if any.
func CurrentChapter(ctx context.Context, artifactHash string) (*DVRChapterRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	row, err := foghorndb.New(db).GetCurrentDVRChapter(ctx, artifactHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	c := mapDVRChapter(row.FoghornDvrChapter)
	return &c, nil
}

func LatestChapterBefore(ctx context.Context, artifactHash, mode string, intervalSeconds int32, beforeStartMs int64) (*DVRChapterRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	row, err := foghorndb.New(db).GetLatestDVRChapterBefore(ctx, foghorndb.GetLatestDVRChapterBeforeParams{
		ArtifactHash: artifactHash, Mode: mode,
		IntervalSeconds: sql.NullInt32{Int32: intervalSeconds, Valid: true}, StartMs: beforeStartMs,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	c := mapDVRChapter(row.FoghornDvrChapter)
	return &c, nil
}

func DeleteChapter(ctx context.Context, chapterID string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	err := foghorndb.New(db).DeleteDVRChapter(ctx, chapterID)
	if err != nil {
		return fmt.Errorf("delete chapter: %w", err)
	}
	return nil
}

// PropagateChapterRetention copies a parent DVR's retention horizon onto every child chapter
// playback artifact, so a chapter's lifetime always tracks the parent. `until` is a time.Time
// for a concrete horizon or nil for keep-forever (NULL). Child chapters are the vod artifacts
// linked through their dvr_chapters row's playback_artifact_hash. Returns the number of chapter
// artifacts updated. Deleted rows are left alone (their retention is moot).
func PropagateChapterRetention(ctx context.Context, dvrHash string, until interface{}) (int64, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	return propagateChapterRetention(ctx, db, dvrHash, until)
}

// PropagateChapterRetentionTx runs the same propagation on the caller's transaction so a
// parent-retention change and its child fan-out commit atomically.
func PropagateChapterRetentionTx(ctx context.Context, tx *sql.Tx, dvrHash string, until interface{}) (int64, error) {
	return propagateChapterRetention(ctx, tx, dvrHash, until)
}

func propagateChapterRetention(ctx context.Context, dbtx foghorndb.DBTX, dvrHash string, until interface{}) (int64, error) {
	rows, err := foghorndb.New(dbtx).PropagateDVRChapterRetention(ctx, foghorndb.PropagateDVRChapterRetentionParams{
		ArtifactHash: dvrHash, RetentionUntil: retentionNullTime(until),
	})
	if err != nil {
		return 0, fmt.Errorf("propagate chapter retention: %w", err)
	}
	return rows, nil
}

// SoftDeleteDVRAndChapters commits the DURABLE deletion of a DVR and its chapters as ONE
// transaction, BEFORE any physical byte cleanup: it soft-deletes the child chapter playback
// artifacts (returning their hashes so the caller can drive cleanup), removes the chapter rows
// so the finalization queue can never re-dispatch a chapter of a deleted parent, and soft-deletes
// the parent DVR artifact. Because parent+children are marked deleted here, the standard
// orphan/purge flow can discover and reclaim ALL their bytes — a byte-first delete could strand
// an active catalog row whose bytes are already gone, or leave active children the purge job
// would never find. Every write is tenant-scoped (partition/authorization scoping so a write never
// touches another tenant's row; artifact_hash is a randomly-minted, globally-unique id). Returns the
// child hashes transitioned and whether THIS call performed
// the parent soft-delete (parentTransitioned) so the caller can suppress a duplicate deletion
// event on a concurrent/repeat delete.
func SoftDeleteDVRAndChapters(ctx context.Context, dvrHash, tenantID string) ([]string, bool, error) {
	if db == nil {
		return nil, false, sql.ErrConnDone
	}
	if tenantID == "" {
		return nil, false, fmt.Errorf("delete dvr: tenant_id required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("delete dvr: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
		}
	}()

	// Soft-delete the parent DVR — GUARDED on status <> 'deleted' AND tenant_id so it is
	// transition-idempotent and tenant-scoped: a concurrent/retry delete of an already-deleted DVR
	// (or a same-hash artifact owned by another tenant) affects 0 rows and does NOT re-enqueue the
	// DVR-deleted lifecycle event. RowsAffected confirms whether this call performed the transition.
	q := foghorndb.New(tx)
	affected, execErr := q.SoftDeleteDVRParent(ctx, foghorndb.SoftDeleteDVRParentParams{
		ArtifactHash: dvrHash, TenantID: tenantID,
	})
	if execErr != nil {
		return nil, false, fmt.Errorf("delete dvr: soft-delete parent: %w", execErr)
	}
	parentTransitioned := affected > 0

	// Cascade children + chapter rows + per-child VOD-deleted events ALWAYS (idempotent —
	// already-deleted children match nothing), so a re-delete of an already-deleted parent whose
	// children were never cascaded still repairs them. Emit the DVR-deleted event ONLY on a real transition.
	childHashes, cascadeErr := CascadeDVRChildrenTx(ctx, tx, dvrHash, tenantID)
	if cascadeErr != nil {
		return nil, false, cascadeErr
	}
	if parentTransitioned {
		dvrData := &ipcpb.DVRLifecycleData{Status: ipcpb.DVRLifecycleData_STATUS_DELETED, DvrHash: dvrHash, TenantId: &tenantID}
		if enqErr := artifactoutbox.EnqueueDVRLifecycleTx(ctx, tx, dvrData); enqErr != nil {
			return nil, false, fmt.Errorf("delete dvr: enqueue dvr lifecycle: %w", enqErr)
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, false, fmt.Errorf("delete dvr: commit: %w", commitErr)
	}
	committed = true
	return childHashes, parentTransitioned, nil
}

// RepairDeletedDVRChildrenBatch enforces the invariant: a deleted DVR parent has no residual
// chapter ledger rows. It selects up to `limit` deleted DVR parents that still have ANY
// foghorn.dvr_chapters row and cascades each in its OWN tenant-scoped transaction (soft-delete any
// live children + remove the chapter rows + enqueue per-child VOD-deleted events). The catalog
// reconciler then projects each child deletion, so Commodore removes the stale chapter vod_assets +
// dvr_chapter_playback rows keyed by the child's own hash. Selecting on the presence of chapter
// rows (not a live joined child) means the pass also converges over rows with a NULL playback hash
// or an already-deleted child — the idempotent cascade removes the ledger row regardless. Bounded;
// a parent drops out of the candidate set once its chapter rows are removed. Returns parents repaired.
func RepairDeletedDVRChildrenBatch(ctx context.Context, limit int) (int, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	rows, err := foghorndb.New(db).ListDeletedDVRParentsWithChapters(ctx, int32(limit))
	if err != nil {
		return 0, fmt.Errorf("repair deleted dvr children: scan candidates: %w", err)
	}
	type candidate struct{ hash, tenant string }
	var candidates []candidate
	for _, row := range rows {
		candidates = append(candidates, candidate{hash: row.ArtifactHash, tenant: row.TenantID})
	}

	repaired := 0
	for _, cnd := range candidates {
		if cnd.tenant == "" {
			continue // can't tenant-scope the cascade; skip rather than run an unscoped delete
		}
		tx, txErr := db.BeginTx(ctx, nil)
		if txErr != nil {
			return repaired, fmt.Errorf("repair deleted dvr children: begin: %w", txErr)
		}
		if _, cascadeErr := CascadeDVRChildrenTx(ctx, tx, cnd.hash, cnd.tenant); cascadeErr != nil {
			tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
			return repaired, cascadeErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return repaired, fmt.Errorf("repair deleted dvr children: commit: %w", commitErr)
		}
		repaired++
	}
	return repaired, nil
}

// CascadeDVRChildrenTx soft-deletes a DVR's child chapter playback artifacts, removes the chapter
// rows (so the finalization/reclaim queues never touch a chapter of a deleted parent), and
// enqueues a VOD-deleted lifecycle per child — ALL on the caller's transaction. It does NOT touch
// the parent DVR row or emit the DVR-deleted event; the caller owns those. Returns the child
// hashes transitioned. Shared by explicit delete and retention expiry so both cascade identically
// and atomically.
func CascadeDVRChildrenTx(ctx context.Context, tx *sql.Tx, dvrHash, tenant string) ([]string, error) {
	if tenant == "" {
		return nil, fmt.Errorf("delete dvr: cascade child chapters: tenant required")
	}
	// Child chapter artifacts → deleted (RETURNING their hashes) BEFORE removing the chapter rows
	// the join relies on. Tenant-scoped so the delete never reaches another tenant's chapter (partition/
	// authorization scoping; artifact_hash is a randomly-minted, globally-unique id).
	q := foghorndb.New(tx)
	childHashes, err := q.SoftDeleteDVRChapterArtifacts(ctx, foghorndb.SoftDeleteDVRChapterArtifactsParams{
		ArtifactHash: dvrHash, TenantID: tenant,
	})
	if err != nil {
		return nil, fmt.Errorf("delete dvr: cascade child chapters: %w", err)
	}
	// Tenant-scoped via the parent artifact (dvr_chapters has no tenant column): join foghorn.artifacts
	// so the ledger delete can't reach a same-hash parent owned by another tenant.
	if err := q.DeleteDVRChapterRowsForTenant(ctx, foghorndb.DeleteDVRChapterRowsForTenantParams{
		ArtifactHash: dvrHash, TenantID: tenant,
	}); err != nil {
		return nil, fmt.Errorf("delete dvr: remove chapter rows: %w", err)
	}

	for _, childHash := range childHashes {
		vodData := &ipcpb.VodLifecycleData{Status: ipcpb.VodLifecycleData_STATUS_DELETED, VodHash: childHash}
		if tenant != "" {
			vodData.TenantId = &tenant
		}
		if enqErr := artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, vodData); enqErr != nil {
			return nil, fmt.Errorf("delete dvr: enqueue chapter lifecycle: %w", enqErr)
		}
	}
	return childHashes, nil
}

func DVRArtifactStillRecording(ctx context.Context, artifactHash string) bool {
	if db == nil {
		return false
	}
	st, err := foghorndb.New(db).GetDVRParentArtifactStatus(ctx, artifactHash)
	if err != nil || !st.Valid {
		return false
	}
	return st.String == "starting" || st.String == "recording"
}

// ClearCurrentChaptersForInactiveDVRs closes any chapter still marked
// is_current=true for a DVR artifact that is no longer recording. Used
// by the chapter sweeper to catch missed close transitions (e.g. DVR
// finalize crashed before CloseCurrentChapterForArtifact ran).
func ClearCurrentChaptersForInactiveDVRs(ctx context.Context) (int64, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	rows, err := foghorndb.New(db).ClearCurrentChaptersForInactiveDVRs(ctx)
	if err != nil {
		return 0, err
	}
	return rows, nil
}

func WithDVRChapterMutationLock(ctx context.Context, artifactHash string, fn func() error) error {
	if db == nil {
		return sql.ErrConnDone
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	q := foghorndb.New(conn)
	if err := q.LockDVRChapterMutation(ctx, artifactHash); err != nil {
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if unlockErr := q.UnlockDVRChapterMutation(unlockCtx, artifactHash); unlockErr != nil {
			return
		}
	}()
	return fn()
}

// ListChaptersForArtifact returns chapters for a player UI page.
// Caller MUST pass a non-zero limit; the bounded-operations invariant
// requires every API page to be capped (default 200 in the public
// surface).
func ListChaptersForArtifact(
	ctx context.Context,
	artifactHash string,
	mode string,
	intervalSeconds int32,
	startMs, endMs int64,
	limit int,
	pageToken string,
) ([]DVRChapterRow, string, error) {
	if db == nil {
		return nil, "", sql.ErrConnDone
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var cursor int64
	var cursorID string
	if pageToken != "" {
		if startPart, idPart, ok := strings.Cut(pageToken, "|"); ok {
			if _, err := fmt.Sscanf(startPart, "%d", &cursor); err == nil {
				cursorID = idPart
			}
		} else {
			var parsed int64
			if _, err := fmt.Sscanf(pageToken, "%d", &parsed); err == nil {
				cursor = parsed
			}
		}
	}
	rows, err := foghorndb.New(db).ListDVRChaptersForArtifact(ctx, foghorndb.ListDVRChaptersForArtifactParams{
		ArtifactHash: artifactHash, StartMs: startMs, EndMs: endMs, Mode: mode,
		CursorStartMs: cursor, CursorChapterID: cursorID, PageLimit: int32(limit + 1),
		IntervalSeconds: sql.NullInt32{Int32: intervalSeconds, Valid: intervalSeconds > 0},
	})
	if err != nil {
		return nil, "", fmt.Errorf("list chapters: %w", err)
	}
	out := make([]DVRChapterRow, len(rows))
	for i := range rows {
		out[i] = mapDVRChapter(rows[i].FoghornDvrChapter)
	}
	var nextToken string
	if len(out) > limit {
		nextToken = fmt.Sprintf("%d|%s", out[limit-1].StartMs, out[limit-1].ChapterID)
		out = out[:limit]
	}
	return out, nextToken, nil
}

func ListVirtualChaptersForArtifact(
	ctx context.Context,
	artifactHash string,
	mode string,
	intervalSeconds int32,
	rangeStartMs, rangeEndMs int64,
	limit int,
	pageToken string,
) ([]DVRChapterRow, string, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	policy, ok, err := ReadDVRChapterPolicy(ctx, artifactHash)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", nil
	}
	if mode == "" {
		mode = policy.Mode
	}
	if intervalSeconds <= 0 {
		intervalSeconds = EffectiveChapterInterval(mode, intervalSeconds, policy.WindowSeconds)
	}
	if mode != policy.Mode && mode != ChapterModeFixedInterval {
		return nil, "", nil
	}
	if intervalSeconds <= 0 {
		return nil, "", nil
	}
	startBound := rangeStartMs
	if startBound <= 0 || startBound < policy.StartedAtMs {
		startBound = policy.StartedAtMs
	}
	endBound := rangeEndMs
	if endBound <= 0 {
		endBound = time.Now().UnixMilli()
	}
	if policy.EndedAtMs > 0 && endBound > policy.EndedAtMs {
		endBound = policy.EndedAtMs
	}
	if endBound <= startBound {
		return nil, "", nil
	}
	tailPage := rangeStartMs <= 0 && rangeEndMs <= 0
	if tailPage && (pageToken == "" || strings.HasPrefix(pageToken, "vprev|")) {
		endCursor := endBound
		if strings.HasPrefix(pageToken, "vprev|") {
			if _, scanErr := fmt.Sscanf(strings.TrimPrefix(pageToken, "vprev|"), "%d", &endCursor); scanErr != nil {
				endCursor = endBound
			}
			if endCursor > endBound {
				endCursor = endBound
			}
		}
		if endCursor <= startBound {
			return nil, "", nil
		}
		nowMs := time.Now().UnixMilli()
		recording := DVRArtifactStillRecording(ctx, artifactHash)
		rows := make([]DVRChapterRow, 0, limit+1)
		cursorEnd := endCursor
		for len(rows) <= limit && cursorEnd > startBound {
			atMs := cursorEnd - 1
			if atMs < startBound {
				atMs = startBound
			}
			startMs, scheduledEndMs, ok := CurrentChapterBounds(mode, intervalSeconds, policy.StartedAtMs, atMs)
			if !ok || scheduledEndMs <= startMs {
				break
			}
			chapterEndMs := scheduledEndMs
			if policy.EndedAtMs > 0 && chapterEndMs > policy.EndedAtMs {
				chapterEndMs = policy.EndedAtMs
			}
			if startMs < startBound {
				startMs = startBound
			}
			chapterID := BuildChapterID(artifactHash, mode, intervalSeconds, startMs, chapterEndMs)
			row := DVRChapterRow{
				ChapterID:       chapterID,
				ArtifactHash:    artifactHash,
				Mode:            mode,
				IntervalSeconds: sql.NullInt32{Int32: intervalSeconds, Valid: intervalSeconds > 0},
				StartMs:         startMs,
				EndMs:           chapterEndMs,
				IsCurrent:       recording && startMs <= nowMs && nowMs < chapterEndMs,
				State:           ChapterStateOpen,
			}
			rows = append([]DVRChapterRow{row}, rows...)
			cursorEnd = startMs
		}
		var overlayErr error
		rows, overlayErr = overlayMaterializedChapters(ctx, rows)
		if overlayErr != nil {
			return nil, "", overlayErr
		}
		var nextToken string
		if len(rows) > limit {
			nextToken = fmt.Sprintf("vprev|%d", rows[1].StartMs)
			rows = rows[1:]
		} else if len(rows) > 0 && rows[0].StartMs > startBound {
			nextToken = fmt.Sprintf("vprev|%d", rows[0].StartMs)
		}
		return rows, nextToken, nil
	}
	cursor := startBound
	if strings.HasPrefix(pageToken, "v|") {
		if _, scanErr := fmt.Sscanf(strings.TrimPrefix(pageToken, "v|"), "%d", &cursor); scanErr != nil {
			cursor = startBound
		}
	}

	rows := make([]DVRChapterRow, 0, limit+1)
	nowMs := time.Now().UnixMilli()
	recording := DVRArtifactStillRecording(ctx, artifactHash)
	for len(rows) <= limit && cursor < endBound {
		startMs, scheduledEndMs, ok := CurrentChapterBounds(mode, intervalSeconds, policy.StartedAtMs, cursor)
		if !ok || scheduledEndMs <= cursor {
			break
		}
		chapterEndMs := scheduledEndMs
		if policy.EndedAtMs > 0 && chapterEndMs > policy.EndedAtMs {
			chapterEndMs = policy.EndedAtMs
		}
		if scheduledEndMs > startBound && startMs < endBound {
			chapterID := BuildChapterID(artifactHash, mode, intervalSeconds, startMs, chapterEndMs)
			row := DVRChapterRow{
				ChapterID:       chapterID,
				ArtifactHash:    artifactHash,
				Mode:            mode,
				IntervalSeconds: sql.NullInt32{Int32: intervalSeconds, Valid: intervalSeconds > 0},
				StartMs:         startMs,
				EndMs:           chapterEndMs,
				IsCurrent:       recording && startMs <= nowMs && nowMs < chapterEndMs,
				State:           ChapterStateOpen,
			}
			rows = append(rows, row)
		}
		cursor = scheduledEndMs
	}
	var overlayErr error
	rows, overlayErr = overlayMaterializedChapters(ctx, rows)
	if overlayErr != nil {
		return nil, "", overlayErr
	}
	var nextToken string
	if len(rows) > limit {
		nextToken = fmt.Sprintf("v|%d", rows[limit].StartMs)
		rows = rows[:limit]
	}
	return rows, nextToken, nil
}

func overlayMaterializedChapters(ctx context.Context, rows []DVRChapterRow) ([]DVRChapterRow, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ChapterID)
	}
	existing, err := getChaptersByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if row, ok := existing[rows[i].ChapterID]; ok {
			rows[i] = row
		}
	}
	return rows, nil
}
