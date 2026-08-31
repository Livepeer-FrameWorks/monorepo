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
    origin_cluster_id, storage_cluster_id, retention_until, requires_auth, playback_policy,
    playback_webhook_secret_enc, playback_authority_ready,
    created_at, updated_at
)
SELECT
    sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(user_id), sqlc.arg(stream_id)::uuid,
    sqlc.arg(dvr_hash), sqlc.arg(internal_name), sqlc.arg(playback_id), sqlc.arg(stream_internal_name),
    sqlc.arg(origin_cluster_id), sqlc.arg(storage_cluster_id), sqlc.arg(retention_until),
    stream.requires_auth, stream.playback_policy, stream.playback_webhook_secret_enc, TRUE, NOW(), NOW()
FROM commodore.streams AS stream
WHERE stream.id = sqlc.arg(stream_id)::uuid
  AND stream.tenant_id = sqlc.arg(tenant_id)::uuid;

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
SELECT v.tenant_id, v.user_id, v.filename, v.title, v.description, v.playback_id,
       v.internal_name, v.origin_cluster_id,
       CASE WHEN v.origin_type = 'dvr_chapter' THEN 'chapter'::text ELSE 'vod'::text END AS content_type,
       COALESCE(parent_dvr.stream_internal_name, parent_stream.internal_name, '')::text AS parent_stream_internal_name
FROM commodore.vod_assets AS v
LEFT JOIN commodore.dvr_chapter_playback AS chapter
  ON chapter.tenant_id = v.tenant_id AND chapter.artifact_hash = v.vod_hash
LEFT JOIN commodore.dvr_recordings AS parent_dvr
  ON parent_dvr.tenant_id = chapter.tenant_id AND parent_dvr.dvr_hash = chapter.dvr_hash
LEFT JOIN commodore.streams AS parent_stream ON parent_stream.id = v.stream_id
WHERE v.vod_hash = $1;

-- name: ResolveVODByID :one
SELECT tenant_id, user_id, vod_hash, playback_id, internal_name
FROM commodore.vod_assets
WHERE id = $1;
