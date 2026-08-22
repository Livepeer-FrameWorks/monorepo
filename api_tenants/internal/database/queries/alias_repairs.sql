-- name: ListDesiredTenantAliases :many
SELECT t.id::text AS tenant_id,
       COALESCE(t.subdomain, '')::text AS subdomain,
       (
           t.is_active
           AND t.deployment_tier IN ('supporter', 'developer', 'production', 'enterprise')
           AND EXISTS (
               SELECT 1
               FROM quartermaster.tenant_cluster_access tca
               WHERE tca.tenant_id = t.id
                 AND tca.is_active = true
           )
       )::boolean AS want
FROM quartermaster.tenants t;

-- name: TenantAliasOutboxHasPending :one
SELECT EXISTS (
    SELECT 1
    FROM quartermaster.navigator_tenant_alias_outbox
    WHERE tenant_id = sqlc.arg(tenant_id)::uuid
      AND completed_at IS NULL
)::boolean;

-- name: RepairTenantPrivateBaseURLBatch :execrows
WITH repair AS (
    SELECT c.id, control.base_url
    FROM quartermaster.infrastructure_clusters c
    JOIN quartermaster.infrastructure_clusters control
      ON control.cluster_id = c.control_cell_id
    WHERE c.cluster_class = 'tenant_private'
      AND NULLIF(c.base_url, '') IS NULL
      AND control.cluster_class = 'platform_official'
      AND NULLIF(control.base_url, '') IS NOT NULL
    ORDER BY c.created_at ASC, c.id ASC
    LIMIT sqlc.arg(batch_size)::integer
)
UPDATE quartermaster.infrastructure_clusters c
SET base_url = repair.base_url,
    updated_at = NOW()
FROM repair
WHERE c.id = repair.id;
