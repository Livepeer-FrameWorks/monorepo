-- name: GetBootstrapUser :one
SELECT id::text AS id,
       COALESCE(first_name, '') AS first_name,
       COALESCE(last_name, '') AS last_name,
       role,
       COALESCE(permissions, '{}') AS permissions,
       platform_operator
FROM commodore.users
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND email = sqlc.arg(email)::citext;

-- name: CreateBootstrapUser :exec
INSERT INTO commodore.users
    (id, tenant_id, email, password_hash, first_name, last_name, role, permissions,
     platform_operator, is_active, verified, created_at, updated_at)
VALUES (sqlc.arg(id)::uuid, sqlc.arg(tenant_id)::uuid, sqlc.arg(email)::citext,
        sqlc.arg(password_hash)::text, sqlc.arg(first_name)::text,
        sqlc.arg(last_name)::text, sqlc.arg(role), sqlc.arg(permissions)::text[],
        sqlc.arg(platform_operator), true, true, NOW(), NOW());

-- name: UpdateBootstrapUserWithCredentials :exec
UPDATE commodore.users
SET first_name = sqlc.arg(first_name)::text,
    last_name = sqlc.arg(last_name)::text,
    role = sqlc.arg(role),
    permissions = sqlc.arg(permissions)::text[],
    password_hash = sqlc.arg(password_hash)::text,
    platform_operator = sqlc.arg(platform_operator),
    updated_at = NOW()
WHERE id = sqlc.arg(id)::uuid AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: UpdateBootstrapUserProfile :exec
UPDATE commodore.users
SET first_name = sqlc.arg(first_name)::text,
    last_name = sqlc.arg(last_name)::text,
    role = sqlc.arg(role),
    permissions = sqlc.arg(permissions)::text[],
    platform_operator = sqlc.arg(platform_operator),
    updated_at = NOW()
WHERE id = sqlc.arg(id)::uuid AND tenant_id = sqlc.arg(tenant_id)::uuid;
