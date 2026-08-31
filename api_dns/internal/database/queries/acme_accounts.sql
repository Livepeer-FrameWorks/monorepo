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
WITH custom_domain_authority AS MATERIALIZED (
    SELECT custom_domain.tenant_id
    FROM navigator.tenant_custom_domains AS custom_domain
    WHERE custom_domain.tenant_id = sqlc.arg(tenant_id)::uuid
      AND custom_domain.status IN ('verified', 'cert_issuing', 'cert_issued', 'cert_failed')
    ORDER BY custom_domain.domain
    LIMIT 1
    FOR UPDATE OF custom_domain
)
INSERT INTO navigator.acme_accounts (tenant_id, email, registration_json, private_key_pem, ca)
SELECT custom_domain_authority.tenant_id, sqlc.arg(email), sqlc.arg(registration_json),
       sqlc.arg(private_key_pem), sqlc.arg(ca)
FROM custom_domain_authority
ON CONFLICT (tenant_id, email, ca) DO UPDATE SET
    registration_json = EXCLUDED.registration_json,
    private_key_pem = EXCLUDED.private_key_pem
RETURNING id, tenant_id, created_at;
