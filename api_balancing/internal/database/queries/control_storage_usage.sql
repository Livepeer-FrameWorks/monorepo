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
  AND tenant_id::text = sqlc.arg(tenant_id)
  AND COALESCE(NULLIF(storage_cluster_id, ''), NULLIF(origin_cluster_id, ''), '') = sqlc.arg(cluster_id);

-- name: SetBillingAttributionCursor :exec
UPDATE foghorn.billing_attribution_cursor
SET last_tenant = sqlc.arg(last_tenant), last_cluster = sqlc.arg(last_cluster)
WHERE id = true;

-- name: ListColdStorageUsage :many
SELECT tenant_id::text AS tenant_id, artifact_type,
       COALESCE(SUM(size_bytes), 0)::bigint AS total_bytes,
       COUNT(*)::bigint AS file_count
FROM foghorn.artifacts
WHERE tenant_id IS NOT NULL
  AND status != 'deleted'
  AND sync_status = 'synced'
  AND durable_backend_local = true
GROUP BY tenant_id, artifact_type;
