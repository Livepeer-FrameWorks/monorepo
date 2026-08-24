-- name: ExpireStrandedCreationCommands :execrows
WITH stranded AS (
    SELECT c.request_id
    FROM foghorn.artifact_creation_commands c
    WHERE c.status = 'accepted'
      AND c.updated_at < NOW() - sqlc.arg(deadline_seconds)::bigint * INTERVAL '1 second'
      AND NOT EXISTS (
          SELECT 1 FROM foghorn.artifacts a
          WHERE a.artifact_hash = c.artifact_hash
            AND a.tenant_id = c.tenant_id
            AND a.artifact_type = c.kind
      )
    ORDER BY c.updated_at
    LIMIT sqlc.arg(batch_limit)
    FOR UPDATE SKIP LOCKED
)
UPDATE foghorn.artifact_creation_commands c
SET status = 'rejected', updated_at = NOW()
FROM stranded s
WHERE c.request_id = s.request_id AND c.status = 'accepted';

-- name: DeleteConsumedCreationCommands :execrows
WITH old_terminal AS (
    SELECT c.request_id
    FROM foghorn.artifact_creation_commands c
    WHERE c.status IN ('committed', 'rejected')
      AND c.consumed_at IS NOT NULL
      AND c.consumed_at < NOW() - sqlc.arg(retention_seconds)::bigint * INTERVAL '1 second'
    ORDER BY c.consumed_at
    LIMIT sqlc.arg(batch_limit)
    FOR UPDATE SKIP LOCKED
)
DELETE FROM foghorn.artifact_creation_commands c
USING old_terminal o
WHERE c.request_id = o.request_id;

-- name: CountStaleUnconsumedCreationCommands :one
SELECT COUNT(*)
FROM foghorn.artifact_creation_commands
WHERE status IN ('committed', 'rejected')
  AND consumed_at IS NULL
  AND updated_at < NOW() - sqlc.arg(retention_seconds)::bigint * INTERVAL '1 second';
