-- name: EnqueueOperatorActivity :exec
INSERT INTO lookout.operator_activity_outbox (
    source_event_id, event_type, tenant_id, channel, payload
)
VALUES (
    sqlc.arg(source_event_id), sqlc.arg(event_type), sqlc.narg(tenant_id)::uuid,
    sqlc.arg(channel), sqlc.arg(payload)
)
ON CONFLICT (source_event_id, channel) DO NOTHING;

-- name: ClaimOperatorActivityCandidates :many
-- Platform worker claim across all tenants; it returns only opaque delivery
-- snapshots, and the token-fenced updates below retain each row's tenant key.
SELECT id, source_event_id, event_type, tenant_id, channel, payload, attempts
FROM lookout.operator_activity_outbox
WHERE delivered_at IS NULL
  AND failed_at IS NULL
  AND next_attempt_at <= NOW()
  AND (claimed_at IS NULL
       OR claimed_at < NOW() - (sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond'))
ORDER BY next_attempt_at, created_at
LIMIT sqlc.arg(batch_size)::integer
FOR UPDATE SKIP LOCKED;

-- name: LeaseOperatorActivity :execrows
UPDATE lookout.operator_activity_outbox
SET claimed_at = NOW(),
    lease_token = sqlc.arg(lease_token)::uuid,
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND delivered_at IS NULL
  AND failed_at IS NULL;

-- name: CompleteOperatorActivity :one
UPDATE lookout.operator_activity_outbox
SET delivered_at = NOW(),
    claimed_at = NULL,
    lease_token = NULL,
    last_error = NULL,
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND lease_token = sqlc.arg(lease_token)::uuid
  AND delivered_at IS NULL
  AND failed_at IS NULL
RETURNING channel, event_type;

-- name: FailOperatorActivity :one
UPDATE lookout.operator_activity_outbox
SET attempts = attempts + 1,
    next_attempt_at = NOW() + (sqlc.arg(backoff_milliseconds)::bigint * interval '1 millisecond'),
    last_error = sqlc.arg(last_error),
    failed_at = CASE WHEN attempts + 1 >= sqlc.arg(max_attempts)::integer THEN NOW() END,
    claimed_at = NULL,
    lease_token = NULL,
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND lease_token = sqlc.arg(lease_token)::uuid
  AND delivered_at IS NULL
  AND failed_at IS NULL
RETURNING channel, event_type, attempts, (failed_at IS NOT NULL)::boolean AS failed;

-- name: DeleteDeliveredOperatorActivityRows :execrows
-- Platform retention across all tenants; only settled rows older than the
-- cutoff are removed, and no tenant data is returned.
DELETE FROM lookout.operator_activity_outbox
WHERE id IN (
    SELECT id FROM lookout.operator_activity_outbox
    WHERE delivered_at < sqlc.arg(delivered_before)::timestamptz
    ORDER BY delivered_at
    LIMIT sqlc.arg(batch_size)::integer
);

-- name: DeleteFailedOperatorActivityRows :execrows
-- Platform retention across all tenants; only terminal rows older than the
-- cutoff are removed, and no tenant data is returned.
DELETE FROM lookout.operator_activity_outbox
WHERE id IN (
    SELECT id FROM lookout.operator_activity_outbox
    WHERE failed_at < sqlc.arg(failed_before)::timestamptz
    ORDER BY failed_at
    LIMIT sqlc.arg(batch_size)::integer
);
