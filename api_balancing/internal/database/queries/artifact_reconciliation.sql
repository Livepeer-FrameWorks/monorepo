-- name: TryArtifactReconcilerLock :one
SELECT pg_try_advisory_lock(hashtext('artifact_reconciler'));

-- name: UnlockArtifactReconciler :exec
SELECT pg_advisory_unlock(hashtext('artifact_reconciler'));

-- name: TryArtifactBillingAttributionLock :one
SELECT pg_try_advisory_lock(hashtext('artifact_billing_attribution'));

-- name: UnlockArtifactBillingAttribution :exec
SELECT pg_advisory_unlock(hashtext('artifact_billing_attribution'));

-- name: TryFreezePublicationLedgerLock :one
SELECT pg_try_advisory_lock(hashtext('freeze_publication_ledger'));

-- name: UnlockFreezePublicationLedger :exec
SELECT pg_advisory_unlock(hashtext('freeze_publication_ledger'));

-- name: GetActiveObjectKeyBackfillCursor :one
SELECT last_hash FROM foghorn.active_object_key_backfill_cursor WHERE id = true;

-- name: ListActiveObjectKeyBackfillRows :many
SELECT artifact_hash, tenant_id::text, s3_url
FROM foghorn.artifacts
WHERE sync_status = 'synced'
  AND artifact_type IN ('clip', 'vod')
  AND COALESCE(active_object_key, '') = ''
  AND COALESCE(s3_url, '') <> ''
  AND durable_backend_local = true
  AND tenant_id IS NOT NULL
  AND artifact_hash > $1
ORDER BY artifact_hash
LIMIT $2;

-- name: SetLegacyActiveObjectKey :execrows
UPDATE foghorn.artifacts
SET active_object_key = $3
WHERE artifact_hash = $1
  AND tenant_id::text = $2
  AND COALESCE(active_object_key, '') = '';

-- name: SetActiveObjectKeyBackfillCursor :exec
UPDATE foghorn.active_object_key_backfill_cursor SET last_hash = $1 WHERE id = true;

-- name: GetFreezePublicationLedgerCursor :one
SELECT last_key FROM foghorn.freeze_publication_ledger_cursor WHERE id = true;

-- name: ListStaleFreezePublicationLedgerRows :many
SELECT object_key, artifact_hash, tenant_id, request_id, guarded,
       COALESCE(backend_id, '')
FROM foghorn.freeze_publication_ledger
WHERE created_at < NOW() - INTERVAL '15 minutes'
  AND object_key > $1
ORDER BY object_key
LIMIT $2;

-- name: GetArtifactPublicationPointers :one
SELECT COALESCE(sync_request_id, '')::text AS sync_request_id,
       COALESCE(dtsh_sync_request_id, '')::text AS dtsh_sync_request_id,
       COALESCE(active_object_key, '')::text AS active_object_key,
       COALESCE(active_dtsh_key, '')::text AS active_dtsh_key
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND tenant_id::text = $2;

-- name: DeleteFreezePublicationLedgerRow :exec
DELETE FROM foghorn.freeze_publication_ledger WHERE object_key = $1;

-- name: EnqueueStagingCleanup :exec
INSERT INTO foghorn.staging_cleanup_queue (object_key, backend_id)
VALUES (sqlc.arg(object_key), NULLIF(sqlc.arg(backend_id)::text, ''))
ON CONFLICT (object_key) DO NOTHING;

-- name: SetFreezePublicationLedgerCursor :exec
UPDATE foghorn.freeze_publication_ledger_cursor SET last_key = $1 WHERE id = true;

-- name: BackfillOriginCluster :execrows
UPDATE foghorn.artifacts
SET origin_cluster_id = $2
WHERE artifact_hash IN (
    SELECT a.artifact_hash
    FROM foghorn.artifacts AS a
    WHERE COALESCE(a.origin_cluster_id, '') = ''
      AND (a.status = 'deleted' OR EXISTS (
          SELECT 1 FROM foghorn.artifact_nodes AS n
          WHERE n.artifact_hash = a.artifact_hash AND n.role = 'origin'
      ))
    ORDER BY a.artifact_hash
    LIMIT $1
);

-- name: BackfillCatalogRevisions :execrows
UPDATE foghorn.artifacts
SET catalog_revision = nextval('foghorn.artifact_catalog_revision_seq')
WHERE artifact_hash IN (
    SELECT candidate.artifact_hash
    FROM foghorn.artifacts AS candidate
    WHERE candidate.catalog_revision = 0
      AND candidate.origin_cluster_id = $2
    ORDER BY candidate.artifact_hash
    LIMIT $1
);

-- name: ListArtifactsForCatalogProjection :many
SELECT artifact_hash, artifact_type, tenant_id::text AS tenant_id,
       storage_cluster_id, has_thumbnails, size_bytes,
       COALESCE(duration_ms, duration_seconds * 1000) AS duration_ms,
       tracks, sync_status, dtsh_synced, storage_location,
       catalog_revision, status,
       retention_until,
       error_message, thumbnail_serving_cluster_id
FROM foghorn.artifacts
WHERE tenant_id IS NOT NULL
  AND origin_cluster_id = $2
  AND catalog_revision > catalog_synced_rev
  AND catalog_revision > catalog_quarantined_rev
  AND (catalog_next_attempt_at IS NULL OR catalog_next_attempt_at <= NOW())
ORDER BY catalog_synced_rev ASC, catalog_revision ASC
LIMIT $1;

-- name: AdvanceCatalogWatermark :exec
UPDATE foghorn.artifacts
SET catalog_synced_rev = $1, catalog_quarantine_error = NULL,
    catalog_next_attempt_at = NULL, catalog_projection_attempts = 0
WHERE artifact_hash = $2 AND catalog_synced_rev < $1;

-- name: BackoffCatalogProjection :exec
UPDATE foghorn.artifacts
SET catalog_projection_attempts = catalog_projection_attempts + 1,
    catalog_next_attempt_at = NOW() + make_interval(secs =>
        LEAST(sqlc.arg(base_seconds)::float8 * power(2, LEAST(catalog_projection_attempts + 1, 6)), 3600))
WHERE artifact_hash = sqlc.arg(artifact_hash);

-- name: QuarantineCatalogProjection :exec
UPDATE foghorn.artifacts
SET catalog_quarantined_rev = $1, catalog_quarantine_error = $3
WHERE artifact_hash = $2 AND catalog_quarantined_rev < $1;

-- name: ListFailedArtifactsForFreezeRetry :many
SELECT a.artifact_hash, a.artifact_type,
       COALESCE(a.stream_internal_name, '')::text AS stream_internal_name,
       a.tenant_id::text AS tenant_id, a.format, an.node_id, an.file_path
FROM foghorn.artifacts AS a
JOIN LATERAL (
    SELECT node_id, file_path
    FROM foghorn.artifact_nodes
    WHERE artifact_hash = a.artifact_hash
      AND is_orphaned = false
      AND is_complete = true
    ORDER BY node_id
    LIMIT 1
) AS an ON true
WHERE a.sync_status = 'failed'
  AND a.artifact_type != 'dvr'
  AND a.updated_at < NOW() - LEAST(
      INTERVAL '5 minutes' * (1 << LEAST(a.failure_count, 4)),
      INTERVAL '1 hour'
  ) * (0.8 + 0.4 * random())
  AND a.status = 'ready'
ORDER BY a.updated_at ASC
LIMIT $1;

-- name: ListPendingArtifactsForFreeze :many
SELECT a.artifact_hash, a.artifact_type,
       COALESCE(a.stream_internal_name, '')::text AS stream_internal_name,
       a.tenant_id::text AS tenant_id, a.format, an.node_id, an.file_path
FROM foghorn.artifacts AS a
JOIN LATERAL (
    SELECT node_id, file_path
    FROM foghorn.artifact_nodes
    WHERE artifact_hash = a.artifact_hash
      AND is_orphaned = false
      AND is_complete = true
    ORDER BY node_id
    LIMIT 1
) AS an ON true
WHERE a.sync_status = 'pending'
  AND a.artifact_type != 'dvr'
  AND a.storage_location = 'local'
  AND a.status = 'ready'
ORDER BY a.created_at ASC
LIMIT $1;

-- name: ExistingArtifactHashes :many
SELECT artifact_hash FROM foghorn.artifacts WHERE artifact_hash = ANY($1::text[]);

-- name: InsertDiscoveredArtifact :exec
INSERT INTO foghorn.artifacts
    (artifact_hash, artifact_type, stream_internal_name, tenant_id,
     format, storage_location, sync_status, created_at, updated_at)
VALUES (sqlc.arg(artifact_hash), sqlc.arg(artifact_type), sqlc.narg(stream_internal_name), sqlc.arg(tenant_id), NULLIF(sqlc.arg(format)::text, ''), 'local', 'pending', NOW(), NOW())
ON CONFLICT (artifact_hash) DO NOTHING;

-- name: UpsertDiscoveredArtifactNode :exec
INSERT INTO foghorn.artifact_nodes
    (artifact_hash, node_id, file_path, size_bytes, last_seen_at, is_orphaned, cached_at)
VALUES ($1, $2, $3, $4, NOW(), false, NOW())
ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
    file_path = EXCLUDED.file_path,
    size_bytes = EXCLUDED.size_bytes,
    last_seen_at = NOW(),
    is_orphaned = false;

-- name: RevertFailedFreezeDispatch :execrows
UPDATE foghorn.artifacts
SET sync_status = 'failed', storage_location = 'local',
    sync_request_id = NULL, sync_node_id = NULL, updated_at = NOW()
WHERE artifact_hash = $1
  AND sync_status = 'in_progress'
  AND status = 'ready'
  AND sync_request_id = $2
  AND sync_node_id = $3
  AND tenant_id::text = $4;
