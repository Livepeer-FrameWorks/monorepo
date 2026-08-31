-- name: EnqueueManagedStreamPlacement :exec
INSERT INTO foghorn.managed_stream_placement_outbox (
    stream_id, tenant_id, cluster_id, desired_active, revision, attempts,
    next_attempt_at, last_attempt_at, created_at, updated_at
) VALUES (
    sqlc.arg(stream_id)::uuid, sqlc.arg(tenant_id)::uuid,
    sqlc.arg(cluster_id)::uuid, sqlc.arg(desired_active),
    1, 0, NOW(), NULL, NOW(), NOW()
)
ON CONFLICT (stream_id) DO UPDATE
SET tenant_id = EXCLUDED.tenant_id,
    cluster_id = EXCLUDED.cluster_id,
    desired_active = EXCLUDED.desired_active,
    revision = foghorn.managed_stream_placement_outbox.revision + 1,
    attempts = 0,
    next_attempt_at = NOW(),
    last_attempt_at = NULL,
    updated_at = NOW()
WHERE foghorn.managed_stream_placement_outbox.tenant_id IS DISTINCT FROM EXCLUDED.tenant_id
   OR foghorn.managed_stream_placement_outbox.cluster_id IS DISTINCT FROM EXCLUDED.cluster_id
   OR foghorn.managed_stream_placement_outbox.desired_active IS DISTINCT FROM EXCLUDED.desired_active;

-- name: ClaimDueManagedStreamPlacements :many
WITH candidates AS (
    SELECT id
    FROM foghorn.managed_stream_placement_outbox
    WHERE next_attempt_at <= NOW()
      AND (lease_until IS NULL OR lease_until <= NOW())
    ORDER BY next_attempt_at, id
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
UPDATE foghorn.managed_stream_placement_outbox AS outbox
SET lease_owner = sqlc.arg(lease_owner),
    lease_until = NOW() + INTERVAL '30 seconds',
    last_attempt_at = NOW(),
    updated_at = NOW()
FROM candidates
WHERE outbox.id = candidates.id
RETURNING outbox.id, outbox.stream_id::text AS stream_id,
          outbox.tenant_id::text AS tenant_id, outbox.cluster_id::text AS cluster_id,
          outbox.desired_active, outbox.revision, outbox.attempts;

-- name: DeleteDeliveredManagedStreamPlacement :execrows
DELETE FROM foghorn.managed_stream_placement_outbox
WHERE id = sqlc.arg(id) AND revision = sqlc.arg(revision)
  AND lease_owner = sqlc.arg(lease_owner);

-- name: RetryManagedStreamPlacement :execrows
UPDATE foghorn.managed_stream_placement_outbox
SET attempts = attempts + 1,
    last_attempt_at = NOW(),
    next_attempt_at = NOW() + LEAST(INTERVAL '5 minutes', INTERVAL '1 second' * (1 << LEAST(attempts, 8))),
    lease_owner = NULL,
    lease_until = NULL,
    updated_at = NOW()
WHERE id = sqlc.arg(id) AND revision = sqlc.arg(revision)
  AND lease_owner = sqlc.arg(lease_owner);

-- name: ReleaseManagedStreamPlacementLease :execrows
UPDATE foghorn.managed_stream_placement_outbox
SET lease_owner = NULL, lease_until = NULL, next_attempt_at = NOW(), updated_at = NOW()
WHERE id = sqlc.arg(id) AND lease_owner = sqlc.arg(lease_owner);
