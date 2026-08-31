-- name: GetClusterOfficialState :one
SELECT is_platform_official, is_active
FROM quartermaster.infrastructure_clusters
WHERE cluster_id = sqlc.arg(cluster_id);

-- name: BootstrapTenantClusterAccess :exec
INSERT INTO quartermaster.tenant_cluster_access
    (tenant_id, cluster_id, access_level, access_source, subscription_status, resource_limits,
     is_active, granted_at, created_at, updated_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_id), 'shared', 'platform_tier', 'active',
        COALESCE(sqlc.narg(resource_limits)::text::jsonb, '{}'::jsonb), true, NOW(), NOW(), NOW())
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    subscription_status = 'active',
    is_active = true,
    access_source = 'platform_tier',
    resource_limits = COALESCE(NULLIF(quartermaster.tenant_cluster_access.resource_limits, '{}'::jsonb), EXCLUDED.resource_limits),
    updated_at = NOW();

-- name: DeactivateTenantClusterAccess :exec
UPDATE quartermaster.tenant_cluster_access
SET is_active = false, subscription_status = 'suspended', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND cluster_id = sqlc.arg(cluster_id)
  AND is_active = true;

-- name: GetClusterAccessMaterializationPolicy :one
SELECT COALESCE(owner_tenant_id::text, '')::text AS owner_tenant_id,
       COALESCE(cluster_class, '')::text AS cluster_class,
       is_platform_official,
       is_active
FROM quartermaster.infrastructure_clusters
WHERE cluster_id = sqlc.arg(cluster_id)::text;

-- name: MaterializeTenantClusterAccess :execrows
INSERT INTO quartermaster.tenant_cluster_access (
    tenant_id, cluster_id, access_level, access_source, subscription_status,
    is_active, granted_at, requested_at, created_at, updated_at
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_id)::text, 'subscriber',
    sqlc.arg(access_source)::text, sqlc.arg(subscription_status)::text,
    sqlc.arg(subscription_status)::text = 'active',
    CASE WHEN sqlc.arg(subscription_status)::text = 'active' THEN NOW() ELSE NULL END,
    CASE WHEN sqlc.arg(subscription_status)::text = 'pending_approval' THEN NOW() ELSE NULL END,
    NOW(), NOW()
)
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    access_level = 'subscriber',
    access_source = EXCLUDED.access_source,
    subscription_status = EXCLUDED.subscription_status,
    is_active = EXCLUDED.is_active,
    granted_at = CASE WHEN EXCLUDED.is_active THEN NOW() ELSE quartermaster.tenant_cluster_access.granted_at END,
    requested_at = CASE
        WHEN EXCLUDED.subscription_status = 'pending_approval'
             AND quartermaster.tenant_cluster_access.subscription_status = 'pending_approval'
            THEN COALESCE(quartermaster.tenant_cluster_access.requested_at, NOW())
        WHEN EXCLUDED.subscription_status = 'pending_approval' THEN NOW()
        ELSE quartermaster.tenant_cluster_access.requested_at
    END,
    approved_at = NULL,
    approved_by = NULL,
    rejection_reason = NULL,
    expires_at = CASE WHEN EXCLUDED.is_active THEN NULL ELSE quartermaster.tenant_cluster_access.expires_at END,
    updated_at = NOW()
WHERE EXCLUDED.access_source = 'owner'
   OR (
       NOT (EXCLUDED.subscription_status = 'pending_approval'
            AND quartermaster.tenant_cluster_access.is_active = true
            AND quartermaster.tenant_cluster_access.subscription_status = 'active'
            AND (quartermaster.tenant_cluster_access.expires_at IS NULL
                 OR quartermaster.tenant_cluster_access.expires_at > NOW()))
       AND (
           quartermaster.tenant_cluster_access.access_source NOT IN ('operator_override', 'private_invite', 'owner')
           OR quartermaster.tenant_cluster_access.is_active = false
           OR quartermaster.tenant_cluster_access.subscription_status <> 'active'
           OR (quartermaster.tenant_cluster_access.expires_at IS NOT NULL
               AND quartermaster.tenant_cluster_access.expires_at <= NOW())
       )
   );

-- name: RevokeMaterializedTenantClusterAccess :execrows
UPDATE quartermaster.tenant_cluster_access
SET is_active = false,
    subscription_status = 'suspended',
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND cluster_id = sqlc.arg(cluster_id)::text
  AND access_source = sqlc.arg(access_source)::text
  AND (is_active = true OR subscription_status = 'active');

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
  AND access_source <> 'unknown'
  AND (expires_at IS NULL OR expires_at > NOW())
ORDER BY cluster_id;

-- name: ListTenantEffectiveAccess :many
SELECT ic.cluster_id,
       ic.cluster_name,
       ic.cluster_type,
       ic.base_url,
       COALESCE(ic.deployment_model, '')::text AS deployment_model,
       COALESCE(ic.owner_tenant_id::text, '')::text AS owner_tenant_id,
       COALESCE(ic.cluster_class, '')::text AS cluster_class,
       COALESCE(ic.health_status, '')::text AS health_status,
	   COALESCE(NULLIF(ic.control_cell_id, ''), NULLIF(ic.cell_id, ''), ic.cluster_id)::text AS control_cell_id,
	   ic.eligible_serving_cell_ids,
       COALESCE(tca.access_level, '')::text AS access_level,
       COALESCE(tca.access_source, 'unknown')::text AS access_source,
       tca.is_active AS access_active,
       tca.subscription_status,
       tca.expires_at AS access_expires_at,
       tca.resource_limits::text AS resource_limits,
       ic.allow_private_pull_sources
FROM quartermaster.tenant_cluster_access tca
JOIN quartermaster.infrastructure_clusters ic ON ic.cluster_id = tca.cluster_id
WHERE tca.tenant_id = sqlc.arg(tenant_id)::uuid
  AND tca.is_active = true
  AND tca.subscription_status = 'active'
  AND tca.access_source <> 'unknown'
  AND (tca.expires_at IS NULL OR tca.expires_at > NOW())
  AND ic.is_active = true
ORDER BY ic.cluster_id;

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
    (tenant_id, cluster_id, access_level, access_source, resource_limits,
     subscription_status, is_active, expires_at, created_at, updated_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_id), sqlc.arg(access_level),
        'operator_override', COALESCE(sqlc.narg(resource_limits)::text::jsonb, '{}'::jsonb),
        'active', true, sqlc.narg(expires_at)::timestamptz, NOW(), NOW())
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    access_level = EXCLUDED.access_level,
    access_source = 'operator_override',
    resource_limits = EXCLUDED.resource_limits,
    subscription_status = 'active',
    is_active = true,
    expires_at = EXCLUDED.expires_at,
    updated_at = NOW();

-- name: GetTenantClusterAccessState :one
SELECT jsonb_build_object(
    'access_level', access_level,
    'access_source', access_source,
    'subscription_status', subscription_status,
    'is_active', is_active,
    'expires_at', expires_at,
    'resource_limits', resource_limits
)::text AS state
FROM quartermaster.tenant_cluster_access
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND cluster_id = sqlc.arg(cluster_id)::text;
