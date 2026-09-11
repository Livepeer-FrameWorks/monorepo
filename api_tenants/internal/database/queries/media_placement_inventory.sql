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
