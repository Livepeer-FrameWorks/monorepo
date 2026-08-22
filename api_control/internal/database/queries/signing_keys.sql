-- name: LockSigningKeyTenant :exec
SELECT pg_advisory_xact_lock(hashtext('commodore_signing_keys'), hashtext(sqlc.arg(tenant_id)::text));

-- name: CountActiveSigningKeys :one
SELECT COUNT(*)
FROM commodore.signing_keys
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = 'active';

-- name: CreateSigningKey :one
INSERT INTO commodore.signing_keys
    (tenant_id, kid, name, public_key_pem, algorithm, status)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(kid), sqlc.arg(name),
        sqlc.arg(public_key_pem), 'ES256', 'active')
RETURNING id::text AS id, created_at;

-- name: GetSigningKey :one
SELECT id::text AS id, kid, name, algorithm, public_key_pem, status,
       created_at, last_used_at, revoked_at
FROM commodore.signing_keys
WHERE id = sqlc.arg(id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetSigningKeyCursor :one
SELECT created_at
FROM commodore.signing_keys
WHERE id = sqlc.arg(id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ListSigningKeys :many
SELECT id::text AS id, kid, name, algorithm, public_key_pem, status,
       created_at, last_used_at, revoked_at
FROM commodore.signing_keys
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListSigningKeysByStatus :many
SELECT id::text AS id, kid, name, algorithm, public_key_pem, status,
       created_at, last_used_at, revoked_at
FROM commodore.signing_keys
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = sqlc.arg(status)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListSigningKeysAfter :many
SELECT id::text AS id, kid, name, algorithm, public_key_pem, status,
       created_at, last_used_at, revoked_at
FROM commodore.signing_keys
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND (created_at, id) < (sqlc.arg(after_created_at)::timestamp, sqlc.arg(after_id)::uuid)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListSigningKeysByStatusAfter :many
SELECT id::text AS id, kid, name, algorithm, public_key_pem, status,
       created_at, last_used_at, revoked_at
FROM commodore.signing_keys
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = sqlc.arg(status)
  AND (created_at, id) < (sqlc.arg(after_created_at)::timestamp, sqlc.arg(after_id)::uuid)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: RevokeSigningKey :one
UPDATE commodore.signing_keys
SET status = 'revoked', revoked_at = NOW()
WHERE id = sqlc.arg(id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = 'active'
RETURNING id::text AS id, kid, name, algorithm, public_key_pem, status,
          created_at, last_used_at, revoked_at;

-- name: RecordSigningKeyUse :exec
UPDATE commodore.signing_keys
SET last_used_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND kid = sqlc.arg(kid)
  AND status = 'active';

-- name: ListActivePlaybackSigningKeys :many
SELECT kid, algorithm, public_key_pem
FROM commodore.signing_keys
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = 'active';

-- name: InsertSigningKeyAudit :exec
INSERT INTO commodore.signing_key_audit
    (tenant_id, kid, action, actor_user_id, actor_ip, detail)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(kid), sqlc.arg(action),
        sqlc.narg(actor_user_id)::uuid, sqlc.narg(actor_ip)::text, sqlc.narg(detail)::text);
