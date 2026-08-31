-- name: EnqueueSigningKeyUse :exec
INSERT INTO foghorn.signing_key_use_outbox (
    tenant_id, kid, revision, attempts, next_attempt_at, last_attempt_at,
    lease_owner, lease_until, observed_at, created_at, updated_at
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(kid), 1, 0, NOW(), NULL,
    NULL, NULL, NOW(), NOW(), NOW()
)
ON CONFLICT (tenant_id, kid) DO UPDATE
SET revision = foghorn.signing_key_use_outbox.revision + 1,
    attempts = 0,
    next_attempt_at = NOW(),
    last_attempt_at = NULL,
    lease_owner = NULL,
    lease_until = NULL,
    observed_at = NOW(),
    updated_at = NOW();

-- name: ClaimDueSigningKeyUses :many
WITH candidates AS (
    SELECT id
    FROM foghorn.signing_key_use_outbox
    WHERE next_attempt_at <= NOW()
      AND (lease_until IS NULL OR lease_until <= NOW())
    ORDER BY next_attempt_at, id
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
UPDATE foghorn.signing_key_use_outbox AS outbox
SET lease_owner = sqlc.arg(lease_owner),
    lease_until = NOW() + INTERVAL '30 seconds',
    last_attempt_at = NOW(),
    updated_at = NOW()
FROM candidates
WHERE outbox.id = candidates.id
RETURNING outbox.id, outbox.tenant_id::text AS tenant_id, outbox.kid,
          outbox.revision, outbox.attempts;

-- name: DeleteDeliveredSigningKeyUse :execrows
DELETE FROM foghorn.signing_key_use_outbox
WHERE id = sqlc.arg(id) AND revision = sqlc.arg(revision)
  AND lease_owner = sqlc.arg(lease_owner);

-- name: RetrySigningKeyUse :execrows
UPDATE foghorn.signing_key_use_outbox
SET attempts = attempts + 1,
    last_attempt_at = NOW(),
    next_attempt_at = NOW() + LEAST(INTERVAL '5 minutes', INTERVAL '1 second' * (1 << LEAST(attempts, 8))),
    lease_owner = NULL,
    lease_until = NULL,
    updated_at = NOW()
WHERE id = sqlc.arg(id) AND revision = sqlc.arg(revision)
  AND lease_owner = sqlc.arg(lease_owner);

-- name: ReleaseSigningKeyUseLease :execrows
UPDATE foghorn.signing_key_use_outbox
SET lease_owner = NULL, lease_until = NULL, next_attempt_at = NOW(), updated_at = NOW()
WHERE id = sqlc.arg(id) AND lease_owner = sqlc.arg(lease_owner);
