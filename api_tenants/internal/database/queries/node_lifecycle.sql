-- name: MarkNodeEdgeInstancesOffline :exec
UPDATE quartermaster.service_instances si
SET health_status = 'unhealthy', status = 'offline',
    stopped_at = COALESCE(si.stopped_at, NOW()),
    last_health_check = NOW(), updated_at = NOW()
FROM quartermaster.services svc
WHERE svc.service_id = si.service_id
  AND (svc.type = 'edge' OR svc.type LIKE 'edge-%')
  AND si.node_id = sqlc.arg(node_id);
