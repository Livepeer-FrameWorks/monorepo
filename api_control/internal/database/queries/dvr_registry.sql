-- name: GetLiveStreamInternalName :one
SELECT internal_name
FROM commodore.streams
WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL;

-- name: GetLiveStreamIDByInternalName :one
SELECT id::text
FROM commodore.streams
WHERE internal_name = $1 AND tenant_id = $2 AND deleted_at IS NULL;

-- name: GetStreamDVRChapterConfig :one
SELECT dvr_chapter_mode, dvr_chapter_interval_seconds
FROM commodore.streams
WHERE id = sqlc.arg(stream_id)::uuid AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetStreamIDForDVRRegistration :one
SELECT id::text
FROM commodore.streams
WHERE internal_name = $1 AND tenant_id = $2;

-- name: InsertDVRRegistration :exec
INSERT INTO commodore.dvr_recordings (
    id, tenant_id, user_id, stream_id, dvr_hash, internal_name, playback_id, stream_internal_name,
    origin_cluster_id, storage_cluster_id, retention_until, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(user_id), sqlc.arg(stream_id)::uuid,
    sqlc.arg(dvr_hash), sqlc.arg(internal_name), sqlc.arg(playback_id), sqlc.arg(stream_internal_name),
    sqlc.arg(origin_cluster_id), sqlc.arg(storage_cluster_id), sqlc.arg(retention_until), NOW(), NOW()
);

-- name: UpdateDVRRetention :execrows
UPDATE commodore.dvr_recordings
SET retention_until = $1,
    updated_at = NOW()
WHERE dvr_hash = $2
  AND tenant_id::text = $3;

-- name: ResolveClipByHash :one
SELECT c.tenant_id, c.user_id, c.stream_id, c.title, c.description,
       c.start_time, c.duration, c.clip_mode, s.internal_name AS stream_internal_name,
       c.playback_id, c.internal_name, c.origin_cluster_id
FROM commodore.clips c
LEFT JOIN commodore.streams s ON c.stream_id = s.id
WHERE c.clip_hash = $1;

-- name: ResolveDVRByHash :one
SELECT tenant_id, user_id, stream_id, stream_internal_name, playback_id, internal_name, origin_cluster_id
FROM commodore.dvr_recordings
WHERE dvr_hash = $1;

-- name: ResolveVODByHash :one
SELECT tenant_id, user_id, filename, title, description, playback_id, internal_name, origin_cluster_id
FROM commodore.vod_assets
WHERE vod_hash = $1;

-- name: ResolveVODByID :one
SELECT tenant_id, user_id, vod_hash, playback_id, internal_name
FROM commodore.vod_assets
WHERE id = $1;
