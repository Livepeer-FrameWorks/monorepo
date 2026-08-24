-- name: OverrideFinalizedArtifactRetention :execrows
UPDATE foghorn.artifacts
SET retention_until = sqlc.narg(retention_until)
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id::text = sqlc.arg(tenant_id)
  AND artifact_type = sqlc.arg(artifact_type)
  AND status IN ('completed', 'completed_partial', 'ready', 'failed');

-- name: GetFinalizedArtifactEndedAt :one
SELECT ended_at
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id::text = sqlc.arg(tenant_id)
  AND artifact_type = sqlc.arg(artifact_type)
  AND status IN ('completed', 'completed_partial', 'ready', 'failed')
  AND ended_at IS NOT NULL;
