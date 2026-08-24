-- name: TouchDVRRecordingNode :exec
INSERT INTO foghorn.artifact_nodes
    (artifact_hash, node_id, last_seen_at, is_orphaned, cached_at)
VALUES (sqlc.arg(artifact_hash), sqlc.arg(node_id), NOW(), false, NOW())
ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
    last_seen_at = NOW(),
    is_orphaned = false,
    cached_at = COALESCE(foghorn.artifact_nodes.cached_at, NOW());

-- name: GetDVRTenantAndStream :one
SELECT tenant_id::text, stream_internal_name
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'dvr';

-- name: GetDVREffectiveWindowSeconds :one
SELECT dvr_window_seconds
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'dvr';

-- name: GetDVRDispatchStatus :one
SELECT status
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'dvr'
LIMIT 1;

-- name: ListDVRDispatchNodes :many
SELECT node_id, COALESCE(is_orphaned, false)::boolean AS is_orphaned
FROM foghorn.artifact_nodes
WHERE artifact_hash = sqlc.arg(artifact_hash);
