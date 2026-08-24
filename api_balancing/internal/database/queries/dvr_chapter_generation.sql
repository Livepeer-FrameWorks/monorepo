-- name: DeleteOpenDVRChapters :exec
DELETE FROM foghorn.dvr_chapters
WHERE artifact_hash = $1 AND state = 'open';

-- name: InsertClosedDVRChapter :exec
INSERT INTO foghorn.dvr_chapters (
    chapter_id, artifact_hash, mode, interval_seconds,
    start_ms, end_ms, is_current, state, segment_count, has_gaps, created_at
) VALUES ($1, $2, $3, $4, $5, $6, false, 'closed', 0, false, NOW())
ON CONFLICT (chapter_id) DO NOTHING;

-- name: GetDVRChapterPolicy :one
SELECT COALESCE(dvr_chapter_mode, '')::text AS mode,
       COALESCE(dvr_chapter_interval, 0)::integer AS interval_seconds,
       COALESCE(EXTRACT(EPOCH FROM started_at) * 1000, 0)::bigint AS started_at_ms,
       COALESCE(EXTRACT(EPOCH FROM ended_at) * 1000, 0)::bigint AS ended_at_ms,
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
