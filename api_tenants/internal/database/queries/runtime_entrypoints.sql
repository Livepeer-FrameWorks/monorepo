-- name: ListServedClustersForInstance :many
SELECT DISTINCT sca.cluster_id
FROM quartermaster.service_cluster_assignments sca
JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
JOIN quartermaster.services svc ON svc.service_id = si.service_id
WHERE si.instance_id = sqlc.arg(instance_id)::text
  AND svc.type = sqlc.arg(service_type)::text
  AND sca.is_active = true;

-- name: ListIngressSitesForNode :many
SELECT site_id,
       cluster_id,
       node_id,
       domains,
       tls_bundle_id,
       kind,
       upstream,
       COALESCE(metadata, '{}'::jsonb)::jsonb AS metadata
FROM quartermaster.ingress_sites
WHERE node_id = sqlc.arg(node_id)::text
ORDER BY site_id;
