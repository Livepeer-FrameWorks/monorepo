-- name: GetWalletIdentityByAddress :one
SELECT tenant_id, user_id
FROM commodore.wallet_identities
WHERE chain_type = $1 AND wallet_address = $2;

-- name: TouchWalletIdentityAuth :exec
UPDATE commodore.wallet_identities
SET last_auth_at = NOW()
WHERE chain_type = $1 AND wallet_address = $2;

-- name: InsertWalletUser :exec
INSERT INTO commodore.users (
    id, tenant_id, email, password_hash,
    role, is_active, verified,
    first_name, last_name,
    created_at, updated_at
)
VALUES ($1, $2, NULL, '', 'owner', true, true, $3, '', NOW(), NOW());

-- name: InsertWalletIdentity :exec
INSERT INTO commodore.wallet_identities (
    id, wallet_address, chain_type, tenant_id, user_id, created_at, last_auth_at
)
VALUES (gen_random_uuid(), $1, $2, $3, $4, NOW(), NOW());

-- name: GetLoginUserByEmail :one
SELECT
    id,
    tenant_id,
    COALESCE(email::text, '')::text AS email,
    COALESCE(password_hash, '')::text AS password_hash,
    first_name,
    last_name,
    COALESCE(role, 'member')::text AS role,
    COALESCE(permissions, ARRAY[]::text[])::text[] AS permissions,
    COALESCE(is_active, false)::boolean AS is_active,
    COALESCE(verified, false)::boolean AS verified,
    created_at,
    updated_at,
    platform_operator
FROM commodore.users
WHERE email = $1;

-- name: TouchUserLastLogin :exec
UPDATE commodore.users
SET last_login_at = NOW()
WHERE id = $1 AND tenant_id = $2;

-- name: InsertRefreshToken :exec
INSERT INTO commodore.refresh_tokens (tenant_id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4);

-- name: FindUserIDByEmail :one
SELECT id
FROM commodore.users
WHERE email = $1;

-- name: CountUsersForTenant :one
SELECT COUNT(*)::integer
FROM commodore.users
WHERE tenant_id = $1;

-- name: InsertRegisteredUser :exec
INSERT INTO commodore.users (
    id, tenant_id, email, password_hash, first_name, last_name,
    role, permissions, is_active, verified, verification_token, token_expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true, false, $9, $10);

-- name: GetUserProfile :one
SELECT
    id,
    tenant_id,
    COALESCE(email::text, '')::text AS email,
    first_name,
    last_name,
    COALESCE(role, 'member')::text AS role,
    COALESCE(permissions, ARRAY[]::text[])::text[] AS permissions,
    COALESCE(is_active, false)::boolean AS is_active,
    COALESCE(verified, false)::boolean AS verified,
    last_login_at,
    created_at,
    updated_at,
    platform_operator
FROM commodore.users
WHERE id = $1 AND tenant_id = $2;

-- name: ListUserWallets :many
SELECT id, wallet_address, created_at, last_auth_at
FROM commodore.wallet_identities
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: DeleteRefreshTokensForUser :exec
DELETE FROM commodore.refresh_tokens
WHERE user_id = $1 AND tenant_id = $2;

-- name: LockRefreshTokenByHash :one
SELECT
    id,
    user_id,
    tenant_id,
    COALESCE(revoked, false)::boolean AS revoked,
    rotated_at,
    replaced_by
FROM commodore.refresh_tokens
WHERE token_hash = $1 AND expires_at > NOW()
FOR UPDATE;

-- name: GetRefreshTokenSuccessorState :one
SELECT COALESCE(revoked, false)::boolean AS revoked
FROM commodore.refresh_tokens
WHERE id = $1;

-- name: RevokeRefreshTokensForUser :exec
UPDATE commodore.refresh_tokens
SET revoked = true
WHERE user_id = $1 AND tenant_id = $2;

-- name: GetRefreshUser :one
SELECT
    COALESCE(email::text, '')::text AS email,
    COALESCE(role, 'member')::text AS role,
    COALESCE(permissions, ARRAY[]::text[])::text[] AS permissions,
    first_name,
    last_name,
    COALESCE(is_active, false)::boolean AS is_active,
    COALESCE(verified, false)::boolean AS verified,
    created_at,
    updated_at,
    platform_operator
FROM commodore.users
WHERE id = $1 AND tenant_id = $2;

-- name: InsertRotatedRefreshToken :one
INSERT INTO commodore.refresh_tokens (tenant_id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: RotateRefreshToken :exec
UPDATE commodore.refresh_tokens
SET revoked = true, rotated_at = NOW(), replaced_by = $2
WHERE id = $1;

-- name: RevokeRefreshTokenByID :exec
UPDATE commodore.refresh_tokens
SET revoked = true
WHERE id = $1;

-- name: RelinkRefreshToken :exec
UPDATE commodore.refresh_tokens
SET replaced_by = $2
WHERE id = $1;
