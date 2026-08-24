-- name: GetThumbnailPointerState :one
SELECT p.active_token,
       COALESCE(
           COALESCE(a.status IN ('deleted', 'failed', 'expired', 'aborted'), false)
               OR t.asset_key IS NOT NULL,
           false
       )::boolean AS gone
FROM (SELECT sqlc.arg(asset_key)::text AS k) key
LEFT JOIN foghorn.thumbnail_active_pointer p ON p.asset_key = key.k
LEFT JOIN foghorn.artifacts a ON a.artifact_hash = key.k
LEFT JOIN foghorn.stream_cleanup_obligation t ON t.asset_key = key.k;
