-- name: GetLiveClipCatalogStateForUpdate :one
SELECT origin_cluster_id, catalog_revision
FROM commodore.clips
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND clip_hash = sqlc.arg(asset_key)
FOR UPDATE;

-- name: GetLiveDVRCatalogStateForUpdate :one
SELECT origin_cluster_id, catalog_revision
FROM commodore.dvr_recordings
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND dvr_hash = sqlc.arg(asset_key)
FOR UPDATE;

-- name: GetLiveVODCatalogStateForUpdate :one
SELECT origin_cluster_id, catalog_revision
FROM commodore.vod_assets
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND vod_hash = sqlc.arg(asset_key)
FOR UPDATE;

-- name: UpsertArtifactCatalogTombstone :one
INSERT INTO commodore.artifact_catalog_tombstones AS t
    (tenant_id, kind, artifact_hash, origin_cluster_id, deletion_revision, deleted_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(kind), sqlc.arg(asset_key),
        sqlc.arg(origin_cluster_id), sqlc.arg(deletion_revision), NOW())
ON CONFLICT (tenant_id, kind, artifact_hash) DO UPDATE
SET deletion_revision = GREATEST(t.deletion_revision, EXCLUDED.deletion_revision),
    deleted_at = CASE
        WHEN EXCLUDED.deletion_revision > t.deletion_revision THEN NOW()
        ELSE t.deleted_at
    END
WHERE t.origin_cluster_id = EXCLUDED.origin_cluster_id
RETURNING deletion_revision;

-- name: DeleteDVRChapterPlaybackByArtifact :exec
DELETE FROM commodore.dvr_chapter_playback
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND artifact_hash = sqlc.arg(asset_key);

-- name: GetArtifactCatalogTombstoneOrigin :one
SELECT origin_cluster_id
FROM commodore.artifact_catalog_tombstones
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND kind = sqlc.arg(kind)
  AND artifact_hash = sqlc.arg(asset_key);

-- name: GetArtifactCatalogTombstoneForUpdate :one
SELECT deletion_revision, origin_cluster_id
FROM commodore.artifact_catalog_tombstones
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND kind = sqlc.arg(kind)
  AND artifact_hash = sqlc.arg(asset_key)
FOR UPDATE;

-- name: ClearArtifactCatalogTombstone :exec
DELETE FROM commodore.artifact_catalog_tombstones
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND kind = sqlc.arg(kind)
  AND artifact_hash = sqlc.arg(asset_key);

-- name: ApplyClipCatalogSnapshot :one
UPDATE commodore.clips
SET size_bytes = sqlc.narg(size_bytes),
    duration = sqlc.narg(duration_ms),
    tracks = CASE WHEN sqlc.arg(tracks_present)::boolean THEN sqlc.narg(tracks)::text::jsonb ELSE tracks END,
    sync_status = sqlc.narg(sync_status),
    is_synced = sqlc.narg(is_synced),
    is_finalized = sqlc.narg(is_finalized),
    storage_location = sqlc.narg(storage_location),
    storage_cluster_id = sqlc.narg(storage_cluster_id),
    has_thumbnails = COALESCE(sqlc.narg(has_thumbnails), has_thumbnails),
    lifecycle_status = COALESCE(sqlc.narg(lifecycle_status), lifecycle_status),
    origin_cluster_id = COALESCE(origin_cluster_id, sqlc.arg(origin_cluster_id)),
    retention_until = to_timestamp(sqlc.narg(retention_until_unix)::bigint),
    error_message = sqlc.narg(error_message),
    thumbnail_serving_cluster_id = COALESCE(sqlc.narg(thumbnail_serving_cluster_id)::varchar, thumbnail_serving_cluster_id),
    catalog_revision = sqlc.arg(source_revision)::bigint,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND clip_hash = sqlc.arg(asset_key)
  AND (catalog_revision IS NULL OR catalog_revision < sqlc.arg(source_revision)::bigint
       OR (catalog_revision = sqlc.arg(source_revision)::bigint
           AND thumbnail_serving_cluster_id IS NULL
           AND sqlc.narg(thumbnail_serving_cluster_id)::varchar IS NOT NULL))
  AND (origin_cluster_id IS NULL OR origin_cluster_id = sqlc.arg(origin_cluster_id))
RETURNING catalog_revision, thumbnail_serving_cluster_id;

-- name: ApplyDVRCatalogSnapshot :one
UPDATE commodore.dvr_recordings
SET size_bytes = sqlc.narg(size_bytes), duration = sqlc.narg(duration_ms),
    tracks = CASE WHEN sqlc.arg(tracks_present)::boolean THEN sqlc.narg(tracks)::text::jsonb ELSE tracks END,
    sync_status = sqlc.narg(sync_status), is_synced = sqlc.narg(is_synced), is_finalized = sqlc.narg(is_finalized),
    storage_location = sqlc.narg(storage_location), storage_cluster_id = sqlc.narg(storage_cluster_id),
    has_thumbnails = COALESCE(sqlc.narg(has_thumbnails), has_thumbnails),
    lifecycle_status = COALESCE(sqlc.narg(lifecycle_status), lifecycle_status),
    origin_cluster_id = COALESCE(origin_cluster_id, sqlc.arg(origin_cluster_id)),
    retention_until = to_timestamp(sqlc.narg(retention_until_unix)::bigint),
    error_message = sqlc.narg(error_message),
    thumbnail_serving_cluster_id = COALESCE(sqlc.narg(thumbnail_serving_cluster_id)::varchar, thumbnail_serving_cluster_id),
    catalog_revision = sqlc.arg(source_revision)::bigint, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND dvr_hash = sqlc.arg(asset_key)
  AND (catalog_revision IS NULL OR catalog_revision < sqlc.arg(source_revision)::bigint
       OR (catalog_revision = sqlc.arg(source_revision)::bigint
           AND thumbnail_serving_cluster_id IS NULL
           AND sqlc.narg(thumbnail_serving_cluster_id)::varchar IS NOT NULL))
  AND (origin_cluster_id IS NULL OR origin_cluster_id = sqlc.arg(origin_cluster_id))
RETURNING catalog_revision, thumbnail_serving_cluster_id;

-- name: ApplyVODCatalogSnapshot :one
UPDATE commodore.vod_assets
SET size_bytes = sqlc.narg(size_bytes), duration = sqlc.narg(duration_ms),
    tracks = CASE WHEN sqlc.arg(tracks_present)::boolean THEN sqlc.narg(tracks)::text::jsonb ELSE tracks END,
    sync_status = sqlc.narg(sync_status), is_synced = sqlc.narg(is_synced), is_finalized = sqlc.narg(is_finalized),
    storage_location = sqlc.narg(storage_location), storage_cluster_id = sqlc.narg(storage_cluster_id),
    has_thumbnails = COALESCE(sqlc.narg(has_thumbnails), has_thumbnails),
    lifecycle_status = COALESCE(sqlc.narg(lifecycle_status), lifecycle_status),
    origin_cluster_id = COALESCE(origin_cluster_id, sqlc.arg(origin_cluster_id)),
    retention_until = to_timestamp(sqlc.narg(retention_until_unix)::bigint),
    error_message = sqlc.narg(error_message),
    thumbnail_serving_cluster_id = COALESCE(sqlc.narg(thumbnail_serving_cluster_id)::varchar, thumbnail_serving_cluster_id),
    catalog_revision = sqlc.arg(source_revision)::bigint, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND vod_hash = sqlc.arg(asset_key)
  AND (catalog_revision IS NULL OR catalog_revision < sqlc.arg(source_revision)::bigint
       OR (catalog_revision = sqlc.arg(source_revision)::bigint
           AND thumbnail_serving_cluster_id IS NULL
           AND sqlc.narg(thumbnail_serving_cluster_id)::varchar IS NOT NULL))
  AND (origin_cluster_id IS NULL OR origin_cluster_id = sqlc.arg(origin_cluster_id))
RETURNING catalog_revision, thumbnail_serving_cluster_id;

-- name: GetClipCatalogState :one
SELECT origin_cluster_id, catalog_revision, thumbnail_serving_cluster_id
FROM commodore.clips
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND clip_hash = sqlc.arg(asset_key);

-- name: GetDVRCatalogState :one
SELECT origin_cluster_id, catalog_revision, thumbnail_serving_cluster_id
FROM commodore.dvr_recordings
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND dvr_hash = sqlc.arg(asset_key);

-- name: GetVODCatalogState :one
SELECT origin_cluster_id, catalog_revision, thumbnail_serving_cluster_id
FROM commodore.vod_assets
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND vod_hash = sqlc.arg(asset_key);
