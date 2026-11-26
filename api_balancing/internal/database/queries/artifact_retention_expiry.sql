-- name: ListExpiredArtifacts :many
SELECT artifact_hash, artifact_type, stream_internal_name, tenant_id::text AS tenant_id,
       COALESCE(user_id::text, '')::text AS user_id, size_bytes, retention_until, started_at, ended_at, manifest_path
FROM foghorn.artifacts
WHERE status IN ('completed', 'completed_partial', 'ready', 'failed')
  AND federated_pointer = false
  AND (
      (retention_until IS NOT NULL AND retention_until < NOW())
      OR (artifact_type <> 'dvr'
          AND COALESCE(origin_type, '') <> 'dvr_chapter'
          AND retention_until IS NULL
          AND status = 'failed'
          AND created_at < NOW() - make_interval(days => sqlc.arg(retention_days)::integer))
  )
LIMIT 500;

-- name: ExpireArtifactIfStillEligible :one
UPDATE foghorn.artifacts
SET status = 'deleted', updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND status IN ('completed', 'completed_partial', 'ready', 'failed')
  AND federated_pointer = false
  AND (
      (retention_until IS NOT NULL AND retention_until < NOW())
      OR (artifact_type <> 'dvr'
          AND COALESCE(origin_type, '') <> 'dvr_chapter'
          AND retention_until IS NULL
          AND status = 'failed'
          AND created_at < NOW() - make_interval(days => sqlc.arg(retention_days)::integer))
  )
RETURNING tenant_id::text;

-- name: ListMediaRecoveryTenants :many
SELECT DISTINCT tenant_id::text AS tenant_id FROM foghorn.artifacts
WHERE tenant_id>sqlc.arg(after_tenant_id)::text::uuid AND federated_pointer=false
 AND status IN ('completed','completed_partial','ready','failed') AND COALESCE(ended_at,created_at)<NOW()-INTERVAL '30 days'
ORDER BY tenant_id LIMIT 20;

-- name: ListMediaRecoveryArtifacts :many
SELECT artifact_hash,artifact_type,stream_internal_name,tenant_id::text AS tenant_id,
 COALESCE(user_id::text,'')::text AS user_id,size_bytes,retention_until
FROM foghorn.artifacts
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND federated_pointer=false
 AND created_at<=sqlc.arg(cutoff_at)::timestamptz AND COALESCE(ended_at,created_at)<NOW()-INTERVAL '30 days' AND status IN ('completed','completed_partial','ready','failed')
ORDER BY artifact_hash LIMIT 500;

-- name: ExpireMediaRecoveryArtifact :one
-- The caller holds the serialized Purser expiry receipt for this tenant and cutoff.
UPDATE foghorn.artifacts SET status='deleted',updated_at=NOW()
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND artifact_hash=sqlc.arg(artifact_hash)
 AND created_at<=sqlc.arg(cutoff_at)::timestamptz AND COALESCE(ended_at,created_at)<NOW()-INTERVAL '30 days' AND federated_pointer=false
 AND status IN ('completed','completed_partial','ready','failed')
RETURNING tenant_id::text;
