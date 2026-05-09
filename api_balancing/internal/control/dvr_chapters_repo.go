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

// Chapters are virtual VOD-shaped views over foghorn.dvr_segments. This
// file is the SQL surface; dvr_chapter_generator.go is the manifest-build
// + S3-upload surface; jobs/chapter_sweeper.go drives the periodic
// materialization.

// Chapter mode constants — match the CHECK constraint on dvr_chapters.mode.
const (
	ChapterModeWindowSized   = "window_sized_chapters"
	ChapterModeFixedInterval = "fixed_interval"
	ChapterModeExplicitRange = "explicit_range"
)

// DVRChapterRow is one row from foghorn.dvr_chapters.
type DVRChapterRow struct {
	ChapterID       string
	ArtifactHash    string
	Mode            string
	IntervalSeconds sql.NullInt32
	StartMs         int64
	EndMs           int64
	IsCurrent       bool
	ManifestS3Key   sql.NullString
	MaterializedAt  sql.NullTime
	LastRebuiltAt   sql.NullTime
	SegmentCount    int32
	HasGaps         bool
	CreatedAt       time.Time
}

// BuildChapterID is the canonical chapter identity. Stable: same inputs
// always produce the same ID. Mode/policy changes that yield different
// (start_ms, end_ms) boundaries produce different IDs; old materialized
// chapters stay readable until cache expiry / retention cleanup, so
// in-flight viewers are never interrupted.
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

// UpsertChapter creates or updates a chapter row. Idempotent on chapter_id.
// Caller stamps materialized_at + last_rebuilt_at + manifest_s3_key after
// the S3 PUT succeeds; cache-on-request can recreate rows whose materialized
// object never made it to S3.
func UpsertChapter(ctx context.Context, c DVRChapterRow) error {
	if db == nil {
		return sql.ErrConnDone
	}
	var intervalArg interface{}
	if c.IntervalSeconds.Valid {
		intervalArg = c.IntervalSeconds.Int32
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin chapter upsert: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	if c.IsCurrent {
		if _, txErr := tx.ExecContext(ctx, `
			UPDATE foghorn.dvr_chapters
			   SET is_current = false
			 WHERE artifact_hash = $1
			   AND is_current = true
			   AND chapter_id <> $2
		`, c.ArtifactHash, c.ChapterID); txErr != nil {
			return fmt.Errorf("clear previous current chapter: %w", txErr)
		}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO foghorn.dvr_chapters (
			chapter_id, artifact_hash, mode, interval_seconds,
			start_ms, end_ms, is_current,
			manifest_s3_key, materialized_at, last_rebuilt_at,
			segment_count, has_gaps, created_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7,
			NULLIF($8,'')::text, $9, $10,
			$11, $12, NOW()
		)
		ON CONFLICT (chapter_id) DO UPDATE SET
			is_current      = EXCLUDED.is_current,
			manifest_s3_key = COALESCE(EXCLUDED.manifest_s3_key, foghorn.dvr_chapters.manifest_s3_key),
			materialized_at = COALESCE(EXCLUDED.materialized_at, foghorn.dvr_chapters.materialized_at),
			last_rebuilt_at = EXCLUDED.last_rebuilt_at,
			segment_count   = EXCLUDED.segment_count,
			has_gaps        = EXCLUDED.has_gaps OR foghorn.dvr_chapters.has_gaps
	`,
		c.ChapterID, c.ArtifactHash, c.Mode, intervalArg,
		c.StartMs, c.EndMs, c.IsCurrent,
		nullStringValue(c.ManifestS3Key), nullTimeValue(c.MaterializedAt), nullTimeValue(c.LastRebuiltAt),
		c.SegmentCount, c.HasGaps,
	)
	if err != nil {
		return fmt.Errorf("upsert chapter: %w", err)
	}
	return tx.Commit()
}

// GetChapter returns the chapter row by ID, or sql.ErrNoRows.
func GetChapter(ctx context.Context, chapterID string) (*DVRChapterRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	row := db.QueryRowContext(ctx, `
		SELECT chapter_id, artifact_hash, mode, interval_seconds,
		       start_ms, end_ms, is_current,
		       manifest_s3_key, materialized_at, last_rebuilt_at,
		       segment_count, has_gaps, created_at
		  FROM foghorn.dvr_chapters
		 WHERE chapter_id = $1
	`, chapterID)
	c, err := scanChapterRow(row)
	if err != nil {
		return nil, err
	}
	return c, nil
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
		       manifest_s3_key, materialized_at, last_rebuilt_at,
		       segment_count, has_gaps, created_at
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CurrentChapter returns the active rolling chapter for an artifact, if any.
func CurrentChapter(ctx context.Context, artifactHash string) (*DVRChapterRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	row := db.QueryRowContext(ctx, `
		SELECT chapter_id, artifact_hash, mode, interval_seconds,
		       start_ms, end_ms, is_current,
		       manifest_s3_key, materialized_at, last_rebuilt_at,
		       segment_count, has_gaps, created_at
		  FROM foghorn.dvr_chapters
		 WHERE artifact_hash = $1 AND is_current = true
		 ORDER BY last_rebuilt_at DESC NULLS LAST, start_ms DESC, chapter_id DESC
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
		       manifest_s3_key, materialized_at, last_rebuilt_at,
		       segment_count, has_gaps, created_at
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

// CloseCurrentChapter flips is_current=false for any current chapters of
// the artifact. Used at boundary rotation and at policy change.
func CloseCurrentChapter(ctx context.Context, artifactHash string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters
		   SET is_current = false
		 WHERE artifact_hash = $1 AND is_current = true
	`, artifactHash)
	if err != nil {
		return fmt.Errorf("close current chapter: %w", err)
	}
	return nil
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

// FlagChaptersOverlappingSegment marks every materialized chapter whose
// (start_ms, end_ms) overlaps the given segment range as has_gaps=true and
// drops last_rebuilt_at so the sweeper rebuilds them with the GAP marker.
// Used by the DVRSegmentDropped(was_uploaded=false) path.
//
// Index-backed via idx_foghorn_dvr_chapters_overlap. Bounded by the
// number of materialized chapters overlapping a single segment — at most
// a small handful for any sane chapter mode.
func FlagChaptersOverlappingSegment(ctx context.Context, artifactHash string, segmentStartMs, segmentEndMs int64) (int64, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	res, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters
		   SET has_gaps        = true,
		       last_rebuilt_at = NULL
		 WHERE artifact_hash = $1
		   AND manifest_s3_key IS NOT NULL
		   AND start_ms < $3
		   AND end_ms   > $2
	`, artifactHash, segmentStartMs, segmentEndMs)
	if err != nil {
		return 0, fmt.Errorf("flag overlapping chapters: %w", err)
	}
	return res.RowsAffected()
}

// DirtyMaterializedChapters returns materialized chapters whose manifest must
// be rebuilt. The dirty marker is last_rebuilt_at=NULL; has_gaps remains the
// durable fact that a rebuilt chapter contains missing media.
func DirtyMaterializedChapters(ctx context.Context, limit int) ([]DVRChapterRow, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, `
		SELECT chapter_id, artifact_hash, mode, interval_seconds,
		       start_ms, end_ms, is_current,
		       manifest_s3_key, materialized_at, last_rebuilt_at,
		       segment_count, has_gaps, created_at
		  FROM foghorn.dvr_chapters
		 WHERE manifest_s3_key IS NOT NULL
		   AND last_rebuilt_at IS NULL
		 ORDER BY created_at ASC, chapter_id ASC
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list dirty chapters: %w", err)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
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

func ClearCurrentChaptersForInactiveDVRs(ctx context.Context) (int64, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	res, err := db.ExecContext(ctx, `
		UPDATE foghorn.dvr_chapters c
		   SET is_current = false
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

func MarkDVRChapterBackfillComplete(ctx context.Context, artifactHash string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		   SET dvr_chapter_backfill_complete = true,
		       updated_at = NOW()
		 WHERE artifact_hash = $1
		   AND artifact_type = 'dvr'
		   AND status IN ('completed', 'completed_partial', 'failed', 'ready')
	`, artifactHash)
	return err
}

func CountMaterializedClosedChapters(ctx context.Context, artifactHash, mode string, intervalSeconds int32, startMs, endMs int64) (int64, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	var count int64
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM foghorn.dvr_chapters
		 WHERE artifact_hash = $1
		   AND mode = $2
		   AND COALESCE(interval_seconds, 0) = $3
		   AND start_ms >= $4
		   AND end_ms <= $5
		   AND is_current = false
		   AND manifest_s3_key IS NOT NULL
		   AND materialized_at IS NOT NULL
		   AND last_rebuilt_at IS NOT NULL
	`, artifactHash, mode, intervalSeconds, startMs, endMs).Scan(&count)
	return count, err
}

func NextMissingClosedChapterStart(ctx context.Context, artifactHash, mode string, intervalSeconds int32, firstStartMs, tailStartMs int64) (int64, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	intervalMs := int64(intervalSeconds) * 1000
	if intervalMs <= 0 || tailStartMs <= firstStartMs {
		return 0, nil
	}
	var startMs int64
	err := db.QueryRowContext(ctx, `
		WITH expected AS (
			SELECT generate_series($4::bigint, $5::bigint - $6::bigint, $6::bigint) AS start_ms
		)
		SELECT e.start_ms
		  FROM expected e
		  LEFT JOIN foghorn.dvr_chapters c
		    ON c.artifact_hash = $1
		   AND c.mode = $2
		   AND COALESCE(c.interval_seconds, 0) = $3
		   AND c.start_ms = e.start_ms
		   AND c.end_ms = e.start_ms + $6
		   AND c.manifest_s3_key IS NOT NULL
		   AND c.last_rebuilt_at IS NOT NULL
		   AND c.is_current = false
		 WHERE c.chapter_id IS NULL
		 ORDER BY e.start_ms ASC
		 LIMIT 1
	`, artifactHash, mode, intervalSeconds, firstStartMs, tailStartMs, intervalMs).Scan(&startMs)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find missing closed chapter: %w", err)
	}
	return startMs, nil
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

// ListChaptersForArtifact returns chapters for a player UI page. Caller
// MUST pass a non-zero limit; the bounded-operations invariant requires
// every API page to be capped (default 200 in the public surface).
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
	// Keyset cursor: pageToken is "start_ms|chapter_id" from the last row.
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
		       manifest_s3_key, materialized_at, last_rebuilt_at,
		       segment_count, has_gaps, created_at
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
		&c.ManifestS3Key, &c.MaterializedAt, &c.LastRebuiltAt,
		&c.SegmentCount, &c.HasGaps, &c.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

func scanChapterRowFromRows(rows *sql.Rows) (*DVRChapterRow, error) {
	return scanChapterRow(rows)
}

func nullStringValue(s sql.NullString) string {
	if !s.Valid {
		return ""
	}
	return s.String
}

func nullTimeValue(t sql.NullTime) interface{} {
	if !t.Valid {
		return nil
	}
	return t.Time
}
