-- name: EnqueueConfigSeedApplyAck :execrows
INSERT INTO foghorn.config_seed_apply_ack_outbox (
    node_id, cluster_id, seed_version, request_payload, result_signature,
    revision, pending, pending_since, attempts, next_attempt_at,
    last_attempt_at, last_error, delivered_at, created_at, updated_at
) VALUES (
    sqlc.arg(node_id), sqlc.arg(cluster_id), sqlc.arg(seed_version),
    sqlc.arg(request_payload), sqlc.arg(result_signature),
    1, true, NOW(), 0, NOW(), NULL, NULL, NULL, NOW(), NOW()
)
ON CONFLICT (node_id) DO UPDATE
SET cluster_id = EXCLUDED.cluster_id,
    seed_version = EXCLUDED.seed_version,
    request_payload = EXCLUDED.request_payload,
    result_signature = EXCLUDED.result_signature,
    revision = foghorn.config_seed_apply_ack_outbox.revision + 1,
    pending = true,
    pending_since = CASE
        WHEN foghorn.config_seed_apply_ack_outbox.pending
        THEN foghorn.config_seed_apply_ack_outbox.pending_since
        ELSE NOW()
    END,
    attempts = 0,
    next_attempt_at = NOW(),
    last_attempt_at = NULL,
    last_error = NULL,
    delivered_at = NULL,
    updated_at = NOW()
WHERE EXCLUDED.seed_version > foghorn.config_seed_apply_ack_outbox.seed_version
   OR (
       EXCLUDED.seed_version = foghorn.config_seed_apply_ack_outbox.seed_version
       AND (
           foghorn.config_seed_apply_ack_outbox.cluster_id IS DISTINCT FROM EXCLUDED.cluster_id
           OR foghorn.config_seed_apply_ack_outbox.result_signature IS DISTINCT FROM EXCLUDED.result_signature
       )
   )
   OR (
       NOT foghorn.config_seed_apply_ack_outbox.pending
       AND foghorn.config_seed_apply_ack_outbox.delivered_at IS NULL
       AND foghorn.config_seed_apply_ack_outbox.last_error IS NOT NULL
       AND EXCLUDED.seed_version >= foghorn.config_seed_apply_ack_outbox.seed_version
   );

-- name: GetConfigSeedApplyAckSeedVersion :one
SELECT seed_version
FROM foghorn.config_seed_apply_ack_outbox
WHERE node_id = sqlc.arg(node_id);

-- name: ClaimDueConfigSeedApplyAcks :many
WITH candidates AS (
    SELECT id
    FROM foghorn.config_seed_apply_ack_outbox
    WHERE pending
      AND next_attempt_at <= NOW()
      AND (lease_until IS NULL OR lease_until <= NOW())
    ORDER BY next_attempt_at, id
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
UPDATE foghorn.config_seed_apply_ack_outbox AS outbox
SET lease_owner = sqlc.arg(lease_owner),
    lease_until = NOW() + INTERVAL '30 seconds',
    last_attempt_at = NOW(),
    updated_at = NOW()
FROM candidates
WHERE outbox.id = candidates.id
RETURNING outbox.id, outbox.node_id, outbox.cluster_id,
          outbox.seed_version, outbox.request_payload, outbox.result_signature,
          outbox.revision, outbox.attempts;

-- name: SettleDeliveredConfigSeedApplyAck :execrows
UPDATE foghorn.config_seed_apply_ack_outbox
SET pending = false,
    lease_owner = NULL,
    lease_until = NULL,
    last_error = NULL,
    delivered_at = NOW(),
    updated_at = NOW()
WHERE id = sqlc.arg(id) AND revision = sqlc.arg(revision)
  AND lease_owner = sqlc.arg(lease_owner) AND pending;

-- name: QuarantineInvalidConfigSeedApplyAck :execrows
UPDATE foghorn.config_seed_apply_ack_outbox
SET pending = false,
    lease_owner = NULL,
    lease_until = NULL,
    last_error = sqlc.arg(last_error),
    delivered_at = NULL,
    updated_at = NOW()
WHERE id = sqlc.arg(id) AND revision = sqlc.arg(revision)
  AND lease_owner = sqlc.arg(lease_owner) AND pending;

-- name: RetryConfigSeedApplyAck :execrows
UPDATE foghorn.config_seed_apply_ack_outbox
SET attempts = attempts + 1,
    next_attempt_at = NOW() + LEAST(INTERVAL '5 minutes', INTERVAL '1 second' * (1 << LEAST(attempts, 8))),
    lease_owner = NULL,
    lease_until = NULL,
    last_error = sqlc.arg(last_error),
    updated_at = NOW()
WHERE id = sqlc.arg(id) AND revision = sqlc.arg(revision)
  AND lease_owner = sqlc.arg(lease_owner) AND pending;

-- name: ReleaseConfigSeedApplyAckLease :execrows
UPDATE foghorn.config_seed_apply_ack_outbox
SET lease_owner = NULL, lease_until = NULL, next_attempt_at = NOW(), updated_at = NOW()
WHERE id = sqlc.arg(id) AND lease_owner = sqlc.arg(lease_owner) AND pending;

-- name: GetConfigSeedApplyAckOutboxStats :one
SELECT
    (SELECT COUNT(*) FROM foghorn.config_seed_apply_ack_outbox WHERE pending)::bigint AS pending,
    COALESCE((
        SELECT EXTRACT(EPOCH FROM (NOW() - MIN(pending_since)))
        FROM foghorn.config_seed_apply_ack_outbox
        WHERE pending
    ), 0)::double precision AS oldest_pending_seconds,
    (SELECT COUNT(*)
     FROM foghorn.config_seed_apply_ack_outbox
     WHERE NOT pending AND delivered_at IS NULL AND last_error IS NOT NULL)::bigint AS quarantined;
