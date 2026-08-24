-- name: ListStaleAbortingVODs :many
SELECT a.artifact_hash,
       a.tenant_id::text AS tenant_id,
       COALESCE(a.user_id::text, '')::text AS user_id,
       COALESCE(v.s3_key, '')::text AS s3_key,
       COALESCE(v.s3_upload_id, '')::text AS upload_id,
       COALESCE(a.backend_id, '')::text AS backend_id
FROM foghorn.artifacts a
JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
WHERE a.artifact_type = 'vod'
  AND a.status = 'aborting'
  AND COALESCE(a.last_sync_attempt, a.updated_at) < NOW() - sqlc.arg(stale_seconds)::bigint * INTERVAL '1 second'
ORDER BY COALESCE(a.last_sync_attempt, a.updated_at)
LIMIT sqlc.arg(batch_limit);

-- name: MarkAbortingVODDeleted :execrows
UPDATE foghorn.artifacts
SET status = 'deleted', updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = 'aborting';

-- name: DeleteVODMetadata :exec
DELETE FROM foghorn.vod_metadata WHERE artifact_hash = $1;
