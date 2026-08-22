-- name: ListHealthPollCandidates :many
SELECT si.instance_id,
       si.service_id,
       si.cluster_id,
       COALESCE(si.protocol, '')::text AS protocol,
       COALESCE(si.advertise_host, '')::text AS advertise_host,
       si.port,
       COALESCE(si.health_endpoint_override, s.health_check_path, '')::text AS path,
       si.last_health_check,
       COALESCE(s.protocol, '')::text AS default_protocol,
       COALESCE(assigned.cluster_id, '')::text AS assigned_cluster_id,
       COALESCE(assigned.base_url, '')::text AS assigned_base_url
FROM quartermaster.service_instances si
JOIN quartermaster.services s ON si.service_id = s.service_id
LEFT JOIN LATERAL (
    SELECT sca.cluster_id, c.base_url
    FROM quartermaster.service_cluster_assignments sca
    JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = sca.cluster_id
    WHERE sca.service_instance_id = si.id
      AND sca.is_active = true
    ORDER BY sca.cluster_id
    LIMIT 1
) assigned ON true
WHERE si.status IN ('running', 'starting')
  AND s.type <> 'edge'
  AND s.type NOT LIKE 'edge-%'
  AND (si.last_health_check IS NULL OR si.last_health_check < sqlc.arg(cutoff)::timestamp)
ORDER BY COALESCE(si.last_health_check, si.created_at) ASC
LIMIT sqlc.arg(batch_size)::integer;

-- name: PersistServiceHealthStatus :one
WITH previous AS (
    SELECT instance_id, health_status AS old_status, service_id
    FROM quartermaster.service_instances
    WHERE instance_id = sqlc.arg(instance_id)::text
)
UPDATE quartermaster.service_instances si
SET health_status = sqlc.arg(status)::text,
    last_health_check = NOW(),
    updated_at = NOW()
FROM previous
WHERE si.instance_id = previous.instance_id
RETURNING COALESCE(previous.old_status, '')::text AS old_status,
          previous.service_id;

-- name: ListGRPCHealthWatchCandidates :many
SELECT si.instance_id,
       si.service_id,
       COALESCE(si.advertise_host, '')::text AS advertise_host,
       si.port,
       COALESCE(si.protocol, '')::text AS protocol,
       COALESCE(s.protocol, '')::text AS default_protocol,
       COALESCE(assigned.cluster_id, '')::text AS assigned_cluster_id,
       COALESCE(assigned.base_url, '')::text AS assigned_base_url
FROM quartermaster.service_instances si
JOIN quartermaster.services s ON si.service_id = s.service_id
LEFT JOIN LATERAL (
    SELECT sca.cluster_id, c.base_url
    FROM quartermaster.service_cluster_assignments sca
    JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = sca.cluster_id
    WHERE sca.service_instance_id = si.id
      AND sca.is_active = true
    ORDER BY sca.cluster_id
    LIMIT 1
) assigned ON true
WHERE si.status IN ('running', 'starting');
