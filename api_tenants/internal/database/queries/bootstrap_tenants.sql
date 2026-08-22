-- name: ListBootstrapTenantAliases :many
SELECT alias, tenant_id::text
FROM quartermaster.bootstrap_tenant_aliases;

-- name: InsertBootstrapTenant :one
INSERT INTO quartermaster.tenants (
    name, deployment_tier, primary_color, secondary_color, created_at, updated_at
) VALUES (
    sqlc.arg(name)::text,
    sqlc.arg(deployment_tier)::text,
    sqlc.arg(primary_color)::text,
    sqlc.arg(secondary_color)::text,
    NOW(), NOW()
)
RETURNING id::text;

-- name: GetBootstrapTenant :one
SELECT name,
       COALESCE(primary_color, '')::text AS primary_color,
       COALESCE(secondary_color, '')::text AS secondary_color
FROM quartermaster.tenants
WHERE id = sqlc.arg(id)::uuid;

-- name: UpdateBootstrapTenant :exec
WITH input AS (
    SELECT sqlc.arg(id)::uuid AS id,
           sqlc.arg(name)::text AS name,
           sqlc.arg(primary_color)::text AS primary_color,
           sqlc.arg(secondary_color)::text AS secondary_color
)
UPDATE quartermaster.tenants
SET name = input.name,
    primary_color = input.primary_color,
    secondary_color = input.secondary_color,
    updated_at = NOW()
FROM input
WHERE tenants.id = input.id;

-- name: UpsertBootstrapTenantAlias :execrows
INSERT INTO quartermaster.bootstrap_tenant_aliases (
    alias, tenant_id, created_at, updated_at
) VALUES (
    sqlc.arg(alias)::text, sqlc.arg(tenant_id)::uuid, NOW(), NOW()
)
ON CONFLICT (alias) DO UPDATE
SET tenant_id = EXCLUDED.tenant_id,
    updated_at = NOW()
WHERE quartermaster.bootstrap_tenant_aliases.tenant_id = EXCLUDED.tenant_id;

-- name: ListBootstrapDefaultAccessClusters :many
SELECT cluster_id
FROM quartermaster.infrastructure_clusters
WHERE is_active = true
  AND is_default_cluster = true
ORDER BY cluster_id;

-- name: ListBootstrapOfficialAccessClusters :many
SELECT cluster_id
FROM quartermaster.infrastructure_clusters
WHERE is_active = true
  AND is_platform_official = true
ORDER BY cluster_id;

-- name: ListBootstrapDefaultOrOfficialAccessClusters :many
SELECT cluster_id
FROM quartermaster.infrastructure_clusters
WHERE is_active = true
  AND (is_default_cluster = true OR is_platform_official = true)
ORDER BY cluster_id;

-- name: GetBootstrapTenantClusterAccess :one
SELECT COALESCE(subscription_status, '')::text AS subscription_status,
       COALESCE(is_active, false)::boolean AS is_active
FROM quartermaster.tenant_cluster_access
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND cluster_id = sqlc.arg(cluster_id)::text;

-- name: InsertBootstrapTenantClusterAccess :exec
INSERT INTO quartermaster.tenant_cluster_access (
    tenant_id, cluster_id, access_level, subscription_status,
    is_active, granted_at, created_at, updated_at
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_id)::text,
    'shared', 'active', true, NOW(), NOW(), NOW()
);

-- name: ActivateBootstrapTenantClusterAccess :exec
UPDATE quartermaster.tenant_cluster_access
SET subscription_status = 'active',
    is_active = true,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND cluster_id = sqlc.arg(cluster_id)::text;
