-- name: ClearBootstrapDefaultCluster :exec
UPDATE quartermaster.infrastructure_clusters
SET is_default_cluster = false,
    updated_at = NOW()
WHERE is_default_cluster = true;

-- name: GetBootstrapCluster :one
SELECT cluster_name,
       cluster_type,
       COALESCE(owner_tenant_id::text, '')::text AS owner_tenant_id,
       COALESCE(base_url, '')::text AS base_url,
       COALESCE(wg_mesh_cidr, '')::text AS wg_mesh_cidr,
       COALESCE(wg_listen_port, 0)::integer AS wg_listen_port,
       COALESCE(is_default_cluster, false)::boolean AS is_default_cluster,
       COALESCE(is_platform_official, false)::boolean AS is_platform_official,
       public_topology,
       allow_private_pull_sources,
       COALESCE(region_id, '')::text AS region_id,
       COALESCE(cell_id, '')::text AS cell_id,
       COALESCE(cluster_class, '')::text AS cluster_class,
       COALESCE(control_cell_id, '')::text AS control_cell_id,
       COALESCE(eligible_serving_cell_ids, ARRAY[]::text[])::text[] AS eligible_serving_cell_ids,
       COALESCE(s3_bucket, '')::text AS s3_bucket,
       COALESCE(s3_endpoint, '')::text AS s3_endpoint,
       COALESCE(s3_region, '')::text AS s3_region,
       (s3_prefix IS NOT NULL)::boolean AS s3_prefix_set,
       COALESCE(s3_prefix, '')::text AS s3_prefix
FROM quartermaster.infrastructure_clusters
WHERE cluster_id = sqlc.arg(cluster_id)::text
FOR UPDATE;

-- name: InsertBootstrapCluster :exec
INSERT INTO quartermaster.infrastructure_clusters (
    cluster_id, cluster_name, cluster_type,
    owner_tenant_id, base_url,
    wg_mesh_cidr, wg_listen_port,
    is_default_cluster, is_platform_official, public_topology, allow_private_pull_sources,
    region_id, cell_id, cluster_class,
    control_cell_id, eligible_serving_cell_ids,
    s3_bucket, s3_endpoint, s3_region, s3_prefix,
    created_at, updated_at
) VALUES (
    sqlc.arg(cluster_id)::text,
    sqlc.arg(cluster_name)::text,
    sqlc.arg(cluster_type)::text,
    NULLIF(sqlc.arg(owner_tenant_id)::text, '')::uuid,
    NULLIF(sqlc.arg(base_url)::text, ''),
    NULLIF(sqlc.arg(wg_mesh_cidr)::text, ''),
    NULLIF(sqlc.arg(wg_listen_port)::integer, 0),
    sqlc.arg(is_default_cluster)::boolean,
    sqlc.arg(is_platform_official)::boolean,
    sqlc.arg(public_topology)::boolean,
    sqlc.arg(allow_private_pull_sources)::boolean,
    NULLIF(sqlc.arg(region_id)::text, ''),
    NULLIF(sqlc.arg(cell_id)::text, ''),
    NULLIF(sqlc.arg(cluster_class)::text, ''),
    NULLIF(sqlc.arg(control_cell_id)::text, ''),
    sqlc.arg(eligible_serving_cell_ids)::text[],
    NULLIF(sqlc.arg(s3_bucket)::text, ''),
    NULLIF(sqlc.arg(s3_endpoint)::text, ''),
    NULLIF(sqlc.arg(s3_region)::text, ''),
    sqlc.arg(s3_prefix)::text,
    NOW(), NOW()
);

-- name: UpdateBootstrapCluster :exec
WITH input AS (
    SELECT sqlc.arg(cluster_id)::text AS cluster_id,
           sqlc.arg(cluster_name)::text AS cluster_name,
           sqlc.arg(base_url)::text AS base_url,
           sqlc.arg(wg_mesh_cidr)::text AS wg_mesh_cidr,
           sqlc.arg(wg_listen_port)::integer AS wg_listen_port,
           sqlc.arg(is_default_cluster)::boolean AS is_default_cluster,
           sqlc.arg(is_platform_official)::boolean AS is_platform_official,
           sqlc.arg(public_topology)::boolean AS public_topology,
           sqlc.arg(allow_private_pull_sources)::boolean AS allow_private_pull_sources,
           sqlc.arg(region_id)::text AS region_id,
           sqlc.arg(cell_id)::text AS cell_id,
           sqlc.arg(cluster_class)::text AS cluster_class,
           sqlc.arg(control_cell_id)::text AS control_cell_id,
           sqlc.arg(eligible_serving_cell_ids)::text[] AS eligible_serving_cell_ids,
           sqlc.arg(s3_bucket)::text AS s3_bucket,
           sqlc.arg(s3_endpoint)::text AS s3_endpoint,
           sqlc.arg(s3_region)::text AS s3_region,
           sqlc.arg(s3_prefix)::text AS s3_prefix
)
UPDATE quartermaster.infrastructure_clusters
SET cluster_name = input.cluster_name,
    base_url = NULLIF(input.base_url, ''),
    wg_mesh_cidr = NULLIF(input.wg_mesh_cidr, ''),
    wg_listen_port = NULLIF(input.wg_listen_port, 0),
    is_default_cluster = input.is_default_cluster,
    is_platform_official = input.is_platform_official,
    public_topology = input.public_topology,
    allow_private_pull_sources = input.allow_private_pull_sources,
    region_id = NULLIF(input.region_id, ''),
    cell_id = NULLIF(input.cell_id, ''),
    cluster_class = NULLIF(input.cluster_class, ''),
    control_cell_id = NULLIF(input.control_cell_id, ''),
    eligible_serving_cell_ids = input.eligible_serving_cell_ids,
    s3_bucket = NULLIF(input.s3_bucket, ''),
    s3_endpoint = NULLIF(input.s3_endpoint, ''),
    s3_region = NULLIF(input.s3_region, ''),
    s3_prefix = input.s3_prefix,
    updated_at = NOW()
FROM input
WHERE infrastructure_clusters.cluster_id = input.cluster_id;
