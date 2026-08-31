-- name: GetPlatformCertificate :one
SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
FROM navigator.certificates
WHERE tenant_id IS NULL AND domain = $1;

-- name: GetTenantCertificate :one
SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
FROM navigator.certificates
WHERE tenant_id = $1 AND domain = $2;

-- name: SavePlatformCertificate :one
INSERT INTO navigator.certificates (tenant_id, domain, cert_pem, key_pem, expires_at, updated_at, issuer_ca)
VALUES (NULL, $1, $2, $3, $4, NOW(), $5)
ON CONFLICT (domain) WHERE tenant_id IS NULL DO UPDATE SET
    cert_pem = EXCLUDED.cert_pem,
    key_pem = EXCLUDED.key_pem,
    expires_at = EXCLUDED.expires_at,
    updated_at = NOW(),
    issuer_ca = EXCLUDED.issuer_ca
RETURNING id, tenant_id, created_at;

-- name: SaveTenantCertificate :one
WITH custom_domain_authority AS MATERIALIZED (
    SELECT custom_domain.tenant_id
    FROM navigator.tenant_custom_domains AS custom_domain
    WHERE custom_domain.tenant_id = sqlc.arg(tenant_id)::uuid
      AND custom_domain.domain = sqlc.arg(domain)
      AND custom_domain.status IN ('verified', 'cert_issuing', 'cert_issued', 'cert_failed')
    FOR UPDATE OF custom_domain
)
INSERT INTO navigator.certificates (tenant_id, domain, cert_pem, key_pem, expires_at, updated_at, issuer_ca)
SELECT custom_domain_authority.tenant_id, sqlc.arg(domain), sqlc.arg(cert_pem), sqlc.arg(key_pem),
       sqlc.arg(expires_at), NOW(), sqlc.arg(issuer_ca)
FROM custom_domain_authority
ON CONFLICT (tenant_id, domain) DO UPDATE SET
    cert_pem = EXCLUDED.cert_pem,
    key_pem = EXCLUDED.key_pem,
    expires_at = EXCLUDED.expires_at,
    updated_at = NOW(),
    issuer_ca = EXCLUDED.issuer_ca
RETURNING id, tenant_id, created_at;

-- name: DeletePlatformCertificate :exec
DELETE FROM navigator.certificates
WHERE tenant_id IS NULL AND domain = $1;

-- name: DeleteTenantCertificate :exec
DELETE FROM navigator.certificates
WHERE tenant_id = $1 AND domain = $2;

-- name: ListExpiringCertificates :many
SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
FROM navigator.certificates
WHERE expires_at < $1
ORDER BY expires_at ASC;

-- name: ListPlatformCertificates :many
SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
FROM navigator.certificates
WHERE tenant_id IS NULL
ORDER BY domain;

-- name: ListTenantCertificates :many
SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
FROM navigator.certificates
WHERE tenant_id = $1
ORDER BY domain;
