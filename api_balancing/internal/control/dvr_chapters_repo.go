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

	"github.com/lib/pq"
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
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters
		   SET playback_id = $2
		 WHERE chapter_id  = $1
	`, chapterID, playbackID)
	return err
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
	var intervalArg interface{}
	if c.IntervalSeconds.Valid {
		intervalArg = c.IntervalSeconds.Int32
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin open chapter: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	res, txErr := tx.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters
		   SET is_current = false,
		       state      = CASE WHEN state = 'open' THEN 'closed' ELSE state END
		 WHERE artifact_hash = $1
		   AND is_current = true
		   AND chapter_id <> $2
	`, c.ArtifactHash, c.ChapterID)
	if txErr != nil {
		return fmt.Errorf("close previous current chapter: %w", txErr)
	}
	closedPrevious, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("close previous current chapter rows affected: %w", err)
	}

	state := c.State
	if state == "" {
		state = ChapterStateOpen
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO foghorn.dvr_chapters (
			chapter_id, artifact_hash, mode, interval_seconds,
			start_ms, end_ms, is_current,
			state, segment_count, has_gaps, created_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, true,
			$7, 0, false, NOW()
		)
		ON CONFLICT (chapter_id) DO UPDATE SET
			is_current = EXCLUDED.is_current
	`,
		c.ChapterID, c.ArtifactHash, c.Mode, intervalArg,
		c.StartMs, c.EndMs, state,
	)
	if err != nil {
		return fmt.Errorf("open chapter: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if state == ChapterStateClosed || closedPrevious > 0 {
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
	res, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters
		   SET is_current = false,
		       state      = 'closed'
		 WHERE chapter_id = $1
		   AND state      = 'open'
	`, chapterID)
	if err != nil {
		return fmt.Errorf("close chapter: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("close chapter rows affected: %w", err)
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
	res, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters
		   SET is_current = false,
		       state      = CASE WHEN state = 'open' THEN 'closed' ELSE state END
		 WHERE artifact_hash = $1
		   AND is_current = true
	`, artifactHash)
	if err != nil {
		return fmt.Errorf("close current chapter for artifact: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("close current chapter rows affected: %w", err)
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
func MarkChapterFinalizing(ctx context.Context, chapterID, playbackHash string, staleTimeout time.Duration) (ok bool, err error) {
	if db == nil {
		return false, sql.ErrConnDone
	}
	res, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters
		   SET state                  = 'finalizing',
		       playback_artifact_hash = $2,
		       finalize_attempts      = finalize_attempts + 1,
		       finalize_started_at    = NOW()
		 WHERE chapter_id = $1
		   AND (state = 'closed'
		     OR (state = 'finalizing'
		         AND COALESCE(finalize_started_at, created_at) < NOW() - make_interval(secs => $3)))
	`, chapterID, playbackHash, staleTimeout.Seconds())
	if err != nil {
		return false, fmt.Errorf("mark chapter finalizing: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// MarkChapterFinalized transitions finalizing → finalized after the
// processing job's PUSH_END handler validates output. segment_count
// and has_gaps come from the actual artifact contents; mediaStartMs /
// mediaEndMs come from the first/last owned segments and pin the MKV
// timeline to wall-clock without drift even when chapter boundaries
// don't align to segment boundaries. Pass 0 for media bounds when
// unknown (column stays NULL).
func MarkChapterFinalized(ctx context.Context, chapterID string, segmentCount int32, hasGaps bool, mediaStartMs, mediaEndMs int64) error {
	if db == nil {
		return sql.ErrConnDone
	}
	var mediaStartArg, mediaEndArg interface{}
	if mediaStartMs > 0 {
		mediaStartArg = mediaStartMs
	}
	if mediaEndMs > mediaStartMs {
		mediaEndArg = mediaEndMs
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters
		   SET state                 = 'finalized',
		       segment_count         = $2,
		       has_gaps              = $3,
		       actual_media_start_ms = $4,
		       actual_media_end_ms   = $5
		 WHERE chapter_id = $1
		   AND state      = 'finalizing'
	`, chapterID, segmentCount, hasGaps, mediaStartArg, mediaEndArg)
	if err != nil {
		return fmt.Errorf("mark chapter finalized: %w", err)
	}
	return nil
}

// MarkChapterFrozen transitions finalized → frozen once the playback
// artifact is sync_status='synced' AND dtsh_synced=true. The reclaim
// sweep can now delete source segments + temporary S3 segment objects.
func MarkChapterFrozen(ctx context.Context, chapterID string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters
		   SET state     = 'frozen',
		       frozen_at = NOW()
		 WHERE chapter_id = $1
		   AND state      = 'finalized'
	`, chapterID)
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
	res, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters
		   SET reclaim_started_at = NOW()
		 WHERE chapter_id = $1
		   AND state      = 'frozen'
		   AND (reclaim_started_at IS NULL OR reclaim_started_at < NOW() - make_interval(secs => $2))
	`, chapterID, freshness.Seconds())
	if err != nil {
		return false, fmt.Errorf("mark chapter reclaim started: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
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
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters
		   SET state = 'reclaimed'
		 WHERE chapter_id = $1
		   AND state      = 'frozen'
	`, chapterID)
	if err != nil {
		return fmt.Errorf("mark chapter reclaimed: %w", err)
	}
	return nil
}

// MarkChapterFailed sets a terminal failure state plus a human-readable
// reason. Used when recovery from source-missing is exhausted, or when
// the input ledger is unrecoverable.
func MarkChapterFailed(ctx context.Context, chapterID, terminalState, reason string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	switch terminalState {
	case ChapterStateFailedSourceMissing, ChapterStateFailedPermanent:
	default:
		return fmt.Errorf("invalid terminal state %q", terminalState)
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters
		   SET state               = $2,
		       last_failure_reason = $3
		 WHERE chapter_id = $1
		   AND state IN ('closed', 'finalizing')
	`, chapterID, terminalState, reason)
	if err != nil {
		return fmt.Errorf("mark chapter failed: %w", err)
	}
	return nil
}

// RetryChapterFinalize rolls finalizing → closed after a transient
// failure so the queue picks the row up again on its next sweep.
// last_failure_reason carries the transient cause for operator
// visibility.
func RetryChapterFinalize(ctx context.Context, chapterID, reason string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters
		   SET state               = 'closed',
		       last_failure_reason = $2
		 WHERE chapter_id = $1
		   AND state      = 'finalizing'
	`, chapterID, reason)
	if err != nil {
		return fmt.Errorf("retry chapter finalize: %w", err)
	}
	return nil
}

// ListChaptersNeedingFinalization returns chapters in 'closed' state
// (or stuck in 'finalizing' past a timeout — caller picks the cutoff).
// Backed by idx_foghorn_dvr_chapters_pending.
func ListChaptersNeedingFinalization(ctx context.Context, limit int, finalizingTimeout time.Duration) ([]DVRChapterRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `
		SELECT chapter_id, artifact_hash, mode, interval_seconds,
		       start_ms, end_ms, is_current,
		       state, playback_artifact_hash, playback_id, finalize_attempts,
		       finalize_started_at, frozen_at,
		       last_failure_reason, reclaim_started_at,
		       segment_count, has_gaps,
		       actual_media_start_ms, actual_media_end_ms,
		       created_at
		  FROM foghorn.dvr_chapters
		 WHERE state = 'closed'
		    OR (state = 'finalizing'
		        AND COALESCE(finalize_started_at, created_at) < NOW() - make_interval(secs => $1))
		 ORDER BY created_at ASC, chapter_id ASC
		 LIMIT $2
	`, finalizingTimeout.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("list chapters needing finalization: %w", err)
	}
	defer rows.Close()
	var out []DVRChapterRow
	for rows.Next() {
		c, err := scanChapterRowFromRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ListChaptersNeedingReclaim returns frozen chapters whose source
// segments haven't been reclaimed yet. Backed by
// idx_foghorn_dvr_chapters_reclaim. Caller MUST call
// MarkChapterReclaimStarted before issuing reclaim orders to prevent
// duplicate work.
func ListChaptersNeedingReclaim(ctx context.Context, limit int, freshness time.Duration) ([]DVRChapterRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `
		SELECT chapter_id, artifact_hash, mode, interval_seconds,
		       start_ms, end_ms, is_current,
		       state, playback_artifact_hash, playback_id, finalize_attempts,
		       finalize_started_at, frozen_at,
		       last_failure_reason, reclaim_started_at,
		       segment_count, has_gaps,
		       actual_media_start_ms, actual_media_end_ms,
		       created_at
		  FROM foghorn.dvr_chapters
		 WHERE state = 'frozen'
		   AND (reclaim_started_at IS NULL OR reclaim_started_at < NOW() - make_interval(secs => $1))
		 ORDER BY created_at ASC, chapter_id ASC
		 LIMIT $2
	`, freshness.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("list chapters needing reclaim: %w", err)
	}
	defer rows.Close()
	var out []DVRChapterRow
	for rows.Next() {
		c, err := scanChapterRowFromRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// GetChapter returns the chapter row by ID, or sql.ErrNoRows.
func GetChapter(ctx context.Context, chapterID string) (*DVRChapterRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	row := db.QueryRowContext(ctx, `
		SELECT chapter_id, artifact_hash, mode, interval_seconds,
		       start_ms, end_ms, is_current,
		       state, playback_artifact_hash, playback_id, finalize_attempts,
		       finalize_started_at, frozen_at,
		       last_failure_reason, reclaim_started_at,
		       segment_count, has_gaps,
		       actual_media_start_ms, actual_media_end_ms,
		       created_at
		  FROM foghorn.dvr_chapters
		 WHERE chapter_id = $1
	`, chapterID)
	return scanChapterRow(row)
}

func getChaptersByID(ctx context.Context, chapterIDs []string) (map[string]DVRChapterRow, error) {
	out := make(map[string]DVRChapterRow, len(chapterIDs))
	if len(chapterIDs) == 0 {
		return out, nil
	}
	if db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := db.QueryContext(ctx, `
		SELECT chapter_id, artifact_hash, mode, interval_seconds,
		       start_ms, end_ms, is_current,
		       state, playback_artifact_hash, playback_id, finalize_attempts,
		       finalize_started_at, frozen_at,
		       last_failure_reason, reclaim_started_at,
		       segment_count, has_gaps,
		       actual_media_start_ms, actual_media_end_ms,
		       created_at
		  FROM foghorn.dvr_chapters
		 WHERE chapter_id = ANY($1)
	`, pq.Array(chapterIDs))
	if err != nil {
		return nil, fmt.Errorf("get chapters by id: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		c, scanErr := scanChapterRowFromRows(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out[c.ChapterID] = *c
	}
	return out, rows.Err()
}

// CurrentChapter returns the in-flight chapter for an artifact, if any.
func CurrentChapter(ctx context.Context, artifactHash string) (*DVRChapterRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	row := db.QueryRowContext(ctx, `
		SELECT chapter_id, artifact_hash, mode, interval_seconds,
		       start_ms, end_ms, is_current,
		       state, playback_artifact_hash, playback_id, finalize_attempts,
		       finalize_started_at, frozen_at,
		       last_failure_reason, reclaim_started_at,
		       segment_count, has_gaps,
		       actual_media_start_ms, actual_media_end_ms,
		       created_at
		  FROM foghorn.dvr_chapters
		 WHERE artifact_hash = $1 AND is_current = true
		 ORDER BY start_ms DESC, chapter_id DESC
		 LIMIT 1
	`, artifactHash)
	c, err := scanChapterRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

func LatestChapterBefore(ctx context.Context, artifactHash, mode string, intervalSeconds int32, beforeStartMs int64) (*DVRChapterRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	row := db.QueryRowContext(ctx, `
		SELECT chapter_id, artifact_hash, mode, interval_seconds,
		       start_ms, end_ms, is_current,
		       state, playback_artifact_hash, playback_id, finalize_attempts,
		       finalize_started_at, frozen_at,
		       last_failure_reason, reclaim_started_at,
		       segment_count, has_gaps,
		       actual_media_start_ms, actual_media_end_ms,
		       created_at
		  FROM foghorn.dvr_chapters
		 WHERE artifact_hash = $1
		   AND mode = $2
		   AND COALESCE(interval_seconds, 0) = $3
		   AND start_ms < $4
		 ORDER BY start_ms DESC, chapter_id DESC
		 LIMIT 1
	`, artifactHash, mode, intervalSeconds, beforeStartMs)
	c, err := scanChapterRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

func DeleteChapter(ctx context.Context, chapterID string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `DELETE FROM foghorn.dvr_chapters WHERE chapter_id = $1`, chapterID)
	if err != nil {
		return fmt.Errorf("delete chapter: %w", err)
	}
	return nil
}

func DVRArtifactStillRecording(ctx context.Context, artifactHash string) bool {
	if db == nil {
		return false
	}
	var st string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM foghorn.artifacts WHERE artifact_hash = $1 AND artifact_type = 'dvr'`,
		artifactHash,
	).Scan(&st); err != nil {
		return false
	}
	return st == "starting" || st == "recording"
}

// ClearCurrentChaptersForInactiveDVRs closes any chapter still marked
// is_current=true for a DVR artifact that is no longer recording. Used
// by the chapter sweeper to catch missed close transitions (e.g. DVR
// finalize crashed before CloseCurrentChapterForArtifact ran).
func ClearCurrentChaptersForInactiveDVRs(ctx context.Context) (int64, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	res, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters c
		   SET is_current = false,
		       state      = CASE WHEN c.state = 'open' THEN 'closed' ELSE c.state END
		  FROM foghorn.artifacts a
		 WHERE c.artifact_hash = a.artifact_hash
		   AND c.is_current = true
		   AND a.artifact_type = 'dvr'
		   AND a.status IN ('completed', 'completed_partial', 'failed', 'ready', 'deleted')
	`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext($1))`, artifactHash); err != nil {
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, unlockErr := conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock(hashtext($1))`, artifactHash); unlockErr != nil {
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
	var intervalArg interface{}
	if intervalSeconds > 0 {
		intervalArg = intervalSeconds
	}
	rows, err := db.QueryContext(ctx, `
		SELECT chapter_id, artifact_hash, mode, interval_seconds,
		       start_ms, end_ms, is_current,
		       state, playback_artifact_hash, playback_id, finalize_attempts,
		       finalize_started_at, frozen_at,
		       last_failure_reason, reclaim_started_at,
		       segment_count, has_gaps,
		       actual_media_start_ms, actual_media_end_ms,
		       created_at
		  FROM foghorn.dvr_chapters
		 WHERE artifact_hash = $1
		   AND start_ms >= $2
		   AND ($3 = 0 OR start_ms < $3)
		   AND ($4 = '' OR mode = $4)
		   AND ($5::int IS NULL OR COALESCE(interval_seconds, 0) = $5::int)
		   AND ($6 = 0 OR start_ms > $6 OR (start_ms = $6 AND chapter_id > $7))
		 ORDER BY start_ms ASC, chapter_id ASC
		 LIMIT $8
	`, artifactHash, startMs, endMs, mode, intervalArg, cursor, cursorID, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list chapters: %w", err)
	}
	defer rows.Close()
	var out []DVRChapterRow
	for rows.Next() {
		c, err := scanChapterRowFromRows(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
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

// helpers

type chapterRowScanner interface {
	Scan(...interface{}) error
}

func scanChapterRow(row chapterRowScanner) (*DVRChapterRow, error) {
	var c DVRChapterRow
	if err := row.Scan(
		&c.ChapterID, &c.ArtifactHash, &c.Mode, &c.IntervalSeconds,
		&c.StartMs, &c.EndMs, &c.IsCurrent,
		&c.State, &c.PlaybackArtifactHash, &c.PlaybackID, &c.FinalizeAttempts,
		&c.FinalizeStartedAt, &c.FrozenAt,
		&c.LastFailureReason, &c.ReclaimStartedAt,
		&c.SegmentCount, &c.HasGaps,
		&c.ActualMediaStartMs, &c.ActualMediaEndMs,
		&c.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

func scanChapterRowFromRows(rows *sql.Rows) (*DVRChapterRow, error) {
	return scanChapterRow(rows)
}
