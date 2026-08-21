-- name: GetPlatformACMEAccount :one
SELECT id, tenant_id, email, registration_json, private_key_pem, created_at, ca
FROM navigator.acme_accounts
WHERE tenant_id IS NULL AND email = $1 AND ca = $2;

-- name: GetTenantACMEAccount :one
SELECT id, tenant_id, email, registration_json, private_key_pem, created_at, ca
FROM navigator.acme_accounts
WHERE tenant_id = $1 AND email = $2 AND ca = $3;

-- name: SavePlatformACMEAccount :one
INSERT INTO navigator.acme_accounts (tenant_id, email, registration_json, private_key_pem, ca)
VALUES (NULL, $1, $2, $3, $4)
ON CONFLICT (email, ca) WHERE tenant_id IS NULL DO UPDATE SET
    registration_json = EXCLUDED.registration_json,
    private_key_pem = EXCLUDED.private_key_pem
RETURNING id, tenant_id, created_at;

-- name: SaveTenantACMEAccount :one
INSERT INTO navigator.acme_accounts (tenant_id, email, registration_json, private_key_pem, ca)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id, email, ca) DO UPDATE SET
    registration_json = EXCLUDED.registration_json,
    private_key_pem = EXCLUDED.private_key_pem
RETURNING id, tenant_id, created_at;
