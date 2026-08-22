-- name: ResolveTenantBySubdomain :one
SELECT id::text AS id, name, primary_cluster_id
FROM quartermaster.tenants
WHERE is_active = true AND subdomain = sqlc.arg(subdomain)::text;

-- name: ResolveTenantByCustomDomain :one
SELECT id::text AS id, name, primary_cluster_id
FROM quartermaster.tenants
WHERE is_active = true AND custom_domain = sqlc.arg(custom_domain)::text;

-- name: ResolveBootstrapTenantAliases :many
SELECT alias, tenant_id::text AS tenant_id
FROM quartermaster.bootstrap_tenant_aliases
WHERE alias = ANY(sqlc.arg(aliases)::text[]);

-- name: ListActiveTenantRecords :many
SELECT id::text AS id, monitoring_enabled
FROM quartermaster.tenants
WHERE is_active = true
ORDER BY id;

-- name: GetActiveTenantClusterRecord :one
SELECT id::text AS id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
       deployment_tier, deployment_model, primary_cluster_id, official_cluster_id,
       kafka_topic_prefix, kafka_brokers, database_url, is_active, monitoring_enabled,
       created_at, updated_at
FROM quartermaster.tenants
WHERE id = sqlc.arg(tenant_id)::uuid AND is_active = true;

-- name: ListActiveTenantsByIDs :many
SELECT id::text AS id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
       deployment_tier, deployment_model, primary_cluster_id, official_cluster_id,
       kafka_topic_prefix, kafka_brokers, database_url, is_active, monitoring_enabled,
       created_at, updated_at
FROM quartermaster.tenants
WHERE id = ANY(sqlc.arg(tenant_ids)::uuid[]) AND is_active = true;

-- name: ListActiveTenantsByCluster :many
SELECT sub.*, count(*) OVER() AS total_count
FROM (
    SELECT DISTINCT t.id::text AS id, t.name, t.subdomain, t.custom_domain, t.logo_url,
           t.primary_color, t.secondary_color, t.deployment_tier, t.deployment_model,
           t.primary_cluster_id, t.official_cluster_id, t.kafka_topic_prefix,
           t.kafka_brokers, t.database_url, t.is_active, t.monitoring_enabled,
           t.created_at, t.updated_at
    FROM quartermaster.tenants t
    LEFT JOIN quartermaster.tenant_cluster_assignments tca ON t.id = tca.tenant_id
    WHERE (t.primary_cluster_id = sqlc.arg(cluster_id) OR tca.cluster_id = sqlc.arg(cluster_id))
      AND t.is_active = true
) sub
ORDER BY sub.created_at
LIMIT sqlc.arg(result_limit);

-- name: ListActiveClusterSlugs :many
SELECT cluster_id
FROM quartermaster.infrastructure_clusters
WHERE is_active = true;

-- name: TenantSubdomainExists :one
SELECT EXISTS (
    SELECT 1 FROM quartermaster.tenants WHERE subdomain = sqlc.arg(subdomain)
);

-- name: ListAliasedTenantsForCluster :many
SELECT t.id::text AS tenant_id, t.subdomain
FROM quartermaster.tenants t
JOIN quartermaster.tenant_cluster_access tca ON tca.tenant_id = t.id
WHERE tca.cluster_id = sqlc.arg(cluster_id)
  AND tca.is_active = true
  AND t.is_active = true
  AND t.deployment_tier IN ('supporter', 'developer', 'production', 'enterprise')
  AND t.subdomain IS NOT NULL
  AND t.subdomain <> ''
ORDER BY t.id;
