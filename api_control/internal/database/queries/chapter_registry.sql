-- name: LockArtifactCatalogKey :exec
SELECT pg_advisory_xact_lock(hashtext($1)::bigint);

-- name: GetVODTombstoneForUpdate :one
SELECT deletion_revision
FROM commodore.artifact_catalog_tombstones
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND kind = 'vod' AND artifact_hash = sqlc.arg(artifact_hash)
FOR UPDATE;

-- name: UpsertChapterPlaybackID :one
INSERT INTO commodore.dvr_chapter_playback (
    chapter_id, tenant_id, playback_id, artifact_hash, dvr_hash, created_at, updated_at
) VALUES (
    sqlc.arg(chapter_id), sqlc.arg(tenant_id)::uuid, sqlc.arg(playback_id), sqlc.arg(artifact_hash),
    NULLIF(sqlc.arg(dvr_hash)::text, ''), NOW(), NOW()
)
ON CONFLICT (chapter_id) DO UPDATE
SET artifact_hash = EXCLUDED.artifact_hash,
    dvr_hash = COALESCE(EXCLUDED.dvr_hash, commodore.dvr_chapter_playback.dvr_hash),
    updated_at = NOW()
WHERE commodore.dvr_chapter_playback.tenant_id = EXCLUDED.tenant_id
RETURNING playback_id;

-- name: UpsertChapterVODAsset :execrows
INSERT INTO commodore.vod_assets (
    id, tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id,
    title, description, filename, content_type,
    origin_cluster_id, storage_cluster_id,
    library_visible, origin_type, origin_id,
    requires_auth, playback_policy, playback_webhook_secret_enc,
    created_at, updated_at
) SELECT
    sqlc.arg(id), parent.tenant_id, parent.user_id, parent.stream_id,
    sqlc.arg(vod_hash), sqlc.arg(internal_name), sqlc.arg(playback_id),
    sqlc.arg(title), NULLIF(sqlc.arg(description)::text, ''), sqlc.arg(filename), sqlc.arg(content_type),
    NULLIF(sqlc.arg(origin_cluster_id)::text, ''), NULLIF(sqlc.arg(storage_cluster_id)::text, ''),
    false, 'dvr_chapter', sqlc.arg(origin_id),
    CASE WHEN parent.playback_authority_ready THEN parent.requires_auth ELSE COALESCE(parent_stream.requires_auth, TRUE) END,
    CASE WHEN parent.playback_authority_ready THEN parent.playback_policy ELSE parent_stream.playback_policy END,
    CASE WHEN parent.playback_authority_ready THEN parent.playback_webhook_secret_enc ELSE parent_stream.playback_webhook_secret_enc END,
    NOW(), NOW()
FROM commodore.dvr_recordings AS parent
LEFT JOIN commodore.streams AS parent_stream
  ON parent_stream.id = parent.stream_id AND parent_stream.tenant_id = parent.tenant_id
WHERE parent.tenant_id = sqlc.arg(tenant_id)::uuid
  AND parent.dvr_hash = sqlc.arg(dvr_hash)
ON CONFLICT (vod_hash) DO UPDATE SET
    user_id = EXCLUDED.user_id,
    stream_id = EXCLUDED.stream_id,
    internal_name = EXCLUDED.internal_name,
    playback_id = EXCLUDED.playback_id,
    title = EXCLUDED.title,
    description = EXCLUDED.description,
    filename = EXCLUDED.filename,
    content_type = EXCLUDED.content_type,
    origin_cluster_id = EXCLUDED.origin_cluster_id,
    storage_cluster_id = EXCLUDED.storage_cluster_id,
    library_visible = false,
    origin_type = 'dvr_chapter',
    origin_id = EXCLUDED.origin_id,
    requires_auth = EXCLUDED.requires_auth,
    playback_policy = EXCLUDED.playback_policy,
    playback_webhook_secret_enc = EXCLUDED.playback_webhook_secret_enc,
    updated_at = NOW()
WHERE commodore.vod_assets.tenant_id = EXCLUDED.tenant_id;

-- name: ResolveChapterByPlaybackID :one
SELECT cp.chapter_id, cp.tenant_id::text AS tenant_id, cp.artifact_hash
FROM commodore.dvr_chapter_playback cp
JOIN commodore.vod_assets v
  ON v.tenant_id = cp.tenant_id AND v.vod_hash = cp.artifact_hash
WHERE lower(cp.playback_id::text) = lower(sqlc.arg(playback_id)::text);
