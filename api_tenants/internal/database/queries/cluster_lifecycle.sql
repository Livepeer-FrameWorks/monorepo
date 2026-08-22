-- name: TenantExists :one
SELECT EXISTS(SELECT 1 FROM quartermaster.tenants WHERE id = sqlc.arg(tenant_id)::uuid);

-- name: ClearDefaultCluster :exec
UPDATE quartermaster.infrastructure_clusters
SET is_default_cluster = false
WHERE is_default_cluster = true;

-- name: CreateInfrastructureCluster :exec
INSERT INTO quartermaster.infrastructure_clusters
    (id, cluster_id, cluster_name, cluster_type, deployment_model,
     owner_tenant_id, base_url, database_url, periscope_url, kafka_brokers,
     max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
     health_status, is_active, is_platform_official, is_default_cluster,
     public_topology, allow_private_pull_sources, created_at, updated_at)
VALUES (sqlc.arg(id)::uuid, sqlc.arg(cluster_id), sqlc.arg(cluster_name), sqlc.arg(cluster_type),
        sqlc.arg(deployment_model), NULLIF(sqlc.arg(owner_tenant_id)::text, '')::uuid,
        sqlc.arg(base_url), sqlc.narg(database_url), sqlc.narg(periscope_url), sqlc.arg(kafka_brokers)::text[],
        sqlc.arg(max_concurrent_streams), sqlc.arg(max_concurrent_viewers), sqlc.arg(max_bandwidth_mbps),
        'healthy', true, sqlc.arg(is_platform_official), sqlc.arg(is_default_cluster),
        sqlc.arg(public_topology), sqlc.arg(allow_private_pull_sources),
        sqlc.arg(created_at)::timestamp, sqlc.arg(created_at)::timestamp);

-- name: ClaimIdleFoghornsForCluster :execrows
INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id)
SELECT si.id, sqlc.arg(cluster_id)
FROM quartermaster.service_instances si
JOIN quartermaster.services svc ON svc.service_id = si.service_id
LEFT JOIN quartermaster.service_cluster_assignments sca
  ON sca.service_instance_id = si.id AND sca.is_active = true
WHERE svc.type = 'foghorn'
  AND si.status = 'running'
  AND si.health_status = 'healthy'
  AND si.protocol = 'grpc'
  AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
  AND sca.id IS NULL
ORDER BY si.started_at ASC
LIMIT sqlc.arg(foghorn_count)
ON CONFLICT DO NOTHING;

-- name: MarkClusterProvisioning :exec
UPDATE quartermaster.infrastructure_clusters
SET health_status = 'provisioning'
WHERE cluster_id = sqlc.arg(cluster_id);

-- name: UpdateClusterMeshConfig :execrows
UPDATE quartermaster.infrastructure_clusters
SET wg_mesh_cidr = sqlc.arg(mesh_cidr)::cidr,
    wg_listen_port = sqlc.arg(wg_listen_port),
    updated_at = NOW()
WHERE cluster_id = sqlc.arg(cluster_id);

-- name: GetTenantClusterOwnershipLimit :one
SELECT max_owned_clusters, is_provider,
       (SELECT COUNT(*) FROM quartermaster.infrastructure_clusters WHERE owner_tenant_id = sqlc.arg(tenant_id)::uuid)::bigint AS current_owned_clusters
FROM quartermaster.tenants WHERE id = sqlc.arg(tenant_id)::uuid;

-- name: GetTenantPreferredClusterRegion :one
SELECT pc.region_id
FROM quartermaster.tenants t
JOIN quartermaster.infrastructure_clusters pc
  ON pc.cluster_id = t.primary_cluster_id AND pc.is_active = true
WHERE t.id = sqlc.arg(tenant_id)::uuid;
