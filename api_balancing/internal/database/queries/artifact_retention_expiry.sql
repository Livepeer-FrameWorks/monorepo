-- name: ListExpiredArtifacts :many
SELECT artifact_hash, artifact_type, stream_internal_name, tenant_id::text AS tenant_id,
       COALESCE(user_id::text, '')::text AS user_id, size_bytes, retention_until, started_at, ended_at, manifest_path
FROM foghorn.artifacts
WHERE status IN ('completed', 'completed_partial', 'ready', 'failed')
  AND (
      (retention_until IS NOT NULL AND retention_until < NOW())
      OR (artifact_type <> 'dvr'
          AND COALESCE(origin_type, '') <> 'dvr_chapter'
          AND retention_until IS NULL
          AND created_at < NOW() - make_interval(days => sqlc.arg(retention_days)::integer))
  )
LIMIT 500;

-- name: ExpireArtifactIfStillEligible :one
UPDATE foghorn.artifacts
SET status = 'deleted', updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND status IN ('completed', 'completed_partial', 'ready', 'failed')
  AND (
      (retention_until IS NOT NULL AND retention_until < NOW())
      OR (artifact_type <> 'dvr'
          AND COALESCE(origin_type, '') <> 'dvr_chapter'
          AND retention_until IS NULL
          AND created_at < NOW() - make_interval(days => sqlc.arg(retention_days)::integer))
  )
RETURNING tenant_id::text;
