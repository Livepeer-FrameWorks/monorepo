-- name: ResetStaleFreezeAttempts :many
WITH stale AS (
    SELECT artifact_hash,
           COALESCE(sync_object_key, '')::text AS canonical_key,
           COALESCE(sync_request_id, '')::text AS attempt_id
    FROM foghorn.artifacts
    WHERE sync_status = 'in_progress'
      AND storage_location = 'freezing'
      AND status NOT IN ('deleted', 'expired', 'aborted')
      AND sync_request_id IS NOT NULL
      AND sync_node_id IS NOT NULL
      AND COALESCE(last_sync_attempt, updated_at) < NOW() - sqlc.arg(stale_seconds)::bigint * INTERVAL '1 second'
    FOR UPDATE
)
UPDATE foghorn.artifacts a
SET storage_location = 'local',
    sync_status = 'failed',
    sync_error = 'sync attempt timed out; recovered for retry',
    sync_request_id = NULL,
    sync_node_id = NULL,
    updated_at = NOW()
FROM stale
WHERE a.artifact_hash = stale.artifact_hash
RETURNING stale.canonical_key, stale.attempt_id;
