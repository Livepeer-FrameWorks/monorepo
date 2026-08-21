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
INSERT INTO navigator.certificates (tenant_id, domain, cert_pem, key_pem, expires_at, updated_at, issuer_ca)
VALUES ($1, $2, $3, $4, $5, NOW(), $6)
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
