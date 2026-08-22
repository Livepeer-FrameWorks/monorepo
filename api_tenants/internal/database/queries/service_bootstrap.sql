-- name: LockServiceType :exec
SELECT pg_advisory_xact_lock(hashtext(sqlc.arg(service_type)));

-- name: FindServiceID :one
SELECT service_id
FROM quartermaster.services
WHERE service_id = sqlc.arg(service_type) OR name = sqlc.arg(service_type);

-- name: CreateServiceCatalogEntry :exec
INSERT INTO quartermaster.services
    (service_id, name, plane, type, protocol, is_active, created_at, updated_at)
VALUES (sqlc.arg(service_id)::text, sqlc.arg(name)::text, 'control',
        sqlc.arg(service_type)::text, sqlc.arg(protocol)::text, true, NOW(), NOW());

-- name: LockServiceBootstrapToken :one
SELECT kind, COALESCE(cluster_id, ''), expires_at
FROM quartermaster.bootstrap_tokens
WHERE token_hash = sqlc.arg(token_hash)::text AND used_at IS NULL
FOR UPDATE;

-- name: GetClusterActiveState :one
SELECT is_active
FROM quartermaster.infrastructure_clusters
WHERE cluster_id = sqlc.arg(cluster_id)::text;

-- name: ListActiveClusterIDs :many
SELECT cluster_id
FROM quartermaster.infrastructure_clusters
WHERE is_active = true;

-- name: FindNodeByClusterIP :one
SELECT node_id
FROM quartermaster.infrastructure_nodes
WHERE cluster_id = sqlc.arg(cluster_id)::text
  AND (wireguard_ip = sqlc.arg(ip)::inet OR internal_ip = sqlc.arg(ip)::inet OR external_ip = sqlc.arg(ip)::inet)
LIMIT 1;

-- name: FindServiceInstanceByRequestedID :one
SELECT id::text, instance_id
FROM quartermaster.service_instances
WHERE service_id = sqlc.arg(service_id)::text
  AND cluster_id = sqlc.arg(cluster_id)::text
  AND protocol = sqlc.arg(protocol)::text
  AND instance_id = sqlc.arg(instance_id)::text
ORDER BY updated_at DESC NULLS LAST, started_at DESC NULLS LAST
LIMIT 1;

-- name: FindServiceInstanceByNodeHost :one
SELECT id::text, instance_id
FROM quartermaster.service_instances
WHERE service_id = sqlc.arg(service_id)::text
  AND cluster_id = sqlc.arg(cluster_id)::text
  AND protocol = sqlc.arg(protocol)::text
  AND port = sqlc.arg(port)::integer
  AND (node_id = sqlc.arg(node_id)::text OR node_id IS NULL)
  AND advertise_host = sqlc.arg(advertise_host)::text
ORDER BY updated_at DESC NULLS LAST, started_at DESC NULLS LAST
LIMIT 1;

-- name: FindServiceInstanceByNode :one
SELECT id::text, instance_id
FROM quartermaster.service_instances
WHERE service_id = sqlc.arg(service_id)::text
  AND cluster_id = sqlc.arg(cluster_id)::text
  AND protocol = sqlc.arg(protocol)::text
  AND port = sqlc.arg(port)::integer
  AND (node_id = sqlc.arg(node_id)::text OR node_id IS NULL)
ORDER BY updated_at DESC NULLS LAST, started_at DESC NULLS LAST
LIMIT 1;

-- name: FindServiceInstanceByHost :one
SELECT id::text, instance_id
FROM quartermaster.service_instances
WHERE service_id = sqlc.arg(service_id)::text
  AND cluster_id = sqlc.arg(cluster_id)::text
  AND protocol = sqlc.arg(protocol)::text
  AND port = sqlc.arg(port)::integer
  AND advertise_host = sqlc.arg(advertise_host)::text
ORDER BY updated_at DESC NULLS LAST, started_at DESC NULLS LAST
LIMIT 1;

-- name: GetClusterOwnerTenantID :one
SELECT owner_tenant_id
FROM quartermaster.infrastructure_clusters
WHERE cluster_id = sqlc.arg(cluster_id)::text;

-- name: ConsumeServiceBootstrapToken :execrows
UPDATE quartermaster.bootstrap_tokens
SET used_at = NOW(), usage_count = usage_count + 1
WHERE token_hash = sqlc.arg(token_hash)::text AND used_at IS NULL;
