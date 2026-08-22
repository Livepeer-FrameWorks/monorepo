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
RETURNING playback_id;

-- name: UpsertChapterVODAsset :exec
INSERT INTO commodore.vod_assets (
    id, tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id,
    title, description, filename, content_type,
    origin_cluster_id, storage_cluster_id,
    library_visible, origin_type, origin_id,
    created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id)::uuid, sqlc.arg(user_id)::uuid, NULLIF(sqlc.arg(stream_id)::text, '')::uuid,
    sqlc.arg(vod_hash), sqlc.arg(internal_name), sqlc.arg(playback_id),
    sqlc.arg(title), NULLIF(sqlc.arg(description)::text, ''), sqlc.arg(filename), sqlc.arg(content_type),
    NULLIF(sqlc.arg(origin_cluster_id)::text, ''), NULLIF(sqlc.arg(storage_cluster_id)::text, ''),
    false, 'dvr_chapter', sqlc.arg(origin_id), NOW(), NOW()
)
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
    updated_at = NOW();

-- name: ResolveChapterByPlaybackID :one
SELECT cp.chapter_id, cp.tenant_id::text AS tenant_id, cp.artifact_hash
FROM commodore.dvr_chapter_playback cp
JOIN commodore.vod_assets v
  ON v.tenant_id = cp.tenant_id AND v.vod_hash = cp.artifact_hash
WHERE lower(cp.playback_id::text) = lower(sqlc.arg(playback_id)::text);
