-- name: EnqueuePushTargetStatus :exec
INSERT INTO foghorn.push_target_status_outbox (
    target_id, tenant_id, status, last_error, event_unix_millis, revision, attempts,
    next_attempt_at, last_attempt_at, created_at, updated_at
) VALUES (
    sqlc.arg(target_id)::uuid, sqlc.arg(tenant_id)::uuid, sqlc.arg(status),
    sqlc.narg(last_error), sqlc.arg(event_unix_millis), 1, 0, NOW(), NULL, NOW(), NOW()
)
ON CONFLICT (target_id) DO UPDATE
SET tenant_id = EXCLUDED.tenant_id,
    status = EXCLUDED.status,
    last_error = EXCLUDED.last_error,
    event_unix_millis = EXCLUDED.event_unix_millis,
    revision = foghorn.push_target_status_outbox.revision + 1,
    attempts = 0,
    next_attempt_at = NOW(),
    last_attempt_at = NULL,
    updated_at = NOW()
WHERE (
       EXCLUDED.event_unix_millis = 0
       AND foghorn.push_target_status_outbox.event_unix_millis = 0
       AND (
           foghorn.push_target_status_outbox.tenant_id IS DISTINCT FROM EXCLUDED.tenant_id
           OR foghorn.push_target_status_outbox.status IS DISTINCT FROM EXCLUDED.status
           OR foghorn.push_target_status_outbox.last_error IS DISTINCT FROM EXCLUDED.last_error
       )
   )
   OR EXCLUDED.event_unix_millis > foghorn.push_target_status_outbox.event_unix_millis
   OR (
       EXCLUDED.event_unix_millis = foghorn.push_target_status_outbox.event_unix_millis
       AND CASE EXCLUDED.status WHEN 'pushing' THEN 1 ELSE 2 END
           >= CASE foghorn.push_target_status_outbox.status WHEN 'pushing' THEN 1 ELSE 2 END
       AND (
           foghorn.push_target_status_outbox.tenant_id IS DISTINCT FROM EXCLUDED.tenant_id
           OR foghorn.push_target_status_outbox.status IS DISTINCT FROM EXCLUDED.status
           OR foghorn.push_target_status_outbox.last_error IS DISTINCT FROM EXCLUDED.last_error
       )
   );

-- name: ClaimDuePushTargetStatuses :many
WITH candidates AS (
    SELECT id
    FROM foghorn.push_target_status_outbox
    WHERE next_attempt_at <= NOW()
      AND (lease_until IS NULL OR lease_until <= NOW())
    ORDER BY next_attempt_at, id
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
UPDATE foghorn.push_target_status_outbox AS outbox
SET lease_owner = sqlc.arg(lease_owner),
    lease_until = NOW() + INTERVAL '30 seconds',
    last_attempt_at = NOW(),
    updated_at = NOW()
FROM candidates
WHERE outbox.id = candidates.id
RETURNING outbox.id, outbox.target_id::text AS target_id,
          outbox.tenant_id::text AS tenant_id, outbox.status, outbox.last_error,
          outbox.revision, outbox.attempts;

-- name: DeleteDeliveredPushTargetStatus :execrows
DELETE FROM foghorn.push_target_status_outbox
WHERE id = sqlc.arg(id) AND revision = sqlc.arg(revision)
  AND lease_owner = sqlc.arg(lease_owner);

-- name: RetryPushTargetStatus :execrows
UPDATE foghorn.push_target_status_outbox
SET attempts = attempts + 1,
    last_attempt_at = NOW(),
    next_attempt_at = NOW() + LEAST(INTERVAL '5 minutes', INTERVAL '1 second' * (1 << LEAST(attempts, 8))),
    lease_owner = NULL,
    lease_until = NULL,
    updated_at = NOW()
WHERE id = sqlc.arg(id) AND revision = sqlc.arg(revision)
  AND lease_owner = sqlc.arg(lease_owner);

-- name: ReleasePushTargetStatusLease :execrows
UPDATE foghorn.push_target_status_outbox
SET lease_owner = NULL, lease_until = NULL, next_attempt_at = NOW(), updated_at = NOW()
WHERE id = sqlc.arg(id) AND lease_owner = sqlc.arg(lease_owner);
