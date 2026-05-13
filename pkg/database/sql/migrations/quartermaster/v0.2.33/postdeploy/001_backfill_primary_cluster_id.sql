-- Backfill tenants.primary_cluster_id so admission filters have a non-null
-- home cluster for every tenant. Derivation order:
--   1. The tenant's is_primary=TRUE row in tenant_cluster_assignments.
--   2. The tenant's first active tenant_cluster_assignments row by priority.
--   3. The platform's is_default_cluster=TRUE cluster.
--
-- No cross-service reads — quartermaster's schema only.
-- Schema source of truth: pkg/database/sql/schema/quartermaster.sql

UPDATE quartermaster.tenants t
SET primary_cluster_id = sub.cluster_id
FROM (
    SELECT DISTINCT ON (tenant_id) tenant_id, cluster_id
    FROM quartermaster.tenant_cluster_assignments
    WHERE is_active = TRUE
    ORDER BY tenant_id, is_primary DESC, priority ASC, created_at ASC
) sub
WHERE t.id = sub.tenant_id
  AND t.primary_cluster_id IS NULL;

UPDATE quartermaster.tenants t
SET primary_cluster_id = (
    SELECT cluster_id
    FROM quartermaster.infrastructure_clusters
    WHERE is_default_cluster = TRUE
      AND is_active = TRUE
    ORDER BY created_at ASC
    LIMIT 1
)
WHERE t.primary_cluster_id IS NULL;
