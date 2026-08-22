-- name: GetInfrastructureCluster :one
SELECT id, cluster_id, cluster_name, cluster_type, owner_tenant_id, deployment_model,
       base_url, database_url, periscope_url, kafka_brokers,
       max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
       health_status, is_active, is_default_cluster, is_platform_official, public_topology,
       created_at, updated_at, visibility, requires_approval, short_description,
       COALESCE(s3_bucket, '')::text AS s3_bucket,
       COALESCE(s3_endpoint, '')::text AS s3_endpoint,
       COALESCE(s3_region, '')::text AS s3_region,
       COALESCE(s3_prefix, '')::text AS s3_prefix,
       (s3_prefix IS NOT NULL)::boolean AS s3_prefix_present,
       COALESCE(region_id, '')::text AS region_id,
       COALESCE(cell_id, '')::text AS cell_id,
       COALESCE(cluster_class, '')::text AS cluster_class,
       COALESCE(control_cell_id, '')::text AS control_cell_id,
       COALESCE(eligible_serving_cell_ids, ARRAY[]::TEXT[])::text[] AS eligible_serving_cell_ids
FROM quartermaster.infrastructure_clusters
WHERE cluster_id = sqlc.arg(cluster_id);

-- name: GetInfrastructureNode :one
SELECT n.id, n.node_id, n.cluster_id, n.node_name, n.node_type, n.internal_ip, n.external_ip,
       n.wireguard_ip, n.wireguard_public_key, n.wireguard_listen_port, n.region, n.availability_zone,
       n.latitude, n.longitude, n.cpu_cores, n.memory_gb, n.disk_gb,
       n.last_heartbeat, n.enrollment_origin, n.applied_mesh_revision, n.status, n.created_at, n.updated_at,
       COALESCE((SELECT c.owner_tenant_id::text FROM quartermaster.infrastructure_clusters c WHERE c.cluster_id = n.cluster_id), '')::text AS owner_tenant_id,
       n.snapshot_cpu_percent, n.snapshot_ram_used_bytes, n.snapshot_ram_total_bytes,
       n.snapshot_disk_used_bytes, n.snapshot_disk_total_bytes, n.snapshot_uptime_seconds, n.snapshot_at
FROM quartermaster.infrastructure_nodes n
WHERE n.node_id = sqlc.arg(node_id) OR n.id::text = sqlc.arg(node_id);

-- name: ListTakenMeshIPs :many
SELECT host(wireguard_ip)::text
FROM quartermaster.infrastructure_nodes
WHERE cluster_id = sqlc.arg(cluster_id) AND wireguard_ip IS NOT NULL;
