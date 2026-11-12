-- name: ListExpiredSessionRetentionKeys :many
-- Global indexed metadata discovery; mutations use the returned full owner.
SELECT id, tenant_id, user_id, idle_expires_at AS ended_at
FROM commodore.auth_sessions
WHERE idle_expires_at < NOW() - INTERVAL '720 hours'
  AND (idle_expires_at, id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid)
ORDER BY idle_expires_at, id LIMIT sqlc.arg(batch_size);

-- name: ListRevokedSessionRetentionKeys :many
SELECT id, tenant_id, user_id, revoked_at::timestamptz AS ended_at
FROM commodore.auth_sessions
WHERE revoked_at < NOW() - INTERVAL '720 hours'
  AND (revoked_at, id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid)
ORDER BY revoked_at, id LIMIT sqlc.arg(batch_size);

-- name: ListUnassignedRefreshRetentionKeys :many
SELECT id, tenant_id, user_id, expires_at AS ended_at
FROM commodore.refresh_tokens
WHERE session_id IS NULL
  AND expires_at < (NOW() AT TIME ZONE 'UTC') - INTERVAL '720 hours'
  AND (expires_at, id) > (sqlc.arg(after_at)::timestamp, sqlc.arg(after_id)::uuid)
ORDER BY expires_at, id LIMIT sqlc.arg(batch_size);

-- name: LockRetainedAuthSession :one
SELECT id FROM commodore.auth_sessions
WHERE tenant_id = $1 AND user_id = $2 AND id = $3
  AND (idle_expires_at < NOW() - INTERVAL '720 hours' OR revoked_at < NOW() - INTERVAL '720 hours')
FOR UPDATE SKIP LOCKED;

-- name: LockRetainedSessionTokenKeys :many
SELECT id FROM commodore.refresh_tokens
WHERE tenant_id = $1 AND user_id = $2 AND session_id = $3
ORDER BY id LIMIT sqlc.arg(batch_size) FOR UPDATE SKIP LOCKED;

-- name: HasLiveUnassignedRefreshToken :one
-- A live legacy successor can depend on an older ancestor for its original age.
SELECT EXISTS(SELECT 1 FROM commodore.refresh_tokens
WHERE tenant_id = $1 AND user_id = $2 AND session_id IS NULL
  AND COALESCE(revoked, false) = false AND expires_at > (NOW() AT TIME ZONE 'UTC'))::boolean;

-- name: DeleteRetainedSessionToken :execrows
DELETE FROM commodore.refresh_tokens
WHERE tenant_id = $1 AND user_id = $2 AND session_id = $3 AND id = $4;

-- name: DeleteRetainedAuthSession :execrows
DELETE FROM commodore.auth_sessions s
WHERE s.tenant_id = $1 AND s.user_id = $2 AND s.id = $3
  AND (s.idle_expires_at < NOW() - INTERVAL '720 hours' OR s.revoked_at < NOW() - INTERVAL '720 hours')
  AND NOT EXISTS(SELECT 1 FROM commodore.refresh_tokens r
      WHERE r.tenant_id = $1 AND r.user_id = $2 AND r.session_id = $3);

-- name: LockRetainedUnassignedToken :one
SELECT id FROM commodore.refresh_tokens
WHERE tenant_id = $1 AND user_id = $2 AND id = $3 AND session_id IS NULL
  AND expires_at < (NOW() AT TIME ZONE 'UTC') - INTERVAL '720 hours'
FOR UPDATE SKIP LOCKED;

-- name: DeleteRetainedUnassignedToken :execrows
DELETE FROM commodore.refresh_tokens
WHERE tenant_id = $1 AND user_id = $2 AND id = $3 AND session_id IS NULL
  AND expires_at < (NOW() AT TIME ZONE 'UTC') - INTERVAL '720 hours';
