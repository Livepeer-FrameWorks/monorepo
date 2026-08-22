-- name: GetBootstrapServiceCatalog :one
SELECT service_id
FROM quartermaster.services
WHERE service_id = $1
   OR name = $1;

-- name: UpdateBootstrapServiceCatalog :exec
WITH input AS (
    SELECT sqlc.arg(service_id)::text AS service_id,
           sqlc.arg(name)::text AS name,
           sqlc.arg(plane)::text AS plane,
           sqlc.arg(type)::text AS type,
           sqlc.arg(protocol)::text AS protocol
)
UPDATE quartermaster.services
SET name = input.name,
    plane = input.plane,
    type = input.type,
    protocol = input.protocol,
    updated_at = NOW()
FROM input
WHERE services.service_id = input.service_id
  AND (
      services.name <> input.name
      OR COALESCE(services.plane, '') <> input.plane
      OR COALESCE(services.type, '') <> input.type
      OR COALESCE(services.protocol, '') <> input.protocol
  );

-- name: InsertBootstrapServiceCatalog :exec
INSERT INTO quartermaster.services (
    service_id, name, plane, type, protocol, is_active, created_at, updated_at
) VALUES (
    sqlc.arg(service_id)::text,
    sqlc.arg(name)::text,
    sqlc.arg(plane)::text,
    sqlc.arg(type)::text,
    sqlc.arg(protocol)::text,
    true, NOW(), NOW()
);

-- name: GetBootstrapNodeAdvertiseHost :one
SELECT cluster_id,
       COALESCE(host(wireguard_ip), '')::text AS wireguard_ip
FROM quartermaster.infrastructure_nodes
WHERE node_id = sqlc.arg(node_id)::text;

-- name: GetBootstrapServiceInstance :one
SELECT id::text AS id,
       COALESCE(advertise_host, '')::text AS advertise_host,
       COALESCE(health_endpoint_override, '')::text AS health_endpoint,
       COALESCE(metadata::text, '{}')::text AS metadata
FROM quartermaster.service_instances
WHERE service_id = sqlc.arg(service_id)::text
  AND cluster_id = sqlc.arg(cluster_id)::text
  AND node_id = sqlc.arg(node_id)::text
  AND protocol = sqlc.arg(protocol)::text
  AND port = sqlc.arg(port)::integer
ORDER BY updated_at DESC NULLS LAST
LIMIT 1;

-- name: InsertBootstrapServiceInstance :exec
INSERT INTO quartermaster.service_instances (
    instance_id, cluster_id, node_id, service_id, protocol, advertise_host,
    health_endpoint_override, port, metadata, status, health_status,
    created_at, updated_at
) VALUES (
    sqlc.arg(instance_id)::text,
    sqlc.arg(cluster_id)::text,
    sqlc.arg(node_id)::text,
    sqlc.arg(service_id)::text,
    sqlc.arg(protocol)::text,
    sqlc.arg(advertise_host)::text,
    sqlc.arg(health_endpoint)::text,
    sqlc.arg(port)::integer,
    sqlc.arg(metadata)::text::jsonb,
    'running', 'unknown', NOW(), NOW()
);

-- name: UpdateBootstrapServiceInstance :exec
WITH input AS (
    SELECT sqlc.arg(id)::uuid AS id,
           sqlc.arg(advertise_host)::text AS advertise_host,
           sqlc.arg(health_endpoint)::text AS health_endpoint,
           sqlc.arg(metadata)::text AS metadata
)
UPDATE quartermaster.service_instances
SET advertise_host = input.advertise_host,
    health_endpoint_override = input.health_endpoint,
    metadata = input.metadata::jsonb,
    updated_at = NOW()
FROM input
WHERE service_instances.id = input.id;
