-- name: GetClusterOfficialState :one
SELECT is_platform_official, is_active
FROM quartermaster.infrastructure_clusters
WHERE cluster_id = sqlc.arg(cluster_id);

-- name: BootstrapTenantClusterAccess :exec
INSERT INTO quartermaster.tenant_cluster_access
    (tenant_id, cluster_id, access_level, subscription_status, resource_limits,
     is_active, granted_at, created_at, updated_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_id), 'shared', 'active',
        COALESCE(sqlc.narg(resource_limits)::text::jsonb, '{}'::jsonb), true, NOW(), NOW(), NOW())
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    subscription_status = 'active',
    is_active = true,
    resource_limits = COALESCE(NULLIF(quartermaster.tenant_cluster_access.resource_limits, '{}'::jsonb), EXCLUDED.resource_limits),
    updated_at = NOW();

-- name: DeactivateTenantClusterAccess :exec
UPDATE quartermaster.tenant_cluster_access
SET is_active = false, subscription_status = 'suspended', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND cluster_id = sqlc.arg(cluster_id)
  AND is_active = true;

-- name: ListTenantClusterAccessRows :many
SELECT tca.cluster_id, tca.is_active, tca.subscription_status,
       COALESCE(ic.is_platform_official, false)::boolean AS is_platform_official
FROM quartermaster.tenant_cluster_access tca
LEFT JOIN quartermaster.infrastructure_clusters ic ON ic.cluster_id = tca.cluster_id
WHERE tca.tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ListTenantEntitledClusterIDs :many
SELECT cluster_id
FROM quartermaster.tenant_cluster_access
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND is_active = TRUE
  AND subscription_status = 'active'
ORDER BY cluster_id;

-- name: GetTenantPrimaryClusterClass :one
SELECT c.cluster_class
FROM quartermaster.tenants t
LEFT JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = t.primary_cluster_id
WHERE t.id = sqlc.arg(tenant_id)::uuid;

-- name: ListRunningServiceClusterAssignments :many
SELECT DISTINCT sca.cluster_id
FROM quartermaster.service_cluster_assignments sca
JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
JOIN quartermaster.services svc ON svc.service_id = si.service_id
WHERE si.instance_id = sqlc.arg(instance_id)
  AND svc.type = sqlc.arg(service_type)
  AND si.status = 'running'
  AND sca.is_active = true;

-- name: GetClusterDeploymentModel :one
SELECT deployment_model
FROM quartermaster.infrastructure_clusters
WHERE cluster_id = sqlc.arg(cluster_id);

-- name: SubscribeTenantToCluster :exec
INSERT INTO quartermaster.tenant_cluster_access
    (tenant_id, cluster_id, access_level, is_active, created_at, updated_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_id), 'subscriber', true, NOW(), NOW())
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    is_active = true,
    updated_at = NOW();

-- name: UnsubscribeTenantFromCluster :exec
UPDATE quartermaster.tenant_cluster_access
SET is_active = false, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND cluster_id = sqlc.arg(cluster_id);

-- name: GrantTenantClusterAccess :exec
INSERT INTO quartermaster.tenant_cluster_access
    (tenant_id, cluster_id, access_level, expires_at, created_at, updated_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_id), sqlc.arg(access_level),
        sqlc.narg(expires_at)::timestamptz, NOW(), NOW())
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    access_level = EXCLUDED.access_level,
    expires_at = EXCLUDED.expires_at,
    updated_at = NOW();
