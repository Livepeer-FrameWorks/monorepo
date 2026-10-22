-- name: GetNodeOwnerRecord :one
SELECT n.node_id, n.cluster_id, c.cluster_name, c.owner_tenant_id, t.name,
    (SELECT si.advertise_host
     FROM quartermaster.service_cluster_assignments sca
     JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
     JOIN quartermaster.services svc ON svc.service_id = si.service_id
     WHERE sca.cluster_id = n.cluster_id AND sca.is_active = true
       AND svc.type = 'foghorn' AND si.status = 'running'
       AND si.health_status = 'healthy' AND si.protocol = 'grpc'
       AND si.metadata->>'foghorn_listener' = 'internal_control'
       AND COALESCE(si.advertise_host, '') <> '' AND COALESCE(si.port, 0) > 0
     ORDER BY si.updated_at DESC, si.id ASC LIMIT 1) AS foghorn_host,
    (SELECT si.port
     FROM quartermaster.service_cluster_assignments sca
     JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
     JOIN quartermaster.services svc ON svc.service_id = si.service_id
     WHERE sca.cluster_id = n.cluster_id AND sca.is_active = true
       AND svc.type = 'foghorn' AND si.status = 'running'
       AND si.health_status = 'healthy' AND si.protocol = 'grpc'
       AND si.metadata->>'foghorn_listener' = 'internal_control'
       AND COALESCE(si.advertise_host, '') <> '' AND COALESCE(si.port, 0) > 0
     ORDER BY si.updated_at DESC, si.id ASC LIMIT 1) AS foghorn_port
FROM quartermaster.infrastructure_nodes n
JOIN quartermaster.infrastructure_clusters c ON n.cluster_id = c.cluster_id
LEFT JOIN quartermaster.tenants t ON c.owner_tenant_id = t.id
WHERE n.node_id = sqlc.arg(node_id);
