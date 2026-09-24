-- name: ListActiveDVRChapterPolicies :many
SELECT artifact_hash,
       dvr_chapter_mode,
       COALESCE(dvr_chapter_interval, 0)::integer AS chapter_interval_seconds,
       COALESCE(EXTRACT(EPOCH FROM started_at) * 1000, 0)::bigint AS started_at_ms,
       COALESCE(dvr_window_seconds, 0)::integer AS window_seconds
FROM foghorn.artifacts
WHERE artifact_type = 'dvr'
  AND status IN ('starting', 'recording')
  AND dvr_chapter_mode IS NOT NULL
  AND dvr_chapter_mode != '';

-- name: ListDVRTerminalChapterBackfill :many
SELECT artifact_hash,
       COALESCE(FLOOR(EXTRACT(EPOCH FROM ended_at) * 1000), 0)::bigint AS ended_at_ms
FROM foghorn.artifacts
WHERE artifact_type = 'dvr'
  AND status IN ('completed', 'completed_partial', 'failed', 'ready')
  AND ended_at IS NOT NULL
  AND dvr_chapter_mode IS NOT NULL
  AND dvr_chapter_mode != ''
  AND dvr_chapter_backfill_complete = false
ORDER BY ended_at, artifact_hash
LIMIT sqlc.arg(batch_size);

-- name: DVRTerminalChapterMaterialized :one
SELECT EXISTS (
    SELECT 1 FROM foghorn.dvr_chapters
    WHERE artifact_hash = sqlc.arg(artifact_hash)
      AND end_ms >= sqlc.arg(ended_at_ms)::bigint
)::boolean AS materialized;

-- name: DVRHasStoredSegments :one
SELECT EXISTS (
    SELECT 1 FROM foghorn.dvr_segments
    WHERE artifact_hash = sqlc.arg(artifact_hash)
      AND status IN ('uploaded', 'deleted_local')
)::boolean AS stored;

-- name: MarkDVRChapterBackfillComplete :exec
UPDATE foghorn.artifacts
SET dvr_chapter_backfill_complete = true
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'dvr';
