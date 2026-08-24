-- name: ListStaleCompletingVODs :many
SELECT a.artifact_hash, a.tenant_id::text AS tenant_id,
       COALESCE(a.user_id::text, '')::text AS user_id,
       COALESCE(a.size_bytes, 0)::bigint AS size_bytes,
       COALESCE(v.s3_key, '')::text AS s3_key,
       COALESCE(v.s3_upload_id, '')::text AS upload_id,
       COALESCE(v.processes_json, '')::text AS processes_json,
       COALESCE(a.backend_id, '')::text AS backend_id,
       COALESCE(v.vod_completion_descriptor::text, '')::text AS completion_descriptor,
       CASE WHEN COALESCE(a.last_sync_attempt, a.updated_at) < NOW() - sqlc.arg(fail_seconds)::bigint * INTERVAL '1 second'
            THEN true ELSE false END AS past_fail_grace
FROM foghorn.artifacts a
JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
WHERE a.artifact_type = 'vod'
  AND a.status = 'completing'
  AND COALESCE(v.s3_key, '') <> ''
  AND COALESCE(a.last_sync_attempt, a.updated_at) < NOW() - sqlc.arg(stale_seconds)::bigint * INTERVAL '1 second'
ORDER BY COALESCE(a.last_sync_attempt, a.updated_at)
LIMIT sqlc.arg(batch_limit);

-- name: MarkCompletingVODProcessing :execrows
UPDATE foghorn.artifacts
SET status = 'processing', storage_location = 's3', sync_status = 'synced',
    sync_error = NULL, last_sync_attempt = NOW(), frozen_at = COALESCE(frozen_at, NOW()),
    s3_url = COALESCE(s3_url, sqlc.narg(s3_url)), durable_backend_local = true,
    active_object_key = COALESCE(active_object_key,
        (SELECT vm.s3_key FROM foghorn.vod_metadata vm WHERE vm.artifact_hash = foghorn.artifacts.artifact_hash)),
    updated_at = NOW()
WHERE foghorn.artifacts.artifact_hash = sqlc.arg(artifact_hash)
  AND foghorn.artifacts.tenant_id = sqlc.arg(tenant_id)::uuid
  AND foghorn.artifacts.status = 'completing';

-- name: MarkCompletingVODFailed :execrows
UPDATE foghorn.artifacts
SET status = 'failed', sync_status = 'failed', sync_error = sqlc.arg(error_message),
    error_message = sqlc.arg(error_message), last_sync_attempt = NOW(), updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = 'completing';
