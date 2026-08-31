-- name: InsertVODUploadRegistration :exec
INSERT INTO commodore.vod_assets (
    id, tenant_id, user_id, vod_hash, internal_name, playback_id,
    title, description, filename, content_type, size_bytes,
    origin_cluster_id, retention_until, requires_auth, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(user_id), sqlc.arg(vod_hash),
    sqlc.arg(internal_name), sqlc.arg(playback_id), sqlc.arg(title), sqlc.arg(description),
    sqlc.arg(filename), sqlc.arg(content_type), sqlc.arg(size_bytes),
    sqlc.arg(origin_cluster_id), sqlc.arg(retention_until), false, NOW(), NOW()
);

-- name: GetVODPlaybackID :one
SELECT playback_id
FROM commodore.vod_assets
WHERE tenant_id = $1 AND vod_hash = $2;

-- name: GetVODOriginCluster :one
SELECT origin_cluster_id
FROM commodore.vod_assets
WHERE vod_hash = $1 AND tenant_id = $2;

-- name: ResolveOwnedInternalNameByPlaybackID :one
SELECT internal_name
FROM (
    SELECT 0 AS priority, internal_name FROM commodore.streams
    WHERE lower(playback_id::text) = lower(sqlc.arg(playback_id)::text)
      AND tenant_id::text = sqlc.arg(tenant_id)::text
    UNION ALL
    SELECT 1, internal_name FROM commodore.vod_assets
    WHERE lower(playback_id::text) = lower(sqlc.arg(playback_id)::text)
      AND tenant_id::text = sqlc.arg(tenant_id)::text
    UNION ALL
    SELECT 2, internal_name FROM commodore.clips
    WHERE lower(playback_id::text) = lower(sqlc.arg(playback_id)::text)
      AND tenant_id::text = sqlc.arg(tenant_id)::text
    UNION ALL
    SELECT 3, internal_name FROM commodore.dvr_recordings
    WHERE lower(playback_id::text) = lower(sqlc.arg(playback_id)::text)
      AND tenant_id::text = sqlc.arg(tenant_id)::text
) candidates
ORDER BY priority
LIMIT 1;
