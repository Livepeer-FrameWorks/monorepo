-- name: AdoptRemoteArtifact :execrows
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, internal_name, stream_internal_name, format,
    status, storage_location, sync_status, origin_cluster_id, storage_cluster_id, federated_pointer
) SELECT
    sqlc.arg(artifact_hash)::varchar(32), sqlc.arg(artifact_type)::varchar(10), sqlc.arg(tenant_id)::uuid,
    sqlc.arg(internal_name)::text, sqlc.arg(stream_internal_name)::text, sqlc.arg(format)::text,
    'ready', 's3', sqlc.arg(sync_status)::text, sqlc.arg(origin_cluster_id)::text, sqlc.narg(storage_cluster_id), true
WHERE NOT EXISTS (
    SELECT 1
    FROM foghorn.media_object_authority_projection AS authority
    WHERE authority.tenant_id = sqlc.arg(tenant_id)::uuid
      AND authority.artifact_hash = sqlc.arg(artifact_hash)::text
      AND authority.lifecycle = 'tombstone'
)
ON CONFLICT (artifact_hash) DO UPDATE SET
	status = 'ready',
	federated_pointer = true,
	storage_location = 's3',
    sync_status = CASE WHEN EXCLUDED.sync_status = 'synced' THEN 'synced' ELSE foghorn.artifacts.sync_status END,
    internal_name = CASE WHEN COALESCE(foghorn.artifacts.internal_name, '') = '' AND EXCLUDED.internal_name <> '' THEN EXCLUDED.internal_name ELSE foghorn.artifacts.internal_name END,
    stream_internal_name = CASE WHEN COALESCE(foghorn.artifacts.stream_internal_name, '') = '' AND EXCLUDED.stream_internal_name <> '' THEN EXCLUDED.stream_internal_name ELSE foghorn.artifacts.stream_internal_name END,
    format = CASE WHEN COALESCE(foghorn.artifacts.format, '') = '' AND EXCLUDED.format <> '' THEN EXCLUDED.format ELSE foghorn.artifacts.format END,
    origin_cluster_id = CASE WHEN COALESCE(foghorn.artifacts.origin_cluster_id, '') = '' THEN EXCLUDED.origin_cluster_id ELSE foghorn.artifacts.origin_cluster_id END,
    storage_cluster_id = CASE WHEN COALESCE(foghorn.artifacts.storage_cluster_id, '') = '' AND EXCLUDED.storage_cluster_id IS NOT NULL THEN EXCLUDED.storage_cluster_id ELSE foghorn.artifacts.storage_cluster_id END
WHERE foghorn.artifacts.tenant_id = EXCLUDED.tenant_id
  AND foghorn.artifacts.federated_pointer = true
  -- A terminal pointer is owned by the ordered purge saga. Do not resurrect
  -- it between the derivative sweep and guarded hard delete; a subsequent
  -- resolve may insert a fresh pointer after the old row is gone.
  AND foghorn.artifacts.status <> 'deleted'
  AND NOT EXISTS (
      SELECT 1
      FROM foghorn.media_object_authority_projection AS authority
      WHERE authority.tenant_id = EXCLUDED.tenant_id
        AND authority.artifact_hash = EXCLUDED.artifact_hash
        AND authority.lifecycle = 'tombstone'
  );

-- Adopted rows are a local routing pointer, not this cell's authoritative
-- catalog source. Mark their local revision covered so deletion does not wait
-- for an origin-only catalog projector that will intentionally never select it.
-- name: SettleFederatedArtifactCatalogRevision :execrows
UPDATE foghorn.artifacts
SET catalog_synced_rev = catalog_revision
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id::text = sqlc.arg(tenant_id)
  AND federated_pointer = true
  AND catalog_synced_rev < catalog_revision;

-- name: TombstoneFederatedArtifact :execrows
UPDATE foghorn.artifacts
SET status = 'deleted', federated_purge_eligible_at = NOW(), updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND federated_pointer = true
  -- A token owner may already have performed an unknown subset of the
  -- out-of-transaction byte cleanup. The row is already terminal; do not
  -- reset its discovery clock or mutate saga-owned state.
  AND federated_purge_token IS NULL;

-- name: BackfillFederatedArtifactLifecycleBatch :one
WITH batch AS (
    SELECT artifact_hash
    FROM foghorn.artifacts
    WHERE status = 'active'
      AND COALESCE(origin_cluster_id, '') <> ''
      AND NOT EXISTS (
          SELECT 1
          FROM foghorn.artifact_nodes invalid_origin
          WHERE invalid_origin.artifact_hash = foghorn.artifacts.artifact_hash
            AND invalid_origin.role = 'origin'
      )
    ORDER BY artifact_hash
    LIMIT sqlc.arg(batch_size)::integer
    FOR UPDATE SKIP LOCKED
), updated AS (
    UPDATE foghorn.artifacts AS artifact
	SET federated_pointer = true,
		status = 'ready',
		catalog_synced_rev = artifact.catalog_revision,
		-- artifacts.updated_at is a nullable legacy timestamp without time zone.
		-- PostgreSQL interprets it in the current session zone, matching the zone
		-- used when the naive value was written. An absent value has no recoverable
		-- age and starts its retention clock at conversion.
		federated_purge_eligible_at = COALESCE(artifact.updated_at, NOW()),
		updated_at = NOW()
    FROM batch
    WHERE artifact.artifact_hash = batch.artifact_hash
    RETURNING artifact.artifact_hash
)
SELECT (SELECT COUNT(*) FROM batch)::bigint AS scanned_count,
       (SELECT COUNT(*) FROM updated)::bigint AS changed_count;

-- name: CountLegacyFederatedArtifactPointers :one
SELECT COUNT(*)::bigint
FROM foghorn.artifacts
WHERE status = 'active'
  AND COALESCE(origin_cluster_id, '') <> ''
  AND NOT EXISTS (
      SELECT 1
      FROM foghorn.artifact_nodes invalid_origin
      WHERE invalid_origin.artifact_hash = foghorn.artifacts.artifact_hash
        AND invalid_origin.role = 'origin'
  );

-- name: BackfillFederatedPointerPurgeEligibilityBatch :one
WITH batch AS (
    SELECT artifact_hash
    FROM foghorn.artifacts
    WHERE federated_pointer = true
      AND updated_at IS NOT NULL
      AND federated_purge_eligible_at > updated_at
    ORDER BY artifact_hash
    LIMIT sqlc.arg(batch_size)::integer
    FOR UPDATE SKIP LOCKED
), updated AS (
    UPDATE foghorn.artifacts AS artifact
    SET federated_purge_eligible_at = artifact.updated_at
    FROM batch
    WHERE artifact.artifact_hash = batch.artifact_hash
    RETURNING artifact.artifact_hash
)
SELECT (SELECT COUNT(*) FROM batch)::bigint AS scanned_count,
       (SELECT COUNT(*) FROM updated)::bigint AS changed_count;

-- name: CountFederatedPointersWithUnnormalizedPurgeEligibility :one
SELECT COUNT(*)::bigint
FROM foghorn.artifacts
WHERE federated_pointer = true
  AND updated_at IS NOT NULL
  AND federated_purge_eligible_at > updated_at;
