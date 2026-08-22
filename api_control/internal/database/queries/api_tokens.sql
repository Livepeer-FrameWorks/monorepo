-- name: ValidateAPITokenHash :one
SELECT id::text, user_id::text, tenant_id::text, permissions
FROM commodore.api_tokens
WHERE token_value = sqlc.arg(token_hash)
  AND is_active = true
  AND (expires_at IS NULL OR expires_at > NOW());

-- name: TouchAPITokenLastUsed :exec
UPDATE commodore.api_tokens
SET last_used_at = NOW()
WHERE id = sqlc.arg(token_id)::uuid;

-- name: GetAPITokenUserContext :one
SELECT COALESCE(email::text, ''::text)::text AS email,
       COALESCE(role, ''::varchar)::varchar AS role,
       platform_operator
FROM commodore.users
WHERE id = sqlc.arg(user_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: InsertAPIToken :exec
INSERT INTO commodore.api_tokens
    (id, tenant_id, user_id, token_value, token_name, permissions, is_active, expires_at)
VALUES
    (sqlc.arg(token_id)::uuid,
     sqlc.arg(tenant_id)::uuid,
     sqlc.arg(user_id)::uuid,
     sqlc.arg(token_hash),
     sqlc.arg(token_name),
     sqlc.arg(permissions)::text[],
     true,
     sqlc.narg(expires_at)::timestamp);

-- name: CountAPITokensForUser :one
SELECT COUNT(*)::integer
FROM commodore.api_tokens
WHERE user_id = sqlc.arg(user_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ListAPITokensForward :many
SELECT id::text, token_name, permissions,
       CASE WHEN is_active AND (expires_at IS NULL OR expires_at > NOW()) THEN 'active' ELSE 'inactive' END::text AS status,
       last_used_at, expires_at, created_at
FROM commodore.api_tokens
WHERE user_id = sqlc.arg(user_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListAPITokensForwardAfter :many
SELECT id::text, token_name, permissions,
       CASE WHEN is_active AND (expires_at IS NULL OR expires_at > NOW()) THEN 'active' ELSE 'inactive' END::text AS status,
       last_used_at, expires_at, created_at
FROM commodore.api_tokens
WHERE user_id = sqlc.arg(user_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND (created_at, id) < (sqlc.arg(cursor_time)::timestamp, sqlc.arg(cursor_id)::uuid)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListAPITokensBackward :many
SELECT id::text, token_name, permissions,
       CASE WHEN is_active AND (expires_at IS NULL OR expires_at > NOW()) THEN 'active' ELSE 'inactive' END::text AS status,
       last_used_at, expires_at, created_at
FROM commodore.api_tokens
WHERE user_id = sqlc.arg(user_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY created_at ASC, id ASC
LIMIT sqlc.arg(row_limit);

-- name: ListAPITokensBackwardBefore :many
SELECT id::text, token_name, permissions,
       CASE WHEN is_active AND (expires_at IS NULL OR expires_at > NOW()) THEN 'active' ELSE 'inactive' END::text AS status,
       last_used_at, expires_at, created_at
FROM commodore.api_tokens
WHERE user_id = sqlc.arg(user_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND (created_at, id) > (sqlc.arg(cursor_time)::timestamp, sqlc.arg(cursor_id)::uuid)
ORDER BY created_at ASC, id ASC
LIMIT sqlc.arg(row_limit);

-- name: RevokeAPIToken :one
UPDATE commodore.api_tokens
SET is_active = false, updated_at = NOW()
WHERE id = sqlc.arg(token_id)::uuid
  AND user_id = sqlc.arg(user_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
RETURNING token_name;
