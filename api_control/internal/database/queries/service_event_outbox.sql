-- name: EnqueueServiceEvent :one
INSERT INTO commodore.service_event_outbox
    (event_type, tenant_id, user_id, resource_type, resource_id, payload)
VALUES (sqlc.arg(event_type), sqlc.arg(tenant_id)::uuid, sqlc.arg(user_id),
        sqlc.arg(resource_type), sqlc.arg(resource_id), sqlc.arg(payload)::text::jsonb)
RETURNING id::text;

-- name: ClaimServiceEventOutboxBatch :many
SELECT id::text AS id, payload::text AS payload, attempts, created_at
FROM commodore.service_event_outbox
WHERE completed_at IS NULL
  AND (claimed_at IS NULL OR claimed_at < NOW() - (sqlc.arg(lease_interval)::text)::interval)
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg(batch_size);

-- name: MarkServiceEventOutboxClaimed :exec
UPDATE commodore.service_event_outbox
SET claimed_at = NOW()
WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: CompleteServiceEventOutbox :exec
UPDATE commodore.service_event_outbox
SET completed_at = NOW(), last_error = NULL
WHERE id = sqlc.arg(id)::uuid;

-- name: FailServiceEventOutbox :exec
UPDATE commodore.service_event_outbox
SET attempts = sqlc.arg(attempts), last_error = sqlc.arg(last_error), claimed_at = NULL
WHERE id = sqlc.arg(id)::uuid;
