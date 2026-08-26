-- name: ValidateTenantRecord :one
SELECT name,
       is_active,
       rate_limit_per_minute,
       rate_limit_burst
FROM quartermaster.tenants
WHERE id = sqlc.arg(tenant_id)::uuid;

-- name: GetTenantRecord :one
SELECT id::text AS id,
       name,
       subdomain,
       custom_domain,
       logo_url,
       primary_color,
       secondary_color,
       deployment_tier,
       deployment_model,
       primary_cluster_id,
       official_cluster_id,
       kafka_topic_prefix,
       kafka_brokers,
       database_url,
       is_active,
       monitoring_enabled,
       created_at,
       updated_at,
       rate_limit_per_minute,
       rate_limit_burst
FROM quartermaster.tenants
WHERE id = sqlc.arg(tenant_id)::uuid;

-- name: GetTenantRoutingSelection :one
SELECT primary_cluster_id,
       COALESCE(official_cluster_id, '')::text AS official_cluster_id,
       deployment_tier
FROM quartermaster.tenants
WHERE id = sqlc.arg(tenant_id)::uuid
  AND is_active = true;

-- name: GetTenantPrimaryClusterRouting :one
WITH input AS (
    SELECT sqlc.arg(cluster_id)::text AS cluster_id,
           sqlc.arg(tenant_id)::uuid AS tenant_id
)
SELECT c.cluster_id,
       c.cluster_name,
       c.cluster_type,
       c.base_url,
       c.kafka_brokers,
       c.database_url,
       c.periscope_url,
       COALESCE(tca.kafka_topic_prefix, t.kafka_topic_prefix, '')::text AS topic_prefix,
       c.max_concurrent_streams,
       c.health_status
FROM quartermaster.infrastructure_clusters c
JOIN input ON true
JOIN quartermaster.tenants t ON t.id = input.tenant_id
JOIN quartermaster.tenant_cluster_access access
  ON access.tenant_id = input.tenant_id
 AND access.cluster_id = c.cluster_id
 AND access.is_active = true
 AND access.subscription_status = 'active'
 AND access.access_source <> 'unknown'
 AND (access.expires_at IS NULL OR access.expires_at > NOW())
LEFT JOIN quartermaster.tenant_cluster_assignments tca
  ON tca.tenant_id = t.id
 AND tca.cluster_id = c.cluster_id
WHERE c.cluster_id = input.cluster_id
  AND c.is_active = true;

-- name: GetTenantClusterResourceLimits :one
SELECT resource_limits
FROM quartermaster.tenant_cluster_access
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND cluster_id = sqlc.arg(cluster_id)::text
  AND is_active = true
  AND subscription_status = 'active'
  AND access_source <> 'unknown'
  AND (expires_at IS NULL OR expires_at > NOW());

-- name: GetHealthyFoghornAddressForCluster :one
SELECT si.advertise_host, si.port
FROM quartermaster.service_cluster_assignments sca
JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
JOIN quartermaster.services svc ON svc.service_id = si.service_id
WHERE sca.cluster_id = sqlc.arg(cluster_id)::text
  AND sca.is_active = true
  AND svc.type = 'foghorn'
  AND si.status = 'running'
  AND si.health_status = 'healthy'
  AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
  AND COALESCE(si.advertise_host, '') <> ''
  AND COALESCE(si.port, 0) > 0
ORDER BY CASE
             WHEN si.metadata->>'foghorn_listener' = 'internal_control' THEN 0
             WHEN si.port = 18019 THEN 1
             WHEN si.metadata->>'foghorn_listener' = 'control' THEN 2
             ELSE 3
         END,
         CASE WHEN si.protocol = 'grpc' THEN 0 ELSE 1 END,
         si.updated_at DESC,
         si.id ASC
LIMIT 1;

-- name: GetActiveClusterIdentity :one
SELECT cluster_name, base_url
FROM quartermaster.infrastructure_clusters
WHERE cluster_id = sqlc.arg(cluster_id)::text
  AND is_active = true;

-- name: ListTenantClusterRoutingPeers :many
SELECT ic.cluster_id,
       ic.cluster_name,
       ic.cluster_type,
       ic.base_url,
       COALESCE(ic.s3_bucket, '')::text AS s3_bucket,
       COALESCE(ic.s3_endpoint, '')::text AS s3_endpoint,
       COALESCE(ic.s3_region, '')::text AS s3_region,
       COALESCE(ic.s3_prefix, '')::text AS s3_prefix,
       (ic.s3_prefix IS NOT NULL)::boolean AS s3_prefix_present,
       COALESCE(ic.region_id, '')::text AS region_id,
       COALESCE(ic.cell_id, '')::text AS cell_id,
       COALESCE(ic.cluster_class, '')::text AS cluster_class,
       COALESCE(ic.health_status, '')::text AS health_status,
       COALESCE(ic.deployment_model, '')::text AS deployment_model,
       COALESCE(ic.owner_tenant_id::text, '')::text AS owner_tenant_id,
       COALESCE(tca.access_level, '')::text AS access_level,
       COALESCE(tca.access_source, 'unknown')::text AS access_source,
       tca.expires_at AS access_expires_at,
       foghorn.advertise_host AS foghorn_advertise_host,
       foghorn.port AS foghorn_port
FROM quartermaster.tenant_cluster_access tca
JOIN quartermaster.infrastructure_clusters ic ON ic.cluster_id = tca.cluster_id
LEFT JOIN LATERAL (
    SELECT si.advertise_host, si.port
    FROM quartermaster.service_cluster_assignments sca
    JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
    JOIN quartermaster.services svc ON svc.service_id = si.service_id
    WHERE sca.cluster_id = ic.cluster_id
      AND sca.is_active = true
      AND svc.type = 'foghorn'
      AND si.status = 'running'
      AND si.health_status = 'healthy'
      AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
      AND COALESCE(si.advertise_host, '') <> ''
      AND COALESCE(si.port, 0) > 0
    ORDER BY CASE
                 WHEN si.metadata->>'foghorn_listener' = 'internal_control' THEN 0
                 WHEN si.port = 18019 THEN 1
                 WHEN si.metadata->>'foghorn_listener' = 'control' THEN 2
                 ELSE 3
             END,
             CASE WHEN si.protocol = 'grpc' THEN 0 ELSE 1 END,
             si.updated_at DESC,
             si.id ASC
    LIMIT 1
) foghorn ON true
WHERE tca.tenant_id = sqlc.arg(tenant_id)::uuid
  AND tca.is_active = true
  AND tca.subscription_status = 'active'
  AND tca.access_source <> 'unknown'
  AND (tca.expires_at IS NULL OR tca.expires_at > NOW())
  AND ic.is_active = true
ORDER BY ic.cluster_id ASC;
