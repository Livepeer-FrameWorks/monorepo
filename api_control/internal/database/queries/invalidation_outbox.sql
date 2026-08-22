-- name: EnqueueInvalidation :one
INSERT INTO commodore.playback_policy_invalidation_outbox
    (tenant_id, reason, internal_names)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(reason), sqlc.arg(internal_names)::text::jsonb)
RETURNING id::text;

-- name: CompleteInvalidation :exec
UPDATE commodore.playback_policy_invalidation_outbox
SET status = 'completed', completed_at = NOW(), last_error = NULL,
    last_failed_clusters = NULL
WHERE id = sqlc.arg(id)::uuid AND status = 'pending';

-- name: FailInvalidation :exec
UPDATE commodore.playback_policy_invalidation_outbox
SET attempts = sqlc.arg(attempts),
    next_attempt_at = NOW() + (sqlc.arg(backoff_ms)::bigint * INTERVAL '1 millisecond'),
    last_error = sqlc.arg(last_error)::text,
    last_failed_clusters = sqlc.arg(last_failed_clusters)::text::jsonb
WHERE id = sqlc.arg(id)::uuid AND status = 'pending';

-- name: ClaimInvalidationBatch :many
SELECT id::text AS id, tenant_id::text AS tenant_id, reason, internal_names::text AS internal_names,
       attempts, CASE WHEN stream_id IS NULL THEN ''::text ELSE stream_id::text END AS stream_id,
       COALESCE(bundle_min_version, 0) AS bundle_min_version
FROM commodore.playback_policy_invalidation_outbox
WHERE status = 'pending' AND next_attempt_at <= NOW()
ORDER BY next_attempt_at
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg(batch_size);

-- name: LeaseInvalidation :exec
UPDATE commodore.playback_policy_invalidation_outbox
SET next_attempt_at = NOW() + (sqlc.arg(lease_ms)::bigint * INTERVAL '1 millisecond')
WHERE id = sqlc.arg(id)::uuid AND status = 'pending';
