-- name: GetBillingAttributionCursor :one
SELECT last_tenant, last_cluster
FROM foghorn.billing_attribution_cursor
WHERE id = true;

-- name: ListUnattributedStoragePairs :many
SELECT p.tenant, p.cluster
FROM (
    SELECT DISTINCT tenant_id::text AS tenant,
           COALESCE(NULLIF(storage_cluster_id, ''), NULLIF(origin_cluster_id, ''), '')::text AS cluster
    FROM foghorn.artifacts
    WHERE sync_status = 'synced'
      AND durable_backend_local = false
      AND federated_pointer = false
      AND tenant_id IS NOT NULL
) p
WHERE (p.tenant, p.cluster) > (sqlc.arg(last_tenant)::text, sqlc.arg(last_cluster)::text)
ORDER BY p.tenant, p.cluster
LIMIT sqlc.arg(page_limit);

-- name: MarkStoragePairLocallyAttributed :execrows
UPDATE foghorn.artifacts
SET durable_backend_local = true
WHERE sync_status = 'synced'
  AND durable_backend_local = false
  AND federated_pointer = false
  AND tenant_id::text = sqlc.arg(tenant_id)
  AND COALESCE(NULLIF(storage_cluster_id, ''), NULLIF(origin_cluster_id, ''), '') = sqlc.arg(cluster_id);

-- name: SetBillingAttributionCursor :exec
UPDATE foghorn.billing_attribution_cursor
SET last_tenant = sqlc.arg(last_tenant), last_cluster = sqlc.arg(last_cluster)
WHERE id = true;

-- name: ListColdStorageUsage :many
SELECT tenant_id::text AS tenant_id, artifact_type,
       COALESCE(SUM(billed_bytes), 0)::bigint AS total_bytes,
       SUM(file_count)::bigint AS file_count
FROM (
    SELECT a.tenant_id, a.artifact_type,
           CASE WHEN a.sync_status = 'synced' AND NOT (a.artifact_type = 'vod' AND a.status IN ('queued', 'processing') AND COALESCE(a.active_object_key = v.original_s3_key, false)) THEN COALESCE(a.size_bytes, 0) ELSE 0 END
             + COALESCE(v.original_size_bytes, 0) AS billed_bytes,
           CASE WHEN a.sync_status = 'synced' AND NOT (a.artifact_type = 'vod' AND a.status IN ('queued', 'processing') AND COALESCE(a.active_object_key = v.original_s3_key, false)) THEN 1 ELSE 0 END
             + CASE WHEN v.original_size_bytes IS NOT NULL THEN 1 ELSE 0 END AS file_count
    FROM foghorn.artifacts a
    LEFT JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
    WHERE a.tenant_id IS NOT NULL AND a.federated_pointer = false
      AND a.status <> 'deleted'
      AND (a.artifact_type <> 'vod' OR a.status NOT IN ('failed', 'expired', 'aborted'))
      AND a.durable_backend_local = true
      AND (a.sync_status = 'synced' OR v.original_size_bytes IS NOT NULL)
) usage
GROUP BY tenant_id, artifact_type;
