-- name: GetBootstrapNode :one
SELECT node_name,
       node_type,
       cluster_id,
       COALESCE(host(external_ip), '')::text AS external_ip,
       COALESCE(host(wireguard_ip), '')::text AS wireguard_ip,
       COALESCE(wireguard_public_key, '')::text AS wireguard_public_key,
       COALESCE(wireguard_listen_port, 0)::integer AS wireguard_listen_port,
       enrollment_origin,
       latitude,
       longitude
FROM quartermaster.infrastructure_nodes
WHERE node_id = $1;

-- name: InsertBootstrapNode :exec
INSERT INTO quartermaster.infrastructure_nodes (
    node_id, cluster_id, node_name, node_type,
    external_ip, wireguard_ip, wireguard_public_key, wireguard_listen_port,
    latitude, longitude,
    enrollment_origin, status,
    created_at, updated_at
) VALUES (
    sqlc.arg(node_id)::text,
    sqlc.arg(cluster_id)::text,
    sqlc.arg(node_name)::text,
    sqlc.arg(node_type)::text,
    NULLIF(sqlc.arg(external_ip)::text, '')::inet,
    NULLIF(sqlc.arg(wireguard_ip)::text, '')::inet,
    sqlc.arg(wireguard_public_key)::text,
    NULLIF(sqlc.arg(wireguard_listen_port)::integer, 0),
    sqlc.narg(latitude)::double precision,
    sqlc.narg(longitude)::double precision,
    'gitops_seed', 'offline',
    NOW(), NOW()
);

-- name: UpdateBootstrapNodeMutableFields :exec
WITH input AS (
    SELECT sqlc.arg(node_id)::text AS node_id,
           sqlc.arg(node_name)::text AS node_name,
           sqlc.arg(node_type)::text AS node_type,
           sqlc.arg(wireguard_listen_port)::integer AS wireguard_listen_port,
           sqlc.narg(latitude)::double precision AS latitude,
           sqlc.narg(longitude)::double precision AS longitude
)
UPDATE quartermaster.infrastructure_nodes
SET node_name = input.node_name,
    node_type = input.node_type,
    wireguard_listen_port = NULLIF(input.wireguard_listen_port, 0),
    latitude = COALESCE(infrastructure_nodes.latitude, input.latitude),
    longitude = COALESCE(infrastructure_nodes.longitude, input.longitude),
    updated_at = NOW()
FROM input
WHERE infrastructure_nodes.node_id = input.node_id;

-- name: MoveBootstrapNodeServiceInstances :exec
WITH input AS (
    SELECT sqlc.arg(node_id)::text AS node_id,
           sqlc.arg(to_cluster_id)::text AS to_cluster_id,
           sqlc.arg(from_cluster_id)::text AS from_cluster_id
)
UPDATE quartermaster.service_instances
SET cluster_id = input.to_cluster_id,
    updated_at = NOW()
FROM input
WHERE service_instances.node_id = input.node_id
  AND service_instances.cluster_id = input.from_cluster_id;

-- name: MoveBootstrapNodeIngressSites :exec
WITH input AS (
    SELECT sqlc.arg(node_id)::text AS node_id,
           sqlc.arg(to_cluster_id)::text AS to_cluster_id,
           sqlc.arg(from_cluster_id)::text AS from_cluster_id
)
UPDATE quartermaster.ingress_sites
SET cluster_id = input.to_cluster_id,
    updated_at = NOW()
FROM input
WHERE ingress_sites.node_id = input.node_id
  AND ingress_sites.cluster_id = input.from_cluster_id;

-- name: MoveBootstrapNodeTLSBundles :exec
WITH input AS (
    SELECT sqlc.arg(node_id)::text AS node_id,
           sqlc.arg(to_cluster_id)::text AS to_cluster_id,
           sqlc.arg(from_cluster_id)::text AS from_cluster_id
)
UPDATE quartermaster.tls_bundles AS bundles
SET cluster_id = input.to_cluster_id,
    updated_at = NOW()
FROM input
WHERE bundles.cluster_id = input.from_cluster_id
  AND bundles.bundle_id IN (
      SELECT tls_bundle_id
      FROM quartermaster.ingress_sites
      WHERE node_id = input.node_id
        AND kind = 'physical'
  );

-- name: MoveBootstrapNode :exec
WITH input AS (
    SELECT sqlc.arg(node_id)::text AS node_id,
           sqlc.arg(to_cluster_id)::text AS to_cluster_id,
           sqlc.arg(from_cluster_id)::text AS from_cluster_id
)
UPDATE quartermaster.infrastructure_nodes
SET cluster_id = input.to_cluster_id,
    updated_at = NOW()
FROM input
WHERE infrastructure_nodes.node_id = input.node_id
  AND infrastructure_nodes.cluster_id = input.from_cluster_id;
