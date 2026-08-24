-- name: GetDVRWindowSeconds :one
SELECT dvr_window_seconds
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash);

-- name: ListDVRCoverageSegments :many
SELECT GREATEST(media_start_ms, sqlc.arg(lower_bound)::bigint)::bigint AS seg_start,
       LEAST(media_end_ms, sqlc.arg(end_ms)::bigint)::bigint AS seg_end
FROM foghorn.dvr_segments
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND status NOT IN ('reclaimed', 'deleted_local', 'lost_local')
  AND media_end_ms > sqlc.arg(lower_bound)::bigint
  AND media_start_ms < sqlc.arg(end_ms)::bigint
ORDER BY media_start_ms, media_end_ms;

-- name: FindLatestDVRForStream :one
SELECT artifact_hash,
       COALESCE(internal_name, '')::text AS internal_name,
       COALESCE(EXTRACT(EPOCH FROM started_at) * 1000, 0)::bigint AS started_at_ms,
       status
FROM foghorn.artifacts
WHERE artifact_type = 'dvr'
  AND stream_internal_name = sqlc.arg(stream_internal_name)
  AND tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY started_at DESC NULLS LAST
LIMIT 1;

-- name: ListActiveDVRRecordingNodes :many
SELECT node_id
FROM foghorn.artifact_nodes
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND is_orphaned = false;

-- name: FindBestDVRChapterOverlap :one
SELECT COALESCE(c.playback_artifact_hash, '')::text AS playback_artifact_hash,
       GREATEST(COALESCE(c.actual_media_start_ms, c.start_ms), sqlc.arg(clip_start_ms)::bigint)::bigint AS overlap_start,
       LEAST(COALESCE(c.actual_media_end_ms, c.end_ms), sqlc.arg(clip_end_ms)::bigint)::bigint AS overlap_end
FROM foghorn.dvr_chapters c
JOIN foghorn.artifacts a ON a.artifact_hash = c.artifact_hash
WHERE a.artifact_type = 'dvr'
  AND a.stream_internal_name = sqlc.arg(stream_internal_name)
  AND a.tenant_id = sqlc.arg(tenant_id)::uuid
  AND c.state IN ('finalized', 'frozen', 'reclaimed')
  AND c.playback_artifact_hash IS NOT NULL
  AND COALESCE(c.actual_media_end_ms, c.end_ms) > sqlc.arg(clip_start_ms)::bigint
  AND COALESCE(c.actual_media_start_ms, c.start_ms) < sqlc.arg(clip_end_ms)::bigint
ORDER BY (LEAST(COALESCE(c.actual_media_end_ms, c.end_ms), sqlc.arg(clip_end_ms)::bigint)
        - GREATEST(COALESCE(c.actual_media_start_ms, c.start_ms), sqlc.arg(clip_start_ms)::bigint)) DESC
LIMIT 1;
