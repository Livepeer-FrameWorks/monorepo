-- name: EnqueueDelivery :exec
INSERT INTO lookout.notification_outbox (event_id, incident_id, tenant_id, channel, payload)
VALUES (
    sqlc.arg(event_id), sqlc.arg(incident_id), sqlc.narg(tenant_id)::uuid,
    sqlc.arg(channel), sqlc.arg(payload)
)
ON CONFLICT (event_id, channel) DO NOTHING;

-- name: ArmIncidentPublication :exec
-- Kafka publication for a tenant incident, keyed by the incident's opening
-- event. When an incident moves to another tenant the existing row (pending,
-- delivered, or failed) is re-armed for the new tenant; an in-flight lease is
-- dropped so its worker cannot settle the re-armed row. A row already armed
-- for this tenant is left untouched.
INSERT INTO lookout.notification_outbox (event_id, incident_id, tenant_id, channel, payload)
VALUES (
    sqlc.arg(event_id), sqlc.arg(incident_id), sqlc.arg(tenant_id)::uuid,
    'kafka', sqlc.arg(payload)
)
ON CONFLICT (event_id, channel) DO UPDATE
SET tenant_id = EXCLUDED.tenant_id,
    payload = EXCLUDED.payload,
    attempts = 0,
    next_attempt_at = NOW(),
    claimed_at = NULL,
    lease_token = NULL,
    last_error = NULL,
    delivered_at = NULL,
    failed_at = NULL,
    updated_at = NOW()
WHERE lookout.notification_outbox.incident_id = EXCLUDED.incident_id
  AND lookout.notification_outbox.tenant_id IS DISTINCT FROM EXCLUDED.tenant_id;

-- name: CancelPendingIncidentPublications :execrows
-- Drops unsettled Kafka publications of an incident that no longer belongs to
-- the tenant. Delivered and failed rows stay as delivery history until retention.
DELETE FROM lookout.notification_outbox
WHERE incident_id = sqlc.arg(incident_id)
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND channel = 'kafka'
  AND delivered_at IS NULL
  AND failed_at IS NULL;

-- name: ClaimDeliveryCandidates :many
SELECT id, event_id, incident_id, tenant_id, channel, payload, attempts
FROM lookout.notification_outbox
WHERE delivered_at IS NULL
  AND failed_at IS NULL
  AND next_attempt_at <= NOW()
  AND (claimed_at IS NULL
       OR claimed_at < NOW() - (sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond'))
ORDER BY next_attempt_at, created_at
LIMIT sqlc.arg(batch_size)::integer
FOR UPDATE SKIP LOCKED;

-- name: LeaseDelivery :execrows
UPDATE lookout.notification_outbox
SET claimed_at = NOW(),
    lease_token = sqlc.arg(lease_token)::uuid,
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND delivered_at IS NULL
  AND failed_at IS NULL;

-- name: CompleteDelivery :one
UPDATE lookout.notification_outbox
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
RETURNING incident_id, event_id, channel;

-- name: FailDelivery :one
-- Records a failed attempt. For operator channels the attempt that reaches
-- max_attempts settles the row as failed, so it is never claimed again. Kafka
-- rows keep retrying at the capped backoff: produce failures are platform
-- outages, and abandoning the row would drop the tenant's Skipper investigation.
UPDATE lookout.notification_outbox
SET attempts = attempts + 1,
    next_attempt_at = NOW() + (sqlc.arg(backoff_milliseconds)::bigint * interval '1 millisecond'),
    last_error = sqlc.arg(last_error),
    failed_at = CASE WHEN channel <> 'kafka' AND attempts + 1 >= sqlc.arg(max_attempts)::integer THEN NOW() END,
    claimed_at = NULL,
    lease_token = NULL,
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND lease_token = sqlc.arg(lease_token)::uuid
  AND delivered_at IS NULL
  AND failed_at IS NULL
RETURNING incident_id, channel, attempts, (failed_at IS NOT NULL)::boolean AS failed;

-- name: DeleteDeliveredOutboxRows :execrows
-- Retention across all tenants: removes one bounded batch of rows delivered
-- before the cutoff. It deletes only settled rows and returns no row data.
DELETE FROM lookout.notification_outbox
WHERE id IN (
    SELECT id FROM lookout.notification_outbox
    WHERE delivered_at < sqlc.arg(delivered_before)::timestamptz
    ORDER BY delivered_at
    LIMIT sqlc.arg(batch_size)::integer
);

-- name: DeleteFailedOutboxRows :execrows
-- Retention across all tenants: removes one bounded batch of rows that failed
-- before the cutoff. It deletes only settled rows and returns no row data.
DELETE FROM lookout.notification_outbox
WHERE id IN (
    SELECT id FROM lookout.notification_outbox
    WHERE failed_at < sqlc.arg(failed_before)::timestamptz
    ORDER BY failed_at
    LIMIT sqlc.arg(batch_size)::integer
);
