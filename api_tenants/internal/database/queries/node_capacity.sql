-- name: LockNodeCapacityOwner :one
SELECT n.node_id, n.cluster_id, c.owner_tenant_id
FROM quartermaster.infrastructure_nodes n
JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = n.cluster_id
WHERE n.node_id = sqlc.arg(node_id) OR n.id::text = sqlc.arg(node_id)
FOR SHARE OF n, c;

-- name: GetNodeCapacity :one
SELECT n.node_id, b.egress_budget_bytes_per_sec, b.ingress_budget_bytes_per_sec,
       b.revision, b.updated_by, b.updated_at
FROM quartermaster.infrastructure_nodes n
JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = n.cluster_id
LEFT JOIN quartermaster.infrastructure_node_capacity b ON b.node_id = n.node_id
WHERE (n.node_id = sqlc.arg(node_id) OR n.id::text = sqlc.arg(node_id))
  AND (sqlc.arg(privileged)::boolean OR c.owner_tenant_id = NULLIF(sqlc.arg(tenant_id)::text, '')::uuid);

-- name: ListNodeCapacitiesByNodes :many
SELECT n.node_id, b.egress_budget_bytes_per_sec, b.ingress_budget_bytes_per_sec,
       b.revision, b.updated_by, b.updated_at
FROM quartermaster.infrastructure_nodes n
JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = n.cluster_id
LEFT JOIN quartermaster.infrastructure_node_capacity b ON b.node_id = n.node_id
WHERE n.node_id = ANY(sqlc.arg(node_ids)::text[])
  AND (sqlc.arg(privileged)::boolean OR c.owner_tenant_id = NULLIF(sqlc.arg(tenant_id)::text, '')::uuid)
ORDER BY n.node_id;

-- name: ListNodeCapacitiesByClusters :many
SELECT n.node_id, b.egress_budget_bytes_per_sec, b.ingress_budget_bytes_per_sec,
       b.revision, b.updated_by, b.updated_at
FROM quartermaster.infrastructure_nodes n
JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = n.cluster_id
LEFT JOIN quartermaster.infrastructure_node_capacity b ON b.node_id = n.node_id
WHERE n.cluster_id = ANY(sqlc.arg(cluster_ids)::text[])
  AND (sqlc.arg(privileged)::boolean OR c.owner_tenant_id = NULLIF(sqlc.arg(tenant_id)::text, '')::uuid)
ORDER BY n.node_id;

-- name: UpsertNodeCapacity :execrows
INSERT INTO quartermaster.infrastructure_node_capacity (
    node_id, egress_budget_bytes_per_sec, ingress_budget_bytes_per_sec, revision, updated_by
)
SELECT n.node_id, sqlc.narg(egress_budget_bytes_per_sec)::bigint,
       sqlc.narg(ingress_budget_bytes_per_sec)::bigint, sqlc.arg(expected_revision)::bigint + 1, sqlc.arg(updated_by)::text
FROM quartermaster.infrastructure_nodes n
JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = n.cluster_id
WHERE n.node_id = sqlc.arg(node_id)
  AND (sqlc.arg(privileged)::boolean OR c.owner_tenant_id = NULLIF(sqlc.arg(tenant_id)::text, '')::uuid)
  AND (sqlc.arg(expected_revision)::bigint = 0 OR EXISTS (
      SELECT 1 FROM quartermaster.infrastructure_node_capacity current
      WHERE current.node_id = n.node_id AND current.revision = sqlc.arg(expected_revision)::bigint
  ))
ON CONFLICT (node_id) DO UPDATE SET
    egress_budget_bytes_per_sec = EXCLUDED.egress_budget_bytes_per_sec,
    ingress_budget_bytes_per_sec = EXCLUDED.ingress_budget_bytes_per_sec,
    revision = EXCLUDED.revision,
    updated_by = EXCLUDED.updated_by,
    updated_at = NOW()
WHERE quartermaster.infrastructure_node_capacity.revision = sqlc.arg(expected_revision)::bigint;
