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
