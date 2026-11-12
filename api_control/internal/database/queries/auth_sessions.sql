-- name: InsertAuthSession :one
INSERT INTO commodore.auth_sessions (
    tenant_id, user_id, auth_method, client_id, device_label, client_version,
    created_ip, last_ip, user_agent, idle_expires_at, absolute_expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8,
    LEAST(sqlc.arg(idle_expires_at)::timestamptz, sqlc.arg(absolute_expires_at)::timestamptz,
          NOW() + INTERVAL '720 hours',
          NOW() + CASE WHEN $4 IN ('cli', 'tray-mac') THEN INTERVAL '4320 hours' ELSE INTERVAL '2160 hours' END),
    LEAST(sqlc.arg(absolute_expires_at)::timestamptz,
          NOW() + CASE WHEN $4 IN ('cli', 'tray-mac') THEN INTERVAL '4320 hours' ELSE INTERVAL '2160 hours' END))
RETURNING *;

-- name: LockAuthSessionUser :one
SELECT id FROM commodore.users WHERE tenant_id = $1 AND id = $2 FOR UPDATE;

-- name: GetSessionPasswordHash :one
SELECT COALESCE(password_hash, '')::text FROM commodore.users
WHERE tenant_id = $1 AND id = $2;

-- name: ChangeSessionPassword :exec
UPDATE commodore.users
SET password_hash = $3, reset_token = NULL, reset_token_expires = NULL, updated_at = NOW()
WHERE tenant_id = $1 AND id = $2;

-- name: DeleteOtherLegacyRefreshTokens :exec
WITH RECURSIVE current_family AS (
    SELECT root.id, root.replaced_by FROM commodore.refresh_tokens root
    WHERE root.tenant_id = $1 AND root.user_id = $2 AND root.session_id = $3
    UNION
    SELECT r.id, r.replaced_by FROM current_family f
    JOIN commodore.refresh_tokens r ON r.tenant_id = $1 AND r.user_id = $2
      AND (r.id = f.replaced_by OR r.replaced_by = f.id)
    WHERE r.session_id IS NULL OR r.session_id = $3
)
DELETE FROM commodore.refresh_tokens r
WHERE r.tenant_id = $1 AND r.user_id = $2 AND r.session_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM current_family f WHERE f.id = r.id);

-- name: GetAuthSession :one
SELECT * FROM commodore.auth_sessions
WHERE tenant_id = $1 AND user_id = $2 AND id = $3;

-- name: LockAuthSession :one
SELECT * FROM commodore.auth_sessions
WHERE tenant_id = $1 AND user_id = $2 AND id = $3 FOR UPDATE;

-- name: ListAuthSessions :many
SELECT * FROM commodore.auth_sessions
WHERE tenant_id = $1 AND user_id = $2
  AND revoked_at IS NULL AND idle_expires_at > NOW() AND absolute_expires_at > NOW()
  AND (sqlc.narg(before_created_at)::timestamptz IS NULL OR
       (created_at, id) < (sqlc.narg(before_created_at)::timestamptz, sqlc.narg(before_id)::uuid))
ORDER BY created_at DESC, id DESC LIMIT sqlc.arg(page_limit);

-- name: CountAuthSessions :one
SELECT COUNT(*)::INTEGER FROM commodore.auth_sessions
WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL
  AND idle_expires_at > NOW() AND absolute_expires_at > NOW();

-- name: RenameAuthSession :execrows
UPDATE commodore.auth_sessions SET device_label = $4
WHERE tenant_id = $1 AND user_id = $2 AND id = $3 AND revoked_at IS NULL;

-- name: RevokeAuthSession :one
UPDATE commodore.auth_sessions SET revoked_at = clock_timestamp(), revoke_reason = $4
WHERE tenant_id = $1 AND user_id = $2 AND id = $3 AND revoked_at IS NULL
RETURNING *;

-- name: RevokeOtherAuthSessions :many
UPDATE commodore.auth_sessions SET revoked_at = clock_timestamp(), revoke_reason = $4
WHERE tenant_id = $1 AND user_id = $2 AND id <> $3 AND revoked_at IS NULL
  AND idle_expires_at > NOW() AND absolute_expires_at > NOW()
RETURNING *;

-- name: RevokeAllAuthSessions :many
UPDATE commodore.auth_sessions SET revoked_at = clock_timestamp(), revoke_reason = $3
WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL
RETURNING *;

-- name: TouchAuthSession :one
UPDATE commodore.auth_sessions
SET last_seen_at = NOW(), last_ip = $4,
    idle_expires_at = LEAST(NOW() + INTERVAL '720 hours', absolute_expires_at)
WHERE tenant_id = $1 AND user_id = $2 AND id = $3
  AND revoked_at IS NULL AND idle_expires_at > NOW() AND absolute_expires_at > NOW()
RETURNING *;

-- name: InsertSessionRefreshToken :one
INSERT INTO commodore.refresh_tokens (tenant_id, user_id, session_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4, $5) RETURNING id;

-- name: RevokeAuthSessionRefreshTokens :exec
WITH RECURSIVE family AS (
    SELECT root.id, root.replaced_by FROM commodore.refresh_tokens root
    WHERE root.tenant_id = $1 AND root.user_id = $2 AND root.session_id = $3
    UNION
    SELECT r.id, r.replaced_by FROM family f
    JOIN commodore.refresh_tokens r
      ON r.tenant_id = $1 AND r.user_id = $2
     AND (r.id = f.replaced_by OR r.replaced_by = f.id)
    WHERE r.session_id IS NULL OR r.session_id = $3
)
DELETE FROM commodore.refresh_tokens r USING family f
WHERE r.tenant_id = $1 AND r.user_id = $2 AND r.id = f.id;

-- name: LockAuthSessionRefreshTokens :many
SELECT id FROM commodore.refresh_tokens
WHERE tenant_id = $1 AND user_id = $2 AND session_id = $3
ORDER BY id FOR UPDATE;

-- name: LockUserRefreshTokensForRevocation :many
SELECT id FROM commodore.refresh_tokens
WHERE tenant_id = $1 AND user_id = $2 ORDER BY id FOR UPDATE;

-- name: ListAuthSessionRevocationsSince :many
-- Platform-wide delivery/backfill is the only cross-tenant session read.
SELECT tenant_id, user_id, id, revoked_at, revoke_reason
FROM commodore.auth_sessions WHERE revoked_at > $1 ORDER BY revoked_at, id;

-- name: GetRefreshTokenOwnerByHash :one
-- The credential hash resolves the owner before taking the user lock.
SELECT user_id, tenant_id FROM commodore.refresh_tokens
WHERE token_hash = $1 AND expires_at > NOW();

-- name: GetRefreshTokenSession :one
SELECT session_id FROM commodore.refresh_tokens
WHERE tenant_id = $1 AND user_id = $2 AND id = $3;

-- name: AdoptAuthSessionRefreshFamily :one
WITH RECURSIVE family AS (
    SELECT root.id, root.replaced_by, root.session_id, root.created_at, root.expires_at, COALESCE(root.revoked, false) AS revoked
    FROM commodore.refresh_tokens root
    WHERE root.tenant_id = $1 AND root.user_id = $2 AND root.id = $3
    UNION
    SELECT r.id, r.replaced_by, r.session_id, r.created_at, r.expires_at, COALESCE(r.revoked, false)
    FROM family f JOIN commodore.refresh_tokens r
      ON r.tenant_id = $1 AND r.user_id = $2
     AND (r.id = f.replaced_by OR r.replaced_by = f.id)
), owner AS (
    SELECT COALESCE(MIN(session_id::text)::uuid, MIN(id::text) FILTER (WHERE NOT revoked AND expires_at > NOW())::uuid) AS session_id,
           MIN(COALESCE(created_at AT TIME ZONE 'UTC', NOW())) AS created_at,
           MAX(expires_at AT TIME ZONE 'UTC') FILTER (WHERE NOT revoked) AS idle_expires_at
    FROM family HAVING COUNT(DISTINCT session_id) <= 1
), inserted AS (
    INSERT INTO commodore.auth_sessions (
        id, tenant_id, user_id, auth_method, client_id, device_label,
        created_at, last_seen_at, idle_expires_at, absolute_expires_at
    ) SELECT session_id, $1, $2, 'unknown', 'unknown', 'Signed in before sessions were tracked',
        created_at, NOW(), LEAST(idle_expires_at, created_at + INTERVAL '2160 hours'), created_at + INTERVAL '2160 hours'
    FROM owner WHERE session_id IS NOT NULL AND idle_expires_at IS NOT NULL
    ON CONFLICT (id) DO NOTHING RETURNING id
), attached AS (
    UPDATE commodore.refresh_tokens r SET session_id = o.session_id
    FROM family f CROSS JOIN owner o
    WHERE r.tenant_id = $1 AND r.user_id = $2 AND r.id = f.id AND r.session_id IS NULL
      AND (EXISTS (SELECT 1 FROM inserted s WHERE s.id = o.session_id) OR
           EXISTS (SELECT 1 FROM commodore.auth_sessions s WHERE s.tenant_id = $1 AND s.user_id = $2 AND s.id = o.session_id))
    RETURNING r.session_id
)
SELECT session_id FROM attached LIMIT 1;

-- name: GetSessionRefreshSuccessor :one
SELECT id, COALESCE(revoked, false)::boolean AS revoked, replaced_by
FROM commodore.refresh_tokens WHERE tenant_id = $1 AND user_id = $2 AND id = $3;

-- name: ListOldestAuthSessions :many
SELECT * FROM commodore.auth_sessions
WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL
  AND idle_expires_at > NOW() AND absolute_expires_at > NOW()
ORDER BY last_seen_at, created_at, id LIMIT $3 FOR UPDATE;
