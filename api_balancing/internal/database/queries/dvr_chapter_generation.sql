-- name: DeleteOpenDVRChapters :exec
DELETE FROM foghorn.dvr_chapters
WHERE artifact_hash = $1 AND state = 'open';

-- Truncates an open chapter at the recording's stop and closes it, keeping
-- its id.
-- name: CloseOpenDVRChapterAt :execrows
UPDATE foghorn.dvr_chapters
SET end_ms = sqlc.arg(end_ms), is_current = false, state = 'closed'
WHERE chapter_id = sqlc.arg(chapter_id) AND state = 'open' AND sqlc.arg(end_ms)::bigint > start_ms;

-- Drops unfinalized chapters of a stopped recording that start after its last
-- recorded segment. A chapter owns the segments that start inside it, so such
-- a chapter can hold no media: the writer outlives the publisher for Mist's
-- resume window and the stop lands past the media. Lost segments keep their
-- ledger rows, so a chapter whose media went missing is never dropped here.
-- chapter_id NULL applies to every chapter of the recording.
-- name: DeleteDVRChaptersPastRecordedMedia :execrows
DELETE FROM foghorn.dvr_chapters c
WHERE c.artifact_hash = sqlc.arg(artifact_hash)
  AND (sqlc.narg(chapter_id)::text IS NULL OR c.chapter_id = sqlc.narg(chapter_id)::text)
  AND c.state IN ('open', 'closed')
  AND c.playback_artifact_hash IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.artifacts pa
      WHERE pa.origin_type = 'dvr_chapter' AND pa.origin_id = c.chapter_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.dvr_segments s
      WHERE s.artifact_hash = c.artifact_hash AND s.media_start_ms >= c.start_ms
  )
  AND EXISTS (
      SELECT 1 FROM foghorn.artifacts a
      WHERE a.artifact_hash = c.artifact_hash AND a.artifact_type = 'dvr'
        AND a.status NOT IN ('requested', 'starting', 'recording', 'stopping')
  );

-- The latest start among a recording's segment ledger rows, lost ones
-- included; -1 when nothing was recorded.
-- name: GetDVRLastSegmentStartMs :one
SELECT COALESCE(MAX(media_start_ms), -1)::bigint AS last_start_ms
FROM foghorn.dvr_segments
WHERE artifact_hash = $1;

-- name: InsertClosedDVRChapter :exec
INSERT INTO foghorn.dvr_chapters (
    chapter_id, artifact_hash, mode, interval_seconds,
    start_ms, end_ms, is_current, state, segment_count, has_gaps, created_at
) VALUES ($1, $2, $3, $4, $5, $6, false, 'closed', 0, false, NOW())
ON CONFLICT (chapter_id) DO NOTHING;

-- name: GetDVRChapterPolicy :one
SELECT COALESCE(dvr_chapter_mode, '')::text AS mode,
       COALESCE(dvr_chapter_interval, 0)::integer AS interval_seconds,
       COALESCE(FLOOR(EXTRACT(EPOCH FROM started_at) * 1000), 0)::bigint AS started_at_ms,
       COALESCE(FLOOR(EXTRACT(EPOCH FROM ended_at) * 1000), 0)::bigint AS ended_at_ms,
       COALESCE(dvr_window_seconds, 0)::integer AS window_seconds
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = 'dvr';

-- name: GetDVRChapterMaxRange :one
SELECT dvr_window_seconds
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = 'dvr';

-- name: GetTenantDVRChapterMaxRange :one
SELECT dvr_window_seconds
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND tenant_id = $2;
