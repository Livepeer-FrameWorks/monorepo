-- name: ClaimStagingCleanupItems :many
UPDATE foghorn.staging_cleanup_queue q
SET leased_until = NOW() + sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second',
    lease_token = gen_random_uuid()::text
WHERE q.object_key IN (
    SELECT object_key FROM foghorn.staging_cleanup_queue
    WHERE next_attempt_at <= NOW()
      AND (leased_until IS NULL OR leased_until <= NOW())
    ORDER BY next_attempt_at
    LIMIT sqlc.arg(batch_limit)
    FOR UPDATE SKIP LOCKED
)
RETURNING q.object_key, q.attempts, q.lease_token, COALESCE(q.backend_id, '')::text AS backend_id;

-- name: FailStagingCleanupItem :exec
UPDATE foghorn.staging_cleanup_queue
SET attempts = attempts + 1,
    next_attempt_at = NOW() + sqlc.arg(backoff_seconds)::bigint * INTERVAL '1 second',
    leased_until = NULL, lease_token = NULL, last_error = sqlc.arg(last_error)::text
WHERE object_key = sqlc.arg(object_key) AND lease_token = sqlc.arg(lease_token)::text;

-- name: DeleteStagingCleanupItem :execrows
DELETE FROM foghorn.staging_cleanup_queue
WHERE object_key = sqlc.arg(object_key) AND lease_token = sqlc.arg(lease_token);
