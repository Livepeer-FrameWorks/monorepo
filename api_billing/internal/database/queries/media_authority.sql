-- name: EnqueueMediaAuthorityRefresh :execrows
INSERT INTO purser.media_authority_refresh_outbox (
    source_event_id, tenant_id, reason
) VALUES (
    sqlc.arg(source_event_id), sqlc.arg(tenant_id)::uuid, sqlc.arg(reason)
)
ON CONFLICT (source_event_id) DO NOTHING;

-- name: ClaimMediaAuthorityRefreshBatch :many
WITH candidates AS (
    SELECT id
    FROM purser.media_authority_refresh_outbox
    WHERE status <> 'completed'
      AND next_attempt_at <= NOW()
      AND (lease_expires_at IS NULL OR lease_expires_at <= NOW())
    ORDER BY next_attempt_at, created_at
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
UPDATE purser.media_authority_refresh_outbox AS refresh
SET status = 'delivering', attempts = refresh.attempts + 1,
    lease_expires_at = NOW() + sqlc.arg(lease_ms)::bigint * INTERVAL '1 millisecond',
    updated_at = NOW()
FROM candidates
WHERE refresh.id = candidates.id
RETURNING refresh.id::text AS id, refresh.source_event_id,
          refresh.tenant_id::text AS tenant_id, refresh.reason, refresh.attempts,
          refresh.revision;

-- name: CompleteMediaAuthorityRefresh :execrows
UPDATE purser.media_authority_refresh_outbox
SET status = 'completed', completed_at = NOW(), lease_expires_at = NULL,
    last_error = NULL, updated_at = NOW()
WHERE id = sqlc.arg(id)::uuid AND status = 'delivering'
  AND revision = sqlc.arg(revision);

-- name: FailMediaAuthorityRefresh :execrows
UPDATE purser.media_authority_refresh_outbox
SET status = 'pending', next_attempt_at = sqlc.arg(next_attempt_at),
    lease_expires_at = NULL, last_error = sqlc.arg(last_error), updated_at = NOW()
WHERE id = sqlc.arg(id)::uuid AND status = 'delivering'
  AND revision = sqlc.arg(revision);
