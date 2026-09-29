-- name: AdminGetUserByEmail :one
SELECT
    id,
    tenant_id,
    COALESCE(email::text, '')::text AS email,
    COALESCE(role, 'member')::text AS role,
    COALESCE(is_active, false)::boolean AS is_active,
    COALESCE(verified, false)::boolean AS verified
FROM commodore.users
WHERE lower(email::text) = lower(sqlc.arg(email)::text);
