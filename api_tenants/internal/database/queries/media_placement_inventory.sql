-- name: GetMediaPlacementInventory :many
SELECT ic.cluster_id,
       COALESCE(n.node_id, '')::text AS node_id,
       COALESCE(n.status = 'active', false)::boolean AS admission_enabled,
       CURRENT_TIMESTAMP::timestamptz AS observed_at
FROM quartermaster.tenant_cluster_access tca
JOIN quartermaster.infrastructure_clusters ic ON ic.cluster_id = tca.cluster_id
LEFT JOIN quartermaster.infrastructure_nodes n
       ON n.cluster_id = ic.cluster_id AND n.node_type = 'edge'
WHERE tca.tenant_id = sqlc.arg(tenant_id)::uuid
  AND ic.cluster_id = ANY(sqlc.arg(cluster_ids)::text[])
  AND COALESCE(NULLIF(ic.control_cell_id, ''), NULLIF(ic.cell_id, ''), ic.cluster_id) = sqlc.arg(control_cell_id)::text
  AND tca.is_active = true
  AND tca.subscription_status = 'active'
  AND tca.access_source <> 'unknown'
  AND (tca.expires_at IS NULL OR tca.expires_at > NOW())
  AND ic.is_active = true
ORDER BY ic.cluster_id, n.node_id
LIMIT 8193;

-- name: GetPlacementMembership :many
-- Tenant-independent membership of one canonical control cell. Inactive
-- clusters stay listed with their flag so a deactivation is observable.
SELECT ic.cluster_id,
       COALESCE(ic.is_active, false)::boolean AS is_active,
       COALESCE(n.node_id, '')::text AS node_id,
       COALESCE(n.status = 'active', false)::boolean AS admission_enabled,
       CURRENT_TIMESTAMP::timestamptz AS observed_at
FROM quartermaster.infrastructure_clusters ic
LEFT JOIN quartermaster.infrastructure_nodes n
       ON n.cluster_id = ic.cluster_id AND n.node_type = 'edge'
WHERE COALESCE(NULLIF(ic.control_cell_id, ''), NULLIF(ic.cell_id, ''), ic.cluster_id) = sqlc.arg(control_cell_id)::text
ORDER BY ic.cluster_id, n.node_id
LIMIT 8193;

-- name: PlacementMembershipFingerprint :one
-- Digests exactly the values GetPlacementMembership derives every cell's
-- membership from: cluster identity, canonical control cell and active flag,
-- and each edge node's cluster and admission. Heartbeats, health refreshes and
-- status changes between two non-admitting states leave it unchanged, so they
-- wake no membership subscriber.
SELECT md5(format('%L|%L',
    (SELECT string_agg(format('%L/%L/%L', ic.cluster_id,
                              COALESCE(NULLIF(ic.control_cell_id, ''), NULLIF(ic.cell_id, ''), ic.cluster_id),
                              COALESCE(ic.is_active, false)), ',' ORDER BY ic.cluster_id)
     FROM quartermaster.infrastructure_clusters ic),
    (SELECT string_agg(format('%L/%L/%L', n.node_id, n.cluster_id, n.status = 'active'), ',' ORDER BY n.node_id, n.cluster_id)
     FROM quartermaster.infrastructure_nodes n
     WHERE n.node_type = 'edge')
))::text AS fingerprint;
