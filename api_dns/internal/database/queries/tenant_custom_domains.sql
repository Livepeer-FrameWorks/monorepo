-- name: EnsureTenantCustomDomain :one
INSERT INTO navigator.tenant_custom_domains
    (tenant_id, domain, status, acme_dns_subdomain, created_at, updated_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(domain), 'pending_verification', sqlc.arg(acme_dns_subdomain), NOW(), NOW())
ON CONFLICT (tenant_id, domain) DO UPDATE SET
    updated_at = NOW()
RETURNING tenant_id, domain, status, acme_dns_subdomain, issuer_id,
          last_verified_at, cert_issued_at, cert_expires_at, last_error,
          created_at, updated_at;

-- name: GetTenantCustomDomain :one
SELECT tenant_id, domain, status, acme_dns_subdomain, issuer_id,
       last_verified_at, cert_issued_at, cert_expires_at, last_error,
       created_at, updated_at
FROM navigator.tenant_custom_domains
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND domain = sqlc.arg(domain);

-- name: ListTenantCustomDomainsByStatus :many
SELECT tenant_id, domain, status, acme_dns_subdomain, issuer_id,
       last_verified_at, cert_issued_at, cert_expires_at, last_error,
       created_at, updated_at
FROM navigator.tenant_custom_domains
WHERE status = ANY(sqlc.arg(statuses)::text[])
ORDER BY updated_at ASC;

-- name: ListTenantCustomDomains :many
SELECT tenant_id, domain, status, acme_dns_subdomain, issuer_id,
       last_verified_at, cert_issued_at, cert_expires_at, last_error,
       created_at, updated_at
FROM navigator.tenant_custom_domains
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY domain ASC;

-- name: SetTenantCustomDomainStatus :execrows
UPDATE navigator.tenant_custom_domains
SET status = sqlc.arg(status),
    last_verified_at = CASE WHEN sqlc.arg(status) = 'verified' THEN NOW() ELSE last_verified_at END,
    cert_issued_at = CASE WHEN sqlc.arg(status) = 'cert_issued' THEN NOW() ELSE cert_issued_at END,
    last_error = NULLIF(sqlc.arg(err_msg)::text, ''),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND domain = sqlc.arg(domain);

-- name: SetTenantCustomDomainCertMetadata :execrows
UPDATE navigator.tenant_custom_domains
SET issuer_id = NULLIF(sqlc.arg(issuer_id)::text, ''),
    cert_expires_at = sqlc.narg(cert_expires_at),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND domain = sqlc.arg(domain);

-- name: DeleteTenantCustomDomain :exec
DELETE FROM navigator.tenant_custom_domains
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND domain = sqlc.arg(domain);
