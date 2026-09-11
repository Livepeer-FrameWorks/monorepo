-- name: UpsertControlReplicaHeartbeat :exec
INSERT INTO foghorn.control_replicas (
    replica_id, release_version, placement_schema_version, placement_enforced, started_at, last_seen_at
) VALUES (
    sqlc.arg(replica_id), sqlc.arg(release_version), sqlc.arg(placement_schema_version), sqlc.arg(placement_enforced), NOW(), NOW()
)
ON CONFLICT (replica_id) DO UPDATE SET
    release_version = EXCLUDED.release_version,
    placement_schema_version = EXCLUDED.placement_schema_version,
    placement_enforced = EXCLUDED.placement_enforced,
    last_seen_at = NOW();

-- name: ReadControlCellPlacementCapability :one
SELECT COUNT(*)::bigint AS live_replicas,
       COALESCE(MIN(placement_schema_version), 0)::integer AS min_schema_version,
       COALESCE(BOOL_AND(placement_enforced), FALSE)::boolean AS all_enforced
FROM foghorn.control_replicas
WHERE last_seen_at > NOW() - (sqlc.arg(liveness_seconds)::integer * INTERVAL '1 second');

-- name: DeleteStaleControlReplicas :execrows
DELETE FROM foghorn.control_replicas
WHERE last_seen_at < NOW() - (sqlc.arg(retention_seconds)::integer * INTERVAL '1 second');
