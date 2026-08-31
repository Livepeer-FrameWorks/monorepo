-- name: MarkFailedArtifactsDeleted :execrows
UPDATE foghorn.artifacts a
SET status = 'deleted'
WHERE a.artifact_type IN ('clip', 'dvr', 'vod', 'chapter')
  AND a.federated_pointer = false
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
WHERE a.artifact_type IN ('clip', 'dvr', 'vod', 'chapter')
  AND a.federated_pointer = false
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
WHERE a.artifact_type IN ('clip', 'dvr', 'vod', 'chapter')
  AND a.federated_pointer = false
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

-- A signed tombstone is the resurrection/version fence, but the pointer row is
-- retained until its cell-local derivatives have been fenced and swept.
-- name: ListTombstonedFederatedArtifactPointersForPurge :many
SELECT artifact.artifact_hash, artifact.tenant_id::text,
       COALESCE(artifact.backend_id, '')::text AS backend_id
FROM foghorn.artifacts AS artifact
JOIN foghorn.media_object_authority_projection AS authority
  ON authority.tenant_id = artifact.tenant_id
 AND authority.artifact_hash = artifact.artifact_hash
 AND authority.lifecycle = 'tombstone'
WHERE artifact.federated_pointer = true
  AND artifact.status = 'deleted'
  AND (
      artifact.federated_purge_token IS NULL
      OR artifact.federated_purge_lease_until <= NOW()
  )
  AND artifact.federated_purge_eligible_at < NOW() - CAST(sqlc.arg(retention_interval) AS text)::interval
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.dvr_chapters chapter
      WHERE chapter.artifact_hash = artifact.artifact_hash
         OR chapter.playback_artifact_hash = artifact.artifact_hash
  )
  AND NOT EXISTS (SELECT 1 FROM foghorn.artifact_nodes node WHERE node.artifact_hash = artifact.artifact_hash AND node.is_orphaned = false)
ORDER BY artifact.federated_purge_eligible_at, artifact.artifact_hash
LIMIT 1000;

-- A pointer whose cache-age threshold has passed may be evicted whenever no
-- unexpired signed authority remains. Its dedicated eligibility clock is not
-- refreshed by metadata, inventory, access, or re-adoption writers; the live
-- predicate remains signed validity.
-- name: ListStaleFederatedArtifactPointersForPurge :many
SELECT artifact.artifact_hash, artifact.tenant_id::text,
       COALESCE(artifact.backend_id, '')::text AS backend_id
FROM foghorn.artifacts AS artifact
WHERE artifact.federated_pointer = true
  AND artifact.status IN ('ready', 'deleted')
  AND (
      artifact.federated_purge_token IS NULL
      OR artifact.federated_purge_lease_until <= NOW()
  )
  AND artifact.federated_purge_eligible_at < NOW() - CAST(sqlc.arg(retention_interval) AS text)::interval
  AND NOT EXISTS (
      SELECT 1
      FROM foghorn.media_object_authority_projection AS authority
      WHERE authority.tenant_id = artifact.tenant_id
        AND authority.artifact_hash = artifact.artifact_hash
        AND authority.lifecycle = 'active'
        AND authority.valid_until > NOW()
  )
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.dvr_chapters chapter
      WHERE chapter.artifact_hash = artifact.artifact_hash
         OR chapter.playback_artifact_hash = artifact.artifact_hash
  )
  AND NOT EXISTS (SELECT 1 FROM foghorn.artifact_nodes node WHERE node.artifact_hash = artifact.artifact_hash AND node.is_orphaned = false)
ORDER BY artifact.federated_purge_eligible_at, artifact.artifact_hash
LIMIT 1000;

-- Recover every expired federated-pointer saga on the short cadence. The
-- current signed-authority state selects the same guarded fence that ordinary
-- discovery would use: tombstone wins, active authority restores only after
-- cleanup settlement, and an authority-free old pointer remains a stale purge.
-- name: ListRecoverableFederatedArtifactPointerPurges :many
SELECT artifact.artifact_hash, artifact.tenant_id::text,
       COALESCE(artifact.backend_id, '')::text AS backend_id,
       CASE
         WHEN EXISTS (
           SELECT 1
           FROM foghorn.media_object_authority_projection AS authority
           WHERE authority.tenant_id = artifact.tenant_id
             AND authority.artifact_hash = artifact.artifact_hash
             AND authority.lifecycle = 'tombstone'
         ) THEN 'tombstone'
         WHEN EXISTS (
           SELECT 1
           FROM foghorn.media_object_authority_projection AS authority
           WHERE authority.tenant_id = artifact.tenant_id
             AND authority.artifact_hash = artifact.artifact_hash
             AND authority.lifecycle = 'active'
             AND authority.valid_until > NOW()
         ) THEN 'interrupted_active'
         ELSE 'stale'
       END::text AS purge_kind
FROM foghorn.artifacts AS artifact
WHERE artifact.federated_pointer = true
  AND artifact.status = 'deleted'
  AND artifact.federated_purge_token IS NOT NULL
  AND artifact.federated_purge_lease_until <= NOW()
  AND (
      EXISTS (
        SELECT 1
        FROM foghorn.media_object_authority_projection AS authority
        WHERE authority.tenant_id = artifact.tenant_id
          AND authority.artifact_hash = artifact.artifact_hash
          AND authority.lifecycle = 'active'
          AND authority.valid_until > NOW()
      )
      OR artifact.federated_purge_eligible_at < NOW() - CAST(sqlc.arg(retention_interval) AS text)::interval
  )
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.dvr_chapters chapter
      WHERE chapter.artifact_hash = artifact.artifact_hash
         OR chapter.playback_artifact_hash = artifact.artifact_hash
  )
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.artifact_nodes node
      WHERE node.artifact_hash = artifact.artifact_hash
        AND node.is_orphaned = false
  )
ORDER BY artifact.federated_purge_lease_until NULLS FIRST, artifact.federated_purge_eligible_at, artifact.artifact_hash
LIMIT 1000;

-- name: FenceTombstonedFederatedArtifactPointerForPurge :execrows
UPDATE foghorn.artifacts AS artifact
SET status = 'deleted',
    federated_purge_token = sqlc.arg(purge_token)::uuid,
    federated_purge_lease_until = NOW() + CAST(sqlc.arg(lease_interval) AS text)::interval
WHERE artifact.artifact_hash = sqlc.arg(artifact_hash)
  AND artifact.tenant_id::text = sqlc.arg(tenant_id)
  AND artifact.federated_pointer = true
  AND artifact.status = 'deleted'
  AND (
      artifact.federated_purge_token IS NULL
      OR artifact.federated_purge_lease_until <= NOW()
  )
  AND artifact.federated_purge_eligible_at < NOW() - CAST(sqlc.arg(retention_interval) AS text)::interval
  AND EXISTS (
      SELECT 1 FROM foghorn.media_object_authority_projection authority
      WHERE authority.tenant_id = artifact.tenant_id
        AND authority.artifact_hash = artifact.artifact_hash
        AND authority.lifecycle = 'tombstone'
  )
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.dvr_chapters chapter
      WHERE chapter.artifact_hash = artifact.artifact_hash
         OR chapter.playback_artifact_hash = artifact.artifact_hash
  )
  AND NOT EXISTS (SELECT 1 FROM foghorn.artifact_nodes node WHERE node.artifact_hash = artifact.artifact_hash AND node.is_orphaned = false);

-- name: FenceStaleFederatedArtifactPointerForPurge :execrows
UPDATE foghorn.artifacts AS artifact
SET status = 'deleted',
    federated_purge_token = sqlc.arg(purge_token)::uuid,
    federated_purge_lease_until = NOW() + CAST(sqlc.arg(lease_interval) AS text)::interval
WHERE artifact.artifact_hash = sqlc.arg(artifact_hash)
  AND artifact.tenant_id::text = sqlc.arg(tenant_id)
  AND artifact.federated_pointer = true
  AND artifact.status IN ('ready', 'deleted')
  AND (
      artifact.federated_purge_token IS NULL
      OR artifact.federated_purge_lease_until <= NOW()
  )
  AND artifact.federated_purge_eligible_at < NOW() - CAST(sqlc.arg(retention_interval) AS text)::interval
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.media_object_authority_projection authority
      WHERE authority.tenant_id = artifact.tenant_id
        AND authority.artifact_hash = artifact.artifact_hash
        AND authority.lifecycle = 'active'
        AND authority.valid_until > NOW()
  )
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.dvr_chapters chapter
      WHERE chapter.artifact_hash = artifact.artifact_hash
         OR chapter.playback_artifact_hash = artifact.artifact_hash
  )
  AND NOT EXISTS (SELECT 1 FROM foghorn.artifact_nodes node WHERE node.artifact_hash = artifact.artifact_hash AND node.is_orphaned = false);

-- name: FenceInterruptedActiveFederatedArtifactPointerPurge :execrows
UPDATE foghorn.artifacts AS artifact
SET federated_purge_token = sqlc.arg(purge_token)::uuid,
    federated_purge_lease_until = NOW() + CAST(sqlc.arg(lease_interval) AS text)::interval
WHERE artifact.artifact_hash = sqlc.arg(artifact_hash)
  AND artifact.tenant_id::text = sqlc.arg(tenant_id)
  AND artifact.federated_pointer = true
  AND artifact.status = 'deleted'
  AND (
      artifact.federated_purge_token IS NULL
      OR artifact.federated_purge_lease_until <= NOW()
  )
  AND EXISTS (
      SELECT 1
      FROM foghorn.media_object_authority_projection authority
      WHERE authority.tenant_id = artifact.tenant_id
        AND authority.artifact_hash = artifact.artifact_hash
        AND authority.lifecycle = 'active'
        AND authority.valid_until > NOW()
  )
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.media_object_authority_projection authority
      WHERE authority.tenant_id = artifact.tenant_id
        AND authority.artifact_hash = artifact.artifact_hash
        AND authority.lifecycle = 'tombstone'
  );

-- Keep the token as evidence that the pointer is inside the ordered cleanup
-- saga, but make the lease immediately reclaimable. Active authority must not
-- restore the pointer around a cleanup whose byte effects are unknown.
-- name: ReleaseFederatedArtifactPointerPurgeClaim :execrows
UPDATE foghorn.artifacts
SET federated_purge_lease_until = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id::text = sqlc.arg(tenant_id)
  AND federated_pointer = true
  AND status = 'deleted'
  AND federated_purge_token = sqlc.arg(purge_token)::uuid;

-- Deterministic evidence gaps need operator/data repair rather than immediate
-- retry churn. Retain token ownership and move only its retry timestamp.
-- name: DeferFederatedArtifactPointerPurgeClaim :execrows
UPDATE foghorn.artifacts
SET federated_purge_lease_until = NOW() + CAST(sqlc.arg(retry_interval) AS text)::interval
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id::text = sqlc.arg(tenant_id)
  AND federated_pointer = true
  AND status = 'deleted'
  AND federated_purge_token = sqlc.arg(purge_token)::uuid;

-- name: RestoreClaimedFederatedArtifactPointerAfterActiveAuthority :execrows
UPDATE foghorn.artifacts AS artifact
SET status = 'ready',
    has_thumbnails = false,
    thumbnail_serving_cluster_id = NULL,
    federated_purge_token = NULL,
    federated_purge_lease_until = NULL,
    federated_purge_eligible_at = NOW(),
    updated_at = NOW()
WHERE artifact.artifact_hash = sqlc.arg(artifact_hash)
  AND artifact.tenant_id::text = sqlc.arg(tenant_id)
  AND artifact.federated_pointer = true
  AND artifact.status = 'deleted'
  AND artifact.federated_purge_token = sqlc.arg(purge_token)::uuid
  AND EXISTS (
      SELECT 1
      FROM foghorn.media_object_authority_projection authority
      WHERE authority.tenant_id = artifact.tenant_id
        AND authority.artifact_hash = artifact.artifact_hash
        AND authority.lifecycle = 'active'
        AND authority.valid_until > NOW()
  )
  AND NOT EXISTS (
      SELECT 1
      FROM foghorn.media_object_authority_projection authority
      WHERE authority.tenant_id = artifact.tenant_id
        AND authority.artifact_hash = artifact.artifact_hash
        AND authority.lifecycle = 'tombstone'
  );

-- name: DeleteFencedFederatedArtifactPointer :execrows
DELETE FROM foghorn.artifacts AS artifact
WHERE artifact.artifact_hash = sqlc.arg(artifact_hash)
  AND artifact.tenant_id::text = sqlc.arg(tenant_id)
  AND artifact.federated_pointer = true
  AND artifact.status = 'deleted'
  AND artifact.federated_purge_token = sqlc.arg(purge_token)::uuid
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.dvr_chapters chapter
      WHERE chapter.artifact_hash = artifact.artifact_hash
         OR chapter.playback_artifact_hash = artifact.artifact_hash
  )
  AND NOT EXISTS (SELECT 1 FROM foghorn.artifact_nodes node WHERE node.artifact_hash = artifact.artifact_hash AND node.is_orphaned = false)
  AND (
      EXISTS (
          SELECT 1 FROM foghorn.media_object_authority_projection authority
          WHERE authority.tenant_id = artifact.tenant_id
            AND authority.artifact_hash = artifact.artifact_hash
            AND authority.lifecycle = 'tombstone'
      ) OR NOT EXISTS (
          SELECT 1 FROM foghorn.media_object_authority_projection authority
          WHERE authority.tenant_id = artifact.tenant_id
            AND authority.artifact_hash = artifact.artifact_hash
            AND authority.lifecycle = 'active'
            AND authority.valid_until > NOW()
      )
  );

-- name: GetFencedFederatedArtifactPointerPurgeState :one
SELECT COALESCE(backend_id, '')::text AS backend_id, has_thumbnails
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id::text = sqlc.arg(tenant_id)
  AND federated_pointer = true
  AND status = 'deleted'
  AND federated_purge_token = sqlc.arg(purge_token)::uuid;

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
