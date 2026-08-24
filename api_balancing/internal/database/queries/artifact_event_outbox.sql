-- name: EnqueueArtifactEvent :exec
INSERT INTO foghorn.artifact_event_outbox
    (event_kind, tenant_id, stream_id, artifact_id, payload)
VALUES (
    sqlc.arg(event_kind),
    NULLIF(sqlc.arg(tenant_id)::text, '')::uuid,
    sqlc.arg(stream_id),
    sqlc.arg(artifact_id),
    sqlc.arg(payload)::jsonb
);

-- name: ClaimArtifactEvents :many
SELECT id::text, event_kind, COALESCE(tenant_id::text, '')::text AS tenant_id, stream_id, artifact_id,
       payload::text, attempts, created_at
FROM foghorn.artifact_event_outbox
WHERE completed_at IS NULL
  AND (claimed_at IS NULL OR claimed_at < NOW() - sqlc.arg(lease_seconds)::double precision * INTERVAL '1 second')
  AND (next_retry_at IS NULL OR next_retry_at <= NOW())
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg(batch_limit);

-- name: MarkArtifactEventsClaimed :exec
UPDATE foghorn.artifact_event_outbox
SET claimed_at = NOW()
WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: MarkArtifactEventCompleted :exec
UPDATE foghorn.artifact_event_outbox
SET completed_at = NOW(), last_error = NULL
WHERE id = sqlc.arg(id)::uuid;

-- name: RecordArtifactEventFailure :exec
UPDATE foghorn.artifact_event_outbox
SET attempts = sqlc.arg(attempts),
    last_error = sqlc.arg(last_error),
    claimed_at = NULL,
    next_retry_at = NOW() + sqlc.arg(retry_milliseconds)::double precision * INTERVAL '1 millisecond'
WHERE id = sqlc.arg(id)::uuid;
