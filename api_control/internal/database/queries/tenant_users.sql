-- name: GetTenantUserCounts :one
SELECT COUNT(*) FILTER (WHERE is_active = true)::integer AS active_count,
       COUNT(*)::integer AS total_count
FROM commodore.users
WHERE tenant_id = $1;

-- name: GetTenantPrimaryUser :one
SELECT id, COALESCE(email::text, '')::text AS email, first_name, last_name,
       EXISTS (SELECT 1 FROM commodore.users eligible
               WHERE eligible.tenant_id = $1 AND eligible.is_active = true
                 AND eligible.verified = true AND eligible.email IS NOT NULL AND eligible.email <> ''
                 AND eligible.role IN ('owner', 'admin'))::boolean AS free_tier_eligible
FROM commodore.users
WHERE tenant_id = $1 AND is_active = true AND email IS NOT NULL AND email <> ''
ORDER BY CASE role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END,
         created_at ASC
LIMIT 1;

-- name: InsertVerifiedTenantUser :exec
INSERT INTO commodore.users (
    id, tenant_id, email, password_hash, first_name, last_name,
    role, permissions, is_active, verified, created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true, true, $9, $9);
