package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// DVR PER-SEGMENT LEDGER
// foghorn.dvr_segments is the durable record of every recorded segment for
// a DVR artifact. Foghorn is the source of truth; the sidecar reports
// segments via the helmsman control stream and never queries this table.
//
// Chapter finalization reads bounded ranges from this table to remux
// the canonical .mkv. A 'lost_local' row inside a chapter range moves
// that chapter to state='failed_source_missing' (all-or-nothing
// chapter artifacts; partial MKVs are never produced).

// DVRSegmentRow is a row from foghorn.dvr_segments.
type DVRSegmentRow struct {
	ArtifactHash   string
	SegmentName    string
	Sequence       int64
	MediaStartMs   int64
	MediaEndMs     int64
	DurationMs     int64
	SizeBytes      sql.NullInt64
	S3Key          string
	Status         string
	DropReason     sql.NullString
	CreatedAt      time.Time
	UploadedAt     sql.NullTime
	DeletedLocalAt sql.NullTime
	DroppedAt      sql.NullTime
}

// ErrDVRSegmentTerminal is returned when the parent DVR artifact rejects
// new segment rows. Callers should not retry; the sidecar drops the segment.
var ErrDVRSegmentTerminal = errors.New("dvr artifact in terminal state; segment rejected")

// ErrDVRSegmentTimingMismatch is returned when a RecordDVRSegment retry (or
// heal-from-lost_local attempt) supplies timing that does not match the
// ledger row's recorded (media_start_ms, media_end_ms, duration_ms). A wrong
// file with the same name must never claim an existing sequence — accepting
// it would corrupt chapter placement.
var ErrDVRSegmentTimingMismatch = errors.New("dvr segment timing mismatch; refusing to reuse sequence")

// rollbackQuiet rolls back a transaction, ignoring sql.ErrTxDone (which
// fires when Commit already succeeded). Real rollback errors are silently
// dropped; in this package the only callers commit-or-die so a failed
// rollback after a failed commit means the work is gone either way.
func rollbackQuiet(tx *sql.Tx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		// best-effort; nothing actionable for the caller
		_ = err //nolint:errcheck
	}
}

// dvrSegmentTerminalStatuses are statuses where new ledger inserts are
// refused. Matches the manifest-permission rejection set so the two sides
// stay aligned.
var dvrSegmentTerminalStatuses = map[string]struct{}{
	"finalizing":        {},
	"completed":         {},
	"completed_partial": {},
	"failed":            {},
	"deleted":           {},
}

var dvrSegmentRecoveryInsertStatuses = map[string]struct{}{
	"completed":         {},
	"completed_partial": {},
	"failed":            {},
}

// InsertDVRSegment inserts a new segment row in 'pending' status and assigns
// a monotonic sequence per artifact. Returns the assigned sequence.
//
// Refuses inserts when the parent artifact is in a terminal state — that
// path returns ErrDVRSegmentTerminal so the caller can emit
// DVRSegmentDropped(was_uploaded=false) instead of looping a doomed retry.
// Startup recovery can opt in to inserting missing rows for finalized
// artifacts after it has recovered timing from a local DVR manifest.
//
// Sequence is computed as max(sequence)+1 inside the same transaction so
// concurrent inserts on the same artifact stay monotonic. The unique index
// idx_foghorn_dvr_segments_sequence enforces the invariant.
func InsertDVRSegment(
	ctx context.Context,
	artifactHash, segmentName, s3Key string,
	mediaStartMs, mediaEndMs, durationMs int64,
	allowRecoveryInsert bool,
) (int64, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer rollbackQuiet(tx)

	var artifactStatus string
	if scanErr := tx.QueryRowContext(ctx,
		`SELECT status FROM foghorn.artifacts WHERE artifact_hash = $1 AND artifact_type = 'dvr' FOR UPDATE`,
		artifactHash,
	).Scan(&artifactStatus); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return 0, fmt.Errorf("dvr artifact %s not found", artifactHash)
		}
		return 0, fmt.Errorf("lookup artifact: %w", scanErr)
	}
	// If a segment by this name already exists (e.g. retry from sidecar OR a
	// reappearance after lost_local), validate timing before reusing the
	// sequence. The strict (media_start_ms, media_end_ms, duration_ms)
	// equality is the safety check: a wrong file with the same name must not
	// heal a gap or claim a sequence belonging to a different segment.
	//
	// For lost_local rows with matching timing, transition back to 'pending'
	// (heal) and reuse the sequence. The sidecar can then upload normally and
	// MarkDVRSegmentUploaded will succeed from 'pending'.
	//
	// The 'finalizing' state still needs retry support — FinalizeDVR asks the
	// sidecar to retry pending rows after claiming finalization. Fully
	// terminal artifacts still reject retry attempts.
	var existing struct {
		sequence     sql.NullInt64
		status       sql.NullString
		mediaStartMs sql.NullInt64
		mediaEndMs   sql.NullInt64
		durationMs   sql.NullInt64
	}
	err = tx.QueryRowContext(ctx,
		`SELECT sequence, status, media_start_ms, media_end_ms, duration_ms
		   FROM foghorn.dvr_segments
		  WHERE artifact_hash = $1 AND segment_name = $2`,
		artifactHash, segmentName,
	).Scan(&existing.sequence, &existing.status, &existing.mediaStartMs, &existing.mediaEndMs, &existing.durationMs)
	if err == nil && existing.sequence.Valid {
		// Strict timing match guards against wrong-file-same-name corruption.
		if existing.mediaStartMs.Int64 != mediaStartMs ||
			existing.mediaEndMs.Int64 != mediaEndMs ||
			existing.durationMs.Int64 != durationMs {
			return 0, ErrDVRSegmentTimingMismatch
		}
		// The parent-terminal check only rejects retries for rows that are
		// already settled — uploaded (S3 has it) or deleted_local (already
		// evicted). Rows in pending / failed_upload / lost_local are NOT
		// settled even when the parent has reached completed/failed: the
		// recording finished but this segment never made it to S3. Allowing
		// the retry lets the seeded-completed-DVR + pending-segments case
		// (and any post-finalize race) actually upload.
		switch existing.status.String {
		case "lost_local":
			// Timing already validated above. Transition back to pending so
			// the sidecar can upload and MarkDVRSegmentUploaded can succeed.
			if _, healErr := tx.ExecContext(ctx, `
				UPDATE foghorn.dvr_segments
				   SET status = 'pending'
				 WHERE artifact_hash = $1 AND segment_name = $2 AND status = 'lost_local'
			`, artifactHash, segmentName); healErr != nil {
				return 0, fmt.Errorf("heal lost_local: %w", healErr)
			}
		case "pending", "failed_upload":
			// Pre-upload state — retry is always permitted, parent state
			// doesn't matter. Caller mints a fresh presigned URL.
		case "uploaded", "deleted_local":
			// Settled. Only block when the parent is also terminal — for
			// 'finalizing' the upload is still in-flight and the sidecar
			// may retry.
			if _, terminal := dvrSegmentTerminalStatuses[artifactStatus]; terminal && artifactStatus != "finalizing" {
				return 0, ErrDVRSegmentTerminal
			}
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return 0, commitErr
		}
		return existing.sequence.Int64, nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("lookup existing segment: %w", err)
	}

	if _, terminal := dvrSegmentTerminalStatuses[artifactStatus]; terminal {
		if _, recoverable := dvrSegmentRecoveryInsertStatuses[artifactStatus]; !allowRecoveryInsert || !recoverable {
			return 0, ErrDVRSegmentTerminal
		}
	}

	if allowRecoveryInsert && s3Key == "" {
		return 0, ErrDVRSegmentTerminal
	}

	var nextSeq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence), -1) + 1 FROM foghorn.dvr_segments WHERE artifact_hash = $1`,
		artifactHash,
	).Scan(&nextSeq); err != nil {
		return 0, fmt.Errorf("assign sequence: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO foghorn.dvr_segments (
			artifact_hash, segment_name, sequence,
			media_start_ms, media_end_ms, duration_ms,
			s3_key, status, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', NOW())
	`, artifactHash, segmentName, nextSeq, mediaStartMs, mediaEndMs, durationMs, s3Key); err != nil {
		return 0, fmt.Errorf("insert segment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return nextSeq, nil
}

// MarkDVRSegmentUploaded transitions a segment row to 'uploaded' and stamps
// the confirmed S3 size. No-op (no error) if the row is already uploaded —
// the sidecar may resend the mark on retry. lost_local is NOT a permitted
// source state for this transition: a wrong file with the same name must not
// heal a gap. Startup recovery uses RecordDVRSegment's strict timing match to
// take the row back to 'pending' before this can succeed.
func MarkDVRSegmentUploaded(ctx context.Context, artifactHash, segmentName string, sizeBytes int64) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_segments
		   SET status = 'uploaded',
		       size_bytes = $3,
		       uploaded_at = NOW()
		 WHERE artifact_hash = $1
		   AND segment_name = $2
		   AND status IN ('pending', 'failed_upload')
	`, artifactHash, segmentName, sizeBytes)
	if err != nil {
		return fmt.Errorf("mark uploaded: %w", err)
	}
	return nil
}

// MarkDVRSegmentDropped transitions a segment row to deleted_local (was
// uploaded before eviction; chapter finalization can recover from S3)
// or lost_local (lost before upload; any chapter overlapping the row
// moves to state='failed_source_missing'). drop_reason is recorded
// for ops triage. Idempotent: a second call with the same
// (was_uploaded) classification is a no-op.
//
// The mediaStart/mediaEnd/durationMs/sizeBytes args carry timing from
// the sidecar's DVRSegmentDropped event. They are only used when the
// row does not already exist — the terminal-rejection path emits
// DVRSegmentDropped for a segment that was never registered via
// RecordDVRSegment, so there is no row to UPDATE. Without those
// timing fields the finalization queue couldn't locate the lost
// segment on the chapter timeline. Pass 0 for unknown/uninteresting
// timing (e.g. mid-stream eviction of a row that already exists).
func MarkDVRSegmentDropped(
	ctx context.Context,
	artifactHash, segmentName, reason string,
	wasUploaded bool,
	mediaStartMs, mediaEndMs, durationMs int64,
	sizeBytes int64,
) error {
	if db == nil {
		return sql.ErrConnDone
	}
	target := "lost_local"
	if wasUploaded {
		target = "deleted_local"
	}
	// Only transition from live source states. Excluding the terminal
	// states (deleted_local, lost_local, reclaimed) keeps a delayed or
	// duplicate Helmsman ack from regressing a fully reclaimed row back
	// to deleted_local — reclaim is meant to be idempotent.
	res, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_segments
		   SET status = $3,
		       drop_reason = $4,
		       deleted_local_at = CASE WHEN $5 THEN NOW() ELSE deleted_local_at END,
		       dropped_at = NOW()
		 WHERE artifact_hash = $1
		   AND segment_name = $2
		   AND status NOT IN ('deleted_local', 'lost_local', 'reclaimed')
	`, artifactHash, segmentName, target, reason, wasUploaded)
	if err != nil {
		return fmt.Errorf("mark dropped: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark dropped rows affected: %w", err)
	}
	if affected > 0 {
		return nil
	}
	// No existing row. Only insert a placeholder for the lost_local case —
	// "deleted_local with no row" is meaningless (the upload would have
	// created the row), but "lost_local with no row" is the real terminal-
	// rejection path (RecordDVRSegment refused → no row exists →
	// DVRSegmentDropped fired). We persist the lost row so the chapter
	// finalization queue can detect the missing segment and classify the
	// overlapping chapter as failed_source_missing.
	if wasUploaded {
		return nil
	}
	if mediaStartMs <= 0 || mediaEndMs <= mediaStartMs {
		// No usable timing — log via caller; without timing we can't place
		// the gap on the timeline, so we'd render a no-op row.
		return fmt.Errorf("lost_local insert refused: missing media timing for %s/%s", artifactHash, segmentName)
	}
	// Insert under a tx so sequence assignment is monotonic against other
	// writers (RecordDVRSegment can race here even though the artifact is
	// terminal — Foghorn's terminal-state guard fires first, but in-flight
	// writes may still arrive).
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for lost_local insert: %w", err)
	}
	defer rollbackQuiet(tx)
	var nextSeq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence), -1) + 1 FROM foghorn.dvr_segments WHERE artifact_hash = $1`,
		artifactHash,
	).Scan(&nextSeq); err != nil {
		return fmt.Errorf("assign sequence for lost_local: %w", err)
	}
	var sizeArg interface{}
	if sizeBytes > 0 {
		sizeArg = sizeBytes
	}
	// The ON CONFLICT clause exists to handle the race where the row
	// gets inserted between the UPDATE above and this INSERT — we want
	// to win the race only against live source states. A delayed
	// was_uploaded=false drop must NOT regress a terminal row
	// (deleted_local / reclaimed / already lost_local) back to lost_local.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO foghorn.dvr_segments (
			artifact_hash, segment_name, sequence,
			media_start_ms, media_end_ms, duration_ms,
			size_bytes, s3_key, status, drop_reason,
			created_at, dropped_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, '', 'lost_local', $8, NOW(), NOW())
		ON CONFLICT (artifact_hash, segment_name) DO UPDATE SET
			status      = 'lost_local',
			drop_reason = EXCLUDED.drop_reason,
			dropped_at  = NOW()
		  WHERE foghorn.dvr_segments.status NOT IN ('deleted_local', 'lost_local', 'reclaimed')
	`, artifactHash, segmentName, nextSeq, mediaStartMs, mediaEndMs, durationMs, sizeArg, reason); err != nil {
		return fmt.Errorf("insert lost_local row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit lost_local: %w", err)
	}
	return nil
}

// ListEvictableDVRSegments returns segment names that are safe to delete
// locally for the given DVR. A segment is evictable iff:
//   - status='uploaded' (durable on S3 as recovery source), AND
//   - media_end_ms is past the rolling DVR window, AND
//   - every overlapping chapter is frozen or reclaimed (or none exists,
//     i.e. chapters mode is off).
//
// The chapter-state predicate is the load-bearing one: source segments
// stay pinned until every overlapping chapter is durable so chapter
// finalization always has a complete ledger to remux from. Used by the
// sidecar's storage-pressure path; routine eviction is owned by the
// chapter reclaim sweep, not the sidecar.
func ListEvictableDVRSegments(
	ctx context.Context,
	artifactHash string,
	windowSeconds int,
	maxCount int,
) ([]string, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	if windowSeconds <= 0 {
		return nil, nil
	}
	cutoffMs := time.Now().UnixMilli() - int64(windowSeconds)*1000
	if maxCount <= 0 {
		maxCount = 500
	}
	if maxCount > 1000 {
		maxCount = 1000
	}
	rows, err := db.QueryContext(ctx, `
		SELECT s.segment_name
		  FROM foghorn.dvr_segments s
		 WHERE s.artifact_hash = $1
		   AND s.status = 'uploaded'
		   AND s.media_end_ms < $2
		   AND NOT EXISTS (
		       SELECT 1
		         FROM foghorn.dvr_chapters c
		        WHERE c.artifact_hash = s.artifact_hash
		          AND c.start_ms < s.media_end_ms
		          AND c.end_ms   > s.media_start_ms
		          AND c.state NOT IN ('frozen', 'reclaimed')
		   )
		 ORDER BY s.sequence ASC
		 LIMIT $3
	`, artifactHash, cutoffMs, maxCount)
	if err != nil {
		return nil, fmt.Errorf("list evictable: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// ListPendingDVRSegments returns segments that are still pending or have
// failed_upload, optionally older than a cutoff. Used during finalization
// to drive RetryDVRSegmentUpload in bounded batches.
func ListPendingDVRSegments(
	ctx context.Context,
	artifactHash string,
	olderThan time.Duration,
	limit int,
) ([]DVRSegmentRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 1000 {
		limit = 1000
	}
	cutoff := time.Now().Add(-olderThan)
	rows, err := db.QueryContext(ctx, `
		SELECT artifact_hash, segment_name, sequence,
		       media_start_ms, media_end_ms, duration_ms,
		       size_bytes, s3_key, status, drop_reason,
		       created_at, uploaded_at, deleted_local_at, dropped_at
		  FROM foghorn.dvr_segments
		 WHERE artifact_hash = $1
		   AND status IN ('pending', 'failed_upload')
		   AND created_at <= $2
		 ORDER BY sequence ASC
		 LIMIT $3
	`, artifactHash, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending: %w", err)
	}
	defer rows.Close()
	return scanDVRSegmentRows(rows)
}

// ListDVRSegmentsOwnedByChapter returns segment rows whose
// media_start_ms falls inside [startMs, endMs). The start-of-segment
// ownership rule ensures every segment belongs to exactly one chapter
// even when chapter boundaries don't align to segment boundaries, so
// finalized chapter MKVs don't overlap and a boundary-straddling
// segment isn't duplicated in two adjacent chapters.
//
// Index-backed via idx_foghorn_dvr_segments_media_order.
func ListDVRSegmentsOwnedByChapter(ctx context.Context, artifactHash string, startMs, endMs int64) ([]DVRSegmentRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := db.QueryContext(ctx, `
		SELECT artifact_hash, segment_name, sequence,
		       media_start_ms, media_end_ms, duration_ms,
		       size_bytes, s3_key, status, drop_reason,
		       created_at, uploaded_at, deleted_local_at, dropped_at
		  FROM foghorn.dvr_segments
		 WHERE artifact_hash = $1
		   AND media_start_ms >= $2
		   AND media_start_ms <  $3
		 ORDER BY media_start_ms ASC, sequence ASC
	`, artifactHash, startMs, endMs)
	if err != nil {
		return nil, fmt.Errorf("list segments owned by chapter: %w", err)
	}
	defer rows.Close()
	return scanDVRSegmentRows(rows)
}

// ListDVRSegmentsForRange returns segment rows whose media-time range
// overlaps [startMs, endMs), ordered by (media_start_ms, sequence). When
// both startMs and endMs are 0, returns all rows for the artifact (admin
// only — chapter-aware callers always pass a bounded range, in keeping
// with the bounded-operations invariant for unbounded artifact lifetime).
//
// Index-backed via idx_foghorn_dvr_segments_media_order.
func ListDVRSegmentsForRange(ctx context.Context, artifactHash string, startMs, endMs int64) ([]DVRSegmentRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	var rows *sql.Rows
	var err error
	if startMs == 0 && endMs == 0 {
		rows, err = db.QueryContext(ctx, `
			SELECT artifact_hash, segment_name, sequence,
			       media_start_ms, media_end_ms, duration_ms,
			       size_bytes, s3_key, status, drop_reason,
			       created_at, uploaded_at, deleted_local_at, dropped_at
			  FROM foghorn.dvr_segments
			 WHERE artifact_hash = $1
			 ORDER BY media_start_ms ASC, sequence ASC
		`, artifactHash)
	} else {
		rows, err = db.QueryContext(ctx, `
			SELECT artifact_hash, segment_name, sequence,
			       media_start_ms, media_end_ms, duration_ms,
			       size_bytes, s3_key, status, drop_reason,
			       created_at, uploaded_at, deleted_local_at, dropped_at
			  FROM foghorn.dvr_segments
			 WHERE artifact_hash = $1
			   AND media_start_ms < $3
			   AND media_end_ms > $2
			 ORDER BY media_start_ms ASC, sequence ASC
		`, artifactHash, startMs, endMs)
	}
	if err != nil {
		return nil, fmt.Errorf("list segments for range: %w", err)
	}
	defer rows.Close()
	return scanDVRSegmentRows(rows)
}

// LookupDVRSegmentsByName returns ledger rows matching (artifact_hash,
// segment_name) IN (...). Bounded by the caller-supplied name list (caller
// must page; current callers cap at ~500 names per call). Used by sidecar
// restart reconciliation to repopulate its in-memory local cache from
// what's actually on disk, without ever asking for "all segments for this
// DVR" — preserves the bounded-operations invariant for unbounded artifact
// lifetime.
func LookupDVRSegmentsByName(ctx context.Context, artifactHash string, segmentNames []string) ([]DVRSegmentRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	if len(segmentNames) == 0 {
		return nil, nil
	}
	// Use unnest($2::text[]) for the IN clause so the query plan stays a
	// single index scan over (artifact_hash, segment_name) regardless of
	// list size.
	rows, err := db.QueryContext(ctx, `
		SELECT artifact_hash, segment_name, sequence,
		       media_start_ms, media_end_ms, duration_ms,
		       size_bytes, s3_key, status, drop_reason,
		       created_at, uploaded_at, deleted_local_at, dropped_at
		  FROM foghorn.dvr_segments
		 WHERE artifact_hash = $1
		   AND segment_name = ANY($2::text[])
	`, artifactHash, pq.StringArray(segmentNames))
	if err != nil {
		return nil, fmt.Errorf("lookup segments by name: %w", err)
	}
	defer rows.Close()
	return scanDVRSegmentRows(rows)
}

// MarkRemainingDVRSegmentsLost reclassifies every pending/failed_upload row
// for the artifact as lost_local with the given drop_reason. Used at the
// end of finalization to draw a hard line under the bounded retry window.
// Returns the number of rows reclassified.
func MarkRemainingDVRSegmentsLost(ctx context.Context, artifactHash, reason string) (int64, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	res, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_segments
		   SET status = 'lost_local',
		       drop_reason = $2,
		       dropped_at = NOW()
		 WHERE artifact_hash = $1
		   AND status IN ('pending', 'failed_upload')
	`, artifactHash, reason)
	if err != nil {
		return 0, fmt.Errorf("mark remaining lost: %w", err)
	}
	return res.RowsAffected()
}

func scanDVRSegmentRows(rows *sql.Rows) ([]DVRSegmentRow, error) {
	var out []DVRSegmentRow
	for rows.Next() {
		var r DVRSegmentRow
		if err := rows.Scan(
			&r.ArtifactHash, &r.SegmentName, &r.Sequence,
			&r.MediaStartMs, &r.MediaEndMs, &r.DurationMs,
			&r.SizeBytes, &r.S3Key, &r.Status, &r.DropReason,
			&r.CreatedAt, &r.UploadedAt, &r.DeletedLocalAt, &r.DroppedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
