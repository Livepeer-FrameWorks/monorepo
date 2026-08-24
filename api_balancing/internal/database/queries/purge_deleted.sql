-- name: MarkFailedArtifactsDeleted :execrows
UPDATE foghorn.artifacts a
SET status = 'deleted'
WHERE a.artifact_type IN ('clip', 'dvr', 'vod')
  AND a.status = 'failed'
  AND a.updated_at < NOW() - CAST(sqlc.arg(retention_interval) AS text)::interval
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.artifact_nodes AS an
      WHERE an.artifact_hash = a.artifact_hash
        AND an.is_orphaned = false
  );

-- name: ListPurgeableArtifacts :many
SELECT a.artifact_hash, a.artifact_type, a.tenant_id::text,
       COALESCE(a.stream_internal_name, '')::text AS stream_internal_name,
       COALESCE(a.format, '')::text AS format,
       COALESCE(a.storage_cluster_id, '')::text AS storage_cluster_id,
       COALESCE(a.origin_cluster_id, '')::text AS origin_cluster_id,
       COALESCE(v.s3_key, '')::text AS vod_s3_key,
       COALESCE(a.s3_url, '')::text AS s3_url,
       COALESCE(a.sync_object_key, '')::text AS sync_object_key,
       COALESCE(a.active_object_key, '')::text AS active_object_key,
       COALESCE(a.active_dtsh_key, '')::text AS active_dtsh_key,
       COALESCE(a.durable_backend_local, false) AS durable_backend_local,
       COALESCE(a.backend_id, '')::text AS backend_id,
       a.status
FROM foghorn.artifacts a
LEFT JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
WHERE a.artifact_type IN ('clip', 'dvr', 'vod')
  AND a.status = 'deleted'
  AND a.updated_at < NOW() - CAST(sqlc.arg(retention_interval) AS text)::interval
  AND a.catalog_revision > 0
  AND a.catalog_synced_rev >= a.catalog_revision
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.artifact_nodes AS an
      WHERE an.artifact_hash = a.artifact_hash
        AND an.is_orphaned = false
  )
ORDER BY a.updated_at
LIMIT 1000;

-- name: ListPurgeableLocalArtifacts :many
SELECT a.artifact_hash, a.artifact_type, a.tenant_id::text,
       COALESCE(a.stream_internal_name, '')::text AS stream_internal_name,
       COALESCE(a.format, '')::text AS format,
       COALESCE(a.storage_cluster_id, '')::text AS storage_cluster_id,
       COALESCE(a.origin_cluster_id, '')::text AS origin_cluster_id,
       COALESCE(v.s3_key, '')::text AS vod_s3_key,
       COALESCE(a.s3_url, '')::text AS s3_url,
       COALESCE(a.sync_object_key, '')::text AS sync_object_key,
       COALESCE(a.active_object_key, '')::text AS active_object_key,
       COALESCE(a.active_dtsh_key, '')::text AS active_dtsh_key,
       COALESCE(a.durable_backend_local, false) AS durable_backend_local,
       COALESCE(a.backend_id, '')::text AS backend_id,
       a.status
FROM foghorn.artifacts a
LEFT JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
WHERE a.artifact_type IN ('clip', 'dvr', 'vod')
  AND a.status = 'deleted'
  AND a.updated_at < NOW() - CAST(sqlc.arg(retention_interval) AS text)::interval
  AND a.catalog_revision > 0
  AND a.catalog_synced_rev >= a.catalog_revision
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.artifact_nodes AS an
      WHERE an.artifact_hash = a.artifact_hash
        AND an.is_orphaned = false
  )
  AND a.backend_id = sqlc.arg(backend_id)::text
ORDER BY a.updated_at
LIMIT 1000;

-- name: DeletePurgedArtifact :exec
DELETE FROM foghorn.artifacts
WHERE artifact_hash = $1 AND tenant_id::text = $2;

-- name: ListStaleUploadingVODs :many
SELECT a.artifact_hash, a.tenant_id::text,
       COALESCE(a.storage_cluster_id, '')::text AS storage_cluster_id,
       COALESCE(a.origin_cluster_id, '')::text AS origin_cluster_id,
       COALESCE(a.backend_id, '')::text AS backend_id
FROM foghorn.artifacts a
JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
WHERE a.status = 'uploading'
  AND v.upload_expires_at IS NOT NULL
  AND v.upload_expires_at < NOW() - INTERVAL '1 hour'
LIMIT 1000;

-- name: ClaimStaleUploadingVOD :execrows
UPDATE foghorn.artifacts
SET status = 'aborting', updated_at = NOW()
WHERE artifact_hash = $1 AND tenant_id = $2 AND status = 'uploading';

-- name: PurgeStaleArtifactNodes :execrows
DELETE FROM foghorn.artifact_nodes
WHERE is_orphaned = true
  AND last_seen_at < NOW() - INTERVAL '7 days';
