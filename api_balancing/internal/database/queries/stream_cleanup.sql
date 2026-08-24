-- name: ClaimStreamCleanupObligations :many
UPDATE foghorn.stream_cleanup_obligation o
SET leased_until = NOW() + sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second',
    lease_token = gen_random_uuid()::text
FROM (
    SELECT oo.asset_key
    FROM foghorn.stream_cleanup_obligation oo
    WHERE oo.status = 'pending'
      AND oo.next_attempt_at <= NOW()
      AND (oo.leased_until IS NULL OR oo.leased_until <= NOW())
    ORDER BY oo.next_attempt_at
    LIMIT sqlc.arg(batch_limit)
    FOR UPDATE SKIP LOCKED
) sel
WHERE o.asset_key = sel.asset_key
RETURNING o.asset_key, o.tenant_id::text AS tenant_id,
          COALESCE(o.backend_id, '')::text AS backend_id, o.lease_token;

-- name: FinalizeStreamCleanupObligation :execrows
UPDATE foghorn.stream_cleanup_obligation
SET status = 'cleaned', cleaned_at = NOW()
WHERE asset_key = sqlc.arg(asset_key)
  AND lease_token = sqlc.arg(lease_token)
  AND status = 'pending'
  AND enqueued_at + sqlc.arg(window_seconds)::bigint * INTERVAL '1 second' <= NOW();

-- name: ArmStreamCleanupSecondSweep :exec
UPDATE foghorn.stream_cleanup_obligation
SET first_swept_at = COALESCE(first_swept_at, NOW()),
    next_attempt_at = enqueued_at + sqlc.arg(window_seconds)::bigint * INTERVAL '1 second',
    leased_until = NULL, lease_token = NULL, attempts = 0, last_error = NULL
WHERE asset_key = sqlc.arg(asset_key)
  AND lease_token = sqlc.arg(lease_token)
  AND status = 'pending';

-- name: FailStreamCleanupObligation :exec
UPDATE foghorn.stream_cleanup_obligation
SET attempts = attempts + 1,
    next_attempt_at = NOW() + LEAST(attempts + 1, 30) * sqlc.arg(backoff_seconds)::bigint * INTERVAL '1 second',
    leased_until = NULL, lease_token = NULL, last_error = sqlc.arg(last_error)
WHERE asset_key = sqlc.arg(asset_key) AND lease_token = sqlc.arg(lease_token);
