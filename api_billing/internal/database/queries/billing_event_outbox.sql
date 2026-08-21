-- name: EnqueueBillingEventOutbox :one
INSERT INTO purser.billing_event_outbox
    (event_type, tenant_id, user_id, resource_type, resource_id, billing_event)
VALUES (
    sqlc.arg(event_type), sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(user_id), sqlc.arg(resource_type), sqlc.arg(resource_id),
    sqlc.arg(billing_event)::jsonb
)
RETURNING id;

-- name: EnqueueBillingEventOutboxNoReturn :exec
INSERT INTO purser.billing_event_outbox
    (event_type, tenant_id, user_id, resource_type, resource_id, billing_event)
VALUES (
    sqlc.arg(event_type), sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(user_id), sqlc.arg(resource_type), sqlc.arg(resource_id),
    sqlc.arg(billing_event)::jsonb
);

-- name: ClaimBillingEventOutboxCandidates :many
SELECT id, event_type, tenant_id, user_id, resource_type, resource_id,
       billing_event, attempts, created_at
FROM purser.billing_event_outbox
WHERE completed_at IS NULL
  AND (claimed_at IS NULL
       OR claimed_at < NOW() - (sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond'))
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg(batch_size)::integer;

-- name: MarkBillingEventOutboxClaimed :exec
UPDATE purser.billing_event_outbox
SET claimed_at = NOW()
WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: CompleteBillingEventOutbox :exec
UPDATE purser.billing_event_outbox
SET completed_at = NOW(), last_error = NULL
WHERE id = sqlc.arg(id)::text::uuid;

-- name: FailBillingEventOutbox :exec
UPDATE purser.billing_event_outbox
SET attempts = attempts + 1, last_error = sqlc.arg(last_error), claimed_at = NULL
WHERE id = sqlc.arg(id)::text::uuid;
